// 蒸馏：后台配置 + 单页试抓。
//
// 开关本身走功能开关那套（feature.distill），这里只管两件事：
// 阅读器地址与代理的读写，以及「配完能不能用」的一次试抓。
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
	reader, _ := s.st.Setting(ctx, store.SettingDistillReader, "")
	proxy, _ := s.st.Setting(ctx, store.SettingDistillProxy, "")
	writeJSON(w, http.StatusOK, map[string]any{
		// reader 为空表示用默认阅读器（前端据此显示「默认通道」）
		"reader":         strings.TrimSpace(reader),
		"proxy":          strings.TrimSpace(proxy),
		"default_reader": distill.DefaultReader,
		"enabled":        s.FeatureEnabled(ctx, "distill"),
	})
}

// handleSetDistillConfig POST /admin/distill {reader, proxy}
func (s *Server) handleSetDistillConfig(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	maxBody(w, r)
	var req struct {
		Reader string `json:"reader"`
		Proxy  string `json:"proxy"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErr(w, http.StatusBadRequest, "请求体不是合法 JSON")
		return
	}

	reader := strings.TrimSpace(req.Reader)
	if reader != "" && !strings.HasPrefix(reader, "http://") && !strings.HasPrefix(reader, "https://") {
		writeErr(w, http.StatusBadRequest, "阅读器地址必须是 http(s) URL")
		return
	}
	// 代理允许只填 host:port（http:// 自动补），socks5 要显式写协议。
	proxy := strings.TrimSpace(req.Proxy)
	if err := s.st.SetSetting(ctx, store.SettingDistillReader, reader); err != nil {
		writeErr(w, http.StatusInternalServerError, "写入阅读器地址失败")
		return
	}
	if err := s.st.SetSetting(ctx, store.SettingDistillProxy, proxy); err != nil {
		writeErr(w, http.StatusInternalServerError, "写入代理失败")
		return
	}
	slog.Info("distill config updated", "custom_reader", reader != "", "proxy", proxy != "")
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

// handleTestDistill POST /admin/distill/test {url}
//
// 配完当场试一页：把蒸馏结果回给后台，能看到标题与前 200 字。
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
	reader, _ := s.st.Setting(ctx, store.SettingDistillReader, "")
	proxy, _ := s.st.Setting(ctx, store.SettingDistillProxy, "")

	cctx, cancel := context.WithTimeout(ctx, 40*time.Second)
	defer cancel()
	page, err := distill.New(reader, proxy).Fetch(cctx, target)
	if err != nil {
		writeErr(w, http.StatusBadGateway, "蒸馏失败："+err.Error())
		return
	}
	// 回给后台看的是纯文本预览，不是 HTML——后台不需要（也不该）渲染远端页面。
	plain := htmlToText(string(page.HTML))
	writeJSON(w, http.StatusOK, map[string]any{
		"title":   page.Title,
		"preview": truncRunes(plain, 200),
		"chars":   len([]rune(plain)),
		"bytes":   len(page.HTML),
	})
}

func truncRunes(s string, n int) string {
	r := []rune(strings.TrimSpace(s))
	if len(r) <= n {
		return string(r)
	}
	return string(r[:n]) + "…"
}
