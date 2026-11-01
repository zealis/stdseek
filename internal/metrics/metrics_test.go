package metrics

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"
)

func TestMiddlewareAndHandler(t *testing.T) {
	r := chi.NewRouter()
	r.Use(Middleware)
	r.Get("/api/ping", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusCreated)
		w.Write([]byte("ok"))
	})
	r.Method(http.MethodGet, "/metrics", Handler())

	// 产生一次被记录的请求
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/ping", nil))
	if rec.Code != http.StatusCreated {
		t.Fatalf("status=%d", rec.Code)
	}

	// 抓取指标
	rec = httptest.NewRecorder()
	r.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/metrics", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("metrics status=%d", rec.Code)
	}
	body := rec.Body.String()
	for _, want := range []string{
		"stdseek_http_requests_total",
		`route="/api/ping"`,
		`status="201"`,
		"stdseek_search_total",
	} {
		if !strings.Contains(body, want) {
			t.Fatalf("metrics output missing %q", want)
		}
	}
}
