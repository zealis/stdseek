package api

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/zealis/stdseek/internal/store"
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

// 没有标签的页面也要能反馈：分类判错了跟标签毫无关系，
// 而「不是所有站点都被打了标签」是常态，不是异常。
func TestCategoryFeedbackWithoutLabels(t *testing.T) {
	e := newTestEnv(t, testOpts{token: "tok"})
	ctx := context.Background()

	// 页面本身一个标签都没有（labels 为空数组）
	if _, err := e.st.UpsertDocument(ctx, store.Document{
		URL: "https://docs.example.com/guide", Domain: "docs.example.com",
		Title: "guide", Labels: `[]`, FetchedAt: time.Now().Unix(),
	}); err != nil {
		t.Fatal(err)
	}
	// 站点分类：分类反馈要记下「当时判成了什么」
	if err := e.st.AddWhitelist(ctx, "docs.example.com", "blog", 86400); err != nil {
		t.Fatal(err)
	}

	post := func(body string) int {
		req := httptest.NewRequest(http.MethodPost, "/api/label-feedback", strings.NewReader(body))
		req.RemoteAddr = "198.51.100.7:1234"
		rec := httptest.NewRecorder()
		e.h.ServeHTTP(rec, req)
		return rec.Code
	}

	// 只提分类、不提标签
	code := post(`{"url":"https://docs.example.com/guide","category":{"verdict":"false_positive","suggest":"official"}}`)
	if code != http.StatusOK {
		t.Fatalf("只提分类应该成功，got %d", code)
	}
	// 光有 URL 什么都没表态 → 400
	if got := post(`{"url":"https://docs.example.com/guide"}`); got != http.StatusBadRequest {
		t.Fatalf("既不提标签也不提分类应 400，got %d", got)
	}
	// 建议分类不在词表 → 400（打错一个字母会造出一条永远读不懂的反馈）
	if got := post(`{"url":"https://docs.example.com/guide","category":{"verdict":"false_positive","suggest":"oficial"}}`); got != http.StatusBadRequest {
		t.Fatalf("建议分类不在词表应 400，got %d", got)
	}

	items, total, err := e.st.ListLabelFeedback(ctx, "", "", 10, 0)
	if err != nil {
		t.Fatal(err)
	}
	if total != 1 {
		t.Fatalf("分类反馈应只有 1 行，got %d", total)
	}
	it := items[0]
	if it.Kind != store.FeedbackKindCategory {
		t.Fatalf("kind 应为 1，got %d", it.Kind)
	}
	if it.Label != "" {
		t.Fatalf("分类反馈的 label 应为空，got %q", it.Label)
	}
	if it.Category != "blog" {
		t.Fatalf("应记下当时的站点分类 blog，got %q", it.Category)
	}
	if it.Suggest != "official" {
		t.Fatalf("应记下用户建议的分类，got %q", it.Suggest)
	}

	// 误判统计只数标签行：分类不是标签，混在一起数等于把两种问题当成一种
	counts, err := e.st.LabelFalsePositiveCounts(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(counts) != 0 {
		t.Fatalf("分类误判不该计入标签误判率：%v", counts)
	}
}

// 标签与分类可以在同一个请求里提，因为人机验证的 token 是一次性的——
// 拆成两个请求时第二条必定失败。
func TestLabelAndCategoryFeedbackInOneRequest(t *testing.T) {
	e := newTestEnv(t, testOpts{})
	ctx := context.Background()
	if _, err := e.st.UpsertDocument(ctx, store.Document{
		URL: "https://blog.example.com/error", Domain: "blog.example.com",
		Title: "t", Labels: `["ai_suspect","ad_heavy"]`, FetchedAt: time.Now().Unix(),
	}); err != nil {
		t.Fatal(err)
	}
	if err := e.st.AddWhitelist(ctx, "blog.example.com", "blog", 86400); err != nil {
		t.Fatal(err)
	}

	req := httptest.NewRequest(http.MethodPost, "/api/label-feedback", strings.NewReader(
		`{"url":"https://blog.example.com/error","labels":["ai_suspect"],"verdict":"false_positive",`+
			`"category":{"verdict":"false_positive","suggest":"official"},"note":"这是官方文档站"}`))
	req.RemoteAddr = "203.0.113.5:5555"
	rec := httptest.NewRecorder()
	e.h.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}

	items, total, err := e.st.ListLabelFeedback(ctx, "", "", 10, 0)
	if err != nil {
		t.Fatal(err)
	}
	if total != 2 {
		t.Fatalf("应写入 1 条标签反馈 + 1 条分类反馈，got %d", total)
	}
	var kinds []int
	for _, it := range items {
		kinds = append(kinds, it.Kind)
		if it.Note != "这是官方文档站" {
			t.Fatalf("备注应两行都带上，got %q", it.Note)
		}
	}
	if !(kinds[0]+kinds[1] == 1) {
		t.Fatalf("两种反馈各一条，got %v", kinds)
	}
}

// 后台改判：类型由反馈本身决定，标签反馈永远只改标签，不会被要求填分类。
func TestAdminEditFeedbackByKind(t *testing.T) {
	e := newTestEnv(t, testOpts{token: "tok"})
	ctx := context.Background()

	// 一条标签反馈 + 一条分类反馈
	if err := e.st.UpsertLabelFeedback(ctx, store.LabelFeedback{
		URL: "https://a.example.com/x", Domain: "a.example.com",
		Label: "ai_suspect", Verdict: store.VerdictFalsePositive,
		Kind: store.FeedbackKindLabel,
	}, "c1"); err != nil {
		t.Fatal(err)
	}
	if err := e.st.UpsertLabelFeedback(ctx, store.LabelFeedback{
		URL: "https://b.example.com/y", Domain: "b.example.com",
		Verdict: store.VerdictFalsePositive, Kind: store.FeedbackKindCategory,
		Category: "blog", Suggest: "official",
	}, "c2"); err != nil {
		t.Fatal(err)
	}
	items, _, err := e.st.ListLabelFeedback(ctx, "", "", 10, 0)
	if err != nil {
		t.Fatal(err)
	}
	var labelID, catID int64
	for _, it := range items {
		if it.Kind == store.FeedbackKindLabel {
			labelID = it.ID
		} else {
			catID = it.ID
		}
	}

	post := func(body string) int {
		req := httptest.NewRequest(http.MethodPost, "/admin/label-feedback/update", strings.NewReader(body))
		req.Header.Set("X-Admin-Token", "tok")
		rec := httptest.NewRecorder()
		e.h.ServeHTTP(rec, req)
		return rec.Code
	}

	// 标签反馈：只认标签，不需要也不接受分类
	if got := post(fmt.Sprintf(`{"id":%d,"kind":0,"label":"ad_heavy","verdict":"confirmed"}`, labelID)); got != http.StatusOK {
		t.Fatalf("改标签应该成功，got %d", got)
	}
	if got := post(fmt.Sprintf(`{"id":%d,"kind":0,"label":"","verdict":"confirmed"}`, labelID)); got != http.StatusBadRequest {
		t.Fatalf("标签留空又没有 no_label 应 400（不知道该记哪个标签），got %d", got)
	}
	// 标签选「无」：该页不该有标签，label 保留用户报的那个用于统计
	if got := post(fmt.Sprintf(`{"id":%d,"kind":0,"label":"ai_suspect","no_label":true,"verdict":"confirmed"}`, labelID)); got != http.StatusOK {
		t.Fatalf("标为无应该成功，got %d", got)
	}
	{
		items, _, err := e.st.ListLabelFeedback(ctx, "", "", 10, 0)
		if err != nil {
			t.Fatal(err)
		}
		for _, it := range items {
			if it.ID != labelID {
				continue
			}
			if !it.NoLabel {
				t.Fatal("no_label 应写入")
			}
			if it.Label != "ai_suspect" {
				t.Fatalf("原标签要留着用于统计，got %q", it.Label)
			}
			// 「不该有标签」与「判定正确」矛盾，服务端必须钉死为误判
			if it.Verdict != store.VerdictFalsePositive {
				t.Fatalf("no_label 时结论应被钉成误判，got %q", it.Verdict)
			}
		}
	}
	if got := post(fmt.Sprintf(`{"id":%d,"kind":0,"label":"nope","verdict":"confirmed"}`, labelID)); got != http.StatusBadRequest {
		t.Fatalf("未知标签应 400，got %d", got)
	}
	// 即使带了分类字段，标签行也不该留下分类信息
	if got := post(fmt.Sprintf(`{"id":%d,"kind":0,"label":"personal","category":"blog","verdict":"confirmed"}`, labelID)); got != http.StatusOK {
		t.Fatalf("改标签应该成功，got %d", got)
	}

	// 分类反馈：分类必填且在词表内，建议可空
	if got := post(fmt.Sprintf(`{"id":%d,"kind":1,"category":"","verdict":"false_positive"}`, catID)); got != http.StatusBadRequest {
		t.Fatalf("分类留空应 400，got %d", got)
	}
	if got := post(fmt.Sprintf(`{"id":%d,"kind":1,"category":"oficial","verdict":"false_positive"}`, catID)); got != http.StatusBadRequest {
		t.Fatalf("分类不在词表应 400，got %d", got)
	}
	if got := post(fmt.Sprintf(`{"id":%d,"kind":1,"category":"media","suggest":"","verdict":"false_positive"}`, catID)); got != http.StatusOK {
		t.Fatalf("改分类应该成功，got %d", got)
	}
	// kind 只能 0 / 1
	if got := post(fmt.Sprintf(`{"id":%d,"kind":7,"label":"personal","verdict":"confirmed"}`, labelID)); got != http.StatusBadRequest {
		t.Fatalf("非法 kind 应 400，got %d", got)
	}

	items, _, err = e.st.ListLabelFeedback(ctx, "", "", 10, 0)
	if err != nil {
		t.Fatal(err)
	}
	for _, it := range items {
		switch it.ID {
		case labelID:
			if it.Kind != store.FeedbackKindLabel || it.Label != "personal" {
				t.Fatalf("标签行应保持标签并更新，got kind=%d label=%q", it.Kind, it.Label)
			}
			if it.Category != "" || it.Suggest != "" {
				t.Fatalf("标签行不该留下分类信息，got category=%q suggest=%q", it.Category, it.Suggest)
			}
		case catID:
			if it.Kind != store.FeedbackKindCategory || it.Label != "" {
				t.Fatalf("分类行应保持分类，got kind=%d label=%q", it.Kind, it.Label)
			}
			if it.Category != "media" || it.Suggest != "" {
				t.Fatalf("分类应更新，got category=%q suggest=%q", it.Category, it.Suggest)
			}
		}
	}
}

// 漏报（should）：用户认为该页面应该有的标签落 false_negative 行；
// 与误报同交、与页面已有标签交集都是矛盾提交，直接 400；不计入误判率。
func TestLabelFeedbackShould(t *testing.T) {
	e := newTestEnv(t, testOpts{})
	ctx := context.Background()
	if _, err := e.st.UpsertDocument(ctx, store.Document{
		URL: "https://blog.example.com/error", Domain: "blog.example.com",
		Title: "t", Labels: `["ai_suspect"]`, FetchedAt: time.Now().Unix(),
	}); err != nil {
		t.Fatal(err)
	}
	post := func(body string) int {
		req := httptest.NewRequest(http.MethodPost, "/api/label-feedback", strings.NewReader(body))
		req.RemoteAddr = "203.0.113.9:4444"
		rec := httptest.NewRecorder()
		e.h.ServeHTTP(rec, req)
		return rec.Code
	}
	if code := post(`{"url":"https://blog.example.com/error","should":["ugc_platform"]}`); code != http.StatusOK {
		t.Fatalf("should status=%d body=%s", code, "want 200")
	}
	items, _, err := e.st.ListLabelFeedback(ctx, "", store.VerdictFalseNegative, 10, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 1 || items[0].Label != "ugc_platform" || items[0].Verdict != store.VerdictFalseNegative {
		t.Fatalf("unexpected false_negative rows: %+v", items)
	}
	for name, body := range map[string]string{
		"unknown label":     `{"url":"https://blog.example.com/error","should":["spam"]}`,
		"clash with labels": `{"url":"https://blog.example.com/error","labels":["ai_suspect"],"verdict":"false_positive","should":["ai_suspect"]}`,
		"already on page":   `{"url":"https://blog.example.com/error","should":["ai_suspect"]}`,
	} {
		if code := post(body); code != http.StatusBadRequest {
			t.Errorf("%s: status=%d want 400", name, code)
		}
	}
	counts, err := e.st.LabelFalsePositiveCounts(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if counts["ugc_platform"] != 0 {
		t.Fatalf("false_negative must not count as false positive: %v", counts)
	}
}
