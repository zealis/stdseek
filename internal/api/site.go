// 站点信息：名称、描述、关键词、管理员邮箱。
//
// 管理员邮箱会展示在隐私政策与服务条款的联系方式里，所以这是对外可见的
// 联系方式配置，不是账号邮箱。
package api

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
)

// siteKeys 站点信息页上的可配置项。
//
// site.base_url 是站点的对外地址，分享卡片与 security.txt/llms.txt 里的绝对链接
// 都靠它（反代后面 r.Host 常常不对）。
//
// 页头/页脚代码不在这张表里：它单独一张卡、单独一次保存。混在一起的话，
// 「改一句描述、顺手点保存」会把没人动过的代码也重写一遍，
// 而这类代码一旦被覆盖就找不回来了。
var siteKeys = []string{"site.name", "site.base_url", "site.description", "site.keywords", "site.admin_email"}

// handleAdminSite GET /admin/site：读站点信息（后台设置页）。
//
// 页头/页脚代码也要返回。保存接口写完之后前端会重新拉一次做回显，
// 不回显的话输入框会被清成空——用户以为没存上，再点一次保存就把刚写进去的
// 代码覆盖成空字符串（2026-10-30 真的这么丢过一次配置）。
func (s *Server) handleAdminSite(w http.ResponseWriter, r *http.Request) {
	out := map[string]string{}
	for _, k := range siteKeys {
		v, _ := s.st.GetSetting(r.Context(), k)
		out[k] = v
	}
	// 不并进 siteKeys：那张表的语义是「站点信息卡一次保存整组字段」，
	// 代码是另一张卡、另一个按钮，混进去会让 handleSaveSite 的意图变模糊。
	for _, k := range []string{settingSiteHeadHTML, settingSiteFootHTML} {
		v, _ := s.st.GetSetting(r.Context(), k)
		out[k] = v
	}
	writeJSON(w, http.StatusOK, out)
}

// handleSaveSite POST /admin/site
//
// 接受 site.name / site.base_url / site.description / site.keywords / site.admin_email，
// 以及 site.head_html / site.foot_html（页头页脚代码）。
//
// **只写请求里出现的键**：整张表覆盖写会让「只改一项」的调用把其余项清空，
// 页头页脚这种大段代码被误清就是直接丢了统计配置。
func (s *Server) handleSaveSite(w http.ResponseWriter, r *http.Request) {
	var req map[string]string
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErr(w, http.StatusBadRequest, "请求体不是合法 JSON")
		return
	}
	for _, k := range append(append([]string{}, siteKeys...), settingSiteHeadHTML, settingSiteFootHTML) {
		raw, present := req[k]
		if !present {
			continue
		}
		v, err := normalizeSiteValue(k, raw)
		if err != nil {
			writeErr(w, http.StatusBadRequest, err.Error())
			return
		}
		if err := s.st.SetSetting(r.Context(), k, v); err != nil {
			writeErr(w, http.StatusInternalServerError, "保存失败")
			return
		}
	}
	// 描述/关键词会进 OG 与 <meta>，改完立刻重算，不用等缓存过期。
	s.invalidateSEO()
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

// normalizeSiteValue 逐字段校验并归一化。
//
// 页头页脚代码**不做语法校验**（写错的 JS 只会静默失败，判不出来也不该假装能判），
// 只挡两件会真的坏事的东西：NUL 字节（能被 C 层截断，绕过后续处理）和超大体积。
func normalizeSiteValue(key, raw string) (string, error) {
	v := strings.TrimSpace(raw)
	switch key {
	case "site.admin_email":
		if v != "" && !strings.Contains(v, "@") {
			return "", errors.New("管理员邮箱格式不正确")
		}
	case "site.base_url":
		if v != "" && !strings.HasPrefix(v, "http://") && !strings.HasPrefix(v, "https://") {
			return "", errors.New("站点地址要以 http:// 或 https:// 开头")
		}
	case settingSiteHeadHTML, settingSiteFootHTML:
		if strings.ContainsRune(raw, 0) {
			return "", errors.New("代码里不能包含 NUL 字节")
		}
		if len(raw) > maxSiteCodeBytes {
			return "", fmt.Errorf("代码过长（上限 %d KB，当前 %d KB）",
				maxSiteCodeBytes>>10, (len(raw)+1023)>>10)
		}
	}
	return v, nil
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
