package robots

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestAllowedRespectsDisallow(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/robots.txt" {
			w.Write([]byte("User-agent: *\nDisallow: /secret\n"))
			return
		}
		w.Write([]byte("ok"))
	}))
	defer srv.Close()

	c := NewCache("stdseek-test", 2*time.Second)
	if !c.Allowed(srv.URL + "/") {
		t.Fatal("homepage should be allowed")
	}
	if c.Allowed(srv.URL + "/secret/page") {
		t.Fatal("disallowed path must be blocked")
	}
}

func TestAllowedWhenRobotsMissing(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.NotFound(w, nil)
	}))
	defer srv.Close()

	c := NewCache("stdseek-test", 2*time.Second)
	if !c.Allowed(srv.URL + "/anything") {
		t.Fatal("missing robots.txt should allow all")
	}
}

// Crawl-delay 是站方明说的礼貌要求，限速层拿它当下限（Heritrix/Nutch 都尊重它）。
func TestCrawlDelay(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/robots.txt" {
			_, _ = w.Write([]byte("User-agent: *\nCrawl-delay: 5\nDisallow: /secret\n"))
			return
		}
		_, _ = w.Write([]byte("ok"))
	}))
	defer srv.Close()

	c := NewCache("stdseek-test", 2*time.Second)
	if got := c.CrawlDelay(srv.URL + "/"); got != 5*time.Second {
		t.Fatalf("Crawl-delay: 5 应解析为 5s，实际 %v", got)
	}
}

// 没有 robots.txt 或没写 Crawl-delay 时返回 0，限速层退回默认间隔。
func TestCrawlDelayMissing(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.NotFound(w, nil)
	}))
	defer srv.Close()

	c := NewCache("stdseek-test", 2*time.Second)
	if got := c.CrawlDelay(srv.URL + "/"); got != 0 {
		t.Fatalf("没有 robots.txt 时应为 0，实际 %v", got)
	}
}
