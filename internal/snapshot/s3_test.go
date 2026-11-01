package snapshot

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestS3Validate(t *testing.T) {
	cases := []S3Config{
		{},
		{Endpoint: "not-a-url", Bucket: "b", Region: "r", AccessKey: "a", SecretKey: "s"},
		{Endpoint: "https://s3.example.com", Region: "r", AccessKey: "a", SecretKey: "s"},
		{Endpoint: "https://s3.example.com", Bucket: "b", AccessKey: "a", SecretKey: "s"},
		{Endpoint: "https://s3.example.com", Bucket: "b", Region: "r", SecretKey: "s"},
	}
	for i, c := range cases {
		if err := c.Validate(); err == nil {
			t.Fatalf("case %d 应校验失败：%+v", i, c)
		}
	}
	ok := S3Config{Endpoint: "https://s3.example.com", Bucket: "b", Region: "r", AccessKey: "a", SecretKey: "s"}
	if err := ok.Validate(); err != nil {
		t.Fatalf("完整配置不应报错：%v", err)
	}
}

func TestTestS3SignsHeadBucket(t *testing.T) {
	var gotPath, gotAuth, gotDate string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotAuth = r.Header.Get("Authorization")
		gotDate = r.Header.Get("x-amz-date")
		if r.Method != http.MethodHead {
			t.Errorf("method=%s want HEAD", r.Method)
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	err := TestS3(context.Background(), S3Config{
		Endpoint: srv.URL, Bucket: "bucket", Region: "us-test-1",
		AccessKey: "AK", SecretKey: "SK", PathStyle: true,
	}, 5*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if gotPath != "/bucket" {
		t.Fatalf("path=%q want /bucket", gotPath)
	}
	if !strings.HasPrefix(gotAuth, "AWS4-HMAC-SHA256 Credential=AK/") ||
		!strings.Contains(gotAuth, "/us-test-1/s3/aws4_request") {
		t.Fatalf("Authorization 不符合 SigV4：%s", gotAuth)
	}
	if gotDate == "" {
		t.Fatal("缺少 x-amz-date")
	}
}

func TestTestS3ReportsAuthFailure(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusForbidden)
	}))
	defer srv.Close()

	err := TestS3(context.Background(), S3Config{
		Endpoint: srv.URL, Bucket: "bucket", Region: "r",
		AccessKey: "AK", SecretKey: "SK", PathStyle: true,
	}, 5*time.Second)
	if err == nil || !strings.Contains(err.Error(), "403") {
		t.Fatalf("应返回 403 错误，实际 %v", err)
	}
}

// B2 的桶级应用密钥没有列桶权限：HeadBucket 会 403，但桶内读写是通的。
// 只验 HeadBucket 会让人以为配置写错了，实际只是权限范围不同。
func TestTestS3FallsBackToObjectProbe(t *testing.T) {
	var probed string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/bucket" {
			w.WriteHeader(http.StatusForbidden)
			return
		}
		probed = r.URL.Path
		w.WriteHeader(http.StatusNotFound) // 对象不存在 = 凭据有效
	}))
	defer srv.Close()

	err := TestS3(context.Background(), S3Config{
		Endpoint: srv.URL, Bucket: "bucket", Region: "us-test-1",
		AccessKey: "AK", SecretKey: "SK", Prefix: "stdseek", PathStyle: true,
	}, 5*time.Second)
	if err != nil {
		t.Fatalf("桶级密钥应通过对象探测兜底：%v", err)
	}
	if probed != "/bucket/stdseek/.probe" {
		t.Fatalf("探测路径=%q", probed)
	}
}

// 两次都 403 才算真的认证失败。
func TestTestS3ReportsAuthFailureWhenProbeAlsoFails(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusForbidden)
	}))
	defer srv.Close()
	err := TestS3(context.Background(), S3Config{
		Endpoint: srv.URL, Bucket: "bucket", Region: "r",
		AccessKey: "AK", SecretKey: "SK", PathStyle: true,
	}, 5*time.Second)
	if err == nil || !strings.Contains(err.Error(), "403") {
		t.Fatalf("应返回 403，实际 %v", err)
	}
}
