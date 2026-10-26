package api

import (
	"log/slog"
	"net/http"
	"strings"
)

// suggestMaxRunes 前缀长度上限。补全只在输入初期有意义，
// 太长的串既不会命中也会白白消耗一次外部请求。
const suggestMaxRunes = 64

// handleSuggest GET /api/suggest?q=前缀：搜索词补全。
//
// 数据来自 Bing 的 qsonhs 接口，由服务端代理并缓存（见 internal/suggest）。
// 失败一律返回空列表 + 200：补不出来是小事，让搜索框因为补全报错才是大事。
func (s *Server) handleSuggest(w http.ResponseWriter, r *http.Request) {
	q := strings.TrimSpace(r.URL.Query().Get("q"))
	empty := map[string]interface{}{"q": q, "suggestions": []string{}}
	if s.Suggester == nil || q == "" || len([]rune(q)) > suggestMaxRunes {
		writeJSON(w, http.StatusOK, empty)
		return
	}
	items, err := s.Suggester.Suggest(r.Context(), q)
	if err != nil {
		// debug 级别：前端的按键请求量很大，warn 会把日志刷满
		slog.Debug("suggest failed", "q", q, "err", err)
		writeJSON(w, http.StatusOK, empty)
		return
	}
	if items == nil {
		items = []string{}
	}
	writeJSON(w, http.StatusOK, map[string]interface{}{"q": q, "suggestions": items})
}
