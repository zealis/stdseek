// Package repair 补齐索引：documents 表里有、索引里却没有的文档，从 KV 快照重新入索引。
//
// 为什么需要：reindex 是"先清空索引再逐篇重建"的，中断一次就会留下
// 「元数据表有 1151 条、索引只有 487 篇」的半成品。此时 crawl 的 BFS 只会
// 补上它种子能走到的页面，覆盖不到表里全部文档——实测就是这样丢掉了
// draven.co 全部 67 篇，纯中文查询集 Recall 从 0.877 掉到 0.418。
//
// 与 reindex 的分工：reindex 重新走一遍 processor（正文提取、标签、指纹全重算），
// 慢但完整；repair 只把"缺的"补回去，元数据沿用表里的值，快一个数量级。
// 适用于中断恢复，不适用于算法升级后的重建。
package repair

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/zealis/stdseek/internal/processor"
	"github.com/zealis/stdseek/internal/snapshot"
	"regexp"
	"strings"

	"github.com/zealis/stdseek/internal/index"
	"github.com/zealis/stdseek/internal/kv"
	"github.com/zealis/stdseek/internal/lang"
	"github.com/zealis/stdseek/internal/store"
	"github.com/zealis/stdseek/internal/urlkey"
)

// Stats 补齐统计。
type Stats struct {
	Checked    int // 表里的文档数（不含已折叠副本）
	Present    int // 索引里已有的
	Repaired   int // 本次补入索引的
	NoSnapshot int // 表里有记录但 KV 没有快照（需重新抓取才能恢复）
	Thin       int // 快照剥出来的正文过短，不补（与 crawler 的 MinWordCount 对齐）
}

var (
	scriptStyleRe = regexp.MustCompile(`(?is)<(script|style|noscript)[^>]*>.*?</(script|style|noscript)>`)
	htmlTagRe     = regexp.MustCompile(`(?s)<[^>]*>`)
)

// Run 补齐索引。
func Run(ctx context.Context, st *store.Store, idx *index.Index, snap snapshot.KVSource) (Stats, error) {
	var stats Stats
	docs, err := st.Fingerprints(ctx)
	if err != nil {
		return stats, fmt.Errorf("load documents: %w", err)
	}

	// 批量攒起来一次提交：逐条 IndexDocument 的吞吐只有 41 篇/秒（见 bench）。
	var batch []index.Doc
	const batchN = 500
	flush := func() error {
		if len(batch) == 0 {
			return nil
		}
		n, err := idx.IndexDocuments(batch)
		stats.Repaired += n
		batch = batch[:0]
		return err
	}

	for _, f := range docs {
		if err := ctx.Err(); err != nil {
			return stats, err
		}
		// 已折叠的副本故意不在索引里，补回去等于撤销去重结论
		if f.DupOf != "" {
			continue
		}
		stats.Checked++
		has, err := idx.Has(f.URL)
		if err != nil {
			return stats, fmt.Errorf("check index %s: %w", f.URL, err)
		}
		if has {
			stats.Present++
			continue
		}

		key := urlkey.Key(f.URL)
		raw, found, err := snap.Get(kv.Key(key))
		if err != nil {
			return stats, fmt.Errorf("read snapshot %s: %w", f.URL, err)
		}
		if !found || len(raw) == 0 {
			stats.NoSnapshot++
			continue
		}
		title, desc, body := extractFields(f.URL, string(raw))
		if len([]rune(body)) < 100 {
			// 与 crawler 的薄内容过滤对齐：正文太短的页面补进去也只是噪声
			stats.Thin++
			continue
		}
		// 标签走表里的原文：repair 不该重新判定标签，那会让同一篇文档
		// 在"补回来"和"重新抓取"两条路径下得到不同的过滤行为。
		var labels []string
		if doc, ok, err := st.GetDocument(ctx, f.URL); err == nil && ok {
			labels = decodeLabels(doc.Labels)
			// 不能只判 title == ""：processor 提取不到标题时兜底成域名，
			// 永不为空，那个分支永远不会触发。快照是纯文本时（seed 语料）
			// 提不到标题，就会把表里正确的标题覆盖成域名——41% 的语料内文档
			// 曾经这样被降级。详见 processor.PickTitle。
			title = processor.PickTitle(title, doc.Title, f.Domain)
			if desc == "" {
				desc = doc.Description
			}
		}
		batch = append(batch, index.Doc{
			URL: f.URL, Domain: f.Domain, Title: title, Description: desc,
			Body: body, Labels: labels, Lang: lang.DocLang(title, body),
			PublishedAt: f.PublishedAt, FetchedAt: f.FetchedAt,
		})
		if len(batch) >= batchN {
			if err := flush(); err != nil {
				return stats, fmt.Errorf("batch index: %w", err)
			}
		}
	}
	if err := flush(); err != nil {
		return stats, err
	}
	return stats, nil
}

// toText 把快照 HTML 剥成纯文本。正则而不是解析器：
// 这里只服务「把缺的文档补回索引」，每篇做一次 DOM 解析没有意义。
func toText(s string) string {
	if !strings.Contains(s, "<") {
		return s
	}
	s = scriptStyleRe.ReplaceAllString(s, " ")
	s = htmlTagRe.ReplaceAllString(s, " ")
	return strings.Join(strings.Fields(s), " ")
}

// extractFields 从快照里取标题与正文。标题优先 <title>，其次 <h1>，最后域名。
func extractFields(rawURL, htmlDoc string) (title, desc, body string) {
	titleRe := regexp.MustCompile(`(?is)<title[^>]*>(.*?)</title>`)
	h1Re := regexp.MustCompile(`(?is)<h1[^>]*>(.*?)</h1>`)
	descRe := regexp.MustCompile(`(?is)<meta[^>]+name=["']description["'][^>]+content=["'](.*?)["']`)

	clean := func(s string) string {
		s = htmlTagRe.ReplaceAllString(s, " ")
		return strings.TrimSpace(strings.Join(strings.Fields(s), " "))
	}
	if m := titleRe.FindStringSubmatch(htmlDoc); len(m) > 1 {
		title = clean(m[1])
	}
	if title == "" {
		if m := h1Re.FindStringSubmatch(htmlDoc); len(m) > 1 {
			title = clean(m[1])
		}
	}
	if m := descRe.FindStringSubmatch(htmlDoc); len(m) > 1 {
		desc = clean(m[1])
	}
	body = toText(htmlDoc)
	return title, desc, body
}

// decodeLabels 解析 documents 表的 labels JSON 字符串。
// 解析失败按「无标签」处理——补索引不该因为一条脏数据整批失败。
func decodeLabels(s string) []string {
	if strings.TrimSpace(s) == "" {
		return nil
	}
	var out []string
	if err := json.Unmarshal([]byte(s), &out); err != nil {
		return nil
	}
	return out
}
