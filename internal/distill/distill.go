// Package distill 用第三方「搜索聚合」接口取回直连抓不到的页面。
//
// 场景：有些站点对机房 IP 直接 403/451、或者握手超时，正文本身并不难拿——
// 只是我们连不上。这类页面交给聚合接口（默认 uapis.cn）按站点+URL 查一次，
// 它替我们取回正文，我们再走原有的提取/入库流程。
//
// 边界（别越过）：
//   - 只在**被拦或连不上**时用，404/410 不蒸馏——页面真没了，换个接口也是没有。
//   - 只发 URL（含域名），不发用户搜索词、不发 IP。
//   - 结果里的正文当「摘要级证据」：标题与正文都来自第三方，入库照常，但
//     原文快照留着合成的那份 HTML，管理员能看出这不是直连拿到的。
package distill

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// DefaultURL 默认接口。后台可改，改成任何兼容 POST {query,site,fetch_full} 的服务都行。
const DefaultURL = "https://uapis.cn/api/v1/search/aggregate"

// 蒸馏只在这些情况下发生：被 WAF 拦、限流、法律屏蔽、服务端异常、或根本连不上。
// 404/410 不在此列——页面真的不存在，蒸馏只会拿回别的东西。
func shouldDistill(status int, err error) bool {
	if err != nil {
		return true
	}
	switch {
	case status == http.StatusForbidden, // 403 WAF
		status == http.StatusTooManyRequests,            // 429
		status == http.StatusUnavailableForLegalReasons, // 451
		status >= 500:
		return true
	}
	return false
}

// Page 蒸馏回来的页面。
type Page struct {
	Title string
	Text  string
	URL   string
}

// Client 调聚合接口。Token 为空时也允许（部分部署的接口不要鉴权）。
type Client struct {
	URL   string
	Token string
	HC    *http.Client
}

// New 构造客户端。url 为空时用 DefaultURL。
func New(rawURL, token string) *Client {
	u := strings.TrimSpace(rawURL)
	if u == "" {
		u = DefaultURL
	}
	return &Client{URL: u, Token: strings.TrimSpace(token), HC: &http.Client{Timeout: 25 * time.Second}}
}

type aggResponse struct {
	Results []struct {
		Title   string `json:"title"`
		URL     string `json:"url"`
		Snippet string `json:"snippet"`
		// fetch_full=true 时，正文可能落在下面几种字段名里（不同部署不一样），
		// 所以都收，取第一个非空的。
		Content     string `json:"content"`
		ContentText string `json:"content_text"`
		Text        string `json:"text"`
	} `json:"results"`
	Code    string `json:"code"`
	Message string `json:"message"`
}

// Fetch 按 URL 蒸馏一页。
//
// 查询策略：site 限定到域名、query 用整条 URL——聚合接口对「URL 当查询词」
// 通常能精确命中那一页；命中不到就退回第一条结果，但只在域名一致时才认，
// 免得把别的站的内容塞进这一页。
func (c *Client) Fetch(ctx context.Context, pageURL string) (Page, error) {
	u, err := url.Parse(pageURL)
	if err != nil || u.Hostname() == "" {
		return Page{}, fmt.Errorf("蒸馏：URL 无效")
	}
	body := map[string]any{
		"query":      pageURL,
		"site":       u.Hostname(),
		"fetch_full": true,
	}
	raw, err := json.Marshal(body)
	if err != nil {
		return Page{}, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.URL, bytes.NewReader(raw))
	if err != nil {
		return Page{}, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	if c.Token != "" {
		req.Header.Set("Authorization", "Bearer "+c.Token)
	}
	resp, err := c.HC.Do(req)
	if err != nil {
		return Page{}, err
	}
	defer resp.Body.Close()
	// 接口偶尔返回 HTML 错误页，限制读取量并把状态码带进错误信息，
	// 排查时能直接看出是鉴权失败还是服务异常。
	data, err := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if err != nil {
		return Page{}, err
	}
	if resp.StatusCode != http.StatusOK {
		return Page{}, fmt.Errorf("蒸馏接口 %d：%s", resp.StatusCode, truncate(string(data), 120))
	}
	var out aggResponse
	if err := json.Unmarshal(data, &out); err != nil {
		return Page{}, fmt.Errorf("蒸馏接口返回的不是 JSON：%s", truncate(string(data), 120))
	}
	if len(out.Results) == 0 {
		if out.Message != "" {
			return Page{}, fmt.Errorf("蒸馏接口：%s", out.Message)
		}
		return Page{}, fmt.Errorf("蒸馏接口没有结果")
	}

	want := u.Hostname()
	pick := -1
	for i, r := range out.Results {
		if sameHost(r.URL, want) {
			pick = i
			break
		}
	}
	if pick < 0 {
		return Page{}, fmt.Errorf("蒸馏接口没有命中该域名")
	}
	r := out.Results[pick]
	text := firstNonEmpty(r.ContentText, r.Content, r.Text, r.Snippet)
	if strings.TrimSpace(text) == "" {
		return Page{}, fmt.Errorf("蒸馏结果没有正文")
	}
	title := strings.TrimSpace(r.Title)
	if title == "" {
		title = u.Hostname()
	}
	return Page{Title: title, Text: text, URL: pageURL}, nil
}

func sameHost(raw, want string) bool {
	u, err := url.Parse(raw)
	if err != nil {
		return false
	}
	got := strings.ToLower(u.Hostname())
	want = strings.ToLower(want)
	// www 变体算同一站：聚合接口回的多半是规范地址，而我们要抓的常是不带 www 的。
	return got == want || strings.TrimPrefix(got, "www.") == strings.TrimPrefix(want, "www.")
}

func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if strings.TrimSpace(v) != "" {
			return v
		}
	}
	return ""
}

func truncate(s string, n int) string {
	r := []rune(strings.TrimSpace(s))
	if len(r) <= n {
		return string(r)
	}
	return string(r[:n]) + "…"
}

// synHTML 把蒸馏到的正文包成一份最小 HTML，交回原有的提取管线。
//
// 为什么合成 HTML 而不是「直接入库」：正文提取、去样板、标签、分词、指纹、
// 去重全在 processor 里，绕过它就等于为蒸馏单独维护一套入库逻辑。
// 合成 HTML 让蒸馏页面走完全一样的路径，行为可预测。
func synHTML(p Page) []byte {
	var b bytes.Buffer
	b.WriteString("<!doctype html><html><head><meta charset=\"utf-8\"><title>")
	b.WriteString(escape(p.Title))
	b.WriteString("</title></head><body><article><h1>")
	b.WriteString(escape(p.Title))
	b.WriteString("</h1>")
	// 正文按空行切段落，保留原有段落边界；单行超长也无所谓，提取器会再切。
	for _, para := range strings.Split(p.Text, "\n") {
		para = strings.TrimSpace(para)
		if para == "" {
			continue
		}
		b.WriteString("<p>")
		b.WriteString(escape(para))
		b.WriteString("</p>")
	}
	b.WriteString("</article></body></html>")
	return b.Bytes()
}

func escape(s string) string {
	r := strings.NewReplacer("&", "&amp;", "<", "&lt;", ">", "&gt;", `"`, "&quot;")
	return r.Replace(s)
}
