package precheck

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestCheckReachableHTML(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/robots.txt", func(w http.ResponseWriter, _ *http.Request) {
		w.Write([]byte("User-agent: *\nDisallow: /secret\n"))
	})
	mux.HandleFunc("/", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Write([]byte("<html><body>ok</body></html>"))
	})
	mux.HandleFunc("/secret", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		w.Write([]byte("<html>secret</html>"))
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	res := Check(context.Background(), srv.URL+"/", "voidxo-test", 3*time.Second)
	if !res.Reachable || res.StatusCode != 200 {
		t.Fatalf("result=%+v", res)
	}
	if !res.IsHTML {
		t.Fatalf("content type not html: %q", res.ContentType)
	}
	if !res.RobotsFound || !res.RobotsAllowed {
		t.Fatalf("robots should be found and allowed: %+v", res)
	}

	blocked := Check(context.Background(), srv.URL+"/secret", "voidxo-test", 3*time.Second)
	if blocked.RobotsAllowed {
		t.Fatalf("disallowed path must be flagged: %+v", blocked)
	}
}

func TestCheckUnreachableAnd404(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.NotFound(w, nil)
	}))
	defer srv.Close()

	res := Check(context.Background(), srv.URL+"/missing", "voidxo-test", 3*time.Second)
	if res.Reachable {
		t.Fatalf("404 should not be reachable: %+v", res)
	}
	if res.StatusCode != 404 {
		t.Fatalf("status=%d", res.StatusCode)
	}

	// 关闭后的端口 → 连接失败
	dead := httptest.NewServer(http.NotFoundHandler())
	url := dead.URL
	dead.Close()
	res = Check(context.Background(), url+"/", "voidxo-test", time.Second)
	if res.Reachable || res.Error == "" {
		t.Fatalf("closed server should carry error: %+v", res)
	}
}
