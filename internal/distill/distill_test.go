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

func TestFetchPicksSameHostAndPrefersFullText(t *testing.T) {
	var gotBody string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		gotBody = string(b)
		if r.Header.Get("Authorization") != "Bearer tok" {
			t.Errorf("令牌没带上：%q", r.Header.Get("Authorization"))
		}
		_, _ = w.Write([]byte(`{"results":[
			{"title":"别的站","url":"https://other.example/a","snippet":"噪声"},
			{"title":"目标页","url":"https://www.go.dev/doc/error","snippet":"摘要","content_text":"完整正文内容"}
		]}`))
	}))
	defer srv.Close()

	c := New(srv.URL, "tok")
	page, err := c.Fetch(context.Background(), "https://go.dev/doc/error")
	if err != nil {
		t.Fatal(err)
	}
	if page.Title != "目标页" || page.Text != "完整正文内容" {
		t.Fatalf("取错了结果：%+v", page)
	}
	// 域名限定与整串 URL 当查询词都要发出去
	if !strings.Contains(gotBody, `"site":"go.dev"`) || !strings.Contains(gotBody, `"fetch_full":true`) {
		t.Fatalf("请求体不对：%s", gotBody)
	}
}

func TestFetchErrors(t *testing.T) {
	cases := []struct {
		name string
		body string
		code int
	}{
		{"没有结果", `{"results":[]}`, 200},
		{"结果都不是该域名", `{"results":[{"title":"x","url":"https://a.example/","snippet":"y"}]}`, 200},
		{"接口报错", `{"code":"UNAUTHORIZED","message":"无效的访问令牌"}`, 401},
		{"不是 JSON", `<html>oops</html>`, 200},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(c.code)
				_, _ = w.Write([]byte(c.body))
			}))
			defer srv.Close()
			if _, err := New(srv.URL, "").Fetch(context.Background(), "https://go.dev/x"); err == nil {
				t.Fatal("应该报错")
			}
		})
	}
}

// 只有被拦/连不上才蒸馏：404 换接口也是没有，不能拿别的内容顶替。
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
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"results":[{"title":"被拦的页","url":"https://blocked.example/p","content":"正文第一段"}]}`))
	}))
	defer api.Close()

	base := roundTripFunc(func(*http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: 403, Body: io.NopCloser(strings.NewReader("forbidden"))}, nil
	})
	tr := Wrap(base, New(api.URL, ""))
	req, _ := http.NewRequest(http.MethodGet, "https://blocked.example/p", nil)
	resp, err := tr.RoundTrip(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Fatalf("应替换成 200，实际 %d", resp.StatusCode)
	}
	body, _ := io.ReadAll(resp.Body)
	if !strings.Contains(string(body), "被拦的页") || !strings.Contains(string(body), "正文第一段") {
		t.Fatalf("合成 HTML 里没有正文：%s", body)
	}
}

func TestTransportLeaves404Alone(t *testing.T) {
	called := false
	api := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { called = true }))
	defer api.Close()

	base := roundTripFunc(func(*http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: 404, Body: io.NopCloser(strings.NewReader("gone"))}, nil
	})
	req, _ := http.NewRequest(http.MethodGet, "https://blocked.example/x", nil)
	resp, _ := Wrap(base, New(api.URL, "")).RoundTrip(req)
	if resp.StatusCode != 404 {
		t.Fatalf("404 不该被替换，实际 %d", resp.StatusCode)
	}
	if called {
		t.Fatal("404 不该调用蒸馏接口")
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
