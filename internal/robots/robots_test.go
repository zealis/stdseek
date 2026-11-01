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
