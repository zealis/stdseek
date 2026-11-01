// Package distill 用「阅读代理」取回直连抓不到的页面。
//
// 场景：有些站点对机房 IP 直接 403/451、或者握手超时，正文本身并不难拿——
// 只是我们连不上。这类页面交给一个阅读器服务（默认 r.jina.ai）：把目标 URL 拼在它后面
// 请求一次，由它替我们访问并返回**整页 HTML**，再走原有的提取/入库流程。
//
// 为什么不再爬搜索结果：试过，走不通。
//   - 谷歌对非浏览器客户端一律返回「请启用 JS」的引导页（200 / 92KB / 0 条结果，
//     换 UA、加 gbv=1、用文本浏览器都一样），HTTP 抓谷歌这条路已经堵死。
//   - Bing 国内版直接无视 `site:`（`site:go.dev` 回的是抖音、知乎）。
//   - 百度一律弹验证码。
//   - DuckDuckGo 的 html 端点能抓，但它按相关度给前 10 条——要的那一页 `/doc/error`
//     常常不在里面，只有教程或首页。按「同域名同路径」严格匹配的结果是十次里九次落空，
//     放宽匹配又会把别的页写进这一页，那比抓不到更糟。
//   - 更根本的是：搜索只给一两百字摘要，而阅读器给的是整页正文。
//
// 边界（别越过）：
//   - 只在**被拦或连不上**时用，404/410 不蒸馏——页面真没了，换个通道也是没有。
//   - 发出去的是目标页 URL（第三方要据此去取），不含用户搜索词与用户 IP。
//   - 阅读器自己也要能出网：它多半在墙外，所以支持单独配代理（抓取仍直连）。
package distill

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/PuerkitoBio/goquery"

	"github.com/zealis/stdseek/internal/outbound"
)

// DefaultReader 默认阅读器。末尾的斜杠别省：它要求「基地址 + 完整 URL」直接拼接。
//
// 换自建服务时只要协议一致（GET <base><目标URL>，返回整页 HTML）就能用。
const DefaultReader = "https://r.jina.ai/"

// 蒸馏只在这些情况下发生：被 WAF 拦、限流、法律屏蔽、服务端异常、或根本连不上。
// 404/410 不在此列——页面真的不存在，换通道只会拿回一张错误页。
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

// ErrBlocked 阅读器自己被限流/要验证码。单独一个错误值，后台试抓时能给出可读的原因。
var ErrBlocked = errors.New("阅读器要求验证码或已限流")

// Page 蒸馏回来的页面。HTML 是整页原文，直接喂给后续管线。
type Page struct {
	Title string
	HTML  []byte
	URL   string
}

// Client 走阅读器。
//
// 有节流：一次抓取里可能有几十个页面被拦，连着打同一个阅读服务会触发它的限流
// （免费额度按分钟算），宁可慢一点。
type Client struct {
	Reader string
	HC     *http.Client

	mu   sync.Mutex
	last time.Time
	// MinGap 两次请求的最小间隔。
	MinGap time.Duration
}

// New 构造客户端。reader 为空时用 DefaultReader。
//
// proxy 是**只给蒸馏用的**出口：抓取要直连（那是被收录站点自己的可访问性），
// 蒸馏要翻墙（阅读器在墙外），两者用同一条出口必然有一头是错的。
// 留空则跟随 outbound 的统一策略（默认直连、忽略 HTTP_PROXY）。
func New(reader, proxy string) *Client {
	return &Client{
		Reader: normalizeReader(reader),
		HC: &http.Client{
			Timeout:   30 * time.Second,
			Transport: transportFor(proxy),
		},
		MinGap: 2 * time.Second,
	}
}

// transportFor 决定蒸馏请求从哪出去。
//
// 没配代理时用 outbound.Transport() 而不是默认 Transport：默认那份会继承 HTTP_PROXY，
// 本机挂个代理软件时蒸馏就悄悄走了它，失败信息还是代理回的 502——
// 抓取与预检早就统一到 outbound 了，蒸馏不能是第三个出口。
func transportFor(proxy string) http.RoundTripper {
	p := strings.TrimSpace(proxy)
	if p == "" {
		return outbound.Transport()
	}
	if !strings.Contains(p, "://") {
		p = "http://" + p
	}
	u, err := url.Parse(p)
	if err != nil || u.Host == "" {
		// 填错了就退回统一策略，而不是让每次请求都拿一个坏代理去撞
		return outbound.Transport()
	}
	return &http.Transport{
		Proxy:             http.ProxyURL(u),
		ForceAttemptHTTP2: true,
		MaxIdleConns:      4,
		IdleConnTimeout:   30 * time.Second,
	}
}

// normalizeReader 把后台填的基地址收敛成「以 / 结尾」的形式。
func normalizeReader(raw string) string {
	r := strings.TrimSpace(raw)
	if r == "" {
		return DefaultReader
	}
	if !strings.HasPrefix(r, "http://") && !strings.HasPrefix(r, "https://") {
		r = "https://" + r
	}
	if !strings.HasSuffix(r, "/") {
		r += "/"
	}
	return r
}

// Fetch 让阅读器取回一页。
func (c *Client) Fetch(ctx context.Context, pageURL string) (Page, error) {
	u, err := url.Parse(pageURL)
	if err != nil || u.Hostname() == "" {
		return Page{}, fmt.Errorf("蒸馏：URL 无效")
	}
	if err := c.wait(ctx); err != nil {
		return Page{}, err
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.Reader+pageURL, nil)
	if err != nil {
		return Page{}, err
	}
	// 要整页 HTML 而不是它默认的 markdown：正文提取、去样板、分词、指纹
	// 全在 processor 里，拿到原始 HTML 才能走完全一样的路径。
	req.Header.Set("X-Return-Format", "html")
	req.Header.Set("Accept", "text/html,application/xhtml+xml")

	resp, err := c.HC.Do(req)
	if err != nil {
		return Page{}, err
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if err != nil {
		return Page{}, err
	}
	switch {
	case resp.StatusCode == http.StatusTooManyRequests, resp.StatusCode == http.StatusPaymentRequired:
		return Page{}, ErrBlocked
	case resp.StatusCode != http.StatusOK:
		return Page{}, fmt.Errorf("阅读器返回 %d：%s", resp.StatusCode, snippets(data))
	}
	// 目标站自己挂了时，阅读器会用 200 把它的错误页原样给我们，并在正文里注明。
	// 这种「抓到了错误页」不该入库——它会以正常页面身份进索引。
	if strings.Contains(firstBytes(data, 2048), "Target URL returned error") {
		return Page{}, fmt.Errorf("目标站返回错误：%s", heading(data))
	}
	if len(bytes.TrimSpace(data)) == 0 {
		return Page{}, fmt.Errorf("阅读器返回空内容")
	}

	title := ""
	if doc, derr := goquery.NewDocumentFromReader(bytes.NewReader(data)); derr == nil {
		title = strings.TrimSpace(doc.Find("title").First().Text())
	}
	if title == "" {
		title = u.Hostname()
	}
	return Page{Title: title, HTML: data, URL: pageURL}, nil
}

// wait 保证两次请求之间至少隔 MinGap，避免把阅读器的免费额度打爆。
func (c *Client) wait(ctx context.Context) error {
	if c.MinGap <= 0 {
		return nil
	}
	c.mu.Lock()
	wait := time.Until(c.last.Add(c.MinGap))
	if wait < 0 {
		wait = 0
	}
	c.last = time.Now().Add(wait)
	c.mu.Unlock()
	if wait == 0 {
		return nil
	}
	t := time.NewTimer(wait)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
		return nil
	}
}

func firstBytes(b []byte, n int) string {
	if len(b) > n {
		b = b[:n]
	}
	return string(b)
}

// heading 取页面标题，用于错误信息里点明「拿回来的是什么」。
func heading(data []byte) string {
	doc, err := goquery.NewDocumentFromReader(bytes.NewReader(data))
	if err != nil {
		return ""
	}
	t := strings.Join(strings.Fields(doc.Find("title").First().Text()), " ")
	if r := []rune(t); len(r) > 60 {
		t = string(r[:60]) + "…"
	}
	return t
}

// snippets 截一段响应体放进错误信息。返回 HTML 时先剥标签，免得日志里全是尖括号。
func snippets(data []byte) string {
	s := strings.Join(strings.Fields(string(data)), " ")
	if r := []rune(s); len(r) > 120 {
		s = string(r[:120]) + "…"
	}
	return s
}
