// Package reindex 从 KV 原文快照重建 Bleve 索引。
// 用途：索引结构升级（如新增 has_published 字段）后，让旧文档重新走一遍 processor 并回填新字段。
//
// 注意：需在服务停止时执行（Bleve 与 Pebble 都是单进程独占）。
package reindex

import (
	"context"
	"fmt"
	"log/slog"
	"runtime"
	"strings"
	"sync"
	"time"

	"voidxo/internal/index"
	"voidxo/internal/kv"
	"voidxo/internal/labels"
	"voidxo/internal/processor"
	"voidxo/internal/snapshot"
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
	Orphans   int // KV 有快照但 documents 无记录（已下架，跳过且不复活）
}

// job 一个待处理的快照。
type job struct {
	urlKey  string
	pageURL string
	host    string
	raw     []byte
}

// outcome worker 的处理结果（元数据写入留给消费者，见 Run）。
type outcome struct {
	pageURL string
	host    string
	res     processor.Result
	procDur time.Duration // 该篇在 worker 里的处理耗时（用于诊断）
	err     error
}

// Run 遍历 KV 中的 page: 快照，重新提取并写入索引与元数据。
//
// workers 为 processor 并行度，<=0 时取 GOMAXPROCS。
//
// 为什么是「processor 并行、写入单点」的三段流水线：
// 实测串行版 1131 篇要 68 分钟，即 3.6 秒/篇，几乎全在 processor
// （readability + gse 分词），索引批量写只要 1ms/篇。所以并行只 parallel
// 这一段；Bleve 批量写与 SQLite upsert 留在单个 goroutine——
// SQLite 是 MaxOpenConns(1) 的单连接，多 goroutine 并发查询只会在连接上排队，
// 没有吞吐收益还徒增锁竞争。
func Run(ctx context.Context, st *store.Store, idx *index.Index, snap snapshot.KVSource, proc *processor.Processor, workers int) (Stats, error) {
	return run(ctx, st, idx, snap, proc, workers, nil)
}

// RunForDomains 只重建这些站点（含子域）的文档。
//
// 给「后台改了站点分类，希望搜索结果立刻按新分类可筛」用：
// 分类是索引里的 keyword 字段，改 SQLite 不会自动改 Bleve。
// 全量重建太重（几十万篇几小时），按域名圈定范围才敢在请求里同步做。
func RunForDomains(ctx context.Context, st *store.Store, idx *index.Index, snap snapshot.KVSource, proc *processor.Processor, workers int, domains []string) (Stats, error) {
	if len(domains) == 0 {
		return Stats{}, nil
	}
	return run(ctx, st, idx, snap, proc, workers, func(host string) bool {
		return matchDomain(host, domains)
	})
}

// matchDomain 站点域名与文档 host 的后缀匹配（blog.csdn.net 属于 csdn.net）。
func matchDomain(host string, domains []string) bool {
	host = strings.ToLower(host)
	for _, d := range domains {
		d = strings.ToLower(strings.TrimSpace(d))
		if d == "" {
			continue
		}
		if host == d || strings.HasSuffix(host, "."+d) {
			return true
		}
	}
	return false
}

func run(ctx context.Context, st *store.Store, idx *index.Index, snap snapshot.KVSource, proc *processor.Processor, workers int, hostFilter func(string) bool) (Stats, error) {
	var stats Stats
	if workers <= 0 {
		workers = runtime.GOMAXPROCS(0)
	}

	urls, err := st.DocumentURLs(ctx)
	if err != nil {
		return stats, fmt.Errorf("load document urls: %w", err)
	}
	// 站点分类一次性全量加载：原来每篇一次 SiteCategory（1131 次 SQL 往返），
	// 而 whitelist 只有几十行。后缀匹配（tech.meituan.com → meituan.com）
	// 在 Go 侧复刻 SQL 的「最长匹配」语义，保持标签判定与串行版完全一致。
	cats, err := st.AllSiteCategories(ctx)
	if err != nil {
		return stats, fmt.Errorf("load site categories: %w", err)
	}
	// 站点级标签同理一次性加载（分类 + 标签一起按最长后缀匹配）。
	siteLbls, err := st.AllSiteLabels(ctx)
	if err != nil {
		return stats, fmt.Errorf("load site labels: %w", err)
	}

	// 索引批量写：逐条 IndexDocument 的吞吐恒定在 41 篇/秒（实测 1k/10k 同值），
	// 重建 50 万篇要 3.4 小时。攒批提交把每条一次的提交开销摊掉。
	const indexBatch = 500
	var buf []index.Doc
	flush := func() error {
		if len(buf) == 0 {
			return nil
		}
		n, ferr := idx.IndexDocuments(buf)
		stats.Indexed += n
		buf = buf[:0]
		return ferr
	}

	// ---------- 三段流水线 ----------
	jobs := make(chan job, workers*2)
	results := make(chan outcome, workers*2)

	// 扫描段：Pebble 迭代器只在这一个 goroutine 里用。
	var scanErr error
	var scanWg sync.WaitGroup
	scanWg.Add(1)
	go func() {
		defer scanWg.Done()
		defer close(jobs)
		scanErr = snap.Scan(kv.Key(""), func(key string, raw []byte) error {
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
			if hostFilter != nil && pageURL != "" && !hostFilter(hostOf(urlKey)) {
				stats.Skipped++
				return nil
			}
			if pageURL == "" {
				// 孤儿快照：documents 里已没有这条记录，说明它被下架了
				// （purge 噪声页 / 后台删除）。绝不能在此兜底成 "https://"+urlKey 再
				// UpsertDocument——那会把已删除的文档重新写回元数据表并重建进索引，
				// 真实事故：服务协议/搜索页/标签页在 reindex 后集体复活并占据搜索结果首位。
				stats.Orphans++
				return nil
			}
			// 必须拷贝 raw：Pebble 的 iter.Value() 契约上只在该步有效，
			// 下次 Next() 后不保证仍然指向同一内容。这里是唯一一处
			// 把快照交给其他 goroutine 的地方（repair/dedupe 都在回调内同步用完），
			// 实测当前版本没出现数据错位，但依赖未定义行为本身就是隐患——
			// 一旦哪天换 Pebble 版本，症状会是"文档内容与标题来自不同页面"，
			// 而且不报错、只能靠评测发现。
			rawCopy := make([]byte, len(raw))
			copy(rawCopy, raw)
			select {
			case jobs <- job{urlKey: urlKey, pageURL: pageURL, host: hostOf(urlKey), raw: rawCopy}:
				return nil
			case <-ctx.Done():
				return ctx.Err()
			}
		})
	}()

	// 处理段：N 个 worker 并行跑 processor（纯 CPU，无 I/O）。
	var procWg sync.WaitGroup
	for i := 0; i < workers; i++ {
		procWg.Add(1)
		go func() {
			defer procWg.Done()
			for j := range jobs {
				t0 := time.Now()
				res, err := proc.Process(j.pageURL, j.raw, processor.Options{
					SiteCategory: categoryFor(j.host, cats),
				})
				dur := time.Since(t0)
				select {
				case results <- outcome{pageURL: j.pageURL, host: j.host, res: res, procDur: dur, err: err}:
				case <-ctx.Done():
					return
				}
			}
		}()
	}
	go func() {
		procWg.Wait()
		close(results)
	}()

	// 写入段：单个 goroutine 消费结果，SQLite 与 Bleve 的写入都只在这里发生。
	//
	// 进度日志每 200 篇一条：全量重建是分钟级操作，静默运行会让人无法判断
	// 是卡住了还是在推进（实测 Windows 上 Defender 扫描新写入的索引文件时，
	// 同样 1137 篇从 93 秒变成 13 分钟，没有进度就只能干等或盲杀）。
	started := time.Now()
	done := 0
	// 分段累计：写入段是单 goroutine，一旦它慢就会把 worker 全堵住
	// （channel 一满，24 个 worker 都在等）。有一次 1137 篇从 93 秒变成
	// 13 分钟，就是靠这组分段才看出堵在 SQLite 往返上。
	var procSum, getSum, upsertSum, flushSum time.Duration
	for o := range results {
		done++
		if done%200 == 0 {
			slog.Info("reindex progress", "done", done, "scanned", stats.Scanned,
				"elapsed", time.Since(started).Round(time.Second).String(),
				"proc_ms", procSum.Milliseconds(), "getdoc_ms", getSum.Milliseconds(),
				"upsert_ms", upsertSum.Milliseconds(), "flush_ms", flushSum.Milliseconds())
		}
		procSum += o.procDur
		if o.err != nil {
			stats.Errors++
			continue // 单页失败不阻断整体重建（与串行版同样的容错口径）
		}
		// 保留原有抓取时间与发布时间，避免重建后时间信息漂移
		fetched := time.Now().Unix()
		var published *int64
		var oldTitle string
		tGet := time.Now()
		old, ok, gerr := st.GetDocument(ctx, o.pageURL)
		getSum += time.Since(tGet)
		if gerr == nil && ok {
			fetched = old.FetchedAt
			published = old.PublishedAt
			oldTitle = old.Title
		}
		if o.res.PublishedAt != nil {
			published = o.res.PublishedAt
		}
		// 标题不能跟着重建一起降级：快照可能是纯文本（seed 语料没有 HTML 结构），
		// 从文本里提不到标题，processor 会兜底成域名，把表里正确的标题覆盖掉。
		// 实测这个覆盖曾让语料内文档 41% 退化成域名。详见 processor.PickTitle。
		title := processor.PickTitle(o.res.Title, oldTitle, o.host)

		buf = append(buf, index.Doc{
			URL: o.pageURL, Domain: o.host, Title: title, Description: o.res.Description,
			Body: o.res.Body, Labels: mergeSiteLabels(o.host, siteLbls, o.res.Labels),
			Category: categoryFor(o.host, cats), Lang: o.res.Lang,
			PublishedAt: published, FetchedAt: fetched,
		})
		if len(buf) >= indexBatch {
			tFlush := time.Now()
			if err := flush(); err != nil {
				stats.Errors++
			}
			flushSum += time.Since(tFlush)
		}
		tUpsert := time.Now()
		if _, err := st.UpsertDocument(ctx, store.Document{
			URL: o.pageURL, Domain: o.host, Title: title, Description: o.res.Description,
			Labels: processor.LabelsJSON(o.res.Labels), LabelReasons: processor.ReasonsJSON(o.res.LabelReasons),
			WordCount: o.res.WordCount, Simhash: o.res.Simhash, ShingleHash: o.res.ShingleHash,
			PublishedAt: published, FetchedAt: fetched,
		}); err != nil {
			stats.Errors++
		}
		upsertSum += time.Since(tUpsert)
	}

	scanWg.Wait() // 保证 stats.Scanned/Orphans 已写完再读
	if scanErr != nil {
		return stats, scanErr
	}
	// 收尾：最后不足一批的文档
	if err := flush(); err != nil {
		stats.Errors++
	}

	// 统计覆盖率：documents 表里有、KV 里没有的文档无法重建（需重新抓取）
	for _, u := range urls {
		if _, found, err := snap.Get(kv.Key(urlkey.Key(u))); err == nil && !found {
			stats.Uncovered++
		}
	}
	return stats, nil
}

// hostOf 从归一化 URL key 取主机名（key 形如 "example.com/path"）。
func hostOf(urlKey string) string {
	if i := strings.Index(urlKey, "/"); i >= 0 {
		return urlKey[:i]
	}
	return urlKey
}

// mergeSiteLabels 把该域名（含父域）的站点级标签并进页级标签。
// 与 crawler 里的 labels.Merge 是同一套语义，只是这里按最长后缀现算。
func mergeSiteLabels(host string, all map[string][]string, page []string) []string {
	site := siteLabelsFor(host, all)
	if len(site) == 0 {
		return page
	}
	merged, _ := labels.Merge(page, nil, site)
	return merged
}

// siteLabelsFor 复刻 AllSiteLabels 的匹配语义：取最长后缀那一条的标签。
func siteLabelsFor(host string, all map[string][]string) []string {
	host = strings.ToLower(host)
	var best string
	for d := range all {
		d = strings.ToLower(d)
		if host == d || strings.HasSuffix(host, "."+d) {
			if len(d) > len(best) {
				best = d
			}
		}
	}
	if best == "" {
		return nil
	}
	return all[best]
}

// categoryFor 复刻 SiteCategory 的 SQL 语义：白名单里最长的那个域名，
// 等于 host 或是 host 的后缀（带点边界）。whitelist 已全量载入，
// 逐篇查 SQL 是 1131 次没必要的往返。
func categoryFor(host string, cats map[string]string) string {
	best := ""
	for d := range cats {
		if d == host || strings.HasSuffix(host, "."+d) {
			if len(d) > len(best) {
				best = d
			}
		}
	}
	return cats[best]
}
