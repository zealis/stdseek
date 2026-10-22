// Package api 提供 chi HTTP 路由：检索、用户提交网站、管理后台（白名单/黑名单/审核队列）。
// 路径设计与规划书一致：/admin/sites /admin/review /admin/whitelist /admin/blacklist。
package api

import (
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"

	"voidxo/internal/index"
	"voidxo/internal/rank"
	"voidxo/internal/store"
)

// Server 聚合依赖。
type Server struct {
	st  *store.Store
	idx *index.Index
	rk  *rank.Ranker
	// SubmitEnabled 控制 /api/submit 是否开放。
	// 红线：首批 20-50 站质量验证前默认关闭，避免审核队列成为烂尾功能。
	SubmitEnabled bool
}

// NewServer 构造 API 服务。
func NewServer(st *store.Store, idx *index.Index, submitEnabled bool) *Server {
	return &Server{st: st, idx: idx, rk: rank.New(rank.DefaultWeights()), SubmitEnabled: submitEnabled}
}

// NewRouter 装配路由与中间件。
func NewRouter(s *Server) http.Handler {
	r := chi.NewRouter()
	r.Use(middleware.RequestID)
	r.Use(middleware.RealIP)
	r.Use(middleware.Recoverer)
	r.Use(middleware.Timeout(30 * time.Second))
	r.Use(jsonContentType)

	r.Get("/healthz", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
	})
	// Prometheus 打点占位：骨架阶段先暴露健康指标，正式指标由 internal/metrics 迭代接入
	r.Get("/metrics", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/plain; version=0.0.4")
		w.Write([]byte("# voidxo metrics placeholder\nvoidxo_up 1\n"))
	})

	r.Route("/api", func(r chi.Router) {
		r.Get("/search", s.handleSearch)
		r.Post("/submit", s.handleSubmit)
	})

	r.Route("/admin", func(r chi.Router) {
		r.Get("/whitelist", s.handleListWhitelist)
		r.Post("/whitelist", s.handleAddWhitelist)
		r.Post("/whitelist/{id}/toggle", s.handleToggleWhitelist)

		r.Get("/blacklist", s.handleListBlacklist)
		r.Post("/blacklist", s.handleAddBlacklist)
		r.Delete("/blacklist/{id}", s.handleRemoveBlacklist)

		r.Get("/review", s.handleListReview)
		r.Post("/review/{id}/approve", s.handleApproveReview)
		r.Post("/review/{id}/reject", s.handleRejectReview)
	})
	return r
}

// ---------- 检索 ----------

// handleSearch GET /api/search?q=...&no=ai,ugc_platform&since=86400&page=1&size=10
// no: 排除型标签开关（ai_suspect/ad_heavy/personal/ugc_platform 的简写映射）。
// since: 时间窗秒数（86400/259200/604800/2592000），0 或缺省为全部。
func (s *Server) handleSearch(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query().Get("q")
	if strings.TrimSpace(q) == "" {
		writeErr(w, http.StatusBadRequest, "缺少查询参数 q")
		return
	}
	exclude := expandLabels(splitCSV(r.URL.Query().Get("no")))
	since, _ := strconv.ParseInt(r.URL.Query().Get("since"), 10, 64)
	page, _ := strconv.Atoi(r.URL.Query().Get("page"))
	size, _ := strconv.Atoi(r.URL.Query().Get("size"))

	if page <= 0 {
		page = 1
	}
	if size <= 0 || size > 50 {
		size = 10
	}
	// 先放大候选池再重排序（BM25 召回 → 可解释排序），排名质量依赖候选宽度
	pool := size * 5
	if pool < 20 {
		pool = 20
	}
	if pool > 100 {
		pool = 100
	}
	hits, total, err := s.idx.Search(index.Query{
		Q: q, Exclude: exclude, SinceSec: since, Page: 1, Size: pool,
	})
	if err != nil {
		slog.Error("search failed", "err", err)
		writeErr(w, http.StatusInternalServerError, "检索失败")
		return
	}

	domains := make([]string, 0, len(hits))
	for _, h := range hits {
		domains = append(domains, h.Domain)
	}
	cats, err := s.st.DomainCategories(r.Context(), domains)
	if err != nil {
		slog.Warn("domain categories failed", "err", err)
	}
	ranked := s.rk.Rank(q, hits, cats, time.Now())

	start := (page - 1) * size
	if start > len(ranked) {
		start = len(ranked)
	}
	end := start + size
	if end > len(ranked) {
		end = len(ranked)
	}
	items := make([]searchItem, 0, end-start)
	for _, res := range ranked[start:end] {
		items = append(items, searchItem{Hit: res.Hit, Score: res.Score,
			Reasons: res.Reasons, Breakdown: res.Breakdown})
	}

	writeJSON(w, http.StatusOK, map[string]interface{}{
		"q":     q,
		"total": total,
		"page":  page,
		"hits":  items,
	})
}

// expandLabels 把结果页开关简写映射为索引期写入的 labels 值。
func expandLabels(in []string) []string {
	m := map[string]string{
		"ai":       "ai_suspect",
		"ads":      "ad_heavy",
		"personal": "personal",
		"platform": "ugc_platform",
	}
	var out []string
	for _, x := range in {
		if v, ok := m[x]; ok {
			out = append(out, v)
		} else if x != "" {
			out = append(out, x) // 允许直接传完整 label
		}
	}
	return out
}

// ---------- 用户提交 ----------

type submitReq struct {
	URL      string `json:"url"`
	Category string `json:"category"`
	Reason   string `json:"reason"`
}

// searchItem 检索结果（扁平化便于前端渲染，保留 Hit 内嵌字段）。
type searchItem struct {
	index.Hit
	Score     float64        `json:"score"`
	Reasons   []string       `json:"reasons"`
	Breakdown rank.Breakdown `json:"breakdown"`
}

// handleSubmit POST /api/submit。默认 403 关闭（红线 6）。
func (s *Server) handleSubmit(w http.ResponseWriter, r *http.Request) {
	if !s.SubmitEnabled {
		writeErr(w, http.StatusForbidden, "网站提交暂未开放（首批白名单质量验证中）")
		return
	}
	var req submitReq
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErr(w, http.StatusBadRequest, "请求体不是合法 JSON")
		return
	}
	domain := domainOf(req.URL)
	if domain == "" {
		writeErr(w, http.StatusBadRequest, "URL 不合法")
		return
	}
	id, err := s.st.CreateSubmission(r.Context(), store.ReviewItem{
		URL: req.URL, Domain: domain, Category: req.Category, Reason: req.Reason,
	})
	if err != nil {
		slog.Error("submit failed", "err", err)
		writeErr(w, http.StatusInternalServerError, "提交失败")
		return
	}
	writeJSON(w, http.StatusOK, map[string]interface{}{"id": id, "status": "pending"})
}

// ---------- 管理：白名单 ----------

func (s *Server) handleListWhitelist(w http.ResponseWriter, r *http.Request) {
	limit, offset := pageParams(r)
	items, total, err := s.st.ListWhitelist(r.Context(), r.URL.Query().Get("kw"), limit, offset)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "查询白名单失败")
		return
	}
	writeJSON(w, http.StatusOK, map[string]interface{}{"total": total, "items": items})
}

type whitelistReq struct {
	Domain        string `json:"domain"`
	Category      string `json:"category"`
	CrawlInterval int    `json:"crawl_interval"`
}

func (s *Server) handleAddWhitelist(w http.ResponseWriter, r *http.Request) {
	var req whitelistReq
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErr(w, http.StatusBadRequest, "请求体不是合法 JSON")
		return
	}
	req.Domain = strings.TrimSpace(strings.ToLower(req.Domain))
	if req.Domain == "" || strings.ContainsAny(req.Domain, "/: ") {
		writeErr(w, http.StatusBadRequest, "domain 不合法（应为主机名，如 go.dev）")
		return
	}
	if req.Category == "" {
		req.Category = "blog"
	}
	if err := s.st.AddWhitelist(r.Context(), req.Domain, req.Category, req.CrawlInterval); err != nil {
		writeErr(w, http.StatusInternalServerError, "写入白名单失败")
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

func (s *Server) handleToggleWhitelist(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	var body struct {
		Enabled bool `json:"enabled"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeErr(w, http.StatusBadRequest, "请求体不是合法 JSON")
		return
	}
	if err := s.st.SetWhitelistEnabled(r.Context(), id, body.Enabled); err != nil {
		writeStoreErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

// ---------- 管理：黑名单 ----------

func (s *Server) handleListBlacklist(w http.ResponseWriter, r *http.Request) {
	limit, offset := pageParams(r)
	items, total, err := s.st.ListBlacklist(r.Context(), r.URL.Query().Get("kw"), limit, offset)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "查询黑名单失败")
		return
	}
	writeJSON(w, http.StatusOK, map[string]interface{}{"total": total, "items": items})
}

func (s *Server) handleAddBlacklist(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Domain string `json:"domain"`
		Reason string `json:"reason"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErr(w, http.StatusBadRequest, "请求体不是合法 JSON")
		return
	}
	req.Domain = strings.TrimSpace(strings.ToLower(req.Domain))
	if req.Domain == "" || strings.ContainsAny(req.Domain, "/: ") {
		writeErr(w, http.StatusBadRequest, "domain 不合法")
		return
	}
	if err := s.st.AddBlacklist(r.Context(), req.Domain, req.Reason); err != nil {
		writeErr(w, http.StatusInternalServerError, "写入黑名单失败")
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

func (s *Server) handleRemoveBlacklist(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	if err := s.st.RemoveBlacklist(r.Context(), id); err != nil {
		writeStoreErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

// ---------- 管理：审核队列 ----------

func (s *Server) handleListReview(w http.ResponseWriter, r *http.Request) {
	limit, offset := pageParams(r)
	items, total, err := s.st.ListReview(r.Context(),
		r.URL.Query().Get("status"), r.URL.Query().Get("kw"), limit, offset)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "查询审核队列失败")
		return
	}
	writeJSON(w, http.StatusOK, map[string]interface{}{"total": total, "items": items})
}

func (s *Server) handleApproveReview(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	if err := s.st.ApproveReview(r.Context(), id); err != nil {
		writeStoreErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "approved"})
}

func (s *Server) handleRejectReview(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	var body struct {
		Reason string `json:"reason"`
	}
	json.NewDecoder(r.Body).Decode(&body) // reason 可空
	if err := s.st.RejectReview(r.Context(), id, body.Reason); err != nil {
		writeStoreErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "rejected"})
}

// ---------- 工具 ----------

func jsonContentType(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		next.ServeHTTP(w, r)
	})
}

func writeJSON(w http.ResponseWriter, code int, v interface{}) {
	w.WriteHeader(code)
	json.NewEncoder(w).Encode(v)
}

func writeErr(w http.ResponseWriter, code int, msg string) {
	writeJSON(w, code, map[string]string{"error": msg})
}

func writeStoreErr(w http.ResponseWriter, err error) {
	if errors.Is(err, strconv.ErrSyntax) {
		writeErr(w, http.StatusBadRequest, "id 不合法")
		return
	}
	writeErr(w, http.StatusBadRequest, err.Error())
}

func pathID(w http.ResponseWriter, r *http.Request) (int64, bool) {
	id, err := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
	if err != nil {
		writeErr(w, http.StatusBadRequest, "id 不合法")
		return 0, false
	}
	return id, true
}

func pageParams(r *http.Request) (limit, offset int) {
	page, _ := strconv.Atoi(r.URL.Query().Get("page"))
	size, _ := strconv.Atoi(r.URL.Query().Get("size"))
	if page <= 0 {
		page = 1
	}
	if size <= 0 || size > 100 {
		size = 20
	}
	return size, (page - 1) * size
}

func splitCSV(s string) []string {
	if s == "" {
		return nil
	}
	var out []string
	for _, x := range strings.Split(s, ",") {
		if x = strings.TrimSpace(x); x != "" {
			out = append(out, x)
		}
	}
	return out
}

// domainOf 从 URL 提取主机名（小写、去端口）。非法输入返回空串。
func domainOf(raw string) string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return ""
	}
	if !strings.Contains(raw, "://") {
		raw = "https://" + raw
	}
	rest := strings.SplitN(raw, "://", 2)[1]
	host := strings.SplitN(rest, "/", 2)[0]
	host = strings.SplitN(host, ":", 2)[0]
	return strings.ToLower(host)
}
