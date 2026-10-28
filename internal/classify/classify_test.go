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
