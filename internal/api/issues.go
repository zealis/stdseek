package api

import (
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"strings"

	"voidxo/internal/store"
)

// ErrCrawlBusy 已有一轮「重新抓取」在跑。
//
// 不排队是有意的：每次重抓都是对目标站点的真实请求（单站 15~80 页、域内 2 秒间隔），
// 让管理员连点几次就排起队来，等于对人家做压测。
var ErrCrawlBusy = errors.New("已有重新抓取任务在运行")

// validIssueKind 只认三类异常，别的值当「全部」——
// 前端传空串表示不筛选，拼错时不该返回 400 让人以为页面坏了。
func validIssueKind(k string) bool {
	switch k {
	case store.CrawlIssueUnreachable, store.CrawlIssueRobots, store.CrawlIssueStructure:
		return true
	}
	return false
}

// handleListCrawlIssues GET /admin/issues?kind=&kw=&page=&size=
//
// 返回的 counts 是各类型总数（不受当前筛选影响），页面拿它显示
// 「无法访问 3 / 抓取规则 1 / 结构变化 2」，不用再请求一次。
func (s *Server) handleListCrawlIssues(w http.ResponseWriter, r *http.Request) {
	limit, offset := pageParams(r)
	kind := r.URL.Query().Get("kind")
	if !validIssueKind(kind) {
		kind = ""
	}
	kw := strings.TrimSpace(r.URL.Query().Get("kw"))

	items, total, err := s.st.ListCrawlIssues(r.Context(), kind, kw, limit, offset)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "查询异常名单失败")
		return
	}
	counts, err := s.st.CrawlIssueCounts(r.Context())
	if err != nil {
		// 计数只是标签上的辅助信息，取不到也要把列表给出去
		slog.Warn("crawl issue counts failed", "err", err)
	}
	if items == nil {
		items = []store.CrawlIssue{}
	}
	if counts == nil {
		counts = map[string]int{}
	}
	writeJSON(w, http.StatusOK, map[string]interface{}{
		"total":  total,
		"items":  items,
		"counts": counts,
	})
}

// issueDomainsReq 「重新抓取」「移出名单」共用的请求体。
type issueDomainsReq struct {
	Domains []string `json:"domains"`
}

// decodeIssueDomains 解析并归一化域名列表（最多 100 个，去重、转小写）。
func decodeIssueDomains(w http.ResponseWriter, r *http.Request) ([]string, bool) {
	if !sameOrigin(r) {
		writeErr(w, http.StatusForbidden, "跨站请求被拒绝")
		return nil, false
	}
	maxBody(w, r)
	var req issueDomainsReq
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErr(w, http.StatusBadRequest, "请求体不是合法 JSON")
		return nil, false
	}
	seen := map[string]bool{}
	out := make([]string, 0, len(req.Domains))
	for _, d := range req.Domains {
		d = strings.ToLower(strings.TrimSpace(d))
		if d == "" || seen[d] {
			continue
		}
		seen[d] = true
		out = append(out, d)
		if len(out) >= 100 {
			break
		}
	}
	if len(out) == 0 {
		writeErr(w, http.StatusBadRequest, "没有指定域名")
		return nil, false
	}
	return out, true
}

// handleRecrawlIssues POST /admin/issues/recrawl {domains}
//
// 立即返回、后台异步跑：单站 15~80 页、域内每次请求间隔 2 秒，
// 同步等会把 HTTP 请求拖到几分钟，浏览器早就超时了。
// 结果看列表刷新（恢复正常会自动从名单里消失）。
func (s *Server) handleRecrawlIssues(w http.ResponseWriter, r *http.Request) {
	if s.RecrawlSites == nil {
		writeErr(w, http.StatusNotImplemented, "爬虫未启用，无法重新抓取")
		return
	}
	domains, ok := decodeIssueDomains(w, r)
	if !ok {
		return
	}
	// 只允许白名单内的域名：这个接口不能让后台变成「抓任意站点」的入口
	listed := make([]string, 0, len(domains))
	for _, d := range domains {
		wl, err := s.st.IsWhitelisted(r.Context(), d)
		if err != nil {
			slog.Warn("whitelist check failed", "domain", d, "err", err)
			continue
		}
		if wl {
			listed = append(listed, d)
		}
	}
	if len(listed) == 0 {
		writeErr(w, http.StatusBadRequest, "这些域名已不在白名单内，无法重新抓取")
		return
	}
	if err := s.RecrawlSites(listed); err != nil {
		if errors.Is(err, ErrCrawlBusy) {
			writeErr(w, http.StatusConflict, "已有一轮重新抓取在跑，请等它结束后再试")
			return
		}
		slog.Warn("recrawl start failed", "err", err)
		writeErr(w, http.StatusInternalServerError, "启动重新抓取失败")
		return
	}
	writeJSON(w, http.StatusOK, map[string]interface{}{
		"status": "queued",
		"count":  len(listed),
	})
}

// handleClearCrawlIssues POST /admin/issues/clear {domains}
//
// 把站点移出名单（管理员确认过是误报，或决定不再管这个站）。
// 不做二次确认：下一轮抓取如果仍然失败，它会自己回到名单里，没有不可逆后果。
func (s *Server) handleClearCrawlIssues(w http.ResponseWriter, r *http.Request) {
	domains, ok := decodeIssueDomains(w, r)
	if !ok {
		return
	}
	cleared := 0
	for _, d := range domains {
		if err := s.st.ClearCrawlIssue(r.Context(), d); err != nil {
			slog.Warn("clear crawl issue failed", "domain", d, "err", err)
			continue
		}
		cleared++
	}
	writeJSON(w, http.StatusOK, map[string]interface{}{
		"status":  "ok",
		"cleared": cleared,
	})
}
