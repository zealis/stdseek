// robots.txt / security.txt / llms.txt
//
// 三个文件都是「机器读的站点门面」，由服务端生成而不是塞进前端产物：
// 内容里有站点名、收录规模、联系邮箱这些随时会变的东西，
// 写死在 dist 里就等于每次改都要重新构建一次。
package api

import (
	"fmt"
	"net/http"
	"strings"
	"time"
)

// securityPath RFC 9116 规定的位置（/security.txt 只是兼容旧做法）。
const securityPath = "/.well-known/security.txt"

// handleRobotsTxt GET /robots.txt
//
// 搜索结果页（?q=）不开放：它们无穷多且内容重复，爬虫进来只会耗光配额。
// 接口与后台一律不开放。
func (s *Server) handleRobotsTxt(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	// 内容几乎不改，但改了要能立刻生效：一小时足够。
	w.Header().Set("Cache-Control", "public, max-age=3600")
	fmt.Fprint(w, "User-agent: *\n")
	fmt.Fprint(w, "Allow: /\n")
	fmt.Fprint(w, "Disallow: /api/\n")
	fmt.Fprint(w, "Disallow: /admin/\n")
	fmt.Fprint(w, "Disallow: /manage/\n")
	fmt.Fprint(w, "Disallow: /login\n")
	fmt.Fprint(w, "Disallow: /register\n")
	fmt.Fprint(w, "Disallow: /settings\n")
	// 搜索结果页：参数化 URL，抓不完且内容重复
	fmt.Fprint(w, "Disallow: /*?q=\n")
}

// handleSecurityTxt GET /.well-known/security.txt（兼容 /security.txt）
//
// 联系地址优先用后台「站点信息 → 管理员邮箱」；没填就退到帮助页 URL——
// 空着不写等于没配，该文件必须有 Contact 才合规。
func (s *Server) handleSecurityTxt(w http.ResponseWriter, r *http.Request) {
	base := siteBaseURL(r)
	email, _ := s.st.GetSetting(r.Context(), "site.admin_email")
	email = strings.TrimSpace(email)

	contact := "/help"
	canonical := securityPath
	if base != "" {
		contact = base + "/help"
		canonical = base + securityPath
	}
	if strings.Contains(email, "@") {
		contact = "mailto:" + email
	}

	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.Header().Set("Cache-Control", "public, max-age=3600")
	fmt.Fprintf(w, "Contact: %s\n", contact)
	fmt.Fprintf(w, "Canonical: %s\n", canonical)
	fmt.Fprintf(w, "Preferred-Languages: zh, en\n")
	// Expires 必须带且不能太远：过期之后扫描器会认为这份失效。
	// 没有漏洞披露流程就不写 Policy——挂一个无关页面比不写更糟。
	fmt.Fprintf(w, "Expires: %s\n", time.Now().UTC().AddDate(1, 0, 0).Format(time.RFC3339))
}

// handleLLMsTxt GET /llms.txt：给大模型看的站点说明书（llmstxt.org 的格式）。
// 标题 + 一句话说明 + 主要页面 + 检索接口 + 使用约束，尽量短，
// 长文件会挤占上下文，那和它的目的相反。
func (s *Server) handleLLMsTxt(w http.ResponseWriter, r *http.Request) {
	base := siteBaseURL(r)
	info := s.seoInfo(r.Context())
	abs := func(p string) string { return base + p }

	var b strings.Builder
	fmt.Fprintf(&b, "# %s\n\n", info.Name)
	fmt.Fprintf(&b, "> %s\n\n", info.Description)
	if info.Docs > 0 {
		fmt.Fprintf(&b, "当前收录 %d 篇文章，覆盖 %d 个站点（仅人工审核过的技术站点）。\n\n",
			info.Docs, info.Sites)
	}
	fmt.Fprint(&b, "## 主要页面\n")
	fmt.Fprintf(&b, "- [%s](%s)：搜索入口\n", "首页", abs("/"))
	fmt.Fprintf(&b, "- [%s](%s)：搜索语法、排除标签与时间筛选\n", "帮助", abs("/help"))
	fmt.Fprintf(&b, "- [%s](%s)：公开检索接口与限流\n", "API 文档", abs("/api-docs"))
	fmt.Fprintf(&b, "- [%s](%s)：每日检索/抓取统计\n", "用量", abs("/usage"))
	fmt.Fprintf(&b, "- [%s](%s)：申请把站点加入白名单\n", "提交站点", abs("/contribute"))
	fmt.Fprintf(&b, "- [%s](%s)\n\n", "隐私政策", abs("/privacy"))

	fmt.Fprint(&b, "## 检索接口\n")
	fmt.Fprint(&b, "- `GET /api/search?q=&exclude=ai,platform&since=7d&page=1&size=10`：JSON 检索\n")
	fmt.Fprint(&b, "- `GET /api/suggest?q=`：搜索补全\n")
	fmt.Fprint(&b, "- `POST /api/summary`：单页证据链摘要（按需调用）\n")
	fmt.Fprint(&b, "- 每 IP 每分钟 120 次；完整参数见 /api-docs\n\n")

	fmt.Fprint(&b, "## 使用约束\n")
	fmt.Fprintf(&b, "- 遵守 %s：/api/、/admin/、/manage/ 与搜索结果页（?q=）不开放自动抓取\n", abs("/robots.txt"))
	fmt.Fprintf(&b, "- 搜索关键词不落库，见 %s\n", abs("/privacy"))
	fmt.Fprint(&b, "- 收录内容的版权归原站所有；原文快照只用于证据引用与灾难恢复\n")
	fmt.Fprintf(&b, "- 安全问题见 %s\n", abs(securityPath))

	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.Header().Set("Cache-Control", "public, max-age=3600")
	fmt.Fprint(w, b.String())
}
