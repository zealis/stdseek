package api

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestRobotsTxt(t *testing.T) {
	e := newTestEnv(t, testOpts{token: "tok"})
	req := httptest.NewRequest(http.MethodGet, "/robots.txt", nil)
	rec := httptest.NewRecorder()
	e.h.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("状态 %d", rec.Code)
	}
	if ct := rec.Header().Get("Content-Type"); !strings.Contains(ct, "text/plain") {
		t.Fatalf("Content-Type=%q", ct)
	}
	body := rec.Body.String()
	for _, w := range []string{"User-agent: *", "Allow: /", "Disallow: /api/", "Disallow: /admin/", "Disallow: /*?q="} {
		if !strings.Contains(body, w) {
			t.Fatalf("robots.txt 缺少 %q：\n%s", w, body)
		}
	}
}

// security.txt：没填管理员邮箱时退回帮助页，填了就用 mailto。
func TestSecurityTxt(t *testing.T) {
	e := newTestEnv(t, testOpts{token: "tok"})

	req := httptest.NewRequest(http.MethodGet, securityPath, nil)
	req.Host = "voidxo.dev"
	rec := httptest.NewRecorder()
	e.h.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("状态 %d", rec.Code)
	}
	body := rec.Body.String()
	if !strings.Contains(body, "Contact: http://voidxo.dev/help") {
		t.Fatalf("没填邮箱应退回帮助页：\n%s", body)
	}
	for _, w := range []string{"Canonical: http://voidxo.dev/.well-known/security.txt", "Preferred-Languages:", "Expires:"} {
		if !strings.Contains(body, w) {
			t.Fatalf("security.txt 缺少 %q：\n%s", w, body)
		}
	}
	// Expires 必须是能解析的时间，且在一年左右
	line := ""
	for _, l := range strings.Split(body, "\n") {
		if strings.HasPrefix(l, "Expires:") {
			line = strings.TrimSpace(strings.TrimPrefix(l, "Expires:"))
		}
	}
	at, err := time.Parse(time.RFC3339, line)
	if err != nil {
		t.Fatalf("Expires 不是 RFC3339：%q", line)
	}
	if d := time.Until(at); d < 300*24*time.Hour || d > 400*24*time.Hour {
		t.Fatalf("Expires 应在一年左右，实际 %v", d)
	}

	// 填了邮箱就走 mailto
	if err := e.st.SetSetting(context.Background(), "site.admin_email", "sec@example.com"); err != nil {
		t.Fatal(err)
	}
	req = httptest.NewRequest(http.MethodGet, "/security.txt", nil)
	req.Host = "voidxo.dev"
	rec = httptest.NewRecorder()
	e.h.ServeHTTP(rec, req)
	if !strings.Contains(rec.Body.String(), "Contact: mailto:sec@example.com") {
		t.Fatalf("填了邮箱应走 mailto：\n%s", rec.Body.String())
	}
}

// llms.txt：站名、规模、主要页面与约束都要有，且是文本不是 HTML。
func TestLLMsTxt(t *testing.T) {
	e := newTestEnv(t, testOpts{token: "tok"})
	if err := e.st.SetSetting(context.Background(), "site.name", "我的搜索"); err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest(http.MethodGet, "/llms.txt", nil)
	req.Host = "voidxo.dev"
	rec := httptest.NewRecorder()
	e.h.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("状态 %d", rec.Code)
	}
	body := rec.Body.String()
	if strings.Contains(body, "<!doctype html") {
		t.Fatal("llms.txt 不该返回 HTML")
	}
	for _, w := range []string{"# 我的搜索", "## 主要页面", "## 使用约束", "http://voidxo.dev/api-docs", "/api/suggest"} {
		if !strings.Contains(body, w) {
			t.Fatalf("llms.txt 缺少 %q：\n%s", w, body)
		}
	}
}

// 三个文件都不能被 SPA 回退成 index.html，也不能被限流组误伤。
func TestSEOFilesNotSPA(t *testing.T) {
	e := newTestEnv(t, testOpts{token: "tok"})
	for _, p := range []string{"/robots.txt", "/security.txt", securityPath, "/llms.txt"} {
		req := httptest.NewRequest(http.MethodGet, p, nil)
		rec := httptest.NewRecorder()
		e.h.ServeHTTP(rec, req)
		if rec.Code != http.StatusOK {
			t.Fatalf("%s 状态 %d", p, rec.Code)
		}
		if strings.Contains(rec.Body.String(), "<!doctype html") {
			t.Fatalf("%s 回退成了首页", p)
		}
	}
}

// 首次启动：三个文件落到 <data>/web/ 下。
func TestEnsureSEOFilesWritesMissing(t *testing.T) {
	e := newTestEnv(t, testOpts{token: "tok"})
	dir := t.TempDir()
	e.srv.DataDir = dir
	if err := e.srv.EnsureSEOFiles(context.Background()); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"robots.txt", "security.txt", "llms.txt"} {
		b, err := os.ReadFile(filepath.Join(dir, "web", name))
		if err != nil {
			t.Fatalf("%s 没生成：%v", name, err)
		}
		if len(b) == 0 {
			t.Fatalf("%s 是空的", name)
		}
	}
}

// 已经存在的（管理员手改过的）不覆盖；只有强制重写才动它。
func TestSEOFilesPreserveHandEdits(t *testing.T) {
	e := newTestEnv(t, testOpts{token: "tok"})
	dir := t.TempDir()
	e.srv.DataDir = dir
	ctx := context.Background()
	if err := e.srv.EnsureSEOFiles(ctx); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "web", "robots.txt")
	if err := os.WriteFile(path, []byte("# 我的自定义规则\nUser-agent: *\nDisallow: /\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := e.srv.EnsureSEOFiles(ctx); err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(path); !strings.Contains(string(b), "我的自定义规则") {
		t.Fatalf("手改内容被覆盖了：%s", b)
	}
	// 强制重写（-emit-web-files）才会恢复默认
	if err := e.srv.EmitSEOFiles(ctx); err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(path); strings.Contains(string(b), "我的自定义规则") {
		t.Fatal("强制重写后不该还留着手改内容")
	}
}

// 服务优先读磁盘上的文件：手改完不用重启就能生效。
func TestServeSEOFileReadsDiskFirst(t *testing.T) {
	e := newTestEnv(t, testOpts{token: "tok"})
	dir := t.TempDir()
	e.srv.DataDir = dir
	if err := e.srv.EnsureSEOFiles(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "web", "robots.txt"), []byte("User-agent: GPTBot\nDisallow: /\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest(http.MethodGet, "/robots.txt", nil)
	rec := httptest.NewRecorder()
	e.h.ServeHTTP(rec, req)
	if !strings.Contains(rec.Body.String(), "GPTBot") {
		t.Fatalf("没有优先读磁盘上的文件：\n%s", rec.Body.String())
	}
}

// 站点地址：配了就用它，og:url / security.txt 的 Canonical 都跟着走。
func TestSiteBaseURLPrefersSetting(t *testing.T) {
	e := newTestEnv(t, testOpts{token: "tok"})
	dir := t.TempDir()
	e.srv.DataDir = dir
	ctx := context.Background()
	if err := e.st.SetSetting(ctx, "site.base_url", "https://voidxo.com/"); err != nil {
		t.Fatal(err)
	}
	if err := e.srv.EmitSEOFiles(ctx); err != nil {
		t.Fatal(err)
	}
	b, _ := os.ReadFile(filepath.Join(dir, "web", "security.txt"))
	if !strings.Contains(string(b), "Canonical: https://voidxo.com/.well-known/security.txt") {
		t.Fatalf("Canonical 没用配置的地址：\n%s", b)
	}
	// 请求里的 Host 不再影响它
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.Host = "127.0.0.1:8080"
	rec := httptest.NewRecorder()
	e.h.ServeHTTP(rec, req)
	if !strings.Contains(rec.Body.String(), `content="https://voidxo.com/og.png"`) {
		t.Fatalf("OG 图片地址没用配置的地址：\n%s", rec.Body.String())
	}
}
