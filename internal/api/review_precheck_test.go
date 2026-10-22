package api

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"voidxo/internal/precheck"
	"voidxo/internal/store"
)

// fakeChecker 返回固定预检结果，避免测试依赖真实网络。
func fakeChecker(res precheck.Result) func(context.Context, string) precheck.Result {
	return func(context.Context, string) precheck.Result { return res }
}

func decodePrecheck(t *testing.T, raw string) precheck.Result {
	t.Helper()
	var got precheck.Result
	if err := json.Unmarshal([]byte(raw), &got); err != nil {
		t.Fatalf("预检 JSON 解析失败: %v (%s)", err, raw)
	}
	return got
}

// 提交入口开启时，预检结果应在落库时就写进 review_queue（审核时无需再手动点）。
func TestSubmitStoresPrecheck(t *testing.T) {
	env := newTestEnv(t, testOpts{
		submit: true,
		checkFn: fakeChecker(precheck.Result{
			Reachable: true, StatusCode: 200,
			ContentType: "text/html; charset=utf-8", IsHTML: true,
			RobotsFound: true, RobotsAllowed: true,
			FinalURL: "https://example.com/post",
		}),
	})

	body := `{"url":"https://example.com/post","category":"blog","reason":"好站"}`
	req := httptest.NewRequest(http.MethodPost, "/api/submit", strings.NewReader(body))
	rec := httptest.NewRecorder()
	env.h.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("submit status=%d body=%s", rec.Code, rec.Body.String())
	}

	items, total, err := env.st.ListReview(context.Background(), "pending", "", 10, 0)
	if err != nil {
		t.Fatal(err)
	}
	if total != 1 || len(items) != 1 {
		t.Fatalf("review queue total=%d items=%d", total, len(items))
	}
	item := items[0]
	if item.PrecheckAt == nil {
		t.Fatal("precheck_at should be set when submit runs precheck")
	}
	got := decodePrecheck(t, item.Precheck)
	if !got.Reachable || got.StatusCode != 200 || !got.IsHTML || !got.RobotsAllowed {
		t.Fatalf("unexpected precheck payload: %+v", got)
	}
	if got.CheckedAt == 0 {
		t.Fatal("checked_at should be recorded for display")
	}
	if got.URL != "https://example.com/post" {
		t.Fatalf("precheck url=%q", got.URL)
	}
}

// 审核员点「重新预检」：结果写回队列，且重复调用可刷新（如站点已恢复）。
func TestReviewPrecheckEndpointPersists(t *testing.T) {
	env := newTestEnv(t, testOpts{
		token:   "tok",
		checkFn: fakeChecker(precheck.Result{Reachable: false, StatusCode: 404, IsHTML: true}),
	})
	ctx := context.Background()
	id, err := env.st.CreateSubmission(ctx, store.ReviewItem{
		URL: "https://example.com/gone", Domain: "example.com", Category: "blog", Reason: "测试",
	})
	if err != nil {
		t.Fatal(err)
	}

	post := func() *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodPost, fmt.Sprintf("/admin/review/%d/precheck", id), nil)
		req.Header.Set("X-Admin-Token", "tok")
		rec := httptest.NewRecorder()
		env.h.ServeHTTP(rec, req)
		return rec
	}

	rec := post()
	if rec.Code != http.StatusOK {
		t.Fatalf("precheck status=%d body=%s", rec.Code, rec.Body.String())
	}
	var out struct {
		ID       int64           `json:"id"`
		Precheck precheck.Result `json:"precheck"`
		Black    bool            `json:"blacklisted"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	if out.ID != id || out.Precheck.Reachable || out.Precheck.StatusCode != 404 {
		t.Fatalf("unexpected response: %+v", out)
	}

	item, err := env.st.GetReview(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	if item.PrecheckAt == nil {
		t.Fatal("precheck_at should be persisted")
	}
	stored := decodePrecheck(t, item.Precheck)
	if stored.Reachable || stored.StatusCode != 404 {
		t.Fatalf("stored precheck=%+v", stored)
	}
	if stored.CheckedAt == 0 {
		t.Fatal("stored precheck should carry checked_at")
	}

	// 站点恢复后再次预检，新结果必须覆盖旧结果（否则审核员看到的是过期信息）
	env.srv.checkFn = fakeChecker(precheck.Result{Reachable: true, StatusCode: 200, IsHTML: true})
	if rec = post(); rec.Code != http.StatusOK {
		t.Fatalf("second precheck status=%d", rec.Code)
	}
	item, err = env.st.GetReview(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	stored = decodePrecheck(t, item.Precheck)
	if !stored.Reachable || stored.StatusCode != 200 {
		t.Fatalf("precheck should be refreshed, got %+v", stored)
	}
}

func TestReviewPrecheckErrors(t *testing.T) {
	env := newTestEnv(t, testOpts{token: "tok", checkFn: fakeChecker(precheck.Result{})})

	// 不存在的条目 → 404
	req := httptest.NewRequest(http.MethodPost, "/admin/review/9999/precheck", nil)
	req.Header.Set("X-Admin-Token", "tok")
	rec := httptest.NewRecorder()
	env.h.ServeHTTP(rec, req)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("missing item status=%d want 404", rec.Code)
	}

	// 库里存在但域名是私网（历史脏数据）→ 拒绝预检，避免内网探测
	ctx := context.Background()
	id, err := env.st.CreateSubmission(ctx, store.ReviewItem{
		URL: "http://127.0.0.1/admin", Domain: "127.0.0.1", Category: "blog",
	})
	if err != nil {
		t.Fatal(err)
	}
	req = httptest.NewRequest(http.MethodPost, fmt.Sprintf("/admin/review/%d/precheck", id), nil)
	req.Header.Set("X-Admin-Token", "tok")
	rec = httptest.NewRecorder()
	env.h.ServeHTTP(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("private domain status=%d want 400", rec.Code)
	}

	// 无令牌 → 401
	req = httptest.NewRequest(http.MethodPost, fmt.Sprintf("/admin/review/%d/precheck", id), nil)
	rec = httptest.NewRecorder()
	env.h.ServeHTTP(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("no token status=%d want 401", rec.Code)
	}
}
