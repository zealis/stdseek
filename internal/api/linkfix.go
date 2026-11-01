// 按用户反馈修「链接本身」。
//
// 后台「用户反馈」里点「修改」改的不是那条反馈记录，而是被反馈的那个页面：
// 标签反馈 → 改这一页的页级标签；分类反馈 → 改这个站点的分类。
// 反馈记录是「用户说过什么」的历史，改它等于篡改证据；真正该动的是数据。
package api

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"strings"

	"github.com/zealis/stdseek/internal/index"
	"github.com/zealis/stdseek/internal/kv"
	"github.com/zealis/stdseek/internal/processor"
	"github.com/zealis/stdseek/internal/sitecat"
	"github.com/zealis/stdseek/internal/store"
	"github.com/zealis/stdseek/internal/urlkey"
)

// errBadLabel 标签不在词表内。单独定义是为了让 handler 与测试用同一句话。
var errBadLabel = errors.New("标签不在词表内")

// manualReason 人工改过的标签统一用这句理由。
// 标签必须可解释——一个说不出理由的标签，用户没法判断该不该信。
const manualReason = "管理员按用户反馈修正"

// handleFixLink POST /admin/feedback/fix {url, kind, labels, category}
//
// kind=0：改这一页的标签（labels 可以为空 = 这一页不该有标签）。
// kind=1：改这个站点的分类（category）。
func (s *Server) handleFixLink(w http.ResponseWriter, r *http.Request) {
	var req struct {
		URL      string   `json:"url"`
		Kind     int      `json:"kind"`
		Labels   []string `json:"labels"`
		Category string   `json:"category"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErr(w, http.StatusBadRequest, "请求体不是合法 JSON")
		return
	}
	req.URL = strings.TrimSpace(req.URL)
	if req.URL == "" {
		writeErr(w, http.StatusBadRequest, "url 不能为空")
		return
	}
	ctx := r.Context()
	doc, found, err := s.st.GetDocument(ctx, req.URL)
	if err != nil {
		slog.Error("fix link: doc lookup failed", "err", err)
		writeErr(w, http.StatusInternalServerError, "读取页面失败")
		return
	}
	if !found {
		writeErr(w, http.StatusNotFound, "该页面不在索引中")
		return
	}

	switch req.Kind {
	case store.FeedbackKindLabel:
		labels, reasons, err := normalizeLabels(req.Labels)
		if err != nil {
			writeErr(w, http.StatusBadRequest, err.Error())
			return
		}
		if err := s.st.SetDocLabels(ctx, doc.URL, labels, reasons); err != nil {
			slog.Error("fix link: set labels failed", "err", err)
			writeErr(w, http.StatusInternalServerError, "保存标签失败")
			return
		}
		// 索引里存的是索引期的标签，不同步的话搜索结果仍旧显示旧标签，
		// 而「改完看不到变化」会让人以为没保存。
		indexed := s.reindexWithLabels(ctx, doc, labels)
		writeJSON(w, http.StatusOK, map[string]any{"status": "ok", "indexed": indexed})

	case store.FeedbackKindCategory:
		cat := strings.TrimSpace(req.Category)
		if !sitecat.Valid(cat) {
			writeErr(w, http.StatusBadRequest, "分类不合法")
			return
		}
		if err := s.st.SetDomainCategory(ctx, doc.Domain, cat); err != nil {
			slog.Error("fix link: set category failed", "err", err)
			writeErr(w, http.StatusInternalServerError, "保存分类失败")
			return
		}
		// 分类会写进每篇文档的索引，逐篇重建代价太大；下次抓取自然带上新值。
		writeJSON(w, http.StatusOK, map[string]any{"status": "ok", "indexed": false})

	default:
		writeErr(w, http.StatusBadRequest, "kind 只能是 0（标签）或 1（分类）")
	}
}

// normalizeLabels 校验并序列化标签。返回 JSON 字符串，直接落库。
func normalizeLabels(raw []string) (labelsJSON, reasonsJSON string, err error) {
	seen := map[string]bool{}
	labels := make([]string, 0, len(raw))
	reasons := map[string]string{}
	for _, l := range raw {
		l = strings.TrimSpace(l)
		if l == "" || seen[l] {
			continue
		}
		if !knownLabels[l] {
			return "", "", errBadLabel
		}
		seen[l] = true
		labels = append(labels, l)
		reasons[l] = manualReason
	}
	return processor.LabelsJSON(labels), processor.ReasonsJSON(reasons), nil
}

// reindexWithLabels 用原文快照重建这一页的索引文档，并换上人工标签。
//
// 为什么不直接改索引里的 labels 字段：Bleve 没有「改一个字段」的操作，
// 写一次就是整篇替换，而正文（body）只索引不存储、取不回来。
// 从 KV 快照重新处理一遍是唯一不丢正文的做法。
// 没有快照时返回 false——那就等下次抓取，抓取时会带上人工标签（见 crawler）。
func (s *Server) reindexWithLabels(ctx context.Context, doc store.Document, labelsJSON string) bool {
	if s.snap == nil || s.idx == nil || s.Proc == nil {
		return false
	}
	raw, found, err := s.snap.Get(ctx, kv.Key(urlkey.Key(doc.URL)))
	if err != nil || !found || len(raw) == 0 {
		return false
	}
	var labels []string
	if err := json.Unmarshal([]byte(labelsJSON), &labels); err != nil {
		return false
	}
	category, _ := s.st.SiteCategory(ctx, doc.Domain)
	res, err := s.Proc.Process(doc.URL, raw, processor.Options{SiteCategory: category})
	if err != nil {
		slog.Warn("fix link: reprocess snapshot failed", "url", doc.URL, "err", err)
		return false
	}
	d := index.Doc{
		URL:         doc.URL,
		Domain:      doc.Domain,
		Title:       res.Title,
		Description: res.Description,
		Body:        res.Body,
		Labels:      labels,
		Category:    category,
		Lang:        res.Lang,
		PublishedAt: res.PublishedAt,
		FetchedAt:   res.FetchedAt,
	}
	if err := s.idx.IndexDocument(d); err != nil {
		slog.Warn("fix link: reindex failed", "url", doc.URL, "err", err)
		return false
	}
	return true
}
