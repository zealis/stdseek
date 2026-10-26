package summarize

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// OpenAICompat 走 OpenAI 兼容的 /chat/completions 接口。
//
// 选这个协议而不是绑死某家：OpenAI、DeepSeek、Moonshot、通义（兼容模式）、
// 本地 vLLM / llama.cpp / ollama（/v1 兼容端点）都能直接用，换供应商只改 base_url。
type OpenAICompat struct {
	BaseURL string // 例：https://api.deepseek.com/v1（不要带 /chat/completions）
	APIKey  string
	Model   string
	Timeout time.Duration
}

func (o *OpenAICompat) Name() string { return "openai-compat:" + o.Model }

// Config 摘要 Provider 的装配配置。
type Config struct {
	BaseURL string
	APIKey  string
	Model   string
	Timeout time.Duration
}

// NewProvider 按配置选择实现：没配 BaseURL/Model 就用抽取式。
//
// 默认不联网是刻意的——摘要功能能在"零配置、零成本、不联网"的状态下跑通，
// 用户才不会因为"还要去申请一个 API Key"而放弃试用。
func NewProvider(c Config) Provider {
	if c.BaseURL == "" || c.Model == "" {
		return &Extractive{}
	}
	return &OpenAICompat{BaseURL: c.BaseURL, APIKey: c.APIKey, Model: c.Model, Timeout: c.Timeout}
}

const summaryPrompt = `你是搜索结果页的证据摘要器。只依据下面编号的原文段落回答用户查询。

规则：
1. 每条结论必须标注来源编号（citations 数组），不得引用编号之外的任何内容。
2. 段落里没有答案时 conclusions 返回空数组——不要用常识补充，不要猜测。
3. 结论用陈述句，一句话说清一件事，不超过 60 字；不要写"本文""该页面""作者认为"这类水词。
4. 最多 4 条结论；结论之间不要重复同一件事。
5. 只输出 JSON，不要 markdown 代码块，不要解释。

输出格式：
{"conclusions":[{"text":"结论","citations":[1,2]}]}

用户查询：%s
文档标题：%s

原文段落：
%s`

func (o *OpenAICompat) Summarize(ctx context.Context, in Input) ([]Conclusion, error) {
	if o.BaseURL == "" || o.Model == "" {
		return nil, fmt.Errorf("模型未配置")
	}
	prompt := fmt.Sprintf(summaryPrompt, in.Query, in.Title, renderPassages(in.Passages))
	body := map[string]interface{}{
		"model": o.Model,
		"messages": []map[string]string{
			{"role": "user", "content": prompt},
		},
		"temperature": 0.2, // 摘要要稳，不要发挥
		"max_tokens":  600,
	}
	buf, err := json.Marshal(body)
	if err != nil {
		return nil, err
	}
	url := strings.TrimSuffix(o.BaseURL, "/") + "/chat/completions"
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(buf))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	if o.APIKey != "" {
		req.Header.Set("Authorization", "Bearer "+o.APIKey)
	}
	timeout := o.Timeout
	if timeout <= 0 {
		timeout = 20 * time.Second
	}
	client := &http.Client{Timeout: timeout}
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("模型返回 %d：%s", resp.StatusCode, clipErr(raw))
	}

	var out struct {
		Choices []struct {
			Message struct {
				Content string `json:"content"`
			} `json:"message"`
		} `json:"choices"`
	}
	if err := json.Unmarshal(raw, &out); err != nil {
		return nil, fmt.Errorf("解析响应失败：%w", err)
	}
	if len(out.Choices) == 0 {
		return nil, fmt.Errorf("模型没有返回内容")
	}
	content := strings.TrimSpace(out.Choices[0].Message.Content)

	var parsed struct {
		Conclusions []struct {
			Text      string `json:"text"`
			Citations []int  `json:"citations"`
		} `json:"conclusions"`
	}
	if err := json.Unmarshal([]byte(stripCodeFence(content)), &parsed); err != nil {
		return nil, fmt.Errorf("模型输出不是合法 JSON")
	}
	cs := make([]Conclusion, 0, len(parsed.Conclusions))
	for _, c := range parsed.Conclusions {
		cites := make([]Citation, 0, len(c.Citations))
		for _, id := range c.Citations {
			cites = append(cites, Citation{ID: id}) // Text 由 verify 按编号回填
		}
		cs = append(cs, Conclusion{Text: c.Text, Citations: cites})
	}
	return cs, nil
}

// renderPassages 把编号段落拼成提示词里的清单。
func renderPassages(ps []Passage) string {
	var b strings.Builder
	for _, p := range ps {
		fmt.Fprintf(&b, "[%d] %s\n", p.ID, p.Text)
	}
	return b.String()
}

// stripCodeFence 有些模型即使被要求"只输出 JSON"也会包一层 ```json。
// 容忍它比报错重试便宜——重试一次的成本比剥个围栏高得多。
func stripCodeFence(s string) string {
	if !strings.HasPrefix(s, "```") {
		return s
	}
	if i := strings.Index(s, "\n"); i >= 0 {
		s = s[i+1:]
	}
	if i := strings.LastIndex(s, "```"); i >= 0 {
		s = s[:i]
	}
	return strings.TrimSpace(s)
}

func clipErr(b []byte) string {
	s := strings.TrimSpace(string(b))
	r := []rune(s)
	if len(r) > 200 {
		return string(r[:200]) + "…"
	}
	return s
}
