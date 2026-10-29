// Package overview 生成搜索页顶部的 AI 概览。
//
// 与 summarize 的区别：summary 只看一篇页面的正文，回答「这一页能提供什么证据」；
// overview 看的是本次搜索的前几条结果，把多篇材料合成一段回答。
// 两者都要求「只依据给定材料」，但 overview 的引用是结果编号，不是段落编号。
package overview

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

// Endpoint 一个 OpenAI 兼容接口。Name 只用于日志与失败原因（primary/backup/…）。
type Endpoint struct {
	Name    string
	BaseURL string
	APIKey  string
	Model   string
}

// Source 一条搜索结果，编号从 1 开始供模型引用。
type Source struct {
	ID     int
	Title  string
	URL    string
	Domain string
	Text   string
}

// Result 概览结果。Citations 已过滤到有效来源编号。
type Result struct {
	Answer    string
	Citations []int
	Provider  string
	Endpoint  string
}

// Generate 按顺序尝试接口，第一个成功即返回；全部失败才返回错误（没有抽取式兜底——
// 概览是从多篇材料合成的新文本，无法用「摘一句原文」冒充）。
func Generate(ctx context.Context, eps []Endpoint, query string, sources []Source, timeout time.Duration) (Result, error) {
	if len(sources) == 0 {
		return Result{}, fmt.Errorf("没有可用的搜索结果")
	}
	usable := make([]Endpoint, 0, len(eps))
	for _, e := range eps {
		if e.BaseURL == "" || e.Model == "" {
			continue
		}
		usable = append(usable, e)
	}
	if len(usable) == 0 {
		return Result{}, fmt.Errorf("没有可用的 AI 接口")
	}
	if timeout <= 0 {
		timeout = 30 * time.Second
	}
	prompt := buildPrompt(query, sources)
	failures := make([]string, 0, len(usable))
	for _, e := range usable {
		got, err := call(ctx, e, prompt, timeout, len(sources))
		if err == nil {
			got.Provider = "openai-compat:" + e.Model
			got.Endpoint = e.Name
			return got, nil
		}
		name := e.Name
		if name == "" {
			name = e.Model
		}
		failures = append(failures, fmt.Sprintf("%s：%v", name, err))
		if ctx.Err() != nil {
			break
		}
	}
	return Result{}, fmt.Errorf("所有 AI 接口都失败（%s）", strings.Join(failures, "；"))
}

func call(ctx context.Context, e Endpoint, prompt string, timeout time.Duration, sourceCount int) (Result, error) {
	// 推理模型会先把预算花在 reasoning_content 上；700 不够时按 finish_reason=length
	// 放大到 1600 重试一次（与站点分类踩过的坑同源）。
	content, finish, err := chat(ctx, e, prompt, 700, timeout)
	if err != nil {
		return Result{}, err
	}
	if strings.TrimSpace(content) == "" && finish == "length" {
		content, finish, err = chat(ctx, e, prompt, 1600, timeout)
		if err != nil {
			return Result{}, err
		}
	}
	content = strings.TrimSpace(content)
	if content == "" {
		return Result{}, fmt.Errorf("模型没有返回内容")
	}
	var parsed struct {
		Answer    string `json:"answer"`
		Citations []int  `json:"citations"`
	}
	if err := json.Unmarshal([]byte(stripCodeFence(content)), &parsed); err != nil {
		return Result{}, fmt.Errorf("模型输出不是合法 JSON")
	}
	answer := strings.TrimSpace(parsed.Answer)
	if answer == "" {
		return Result{}, fmt.Errorf("模型没有给出回答")
	}
	return Result{Answer: answer, Citations: validCitations(parsed.Citations, sourceCount)}, nil
}

// validCitations 去重并剔除越界编号。模型编造的来源不能出现在页面上。
func validCitations(in []int, n int) []int {
	seen := make(map[int]bool, len(in))
	out := make([]int, 0, len(in))
	for _, id := range in {
		if id < 1 || id > n || seen[id] {
			continue
		}
		seen[id] = true
		out = append(out, id)
	}
	return out
}

func chat(ctx context.Context, e Endpoint, prompt string, maxTokens int, timeout time.Duration) (string, string, error) {
	body := map[string]interface{}{
		"model": e.Model,
		"messages": []map[string]string{
			{"role": "user", "content": prompt},
		},
		"temperature": 0.2,
		"max_tokens":  maxTokens,
	}
	buf, err := json.Marshal(body)
	if err != nil {
		return "", "", err
	}
	url := strings.TrimSuffix(e.BaseURL, "/") + "/chat/completions"
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(buf))
	if err != nil {
		return "", "", err
	}
	req.Header.Set("Content-Type", "application/json")
	if e.APIKey != "" {
		req.Header.Set("Authorization", "Bearer "+e.APIKey)
	}
	client := &http.Client{Timeout: timeout}
	resp, err := client.Do(req)
	if err != nil {
		return "", "", err
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return "", "", err
	}
	if resp.StatusCode != http.StatusOK {
		return "", "", fmt.Errorf("模型返回 %d：%s", resp.StatusCode, clipErr(raw))
	}
	var out struct {
		Choices []struct {
			FinishReason string `json:"finish_reason"`
			Message      struct {
				Content string `json:"content"`
			} `json:"message"`
		} `json:"choices"`
	}
	if err := json.Unmarshal(raw, &out); err != nil {
		return "", "", fmt.Errorf("解析响应失败：%w", err)
	}
	if len(out.Choices) == 0 {
		return "", "", fmt.Errorf("模型没有返回内容")
	}
	return out.Choices[0].Message.Content, out.Choices[0].FinishReason, nil
}

func buildPrompt(query string, sources []Source) string {
	var b strings.Builder
	b.WriteString("你是搜索引擎的 AI 概览生成器。根据用户问题和下面的搜索结果，用中文直接回答。\n\n规则：\n")
	b.WriteString("1. 只使用下面搜索结果中的信息，不要补充外部知识。\n")
	b.WriteString("2. 先给结论，再给必要要点；最多 4 条，总长度不超过 300 字。\n")
	b.WriteString("3. 每个要点末尾用 [编号] 标注来源，编号必须是下面给出的来源编号。\n")
	b.WriteString("4. 搜索结果不足以回答时，answer 写「搜索结果中没有足够信息回答这个问题」，citations 为空。\n")
	b.WriteString("5. 只输出 JSON：{\"answer\":\"...\",\"citations\":[1,2]}，不要 markdown 代码块，不要解释。\n\n")
	fmt.Fprintf(&b, "用户问题：%s\n\n搜索结果：\n", query)
	for _, s := range sources {
		text := strings.TrimSpace(s.Text)
		if r := []rune(text); len(r) > 300 {
			text = string(r[:300]) + "…"
		}
		fmt.Fprintf(&b, "[%d] %s（%s）：%s\n", s.ID, s.Title, s.Domain, text)
	}
	return b.String()
}

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
