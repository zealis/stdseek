package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"voidxo/internal/store"
)

// 提交后必须能把「凭证」交给用户：自增 id 可顺序遍历，会把别人提交的站点暴露出去，
// 所以对外只发随机 ticket，且用 ticket 能查回同一记录。
func TestSubmitReturnsQueryableTicket(t *testing.T) {
	e := newTestEnv(t, testOpts{submit: true})

	body := `{"url":"https://example.com","category":"blog","reason":"测试"}`
	req := httptest.NewRequest(http.MethodPost, "/api/submit", strings.NewReader(body))
	rec := httptest.NewRecorder()
	e.h.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("submit status=%d body=%s", rec.Code, rec.Body.String())
	}
	var created struct {
		Ticket string `json:"ticket"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &created); err != nil {
		t.Fatal(err)
	}
	if len(created.Ticket) < 10 || !strings.HasPrefix(created.Ticket, "vx") {
		t.Fatalf("ticket looks wrong: %q", created.Ticket)
	}
	if strings.Contains(rec.Body.String(), `"id"`) {
		t.Fatalf("response must not expose the enumerable id: %s", rec.Body.String())
	}

	// 用 ticket 查回状态
	req = httptest.NewRequest(http.MethodGet, "/api/submission?ticket="+created.Ticket, nil)
	rec = httptest.NewRecorder()
	e.h.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("query status=%d body=%s", rec.Code, rec.Body.String())
	}
	var got map[string]interface{}
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if got["domain"] != "example.com" || got["status"] != "pending" {
		t.Fatalf("unexpected payload: %v", got)
	}
	if got["status_text"] != "等待审核" {
		t.Fatalf("status_text should be user-facing Chinese, got %v", got["status_text"])
	}

	// 不存在的查询码 → 404，而不是 200 + 空对象
	req = httptest.NewRequest(http.MethodGet, "/api/submission?ticket=vxnotarealticket", nil)
	rec = httptest.NewRecorder()
	e.h.ServeHTTP(rec, req)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("unknown ticket status=%d want 404", rec.Code)
	}
}

// 每次提交的 ticket 必须不同，否则等于把查询码变成共享的。
func TestSubmissionTicketsAreUnique(t *testing.T) {
	e := newTestEnv(t, testOpts{submit: true})
	ctx := context.Background()
	seen := map[string]bool{}
	for i := 0; i < 5; i++ {
		_, ticket, err := e.st.CreateSubmission(ctx, store.ReviewItem{
			URL: "https://example.com/" + string(rune('a'+i)), Domain: "example.com", Category: "blog",
		})
		if err != nil {
			t.Fatal(err)
		}
		if seen[ticket] {
			t.Fatalf("duplicate ticket: %s", ticket)
		}
		seen[ticket] = true
	}
}

// 标签反馈：只在索引内页面接受，未知标签与非法结论都拒绝。
func TestLabelFeedbackValidation(t *testing.T) {
	e := newTestEnv(t, testOpts{})
	// 预检用的 documents 行（保证 URL 在库内且带标签理由）
	if _, err := e.st.UpsertDocument(context.Background(), store.Document{
		URL: "https://blog.example.com/error", Domain: "blog.example.com",
		Title: "error handling guide", Labels: `["ai_suspect"]`,
		LabelReasons: `{"ai_suspect":"模板化段落占比 82%"}`,
		FetchedAt:    time.Now().Unix(),
	}); err != nil {
		t.Fatal(err)
	}

	cases := []struct {
		name string
		body string
		want int
	}{
		{"ok", `{"url":"https://blog.example.com/error","label":"ai_suspect","verdict":"false_positive","note":"人工写的"}`, http.StatusOK},
		{"unknown label", `{"url":"https://blog.example.com/error","label":"spam","verdict":"false_positive"}`, http.StatusBadRequest},
		{"bad verdict", `{"url":"https://blog.example.com/error","label":"ai_suspect","verdict":"maybe"}`, http.StatusBadRequest},
		{"not indexed", `{"url":"https://nope.example.com/x","label":"ai_suspect","verdict":"false_positive"}`, http.StatusNotFound},
	}
	for _, c := range cases {
		req := httptest.NewRequest(http.MethodPost, "/api/label-feedback", strings.NewReader(c.body))
		rec := httptest.NewRecorder()
		e.h.ServeHTTP(rec, req)
		if rec.Code != c.want {
			t.Errorf("%s: status=%d want %d body=%s", c.name, rec.Code, c.want, rec.Body.String())
		}
	}
}

// 同一 client 对同一 (页面, 标签) 重复提交应改判而不是新增一行——
// 否则后台会把一次犹豫看成两条相反意见。
func TestLabelFeedbackUpsertPerClient(t *testing.T) {
	e := newTestEnv(t, testOpts{})
	ctx := context.Background()
	if _, err := e.st.UpsertDocument(ctx, store.Document{
		URL: "https://blog.example.com/error", Domain: "blog.example.com",
		Title: "t", Labels: `["ai_suspect"]`, FetchedAt: time.Now().Unix(),
	}); err != nil {
		t.Fatal(err)
	}
	post := func(verdict string) {
		body := `{"url":"https://blog.example.com/error","label":"ai_suspect","verdict":"` + verdict + `"}`
		req := httptest.NewRequest(http.MethodPost, "/api/label-feedback", strings.NewReader(body))
		req.RemoteAddr = "203.0.113.9:4444"
		rec := httptest.NewRecorder()
		e.h.ServeHTTP(rec, req)
		if rec.Code != http.StatusOK {
			t.Fatalf("feedback status=%d body=%s", rec.Code, rec.Body.String())
		}
	}
	post("false_positive")
	post("confirmed")

	items, total, err := e.st.ListLabelFeedback(ctx, "", "", 10, 0)
	if err != nil {
		t.Fatal(err)
	}
	if total != 1 || len(items) != 1 {
		t.Fatalf("want 1 row after re-judge, got total=%d items=%d", total, len(items))
	}
	if items[0].Verdict != store.VerdictConfirmed {
		t.Fatalf("latest verdict should win, got %s", items[0].Verdict)
	}

	// 误判统计只算 false_positive
	counts, err := e.st.LabelFalsePositiveCounts(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if counts["ai_suspect"] != 0 {
		t.Fatalf("confirmed should not count as false positive: %v", counts)
	}
}

// 后台：列表带按标签汇总，批量删除返回真实删除条数。
func TestAdminLabelFeedbackListAndDelete(t *testing.T) {
	e := newTestEnv(t, testOpts{token: "tok"})
	ctx := context.Background()
	if _, err := e.st.UpsertDocument(ctx, store.Document{
		URL: "https://blog.example.com/error", Domain: "blog.example.com",
		Title: "t", Labels: `["ai_suspect"]`, FetchedAt: time.Now().Unix(),
	}); err != nil {
		t.Fatal(err)
	}
	for i, verdict := range []string{store.VerdictFalsePositive, store.VerdictFalsePositive} {
		if err := e.st.UpsertLabelFeedback(ctx, store.LabelFeedback{
			URL: "https://blog.example.com/error", Domain: "blog.example.com",
			Label: "ai_suspect", Verdict: verdict, Note: "样本",
		}, string(rune('a'+i))); err != nil {
			t.Fatal(err)
		}
	}

	req := httptest.NewRequest(http.MethodGet, "/admin/label-feedback?verdict=false_positive&kw=error", nil)
	req.Header.Set("X-Admin-Token", "tok")
	rec := httptest.NewRecorder()
	e.h.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("list status=%d body=%s", rec.Code, rec.Body.String())
	}
	var listed struct {
		Total   int            `json:"total"`
		ByLabel map[string]int `json:"by_label"`
		Items   []struct {
			ID int64 `json:"id"`
		} `json:"items"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &listed); err != nil {
		t.Fatal(err)
	}
	if listed.Total != 2 || listed.ByLabel["ai_suspect"] != 2 {
		t.Fatalf("unexpected list payload: %s", rec.Body.String())
	}

	body := `{"ids":[` + itoa(listed.Items[0].ID) + `]}`
	req = httptest.NewRequest(http.MethodPost, "/admin/label-feedback/delete", strings.NewReader(body))
	req.Header.Set("X-Admin-Token", "tok")
	rec = httptest.NewRecorder()
	e.h.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("delete status=%d body=%s", rec.Code, rec.Body.String())
	}
	if _, total, _ := e.st.ListLabelFeedback(ctx, "", "", 10, 0); total != 1 {
		t.Fatalf("after delete want 1 row, got %d", total)
	}
}

// 检索结果必须带标签理由，否则「误判」按钮就是让用户盲猜。
func TestSearchReturnsLabelReasons(t *testing.T) {
	e := newTestEnv(t, testOpts{})
	ctx := context.Background()
	if _, err := e.st.UpsertDocument(ctx, store.Document{
		URL: "https://blog.example.com/error", Domain: "blog.example.com",
		Title: "error handling guide", Labels: `["ai_suspect"]`,
		LabelReasons: `{"ai_suspect":"模板化段落占比 82%"}`,
		FetchedAt:    time.Now().Unix(),
	}); err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest(http.MethodGet, "/api/search?q=error&size=5", nil)
	rec := httptest.NewRecorder()
	e.h.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("search status=%d", rec.Code)
	}
	var resp struct {
		Hits []struct {
			URL          string            `json:"url"`
			LabelReasons map[string]string `json:"label_reasons"`
		} `json:"hits"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	var found bool
	for _, h := range resp.Hits {
		if h.URL == "https://blog.example.com/error" {
			found = true
			if h.LabelReasons["ai_suspect"] == "" {
				t.Fatalf("label reason missing for %s", h.URL)
			}
		}
	}
	if !found {
		t.Fatalf("expected doc in hits: %s", rec.Body.String())
	}
}

// itoa 避免为一个断言引入 strconv。
func itoa(v int64) string {
	if v == 0 {
		return "0"
	}
	var buf [20]byte
	i := len(buf)
	for v > 0 {
		i--
		buf[i] = byte('0' + v%10)
		v /= 10
	}
	return string(buf[i:])
}
