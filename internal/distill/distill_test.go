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
