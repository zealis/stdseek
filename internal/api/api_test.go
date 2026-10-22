package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	"voidxo/internal/index"
	"voidxo/internal/store"
)

func newTestServer(t *testing.T) http.Handler {
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
	return NewRouter(NewServer(st, idx, false))
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
	h := newTestServer(t)
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
	h := newTestServer(t)
	out := doSearch(t, h, "/api/search?q=error&no=ai")
	if out.Total != 1 || len(out.Hits) != 1 {
		t.Fatalf("no=ai should exclude ai_suspect doc, got total=%d hits=%d", out.Total, len(out.Hits))
	}
	if out.Hits[0].Domain != "go.dev" {
		t.Fatalf("unexpected remaining hit: %s", out.Hits[0].Domain)
	}
}

func TestSubmitStaysClosedByDefault(t *testing.T) {
	h := newTestServer(t)
	req := httptest.NewRequest(http.MethodPost, "/api/submit", nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("submit should be 403 by default, got %d", rec.Code)
	}
}
