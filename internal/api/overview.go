// AI 概览：搜索结果上方的一段 AI 回答。
//
// 与「原文摘要」的区别：原文摘要只看被点开的那一页；概览看的是本次搜索的前几条结果，
// 把多篇材料合成回答。触发条件也刻意收紧：**只有提问式查询**才调用模型，
// 普通关键词（如「deepseek」）不生成——否则每个词都烧一次调用，收益却很低。
package api

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"voidxo/internal/index"
	"voidxo/internal/overview"
	"voidxo/internal/querysyntax"
	"voidxo/internal/store"
)

const (
	overviewCacheTTL   = 6 * time.Hour
	overviewMaxSources = 5
	// overviewMaxQueryLen 已并入 queryguard 的 maxQueryRunes：
	// 概览与检索用同一个查询上限，两处各写一个值必然漂。
)

type overviewEntry struct {
	resp overviewResp
	at   time.Time
}

type overviewSource struct {
	ID     int    `json:"id"`
	Title  string `json:"title"`
	URL    string `json:"url"`
	Domain string `json:"domain"`
}

type overviewResp struct {
	Enabled  bool             `json:"enabled"`
	Question bool             `json:"question"`
	Answer   string           `json:"answer,omitempty"`
	Sources  []overviewSource `json:"sources,omitempty"`
	Provider string           `json:"provider,omitempty"`
	Reason   string           `json:"reason,omitempty"`
}

// questionMarkers 中文提问标志。命中任意一个就认为用户在提问；
// 只用来决定「要不要花一次模型调用」，误判的代价只是多/少一张概览卡。
var questionMarkers = []string{
	"是什么", "什么是", "为什么", "怎么样", "怎么", "如何", "怎样",
	"多少", "哪些", "哪个", "哪里", "谁", "是否", "能否", "能不能",
	"可不可以", "区别", "原理", "机制", "原因", "介绍", "教程", "吗", "呢",
}

// looksLikeQuestion 是否是提问式查询。与 frontend 的展示无关——
// 前端每次都问后端，由这里统一决定是否生成，避免两套词表漂移。
func looksLikeQuestion(q string) bool {
	q = strings.TrimSpace(q)
	if q == "" {
		return false
	}
	if strings.ContainsAny(q, "?？") {
		return true
	}
	for _, m := range questionMarkers {
		if strings.Contains(q, m) {
			return true
		}
	}
	words := strings.Fields(strings.ToLower(q))
	if len(words) > 0 && isEnglishQuestionWord(words[0]) {
		return true
	}
	return false
}

func isEnglishQuestionWord(w string) bool {
	switch strings.ToLower(w) {
	case "what", "how", "why", "which", "who", "when", "where",
		"is", "are", "can", "does", "do", "should", "could", "would":
		return true
	}
	return false
}

// overviewRetrievalQuery 去掉疑问词再检索。「deepseek是什么」直接拿去搜，
// 「是什么」会稀释 BM25；去掉后回到用户真正要找的主题词。
func overviewRetrievalQuery(q string) string {
	cleaned := q
	for _, m := range questionMarkers {
		cleaned = strings.ReplaceAll(cleaned, m, " ")
	}
	cleaned = strings.ReplaceAll(cleaned, "？", " ")
	cleaned = strings.ReplaceAll(cleaned, "?", " ")
	words := strings.Fields(cleaned)
	for len(words) > 1 && isEnglishQuestionWord(words[0]) {
		words = words[1:]
	}
	cleaned = strings.Join(words, " ")
	if strings.TrimSpace(cleaned) == "" {
		return strings.TrimSpace(q)
	}
	return cleaned
}

// handleOverview POST /api/overview {q}
func (s *Server) handleOverview(w http.ResponseWriter, r *http.Request) {
	if !s.FeatureEnabled(r.Context(), "ai_overview") {
		writeErr(w, http.StatusForbidden, "AI 概览未开启")
		return
	}
	maxBody(w, r)
	var req struct {
		Q string `json:"q"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErr(w, http.StatusBadRequest, "请求体不是合法 JSON")
		return
	}
	q, err := normalizeQuery(req.Q)
	if err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	// 普通关键词直接返回「不生成」：前端据此不显示卡片，也不消耗模型调用。
	if !looksLikeQuestion(q) {
		writeJSON(w, http.StatusOK, overviewResp{Enabled: true, Question: false})
		return
	}
	if res, ok := s.cachedOverview(r.Context(), q); ok {
		writeJSON(w, http.StatusOK, res)
		return
	}

	eps := s.summaryEndpoints(r.Context())
	if len(eps) == 0 {
		writeJSON(w, http.StatusOK, overviewResp{Enabled: true, Question: true, Reason: "未配置 AI 接口"})
		return
	}

	parsed := querysyntax.Parse(overviewRetrievalQuery(q))
	hits, _, err := s.cachedSearch(index.Query{
		Q: parsed.Text, Required: parsed.Required, Excluded: parsed.Excluded,
		Phrases: parsed.Phrases, Site: parsed.Site, Page: 1, Size: overviewMaxSources,
	})
	if err != nil {
		slog.Warn("overview search failed", "q", q, "err", err)
		writeJSON(w, http.StatusOK, overviewResp{Enabled: true, Question: true, Reason: "检索失败，未生成 AI 概览"})
		return
	}
	sources := overviewSources(hits, overviewMaxSources)
	if len(sources) == 0 {
		writeJSON(w, http.StatusOK, overviewResp{Enabled: true, Question: true, Reason: "没有找到可用于回答的页面"})
		return
	}

	out := make([]overview.Endpoint, 0, len(eps))
	for _, e := range eps {
		out = append(out, overview.Endpoint{Name: e.Name, BaseURL: e.BaseURL, APIKey: e.APIKey, Model: e.Model})
	}
	ctx, cancel := context.WithTimeout(r.Context(), 40*time.Second)
	defer cancel()
	got, err := overview.Generate(ctx, out, q, sources, 12*time.Second)
	if err != nil {
		slog.Warn("overview generate failed", "q", q, "err", err)
		writeJSON(w, http.StatusOK, overviewResp{Enabled: true, Question: true, Reason: "AI 概览暂时不可用"})
		return
	}
	// 没有有效引用 = 无法核验来源，宁可不展示，也不给一段没有出处的结论。
	if len(got.Citations) == 0 {
		writeJSON(w, http.StatusOK, overviewResp{Enabled: true, Question: true, Reason: "搜索结果中没有足够信息"})
		return
	}
	byID := make(map[int]overviewSource, len(sources))
	for _, src := range sources {
		byID[src.ID] = overviewSource{ID: src.ID, Title: src.Title, URL: src.URL, Domain: src.Domain}
	}
	cited := make([]overviewSource, 0, len(got.Citations))
	for _, id := range got.Citations {
		if src, ok := byID[id]; ok {
			cited = append(cited, src)
		}
	}
	resp := overviewResp{
		Enabled: true, Question: true, Answer: got.Answer, Sources: cited, Provider: got.Provider,
	}
	s.storeOverview(r.Context(), q, resp)
	writeJSON(w, http.StatusOK, resp)
}

// overviewSources 把检索结果转成给模型的材料。没有正文摘录的页面跳过：
// 只给标题让模型「补全」等于放任它编造。
func overviewSources(hits []index.Hit, max int) []overview.Source {
	out := make([]overview.Source, 0, max)
	for _, h := range hits {
		if len(out) >= max {
			break
		}
		text := strings.TrimSpace(h.Description)
		if len([]rune(text)) < 40 {
			if ex := strings.TrimSpace(h.Excerpt); len([]rune(ex)) > len([]rune(text)) {
				text = ex
			}
		}
		if text == "" {
			continue
		}
		out = append(out, overview.Source{
			ID: len(out) + 1, Title: h.Title, URL: h.URL, Domain: h.Domain, Text: text,
		})
	}
	return out
}

func overviewCacheKey(q string) string { return strings.ToLower(strings.TrimSpace(q)) }

// cachedOverview 先看进程内缓存，再看 SQLite 持久缓存。
// 持久化是为了「热门问题」跨重启仍然命中——搜索词不落库，无法预知热门，
// 只能首次生成后缓存；重复搜索不再调用模型。
func (s *Server) cachedOverview(ctx context.Context, q string) (overviewResp, bool) {
	key := overviewCacheKey(q)
	s.ovMu.Lock()
	e, ok := s.ovCache[key]
	s.ovMu.Unlock()
	if ok {
		if time.Since(e.at) <= overviewCacheTTL {
			return e.resp, true
		}
		s.ovMu.Lock()
		delete(s.ovCache, key)
		s.ovMu.Unlock()
	}
	ce, found, err := s.st.OverviewCacheGet(ctx, key)
	if err != nil {
		slog.Warn("overview cache read failed", "err", err)
		return overviewResp{}, false
	}
	if !found || ce.ExpiresAt <= time.Now().Unix() {
		return overviewResp{}, false
	}
	var sources []overviewSource
	if strings.TrimSpace(ce.Sources) != "" {
		_ = json.Unmarshal([]byte(ce.Sources), &sources)
	}
	resp := overviewResp{
		Enabled: true, Question: true, Answer: ce.Answer, Sources: sources, Provider: ce.Provider,
	}
	s.ovMu.Lock()
	if s.ovCache == nil {
		s.ovCache = make(map[string]overviewEntry, 64)
	}
	// 用 DB 的到期时间回填内存，避免「读一次 DB 就多续 6 小时」。
	s.ovCache[key] = overviewEntry{resp: resp, at: time.Unix(ce.ExpiresAt, 0).Add(-overviewCacheTTL)}
	s.ovMu.Unlock()
	return resp, true
}

func (s *Server) storeOverview(ctx context.Context, q string, resp overviewResp) {
	key := overviewCacheKey(q)
	s.ovMu.Lock()
	if s.ovCache == nil {
		s.ovCache = make(map[string]overviewEntry, 64)
	}
	if len(s.ovCache) >= 300 {
		for k := range s.ovCache {
			delete(s.ovCache, k)
			break
		}
	}
	s.ovCache[key] = overviewEntry{resp: resp, at: time.Now()}
	s.ovMu.Unlock()

	sourcesJSON, _ := json.Marshal(resp.Sources)
	if err := s.st.OverviewCachePut(ctx, key, store.OverviewCacheEntry{
		Answer: resp.Answer, Sources: string(sourcesJSON), Provider: resp.Provider,
		ExpiresAt: time.Now().Add(overviewCacheTTL).Unix(),
	}); err != nil {
		slog.Warn("overview cache write failed", "err", err)
	}
	_ = s.st.OverviewCacheDeleteExpired(ctx, time.Now().Unix())
}

// clearOverviewCache 只清进程内缓存；SQLite 持久缓存保留（内容与用哪个接口生成无关）。
func (s *Server) clearOverviewCache() {
	s.ovMu.Lock()
	defer s.ovMu.Unlock()
	s.ovCache = nil
}
