package backup

import (
	"bytes"
	"compress/gzip"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"voidxo/internal/kv"
	"voidxo/internal/objstore"
	"voidxo/internal/snapshot"
	"voidxo/internal/store"
)

type memSettings struct {
	mu sync.Mutex
	m  map[string]string
}

func newMemSettings() *memSettings { return &memSettings{m: map[string]string{}} }

func (s *memSettings) Setting(_ context.Context, key, def string) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if v, ok := s.m[key]; ok {
		return v, nil
	}
	return def, nil
}

func (s *memSettings) SetSetting(_ context.Context, key, value string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.m[key] = value
	return nil
}

// fakeS3 存对象的最小 S3：PUT 存、GET 支持 Range、HEAD 判存在。
type fakeS3 struct {
	mu      sync.Mutex
	objects map[string][]byte
	puts    int
}

func newFakeS3() (*fakeS3, *httptest.Server) {
	f := &fakeS3{objects: map[string][]byte{}}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		key := strings.TrimPrefix(r.URL.Path, "/bucket/")
		f.mu.Lock()
		defer f.mu.Unlock()
		switch r.Method {
		case http.MethodPut:
			body, _ := io.ReadAll(r.Body)
			f.objects[key] = body
			f.puts++
			w.WriteHeader(http.StatusOK)
		case http.MethodHead:
			if _, ok := f.objects[key]; !ok {
				w.WriteHeader(http.StatusNotFound)
				return
			}
			w.WriteHeader(http.StatusOK)
		case http.MethodGet:
			body, ok := f.objects[key]
			if !ok {
				w.WriteHeader(http.StatusNotFound)
				return
			}
			if rng := r.Header.Get("Range"); rng != "" {
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
		default:
			w.WriteHeader(http.StatusMethodNotAllowed)
		}
	}))
	return f, srv
}

func setup(t *testing.T, partBytes int64) (*Runner, *fakeS3, []string, map[string][]byte) {
	t.Helper()
	dir := t.TempDir()
	k, err := kv.Open(filepath.Join(dir, "kv"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { k.Close() })
	pages := map[string][]byte{
		"page:aaa": bytes.Repeat([]byte("甲"), 200),
		"page:bbb": bytes.Repeat([]byte("乙"), 200),
		"page:ccc": bytes.Repeat([]byte("丙"), 200),
	}
	keys := make([]string, 0, len(pages))
	for key, val := range pages {
		if err := k.Put(key, val); err != nil {
			t.Fatal(err)
		}
		keys = append(keys, key)
	}
	f, srv := newFakeS3()
	t.Cleanup(srv.Close)
	cfg := objstore.Config{
		Endpoint: srv.URL, Bucket: "bucket", Region: "us-test-1",
		AccessKey: "AK", SecretKey: "SK", Prefix: "snapshots", PathStyle: true,
	}
	r := &Runner{
		Snaps: snapshot.NewLocal(k), Client: objstore.New(cfg, 5*time.Second),
		Settings: newMemSettings(), PartBytes: partBytes, TempDir: dir,
	}
	return r, f, keys, pages
}

func TestPackUploadsRangesAndIsIdempotent(t *testing.T) {
	// 上限 300 字节，三页必然被切到多个包里——顺便验证跨包读取。
	r, f, keys, pages := setup(t, 300)
	ctx := context.Background()

	res, err := r.Pack(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if res.Keys != 3 {
		t.Fatalf("归档页数=%d want 3", res.Keys)
	}
	if res.Parts < 2 {
		t.Fatalf("包数=%d，上限 300 字节应该切包", res.Parts)
	}

	// 每一页都要能按 manifest 的范围读回来。
	for _, key := range keys {
		got, err := r.Fetch(ctx, key)
		if err != nil {
			t.Fatalf("取回 %s 失败：%v", key, err)
		}
		if !bytes.Equal(got, pages[key]) {
			t.Fatalf("取回内容与原文不一致：%s", key)
		}
	}

	// 再跑一轮：全部跳过，不产生新包。
	before := f.puts
	res2, err := r.Pack(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if res2.Keys != 0 || res2.Skipped != 3 {
		t.Fatalf("第二轮应全部跳过：%+v", res2)
	}
	if f.puts != before {
		t.Fatalf("第二轮不该上传任何东西，PUT 次数 %d → %d", before, f.puts)
	}

	st := ReadStatus(ctx, r.Settings)
	if st.Keys != 3 || st.Parts < 2 || st.LastRun == "" || st.LastError != "" {
		t.Fatalf("状态没落库：%+v", st)
	}
}

func TestFetchMissingReportsNotArchived(t *testing.T) {
	r, _, _, _ := setup(t, 1<<20)
	if _, err := r.Fetch(context.Background(), "page:nope"); err != ErrNotArchived {
		t.Fatalf("未归档应返回 ErrNotArchived，实际 %v", err)
	}
}

func TestExportWritesJSONL(t *testing.T) {
	r, f, _, _ := setup(t, 1<<20)
	docs := []store.ExportDoc{
		{URL: "https://a.example/1", Domain: "a.example", Title: "标题一", Labels: []string{"ugc_platform"}},
		{URL: "https://b.example/2", Domain: "b.example", Title: "标题二", Labels: []string{}},
	}
	src := func(_ context.Context, fn func(store.ExportDoc) error) (int, error) {
		for _, d := range docs {
			if err := fn(d); err != nil {
				return 0, err
			}
		}
		return len(docs), nil
	}
	res, err := r.Export(context.Background(), src)
	if err != nil {
		t.Fatal(err)
	}
	if res.Docs != 2 || res.Bytes == 0 {
		t.Fatalf("导出结果不对：%+v", res)
	}
	if !strings.HasPrefix(res.Key, "snapshots/exports/") {
		t.Fatalf("导出对象名=%q", res.Key)
	}
	f.mu.Lock()
	raw := f.objects[res.Key]
	f.mu.Unlock()
	zr, err := gzip.NewReader(bytes.NewReader(raw))
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(zr)
	lines := strings.Split(strings.TrimSpace(string(body)), "\n")
	if len(lines) != 2 {
		t.Fatalf("JSONL 行数=%d want 2：%s", len(lines), body)
	}
	var got store.ExportDoc
	if err := json.Unmarshal([]byte(lines[0]), &got); err != nil {
		t.Fatal(err)
	}
	if got.URL != docs[0].URL || got.Title != "标题一" || len(got.Labels) != 1 {
		t.Fatalf("导出行内容不对：%+v", got)
	}
	if st := ReadStatus(context.Background(), r.Settings); st.ExportDocs != 2 || st.ExportedAt == "" {
		t.Fatalf("导出状态没落库：%+v", st)
	}
}

func TestPackRequiresConfig(t *testing.T) {
	r, _, _, _ := setup(t, 1<<20)
	r.Client = objstore.New(objstore.Config{}, time.Second)
	if _, err := r.Pack(context.Background()); err == nil {
		t.Fatal("没有配置时应该报错")
	}
}
