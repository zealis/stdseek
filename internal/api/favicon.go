// 网站图标代理：结果页的站点图标由本站服务器代取。
//
// 为什么走代理而不是让浏览器直连第三方：访客浏览器直连，会把「你在看哪些站点」
// （连同 IP）暴露给第三方，也违反隐私政策「无第三方资源」的承诺；
// 服务器代取之后，访客的浏览器只跟本站通信。
//
// 上游是一条写死的链，取第一个通的：
//  1. google.com/s2/favicons（Google 图标服务）
//  2. t0.gstatic.cn/faviconV2（Google 国内镜像，同一服务、URL 形态不同）
//  3. 站点自己的 /favicon.ico（https → http）——Google 库里没有的站
//     （个人博客、新站常见）从这里兜底
//
// 第 3 跳的目标域名来自用户参数，必须防内网探测：解析出的 IP 落在回环/私网/链路本地
// 一律拒绝（faviconDial），且响应体要过 http.DetectContentType 的图片嗅探
// ——有些站的 404 页也回 200 + HTML，不能当图标。
//
// 缓存策略（产品约定，2026-10-31）：只缓存在**内存**里、TTL 三天，重启即空——
// 图标是随时可再生的小资源，不值得落盘、进 KV 或进备份。取失败的负缓存一小时，
// 负缓存的命中直接回 404（曾经把它当命中回过 200 空体，浏览器会把空响应缓存一天，
// 表现就是「图标没了」，2026-11-01 修）。
package api

import (
	"context"
	"io"
	"net"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/zealis/stdseek/internal/outbound"
)

const (
	faviconUpstream1 = "https://www.google.com/s2/favicons?sz=64&domain="
	faviconUpstream2 = "https://t0.gstatic.cn/faviconV2?client=SOCIAL&type=FAVICON&fallback_opts=TYPE,SIZE,URL&url=http://"
	faviconUpstream3 = "&size=64"

	faviconTTL      = 3 * 24 * time.Hour // 「缓存几天」：三天
	faviconNegative = time.Hour          // 失败负缓存
	faviconMaxSize  = 256 << 10          // 单个图标上限 256KB（64px 的 PNG/ICO 远用不满）
	faviconMaxKeep  = 8192               // 内存条目上限：超出先清过期，再丢最早的
)

// 域名只作上游查询参数与固定路径的主机名，不作任意请求目标；
// 这个校验挡的是拼进查询串的奇怪字符与手搓请求。
var faviconDomainRe = regexp.MustCompile(`^[a-z0-9]([a-z0-9-]*[a-z0-9])?(\.[a-z0-9]([a-z0-9-]*[a-z0-9])?)+$`)

type faviconEntry struct {
	data     []byte
	ctype    string
	expires  time.Time
	negative bool // 取失败：命中时直接回 404，不再打上游
}

type faviconCache struct {
	mu    sync.Mutex
	items map[string]faviconEntry
	// out 记录插入顺序（近似）：超量时按插入序淘汰，比每次做 LRU 简单且够用。
	out []string
}

var favIcons = &faviconCache{items: map[string]faviconEntry{}}

func (c *faviconCache) get(domain string) (faviconEntry, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	e, ok := c.items[domain]
	if !ok {
		return faviconEntry{}, false
	}
	if time.Now().After(e.expires) {
		delete(c.items, domain)
		return faviconEntry{}, false
	}
	return e, true
}

func (c *faviconCache) put(domain string, e faviconEntry, negative bool) {
	ttl := faviconTTL
	if negative {
		ttl = faviconNegative
	}
	e.expires = time.Now().Add(ttl)
	e.negative = negative
	c.mu.Lock()
	defer c.mu.Unlock()
	if _, exists := c.items[domain]; !exists {
		c.out = append(c.out, domain)
	}
	c.items[domain] = e
	// 超量：先清过期的；仍超就从最早的开始丢。
	if len(c.items) > faviconMaxKeep {
		now := time.Now()
		kept := c.out[:0]
		for _, d := range c.out {
			if it, ok := c.items[d]; ok && now.After(it.expires) {
				delete(c.items, d)
			} else {
				kept = append(kept, d)
			}
		}
		c.out = kept
	}
	for len(c.items) > faviconMaxKeep && len(c.out) > 0 {
		d := c.out[0]
		c.out = c.out[1:]
		delete(c.items, d)
	}
}

// faviconDial 自定义拨号：解析目标域名的 IP，回环/私网/链路本地一律拒绝。
// 第 3 跳（站点自己的 /favicon.ico）的目标域名来自用户参数，
// 不做这个校验它就是一台内网探测器。对固定上游（google/gstatic，公网 IP）无感。
func faviconDial(ctx context.Context, network, addr string) (net.Conn, error) {
	host, port, err := net.SplitHostPort(addr)
	if err != nil {
		return nil, err
	}
	ips, err := net.DefaultResolver.LookupIPAddr(ctx, host)
	if err != nil {
		return nil, err
	}
	for _, ip := range ips {
		if outbound.IsLocalHost(ip.String()) {
			continue
		}
		var d net.Dialer
		return d.DialContext(ctx, network, net.JoinHostPort(ip.String(), port))
	}
	return nil, &net.AddrError{Err: "解析出的地址都在内网/回环段", Addr: host}
}

// faviconTransport 在 outbound 出口策略之上挂 IP 校验拨号。
var faviconTransport = func() *http.Transport {
	t := outbound.Transport()
	t.DialContext = faviconDial
	return t
}()

var faviconClient = &http.Client{Timeout: 5 * time.Second, Transport: faviconTransport}

// handleFavicon GET /api/favicon?d=<域名>：代理取站点图标。
func (s *Server) handleFavicon(w http.ResponseWriter, r *http.Request) {
	d := strings.ToLower(strings.TrimSpace(r.URL.Query().Get("d")))
	if len(d) > 253 || !faviconDomainRe.MatchString(d) {
		writeErr(w, http.StatusBadRequest, "域名不合法")
		return
	}
	if e, ok := favIcons.get(d); ok {
		if e.negative {
			writeErr(w, http.StatusNotFound, "图标不可用")
			return
		}
		writeFavicon(w, e.data, e.ctype)
		return
	}

	// 上游链，取第一个通的；全失败才负缓存。
	targets := []string{
		faviconUpstream1 + url.QueryEscape(d),
		faviconUpstream2 + url.QueryEscape(d) + faviconUpstream3,
		"https://" + d + "/favicon.ico",
		"http://" + d + "/favicon.ico",
	}
	ctx, cancel := context.WithTimeout(r.Context(), 14*time.Second)
	defer cancel()
	for _, target := range targets {
		data, ctype, ok := fetchFavicon(ctx, target)
		if !ok {
			continue
		}
		favIcons.put(d, faviconEntry{data: data, ctype: ctype}, false)
		writeFavicon(w, data, ctype)
		return
	}
	favIcons.put(d, faviconEntry{}, true)
	writeErr(w, http.StatusNotFound, "图标不可用")
}

// fetchFavicon 取单个上游。返回 (数据, Content-Type, 是否成功)。
func fetchFavicon(ctx context.Context, target string) ([]byte, string, bool) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, target, nil)
	if err != nil {
		return nil, "", false
	}
	req.Header.Set("User-Agent", "stdseek-favicon-proxy/0.1")
	resp, err := faviconClient.Do(req)
	if err != nil {
		return nil, "", false
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, "", false
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, faviconMaxSize+1))
	if err != nil || len(data) == 0 || len(data) > faviconMaxSize {
		return nil, "", false
	}
	// 图片嗅探：有些站对 /favicon.ico 也回 200 + HTML 404 页，不能当图标下发。
	// 两家上游实际都返回 PNG，这里统一用嗅探结果。
	ctype := http.DetectContentType(data)
	if !strings.HasPrefix(ctype, "image/") {
		return nil, "", false
	}
	return data, ctype, true
}

func writeFavicon(w http.ResponseWriter, data []byte, ctype string) {
	w.Header().Set("Content-Type", ctype)
	// 浏览器缓存一天：图标不常变，但别永久缓存（站点换图标要好几天才生效）。
	w.Header().Set("Cache-Control", "public, max-age=86400")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.WriteHeader(http.StatusOK)
	w.Write(data)
}
