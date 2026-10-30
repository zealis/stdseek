// robots.txt / security.txt / llms.txt
//
// 三个文件都是「机器读的站点门面」，由服务端生成并落到 <data>/web/ 下：
// 内容里有站点名、收录规模、联系邮箱、站点地址这些随时会变的东西，
// 写死在前端产物里等于每次改都要重新构建一次。
//
// 落盘的目的不是「让服务读文件」，而是**装完就有一份能改的**：
// 默认内容按现状生成，管理员可以直接编辑；服务优先读磁盘上的文件。
package api

import (
	"context"
	"encoding/xml"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// securityPath RFC 9116 规定的位置（/security.txt 只是兼容旧做法）。
const securityPath = "/.well-known/security.txt"

// sitemapPath 站点地图。robots.txt 里要引用它，所以单独一个常量。
const sitemapPath = "/sitemap.xml"

// seoFiles 三个文件的路由与落盘文件名。改这里同时改了两处。
var seoFiles = []struct{ route, name string }{
	{"/robots.txt", "robots.txt"},
	{securityPath, "security.txt"},
	{"/llms.txt", "llms.txt"},
}

// seoFilesDir 三个文件在磁盘上的位置（<data>/web）。
func (s *Server) seoFilesDir() string {
	if s.DataDir == "" {
		return ""
	}
	return filepath.Join(s.DataDir, "web")
}

// EnsureSEOFiles 首次启动时把三个文件写出来；已经存在就不动。
//
// 不覆盖是有意的：这三个文件是给人手改的，把管理员自己写的 robots 规则冲掉，
// 比不生成更糟。要重写走 -emit-web-files。
func (s *Server) EnsureSEOFiles(ctx context.Context) error {
	return s.emitSEOFiles(ctx, false)
}

// EmitSEOFiles 强制重写三个文件（配好站点地址之后用一次）。
func (s *Server) EmitSEOFiles(ctx context.Context) error {
	return s.emitSEOFiles(ctx, true)
}

func (s *Server) emitSEOFiles(ctx context.Context, force bool) error {
	dir := s.seoFilesDir()
	if dir == "" {
		return nil
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("创建 %s 失败：%w", dir, err)
	}
	base := s.siteBase(ctx)
	info := s.seoInfo(ctx)
	var written, kept []string
	for _, f := range seoFiles {
		path := filepath.Join(dir, f.name)
		if !force {
			if _, err := os.Stat(path); err == nil {
				kept = append(kept, f.name)
				continue
			}
		}
		if err := os.WriteFile(path, []byte(s.seoFileBody(ctx, f.name, base, info)), 0o644); err != nil {
			return fmt.Errorf("写入 %s 失败：%w", path, err)
		}
		written = append(written, f.name)
	}
	if len(written) > 0 {
		slog.Info("seo files written", "dir", dir, "files", strings.Join(written, ","), "base", base)
	}
	if len(kept) > 0 {
		slog.Info("seo files kept", "existing", strings.Join(kept, ","))
	}
	return nil
}

// seoFileBody 按名字生成内容。base 为空时退到相对地址（安装时还不知道域名）。
func (s *Server) seoFileBody(ctx context.Context, name, base string, info seoData) string {
	switch name {
	case "robots.txt":
		return robotsBody(base)
	case "security.txt":
		return s.securityBody(ctx, base)
	case "llms.txt":
		return llmsBody(base, info)
	}
	return ""
}

// siteBase 配置里的站点地址。
//
// 两处来源共用同一个值：后台「站点信息 → 站点地址」（admin 可随时改）优先，
// 其次是启动参数 -public-url（Docker 里配的兜底）。都空就退回按请求推断。
func (s *Server) siteBase(ctx context.Context) string {
	if v, _ := s.st.GetSetting(ctx, "site.base_url"); v != "" {
		return strings.TrimSuffix(strings.TrimSpace(v), "/")
	}
	return strings.TrimSuffix(strings.TrimSpace(s.PublicURL), "/")
}

// ---------- 路由 ----------

// serveSEOFile 优先读磁盘上的文件，没有就现生成一份。
// 文件优先是为了让管理员手改的规则立刻生效，不用重启。
func (s *Server) serveSEOFile(name string, gen func(base string) string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		// 手改之后要能较快看到效果，一小时足够。
		w.Header().Set("Cache-Control", "public, max-age=3600")
		if dir := s.seoFilesDir(); dir != "" {
			if b, err := os.ReadFile(filepath.Join(dir, name)); err == nil && len(b) > 0 {
				_, _ = w.Write(b)
				return
			}
		}
		fmt.Fprint(w, gen(s.siteBaseURL(r)))
	}
}

func (s *Server) handleRobotsTxt(w http.ResponseWriter, r *http.Request) {
	base := s.siteBaseURL(r)
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.Header().Set("Cache-Control", "public, max-age=3600")
	if dir := s.seoFilesDir(); dir != "" {
		if b, err := os.ReadFile(filepath.Join(dir, "robots.txt")); err == nil && len(b) > 0 {
			body := string(b)
			// 文件里没写 Sitemap 就补一行：手改过规则的人不必记得回来加，
			// 而自己写了 Sitemap（可能指向别处）的就不动它。
			if base != "" && !strings.Contains(body, "Sitemap:") {
				body = strings.TrimRight(body, "\n") + "\n\nSitemap: " + base + sitemapPath + "\n"
			}
			_, _ = io.WriteString(w, body)
			return
		}
	}
	fmt.Fprint(w, robotsBody(base))
}

func (s *Server) handleSecurityTxt(w http.ResponseWriter, r *http.Request) {
	s.serveSEOFile("security.txt", func(base string) string {
		return s.securityBody(r.Context(), base)
	})(w, r)
}

func (s *Server) handleLLMsTxt(w http.ResponseWriter, r *http.Request) {
	s.serveSEOFile("llms.txt", func(base string) string {
		return llmsBody(base, s.seoInfo(r.Context()))
	})(w, r)
}

// handleSitemap GET /sitemap.xml
//
// 只列站点自己的可收录页面。**不要把索引里的文档塞进来**：那些 URL 属于别人，
// 声明在自家 sitemap 里既没意义（我们不是它们的站长），也会让搜索引擎误判重复内容。
func (s *Server) handleSitemap(w http.ResponseWriter, r *http.Request) {
	base := s.siteBaseURL(r)
	set := sitemapURLSet{
		Xmlns: "http://www.sitemaps.org/schemas/sitemap/0.9",
		URLs:  sitemapEntries(base),
	}
	body, err := xml.MarshalIndent(set, "", "  ")
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "生成 sitemap 失败")
		return
	}
	w.Header().Set("Content-Type", "application/xml; charset=utf-8")
	w.Header().Set("Cache-Control", "public, max-age=3600")
	_, _ = w.Write([]byte(xml.Header))
	_, _ = w.Write(body)
}

// sitemapURLSet sitemap 根元素。
type sitemapURLSet struct {
	XMLName xml.Name       `xml:"urlset"`
	Xmlns   string         `xml:"xmlns,attr"`
	URLs    []sitemapEntry `xml:"url"`
}

type sitemapEntry struct {
	Loc        string `xml:"loc"`
	Changefreq string `xml:"changefreq,omitempty"`
	Priority   string `xml:"priority,omitempty"`
}

// sitemapEntries 可收录页面清单，与 robots.txt 的 Disallow 保持一致
// （搜索结果页、账号页、后台都不在内）。
func sitemapEntries(base string) []sitemapEntry {
	pages := []struct{ path, freq, priority string }{
		{"/", "daily", "1.0"},
		{"/help", "monthly", "0.6"},
		{"/api-docs", "monthly", "0.6"},
		{"/usage", "daily", "0.5"},
		{"/contribute", "monthly", "0.4"},
		{"/privacy", "yearly", "0.3"},
	}
	out := make([]sitemapEntry, 0, len(pages))
	for _, p := range pages {
		out = append(out, sitemapEntry{
			Loc:        base + p.path,
			Changefreq: p.freq,
			Priority:   p.priority,
		})
	}
	return out
}

// ---------- 内容 ----------

// robotsBody 爬虫规则。搜索结果页（?q=）不开放：参数化 URL 无穷多且内容重复，
// 放爬虫进来只会耗光配额。接口与后台一律不开放。
func robotsBody(base string) string {
	var b strings.Builder
	b.WriteString("User-agent: *\n")
	b.WriteString("Allow: /\n")
	b.WriteString("Disallow: /api/\n")
	b.WriteString("Disallow: /admin/\n")
	b.WriteString("Disallow: /manage/\n")
	b.WriteString("Disallow: /login\n")
	b.WriteString("Disallow: /register\n")
	b.WriteString("Disallow: /settings\n")
	// 搜索结果页：参数化 URL，抓不完且内容重复
	b.WriteString("Disallow: /*?q=\n")
	// Sitemap 必须是绝对地址，所以只有知道站点地址时才写。
	if base != "" {
		b.WriteString("\nSitemap: " + base + sitemapPath + "\n")
	}
	return b.String()
}

// securityBody RFC 9116。联系地址优先用后台「管理员邮箱」，没填退到帮助页 URL——
// 空着不写等于没配，这个文件必须有 Contact 才合规。
func (s *Server) securityBody(ctx context.Context, base string) string {
	email, _ := s.st.GetSetting(ctx, "site.admin_email")
	email = strings.TrimSpace(email)

	contact := "/help"
	if base != "" {
		contact = base + "/help"
	}
	if strings.Contains(email, "@") {
		contact = "mailto:" + email
	}

	var b strings.Builder
	fmt.Fprintf(&b, "Contact: %s\n", contact)
	// Canonical 必须是绝对地址；安装时还不知道域名就先不写，
	// 配好站点地址后用 -emit-web-files 重写一次即可。
	if base != "" {
		fmt.Fprintf(&b, "Canonical: %s/.well-known/security.txt\n", base)
	}
	fmt.Fprintf(&b, "Preferred-Languages: zh, en\n")
	// Expires 必须带且不能太远：过期之后扫描器会认为这份失效。
	// 没有漏洞披露流程就不写 Policy——挂一个无关页面比不写更糟。
	fmt.Fprintf(&b, "Expires: %s\n", time.Now().UTC().AddDate(1, 0, 0).Format(time.RFC3339))
	return b.String()
}

// llmsBody 给大模型看的站点说明书（llmstxt.org 的格式）。
// 刻意短：这个文件越占上下文，越违背它的目的。
func llmsBody(base string, info seoData) string {
	abs := func(p string) string {
		if base == "" {
			return p
		}
		return base + p
	}
	var b strings.Builder
	fmt.Fprintf(&b, "# %s\n\n", info.Name)
	fmt.Fprintf(&b, "> %s\n\n", info.Description)
	if info.Docs > 0 {
		fmt.Fprintf(&b, "当前收录 %d 篇文章，覆盖 %d 个站点（仅人工审核过的技术站点）。\n\n",
			info.Docs, info.Sites)
	}
	b.WriteString("## 主要页面\n")
	fmt.Fprintf(&b, "- [%s](%s)：搜索入口\n", "首页", abs("/"))
	fmt.Fprintf(&b, "- [%s](%s)：搜索语法、排除标签与时间筛选\n", "帮助", abs("/help"))
	fmt.Fprintf(&b, "- [%s](%s)：公开检索接口与限流\n", "API 文档", abs("/api-docs"))
	fmt.Fprintf(&b, "- [%s](%s)：每日检索/抓取统计\n", "用量", abs("/usage"))
	fmt.Fprintf(&b, "- [%s](%s)：申请把站点加入白名单\n", "提交站点", abs("/contribute"))
	fmt.Fprintf(&b, "- [%s](%s)\n\n", "隐私政策", abs("/privacy"))

	b.WriteString("## 检索接口\n")
	b.WriteString("- `GET /api/search?q=&exclude=ai,platform&since=7d&page=1&size=10`：JSON 检索\n")
	b.WriteString("- `GET /api/suggest?q=`：搜索补全\n")
	b.WriteString("- `POST /api/summary`：单页证据链摘要（按需调用）\n")
	b.WriteString("- 每 IP 每分钟 120 次；完整参数见 /api-docs\n\n")

	b.WriteString("## 使用约束\n")
	fmt.Fprintf(&b, "- 遵守 %s：/api/、/admin/、/manage/ 与搜索结果页（?q=）不开放自动抓取\n", abs("/robots.txt"))
	fmt.Fprintf(&b, "- 搜索关键词不落库，见 %s\n", abs("/privacy"))
	b.WriteString("- 收录内容的版权归原站所有；原文快照只用于证据引用与灾难恢复\n")
	fmt.Fprintf(&b, "- 安全问题见 %s\n", abs(securityPath))
	return b.String()
}
