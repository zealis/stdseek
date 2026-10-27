package api

import (
	"voidxo/internal/auth"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"log/slog"
	"net"
	"net/http"
	"strings"

	"voidxo/internal/store"
)

// knownLabels 与 processor 的标签常量保持一致。校验它有两个作用：
// ① 挡住随手拼出来的标签污染统计；② 让「误判」统计的 key 集合是封闭的。
var knownLabels = map[string]bool{
	"ai_suspect":   true,
	"ad_heavy":     true,
	"personal":     true,
	"ugc_platform": true,
}

type feedbackReq struct {
	URL string `json:"url"`
	// Label 单条提交（兼容旧调用），Labels 批量提交。
	// 批量是必需的：人机验证的 token 一次性，一条一个请求时第二个标签必定失败。
	Label   string   `json:"label"`
	Labels  []string `json:"labels"`
	Verdict string   `json:"verdict"`
	Note    string   `json:"note"`
	// 人机验证
	Captcha        *auth.Challenge `json:"captcha"`
	Answer         string          `json:"answer"`
	TurnstileToken string          `json:"turnstile_token"`
}

// handleLabelFeedback POST /api/label-feedback
// 规划书红线：概率性标签必须「可解释 + 可反馈」。可解释靠搜索接口返回 label_reasons，
// 可反馈就是这里——反馈落库供后台按标签统计误判率，用于收紧规则而不是当场改索引。
//
// 设计取舍：提交后不立即撤销标签。标签写在 Bleve 索引里，逐条改会带来索引抖动，
// 且单个用户的判断不足以推翻全局规则；先累积样本，再由后台决定规则调整后整体重建。
func (s *Server) handleLabelFeedback(w http.ResponseWriter, r *http.Request) {
	maxBody(w, r)
	var req feedbackReq
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErr(w, http.StatusBadRequest, "请求体不是合法 JSON")
		return
	}
	if err := s.checkCaptchaUnlessAdmin(r, req.Captcha, req.Answer, req.TurnstileToken, "feedback"); err != nil {
		authErr(w, err)
		return
	}
	req.URL = strings.TrimSpace(req.URL)
	labels := req.Labels
	if len(labels) == 0 && strings.TrimSpace(req.Label) != "" {
		labels = []string{strings.TrimSpace(req.Label)}
	}
	if req.URL == "" || len(labels) == 0 || len(labels) > 8 {
		writeErr(w, http.StatusBadRequest, "url 或 label 不合法")
		return
	}
	for _, l := range labels {
		if !knownLabels[strings.TrimSpace(l)] {
			writeErr(w, http.StatusBadRequest, "url 或 label 不合法")
			return
		}
	}
	if req.Verdict != store.VerdictFalsePositive && req.Verdict != store.VerdictConfirmed {
		writeErr(w, http.StatusBadRequest, "verdict 只能是 false_positive 或 confirmed")
		return
	}
	if len([]rune(req.Note)) > 300 {
		writeErr(w, http.StatusBadRequest, "备注过长（上限 300 字）")
		return
	}
	// 只接受索引内页面：否则任何人可以往反馈表里塞任意 URL 刷数据。
	doc, found, err := s.st.GetDocument(r.Context(), req.URL)
	if err != nil {
		slog.Error("feedback doc lookup failed", "err", err)
		writeErr(w, http.StatusInternalServerError, "提交失败")
		return
	}
	if !found {
		writeErr(w, http.StatusNotFound, "该页面不在索引中，无法反馈标签")
		return
	}
	cid := clientID(r)
	for _, l := range labels {
		if err := s.st.UpsertLabelFeedback(r.Context(), store.LabelFeedback{
			URL:     doc.URL,
			Domain:  doc.Domain,
			Label:   strings.TrimSpace(l),
			Verdict: req.Verdict,
			Note:    strings.TrimSpace(req.Note),
		}, cid); err != nil {
			slog.Error("feedback write failed", "err", err)
			writeErr(w, http.StatusInternalServerError, "提交失败")
			return
		}
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

// clientID 由客户端 IP 派生的匿名标识，仅用于「同一人对同一标签只记一条」。
// 不存原 IP：反馈表不需要可追溯到人，只要能去重。
func clientID(r *http.Request) string {
	ip := r.RemoteAddr
	if host, _, err := net.SplitHostPort(ip); err == nil {
		ip = host
	}
	sum := sha256.Sum256([]byte("voidxo-feedback:" + ip))
	return hex.EncodeToString(sum[:8])
}

// ---------- 后台 ----------

// handleListLabelFeedback GET /admin/label-feedback?verdict=&kw=&page=&size=
// 附带 by_label：按标签汇总的误判条数，据此判断哪条规则需要收紧。
func (s *Server) handleListLabelFeedback(w http.ResponseWriter, r *http.Request) {
	limit, offset := pageParams(r)
	items, total, err := s.st.ListLabelFeedback(r.Context(),
		strings.TrimSpace(r.URL.Query().Get("kw")),
		r.URL.Query().Get("verdict"), limit, offset)
	if err != nil {
		slog.Error("list feedback failed", "err", err)
		writeErr(w, http.StatusInternalServerError, "查询反馈失败")
		return
	}
	counts, err := s.st.LabelFalsePositiveCounts(r.Context())
	if err != nil {
		slog.Warn("feedback counts failed", "err", err)
	}
	writeJSON(w, http.StatusOK, map[string]interface{}{
		"total": total, "items": items, "by_label": counts,
	})
}

// handleDeleteLabelFeedback POST /admin/label-feedback/delete {"ids":[1,2]}
// 用 POST 而非 DELETE + body：浏览器与中间层对 DELETE 携带请求体的支持不一致。
func (s *Server) handleDeleteLabelFeedback(w http.ResponseWriter, r *http.Request) {
	maxBody(w, r)
	var req struct {
		IDs []int64 `json:"ids"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErr(w, http.StatusBadRequest, "请求体不是合法 JSON")
		return
	}
	if len(req.IDs) == 0 {
		writeErr(w, http.StatusBadRequest, "ids 不能为空")
		return
	}
	n, err := s.st.DeleteLabelFeedback(r.Context(), req.IDs)
	if err != nil {
		slog.Error("delete feedback failed", "err", err)
		writeErr(w, http.StatusInternalServerError, "删除失败")
		return
	}
	writeJSON(w, http.StatusOK, map[string]interface{}{"deleted": n})
}

// handleUpdateLabelFeedback POST /admin/label-feedback/update {id, label, verdict}
// 后台修正一条反馈：用户点错标签时直接改对，避免污染误判率统计。
func (s *Server) handleUpdateLabelFeedback(w http.ResponseWriter, r *http.Request) {
	var req struct {
		ID      int64  `json:"id"`
		Label   string `json:"label"`
		Verdict string `json:"verdict"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErr(w, http.StatusBadRequest, "请求体不是合法 JSON")
		return
	}
	if req.ID <= 0 || !knownLabels[req.Label] {
		writeErr(w, http.StatusBadRequest, "id 或 label 不合法")
		return
	}
	if req.Verdict != store.VerdictFalsePositive && req.Verdict != store.VerdictConfirmed {
		writeErr(w, http.StatusBadRequest, "verdict 只能是 false_positive 或 confirmed")
		return
	}
	if err := s.st.UpdateLabelFeedback(r.Context(), req.ID, req.Label, req.Verdict); err != nil {
		slog.Error("label feedback update failed", "err", err)
		writeErr(w, http.StatusInternalServerError, "保存失败")
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}
