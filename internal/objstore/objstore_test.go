package objstore

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

// fakeS3 一个够用的 S3 兼容服务：存对象、支持 Range 读。
// 用真的 HTTP 而不是打桩 Client：签名头、路径拼接、Range 语义
// 都是这一层最容易错的地方，绕过 HTTP 等于什么都没测。
type fakeS3 struct {
	mu      sync.Mutex
	objects map[string][]byte
	lastReq []*http.Request
}

func newFakeS3() (*fakeS3, *httptest.Server) {
	f := &fakeS3{objects: map[string][]byte{}}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		f.lastReq = append(f.lastReq, r)
		f.mu.Unlock()
		key := strings.TrimPrefix(r.URL.Path, "/bucket/")
		switch r.Method {
		case http.MethodPut:
			body, _ := io.ReadAll(r.Body)
			f.mu.Lock()
			f.objects[key] = body
			f.mu.Unlock()
			w.WriteHeader(http.StatusOK)
		case http.MethodHead:
			f.mu.Lock()
			body, ok := f.objects[key]
			f.mu.Unlock()
			if !ok {
				w.WriteHeader(http.StatusNotFound)
				return
			}
			// 真实的 S3 在 HEAD 上会带 Content-Length；不带的话客户端拿到 -1。
			w.Header().Set("Content-Length", strconv.Itoa(len(body)))
			w.WriteHeader(http.StatusOK)
		case http.MethodGet:
			f.mu.Lock()
			body, ok := f.objects[key]
			f.mu.Unlock()
			if !ok {
				w.WriteHeader(http.StatusNotFound)
				return
			}
			if rng := r.Header.Get("Range"); rng != "" {
				// 只支持单区间 bytes=a-b
				parts := strings.SplitN(strings.TrimPrefix(rng, "bytes="), "-", 2)
				start, _ := strconv.Atoi(parts[0])
				end, _ := strconv.Atoi(parts[1])
				if start < 0 || end >= len(body) || start > end {
					w.WriteHeader(http.StatusRequestedRangeNotSatisfiable)
					return
				}
				w.WriteHeader(http.StatusPartialContent)
				_, _ = w.Write(body[start : end+1])
				return
			}
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write(body)
		case http.MethodDelete:
			f.mu.Lock()
			delete(f.objects, key)
			f.mu.Unlock()
			w.WriteHeader(http.StatusNoContent)
		default:
			w.WriteHeader(http.StatusMethodNotAllowed)
		}
	}))
	return f, srv
}

func testConfig(srv *httptest.Server) Config {
	return Config{
		Endpoint: srv.URL, Bucket: "bucket", Region: "us-test-1",
		AccessKey: "AK", SecretKey: "SK", Prefix: "snapshots", PathStyle: true,
	}
}

func TestPutGetRangeAndHead(t *testing.T) {
	f, srv := newFakeS3()
	defer srv.Close()
	c := New(testConfig(srv), 5*time.Second)
	ctx := context.Background()

	payload := []byte("hello StdSeek world")
	key := c.Config().ObjectKey("parts/a.pack")
	if key != "snapshots/parts/a.pack" {
		t.Fatalf("对象名=%q", key)
	}
	if err := c.PutBytes(ctx, key, payload, "application/octet-stream"); err != nil {
		t.Fatal(err)
	}
	f.mu.Lock()
	stored := f.objects[key]
	f.mu.Unlock()
	if !bytes.Equal(stored, payload) {
		t.Fatalf("桶里内容不一致：%q", stored)
	}

	got, err := c.Get(ctx, key)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, payload) {
		t.Fatalf("Get 内容不一致：%q", got)
	}

	// Range 是冷备回源的唯一读取路径，必须精确到字节。
	part, err := c.GetRange(ctx, key, 6, 6)
	if err != nil {
		t.Fatal(err)
	}
	if string(part) != string(payload[6:12]) {
		t.Fatalf("Range 内容=%q want %q", part, payload[6:12])
	}

	size, err := c.Head(ctx, key)
	if err != nil {
		t.Fatal(err)
	}
	if size != int64(len(payload)) {
		t.Fatalf("Head=%d want %d", size, len(payload))
	}
	if err := c.Delete(ctx, key); err != nil {
		t.Fatal(err)
	}
	if _, err := c.Head(ctx, key); err == nil {
		t.Fatal("删除后 Head 应该返回错误")
	}
}

func TestSignsRequestsAndMapsErrors(t *testing.T) {
	f, srv := newFakeS3()
	defer srv.Close()
	c := New(testConfig(srv), 5*time.Second)
	ctx := context.Background()
	if err := c.PutBytes(ctx, "snapshots/x", []byte("1"), ""); err != nil {
		t.Fatal(err)
	}
	f.mu.Lock()
	req := f.lastReq[len(f.lastReq)-1]
	f.mu.Unlock()
	auth := req.Header.Get("Authorization")
	if !strings.HasPrefix(auth, "AWS4-HMAC-SHA256 Credential=AK/") ||
		!strings.Contains(auth, "/us-test-1/s3/aws4_request") {
		t.Fatalf("Authorization 不符合 SigV4：%s", auth)
	}
	if req.Header.Get("x-amz-content-sha256") == "" {
		t.Fatal("缺少 x-amz-content-sha256")
	}
	// 404 必须是 ErrNotFound：上层靠它区分「还没归档」与「真的出错」。
	if _, err := c.Get(ctx, "snapshots/missing"); err != ErrNotFound {
		t.Fatalf("404 应映射为 ErrNotFound，实际 %v", err)
	}
}

func TestHeadBucketMaps403(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusForbidden)
	}))
	defer srv.Close()
	err := New(testConfig(srv), 5*time.Second).HeadBucket(context.Background())
	if err == nil || !strings.Contains(err.Error(), "403") {
		t.Fatalf("应返回 403 错误，实际 %v", err)
	}
}

func TestValidateRejectsIncomplete(t *testing.T) {
	cases := []Config{
		{},
		{Endpoint: "not-a-url", Bucket: "b", Region: "r", AccessKey: "a", SecretKey: "s"},
		{Endpoint: "https://s3.example.com", Region: "r", AccessKey: "a", SecretKey: "s"},
		{Endpoint: "https://s3.example.com", Bucket: "b", AccessKey: "a", SecretKey: "s"},
	}
	for i, c := range cases {
		if err := c.Validate(); err == nil {
			t.Fatalf("case %d 应校验失败：%+v", i, c)
		}
	}
}
