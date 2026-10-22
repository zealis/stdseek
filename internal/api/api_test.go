package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"testing"
	"time"

	"voidxo/internal/index"
	"voidxo/internal/kv"
	"voidxo/internal/store"
	"voidxo/internal/urlkey"
)

func newTestServer(t *testing.T) (http.Handler, *kv.Store) {
	t.Helper()
	dir := t.TempDir()
	st, err := store.Open(filepath.Join(dir, "t.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	idx, err := index.Open(filepath.Join(dir, "bleve"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { idx.Close() })
	snap, err := kv.Open(filepath.Join(dir, "kv"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { snap.Close() })

	ctx := context.Background()
	if err := st.AddWhitelist(ctx, "go.dev", "official", 86400); err != nil {
		t.Fatal(err)
	}
	if err := st.AddWhitelist(ctx, "blog.example.com", "blog", 86400); err != nil {
		t.Fatal(err)
	}

	now := time.Now().Unix()
	pub := now - 3600
	docs := []index.Doc{
		// 两个文档正文完全一致，唯一差异是域名分类与标签，便于验证质量分排序
		{URL: "https://go.dev/doc/error", Domain: "go.dev", Title: "error handling guide",
			Description: "官方错误处理指南", Body: "error handling error wrapping",
			Labels: []string{}, PublishedAt: &pub, FetchedAt: now},
		{URL: "https://blog.example.com/error", Domain: "blog.example.com", Title: "error handling guide",
			Description: "官方错误处理指南", Body: "error handling error wrapping", Labels: []string{"ai_suspect"},
			FetchedAt: now},
	}
	for _, d := range docs {
		if err := idx.IndexDocument(d); err != nil {
			t.Fatal(err)
		}
	}
	return NewRouter(NewServer(st, idx, snap, false)), snap
}

type searchResp struct {
	Total int `json:"total"`
	Hits  []struct {
		URL       string   `json:"url"`
		Domain    string   `json:"domain"`
		Reasons   []string `json:"reasons"`
		Score     float64  `json:"score"`
		Breakdown struct {
			Exact     float64 `json:"exact"`
			Quality   float64 `json:"quality"`
			Diversity float64 `json:"diversity"`
		} `json:"breakdown"`
	} `json:"hits"`
}

func doSearch(t *testing.T, h http.Handler, target string) searchResp {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, target, nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d: %s", rec.Code, rec.Body.String())
	}
	var out searchResp
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatalf("decode: %v body=%s", err, rec.Body.String())
	}
	return out
}

func TestSearchReturnsRankedResultsWithReasons(t *testing.T) {
	h, _ := newTestServer(t)
	out := doSearch(t, h, "/api/search?q=error")
	if out.Total < 2 || len(out.Hits) < 2 {
		t.Fatalf("want 2 hits, got total=%d len=%d", out.Total, len(out.Hits))
	}
	if out.Hits[0].Domain != "go.dev" {
		t.Fatalf("official docs should rank first, got %s\n%+v", out.Hits[0].Domain, out.Hits)
	}
	if len(out.Hits[0].Reasons) == 0 {
		t.Fatal("ranked results must carry explanations")
	}
	if out.Hits[0].Score <= 0 {
		t.Fatalf("score must be positive: %v", out.Hits[0].Score)
	}
}

func TestSearchExcludesAILabel(t *testing.T) {
	h, _ := newTestServer(t)
	out := doSearch(t, h, "/api/search?q=error&no=ai")
	if out.Total != 1 || len(out.Hits) != 1 {
		t.Fatalf("no=ai should exclude ai_suspect doc, got total=%d hits=%d", out.Total, len(out.Hits))
	}
	if out.Hits[0].Domain != "go.dev" {
		t.Fatalf("unexpected remaining hit: %s", out.Hits[0].Domain)
	}
}

func TestPageSnapshotEndpoint(t *testing.T) {
	h, snap := newTestServer(t)
	// 直接向 KV 写入一份快照（实际由爬虫写入）
	if err := snap.Put(kv.Key(urlkey.Key("https://go.dev/doc/error")), []byte("<html>snapshot</html>")); err != nil {
		t.Fatalf("seed snapshot: %v", err)
	}

	req := httptest.NewRequest(http.MethodGet, "/api/page?url="+url.QueryEscape("https://go.dev/doc/error"), nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	var out struct {
		Key  string `json:"key"`
		HTML string `json:"html"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	if out.Key != urlkey.Key("https://go.dev/doc/error") || out.HTML != "<html>snapshot</html>" {
		t.Fatalf("unexpected snapshot: %+v", out)
	}

	// 不存在的页面 → 404
	req = httptest.NewRequest(http.MethodGet, "/api/page?url=https://nope.example/x", nil)
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("missing snapshot should 404, got %d", rec.Code)
	}
}

func TestSubmitStaysClosedByDefault(t *testing.T) {
	h, _ := newTestServer(t)
	req := httptest.NewRequest(http.MethodPost, "/api/submit", nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("submit should be 403 by default, got %d", rec.Code)
	}
}
