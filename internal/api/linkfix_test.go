package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/zealis/stdseek/internal/store"
)

// 「修改」改的是链接，不是反馈记录：标签反馈改页级标签，分类反馈改站点分类。
func TestFixLinkEditsTheLinkNotTheFeedback(t *testing.T) {
	e := newTestEnv(t, testOpts{token: "tok"})
	ctx := context.Background()

	if _, err := e.st.UpsertDocument(ctx, store.Document{
		URL: "https://blog.example.com/a", Domain: "blog.example.com",
		Title: "a", Labels: `["ai_suspect"]`, LabelReasons: `{"ai_suspect":"模板化段落占比 82%"}`,
		FetchedAt: time.Now().Unix(),
	}); err != nil {
		t.Fatal(err)
	}
	if err := e.st.AddWhitelist(ctx, "blog.example.com", "blog", 86400); err != nil {
		t.Fatal(err)
	}
	if err := e.st.UpsertLabelFeedback(ctx, store.LabelFeedback{
		URL: "https://blog.example.com/a", Domain: "blog.example.com",
		Label: "ai_suspect", Verdict: store.VerdictFalsePositive, Kind: store.FeedbackKindLabel,
	}, "c1"); err != nil {
		t.Fatal(err)
	}

	post := func(body string) (int, map[string]any) {
		req := httptest.NewRequest(http.MethodPost, "/admin/feedback/fix", strings.NewReader(body))
		req.Header.Set("X-Admin-Token", "tok")
		rec := httptest.NewRecorder()
		e.h.ServeHTTP(rec, req)
		out := map[string]any{}
		_ = json.Unmarshal(rec.Body.Bytes(), &out)
		return rec.Code, out
	}

	// 改标签：把 ai_suspect 换成 ad_heavy
	if code, _ := post(`{"url":"https://blog.example.com/a","kind":0,"labels":["ad_heavy"]}`); code != http.StatusOK {
		t.Fatalf("改标签应 200，got %d", code)
	}
	doc, _, err := e.st.GetDocument(ctx, "https://blog.example.com/a")
	if err != nil {
		t.Fatal(err)
	}
	if doc.Labels != `["ad_heavy"]` {
		t.Fatalf("链接的标签应被改掉，got %s", doc.Labels)
	}
	if doc.LabelsSource != store.LabelSourceManual {
		t.Fatalf("应锁定为人工标签，got %q", doc.LabelsSource)
	}
	if !strings.Contains(doc.LabelReasons, manualReason) {
		t.Fatalf("人工标签也要有理由（标签必须可解释），got %s", doc.LabelReasons)
	}

	// 反馈记录必须原样不动
	items, _, err := e.st.ListLabelFeedback(ctx, "", "", 10, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 1 || items[0].Label != "ai_suspect" || items[0].Verdict != store.VerdictFalsePositive {
		t.Fatalf("反馈记录不该被改：%+v", items)
	}

	// 手工标签不能被下一轮抓取覆盖
	if _, err := e.st.UpsertDocument(ctx, store.Document{
		URL: "https://blog.example.com/a", Domain: "blog.example.com",
		Title: "a2", Labels: `["ugc_platform"]`, LabelReasons: `{}`,
		FetchedAt: time.Now().Unix(),
	}); err != nil {
		t.Fatal(err)
	}
	doc, _, err = e.st.GetDocument(ctx, "https://blog.example.com/a")
	if err != nil {
		t.Fatal(err)
	}
	if doc.Labels != `["ad_heavy"]` {
		t.Fatalf("重新抓取不该覆盖人工标签，got %s", doc.Labels)
	}
	if doc.Title != "a2" {
		t.Fatalf("标题这类字段仍应更新，got %q", doc.Title)
	}

	// 空数组 = 这一页不该有任何标签
	if code, _ := post(`{"url":"https://blog.example.com/a","kind":0,"labels":[]}`); code != http.StatusOK {
		t.Fatalf("清空标签应 200，got %d", code)
	}
	doc, _, err = e.st.GetDocument(ctx, "https://blog.example.com/a")
	if err != nil {
		t.Fatal(err)
	}
	if doc.Labels != `[]` {
		t.Fatalf("应写成空数组，got %s", doc.Labels)
	}

	// 改分类
	if code, _ := post(`{"url":"https://blog.example.com/a","kind":1,"category":"official"}`); code != http.StatusOK {
		t.Fatalf("改分类应 200，got %d", code)
	}
	sites, _, err := e.st.ListWhitelist(ctx, "blog.example.com", 1, 0)
	if err != nil || len(sites) != 1 {
		t.Fatalf("whitelist 查询失败: %v", err)
	}
	if sites[0].Category != "official" || sites[0].CategorySource != store.CategorySourceManual {
		t.Fatalf("站点分类应改为 official 并锁定，got %q/%q", sites[0].Category, sites[0].CategorySource)
	}
}

// 乱填的要挡住，免得后台改出一个谁也读不懂的标签/分类。
func TestFixLinkValidation(t *testing.T) {
	e := newTestEnv(t, testOpts{token: "tok"})
	ctx := context.Background()
	if _, err := e.st.UpsertDocument(ctx, store.Document{
		URL: "https://docs.example.com/x", Domain: "docs.example.com",
		Title: "x", Labels: `[]`, FetchedAt: time.Now().Unix(),
	}); err != nil {
		t.Fatal(err)
	}
	if err := e.st.AddWhitelist(ctx, "docs.example.com", "docs", 86400); err != nil {
		t.Fatal(err)
	}

	cases := []struct {
		name string
		body string
		want int
	}{
		{"未知标签", `{"url":"https://docs.example.com/x","kind":0,"labels":["spam"]}`, http.StatusBadRequest},
		{"非法分类", `{"url":"https://docs.example.com/x","kind":1,"category":"oficial"}`, http.StatusBadRequest},
		{"空 url", `{"url":"","kind":0,"labels":[]}`, http.StatusBadRequest},
		{"非法 kind", `{"url":"https://docs.example.com/x","kind":7}`, http.StatusBadRequest},
		{"页面不在索引", `{"url":"https://nope.example.com/y","kind":0,"labels":[]}`, http.StatusNotFound},
	}
	for _, c := range cases {
		req := httptest.NewRequest(http.MethodPost, "/admin/feedback/fix", strings.NewReader(c.body))
		req.Header.Set("X-Admin-Token", "tok")
		rec := httptest.NewRecorder()
		e.h.ServeHTTP(rec, req)
		if rec.Code != c.want {
			t.Errorf("%s: status=%d want %d body=%s", c.name, rec.Code, c.want, rec.Body.String())
		}
	}

	// 写操作必须带令牌
	req := httptest.NewRequest(http.MethodPost, "/admin/feedback/fix",
		strings.NewReader(`{"url":"https://docs.example.com/x","kind":0,"labels":[]}`))
	rec := httptest.NewRecorder()
	e.h.ServeHTTP(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("无令牌应 401，got %d", rec.Code)
	}
}

// 列表要带上「链接现状」：改链接的弹窗得先看到这一页现在挂着什么标签。
func TestFeedbackListCarriesLinkState(t *testing.T) {
	e := newTestEnv(t, testOpts{token: "tok"})
	ctx := context.Background()
	if _, err := e.st.UpsertDocument(ctx, store.Document{
		URL: "https://blog.example.com/a", Domain: "blog.example.com",
		Title: "a", Labels: `["ai_suspect","ad_heavy"]`, FetchedAt: time.Now().Unix(),
	}); err != nil {
		t.Fatal(err)
	}
	if err := e.st.AddWhitelist(ctx, "blog.example.com", "blog", 86400); err != nil {
		t.Fatal(err)
	}
	if err := e.st.UpsertLabelFeedback(ctx, store.LabelFeedback{
		URL: "https://blog.example.com/a", Domain: "blog.example.com",
		Label: "ai_suspect", Verdict: store.VerdictFalsePositive, Kind: store.FeedbackKindLabel,
	}, "c1"); err != nil {
		t.Fatal(err)
	}

	req := httptest.NewRequest(http.MethodGet, "/admin/label-feedback?verdict=&kw=", nil)
	req.Header.Set("X-Admin-Token", "tok")
	rec := httptest.NewRecorder()
	e.h.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	var out struct {
		Items []struct {
			PageLabels   []string `json:"page_labels"`
			SiteCategory string   `json:"site_category"`
		} `json:"items"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	if len(out.Items) != 1 {
		t.Fatalf("want 1 item, got %d", len(out.Items))
	}
	if len(out.Items[0].PageLabels) != 2 {
		t.Fatalf("应带出这一页当前的两个标签，got %v", out.Items[0].PageLabels)
	}
	if out.Items[0].SiteCategory != "blog" {
		t.Fatalf("应带出站点当前分类，got %q", out.Items[0].SiteCategory)
	}
}
