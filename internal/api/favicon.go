// 网站图标代理：结果页的站点图标由本站服务器向 Google 的图标接口代取。
//
// 为什么走代理而不是让浏览器直连 google.com：访客浏览器直连第三方，会把
// 「你在看哪些站点」（连同 IP）暴露给它，也违反隐私政策「无第三方资源」的承诺；
// 服务器代取之后，访客的浏览器只跟本站通信。
//
// 缓存策略（产品约定，2026-10-31）：只缓存在**内存**里、TTL 三天，重启即空——
// 图标是随时可再生的小资源，不值得落盘、进 KV 或进备份。取失败的负缓存一小时，
// 避免同一个不存在的域名被反复打上游。
//
// 上游是一条写死的链：google.com/s2 → gstatic.cn（Google 国内镜像，同一服务），
// 域名只作为查询参数、不作请求目标，没有任意 URL 的 SSRF 面。
package api

import (
	"context"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"sync"
	"time"
)

const (
	// 上游链：同一服务的两个入口，从左到右取第一个通的。
	// 主用 google.com/s2（教程口径），网络受限时退 gstatic.cn（Google 国内镜像，
	// 同一套 favicon 服务、不同 URL 形态）。都写死在代码里，域名只是参数。
	faviconUpstream1 = "https://www.google.com/s2/favicons?sz=64&domain="
	faviconUpstream2 = "https://t0.gstatic.cn/faviconV2?client=SOCIAL&type=FAVICON&fallback_opts=TYPE,SIZE,URL&url=http://"
	faviconUpstream3 = "&size=64"

	faviconTTL     = 3 * 24 * time.Hour // 「缓存几天」：三天
	faviconNegative = time.Hour         // 失败负缓存
	faviconMaxSize  = 256 << 10         // 单个图标上限 256KB（64px 的 PNG/ICO 远用不满）
	faviconMaxKeep  = 8192              // 内存条目上限：超出先清过期，再丢最早的
)

// 域名只作上游查询参数、不作请求目标，没有任意 URL 的 SSRF 面；
// 这个校验挡的是拼进查询串的奇怪字符与手搓请求。
var faviconDomainRe = regexp.MustCompile(`^[a-z0-9]([a-z0-9-]*[a-z0-9])?(\.[a-z0-9]([a-z0-9-]*[a-z0-9])?)+$`)

type faviconEntry struct {
	data    []byte
	ctype   string
	expires time.Time
}

type faviconCache struct {
	mu    sync.Mutex
	items map[string]faviconEntry
	// out 记录插入顺序（近似）：超量时按插入序淘汰，比每次做 LRU 简单且够用。
	out []string
}

var favIcons = &faviconCache{items: map[string]faviconEntry{}}

// faviconClient 上游专用：连接与响应各留 5s，图标取不到就放弃，不拖慢结果页。
var faviconClient = &http.Client{Timeout: 5 * time.Second}

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
	c.mu.Lock()
	defer c.mu.Unlock()
	if _, exists := c.items[domain]; !exists {
		c.out = append(c.out, domain)
	}
	c.items[domain] = e
	// 超量：先清过期的；仍超就从最早的开始丢（包一层循环而不是递归重入锁）。
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

// handleFavicon GET /api/favicon?d=<域名>：代理取站点图标。
func (s *Server) handleFavicon(w http.ResponseWriter, r *http.Request) {
	d := strings.ToLower(strings.TrimSpace(r.URL.Query().Get("d")))
	if len(d) > 253 || !faviconDomainRe.MatchString(d) {
		writeErr(w, http.StatusBadRequest, "域名不合法")
		return
	}
	if e, ok := favIcons.get(d); ok {
		writeFavicon(w, e.data, e.ctype)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 12*time.Second)
	defer cancel()

	// 依上游链取图：第一个返回 200 的胜出；全失败才负缓存。
	// 每跳 5s 超时（faviconClient），整条链 12s 封顶，不拖慢结果页其余请求。
	for _, upstream := range []string{faviconUpstream1 + url.QueryEscape(d), faviconUpstream2 + url.QueryEscape(d) + faviconUpstream3} {
		data, ctype, ok := fetchFavicon(ctx, upstream)
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
	req.Header.Set("User-Agent", "voidxo-favicon-proxy/0.1")
	resp, err := faviconClient.Do(req)
	if err != nil {
		return nil, "", false
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, "", false
	}
	ctype := resp.Header.Get("Content-Type")
	if !strings.HasPrefix(ctype, "image/") {
		ctype = "image/png" // 两家上游实际都返回 PNG；兜住意外值
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, faviconMaxSize+1))
	if err != nil || len(data) == 0 || len(data) > faviconMaxSize {
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
