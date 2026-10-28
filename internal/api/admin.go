package api

import (
	"crypto/subtle"
	"encoding/json"
	"log/slog"
	"net"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"

	"voidxo/internal/precheck"
	"voidxo/internal/store"
)

// adminAuth 后台鉴权：管理员会话优先，启动令牌作为应急入口。
//
// 两条路都留着是有意的：日常用账号登录（可撤销、可追责、支持两步验证），
// 令牌则是「密码忘了/账号锁了」时不必动数据库就能进后台的最后一道门。
func (s *Server) adminAuth() func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if u, ok := s.currentUser(r); ok && u.Role == "admin" {
				next.ServeHTTP(w, r)
				return
			}
			token := s.AdminToken
			if token == "" {
				next.ServeHTTP(w, r)
				return
			}
			got := r.Header.Get("X-Admin-Token")
			if got == "" {
				if h := r.Header.Get("Authorization"); strings.HasPrefix(h, "Bearer ") {
					got = strings.TrimPrefix(h, "Bearer ")
				}
			}
			if subtle.ConstantTimeCompare([]byte(got), []byte(token)) != 1 {
				w.Header().Set("WWW-Authenticate", "Bearer")
				writeErr(w, http.StatusUnauthorized, "需要管理员令牌")
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}

// 域名标签：字母数字开头结尾，允许连字符。
var domainLabel = regexp.MustCompile(`^[a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?$`)

// validPublicDomain 校验提交的域名：拒绝 IP 字面量、localhost/内网后缀与畸形主机名，
// 防止后台把内网地址写进白名单后由爬虫触发 SSRF。
func validPublicDomain(host string) bool {
	host = strings.TrimSuffix(strings.ToLower(strings.TrimSpace(host)), ".")
	if host == "" || len(host) > 253 || len(host) < 4 {
		return false
	}
	if net.ParseIP(host) != nil {
		return false
	}
	for _, bad := range []string{"localhost", ".local", ".internal", ".localhost", ".lan", ".home"} {
		if host == strings.TrimPrefix(bad, ".") || strings.HasSuffix(host, bad) {
			return false
		}
	}
	if !strings.Contains(host, ".") {
		return false
	}
	for _, label := range strings.Split(host, ".") {
		if !domainLabel.MatchString(label) {
			return false
		}
	}
	// 顶级域不能是纯数字（排除 123.456 之类）
	tld := host[strings.LastIndex(host, ".")+1:]
	if regexp.MustCompile(`^[0-9]+$`).MatchString(tld) {
		return false
	}
	return true
}

// handlePrecheck POST /admin/precheck {url}：提交前预检（可达性/robots/内容类型/黑名单历史）。
// 只允许公开域名，避免把预检变成内网探测工具。
func (s *Server) handlePrecheck(w http.ResponseWriter, r *http.Request) {
	maxBody(w, r)
	var req struct {
		URL string `json:"url"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErr(w, http.StatusBadRequest, "请求体不是合法 JSON")
		return
	}
	raw := strings.TrimSpace(req.URL)
	if !strings.Contains(raw, "://") {
		raw = "https://" + raw
	}
	domain := domainOf(raw)
	if !validPublicDomain(domain) {
		writeErr(w, http.StatusBadRequest, "URL 不合法（需为公开域名，禁止 IP/内网地址）")
		return
	}

	res := precheck.Check(r.Context(), raw, "voidxo-precheck/0.1", 5*time.Second)
	blacklisted, err := s.st.IsBlacklisted(r.Context(), domain)
	if err != nil {
		slog.Warn("blacklist check failed", "domain", domain, "err", err)
	}
	writeJSON(w, http.StatusOK, map[string]interface{}{
		"precheck":    res,
		"blacklisted": blacklisted,
	})
}

// adminRouter 后台路由（统一挂鉴权）。
func (s *Server) adminRouter() http.Handler {
	r := chi.NewRouter()
	r.Use(s.adminAuth())
	r.Post("/precheck", s.handlePrecheck)
	r.Get("/whitelist", s.handleListWhitelist)
	r.Post("/whitelist", s.handleAddWhitelist)
	r.Post("/whitelist/{id}/toggle", s.handleToggleWhitelist)
	// 抓取异常名单：无法访问 / robots 规则变更 / 页面结构变化
	r.Get("/issues", s.handleListCrawlIssues)
	r.Post("/issues/recrawl", s.handleRecrawlIssues)
	r.Post("/issues/clear", s.handleClearCrawlIssues)
	r.Get("/blacklist", s.handleListBlacklist)
	r.Post("/blacklist", s.handleAddBlacklist)
	r.Delete("/blacklist/{id}", s.handleRemoveBlacklist)
	r.Get("/review", s.handleListReview)
	r.Post("/review/{id}/precheck", s.handleReviewPrecheck)
	r.Post("/review/{id}/approve", s.handleApproveReview)
	r.Post("/review/{id}/reject", s.handleRejectReview)
	r.Get("/label-feedback", s.handleListLabelFeedback)
	r.Post("/label-feedback/delete", s.handleDeleteLabelFeedback)
	r.Post("/label-feedback/update", s.handleUpdateLabelFeedback)
	// 功能开关：后台唯一能改运行期配置的入口（AI 摘要等）
	r.Get("/settings", s.handleListFeatures)
	r.Post("/settings/feature", s.handleSetFeature)
	// AI 站点分类：接口配置（落 DB，改完即时生效）+ 批量判定
	r.Get("/ai", s.handleGetAIConfig)
	r.Post("/ai", s.handleSetAIConfig)
	r.Post("/ai/test", s.handleTestAI)
	r.Post("/ai/classify", s.handleClassifySites)
	// 原文快照：开关（只停新增）+ 占用 + 清理（孤儿/全清）
	r.Get("/snapshot", s.handleSnapshotStats)
	r.Post("/snapshot", s.handleSetSnapshot)
	r.Post("/snapshot/cleanup", s.handleCleanupSnapshot)
	r.Get("/site", s.handleAdminSite)
	r.Post("/site", s.handleSaveSite)
	// 每日用量：检索 / 抓取 / 入库 / 摘要的按天累计
	r.Get("/usage", s.handleUsage)
	// 用量维度排行：API 调用来源域名、请求地区
	r.Get("/usage/dims", s.handleUsageDims)
	// 邮件（SMTP）：邮箱验证与找回密码依赖它
	r.Get("/mail", s.handleGetMailConfig)
	r.Post("/mail", s.handleSetMailConfig)
	r.Post("/mail/test", s.handleTestMail)
	// 人机验证配置（后台可选 provider、填各自的 key）
	r.Get("/captcha", s.handleGetCaptchaConfig)
	r.Post("/captcha", s.handleSetCaptchaConfig)
	r.Post("/captcha/test", s.handleTestCaptcha)
	// IP 封禁与归属地
	r.Get("/ip-bans", s.handleIPBans)
	r.Post("/ip-bans", s.handleIPBans)
	r.Delete("/ip-bans", s.handleIPBans)
	r.Get("/geo", s.handleGetGeo)
	r.Post("/geo", s.handleSetGeo)
	r.Get("/geo/risk", s.handleGeoRisk)
	r.Post("/geo/resolve", s.handleResolveGeo)
	r.Get("/accounts-by-ip", s.handleAccountsByIP)
	// 账号管理
	r.Get("/users", s.handleListUsers)
	r.Post("/users/{id}/role", s.handleSetUserRole)
	r.Post("/users/{id}/status", s.handleSetUserStatus)
	r.Get("/users/{id}/sessions", s.handleUserSessions)
	return r
}

// handleUsage GET /admin/usage?days=30：每日用量趋势。
//
// 放在后台而不是公开页：抓取页数与入库量属于运营口径，公开只会有两种下场——
// 被竞品盯着看，或者被用户当成"你今天怎么只抓了 3 页"的质问材料。
func (s *Server) handleUsage(w http.ResponseWriter, r *http.Request) {
	if s.Usage == nil {
		writeErr(w, http.StatusNotImplemented, "用量统计未启用")
		return
	}
	days := 30
	if v := r.URL.Query().Get("days"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 && n <= 365 {
			days = n
		}
	}
	rows, err := s.Usage.Snapshot(r.Context(), days)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "读取用量失败")
		return
	}
	if rows == nil {
		rows = []store.UsageRow{}
	}
	// 累计口径与按天口径一起给：页面上「今日 X / 累计 Y」是同一张卡片，
	// 分成两个接口只会多一次往返，还可能出现两次读数不一致。
	totals, err := s.st.UsageTotals(r.Context())
	if err != nil {
		slog.Warn("usage totals failed", "err", err)
	}
	writeJSON(w, http.StatusOK, map[string]interface{}{
		"days":   days,
		"rows":   rows,
		"totals": totals,
	})
}

// handleUsageDims GET /admin/usage/dims?days=30：调用来源与请求地区的排行。
//
// 两个维度一次返回：页面是一起渲染的，分两次请求只会多一次往返。
func (s *Server) handleUsageDims(w http.ResponseWriter, r *http.Request) {
	if s.Usage == nil {
		writeErr(w, http.StatusNotImplemented, "用量统计未启用")
		return
	}
	days := 30
	if v := r.URL.Query().Get("days"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 && n <= 365 {
			days = n
		}
	}
	const topN = 10
	origins, err := s.Usage.TopDims(r.Context(), days, "origin", topN)
	if err != nil {
		slog.Warn("usage dims origins failed", "err", err)
	}
	countries, err := s.Usage.TopDims(r.Context(), days, "country", topN)
	if err != nil {
		slog.Warn("usage dims countries failed", "err", err)
	}
	if origins == nil {
		origins = []store.DimRow{}
	}
	if countries == nil {
		countries = []store.DimRow{}
	}
	writeJSON(w, http.StatusOK, map[string]interface{}{
		"days":      days,
		"origins":   origins,
		"countries": countries,
		// 回显当前是否启用了地区统计，页面上要据此提示「未开启」
		"geo_header": s.GeoHeader,
	})
}
