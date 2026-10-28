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

const promptTmpl = `你是站点分类器。根据下面的站点信息，从给定分类里选出最贴切的一个。

分类清单（只能返回清单里的 key）：
%s
规则：
1. 只输出 JSON：{"category":"key"}，不要 markdown、不要解释。
2. 拿不准时按「用户会把这个站归到哪一类」判断，而不是按技术栈。
3. 官方产品站 + 文档站都算 official；问答/论坛/聚合算 platform。
4. 信息不足以判断、或确实不属于上面任何一类时，返回 {"category":"other"}；
   不要为了给出答案硬塞进某一类。

站点域名：%s
站点名称：%s
站点描述：%s
近期文章标题：
%s`

// Site 判定单个站点分类，返回 sitecat 的 key。
func (c *Client) Site(ctx context.Context, in Input) (string, error) {
	if !c.Ready() {
		return "", errors.New("AI 分类未配置")
	}
	titles := "-（暂无）"
	if len(in.Titles) > 0 {
		var b strings.Builder
		for _, t := range in.Titles {
			b.WriteString("- " + truncate(t, 80) + "\n")
		}
		titles = strings.TrimRight(b.String(), "\n")
	}
	prompt := fmt.Sprintf(promptTmpl, sitecat.Prompt(),
		in.Domain, orDash(in.Name), orDash(in.Description), titles)

	raw, err := c.chat(ctx, prompt)
	if err != nil {
		return "", err
	}
	key := parseCategory(raw)
	if !sitecat.Valid(key) {
		// 模型没按词表回答（或答了个不存在的 key）时落到「其他」，不报错：
		// 批量 20 个站时，一个不合规的回答不该让整条结果变成红色错误，
		// 而「其他」本身就是「判不出来」的落点，语义是对的。
		return sitecat.Other, nil
	}
	return key, nil
}

// chat 走 /chat/completions，返回模型正文。
func (c *Client) chat(ctx context.Context, prompt string) (string, error) {
	body, _ := json.Marshal(map[string]interface{}{
		"model":       c.cfg.Model,
		"temperature": 0,
		"max_tokens":  64,
		"messages": []map[string]string{
			{"role": "system", "content": "你只输出严格 JSON，不写任何解释。"},
			{"role": "user", "content": prompt},
		},
	})
	url := strings.TrimRight(c.cfg.BaseURL, "/") + "/chat/completions"
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/json")
	if c.cfg.APIKey != "" {
		req.Header.Set("Authorization", "Bearer "+c.cfg.APIKey)
	}
	resp, err := c.hc.Do(req)
	if err != nil {
		return "", fmt.Errorf("请求模型失败：%w", err)
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return "", err
	}
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("模型返回 %d：%s", resp.StatusCode, truncate(string(data), 160))
	}
	var out struct {
		Choices []struct {
			Message struct {
				Content string `json:"content"`
			} `json:"message"`
		} `json:"choices"`
	}
	if err := json.Unmarshal(data, &out); err != nil || len(out.Choices) == 0 {
		return "", errors.New("模型响应无法解析")
	}
	return out.Choices[0].Message.Content, nil
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
