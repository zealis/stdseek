// 隐私政策的元数据接口。
//
// 页面正文是静态的（前端模板），这里只提供两个会变的时间：
//   - created_at：最早管理员账号创建时间，服务从那天开始运行、开始收集数据
//   - updated_at：政策最后修订时间，存在 settings 里（改一次政策写一次）
package api

import (
	"net/http"
	"strconv"
)

// handlePrivacyMeta GET /api/privacy
func (s *Server) handlePrivacyMeta(w http.ResponseWriter, r *http.Request) {
	created := int64(0)
	if s.st != nil {
		if ts, err := s.st.EarliestAdminCreatedAt(r.Context()); err == nil {
			created = ts
		}
	}
	updated := created
	if s.st != nil {
		if v, err := s.st.GetSetting(r.Context(), "privacy.updated_at"); err == nil {
			if n, e := strconv.ParseInt(v, 10, 64); e == nil && n > 0 {
				updated = n
			}
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"created_at": created,
		"updated_at": updated,
	})
}
