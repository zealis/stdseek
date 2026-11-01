// 蒸馏：后台配置 + 单页试抓。
//
// 开关本身走功能开关那套（feature.distill），这里只管两件事：
// 接口地址/令牌的读写，以及「配完能不能用」的一次试抓。
package api

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/zealis/stdseek/internal/distill"
	"github.com/zealis/stdseek/internal/store"
)

// handleGetDistillConfig GET /admin/distill：当前配置、默认值与开关状态。
func (s *Server) handleGetDistillConfig(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	apiURL, _ := s.st.Setting(ctx, store.SettingDistillURL, "")
	token, _ := s.st.Setting(ctx, store.SettingDistillToken, "")
	writeJSON(w, http.StatusOK, map[string]any{
		// api_url 为空表示用默认接口（前端据此显示「默认接口」）
		"api_url":     strings.TrimSpace(apiURL),
		"default_url": distill.DefaultURL,
		// 令牌只回「有没有」，不回明文——后台页面被截图外传是常见事故。
		"has_token": token != "",
		"enabled":   s.FeatureEnabled(ctx, "distill"),
	})
}

// handleSetDistillConfig POST /admin/distill {api_url, token}
//
// token 留空 = 不修改（和 AI 接口那一套一致）：管理员改接口地址时不必重新粘一遍令牌。
// clear_token=true 才清空。
func (s *Server) handleSetDistillConfig(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	maxBody(w, r)
	var req struct {
		APIURL     string `json:"api_url"`
		Token      string `json:"token"`
		ClearToken bool   `json:"clear_token"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErr(w, http.StatusBadRequest, "请求体不是合法 JSON")
		return
	}

	apiURL := strings.TrimSpace(req.APIURL)
	if apiURL != "" && !strings.HasPrefix(apiURL, "http://") && !strings.HasPrefix(apiURL, "https://") {
		writeErr(w, http.StatusBadRequest, "接口地址必须是 http(s) URL")
		return
	}
	if err := s.st.SetSetting(ctx, store.SettingDistillURL, apiURL); err != nil {
		writeErr(w, http.StatusInternalServerError, "写入接口地址失败")
		return
	}
	switch {
	case req.ClearToken:
		if err := s.st.SetSetting(ctx, store.SettingDistillToken, ""); err != nil {
			writeErr(w, http.StatusInternalServerError, "清空令牌失败")
			return
		}
	case strings.TrimSpace(req.Token) != "":
		if err := s.st.SetSetting(ctx, store.SettingDistillToken, strings.TrimSpace(req.Token)); err != nil {
			writeErr(w, http.StatusInternalServerError, "写入令牌失败")
			return
		}
	}
	slog.Info("distill config updated", "custom_url", apiURL != "", "token_set", req.Token != "")
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

// handleTestDistill POST /admin/distill/test {url}
//
// 配完当场试一页：直接把蒸馏结果回给后台，能看到标题与前 200 字。
// 不写库——试抓是配置动作，不该在索引里留一条半成品。
func (s *Server) handleTestDistill(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	maxBody(w, r)
	var req struct {
		URL string `json:"url"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErr(w, http.StatusBadRequest, "请求体不是合法 JSON")
		return
	}
	target := strings.TrimSpace(req.URL)
	if !strings.HasPrefix(target, "http://") && !strings.HasPrefix(target, "https://") {
		writeErr(w, http.StatusBadRequest, "请填一个 http(s) 地址")
		return
	}
	apiURL, _ := s.st.Setting(ctx, store.SettingDistillURL, "")
	token, _ := s.st.Setting(ctx, store.SettingDistillToken, "")

	cctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	page, err := distill.New(apiURL, token).Fetch(cctx, target)
	if err != nil {
		writeErr(w, http.StatusBadGateway, "蒸馏失败："+err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"title":   page.Title,
		"preview": truncRunes(page.Text, 200),
		"chars":   len([]rune(page.Text)),
	})
}

func truncRunes(s string, n int) string {
	r := []rune(strings.TrimSpace(s))
	if len(r) <= n {
		return string(r)
	}
	return string(r[:n]) + "…"
}
