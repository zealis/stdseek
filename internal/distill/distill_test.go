package distill

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// reader 造一个假的阅读器：把目标 URL 拼在基地址后面。
func reader(t *testing.T, title, body string, status int) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("X-Return-Format") != "html" {
			t.Errorf("没要求 HTML 格式：%q", r.Header.Get("X-Return-Format"))
		}
		if !strings.Contains(r.URL.Path, "blocked.example") {
			t.Errorf("目标 URL 没拼在基地址后面：%s", r.URL.Path)
		}
		w.WriteHeader(status)
		_, _ = w.Write([]byte(`<html><head><title>` + title + `</title></head><body><article>` + body + `</article></body></html>`))
	}))
}

func TestFetchReturnsWholePage(t *testing.T) {
	srv := reader(t, "被拦的那一页", "<p>整页正文第一段</p><p>第二段</p>", http.StatusOK)
	defer srv.Close()

	page, err := New(srv.URL+"/", "").Fetch(context.Background(), "https://blocked.example/p")
	if err != nil {
		t.Fatal(err)
	}
	if page.Title != "被拦的那一页" {
		t.Fatalf("标题取错：%q", page.Title)
	}
	if !strings.Contains(string(page.HTML), "整页正文第一段") || !strings.Contains(string(page.HTML), "第二段") {
		t.Fatalf("正文没拿到：%s", page.HTML)
	}
	if page.URL != "https://blocked.example/p" {
		t.Fatalf("URL 记错：%q", page.URL)
	}
}

// 目标站自己 404 时，阅读器会用 200 把它的错误页原样给我们——这种不能入库。
func TestFetchRejectsUpstreamError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("Title: -\n\nWarning: Target URL returned error 404: Not Found\n\nMarkdown Content:\n"))
	}))
	defer srv.Close()

	if _, err := New(srv.URL+"/", "").Fetch(context.Background(), "https://blocked.example/gone"); err == nil {
		t.Fatal("抓到的是错误页，应该报错")
	}
}

func TestFetchErrors(t *testing.T) {
	cases := []struct {
		name   string
		status int
		body   string
	}{
		{"限流", http.StatusTooManyRequests, "slow down"},
		{"额度用尽", http.StatusPaymentRequired, "pay"},
		{"服务异常", http.StatusBadGateway, "bad gateway"},
		{"空的", http.StatusOK, ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(c.status)
				_, _ = w.Write([]byte(c.body))
			}))
			defer srv.Close()
			if _, err := New(srv.URL+"/", "").Fetch(context.Background(), "https://blocked.example/x"); err == nil {
				t.Fatal("应该报错")
			}
		})
	}
}

func TestNormalizeReader(t *testing.T) {
	if got := normalizeReader(""); got != DefaultReader {
		t.Fatalf("空值应回默认，实际 %q", got)
	}
	if got := normalizeReader("r.jina.ai"); got != "https://r.jina.ai/" {
		t.Fatalf("缺协议要补 https 且补斜杠，实际 %q", got)
	}
	if got := normalizeReader("https://reader.example/get?u="); got != "https://reader.example/get?u=/" {
		t.Fatalf("末尾没斜杠要补，实际 %q", got)
	}
	if got := normalizeReader("https://reader.example/"); got != "https://reader.example/" {
		t.Fatalf("已规范的不该被改，实际 %q", got)
	}
}

// 只有被拦/连不上才蒸馏：404 换个通道也是没有，不能拿别的内容顶替。
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
	srv := reader(t, "被拦的页", "<p>整页正文第一段</p>", http.StatusOK)
	defer srv.Close()

	base := roundTripFunc(func(*http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: 403, Body: io.NopCloser(strings.NewReader("forbidden"))}, nil
	})
	req, _ := http.NewRequest(http.MethodGet, "https://blocked.example/p", nil)
	resp, err := Wrap(base, New(srv.URL+"/", "")).RoundTrip(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Fatalf("应替换成 200，实际 %d", resp.StatusCode)
	}
	body, _ := io.ReadAll(resp.Body)
	s := string(body)
	if !strings.Contains(s, "被拦的页") || !strings.Contains(s, "整页正文第一段") {
		t.Fatalf("合成 HTML 里没有正文：%s", s)
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
	resp, _ := Wrap(base, New(srv.URL+"/", "")).RoundTrip(req)
	if resp.StatusCode != 404 {
		t.Fatalf("404 不该被替换，实际 %d", resp.StatusCode)
	}
	if called {
		t.Fatal("404 不该去请求阅读器")
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

// 阅读器过不去的拦截页会用 200 回来，必须判失败——否则拦截页会当正文入库。
func TestFetchRejectsChallengePage(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`<html><head><title>Just a moment...</title></head>
<body><div id="cf-challenge-running">Checking your browser before accessing.</div></body></html>`))
	}))
	defer srv.Close()

	_, err := New(srv.URL+"/", "").Fetch(context.Background(), "https://blocked.example/p")
	if err == nil {
		t.Fatal("CF 挑战页应该判失败")
	}
	if !strings.Contains(err.Error(), "拦截页") {
		t.Fatalf("错误信息要说清是被拦了：%v", err)
	}
}

func TestBlockMarker(t *testing.T) {
	cases := []struct {
		title, plain string
		blocked      bool
	}{
		{"Just a moment...", "", true},
		{"", "You've been blocked by network security.", true},
		{"Attention Required! | Cloudflare", "Sorry, you have been blocked", true},
		{"", "whoa there, pardner!", true},
		{"百度安全验证", "", true},
		{"Errors - Go", "handle errors in Go", false},
		{"Cloudflare Turnstile 怎么用", "这篇文章讲 Turnstile 的接入方式", false},
	}
	for _, c := range cases {
		if got := blockMarker(c.title, c.plain) != ""; got != c.blocked {
			t.Errorf("%s / %.30s：期望 %v，实际 %v", c.title, c.plain, c.blocked, got)
		}
	}
}

// 空壳页判定：200、去掉脚本后几乎没有正文、但带着外部脚本——
// 正文要 JS 渲染才出现，阅读器（真浏览器）才取得回不同的东西。
func TestShellish(t *testing.T) {
	shell := []byte(`<html><head><link rel="stylesheet" href="/app.css"></head>
		<body><div id="app"></div><script src="/bundle.js"></script></body></html>`)
	if !shellish(shell) {
		t.Fatal("SPA 空壳应该被识别")
	}
	content := []byte(`<html><body><article>` + strings.Repeat("正文内容，讲得很细。", 60) +
		`</article><script src="/bundle.js"></script></body></html>`)
	if shellish(content) {
		t.Fatal("有正文的服务端渲染页不该被识别为空壳")
	}
	plain := []byte(`<html><body><p>没有脚本的静态薄页</p></body></html>`)
	if shellish(plain) {
		t.Fatal("没有外部脚本的页面，换通道拿回来的还是它自己")
	}
}

// 200 空壳页交给阅读器渲染：管线拿到的是渲染后的整页。
func TestTransportDistillsShell(t *testing.T) {
	srv := reader(t, "渲染后的标题", "<p>渲染后的正文</p>", http.StatusOK)
	defer srv.Close()

	base := roundTripFunc(func(*http.Request) (*http.Response, error) {
		return &http.Response{
			StatusCode: http.StatusOK,
			Header:     http.Header{"Content-Type": []string{"text/html"}},
			Body: io.NopCloser(strings.NewReader(
				`<html><body><div id="app"></div><script src="/bundle.js"></script></body></html>`)),
		}, nil
	})
	req, _ := http.NewRequest(http.MethodGet, "https://blocked.example/p", nil)
	resp, err := Wrap(base, New(srv.URL+"/", "")).RoundTrip(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != 200 || !strings.Contains(string(body), "渲染后的正文") {
		t.Fatalf("空壳页应换成阅读器渲染的结果，实际 %d：%s", resp.StatusCode, body)
	}
}

// 非空壳的 200 原样放行，阅读器一次都不该被惊动。
func TestTransportPassesThroughContentPage(t *testing.T) {
	readerCalled := false
	srv := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		readerCalled = true
	}))
	defer srv.Close()

	original := `<html><body><article>` + strings.Repeat("正文内容。", 80) +
		`</article><script src="/bundle.js"></script></body></html>`
	base := roundTripFunc(func(*http.Request) (*http.Response, error) {
		return &http.Response{
			StatusCode: http.StatusOK,
			Header:     http.Header{"Content-Type": []string{"text/html"}},
			Body:       io.NopCloser(strings.NewReader(original)),
		}, nil
	})
	req, _ := http.NewRequest(http.MethodGet, "https://blocked.example/p", nil)
	resp, err := Wrap(base, New(srv.URL+"/", "")).RoundTrip(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if string(body) != original {
		t.Fatal("有正文的页面应原样放行，响应体不该被改动")
	}
	if readerCalled {
		t.Fatal("有正文的页面不该去请求阅读器")
	}
}

// 空壳重取有每轮预算：名额恰好 maxShellFetches 个，用完即止。
func TestShellBudgetCapsRefetches(t *testing.T) {
	cli := New("https://reader.example/", "")
	for i := 0; i < maxShellFetches; i++ {
		if !cli.takeShell() {
			t.Fatalf("第 %d 次应该领到名额", i+1)
		}
	}
	if cli.takeShell() {
		t.Fatal("预算用完不该再发名额")
	}
}
