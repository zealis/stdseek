package api

import (
	"encoding/json"
	"net/http"
	"strings"
	"time"

	"voidxo/internal/index"
	"voidxo/internal/kv"
	"voidxo/internal/summarize"
	"voidxo/internal/urlkey"
)

// 证据链摘要接口。
//
// 门禁有两层，缺一不可：
//  1. ai_summary 开关（后台可改）——产品决策：要不要对外提供这个能力
//  2. 有 KV 原文快照——技术前提：没有原文就没有引用，宁可不生成
//
// 第 2 条是红线「检索层没有正确原文时，宁可显示未找到可靠来源」的落点：
// 索引里只有正文纯文本、没有分段定位，拿不到快照就直接 404 并说明原因，
// 而不是把 description 拼一拼当成摘要。

const (
	summaryCacheTTL = 30 * time.Minute
	summaryCacheMax = 500
	// summaryMaxBodyRunes 参与摘要的正文上限（字符）。
	// 快照是原始 HTML，剥完文本通常 5~30KB；再长的页面抽取阶段也没意义，
	// 截断能挡住异常大页面把单次请求拖到秒级。
	summaryMaxBodyRunes = 40000
)

type summaryEntry struct {
	res summarize.Result
	at  time.Time
}

// SetSummarizer 注入摘要 Provider（main 里按启动参数构造）。
// 不注入时用抽取式：零成本、不联网，功能可用但不"聪明"。
func (s *Server) SetSummarizer(p summarize.Provider) {
	s.sumMu.Lock()
	defer s.sumMu.Unlock()
	s.sumPrv = p
}

// handleSummary POST /api/summary {url, q}
func (s *Server) handleSummary(w http.ResponseWriter, r *http.Request) {
	if !s.FeatureEnabled(r.Context(), "ai_summary") {
		writeErr(w, http.StatusForbidden, "原文摘要未开启")
		return
	}
	maxBody(w, r)
	var req struct {
		URL string `json:"url"`
		Q   string `json:"q"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErr(w, http.StatusBadRequest, "请求体不是合法 JSON")
		return
	}
	key := urlkey.Key(req.URL)
	if key == "" {
		writeErr(w, http.StatusBadRequest, "url 不合法")
		return
	}
	q := strings.TrimSpace(req.Q)
	if q == "" {
		writeErr(w, http.StatusBadRequest, "缺少查询词")
		return
	}
	if s.snap == nil {
		writeErr(w, http.StatusNotImplemented, "原文快照未启用")
		return
	}

	cacheKey := key + "\x00" + q
	if res, ok := s.cachedSummary(cacheKey); ok {
		writeJSON(w, http.StatusOK, res)
		return
	}

	body, found, err := s.snap.Get(kv.Key(key))
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "读取快照失败")
		return
	}
	if !found || len(body) == 0 {
		// 没有原文 → 不生成。这是红线，不是"降级为用 description 凑一段"。
		writeErr(w, http.StatusNotFound, "没有该页面的原文快照，无法生成带引用的摘要")
		return
	}
	text := htmlToText(string(body))
	if r := []rune(text); len(r) > summaryMaxBodyRunes {
		text = string(r[:summaryMaxBodyRunes])
	}

	title := ""
	if doc, ok, derr := s.st.GetDocument(r.Context(), req.URL); derr == nil && ok {
		title = doc.Title
	}

	s.sumMu.Lock()
	prv := s.sumPrv
	s.sumMu.Unlock()

	res := summarize.Summarize(r.Context(), summarize.Document{
		Query: q, Title: title, URL: req.URL, Body: text,
	}, prv, summarize.Options{
		// 摘要层复用排序层的术语词典：同一份映射，召回用了、排序用了，
		// 摘要不用就是白做——「Rust 社区」打英文 Community 页会抽不出段落。
		Expand: index.TermVariants,
	})

	s.storeSummary(cacheKey, res)
	writeJSON(w, http.StatusOK, res)
}

func (s *Server) cachedSummary(key string) (summarize.Result, bool) {
	s.sumMu.Lock()
	defer s.sumMu.Unlock()
	e, ok := s.sumCache[key]
	if !ok {
		return summarize.Result{}, false
	}
	if time.Since(e.at) > summaryCacheTTL {
		delete(s.sumCache, key)
		return summarize.Result{}, false
	}
	return e.res, true
}

// storeSummary 写缓存。满了随机淘汰一条——摘要缓存的价值是"同一页面别重复调模型"，
// 命中率不敏感，不必为了 LRU 的精确性引入链表。
func (s *Server) storeSummary(key string, res summarize.Result) {
	s.sumMu.Lock()
	defer s.sumMu.Unlock()
	if s.sumCache == nil {
		s.sumCache = make(map[string]summaryEntry, summaryCacheMax)
	}
	if len(s.sumCache) >= summaryCacheMax {
		for k := range s.sumCache {
			delete(s.sumCache, k)
			break
		}
	}
	s.sumCache[key] = summaryEntry{res: res, at: time.Now()}
}
