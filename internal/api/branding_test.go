package api

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// 1×1 透明 PNG，够用来验「按魔数认类型」和「原样吐回」。
const tinyPNG = "iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAYAAAAfFcSJAAAADUlEQVR42mP8z8BQDwAEhQGAhKmMIQAAAABJRU5ErkJggg=="

func pngBytes(t *testing.T) []byte {
	t.Helper()
	b, err := base64.StdEncoding.DecodeString(tinyPNG)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

// uploadIcon 走一次真实的多部分上传。
func uploadIcon(t *testing.T, e testEnv, name string, data []byte) *httptest.ResponseRecorder {
	t.Helper()
	var body bytes.Buffer
	mw := multipart.NewWriter(&body)
	fw, err := mw.CreateFormFile("file", name)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := fw.Write(data); err != nil {
		t.Fatal(err)
	}
	mw.Close()

	req := httptest.NewRequest(http.MethodPost, "/admin/branding/icon", &body)
	req.Header.Set("Content-Type", mw.FormDataContentType())
	req.Header.Set("X-Admin-Token", "tok")
	rec := httptest.NewRecorder()
	e.h.ServeHTTP(rec, req)
	return rec
}

func decodeBranding(t *testing.T, raw []byte) Branding {
	t.Helper()
	var got struct {
		Branding Branding `json:"branding"`
	}
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatalf("响应不是 JSON：%s", raw)
	}
	return got.Branding
}

// 上传后：接口回显、/brand/icon 原样返回、favicon 被顶掉、ETag 能 304。
func TestBrandIconUploadAndServe(t *testing.T) {
	e := newTestEnv(t, testOpts{token: "tok"})
	e.srv.DataDir = t.TempDir()
	png := pngBytes(t)

	rec := uploadIcon(t, e, "logo.png", png)
	if rec.Code != http.StatusOK {
		t.Fatalf("上传失败：%d %s", rec.Code, rec.Body.String())
	}
	b := decodeBranding(t, rec.Body.Bytes())
	if !b.HasIcon || !strings.HasPrefix(b.IconURL, "/brand/icon?v=") || b.Version == "" {
		t.Fatalf("回显不对：%+v", b)
	}

	// 公开接口也要能读到
	req := httptest.NewRequest(http.MethodGet, "/api/branding", nil)
	r2 := httptest.NewRecorder()
	e.h.ServeHTTP(r2, req)
	var pub Branding
	if err := json.Unmarshal(r2.Body.Bytes(), &pub); err != nil || !pub.HasIcon {
		t.Fatalf("/api/branding 应显示已设置图标：%s", r2.Body.String())
	}

	req = httptest.NewRequest(http.MethodGet, b.IconURL, nil)
	r3 := httptest.NewRecorder()
	e.h.ServeHTTP(r3, req)
	if r3.Code != http.StatusOK || !bytes.Equal(r3.Body.Bytes(), png) {
		t.Fatalf("取回图标失败：%d，%d 字节", r3.Code, r3.Body.Len())
	}
	if ct := r3.Header().Get("Content-Type"); ct != "image/png" {
		t.Fatalf("Content-Type=%q", ct)
	}
	if cc := r3.Header().Get("Cache-Control"); !strings.Contains(cc, "immutable") {
		t.Fatalf("带版本号应可长缓存，实际 %q", cc)
	}
	if csp := r3.Header().Get("Content-Security-Policy"); !strings.Contains(csp, "default-src 'none'") {
		t.Fatalf("图标响应缺 CSP：%q", csp)
	}

	// 标签页图标：没版本号，靠 ETag 立刻生效
	req = httptest.NewRequest(http.MethodGet, "/favicon.ico", nil)
	r4 := httptest.NewRecorder()
	e.h.ServeHTTP(r4, req)
	if r4.Code != http.StatusOK || !bytes.Equal(r4.Body.Bytes(), png) {
		t.Fatalf("favicon 没被顶掉：%d", r4.Code)
	}
	etag := r4.Header().Get("ETag")
	if etag == "" {
		t.Fatal("favicon 应带 ETag")
	}
	req = httptest.NewRequest(http.MethodGet, "/favicon.ico", nil)
	req.Header.Set("If-None-Match", etag)
	r5 := httptest.NewRecorder()
	e.h.ServeHTTP(r5, req)
	if r5.Code != http.StatusNotModified {
		t.Fatalf("带 If-None-Match 应 304，实际 %d", r5.Code)
	}
}

// 恢复默认：配置清空、文件删掉、favicon 回到内嵌产物。
func TestBrandIconClear(t *testing.T) {
	e := newTestEnv(t, testOpts{token: "tok"})
	e.srv.DataDir = t.TempDir()
	if rec := uploadIcon(t, e, "logo.png", pngBytes(t)); rec.Code != http.StatusOK {
		t.Fatalf("上传失败：%d", rec.Code)
	}

	req := httptest.NewRequest(http.MethodPost, "/admin/branding/icon/clear", nil)
	req.Header.Set("X-Admin-Token", "tok")
	rec := httptest.NewRecorder()
	e.h.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("清空失败：%d %s", rec.Code, rec.Body.String())
	}
	if decodeBranding(t, rec.Body.Bytes()).HasIcon {
		t.Fatal("清空后不该还报告有图标")
	}

	req = httptest.NewRequest(http.MethodGet, "/brand/icon", nil)
	r2 := httptest.NewRecorder()
	e.h.ServeHTTP(r2, req)
	if r2.Code != http.StatusNotFound {
		t.Fatalf("没有图标时 /brand/icon 应 404，实际 %d", r2.Code)
	}

	// favicon 回退到内嵌产物：仍能取到东西，但不是我们传的 PNG
	req = httptest.NewRequest(http.MethodGet, "/favicon.svg", nil)
	r3 := httptest.NewRecorder()
	e.h.ServeHTTP(r3, req)
	if r3.Code != http.StatusOK {
		t.Fatalf("回退失败：%d", r3.Code)
	}
	if bytes.Equal(r3.Body.Bytes(), pngBytes(t)) {
		t.Fatal("清空后不该还返回上传的图标")
	}
}

// 非图片、空文件、超大文件都要挡住——图标是同源提供的，不能让任意内容进来。
func TestBrandIconRejectsBadPayload(t *testing.T) {
	e := newTestEnv(t, testOpts{token: "tok"})
	e.srv.DataDir = t.TempDir()

	if rec := uploadIcon(t, e, "x.html", []byte("<html><script>alert(1)</script></html>")); rec.Code != http.StatusBadRequest {
		t.Fatalf("HTML 应被拒，实际 %d", rec.Code)
	}
	if rec := uploadIcon(t, e, "empty.png", nil); rec.Code != http.StatusBadRequest {
		t.Fatalf("空文件应被拒，实际 %d", rec.Code)
	}
	big := append(pngBytes(t), bytes.Repeat([]byte("x"), maxBrandBytes)...)
	if rec := uploadIcon(t, e, "big.png", big); rec.Code != http.StatusBadRequest {
		t.Fatalf("超大文件应被拒，实际 %d", rec.Code)
	}
}

// 没配置令牌就改不了图标。
func TestBrandIconRequiresToken(t *testing.T) {
	e := newTestEnv(t, testOpts{token: "tok"})
	req := httptest.NewRequest(http.MethodPost, "/admin/branding/icon/clear", nil)
	rec := httptest.NewRecorder()
	e.h.ServeHTTP(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("无令牌应 401，实际 %d", rec.Code)
	}
}

// 站点标识要写进 HTML：慢网下不能先画默认章再跳。
func TestBrandingInjectedIntoHTML(t *testing.T) {
	e := newTestEnv(t, testOpts{token: "tok"})
	e.srv.DataDir = t.TempDir()
	ctx := context.Background()
	if err := e.st.SetSetting(ctx, "site.name", "搜索派"); err != nil {
		t.Fatal(err)
	}
	if rec := uploadIcon(t, e, "logo.png", pngBytes(t)); rec.Code != http.StatusOK {
		t.Fatalf("上传失败：%d", rec.Code)
	}
	b := decodeBranding(t, uploadIcon(t, e, "logo.png", pngBytes(t)).Body.Bytes())

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	rec := httptest.NewRecorder()
	e.h.ServeHTTP(rec, req)
	body := rec.Body.String()

	if !strings.Contains(body, "window.__STDSEEK_BRANDING__=") {
		t.Fatal("首页没有注入站点标识")
	}
	if !strings.Contains(body, `"name":"搜索派"`) || !strings.Contains(body, b.IconURL) {
		t.Fatalf("注入内容不完整：%s", body)
	}
	// 首屏骨架也要用真实站名，否则慢网下先闪一下默认名
	if !strings.Contains(body, `<h1 class="boot-title">搜索派</h1>`) {
		t.Fatal("首屏骨架没换成真实站名")
	}
	if strings.Contains(body, `<h1 class="boot-title">stdseek`) {
		t.Fatal("骨架里还留着默认站名")
	}
}

// 站名里带 </script> 也不能逃出 JSON（json.Marshal 会把 < 转成 \u003c）。
func TestBrandingScriptEscapesName(t *testing.T) {
	e := newTestEnv(t, testOpts{token: "tok"})
	e.srv.DataDir = t.TempDir()
	evil := `</script><script>alert(1)</script>`
	if err := e.st.SetSetting(context.Background(), "site.name", evil); err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	rec := httptest.NewRecorder()
	e.h.ServeHTTP(rec, req)
	body := rec.Body.String()
	if strings.Contains(body, "</script><script>alert(1)") {
		t.Fatalf("站名逃出了 script 标签：%s", body)
	}
	if !strings.Contains(body, `\u003c/script\u003e`) {
		t.Fatal("注入的 JSON 应把 < 转义成 \\u003c")
	}
}

// iOS Safari 拿不到「只有 viewBox」的 SVG 的固有尺寸，会显示成破图标。
// 服务端补绝对宽高，用户在手机上才看得见。
func TestNormalizeSVGSize(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want string
		bad  string
	}{
		{
			name: "只有 viewBox",
			in:   `<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 1000 1000"><path d="M0 0"/></svg>`,
			want: `width="512" height="512"`,
		},
		{
			name: "按 viewBox 比例",
			in:   `<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 1000 500"></svg>`,
			want: `width="512" height="256"`,
		},
		{
			name: "百分比要换掉",
			in:   `<svg width="100%" height="100%" viewBox="0 0 10 10"></svg>`,
			want: `width="512" height="512"`,
			bad:  "100%",
		},
		{
			name: "已有绝对尺寸不动",
			in:   `<svg width="64" height="64" viewBox="0 0 8 8"></svg>`,
			want: `width="64" height="64"`,
		},
	}
	for _, c := range cases {
		got := string(normalizeSVGSize([]byte(c.in)))
		if !strings.Contains(got, c.want) {
			t.Fatalf("%s：期望包含 %s，实际 %s", c.name, c.want, got)
		}
		if c.bad != "" && strings.Contains(got, c.bad) {
			t.Fatalf("%s：不该还留着 %s → %s", c.name, c.bad, got)
		}
		// 内容不能被改坏
		if !strings.Contains(got, "</svg>") {
			t.Fatalf("%s：结构被破坏 → %s", c.name, got)
		}
	}
}

// 端到端：传一个没有宽高的 SVG，取回来时已经补上了。
func TestSVGIconServedWithSize(t *testing.T) {
	e := newTestEnv(t, testOpts{token: "tok"})
	e.srv.DataDir = t.TempDir()
	svg := []byte(`<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 100 100"><rect width="100" height="100" fill="#fc5243"/></svg>`)
	rec := uploadIcon(t, e, "logo.svg", svg)
	if rec.Code != http.StatusOK {
		t.Fatalf("上传失败：%d %s", rec.Code, rec.Body.String())
	}
	b := decodeBranding(t, rec.Body.Bytes())

	req := httptest.NewRequest(http.MethodGet, b.IconURL, nil)
	out := httptest.NewRecorder()
	e.h.ServeHTTP(out, req)
	body := out.Body.String()
	if ct := out.Header().Get("Content-Type"); ct != "image/svg+xml" {
		t.Fatalf("Content-Type=%q", ct)
	}
	if !strings.Contains(body, `width="512" height="512"`) {
		t.Fatalf("没有补宽高：%s", body)
	}
}

// 换图标后 HTML 必须指向带版本号的地址。
//
// 内置那两条是固定 URL：线上实测边缘节点把 /favicon.ico 缓存成了上传前的
// 内置 ICO，页面照样声明它，标签页就一直显示旧图标。
func TestIndexHTMLUsesVersionedIconLink(t *testing.T) {
	e := newTestEnv(t, testOpts{token: "tok"})
	e.srv.DataDir = t.TempDir()

	index := func() string {
		req := httptest.NewRequest(http.MethodGet, "/", nil)
		rec := httptest.NewRecorder()
		e.h.ServeHTTP(rec, req)
		if rec.Code != http.StatusOK {
			t.Fatalf("首页 %d", rec.Code)
		}
		return rec.Body.String()
	}

	// 没有自定义图标：内置两条原样留着，别把默认图标弄丢了
	page := index()
	if !strings.Contains(page, `href="favicon.svg"`) || !strings.Contains(page, `href="favicon.ico"`) {
		t.Fatalf("默认图标声明被动了：%s", page)
	}

	rec := uploadIcon(t, e, "logo.png", pngBytes(t))
	if rec.Code != http.StatusOK {
		t.Fatalf("上传失败：%d %s", rec.Code, rec.Body.String())
	}
	b := decodeBranding(t, rec.Body.Bytes())

	page = index()
	if !strings.Contains(page, `rel="icon" href="`+b.IconURL+`"`) {
		t.Fatalf("标签页图标没换成版本化地址：%s", page)
	}
	if strings.Contains(page, `href="favicon.svg"`) || strings.Contains(page, `href="favicon.ico"`) {
		t.Fatal("固定 URL 的图标声明应该被替换掉，否则浏览器仍按旧地址缓存")
	}
}

// manifest 的图标类型要跟着上传格式走。
//
// 以前无条件列 /favicon.svg 且写死 type=image/svg+xml；传 PNG 后这个地址返回的
// 是 PNG 字节，安装器按 SVG 解析失败，装出来的 PWA 没有图标。
func TestManifestIconFollowsUploadedType(t *testing.T) {
	e := newTestEnv(t, testOpts{token: "tok"})
	e.srv.DataDir = t.TempDir()

	type manifestIcon struct {
		Src  string `json:"src"`
		Type string `json:"type"`
	}
	manifest := func() []manifestIcon {
		req := httptest.NewRequest(http.MethodGet, "/manifest.webmanifest", nil)
		rec := httptest.NewRecorder()
		e.h.ServeHTTP(rec, req)
		if rec.Code != http.StatusOK {
			t.Fatalf("manifest %d", rec.Code)
		}
		var m struct {
			Icons []manifestIcon `json:"icons"`
		}
		if err := json.Unmarshal(rec.Body.Bytes(), &m); err != nil {
			t.Fatalf("manifest 不是 JSON：%s", rec.Body.String())
		}
		return m.Icons
	}

	// 没传图标：只有内置 SVG
	icons := manifest()
	if len(icons) != 1 || icons[0].Src != "/favicon.svg" || icons[0].Type != "image/svg+xml" {
		t.Fatalf("默认图标不对：%+v", icons)
	}

	rec := uploadIcon(t, e, "logo.png", pngBytes(t))
	if rec.Code != http.StatusOK {
		t.Fatalf("上传失败：%d %s", rec.Code, rec.Body.String())
	}
	b := decodeBranding(t, rec.Body.Bytes())

	icons = manifest()
	if len(icons) != 1 || icons[0].Src != b.IconURL || icons[0].Type != "image/png" {
		t.Fatalf("manifest 应只列自定义图标且类型为 image/png：%+v", icons)
	}
}
