// Package distill 用搜索引擎的 `site:` 结果，取回直连抓不到的页面。
//
// 场景：有些站点对机房 IP 直接 403/451、或者握手超时，正文本身并不难拿——
// 只是我们连不上。这类页面改走一次 `site:域名 + 路径词` 的搜索，
// 把它返回的标题与摘要当成这一页的内容，再走原有的提取/入库流程。
//
// 边界（别越过）：
//   - 只在**被拦或连不上**时用，404/410 不蒸馏——页面真没了，换个来源也是没有。
//   - 只发站点域名与页面 URL 里的路径词，不发用户搜索词、不发用户 IP。
//   - 拿到的只有标题与一两百字的摘要，所以蒸馏页在索引里是「薄页」：
//     能搜到、能点回原站，但摘要不会比搜索引擎给的长。要整页正文只能等站点放行。
//   - 必须命中同一个域名、同一条路径才算数。搜索给的多是分类页或首页，
//     张冠李戴地塞进来比「没抓到」更糟——那种错在页面上根本看不出来。
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
	"github.com/zealis/stdseek/internal/ua"
)

// DefaultEngine 默认搜索引擎地址。后台可改（换成别的地区域名，或自建的可信入口）。
const DefaultEngine = "https://www.google.com/search"

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

// ErrBlocked 搜索引擎要验证码/限流。单独一个错误值，后台试抓时能给出可读的原因。
var ErrBlocked = errors.New("搜索引擎要求验证码或已限流")

// Page 蒸馏回来的页面。Text 通常是标题之外的一两百字摘要。
type Page struct {
	Title string
	Text  string
	URL   string
}

// Client 走搜索引擎。
//
// 有节流：一次抓取里可能有几十个页面被拦，连着打同一个搜索出口必然进验证码。
// 宁可慢，也不要在这台机器上把出口 IP 打死。
type Client struct {
	Engine string
	HC     *http.Client

	mu   sync.Mutex
	last time.Time
	// MinGap 两次搜索的最小间隔。
	MinGap time.Duration
}

// New 构造客户端。engine 为空时用 DefaultEngine，只填域名时补上 /search。
//
// 走 outbound.Transport() 而不是默认 Transport：默认那份会继承 HTTP_PROXY，
// 本机挂个代理软件时搜索请求会经它出去，失败信息还是代理回的 502——
// 抓取与预检早就统一到 outbound 了，蒸馏不能是第三个出口。
func New(engine string) *Client {
	return &Client{
		Engine: normalizeEngine(engine),
		HC: &http.Client{
			Timeout:   20 * time.Second,
			Transport: outbound.Transport(),
		},
		MinGap: 3 * time.Second,
	}
}

// normalizeEngine 把后台填的地址收敛成可直接拼查询串的形式。
//
// 允许只填域名（https://www.google.com）：不补的话拼出来是 /?q=…，
// 会被当成首页查询重定向，白白多一跳。
func normalizeEngine(raw string) string {
	e := strings.TrimSpace(raw)
	if e == "" {
		return DefaultEngine
	}
	if !strings.HasPrefix(e, "http://") && !strings.HasPrefix(e, "https://") {
		e = "https://" + e
	}
	if u, err := url.Parse(e); err == nil && (u.Path == "" || u.Path == "/") {
		u.Path = "/search"
		e = u.String()
	}
	return e
}

// Fetch 按 URL 蒸馏一页。
//
// 查询词是 `site:域名 路径里的词`——我们不知道这一页讲什么，但 URL 的路径
// 通常就是它的关键词（/docs/getting-started → docs getting started）。
func (c *Client) Fetch(ctx context.Context, pageURL string) (Page, error) {
	u, err := url.Parse(pageURL)
	if err != nil || u.Hostname() == "" {
		return Page{}, fmt.Errorf("蒸馏：URL 无效")
	}
	q := "site:" + strings.ToLower(u.Hostname())
	if kw := keywords(u.Path); kw != "" {
		q += " " + kw
	}

	data, err := c.search(ctx, q)
	if err != nil {
		return Page{}, err
	}
	results, err := parseSERP(data)
	if err != nil {
		return Page{}, err
	}
	if len(results) == 0 {
		return Page{}, fmt.Errorf("搜索没有结果")
	}

	pick, best := -1, 0
	for i, r := range results {
		if s := matchScore(pageURL, r.URL); s > best {
			pick, best = i, s
		}
	}
	if pick < 0 {
		return Page{}, fmt.Errorf("搜索结果里没有这一页")
	}
	r := results[pick]
	text := strings.TrimSpace(r.Snippet)
	if text == "" {
		return Page{}, fmt.Errorf("搜索结果没有摘要")
	}
	title := strings.TrimSpace(r.Title)
	if title == "" {
		title = u.Hostname()
	}
	return Page{Title: title, Text: text, URL: pageURL}, nil
}

// search 发一次搜索请求，返回响应体。带节流与「被拦」识别。
func (c *Client) search(ctx context.Context, q string) ([]byte, error) {
	if err := c.wait(ctx); err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.Engine, nil)
	if err != nil {
		return nil, err
	}
	params := req.URL.Query()
	params.Set("q", q)
	params.Set("num", "10")
	// pws=0 关掉个性化：同一个查询在不同机器上要给出同一批结果，否则同一页
	// 今天抓得到、明天抓不到，抓取记录没法复盘。
	params.Set("pws", "0")
	req.URL.RawQuery = params.Encode()

	req.Header.Set("User-Agent", ua.Browser)
	req.Header.Set("Accept", "text/html,application/xhtml+xml")
	req.Header.Set("Accept-Language", "zh-CN,zh;q=0.9,en;q=0.8")
	// 欧盟节点会先跳同意页，带上这个 cookie 直接给结果页。
	req.Header.Set("Cookie", "CONSENT=YES+cb")

	resp, err := c.HC.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if err != nil {
		return nil, err
	}
	if resp.StatusCode == http.StatusTooManyRequests || looksBlocked(data) {
		return nil, ErrBlocked
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("搜索返回 %d", resp.StatusCode)
	}
	return data, nil
}

// wait 保证两次搜索之间有 MinGap，避免短时间内把出口 IP 打进验证码。
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

// looksBlocked 认搜索引擎的验证码页。判字符串而不是状态码：它经常用 200 返回 /sorry/。
func looksBlocked(body []byte) bool {
	head := body
	if len(head) > 64<<10 {
		head = head[:64<<10]
	}
	s := string(head)
	return strings.Contains(s, "/sorry/") ||
		strings.Contains(s, "unusual traffic") ||
		strings.Contains(s, "g-recaptcha")
}

type serpResult struct {
	URL     string
	Title   string
	Snippet string
}

// parseSERP 从结果页里抠出「链接 + 标题 + 摘要」。
//
// 不依赖具体的 class 名（那些名字隔几个月就换一次）：以「一个含 <h3> 的链接」
// 作为一条结果的锚点，再往上找容器取摘要文本。class 只用来**优先**取摘要，
// 取不到就退回「容器文本减标题」。
func parseSERP(data []byte) ([]serpResult, error) {
	doc, err := goquery.NewDocumentFromReader(bytes.NewReader(data))
	if err != nil {
		return nil, err
	}
	var out []serpResult
	seen := map[string]bool{}
	doc.Find("a[href]").Each(func(_ int, a *goquery.Selection) {
		h3 := a.Find("h3").First()
		if h3.Length() == 0 {
			return
		}
		href := cleanHref(a.AttrOr("href", ""))
		if href == "" || seen[href] {
			return
		}
		title := strings.TrimSpace(h3.Text())
		if title == "" {
			return
		}
		snippet := ""
		if block := closestBlock(a); block != nil {
			snippet = strings.TrimSpace(block.Find(".VwiC3b, div[data-sncf], .IsZvec").First().Text())
			if snippet == "" {
				snippet = strings.TrimSpace(strings.Replace(block.Text(), title, "", 1))
			}
		}
		seen[href] = true
		out = append(out, serpResult{URL: href, Title: title, Snippet: collapseSpace(snippet)})
	})
	return out, nil
}

// closestBlock 找这条结果的内容容器：优先搜索引擎自己的结果壳，找不到就上溯两层。
func closestBlock(a *goquery.Selection) *goquery.Selection {
	for _, sel := range []string{"div.g", "div.MjjYud", "div[data-hveid]"} {
		if s := a.Closest(sel); s.Length() > 0 {
			return s.First()
		}
	}
	if p1 := a.Parent(); p1.Length() > 0 {
		if p2 := p1.Parent(); p2.Length() > 0 {
			return p2
		}
		return p1
	}
	return nil
}

// cleanHref 还原跳转链接。
//
// 老格式是 /url?q=<转义后的地址>&sa=…；新格式直接给完整地址。
// 站内链接（/search?… 、/preferences…）一律丢掉——它们不是搜索结果。
func cleanHref(h string) string {
	h = strings.TrimSpace(h)
	if strings.HasPrefix(h, "/url?") || strings.Contains(h, "/url?q=") {
		if u, err := url.Parse(h); err == nil {
			if q := u.Query().Get("q"); q != "" {
				h = q
			}
		}
	}
	if !strings.HasPrefix(h, "http://") && !strings.HasPrefix(h, "https://") {
		return ""
	}
	if strings.Contains(h, "/search?") {
		return ""
	}
	return h
}

// matchScore 给「这条结果是不是我们要的那一页」打分，0 表示不是。
//
//	3 = 地址完全一致（忽略协议、www、末尾斜杠）
//	2 = 路径一致、对方带查询串（多半是 utm 之类）
//	1 = 路径一致、我们要的带查询串而对方被规范化掉了
//
// 只认路径，不做「前缀相近」的模糊匹配：/docs 与 /docs/api 是两页，
// 认错了就是把目录页的摘要写进正文页，而这类错在页面上看不出来。
func matchScore(want, got string) int {
	wh, wp, wq, ok := splitURL(want)
	if !ok {
		return 0
	}
	gh, gp, gq, ok := splitURL(got)
	if !ok || gh != wh || gp != wp {
		return 0
	}
	switch {
	case wq == gq:
		return 3
	case wq == "":
		return 2
	case gq == "":
		return 1
	}
	return 0
}

func splitURL(raw string) (host, path, query string, ok bool) {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || u.Hostname() == "" {
		return "", "", "", false
	}
	host = strings.TrimPrefix(strings.ToLower(u.Hostname()), "www.")
	path = strings.TrimSuffix(u.Path, "/")
	if path == "" {
		path = "/"
	}
	return host, path, u.RawQuery, true
}

// keywords 从 URL 路径里取查询词。
//
// 丢掉扩展名、纯数字（文章 id 帮不上搜索）与过短的词段，最多取 6 个：
// 词太多时搜索引擎会当成另一个查询，反而不容易命中要的那一页。
func keywords(p string) string {
	p = strings.Trim(p, "/")
	if p == "" {
		return ""
	}
	// 去掉扩展名（/docs/guide.html 里的 html 是文件类型，不是这页的内容）
	if i := strings.LastIndex(p, "."); i > strings.LastIndex(p, "/") {
		p = p[:i]
	}
	var words []string
	for _, seg := range strings.FieldsFunc(p, func(r rune) bool {
		return r == '/' || r == '-' || r == '_' || r == '.'
	}) {
		seg = strings.TrimSpace(seg)
		if len([]rune(seg)) < 2 || isAllDigits(seg) {
			continue
		}
		words = append(words, seg)
		if len(words) == 6 {
			break
		}
	}
	return strings.Join(words, " ")
}

func isAllDigits(s string) bool {
	if s == "" {
		return false
	}
	for _, r := range s {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}

// collapseSpace 把摘要里的连续空白压成单个空格：搜索结果常带换行与多个空格，
// 直接入库后摘要中间会出现大段空白。
func collapseSpace(s string) string {
	return strings.Join(strings.Fields(s), " ")
}

// synHTML 把蒸馏到的内容包成一份最小 HTML，交回原有的提取管线。
//
// 为什么合成 HTML 而不是「直接入库」：正文提取、去样板、标签、分词、指纹、
// 去重全在 processor 里，绕过它就等于为蒸馏单独维护一套入库逻辑。
// 合成 HTML 让蒸馏页面走完全一样的路径，行为可预测。
//
// 带一条 meta 标明来源：原文快照里存的就是这份合成 HTML，管理员翻快照时
// 能一眼看出「这页是蒸馏来的，不是直连抓的」。
func synHTML(p Page) []byte {
	var b bytes.Buffer
	b.WriteString("<!doctype html><html><head><meta charset=\"utf-8\">")
	b.WriteString(`<meta name="stdseek-source" content="distill:search">`)
	b.WriteString("<title>")
	b.WriteString(escape(p.Title))
	b.WriteString("</title></head><body><article><h1>")
	b.WriteString(escape(p.Title))
	b.WriteString("</h1><p>")
	b.WriteString(escape(p.Text))
	b.WriteString("</p></article></body></html>")
	return b.Bytes()
}

func escape(s string) string {
	r := strings.NewReplacer("&", "&amp;", "<", "&lt;", ">", "&gt;", `"`, "&quot;")
	return r.Replace(s)
}
