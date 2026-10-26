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

// adminAuth 校验后台令牌。token 为空时退化为「不校验」（仅供测试/本地开发），
// 生产入口 main 会强制生成或读取令牌。
func adminAuth(token string) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
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
	r.Use(adminAuth(s.AdminToken))
	r.Post("/precheck", s.handlePrecheck)
	r.Get("/whitelist", s.handleListWhitelist)
	r.Post("/whitelist", s.handleAddWhitelist)
	r.Post("/whitelist/{id}/toggle", s.handleToggleWhitelist)
	r.Get("/blacklist", s.handleListBlacklist)
	r.Post("/blacklist", s.handleAddBlacklist)
	r.Delete("/blacklist/{id}", s.handleRemoveBlacklist)
	r.Get("/review", s.handleListReview)
	r.Post("/review/{id}/precheck", s.handleReviewPrecheck)
	r.Post("/review/{id}/approve", s.handleApproveReview)
	r.Post("/review/{id}/reject", s.handleRejectReview)
	r.Get("/label-feedback", s.handleListLabelFeedback)
	r.Post("/label-feedback/delete", s.handleDeleteLabelFeedback)
	// 功能开关：后台唯一能改运行期配置的入口（AI 摘要等）
	r.Get("/settings", s.handleListFeatures)
	r.Post("/settings/feature", s.handleSetFeature)
	// 每日用量：检索 / 抓取 / 入库 / 摘要的按天累计
	r.Get("/usage", s.handleUsage)
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
	writeJSON(w, http.StatusOK, map[string]interface{}{
		"days": days,
		"rows": rows,
	})
}
