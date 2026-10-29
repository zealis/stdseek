package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"voidxo/internal/index"
	"voidxo/internal/kv"
	"voidxo/internal/precheck"
	"voidxo/internal/snapshot"
	"voidxo/internal/store"
	"voidxo/internal/urlkey"
)

func newTestServer(t *testing.T) (http.Handler, *kv.Store) {
	e := newTestEnv(t, testOpts{})
	return e.h, e.snap
}

// newTestServerWithToken 构建带种子数据的测试服务；token 为空表示不校验后台。
func newTestServerWithToken(t *testing.T, token string) (http.Handler, *kv.Store) {
	e := newTestEnv(t, testOpts{token: token})
	return e.h, e.snap
}

// testOpts 测试服务选项：后台令牌、提交入口开关、可注入的预检实现。
type testOpts struct {
	token   string
	submit  bool
	checkFn func(ctx context.Context, rawURL string) precheck.Result
}

// testEnv 暴露 handler、Server 与底层存储，便于断言落库结果、替换依赖。
type testEnv struct {
	h    http.Handler
	srv  *Server
	st   *store.Store
	snap *kv.Store
}

func newTestEnv(t *testing.T, o testOpts) testEnv {
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
	srv := NewServer(st, idx, snapshot.NewLocal(snap), o.submit, o.token)
	srv.checkFn = o.checkFn
	return testEnv{h: NewRouter(srv), srv: srv, st: st, snap: snap}
}

func TestAdminRequiresToken(t *testing.T) {
	h, _ := newTestServerWithToken(t, "s3cret")

	req := httptest.NewRequest(http.MethodGet, "/admin/whitelist", nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("without token status=%d want 401", rec.Code)
	}

	req = httptest.NewRequest(http.MethodGet, "/admin/whitelist", nil)
	req.Header.Set("X-Admin-Token", "s3cret")
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("with token status=%d want 200 body=%s", rec.Code, rec.Body.String())
	}

	// 公开检索不受影响
	req = httptest.NewRequest(http.MethodGet, "/api/search?q=error", nil)
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("public search should stay open, got %d", rec.Code)
	}
}

func TestRejectPrivateOrInvalidDomains(t *testing.T) {
	h, _ := newTestServerWithToken(t, "tok")
	bad := []string{"127.0.0.1", "localhost", "10.0.0.1", "192.168.1.5", "foo.local", "bad_domain.com", "-bad.com", "nodot"}
	for _, d := range bad {
		body := `{"domain":"` + d + `"}`
		req := httptest.NewRequest(http.MethodPost, "/admin/whitelist", strings.NewReader(body))
		req.Header.Set("X-Admin-Token", "tok")
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		if rec.Code != http.StatusBadRequest {
			t.Fatalf("domain %q should be rejected, got %d", d, rec.Code)
		}
	}
	okBody := `{"domain":"go.dev","category":"official"}`
	req := httptest.NewRequest(http.MethodPost, "/admin/whitelist", strings.NewReader(okBody))
	req.Header.Set("X-Admin-Token", "tok")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("public domain should be accepted, got %d body=%s", rec.Code, rec.Body.String())
	}
}

func TestQueryTooLong(t *testing.T) {
	h, _ := newTestServer(t)
	long := strings.Repeat("a", 600)
	req := httptest.NewRequest(http.MethodGet, "/api/search?q="+long, nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("long query status=%d want 400", rec.Code)
	}
}

func TestPrecheckRejectsPrivateTargets(t *testing.T) {
	h, _ := newTestServerWithToken(t, "tok")
	for _, u := range []string{"http://127.0.0.1/", "http://localhost:8080/", "http://10.0.0.5/"} {
		body := `{"url":"` + u + `"}`
		req := httptest.NewRequest(http.MethodPost, "/admin/precheck", strings.NewReader(body))
		req.Header.Set("X-Admin-Token", "tok")
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		if rec.Code != http.StatusBadRequest {
			t.Fatalf("precheck target %q status=%d want 400", u, rec.Code)
		}
	}
}

func TestAdminBodyLimit(t *testing.T) {
	h, _ := newTestServerWithToken(t, "tok")
	big := `{"domain":"` + strings.Repeat("a", 2<<20) + `"}`
	req := httptest.NewRequest(http.MethodPost, "/admin/whitelist", strings.NewReader(big))
	req.Header.Set("X-Admin-Token", "tok")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("oversized body status=%d want 400", rec.Code)
	}
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

// TestWhitelistDomainAcceptsPastedURL domain 字段必须容忍粘贴完整网址。
//
// 起因：管理员提交 bbs1.org 时填了地址栏里的 https://bbs1.org/，被判不合法——
// 但 bbs1.org 本身完全合法（validPublicDomain 返回 true）。报错只说「domain 不合法」，
// 看不出是格式问题还是域名本身有问题，也无法照着改。
// 提交站点的自然动作就是复制 URL，字段不该要求先手工剥掉协议和路径。
func TestWhitelistDomainAcceptsPastedURL(t *testing.T) {
	accept := []string{
		"bbs1.org",              // 纯域名
		"https://bbs1.org/",     // 完整网址（最常见的粘贴形式）
		"https://bbs1.org",      // 无尾斜杠
		"http://bbs1.org/pick/", // 带路径
		"BBS1.ORG",              // 大小写
		"bbs1.org:8080",         // 带端口
		"go.dev",
	}
	for _, in := range accept {
		if got := domainOf(in); !validPublicDomain(got) {
			t.Errorf("domainOf(%q)=%q 应被接受", in, got)
		}
	}
	// 归一化后必须都是同一个域名，否则白名单会出现重复条目
	for _, in := range []string{"bbs1.org", "https://bbs1.org/", "BBS1.ORG", "bbs1.org:8080"} {
		if got := domainOf(in); got != "bbs1.org" {
			t.Errorf("domainOf(%q)=%q，应为 bbs1.org", in, got)
		}
	}

	// 仍然要挡住的（不能因为放宽格式就放进内网地址）
	reject := []string{
		"192.168.1.1", "https://192.168.1.1/", "10.0.0.5",
		"localhost", "http://localhost:8080", "foo.local", "a.internal",
		"", "1.2",
	}
	for _, in := range reject {
		if got := domainOf(in); validPublicDomain(got) {
			t.Errorf("domainOf(%q)=%q 不该被接受", in, got)
		}
	}
}
