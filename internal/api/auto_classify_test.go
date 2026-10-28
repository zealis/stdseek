package api

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"voidxo/internal/store"
)

// 判定门槛：30 → 300 → 之后每 +3000，且「从未判过」时只要有内容就判。
func TestClassifyDue(t *testing.T) {
	cases := []struct {
		cat          string
		aiDocs, docs int
		want         bool
	}{
		{"blog", 0, 0, false},  // 一页都没有，判了也只是猜
		{"", 0, 1, true},       // 分类为空 + 有内容 = 首判（用户定的规则）
		{"", 0, 5, true},       // 同上，不因内容少而推迟
		{"blog", 0, 1, false},  // 已有分类：先等门槛，不为省事就重猜
		{"blog", 0, 29, false}, // 未判过的站，第一道门槛就是 30
		{"blog", 0, 30, true},
		{"blog", 30, 299, false}, // 判过 30 篇，等 300
		{"blog", 30, 300, true},
		{"blog", 300, 3299, false}, // 之后每 +3000
		{"blog", 300, 3300, true},
		{"blog", 3300, 6300, true}, // 游标随判定推进，不会重复命中
		{"blog", 3300, 6299, false},
	}
	for _, c := range cases {
		if got := classifyDue(c.cat, c.aiDocs, c.docs); got != c.want {
			t.Fatalf("classifyDue(%q,%d,%d)=%v want %v", c.cat, c.aiDocs, c.docs, got, c.want)
		}
	}
	// 门槛必须严格递增，否则会反复命中同一个站（等于每轮抓取都烧一次模型调用）。
	for _, docs := range []int{0, 25, 300, 3300, 9000, 30000} {
		if next := nextClassifyDocs(docs); next <= docs {
			t.Fatalf("nextClassifyDocs(%d)=%d 必须大于当前值", docs, next)
		}
	}
}

// 自动判定：只判到点的站；手改过的分类不覆盖；判完写游标防止反复判。
func TestAutoClassifyHonoursThresholdsAndManual(t *testing.T) {
	e := newTestEnv(t, testOpts{})
	ctx := context.Background()

	var calls int32
	model := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&calls, 1)
		json.NewEncoder(w).Encode(map[string]any{
			"choices": []map[string]any{{
				"finish_reason": "stop",
				"message":       map[string]string{"content": `{"category":"dev","labels":["personal"]}`},
			}},
		})
	}))
	defer model.Close()

	for k, v := range map[string]string{
		"ai.base_url":         model.URL,
		"ai.model":            "stub",
		"feature.ai_labeling": "true",
	} {
		if err := e.st.SetSetting(ctx, k, v); err != nil {
			t.Fatal(err)
		}
	}

	// from 是起始序号：同一个站分两批灌数据时，URL 不能重复（UNIQUE 会把新行
	// 当成更新，文档数永远涨不上去——测试里踩过一次）。
	seedDocs := func(domain string, from, n int) {
		for i := from; i < from+n; i++ {
			if _, err := e.st.UpsertDocument(ctx, store.Document{
				URL: fmt.Sprintf("https://%s/p/%d", domain, i), Domain: domain,
				Title: fmt.Sprintf("%s 的第 %d 篇文章", domain, i), Description: "desc",
				Labels: "[]", LabelReasons: "{}", WordCount: 900, FetchedAt: time.Now().Unix(),
			}); err != nil {
				t.Fatal(err)
			}
		}
	}
	addSite := func(domain, category string) int64 {
		if err := e.st.AddWhitelist(ctx, domain, category, 86400); err != nil {
			t.Fatal(err)
		}
		sites, _, err := e.st.ListWhitelist(ctx, domain, 10, 0)
		if err != nil || len(sites) != 1 {
			t.Fatalf("load %s: %v %d", domain, err, len(sites))
		}
		return sites[0].ID
	}

	// A：判过 5 篇，现有 5 篇 → 还没到 30，不判
	aID := addSite("a.example", "blog")
	seedDocs("a.example", 0, 5)
	if err := e.st.ApplySiteClassify(ctx, aID, "blog", nil, 5); err != nil {
		t.Fatal(err)
	}
	// B：管理员手改过分类，300 篇也不许自动改
	bID := addSite("b.example", "blog")
	seedDocs("b.example", 0, 300)
	if err := e.st.SetWhitelistCategory(ctx, bID, "official"); err != nil {
		t.Fatal(err)
	}
	// C：从未判过就已经有 30 篇 → 首次到门槛，该判
	_ = addSite("c.example", "blog")
	seedDocs("c.example", 0, 30)
	// D：只有 1 篇且已有分类 → 不到门槛，不判
	_ = addSite("d.example", "blog")
	seedDocs("d.example", 0, 1)

	n, err := e.srv.AutoClassify(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Fatalf("should judge only c.example, got %d (calls=%d)", n, atomic.LoadInt32(&calls))
	}
	if got := atomic.LoadInt32(&calls); got != 1 {
		t.Fatalf("model called %d times, want 1", got)
	}

	sites, _, err := e.st.ListWhitelist(ctx, "", 10, 0)
	if err != nil {
		t.Fatal(err)
	}
	byDomain := map[string]store.WhitelistSite{}
	for _, s := range sites {
		byDomain[s.Domain] = s
	}
	if s := byDomain["c.example"]; s.Category != "dev" || s.CategorySource != store.CategorySourceAI || s.AIClassifiedDocs != 30 {
		t.Fatalf("c.example 应被判定并写游标，got %+v", s)
	}
	if s := byDomain["d.example"]; s.CategorySource == store.CategorySourceAI {
		t.Fatalf("不到门槛的站不该被判，got %+v", s)
	}
	if len(byDomain["c.example"].Labels) != 1 || byDomain["c.example"].Labels[0] != "personal" {
		t.Fatalf("站点级标签应写回，got %v", byDomain["c.example"].Labels)
	}
	if s := byDomain["b.example"]; s.Category != "official" || s.CategorySource != store.CategorySourceManual {
		t.Fatalf("手改的分类不能被自动判定覆盖，got %+v", s)
	}
	if s := byDomain["a.example"]; s.AIClassifiedDocs != 5 {
		t.Fatalf("未到门槛的站不该被写游标，got %+v", s)
	}

	// A 涨到 30 篇：这一轮该判它
	seedDocs("a.example", 5, 25)
	if n, err := e.srv.AutoClassify(ctx, nil); err != nil || n != 1 {
		t.Fatalf("A 涨到 30 篇后应判 1 个，got n=%d err=%v", n, err)
	}
	sites, _, _ = e.st.ListWhitelist(ctx, "a.example", 10, 0)
	if sites[0].AIClassifiedDocs != 30 {
		t.Fatalf("游标应更新为 30，got %d", sites[0].AIClassifiedDocs)
	}

	// 再跑一次：没人到点，不该再调模型
	before := atomic.LoadInt32(&calls)
	if n, err := e.srv.AutoClassify(ctx, nil); err != nil || n != 0 {
		t.Fatalf("second run should judge nothing, got n=%d err=%v", n, err)
	}
	if got := atomic.LoadInt32(&calls); got != before {
		t.Fatalf("空闲轮不该调用模型：%d -> %d", before, got)
	}
}

// 开关关着时自动判定必须彻底安静（不能悄悄烧模型调用）。
func TestAutoClassifySkipsWhenSwitchOff(t *testing.T) {
	e := newTestEnv(t, testOpts{})
	ctx := context.Background()

	var calls int32
	model := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&calls, 1)
		json.NewEncoder(w).Encode(map[string]any{"choices": []map[string]any{{"message": map[string]string{"content": `{"category":"dev"}`}}}})
	}))
	defer model.Close()
	_ = e.st.SetSetting(ctx, "ai.base_url", model.URL)
	_ = e.st.SetSetting(ctx, "ai.model", "stub")
	_ = e.st.SetSetting(ctx, "feature.ai_labeling", "false")

	if _, err := e.st.UpsertDocument(ctx, store.Document{
		URL: "https://go.dev/x", Domain: "go.dev", Title: "x", Labels: "[]",
		LabelReasons: "{}", WordCount: 500, FetchedAt: time.Now().Unix(),
	}); err != nil {
		t.Fatal(err)
	}
	if n, err := e.srv.AutoClassify(ctx, nil); err != nil || n != 0 {
		t.Fatalf("switch off should judge nothing, got n=%d err=%v", n, err)
	}
	if atomic.LoadInt32(&calls) != 0 {
		t.Fatal("开关关着时不该调用模型")
	}
}

// 白名单按分类筛选：结果只含该分类，且带回各分类计数（筛选下拉要用）。
func TestWhitelistCategoryFilter(t *testing.T) {
	h, _ := newTestServerWithToken(t, "tok")
	req := httptest.NewRequest(http.MethodGet, "/admin/whitelist?cat=blog", nil)
	req.Header.Set("X-Admin-Token", "tok")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	var out struct {
		Total  int                   `json:"total"`
		Items  []store.WhitelistSite `json:"items"`
		Counts map[string]int        `json:"cat_counts"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	if out.Total != 1 || len(out.Items) != 1 || out.Items[0].Domain != "blog.example.com" {
		t.Fatalf("cat=blog 应只返回 blog.example.com，got %+v", out)
	}
	if out.Counts["blog"] != 1 || out.Counts["official"] != 1 {
		t.Fatalf("分类计数不对：%+v", out.Counts)
	}
	// 未知分类要返回空列表而不是 400：筛选值来自 UI，用户手改 URL 不该报错
	req = httptest.NewRequest(http.MethodGet, "/admin/whitelist?cat=nope", nil)
	req.Header.Set("X-Admin-Token", "tok")
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("unknown cat status=%d", rec.Code)
	}
}
