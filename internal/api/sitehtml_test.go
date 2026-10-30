package api

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// saveSite 走真实接口保存，避免测试自己拼 settings 绕过校验。
func saveSite(t *testing.T, e testEnv, body string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/admin/site", strings.NewReader(body))
	req.Header.Set("X-Admin-Token", "tok")
	rec := httptest.NewRecorder()
	e.h.ServeHTTP(rec, req)
	return rec
}

func getPage(t *testing.T, e testEnv, path string) string {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, path, nil)
	rec := httptest.NewRecorder()
	e.h.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("%s 状态 %d", path, rec.Code)
	}
	return rec.Body.String()
}

const (
	testHeadCode = `<script>window.__analytics=1;</script>`
	testFootCode = `<script>window.__footerLoaded=1;</script>`
)

// 页头页脚要真的出现在访客页面上，而且插在正确的位置。
func TestSiteCodeInjectedIntoVisitorPages(t *testing.T) {
	e := newTestEnv(t, testOpts{token: "tok"})
	body := `{"site.head_html":"` + strings.ReplaceAll(testHeadCode, `"`, `\"`) + `",` +
		`"site.foot_html":"` + strings.ReplaceAll(testFootCode, `"`, `\"`) + `"}`
	if rec := saveSite(t, e, body); rec.Code != http.StatusOK {
		t.Fatalf("保存失败：%d %s", rec.Code, rec.Body.String())
	}

	page := getPage(t, e, "/")
	if !strings.Contains(page, testHeadCode) || !strings.Contains(page, testFootCode) {
		t.Fatal("首页没有注入页头/页脚代码")
	}
	// 位置错了等于脚本在 DOM 还没就绪时执行，统计常常因此丢数据
	if strings.Index(page, testHeadCode) > strings.Index(page, "</head>") {
		t.Fatal("页头代码应在 </head> 之前")
	}
	if strings.Index(page, testFootCode) > strings.LastIndex(page, "</body>") {
		t.Fatal("页脚代码应在 </body> 之前")
	}
	// 搜索结果页是访客页面，也要有
	if got := getPage(t, e, "/search?q=error"); !strings.Contains(got, testHeadCode) {
		t.Fatal("搜索结果页也应注入")
	}
}

// 后台与账号页面不注入：那里有账号、白名单和密码输入框，
// 第三方脚本一旦被投毒读到的就是这些。
func TestSiteCodeNotInjectedIntoAdminSurfaces(t *testing.T) {
	e := newTestEnv(t, testOpts{token: "tok"})
	if rec := saveSite(t, e, `{"site.head_html":"`+testHeadCode+`","site.foot_html":"`+testFootCode+`"}`); rec.Code != http.StatusOK {
		t.Fatalf("保存失败：%d", rec.Code)
	}
	for _, p := range []string{"/manage/site", "/manage/features", "/login", "/register", "/settings"} {
		if got := getPage(t, e, p); strings.Contains(got, testHeadCode) || strings.Contains(got, testFootCode) {
			t.Fatalf("%s 不该注入页头页脚代码", p)
		}
	}
}

// 保存站点信息时只写请求里出现的键：改个描述不能把代码清空。
func TestSaveSiteKeepsUntouchedKeys(t *testing.T) {
	e := newTestEnv(t, testOpts{token: "tok"})
	if rec := saveSite(t, e, `{"site.name":"甲","site.head_html":"`+testHeadCode+`"}`); rec.Code != http.StatusOK {
		t.Fatalf("保存失败：%d", rec.Code)
	}
	// 只改描述，不带 head_html
	if rec := saveSite(t, e, `{"site.description":"新描述"}`); rec.Code != http.StatusOK {
		t.Fatalf("保存失败：%d", rec.Code)
	}
	page := getPage(t, e, "/")
	if !strings.Contains(page, testHeadCode) {
		t.Fatal("只改描述不该清掉页头代码")
	}
	if !strings.Contains(page, "新描述") {
		t.Fatal("描述没写进去")
	}
}

// 代码字段的两条硬限制：NUL 字节与超大体积。
func TestSiteCodeLimits(t *testing.T) {
	e := newTestEnv(t, testOpts{token: "tok"})

	nul := `{"site.head_html":"a\u0000b"}`
	if rec := saveSite(t, e, nul); rec.Code != http.StatusBadRequest {
		t.Fatalf("含 NUL 应 400，got %d", rec.Code)
	}

	huge := `{"site.head_html":"` + strings.Repeat("a", maxSiteCodeBytes+1) + `"}`
	rec := saveSite(t, e, huge)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("超过 %d 字节应 400，got %d", maxSiteCodeBytes, rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "代码过长") {
		t.Fatalf("报错要说清原因：%s", rec.Body.String())
	}

	// 正好到上限要放行（差一字节就拒会让人反复试）
	ok := `{"site.head_html":"` + strings.Repeat("a", maxSiteCodeBytes) + `"}`
	if rec := saveSite(t, e, ok); rec.Code != http.StatusOK {
		t.Fatalf("正好 %d 字节应该放行，got %d %s", maxSiteCodeBytes, rec.Code, rec.Body.String())
	}
}

// 这段代码只能从后台读，公开接口一个字都不能带。
func TestSiteCodeNotExposedPublicly(t *testing.T) {
	e := newTestEnv(t, testOpts{token: "tok"})
	if rec := saveSite(t, e, `{"site.head_html":"`+testHeadCode+`"}`); rec.Code != http.StatusOK {
		t.Fatalf("保存失败：%d", rec.Code)
	}
	req := httptest.NewRequest(http.MethodGet, "/api/site", nil)
	rec := httptest.NewRecorder()
	e.h.ServeHTTP(rec, req)
	if strings.Contains(rec.Body.String(), "__analytics") {
		t.Fatalf("/api/site 泄漏了页头代码：%s", rec.Body.String())
	}
}
