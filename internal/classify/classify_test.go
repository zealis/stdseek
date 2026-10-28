package classify

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"voidxo/internal/sitecat"
)

// stubModel 起一个假的 /chat/completions，按调用顺序返回预设响应。
// 每个响应是一个 choices[0] 的片段（finish_reason + content）。
func stubModel(t *testing.T, replies []struct {
	finish  string
	content string
}) (*Client, *[]int) {
	t.Helper()
	var (
		calls     int32
		maxTokens []int
	)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		var body struct {
			MaxTokens int `json:"max_tokens"`
		}
		_ = json.Unmarshal(raw, &body)
		maxTokens = append(maxTokens, body.MaxTokens)
		i := int(atomic.AddInt32(&calls, 1)) - 1
		if i >= len(replies) {
			i = len(replies) - 1
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"choices": []map[string]any{{
				"finish_reason": replies[i].finish,
				"message":       map[string]string{"content": replies[i].content},
			}},
		})
	}))
	t.Cleanup(srv.Close)
	return New(Config{BaseURL: srv.URL, Model: "test-model", Timeout: 5 * time.Second}), &maxTokens
}

// 推理模型把预算吃在思考上时：第一轮空正文 + length，要自动放大预算重试一次。
func TestSiteRetriesWhenReasoningEatsBudget(t *testing.T) {
	c, tokens := stubModel(t, []struct {
		finish  string
		content string
	}{
		{"length", ""},
		{"stop", `{"category":"dev","labels":["ugc_platform","bogus"]}`},
	})

	got, err := c.Site(context.Background(), Input{Domain: "github.com"})
	if err != nil {
		t.Fatalf("retry should succeed, got %v", err)
	}
	if got.Category != "dev" {
		t.Fatalf("category=%q want dev", got.Category)
	}
	// 词表外的标签必须被丢掉，只留合法的那个
	if len(got.Labels) != 1 || got.Labels[0] != "ugc_platform" {
		t.Fatalf("labels=%v want [ugc_platform]", got.Labels)
	}
	if len(*tokens) != 2 {
		t.Fatalf("want 2 calls, got %d", len(*tokens))
	}
	// 回归防护：第一轮不能再是 64 —— 那是「所有站点被判成其他」的根因
	if (*tokens)[0] != classifyMaxTokens || (*tokens)[1] != classifyRetryTokens {
		t.Fatalf("token budgets = %v, want [%d %d]", *tokens, classifyMaxTokens, classifyRetryTokens)
	}
	if classifyMaxTokens < 256 {
		t.Fatal("推理模型下 64 token 的预算不够，别再调回去")
	}
}

// 一直拿不到正文时要报错，不能静默落成「其他」。
func TestSiteErrorsOnEmptyContent(t *testing.T) {
	c, _ := stubModel(t, []struct {
		finish  string
		content string
	}{{"length", ""}})

	_, err := c.Site(context.Background(), Input{Domain: "example.com"})
	if err == nil {
		t.Fatal("empty content must be an error")
	}
	if !strings.Contains(err.Error(), "没有返回正文") {
		t.Fatalf("unexpected error: %v", err)
	}
}

// 正常返回：解析出分类与标签。
func TestSiteParsesRewrittenJSON(t *testing.T) {
	c, _ := stubModel(t, []struct {
		finish  string
		content string
	}{{"stop", "```json\n{\"category\":\"official\",\"labels\":[]}\n```"}})

	got, err := c.Site(context.Background(), Input{Domain: "go.dev"})
	if err != nil {
		t.Fatal(err)
	}
	if got.Category != "official" {
		t.Fatalf("category=%q want official", got.Category)
	}
	if len(got.Labels) != 0 {
		t.Fatalf("labels=%v want empty", got.Labels)
	}
}

// 词表外的分类落「其他」，不报错（这是既定语义：判不出来就承认判不出来）。
func TestSiteUnknownCategoryFallsBackToOther(t *testing.T) {
	c, _ := stubModel(t, []struct {
		finish  string
		content string
	}{{"stop", `{"category":"not_a_real_key","labels":[]}`}})

	got, err := c.Site(context.Background(), Input{Domain: "x.example"})
	if err != nil {
		t.Fatal(err)
	}
	if got.Category != "other" {
		t.Fatalf("category=%q want other", got.Category)
	}
}

// 语料必须完整进提示词：漏了「其他」分类或某个标签，模型就会自己发明 key
// （编造的 key 会被 parseLabels 丢掉，等于那个标签永远打不上）。
func TestBuildPromptCarriesCorpus(t *testing.T) {
	in := Input{
		Domain:      "ruanyifeng.com",
		Name:        "阮一峰的个人网站",
		Description: "科技爱好者周刊，每周五发布",
		Titles:      []string{"科技爱好者周刊（第 320 期）", "TypeScript 教程"},
		Excerpts:    []string{"本周话题：为什么 AI 生成的代码需要人读一遍。正文讲了三件事。"},
		Signals: Signals{
			Docs: 120, MedianWords: 1800, UserPathPages: 0, ArticlePages: 118,
			AdTagged: 2, AITagged: 1,
		},
	}
	p := buildPrompt(in)
	for _, want := range []string{
		"ruanyifeng.com", "阮一峰的个人网站", "科技爱好者周刊（第 320 期）",
		"本周话题", "120", "1800",
		"other",                                              // 「其他」分类必须给模型看到
		"ai_suspect", "ad_heavy", "personal", "ugc_platform", // 四个标签一个不能少
		"ad_tagged 占已入库页面的比例高", // 阈值判据要写清
	} {
		if !strings.Contains(p, want) {
			t.Fatalf("prompt 缺少 %q\n---\n%s", want, p)
		}
	}
	// 摘录要限长：3 段各 300 字，超出的部分不该继续塞进提示词。
	long := strings.Repeat("字", 900)
	p2 := buildPrompt(Input{Domain: "x.com", Excerpts: []string{long}})
	if strings.Count(p2, "字") > excerptRunes+10 {
		t.Fatalf("摘录没有截断，提示词长度失控：%d 字", strings.Count(p2, "字"))
	}
}

// 空语料也要能拼出提示词：一个刚入库的站只有域名，不该让批量判定整体失败。
func TestBuildPromptEmptyCorpus(t *testing.T) {
	p := buildPrompt(Input{Domain: "new.example"})
	if !strings.Contains(p, "（暂无）") || !strings.Contains(p, "暂无正文") {
		t.Fatalf("空语料提示词缺少占位说明：\n%s", p)
	}
	if !strings.Contains(p, "已入库页面 0 篇") {
		t.Fatalf("信号应当如实给 0，而不是省略：\n%s", p)
	}
}

// 主接口挂了要自动退到备用，并把「这次用的是谁」回传给调用方——
// 备用悄悄顶上而不告诉管理员，等于把故障静音了。
func TestSiteFallsBackToBackupEndpoint(t *testing.T) {
	bad := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "upstream exploded", http.StatusBadGateway)
	}))
	defer bad.Close()
	good := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(map[string]any{"choices": []map[string]any{{
			"finish_reason": "stop",
			"message":       map[string]string{"content": `{"category":"dev","labels":["personal"]}`},
		}}})
	}))
	defer good.Close()

	c := New(Config{
		Endpoints: []Endpoint{
			{Name: "primary", BaseURL: bad.URL, Model: "m1"},
			{Name: "backup", BaseURL: good.URL, Model: "m2"},
		},
		Timeout: 5 * time.Second,
	})
	if !c.Ready() {
		t.Fatal("有两个可用接口时就该 Ready")
	}
	got, err := c.Site(context.Background(), Input{Domain: "x.com"})
	if err != nil {
		t.Fatalf("备用接口应该兜住：%v", err)
	}
	if got.Endpoint != "backup" {
		t.Fatalf("应回传用的是备用接口，got %q", got.Endpoint)
	}
	if got.Category != "dev" || len(got.Labels) != 1 {
		t.Fatalf("判定结果应来自备用接口，got %+v", got)
	}

	// 全部挂掉：要报错，且不能谎报用了哪个接口
	c2 := New(Config{Endpoints: []Endpoint{{Name: "primary", BaseURL: bad.URL, Model: "m1"}}, Timeout: 5 * time.Second})
	if _, err := c2.Site(context.Background(), Input{Domain: "x.com"}); err == nil {
		t.Fatal("接口全挂时应返回错误")
	}
}

// 模型答非所问不算接口故障：换供应商多半还是一样，不该白白切走。
func TestSiteDoesNotFailoverOnBadAnswer(t *testing.T) {
	var calls int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&calls, 1)
		json.NewEncoder(w).Encode(map[string]any{"choices": []map[string]any{{
			"finish_reason": "stop",
			"message":       map[string]string{"content": "我不知道"},
		}}})
	}))
	defer srv.Close()
	backup := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Error("模型答得不对不该去试备用接口")
	}))
	defer backup.Close()

	c := New(Config{Endpoints: []Endpoint{
		{Name: "primary", BaseURL: srv.URL, Model: "m1"},
		{Name: "backup", BaseURL: backup.URL, Model: "m2"},
	}, Timeout: 5 * time.Second})
	got, err := c.Site(context.Background(), Input{Domain: "x.com"})
	if err != nil {
		t.Fatal(err)
	}
	if got.Endpoint != "primary" || got.Category != sitecat.Other {
		t.Fatalf("答非所问应留在主接口并落「其他」，got %+v", got)
	}
	if atomic.LoadInt32(&calls) != 1 {
		t.Fatalf("主接口只该被调一次，got %d", calls)
	}
}
