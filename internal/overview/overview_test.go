package overview

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func sources() []Source {
	return []Source{
		{ID: 1, Title: "DeepSeek 介绍", Domain: "deepseek.com", URL: "https://deepseek.com/a", Text: "DeepSeek 是一家 AI 公司。"},
		{ID: 2, Title: "DeepSeek 模型", Domain: "example.com", URL: "https://example.com/b", Text: "DeepSeek 发布了大模型。"},
	}
}

func TestGenerateFiltersInvalidCitations(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{"choices": []map[string]any{{
			"finish_reason": "stop",
			"message": map[string]string{
				"content": `{"answer":"DeepSeek 是一家 AI 公司。[1][2]","citations":[1,99,2,2]}`,
			},
		}}})
	}))
	defer srv.Close()

	res, err := Generate(context.Background(), []Endpoint{{Name: "primary", BaseURL: srv.URL, Model: "m"}},
		"deepseek是什么", sources(), 0)
	if err != nil {
		t.Fatal(err)
	}
	if res.Answer == "" || len(res.Citations) != 2 || res.Citations[0] != 1 || res.Citations[1] != 2 {
		t.Fatalf("应只保留有效且去重的引用，got %+v", res)
	}
	if res.Provider != "openai-compat:m" || res.Endpoint != "primary" {
		t.Fatalf("provider/endpoint 不对：%+v", res)
	}
}

func TestGenerateFallsBackToNextEndpoint(t *testing.T) {
	bad := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "down", http.StatusBadGateway)
	}))
	defer bad.Close()
	good := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{"choices": []map[string]any{{
			"finish_reason": "stop",
			"message":       map[string]string{"content": `{"answer":"备用接口的回答","citations":[1]}`},
		}}})
	}))
	defer good.Close()

	res, err := Generate(context.Background(), []Endpoint{
		{Name: "primary", BaseURL: bad.URL, Model: "m1"},
		{Name: "backup", BaseURL: good.URL, Model: "m2"},
	}, "deepseek是什么", sources(), 0)
	if err != nil {
		t.Fatal(err)
	}
	if res.Endpoint != "backup" || res.Answer != "备用接口的回答" {
		t.Fatalf("应回退到备用接口，got %+v", res)
	}
}

// 推理模型把 1024 token 全花在 reasoning 上时，要按 finish_reason=length 放大重试一次。
func TestGenerateRetriesWhenReasoningEatsBudget(t *testing.T) {
	calls := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			MaxTokens int `json:"max_tokens"`
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		calls++
		if body.MaxTokens <= 1024 {
			_ = json.NewEncoder(w).Encode(map[string]any{"choices": []map[string]any{{
				"finish_reason": "length",
				"message":       map[string]string{"content": ""},
			}}})
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"choices": []map[string]any{{
			"finish_reason": "stop",
			"message":       map[string]string{"content": `{"answer":"放大预算后的回答","citations":[1]}`},
		}}})
	}))
	defer srv.Close()

	res, err := Generate(context.Background(), []Endpoint{{Name: "primary", BaseURL: srv.URL, Model: "m"}},
		"deepseek是什么", sources(), 0)
	if err != nil {
		t.Fatal(err)
	}
	if calls != 2 || res.Answer != "放大预算后的回答" {
		t.Fatalf("应重试一次并成功，calls=%d res=%+v", calls, res)
	}
}

func TestGenerateRequiresSourcesAndEndpoints(t *testing.T) {
	if _, err := Generate(context.Background(), []Endpoint{{BaseURL: "http://x", Model: "m"}}, "q", nil, 0); err == nil {
		t.Fatal("没有来源时应报错")
	}
	if _, err := Generate(context.Background(), nil, "q", sources(), 0); err == nil {
		t.Fatal("没有接口时应报错")
	}
}

func TestBuildPromptCarriesSources(t *testing.T) {
	p := buildPrompt("deepseek是什么", sources())
	for _, want := range []string{"deepseek是什么", "[1]", "[2]", "deepseek.com", "DeepSeek 发布了大模型"} {
		if !strings.Contains(p, want) {
			t.Fatalf("提示词缺少 %q：%s", want, p)
		}
	}
}
