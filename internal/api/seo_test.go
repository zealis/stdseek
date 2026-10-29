package api

import (
	"bytes"
	"context"
	"image/png"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"voidxo/internal/store"
)

// 首页必须带站点级 OG / description / keywords，而且描述是按索引现状自动生成的。
func TestSEOMetaInjected(t *testing.T) {
	e := newTestEnv(t, testOpts{token: "tok"})
	if err := e.st.SetSetting(context.Background(), "site.name", "我的技术搜索"); err != nil {
		t.Fatal(err)
	}
	e.srv.invalidateSEO()
	if _, err := e.st.UpsertDocument(context.Background(), store.Document{
		URL: "https://go.dev/doc/error", Domain: "go.dev", Title: "error handling",
		FetchedAt: time.Now().Unix(),
	}); err != nil {
		t.Fatal(err)
	}

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.Host = "example.com"
	rec := httptest.NewRecorder()
	e.h.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("首页状态 %d", rec.Code)
	}
	if ct := rec.Header().Get("Content-Type"); !strings.Contains(ct, "text/html") {
		t.Fatalf("Content-Type=%q", ct)
	}
	body := rec.Body.String()

	wants := []string{
		`<title>我的技术搜索</title>`,
		`<meta property="og:site_name" content="我的技术搜索">`,
		`<meta property="og:type" content="website">`,
		`<meta property="og:image" content="http://example.com/og.png">`,
		`<meta property="og:url" content="http://example.com/">`,
		`<meta name="twitter:card" content="summary_large_image">`,
		`<link rel="canonical" href="http://example.com/">`,
		// 自动描述里应带上真实文档数（上面插了一篇）
		`已收录 1 篇文章`,
	}
	for _, w := range wants {
		if !strings.Contains(body, w) {
			t.Fatalf("首页缺少 %s", w)
		}
	}
	// 原来那条静态 description 必须被去掉，一页两个会让抓取器随机取一个
	if n := strings.Count(body, `name="description"`); n != 1 {
		t.Fatalf("description 应只有一条，实际 %d", n)
	}
	// 关键词自动生成：至少包含固定词与分类名
	if !strings.Contains(body, "技术搜索") || !strings.Contains(body, "官方网站") {
		t.Fatalf("关键词不像自动生成的：%s", body)
	}
}

// 反代下用转发头推导绝对地址，否则 OG 会指到内网。
func TestSEOUsesForwardedHost(t *testing.T) {
	e := newTestEnv(t, testOpts{token: "tok"})
	req := httptest.NewRequest(http.MethodGet, "/search", nil)
	req.Host = "127.0.0.1:8080"
	req.Header.Set("X-Forwarded-Proto", "https")
	req.Header.Set("X-Forwarded-Host", "voidxo.dev")
	rec := httptest.NewRecorder()
	e.h.ServeHTTP(rec, req)
	body := rec.Body.String()
	if !strings.Contains(body, `content="https://voidxo.dev/og.png"`) {
		t.Fatalf("OG 图片地址没走转发头：%s", body)
	}
	if !strings.Contains(body, `href="https://voidxo.dev/search"`) {
		t.Fatal("canonical 没走转发头")
	}
}

// 后台存了描述与关键词就用它们，覆盖自动生成；并且立刻生效（不等缓存过期）。
func TestSEOSiteSettingsOverride(t *testing.T) {
	e := newTestEnv(t, testOpts{token: "tok"})

	// 先读一次把自动生成的描述缓存热起来，再保存，验证「立刻生效」不是靠 TTL 过期
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	rec := httptest.NewRecorder()
	e.h.ServeHTTP(rec, req)
	if !strings.Contains(rec.Body.String(), "只索引人工审核过的技术站点") {
		t.Fatalf("自动描述没生成：%s", rec.Body.String())
	}

	body := `{"site.name":"维空","site.description":"手写的描述","site.keywords":"甲,乙,丙","site.admin_email":""}`
	req = httptest.NewRequest(http.MethodPost, "/admin/site", strings.NewReader(body))
	req.Header.Set("X-Admin-Token", "tok")
	req.Header.Set("Content-Type", "application/json")
	rec = httptest.NewRecorder()
	e.h.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("保存站点信息失败：%d %s", rec.Code, rec.Body.String())
	}

	// 先读一次把缓存热起来，再确认保存后的值已生效
	req = httptest.NewRequest(http.MethodGet, "/", nil)
	rec = httptest.NewRecorder()
	e.h.ServeHTTP(rec, req)
	page := rec.Body.String()
	for _, w := range []string{`content="手写的描述"`, `content="甲,乙,丙"`, `<title>维空</title>`} {
		if !strings.Contains(page, w) {
			t.Fatalf("保存后首页缺少 %s", w)
		}
	}
}

// /api 未命中仍是 JSON 404，不能被 SPA 注入逻辑吞成 HTML。
func TestUnknownAPINotInjected(t *testing.T) {
	e := newTestEnv(t, testOpts{token: "tok"})
	req := httptest.NewRequest(http.MethodGet, "/api/nope", nil)
	rec := httptest.NewRecorder()
	e.h.ServeHTTP(rec, req)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("状态 %d", rec.Code)
	}
	// 不能把 SPA 的 index.html 当成 404 返回：那会让前端以为是路由页
	if strings.Contains(rec.Body.String(), "<!doctype html") {
		t.Fatalf("不该注入 HTML：%s", rec.Body.String())
	}
}

// 卡片图：合法 PNG、可缓存、ETag 命中返回 304。
func TestOGImage(t *testing.T) {
	e := newTestEnv(t, testOpts{token: "tok"})
	e.srv.DataDir = t.TempDir()

	req := httptest.NewRequest(http.MethodGet, "/og.png", nil)
	rec := httptest.NewRecorder()
	e.h.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("状态 %d", rec.Code)
	}
	if ct := rec.Header().Get("Content-Type"); ct != "image/png" {
		t.Fatalf("Content-Type=%q", ct)
	}
	if _, err := png.Decode(bytes.NewReader(rec.Body.Bytes())); err != nil {
		t.Fatalf("不是合法 PNG：%v", err)
	}
	if rec.Body.Len() < 5000 {
		t.Fatalf("卡片图太小（%d 字节），可能没画上东西", rec.Body.Len())
	}

	etag := rec.Header().Get("ETag")
	if etag == "" {
		t.Fatal("缺 ETag")
	}
	req = httptest.NewRequest(http.MethodGet, "/og.png", nil)
	req.Header.Set("If-None-Match", etag)
	rec = httptest.NewRecorder()
	e.h.ServeHTTP(rec, req)
	if rec.Code != http.StatusNotModified {
		t.Fatalf("应 304，实际 %d", rec.Code)
	}
}
