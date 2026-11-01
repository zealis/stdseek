package i18n

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestPickPrefersCookie(t *testing.T) {
	r := httptest.NewRequest("GET", "/", nil)
	r.Header.Set("Accept-Language", "en-US,en;q=0.9")
	r.AddCookie(&http.Cookie{Name: CookieName, Value: ZhCN})
	if got := Pick(r); got != ZhCN {
		t.Fatalf("cookie 应该盖过 Accept-Language，got %q", got)
	}
}

func TestPickFallsBackToAcceptLanguage(t *testing.T) {
	cases := []struct {
		header string
		want   string
	}{
		{"en-US,en;q=0.9", En},
		{"zh-CN,zh;q=0.9,en;q=0.8", ZhCN},
		{"zh-TW", ZhCN},
		{"de-DE,de;q=0.9,en;q=0.8", En}, // 不认识的语言跳过，接着往下找
		{"de-DE", Default},              // 一个都不认识 → 默认
		{"", Default},
		{"en", En},
	}
	for _, c := range cases {
		r := httptest.NewRequest("GET", "/", nil)
		if c.header != "" {
			r.Header.Set("Accept-Language", c.header)
		}
		if got := Pick(r); got != c.want {
			t.Errorf("Accept-Language=%q → %q，want %q", c.header, got, c.want)
		}
	}
}

func TestPickIgnoresUnknownCookie(t *testing.T) {
	r := httptest.NewRequest("GET", "/", nil)
	r.AddCookie(&http.Cookie{Name: CookieName, Value: "de"})
	r.Header.Set("Accept-Language", "en")
	if got := Pick(r); got != En {
		t.Fatalf("非法 cookie 应该被忽略，got %q", got)
	}
}
