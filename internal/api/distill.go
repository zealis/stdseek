// 蒸馏：后台配置 + 单页试抓。
//
// 开关本身走功能开关那套（feature.distill），这里只管两件事：
// 搜索引擎地址的读写，以及「配完能不能用」的一次试抓。
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
	engine, _ := s.st.Setting(ctx, store.SettingDistillEngine, "")
	writeJSON(w, http.StatusOK, map[string]any{
		// engine 为空表示用默认搜索（前端据此显示「默认引擎」）
		"engine":         strings.TrimSpace(engine),
		"default_engine": distill.DefaultEngine,
		"enabled":        s.FeatureEnabled(ctx, "distill"),
	})
}

// handleSetDistillConfig POST /admin/distill {engine}
//
// 只存一个搜索引擎地址：整页正文拿不到这件事已经写进功能说明里了，
// 不再有接口地址与令牌——那两样在「用 site: 搜索」这条路上没有位置。
func (s *Server) handleSetDistillConfig(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	maxBody(w, r)
	var req struct {
		Engine string `json:"engine"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErr(w, http.StatusBadRequest, "请求体不是合法 JSON")
		return
	}

	engine := strings.TrimSpace(req.Engine)
	if engine != "" && !strings.HasPrefix(engine, "http://") && !strings.HasPrefix(engine, "https://") {
		writeErr(w, http.StatusBadRequest, "搜索引擎地址必须是 http(s) URL")
		return
	}
	if err := s.st.SetSetting(ctx, store.SettingDistillEngine, engine); err != nil {
		writeErr(w, http.StatusInternalServerError, "写入搜索引擎地址失败")
		return
	}
	slog.Info("distill config updated", "custom_engine", engine != "")
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
	engine, _ := s.st.Setting(ctx, store.SettingDistillEngine, "")

	cctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	page, err := distill.New(engine).Fetch(cctx, target)
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
