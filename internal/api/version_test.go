package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// 后台标题旁的版本号：要能答，而且不能把 commit 之外的内部信息吐出去。
func TestAdminVersion(t *testing.T) {
	e := newTestEnv(t, testOpts{token: "tok"})
	e.srv.Version = "0.75.0"
	e.srv.Commit = "abc1234"
	e.srv.BuiltAt = "2026-10-30T05:00:00Z"

	req := httptest.NewRequest(http.MethodGet, "/admin/version", nil)
	req.Header.Set("X-Admin-Token", "tok")
	rec := httptest.NewRecorder()
	e.h.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d", rec.Code)
	}
	body := rec.Body.String()
	for _, w := range []string{`"version":"0.75.0"`, `"commit":"abc1234"`, `"built_at":"2026-10-30T05:00:00Z"`} {
		if !strings.Contains(body, w) {
			t.Fatalf("缺少 %s：%s", w, body)
		}
	}

	// 没有令牌不能读（后台接口一律如此，避免外人探测构建）
	req = httptest.NewRequest(http.MethodGet, "/admin/version", nil)
	rec = httptest.NewRecorder()
	e.h.ServeHTTP(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("无令牌应 401，got %d", rec.Code)
	}
}

// 概览配额要能在后台改、能回读；老前端不带这个字段时不能被静默改成 0。
func TestAIConfigSavesOverviewQuota(t *testing.T) {
	e := newTestEnv(t, testOpts{token: "tok"})
	save := func(body string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodPost, "/admin/ai", strings.NewReader(body))
		req.Header.Set("X-Admin-Token", "tok")
		rec := httptest.NewRecorder()
		e.h.ServeHTTP(rec, req)
		return rec
	}
	read := func() aiConfigView {
		req := httptest.NewRequest(http.MethodGet, "/admin/ai", nil)
		req.Header.Set("X-Admin-Token", "tok")
		rec := httptest.NewRecorder()
		e.h.ServeHTTP(rec, req)
		var v aiConfigView
		if err := json.Unmarshal(rec.Body.Bytes(), &v); err != nil {
			t.Fatal(err)
		}
		return v
	}

	if rec := save(`{"base_url":"https://p.example.com/v1","model":"pm","summary_endpoint":"primary","overview_daily_quota":7}`); rec.Code != http.StatusOK {
		t.Fatalf("保存失败：%d %s", rec.Code, rec.Body.String())
	}
	if got := read().OverviewDailyQuota; got != 7 {
		t.Fatalf("应回读 7，实际 %d", got)
	}

	// 不带该字段（老前端）：保持原值，而不是被当成 0 = 不限制
	if rec := save(`{"base_url":"https://p.example.com/v1","model":"pm","summary_endpoint":"primary"}`); rec.Code != http.StatusOK {
		t.Fatalf("保存失败：%d", rec.Code)
	}
	if got := read().OverviewDailyQuota; got != 7 {
		t.Fatalf("未传该字段时不该改动，实际 %d", got)
	}

	// 越界要拒
	if rec := save(`{"overview_daily_quota":99999}`); rec.Code != http.StatusBadRequest {
		t.Fatalf("越界应 400，got %d", rec.Code)
	}
}
