package api

import (
	"crypto/subtle"
	"net"
	"net/http"
	"regexp"
	"strings"

	"github.com/go-chi/chi/v5"
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

// adminRouter 后台路由（统一挂鉴权）。
func (s *Server) adminRouter() http.Handler {
	r := chi.NewRouter()
	r.Use(adminAuth(s.AdminToken))
	r.Get("/whitelist", s.handleListWhitelist)
	r.Post("/whitelist", s.handleAddWhitelist)
	r.Post("/whitelist/{id}/toggle", s.handleToggleWhitelist)
	r.Get("/blacklist", s.handleListBlacklist)
	r.Post("/blacklist", s.handleAddBlacklist)
	r.Delete("/blacklist/{id}", s.handleRemoveBlacklist)
	r.Get("/review", s.handleListReview)
	r.Post("/review/{id}/approve", s.handleApproveReview)
	r.Post("/review/{id}/reject", s.handleRejectReview)
	return r
}
