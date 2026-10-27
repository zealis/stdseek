// 站点信息：名称、描述、关键词、管理员邮箱。
//
// 管理员邮箱会展示在隐私政策与服务条款的联系方式里，所以这是对外可见的
// 联系方式配置，不是账号邮箱。
package api

import (
	"encoding/json"
	"net/http"
	"strings"
)

// siteKeys 四个可配置项对应的 settings key。
var siteKeys = []string{"site.name", "site.description", "site.keywords", "site.admin_email"}

// handleAdminSite GET /admin/site：读站点信息（后台设置页）。
func (s *Server) handleAdminSite(w http.ResponseWriter, r *http.Request) {
	out := map[string]string{}
	for _, k := range siteKeys {
		v, _ := s.st.GetSetting(r.Context(), k)
		out[k] = v
	}
	writeJSON(w, http.StatusOK, out)
}

// handleSaveSite POST /admin/site {site.name, site.description, site.keywords, site.admin_email}
func (s *Server) handleSaveSite(w http.ResponseWriter, r *http.Request) {
	var req map[string]string
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErr(w, http.StatusBadRequest, "请求体不是合法 JSON")
		return
	}
	for _, k := range siteKeys {
		v := strings.TrimSpace(req[k])
		if k == "site.admin_email" && v != "" && !strings.Contains(v, "@") {
			writeErr(w, http.StatusBadRequest, "管理员邮箱格式不正确")
			return
		}
		if err := s.st.SetSetting(r.Context(), k, v); err != nil {
			writeErr(w, http.StatusInternalServerError, "保存失败")
			return
		}
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

// handlePublicSite GET /api/site：对外只暴露名称与管理员邮箱（隐私政策用）。
// 描述与关键词是 SEO 用的，不在这里泄漏给普通页面以外的场景。
func (s *Server) handlePublicSite(w http.ResponseWriter, r *http.Request) {
	name, _ := s.st.GetSetting(r.Context(), "site.name")
	email, _ := s.st.GetSetting(r.Context(), "site.admin_email")
	writeJSON(w, http.StatusOK, map[string]any{
		"name":        name,
		"admin_email": email,
	})
}
