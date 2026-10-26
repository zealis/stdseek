package api

import (
	"net/http"
	"strconv"
)

// publicUsageRow 公开的每日用量：只含检索维度。
type publicUsageRow struct {
	Day       string `json:"day"`
	Searches  int64  `json:"searches"`
	Succeeded int64  `json:"succeeded"`
	API       int64  `json:"api"`
	Hits      int64  `json:"hits"`
}

// handlePublicUsage GET /api/usage?days=30：每日检索用量。
//
// 只暴露检索维度。抓取页数与入库量属于运营口径，留在 /admin/usage——
// 公开它们只会变成「你今天怎么只抓了 3 页」的质问材料。
func (s *Server) handlePublicUsage(w http.ResponseWriter, r *http.Request) {
	if s.Usage == nil {
		writeErr(w, http.StatusNotImplemented, "用量统计未启用")
		return
	}
	days := 30
	if v := r.URL.Query().Get("days"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 && n <= 90 {
			days = n
		}
	}
	rows, err := s.Usage.Snapshot(r.Context(), days)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "读取用量失败")
		return
	}
	out := make([]publicUsageRow, 0, len(rows))
	for _, x := range rows {
		out = append(out, publicUsageRow{
			Day:       x.Day,
			Searches:  x.Searches,
			Succeeded: x.Succeeded,
			API:       x.API,
			Hits:      x.Hits,
		})
	}
	writeJSON(w, http.StatusOK, map[string]interface{}{"days": days, "rows": out})
}
