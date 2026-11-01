package distill

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
)

// serp 造一份「像搜索结果页」的 HTML。
//
// 结构照抄真实页面：结果容器 > 链接 > h3，摘要单独一段。
// 这里不用谷歌的 class 名做断言依据——解析器也不该依赖它们，
// 但 fixture 用真实结构才能验证「容器文本减标题」这条兜底路径。
func serp(items ...string) string {
	var b strings.Builder
	b.WriteString(`<html><body>`)
	for _, it := range items {
		b.WriteString(`<div class="MjjYud"><div class="g"><div class="yuRUbf">`)
		b.WriteString(it)
		b.WriteString(`</div></div></div>`)
	}
	b.WriteString(`</body></html>`)
	return b.String()
}

func hit(href, title, snippet string) string {
	return `<a href="` + href + `"><h3>` + title + `</h3></a>` +
		`<div class="VwiC3b">` + snippet + `</div>`
}

// 只认同一条路径：别的站、别的页都不能拿来顶替。
func TestFetchPicksSamePath(t *testing.T) {
	var gotQuery string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotQuery = r.URL.Query().Get("q")
		_, _ = w.Write([]byte(serp(
			hit("https://other.example/doc/error", "别的站", "噪声"),
			hit("https://go.dev/doc/error?utm_source=x", "带参数的同一页", "参数摘要"),
			hit("https://go.dev/doc/error", "Errors and error handling", "Learn how to handle errors in Go."),
		)))
	}))
	defer srv.Close()

	page, err := New(srv.URL).Fetch(context.Background(), "https://go.dev/doc/error")
	if err != nil {
		t.Fatal(err)
	}
	// 完全一致那条优先于「同路径带 utm」那条
	if page.Title != "Errors and error handling" || page.Text != "Learn how to handle errors in Go." {
		t.Fatalf("取错了结果：%+v", page)
	}
	if gotQuery != "site:go.dev doc error" {
		t.Fatalf("查询词应该是 site:域名 + 路径词，实际 %q", gotQuery)
	}
}

// 搜索结果里只有目录页、没有这一页时必须报错，不能把目录页的摘要写进正文页。
func TestFetchRejectsOtherPath(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(serp(
			hit("https://go.dev/doc/", "文档首页", "所有文档"),
			hit("https://go.dev/doc/error/extra", "更深的页", "别的正文"),
		)))
	}))
	defer srv.Close()

	if _, err := New(srv.URL).Fetch(context.Background(), "https://go.dev/doc/error"); err == nil {
		t.Fatal("路径不一致时应该报错，而不是拿别的页顶替")
	}
}

// 只有摘要为空才算失败：标题能拿到但没摘要，说明这条结果不是正文页。
func TestFetchNeedsSnippet(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(serp(hit("https://go.dev/doc/error", "只有标题", ""))))
	}))
	defer srv.Close()

	if _, err := New(srv.URL).Fetch(context.Background(), "https://go.dev/doc/error"); err == nil {
		t.Fatal("没有摘要时应报错")
	}
}

// 验证码页经常是 200 /sorry/，要认出来并给出可读的原因。
func TestFetchDetectsCaptcha(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`<html><body><a href="/sorry/index?continue=x">请验证</a></body></html>`))
	}))
	defer srv.Close()

	_, err := New(srv.URL).Fetch(context.Background(), "https://go.dev/x")
	if !errors.Is(err, ErrBlocked) {
		t.Fatalf("应该识别成被拦，实际 %v", err)
	}
}

// 老的 /url?q= 跳转格式要还原成真实地址，否则域名与路径都对不上。
func TestParseSERPUnwrapsRedirect(t *testing.T) {
	body := serp(`<a href="/url?q=https://go.dev/doc/error&amp;sa=U"><h3>目标</h3></a><div class="VwiC3b">摘要</div>`)
	got, err := parseSERP([]byte(body))
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].URL != "https://go.dev/doc/error" {
		t.Fatalf("跳转链接没还原：%+v", got)
	}
	if got[0].Snippet != "摘要" || got[0].Title != "目标" {
		t.Fatalf("标题/摘要解析不对：%+v", got[0])
	}
}

func TestMatchScore(t *testing.T) {
	cases := []struct {
		want, got string
		score     int
	}{
		{"https://go.dev/doc/error", "https://go.dev/doc/error", 3},
		{"https://go.dev/doc/error", "http://www.go.dev/doc/error/", 3},
		{"https://go.dev/doc/error", "https://go.dev/doc/error?utm_source=x", 2},
		{"https://go.dev/doc/error?v=2", "https://go.dev/doc/error", 1},
		{"https://go.dev/doc/error?v=2", "https://go.dev/doc/error?v=3", 0},
		{"https://go.dev/doc/error", "https://go.dev/doc/", 0},
		{"https://go.dev/doc/error", "https://other.dev/doc/error", 0},
	}
	for _, c := range cases {
		if got := matchScore(c.want, c.got); got != c.score {
			t.Errorf("%s vs %s：期望 %d，实际 %d", c.want, c.got, c.score, got)
		}
	}
}

func TestKeywords(t *testing.T) {
	cases := map[string]string{
		"/":                                  "",
		"/doc/error":                         "doc error",
		"/docs/getting-started_2024.html":    "docs getting started",
		"/p/12345/go-generics":               "go generics",
		"/a/b/c/d/e/f/g/h":                   "", // 单字母段没有检索价值，全被丢掉
		"/%E4%B8%AD%E6%96%87/guide":          "中文 guide",
		"/2024/10/31/some-post":              "some post",
		"/x/abcdefghijklmnopqrstuvwxyz/next": "abcdefghijklmnopqrstuvwxyz next",
	}
	for in, want := range cases {
		// URL 里的中文是百分号编码的，先解码再进 keywords
		p, err := url.PathUnescape(in)
		if err != nil {
			t.Fatal(err)
		}
		if got := keywords(p); got != want {
			t.Errorf("keywords(%q) = %q，期望 %q", in, got, want)
		}
	}
}

func TestNormalizeEngine(t *testing.T) {
	if got := normalizeEngine(""); got != DefaultEngine {
		t.Fatalf("空值应回默认，实际 %q", got)
	}
	if got := normalizeEngine("https://www.google.com"); got != "https://www.google.com/search" {
		t.Fatalf("只填域名要补 /search，实际 %q", got)
	}
	if got := normalizeEngine("www.bing.com/search"); got != "https://www.bing.com/search" {
		t.Fatalf("缺协议要补 https，实际 %q", got)
	}
	if got := normalizeEngine("https://search.example/s"); got != "https://search.example/s" {
		t.Fatalf("已有路径不该被改，实际 %q", got)
	}
}

// 只有被拦/连不上才蒸馏：404 换个来源也是没有，不能拿别的内容顶替。
func TestShouldDistill(t *testing.T) {
	if shouldDistill(404, nil) || shouldDistill(410, nil) || shouldDistill(200, nil) {
		t.Fatal("404/410/200 不该触发蒸馏")
	}
	if !shouldDistill(403, nil) || !shouldDistill(451, nil) || !shouldDistill(503, nil) {
		t.Fatal("403/451/5xx 应该触发蒸馏")
	}
	if !shouldDistill(0, errors.New("dial tcp: timeout")) {
		t.Fatal("连不上应该触发蒸馏")
	}
}

func TestTransportReplacesBlockedResponse(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(serp(hit("https://blocked.example/p", "被拦的页", "摘要第一段"))))
	}))
	defer srv.Close()

	base := roundTripFunc(func(*http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: 403, Body: io.NopCloser(strings.NewReader("forbidden"))}, nil
	})
	req, _ := http.NewRequest(http.MethodGet, "https://blocked.example/p", nil)
	resp, err := Wrap(base, New(srv.URL)).RoundTrip(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Fatalf("应替换成 200，实际 %d", resp.StatusCode)
	}
	body, _ := io.ReadAll(resp.Body)
	s := string(body)
	if !strings.Contains(s, "被拦的页") || !strings.Contains(s, "摘要第一段") {
		t.Fatalf("合成 HTML 里没有标题与摘要：%s", s)
	}
	// 快照里存的就是这份合成 HTML，得能一眼看出它不是直连抓到的
	if !strings.Contains(s, `name="stdseek-source"`) {
		t.Fatalf("合成 HTML 缺少来源标记：%s", s)
	}
}

func TestTransportLeaves404Alone(t *testing.T) {
	called := false
	srv := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { called = true }))
	defer srv.Close()

	base := roundTripFunc(func(*http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: 404, Body: io.NopCloser(strings.NewReader("gone"))}, nil
	})
	req, _ := http.NewRequest(http.MethodGet, "https://blocked.example/x", nil)
	resp, _ := Wrap(base, New(srv.URL)).RoundTrip(req)
	if resp.StatusCode != 404 {
		t.Fatalf("404 不该被替换，实际 %d", resp.StatusCode)
	}
	if called {
		t.Fatal("404 不该去搜索")
	}
}

func TestWrapNilClientIsNoop(t *testing.T) {
	base := roundTripFunc(func(*http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: 403, Body: io.NopCloser(strings.NewReader("x"))}, nil
	})
	req, _ := http.NewRequest(http.MethodGet, "https://blocked.example/x", nil)
	resp, err := Wrap(base, nil).RoundTrip(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 403 {
		t.Fatalf("没有客户端时应原样返回，实际 %d", resp.StatusCode)
	}
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }
