package api

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"log/slog"
	"net"
	"net/http"
	"strings"

	"voidxo/internal/auth"
	"voidxo/internal/labels"
	"voidxo/internal/sitecat"
	"voidxo/internal/store"
)

// knownLabels 从标签词表（internal/labels）派生，不再手抄 key——
// 词表加一个标签这里自动跟上，漏改的症状是「新标签的反馈永远提交失败」很难查。
// 校验它有两个作用：① 挡住随手拼出来的标签污染统计；② 让「误判」统计的 key 集合是封闭的。
var knownLabels = func() map[string]bool {
	m := make(map[string]bool, len(labels.All))
	for _, l := range labels.All {
		m[l.Key] = true
	}
	return m
}()

// categoryFeedback 分类反馈部分。Verdict 为空表示不表态——
// 用户可能只想说「标签错了」，并不想对分类下判断。
type categoryFeedback struct {
	Verdict string `json:"verdict"` // false_positive | confirmed
	Suggest string `json:"suggest"` // 认为正确的分类 key，可空
}

type feedbackReq struct {
	URL string `json:"url"`
	// Label 单条提交（兼容旧调用），Labels 批量提交。
	// 批量是必需的：人机验证的 token 一次性，一条一个请求时第二个标签必定失败。
	Label   string   `json:"label"`
	Labels  []string `json:"labels"`
	Verdict string   `json:"verdict"`
	Note    string   `json:"note"`
	// Should 漏报：用户认为该页面**应该有**的标签（页面上本来没有）。
	// 每个落一条 verdict=false_negative 的 kind=0 行，Label=该标签。
	Should []string `json:"should"`
	// Category 分类反馈。与标签同表不同行：页面可以没有标签（不是所有站都被打标），
	// 但一定有归属站点，所以分类永远反馈得了。
	Category *categoryFeedback `json:"category"`
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
	// 分类反馈可以单独提交：没有标签的页面也要能反馈——
	// 「这个站被判成技术博客，其实是官方文档」跟标签对不对毫无关系。
	catPart := req.Category
	catVerdict := ""
	catSuggest := ""
	if catPart != nil {
		catVerdict = strings.TrimSpace(catPart.Verdict)
		catSuggest = strings.TrimSpace(catPart.Suggest)
		if catVerdict != "" && catVerdict != store.VerdictFalsePositive && catVerdict != store.VerdictConfirmed {
			writeErr(w, http.StatusBadRequest, "分类结论只能是 false_positive 或 confirmed")
			return
		}
		if catVerdict != "" && catSuggest != "" && !sitecat.Valid(catSuggest) {
			writeErr(w, http.StatusBadRequest, "建议分类不在词表内")
			return
		}
	}
	// 漏报（should）：用户认为该页面**应该有**的标签。每项落一条 verdict=false_negative
	// 的 kind=0 行。页面上已有的标签不能报漏报——它已经在，「应该有」无从谈起；
	// 交集提交要么是手搓请求要么是前端 bug，直接 400（见下）。
	should := make([]string, 0, len(req.Should))
	seenShould := map[string]bool{}
	for _, l := range req.Should {
		l = strings.TrimSpace(l)
		if l == "" || seenShould[l] {
			continue
		}
		if !knownLabels[l] {
			writeErr(w, http.StatusBadRequest, "should 里的标签不合法")
			return
		}
		seenShould[l] = true
		should = append(should, l)
	}
	hasLabels := len(labels) > 0
	hasCategory := catVerdict != ""
	hasShould := len(should) > 0
	if req.URL == "" || (!hasLabels && !hasCategory && !hasShould) || len(labels) > 8 || len(should) > 8 {
		writeErr(w, http.StatusBadRequest, "url、label 或 category 不合法")
		return
	}
	for _, l := range labels {
		if !knownLabels[strings.TrimSpace(l)] {
			writeErr(w, http.StatusBadRequest, "url 或 label 不合法")
			return
		}
		if seenShould[strings.TrimSpace(l)] {
			writeErr(w, http.StatusBadRequest, "同一标签不能既报误判又报漏报")
			return
		}
	}
	if hasLabels && req.Verdict != store.VerdictFalsePositive && req.Verdict != store.VerdictConfirmed {
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
	// 与页面现状比对：文档上已有的标签再报「应该有」自相矛盾（前端已排除，这里兜底）。
	var current []string
	if doc.Labels != "" {
		_ = json.Unmarshal([]byte(doc.Labels), &current)
	}
	currentSet := make(map[string]bool, len(current))
	for _, l := range current {
		currentSet[strings.TrimSpace(l)] = true
	}
	for _, l := range should {
		if currentSet[l] {
			writeErr(w, http.StatusBadRequest, "该标签已在页面上，不能报漏报")
			return
		}
	}
	cid := clientID(r)
	note := strings.TrimSpace(req.Note)
	for _, l := range labels {
		if err := s.st.UpsertLabelFeedback(r.Context(), store.LabelFeedback{
			URL:     doc.URL,
			Domain:  doc.Domain,
			Label:   strings.TrimSpace(l),
			Verdict: req.Verdict,
			Note:    note,
			Kind:    store.FeedbackKindLabel,
		}, cid); err != nil {
			slog.Error("feedback write failed", "err", err)
			writeErr(w, http.StatusInternalServerError, "提交失败")
			return
		}
	}
	for _, l := range should {
		if err := s.st.UpsertLabelFeedback(r.Context(), store.LabelFeedback{
			URL:     doc.URL,
			Domain:  doc.Domain,
			Label:   l,
			Verdict: store.VerdictFalseNegative,
			Note:    note,
			Kind:    store.FeedbackKindLabel,
		}, cid); err != nil {
			slog.Error("feedback write failed", "err", err)
			writeErr(w, http.StatusInternalServerError, "提交失败")
			return
		}
	}
	if hasCategory {
		// 记下当时的站点分类：AI 会重判分类，事后再看就分不清用户在说哪个。
		if err := s.st.UpsertLabelFeedback(r.Context(), store.LabelFeedback{
			URL:      doc.URL,
			Domain:   doc.Domain,
			Label:    "",
			Verdict:  catVerdict,
			Note:     note,
			Kind:     store.FeedbackKindCategory,
			Category: s.docCategory(r.Context(), doc.Domain),
			Suggest:  catSuggest,
		}, cid); err != nil {
			slog.Error("category feedback write failed", "err", err)
			writeErr(w, http.StatusInternalServerError, "提交失败")
			return
		}
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

// docCategory 该页面所属站点的分类（白名单里存的那份），取不到返回空串。
// 分类反馈没有它等于只留一个 URL：光看「分类错了」不知道错的是哪个分类。
func (s *Server) docCategory(ctx context.Context, domain string) string {
	if domain == "" {
		return ""
	}
	cats, err := s.st.DomainCategories(ctx, []string{domain})
	if err != nil {
		slog.Warn("feedback category lookup failed", "domain", domain, "err", err)
		return ""
	}
	return cats[domain]
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
		"total": total, "items": s.withLinkState(r.Context(), items), "by_label": counts,
	})
}

// feedbackItem 一行反馈 + 被反馈链接的当前状态。
//
// 后台「修改」改的是链接本身，所以弹窗必须先看到「这一页现在挂着哪些标签、
// 这个站现在是什么分类」——只给用户报的那一个标签，等于让人蒙着眼睛改。
type feedbackItem struct {
	store.LabelFeedback
	PageLabels   []string `json:"page_labels"`
	SiteCategory string   `json:"site_category"`
}

// withLinkState 批量补上链接现状（一页 10 条，两条 IN 查询足够）。
func (s *Server) withLinkState(ctx context.Context, items []store.LabelFeedback) []feedbackItem {
	out := make([]feedbackItem, len(items))
	urls := make([]string, 0, len(items))
	domains := make([]string, 0, len(items))
	for i, it := range items {
		out[i] = feedbackItem{LabelFeedback: it, PageLabels: []string{}}
		urls = append(urls, it.URL)
		domains = append(domains, it.Domain)
	}
	if labels, err := s.st.LabelsByURLs(ctx, urls); err != nil {
		slog.Warn("feedback page labels failed", "err", err)
	} else {
		for i := range out {
			if ls, ok := labels[out[i].URL]; ok && ls != nil {
				out[i].PageLabels = ls
			}
		}
	}
	if cats, err := s.st.DomainCategories(ctx, domains); err != nil {
		slog.Warn("feedback site category failed", "err", err)
	} else {
		for i := range out {
			out[i].SiteCategory = cats[out[i].Domain]
		}
	}
	return out
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

// handleUpdateLabelFeedback POST /admin/label-feedback/update
// {id, kind, label, no_label, category, suggest, verdict}
//
// 只用于纠正**录错的反馈**（用户点错按钮、报错页面）。后台界面的「修改」
// 走的是 /admin/feedback/fix 改链接本身，不是这里——反馈是「用户说过什么」的历史。
//
// kind 说明这条反馈指向什么（0 标签 / 1 分类），由数据本身决定，
// 不接受「换个类型」——把一条标签反馈改成分类反馈不是修正，是编造一条用户没说过的话。
//
// 只校验和写入该类型用得上的那个对象：标签反馈只认 label，分类反馈只认
// category + suggest。两种对象互斥，不允许各留一半。
func (s *Server) handleUpdateLabelFeedback(w http.ResponseWriter, r *http.Request) {
	var req struct {
		ID      int64  `json:"id"`
		Kind    int    `json:"kind"`
		Label   string `json:"label"`
		NoLabel bool   `json:"no_label"`
		// Category / Suggest 只在 Kind=1（分类反馈）时生效。
		Category string `json:"category"`
		Suggest  string `json:"suggest"`
		Verdict  string `json:"verdict"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErr(w, http.StatusBadRequest, "请求体不是合法 JSON")
		return
	}
	req.Label = strings.TrimSpace(req.Label)
	req.Category = strings.TrimSpace(req.Category)
	req.Suggest = strings.TrimSpace(req.Suggest)

	if req.ID <= 0 {
		writeErr(w, http.StatusBadRequest, "id 不合法")
		return
	}
	if req.Verdict != store.VerdictFalsePositive && req.Verdict != store.VerdictConfirmed {
		writeErr(w, http.StatusBadRequest, "verdict 只能是 false_positive 或 confirmed")
		return
	}
	switch req.Kind {
	case store.FeedbackKindLabel:
		if !knownLabels[req.Label] {
			writeErr(w, http.StatusBadRequest, "label 不合法")
			return
		}
		// 「无」= 这条反馈成立，且该页面不该有任何标签。它与「判定正确」自相矛盾，
		// 服务端直接钉死而不是指望前端把选项禁掉——两边都挡，才不会存出矛盾状态。
		if req.NoLabel {
			req.Verdict = store.VerdictFalsePositive
		}
	case store.FeedbackKindCategory:
		// 分类必须落在词表内，否则后台会留下一句谁也读不懂的 key；
		// suggest 允许为空（用户没说该是什么）。
		if !sitecat.Valid(req.Category) {
			writeErr(w, http.StatusBadRequest, "分类不合法")
			return
		}
		if req.Suggest != "" && !sitecat.Valid(req.Suggest) {
			writeErr(w, http.StatusBadRequest, "建议分类不合法")
			return
		}
	default:
		writeErr(w, http.StatusBadRequest, "kind 只能是 0（标签）或 1（分类）")
		return
	}
	// UpdateFeedback 自己按 kind 清掉另一类字段，所以这里把两者都传进去也无害。
	if err := s.st.UpdateFeedback(r.Context(), req.ID, req.Kind, req.Label, req.NoLabel,
		req.Category, req.Suggest, req.Verdict); err != nil {
		slog.Error("feedback update failed", "err", err)
		writeErr(w, http.StatusInternalServerError, "保存失败")
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}
