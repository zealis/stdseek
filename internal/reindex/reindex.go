// Package reindex 从 KV 原文快照重建 Bleve 索引。
// 用途：索引结构升级（如新增 has_published 字段）后，让旧文档重新走一遍 processor 并回填新字段。
//
// 注意：需在服务停止时执行（Bleve 与 Pebble 都是单进程独占）。
package reindex

import (
	"context"
	"fmt"
	"strings"
	"time"

	"voidxo/internal/index"
	"voidxo/internal/kv"
	"voidxo/internal/processor"
	"voidxo/internal/store"
	"voidxo/internal/urlkey"
)

// Stats 重建统计。
type Stats struct {
	Scanned   int // KV 中的快照数
	Indexed   int // 成功重建
	Skipped   int // 无法解析/空快照
	Errors    int // 处理或写入失败
	Uncovered int // documents 有记录但 KV 无快照（需重新抓取才能重建）
}

// Run 遍历 KV 中的 page: 快照，重新提取并写入索引与元数据。
func Run(ctx context.Context, st *store.Store, idx *index.Index, snap *kv.Store, proc *processor.Processor) (Stats, error) {
	var stats Stats
	urls, err := st.DocumentURLs(ctx)
	if err != nil {
		return stats, fmt.Errorf("load document urls: %w", err)
	}

	err = snap.Scan(kv.Key(""), func(key string, raw []byte) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		stats.Scanned++
		if len(raw) == 0 {
			stats.Skipped++
			return nil
		}

		urlKey := strings.TrimPrefix(key, kv.Key(""))
		pageURL := urls[urlKey]
		if pageURL == "" {
			pageURL = "https://" + urlKey
		}
		host := urlKey
		if i := strings.Index(urlKey, "/"); i >= 0 {
			host = urlKey[:i]
		}

		category, err := st.SiteCategory(ctx, host)
		if err != nil {
			return fmt.Errorf("site category %s: %w", host, err)
		}
		res, err := proc.Process(pageURL, raw, processor.Options{SiteCategory: category})
		if err != nil {
			stats.Errors++
			return nil // 单页失败不阻断整体重建
		}

		// 保留原有抓取时间与发布时间，避免重建后时间信息漂移
		fetched := time.Now().Unix()
		var published *int64
		if old, ok, err := st.GetDocument(ctx, pageURL); err == nil && ok {
			fetched = old.FetchedAt
			published = old.PublishedAt
		}
		if res.PublishedAt != nil {
			published = res.PublishedAt
		}

		if err := idx.IndexDocument(index.Doc{
			URL: pageURL, Domain: host, Title: res.Title, Description: res.Description,
			Body: res.Body, Labels: res.Labels, PublishedAt: published, FetchedAt: fetched,
		}); err != nil {
			stats.Errors++
			return nil
		}
		if _, err := st.UpsertDocument(ctx, store.Document{
			URL: pageURL, Domain: host, Title: res.Title, Description: res.Description,
			Labels: processor.LabelsJSON(res.Labels), LabelReasons: processor.ReasonsJSON(res.LabelReasons),
			WordCount: res.WordCount, Simhash: res.Simhash, PublishedAt: published, FetchedAt: fetched,
		}); err != nil {
			stats.Errors++
			return nil
		}
		stats.Indexed++
		return nil
	})
	if err != nil {
		return stats, err
	}

	// 统计覆盖率：documents 表里有、KV 里没有的文档无法重建（需重新抓取）
	for _, u := range urls {
		if _, found, err := snap.Get(kv.Key(urlkey.Key(u))); err == nil && !found {
			stats.Uncovered++
		}
	}
	return stats, nil
}
