// Package classify 用 OpenAI 兼容接口给站点判定分类。
//
// 与 summarize 的区别：摘要读正文，分类只看「这站是干嘛的」——
// 输入是域名 + 站点名 + 几条文章标题，成本比读正文低一个量级。
// 分类结果是**站点级**的（写回白名单），不是页级标签：
// 同一站点的所有页面共享一个分类，用户筛选的也是「我要哪类站」。
package classify

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"voidxo/internal/labels"
	"voidxo/internal/sitecat"
)

// Config 接口配置。空 BaseURL 或 Model 视为未配置。
type Config struct {
	BaseURL string
	APIKey  string
	Model   string
	Timeout time.Duration
}

// Client 分类客户端。零值不可用，用 New 构造。
type Client struct {
	cfg Config
	hc  *http.Client
}

// New 构造客户端；未配置时 Ready() 为 false，调用方据此降级。
func New(cfg Config) *Client {
	if cfg.Timeout <= 0 {
		cfg.Timeout = 30 * time.Second
	}
	return &Client{cfg: cfg, hc: &http.Client{Timeout: cfg.Timeout}}
}

// Ready 是否具备调用条件（有地址和模型就算，key 视供应商而定）。
func (c *Client) Ready() bool {
	return c != nil && c.cfg.BaseURL != "" && c.cfg.Model != ""
}

// Model 当前模型名（后台展示用）。
func (c *Client) Model() string {
	if c == nil {
		return ""
	}
	return c.cfg.Model
}

// Input 判定输入。Titles 是同一域名下最近的几篇文章标题。
type Input struct {
	Domain      string
	Name        string
	Description string
	Titles      []string
}

// 每次请求的 token 预算。见 chat() 的说明：推理模型会把预算花在思考上。
const (
	classifyMaxTokens   = 512
	classifyRetryTokens = 2048
)

const promptTmpl = `你是站点分类器。根据下面的站点信息，给这个站点判定**分类**与**标签**。

一、分类（单选，只能返回清单里的 key）：
%s
二、标签（可多选，没有就给空数组；只能返回清单里的 key）：
%s
规则：
1. 只输出 JSON：{"category":"key","labels":["key","key"]}，不要 markdown、不要解释。
2. 分类拿不准时按「用户会把这个站归到哪一类」判断，而不是按技术栈。
3. 官方产品站 + 文档站都算 official；问答/论坛/聚合算 platform。
4. 分类信息不足、或确实不属于清单里任何一类时，返回 "other"；不要为了给出答案硬塞。
5. 标签只在**有把握**时给：站点整体特征明显才打，看不准就留空数组。
   标签是给用户做「排除」用的，误标比不标更讨厌。
6. 标签针对的是**整个站点**（不是这一篇文章）：个人维护的站打 personal，
   用户发帖为主的平台打 ugc_platform，明显模板化批量生成的打 ai_suspect，
   正文被广告/推广包住的打 ad_heavy。

站点域名：%s
站点名称：%s
站点描述：%s
近期文章标题：
%s`

// Result 一次判定的结果：分类单选、标签多选。
type Result struct {
	Category string
	Labels   []string
}

// Site 判定单个站点的分类与标签。
func (c *Client) Site(ctx context.Context, in Input) (Result, error) {
	if !c.Ready() {
		return Result{}, errors.New("AI 分类未配置")
	}
	titles := "-（暂无）"
	if len(in.Titles) > 0 {
		var b strings.Builder
		for _, t := range in.Titles {
			b.WriteString("- " + truncate(t, 80) + "\n")
		}
		titles = strings.TrimRight(b.String(), "\n")
	}
	prompt := fmt.Sprintf(promptTmpl, sitecat.Prompt(), labels.Prompt(),
		in.Domain, orDash(in.Name), orDash(in.Description), titles)

	raw, err := c.chat(ctx, prompt)
	if err != nil {
		return Result{}, err
	}
	key := parseCategory(raw)
	if !sitecat.Valid(key) {
		// 模型没按词表回答（或答了个不存在的 key）时落到「其他」，不报错：
		// 批量 20 个站时，一个不合规的回答不该让整条结果变成红色错误，
		// 而「其他」本身就是「判不出来」的落点，语义是对的。
		key = sitecat.Other
	}
	return Result{Category: key, Labels: parseLabels(raw)}, nil
}

// parseLabels 从模型输出里取标签，**只保留词表里的 key**。
//
// 模型偶尔会编一个不存在的标签（比如 "low_quality"），直接写进索引就成了
// 一条永远筛不到、也永远清不掉的脏数据；这里直接丢掉，宁可少一个标签。
func parseLabels(s string) []string {
	i := strings.Index(s, "{")
	j := strings.LastIndex(s, "}")
	if i < 0 || j <= i {
		return nil
	}
	var obj struct {
		Labels []string `json:"labels"`
	}
	if err := json.Unmarshal([]byte(s[i:j+1]), &obj); err != nil {
		return nil
	}
	out := make([]string, 0, len(obj.Labels))
	seen := map[string]bool{}
	for _, l := range obj.Labels {
		k := strings.ToLower(strings.TrimSpace(l))
		if !labels.Valid(k) || seen[k] {
			continue
		}
		seen[k] = true
		out = append(out, k)
	}
	return out
}

// chat 走 /chat/completions，返回模型正文。
//
// max_tokens 给到 512 而不是几十：现在常见的「flash」档模型其实是**推理模型**，
// 会先输出一段 reasoning_content 再给正文。实测 deepseek-v4.1-flash 用 64 时
// content 为空、finish_reason=length，解析结果落到「其他」——24 个站全被标成「其他」，
// 比不判还糟。512 够它想完再吐那 30 来个字符的 JSON。
func (c *Client) chat(ctx context.Context, prompt string) (string, error) {
	content, finish, err := c.chatOnce(ctx, prompt, classifyMaxTokens)
	if err != nil {
		return "", err
	}
	// 个别站点的提示词会让模型想得更久：被截断且没拿到正文时，放大预算再要一次。
	if strings.TrimSpace(content) == "" && finish == "length" {
		content, _, err = c.chatOnce(ctx, prompt, classifyRetryTokens)
		if err != nil {
			return "", err
		}
	}
	// 空正文必须报错，不能静默落「其他」：那是「判不出来」的语义，
	// 而这里其实是「这次调用没成功」，两者混在一起会让批量结果看起来全都正常。
	if strings.TrimSpace(content) == "" {
		return "", errors.New("模型没有返回正文（推理预算被吃光，或该模型不支持 JSON 输出）")
	}
	return content, nil
}

// chatOnce 发一次请求，返回正文与 finish_reason。
func (c *Client) chatOnce(ctx context.Context, prompt string, maxTokens int) (string, string, error) {
	body, _ := json.Marshal(map[string]interface{}{
		"model":       c.cfg.Model,
		"temperature": 0,
		"max_tokens":  maxTokens,
		"messages": []map[string]string{
			{"role": "system", "content": "你只输出严格 JSON，不写任何解释。"},
			{"role": "user", "content": prompt},
		},
	})
	url := strings.TrimRight(c.cfg.BaseURL, "/") + "/chat/completions"
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return "", "", err
	}
	req.Header.Set("Content-Type", "application/json")
	if c.cfg.APIKey != "" {
		req.Header.Set("Authorization", "Bearer "+c.cfg.APIKey)
	}
	resp, err := c.hc.Do(req)
	if err != nil {
		return "", "", fmt.Errorf("请求模型失败：%w", err)
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return "", "", err
	}
	if resp.StatusCode != http.StatusOK {
		return "", "", fmt.Errorf("模型返回 %d：%s", resp.StatusCode, truncate(string(data), 160))
	}
	var out struct {
		Choices []struct {
			FinishReason string `json:"finish_reason"`
			Message      struct {
				Content string `json:"content"`
			} `json:"message"`
		} `json:"choices"`
	}
	if err := json.Unmarshal(data, &out); err != nil || len(out.Choices) == 0 {
		return "", "", errors.New("模型响应无法解析")
	}
	return out.Choices[0].Message.Content, out.Choices[0].FinishReason, nil
}

// parseCategory 从模型输出里抠出分类 key。
// 模型偶尔会裹上代码块或加一句解释，所以先按 JSON 解析，失败再退回「扫已知 key」。
func parseCategory(s string) string {
	s = strings.TrimSpace(s)
	if i := strings.Index(s, "{"); i >= 0 {
		if j := strings.LastIndex(s, "}"); j > i {
			var obj struct {
				Category string `json:"category"`
			}
			if err := json.Unmarshal([]byte(s[i:j+1]), &obj); err == nil && obj.Category != "" {
				return strings.ToLower(strings.TrimSpace(obj.Category))
			}
		}
	}
	low := strings.ToLower(s)
	for _, c := range sitecat.List {
		if strings.Contains(low, c.Key) {
			return c.Key
		}
	}
	return ""
}

func orDash(s string) string {
	if strings.TrimSpace(s) == "" {
		return "（无）"
	}
	return truncate(s, 120)
}

func truncate(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n]) + "…"
}
