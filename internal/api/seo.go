// 站点级 SEO / Open Graph。
//
// 为什么放在服务端：搜索引擎与社交平台的抓取器不执行 JS，只读 HTML 的 <head>。
// 前端用 Title/Meta 服务改是给「已经跑起来的页面」用的，对分享卡片没有意义。
//
// 描述与关键词的规则：后台填了就用后台的，留空则按索引现状自动生成。
// 这两项最容易长期空着——空着的结果是分享卡片没有描述、搜索结果里没有摘要。
package api

import (
	"context"
	"fmt"
	"html"
	"net/http"
	"path"
	"sort"
	"strings"
	"time"

	"voidxo/internal/sitecat"
	"voidxo/internal/webui"
)

const (
	// defaultSiteName 与前端 index.html 的 <title> 保持一致。
	defaultSiteName = "voidxo 维空"
	// seoTTL 自动生成部分的缓存时长。首页 HTML 每个 SPA 路由都要一次，
	// 不能每次都去数文档与站点；后台改配置时会主动失效，不用等它过期。
	seoTTL = 10 * time.Minute
	// 描述按搜索引擎的习惯截到 160 个字符左右，再长会被截断且显得啰嗦。
	descLimit = 160
)

// seoData 站点级三段文字 + 规模（llms.txt 用得上）。
type seoData struct {
	Name        string
	Description string
	Keywords    string
	Docs        int64
	Sites       int
}

func (s *Server) seoInfo(ctx context.Context) seoData {
	s.seoMu.Lock()
	defer s.seoMu.Unlock()
	if !s.seoAt.IsZero() && time.Since(s.seoAt) < seoTTL {
		return s.seoVal
	}
	s.seoVal = s.buildSEO(ctx)
	s.seoAt = time.Now()
	return s.seoVal
}

// invalidateSEO 后台改完站点信息立刻生效。
func (s *Server) invalidateSEO() {
	s.seoMu.Lock()
	s.seoAt = time.Time{}
	s.seoMu.Unlock()
}

func (s *Server) buildSEO(ctx context.Context) seoData {
	name, _ := s.st.GetSetting(ctx, "site.name")
	name = strings.TrimSpace(name)
	if name == "" {
		name = defaultSiteName
	}
	desc, _ := s.st.GetSetting(ctx, "site.description")
	kw, _ := s.st.GetSetting(ctx, "site.keywords")
	desc = strings.TrimSpace(desc)
	kw = strings.TrimSpace(kw)
	if desc != "" && kw != "" {
		docs, _ := s.st.CountDocuments(ctx)
		sites, _ := s.st.WhitelistCategoryCounts(ctx)
		return seoData{Name: name, Description: truncateRunes(desc, descLimit), Keywords: kw,
			Docs: docs, Sites: countSites(sites)}
	}

	docs, _ := s.st.CountDocuments(ctx)
	sites, _ := s.st.WhitelistCategoryCounts(ctx)
	siteCount := countSites(sites)
	if desc == "" {
		if docs > 0 {
			desc = fmt.Sprintf(
				"%s：只收录人工审核过的技术站点，已收录 %d 篇文章、覆盖 %d 个站点，每条结果都附排序理由。",
				name, docs, siteCount)
		} else {
			desc = name + "：只索引人工审核过的技术站点，每条结果附排序理由。"
		}
	}
	if kw == "" {
		kw = s.autoKeywords(ctx, sites)
	}
	return seoData{Name: name, Description: truncateRunes(desc, descLimit), Keywords: kw,
		Docs: docs, Sites: siteCount}
}

// countSites 把「各分类站点数」加总成站点总数。
func countSites(cats map[string]int) int {
	n := 0
	for _, v := range cats {
		n += v
	}
	return n
}

// autoKeywords 关键词按「有人会搜的词」拼：站点类型 → 站点名。
// 不用域名：域名不像人会敲进搜索框的词，而且列表一长就没法看。
func (s *Server) autoKeywords(ctx context.Context, cats map[string]int) string {
	out := []string{"技术搜索", "开发者搜索", "技术文档", "中文技术内容"}

	type item struct {
		label string
		n     int
	}
	var cs []item
	for _, c := range sitecat.List {
		if n := cats[c.Key]; n > 0 {
			cs = append(cs, item{label: c.Label, n: n})
		}
	}
	sort.SliceStable(cs, func(i, j int) bool { return cs[i].n > cs[j].n })
	for i := 0; i < len(cs) && i < 3; i++ {
		out = append(out, cs[i].label)
	}

	if sites, _, err := s.st.ListWhitelistIn(ctx, "", "", 200, 0); err == nil {
		sort.SliceStable(sites, func(i, j int) bool { return sites[i].DocCount > sites[j].DocCount })
		for i := 0; i < len(sites) && i < 5; i++ {
			// 站点名是爬虫从 og:site_name 提的（如「Kubernetes 文档」），比域名有信息量。
			if n := strings.TrimSpace(sites[i].SiteName); n != "" {
				out = append(out, n)
			}
		}
	}
	return strings.Join(dedupe(out), ",")
}

// headTags 生成注入 <head> 的标签。带绝对地址的按请求推导，部署在反代后面也能对。
func (s *Server) headTags(r *http.Request, info seoData) string {
	base := s.siteBaseURL(r)
	page := base + r.URL.Path
	image := base + "/og.png"
	esc := html.EscapeString

	var b strings.Builder
	write := func(format string, args ...any) { fmt.Fprintf(&b, format+"\n", args...) }
	// 搜索结果页（以及后台、账号页）不该被索引：这里给 noindex，并且**不写 canonical**。
	//
	// 写 canonical 是有害的：它等于告诉搜索引擎「这一页的正本是 /search」，
	// 那正是我们不想被收录的那一页。搜索结果页本身也没有分享价值，
	// 所以 og:url / og:image 一并省掉，只留标题与描述。
	noindex := noIndexPath(r.URL.Path)
	if noindex {
		write(`<meta name="robots" content="noindex, nofollow">`)
		write(`<meta name="description" content="%s">`, esc(info.Description))
		write(`<meta property="og:site_name" content="%s">`, esc(info.Name))
		write(`<meta property="og:title" content="%s">`, esc(info.Name))
		write(`<meta property="og:description" content="%s">`, esc(info.Description))
		return b.String()
	}
	write(`<meta name="description" content="%s">`, esc(info.Description))
	write(`<meta name="keywords" content="%s">`, esc(info.Keywords))
	write(`<link rel="canonical" href="%s">`, esc(page))
	write(`<meta property="og:type" content="website">`)
	write(`<meta property="og:locale" content="zh_CN">`)
	write(`<meta property="og:site_name" content="%s">`, esc(info.Name))
	write(`<meta property="og:title" content="%s">`, esc(info.Name))
	write(`<meta property="og:description" content="%s">`, esc(info.Description))
	write(`<meta property="og:url" content="%s">`, esc(page))
	write(`<meta property="og:image" content="%s">`, esc(image))
	write(`<meta property="og:image:width" content="1200">`)
	write(`<meta property="og:image:height" content="630">`)
	write(`<meta name="twitter:card" content="summary_large_image">`)
	write(`<meta name="twitter:title" content="%s">`, esc(info.Name))
	write(`<meta name="twitter:description" content="%s">`, esc(info.Description))
	write(`<meta name="twitter:image" content="%s">`, esc(image))
	return b.String()
}

// spaHandler 在内嵌产物外面包一层：HTML 响应注入站点级 meta，静态资源原样透传。
func (s *Server) spaHandler() http.Handler {
	inner := webui.Handler()
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		p := r.URL.Path
		// /api、/admin 的未命中要保留 JSON 404；带扩展名的是静态资源。
		if strings.HasPrefix(p, "/api/") || strings.HasPrefix(p, "/admin/") || path.Ext(p) != "" {
			inner.ServeHTTP(w, r)
			return
		}
		page, err := webui.IndexHTML()
		if err != nil {
			inner.ServeHTTP(w, r)
			return
		}
		info := s.seoInfo(r.Context())
		body := injectSEO(page, info.Name, s.headTags(r, info))
		// 站点标识也写进 HTML：否则慢网下会先画默认的 </> 章和默认站名再跳。
		body = injectBranding(body, s.brandInfo(r.Context()))
		// 页头/页脚代码最后插：它不该被前面的替换逻辑碰到，也不能碰别人。
		// 后台与账号页面不注入（见 sitehtml.go 的文件头）。
		if injectsSiteCode(p) {
			head, foot := s.siteCode(r)
			body = injectSiteCode(body, head, foot)
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		// 描述是动态生成的，不能让浏览器或反代把旧的一份缓存住。
		w.Header().Set("Cache-Control", "no-cache")
		_, _ = w.Write(body)
	})
}

// injectSEO 把标题、description 与新增标签写进 HTML 的 head。
//
// 原 index.html 里的 description 是静态兜底，直接去掉再插新的：
// 一页两个 description，抓取器取哪个全看心情。
func injectSEO(page []byte, title, tags string) []byte {
	s := string(page)
	if i := strings.Index(s, `<meta name="description"`); i >= 0 {
		if j := strings.Index(s[i:], ">"); j >= 0 {
			s = s[:i] + s[i+j+1:]
		}
	}
	if title != "" {
		if i := strings.Index(s, "<title>"); i >= 0 {
			if j := strings.Index(s[i:], "</title>"); j >= 0 {
				start := i + len("<title>")
				s = s[:start] + html.EscapeString(title) + s[i+j:]
			}
		}
	}
	return []byte(strings.Replace(s, "</head>", tags+"</head>", 1))
}

// siteBaseURL 站点的外部地址。
//
// 顺序：后台配的「站点地址」→ 反代的转发头 → 请求里的 Host。
// 配置优先是因为容器里 r.Host 常常是 127.0.0.1:8080 这类内网地址，
// 而分享卡片的 og:url 必须是外网能打开的。
func (s *Server) siteBaseURL(r *http.Request) string {
	if base := s.siteBase(r.Context()); base != "" {
		return base
	}
	scheme := "http"
	if r.TLS != nil {
		scheme = "https"
	}
	if v := firstHeader(r, "X-Forwarded-Proto"); v != "" {
		scheme = v
	}
	host := r.Host
	if v := firstHeader(r, "X-Forwarded-Host"); v != "" {
		host = v
	}
	if host == "" {
		return ""
	}
	return scheme + "://" + host
}

func firstHeader(r *http.Request, key string) string {
	v := r.Header.Get(key)
	if v == "" {
		return ""
	}
	if i := strings.IndexByte(v, ','); i >= 0 {
		v = v[:i]
	}
	return strings.TrimSpace(v)
}

// truncateRunes 按字符截断（不是字节），避免把中文切成半个字。
func truncateRunes(s string, n int) string {
	runes := []rune(s)
	if len(runes) <= n {
		return s
	}
	return string(runes[:n]) + "…"
}

func dedupe(in []string) []string {
	seen := make(map[string]bool, len(in))
	out := in[:0]
	for _, v := range in {
		v = strings.TrimSpace(v)
		if v == "" || seen[v] {
			continue
		}
		seen[v] = true
		out = append(out, v)
	}
	return out
}
