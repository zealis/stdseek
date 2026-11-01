package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// 前缀是桶内的命名空间，不是仓库地址：只能有一段、不含点。
// 这条断言拦的是「机械改名把模块路径塞进默认值」——v0.80.0 真发生过一次。
func TestDefaultS3PrefixIsSingleSegment(t *testing.T) {
	if !strings.HasSuffix(defaultS3Prefix, "/") {
		t.Fatalf("默认前缀应以 / 结尾，got %q", defaultS3Prefix)
	}
	seg := strings.TrimSuffix(defaultS3Prefix, "/")
	if seg == "" || strings.ContainsAny(seg, "/.") {
		t.Fatalf("默认前缀应只有一段且不含点，got %q", defaultS3Prefix)
	}
}

func TestAdminSnapshotStorageConfigAndTest(t *testing.T) {
	e := newTestEnv(t, testOpts{token: "tok"})

	getStats := func() snapshotStatsView {
		req := httptest.NewRequest(http.MethodGet, "/admin/snapshot", nil)
		req.Header.Set("X-Admin-Token", "tok")
		rec := httptest.NewRecorder()
		e.h.ServeHTTP(rec, req)
		if rec.Code != http.StatusOK {
			t.Fatalf("stats status=%d body=%s", rec.Code, rec.Body.String())
		}
		var out snapshotStatsView
		if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
			t.Fatal(err)
		}
		return out
	}

	s := getStats()
	if s.Storage.Backend != "local" || s.Storage.S3Configured {
		t.Fatalf("初始应为本地且未配置，got %+v", s.Storage)
	}

	s3 := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodHead {
			t.Errorf("method=%s want HEAD", r.Method)
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer s3.Close()

	body := `{"endpoint":"` + s3.URL + `","bucket":"b","region":"us-test-1",` +
		`"prefix":"snapshots/","access_key":"AK","secret_key":"SK","path_style":true}`
	req := httptest.NewRequest(http.MethodPost, "/admin/snapshot/storage", strings.NewReader(body))
	req.Header.Set("X-Admin-Token", "tok")
	rec := httptest.NewRecorder()
	e.h.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("save status=%d body=%s", rec.Code, rec.Body.String())
	}
	if strings.Contains(rec.Body.String(), "AK") || strings.Contains(rec.Body.String(), "SK") {
		t.Fatalf("保存响应不应回显凭据：%s", rec.Body.String())
	}
	s = getStats()
	if !s.Storage.S3Configured || !s.Storage.HasAccessKey || !s.Storage.HasSecret || !s.Storage.PathStyle {
		t.Fatalf("配置未正确保存：%+v", s.Storage)
	}
	if s.Storage.Endpoint != s3.URL || s.Storage.Bucket != "b" || s.Storage.Prefix != "snapshots/" {
		t.Fatalf("配置回读不对：%+v", s.Storage)
	}

	// 密钥留空 = 用已保存值测试。
	testBody := `{"endpoint":"` + s3.URL + `","bucket":"b","region":"us-test-1","prefix":"snapshots/","path_style":true}`
	req = httptest.NewRequest(http.MethodPost, "/admin/snapshot/storage/test", strings.NewReader(testBody))
	req.Header.Set("X-Admin-Token", "tok")
	rec = httptest.NewRecorder()
	e.h.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("test status=%d body=%s", rec.Code, rec.Body.String())
	}
	var tr struct {
		OK      bool   `json:"ok"`
		Message string `json:"message"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &tr); err != nil {
		t.Fatal(err)
	}
	if !tr.OK || tr.Message == "" {
		t.Fatalf("测试连接应成功：%+v", tr)
	}
}

func TestAdminSnapshotStorageRejectsBadEndpoint(t *testing.T) {
	e := newTestEnv(t, testOpts{token: "tok"})
	req := httptest.NewRequest(http.MethodPost, "/admin/snapshot/storage",
		strings.NewReader(`{"endpoint":"not-a-url","bucket":"b","region":"r"}`))
	req.Header.Set("X-Admin-Token", "tok")
	rec := httptest.NewRecorder()
	e.h.ServeHTTP(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("非法 endpoint 应 400，got %d body=%s", rec.Code, rec.Body.String())
	}
}
