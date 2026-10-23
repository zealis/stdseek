// Package crawler 实现白名单策展爬虫骨架。
//
// 红线：种子只能来自 whitelist 表（管理员提交 + 用户提交审核通过），
// 出链仅在「同站 / 同白名单域名」内跟进，禁止开放 BFS。
// 参考 sese-engine 虫.py 的已验证策略：robots 检查、按域名限流、utf8→gbk 兜底、重定向记录。
package crawler

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"time"

	"github.com/gocolly/colly/v2"
	"golang.org/x/text/encoding/simplifiedchinese"

	"voidxo/internal/index"
	"voidxo/internal/kv"
	"voidxo/internal/metrics"
	"voidxo/internal/processor"
	"voidxo/internal/robots"
	"voidxo/internal/store"
	"voidxo/internal/urlkey"
)

// listPageMinWords 应用链接密度判断的最小正文词数。
// 与 MinWordCount 解耦：真实语料里需要拦的列表页只有 163-275 词
// （redis 营销页 0.712、博客园列表 0.918），门槛设 300 会把它们放过去。
const listPageMinWords = 150

// Config 爬虫参数。
type Config struct {
	UserAgent       string        // 爬虫 UA，同时用于 robots 判定
	SeedScheme      string        // 种子 URL 协议：https（默认）/ http（测试或 http-only 站点）
	DomainDelay     time.Duration // 同域名两次请求的最小间隔（限流）
	MaxDepth        int           // 域内 BFS 最大深度
	MaxPagesPerHost int           // 单次运行单域名最多抓取页数
	// MinWordCount 入索引的最小正文词数（分词后 token 数）。
	// 低于该值视为导航页/营销页：不索引、不落库、不写出链，避免污染检索结果。
	MinWordCount int
	// MaxLinkDensity 正文中链接文本占比上限（0 表示不判断）。
	// 真实语料实测呈双峰分布：列表页 >=0.5，文章页 <=0.3，0.4-0.5 区间为空，
	// 因此 0.5 是安全阈值。
	MaxLinkDensity float64
	// Concurrency 同时抓取的站点数。域内始终串行（受 DomainDelay 约束），
	// 跨站并发才能让几十个站点的一轮抓取在可接受时间内完成。
	Concurrency int
	Timeout     time.Duration
}

// DefaultConfig 默认参数（保守值，避免给站点压力）。
func DefaultConfig() Config {
	return Config{
		UserAgent:       "voidxo-spider/0.1 (+https://voidxo.example/bot)",
		SeedScheme:      "https",
		DomainDelay:     2 * time.Second,
		MaxDepth:        2,
		MaxPagesPerHost: 10,
		MinWordCount:    150,
		MaxLinkDensity:  0.5,
		Concurrency:     4,
		Timeout:         15 * time.Second,
	}
}

// Stats 单次运行统计。
type Stats struct {
	Seeds    int
	Fetched  int
	Indexed  int
	Skipped  int // 白名单/黑名单/robots/页数上限拦截
	Thin     int // 正文过短，未入索引
	ListPage int // 链接密度过高（列表/聚合页），未入索引
	Errors   int
}

// Crawler 白名单爬虫。
type Crawler struct {
	st   *store.Store
	idx  *index.Index
	proc *processor.Processor
	snap *kv.Store // 原文快照（可为 nil，测试时可不落 KV）
	cfg  Config
}

// New 构造爬虫。proc 负责正文提取、分词与标签打标；snap 保存原文快照。
func New(st *store.Store, idx *index.Index, proc *processor.Processor, snap *kv.Store, cfg Config) *Crawler {
	if cfg.UserAgent == "" {
		cfg = DefaultConfig()
	}
	if cfg.SeedScheme == "" {
		cfg.SeedScheme = "https"
	}
	return &Crawler{st: st, idx: idx, proc: proc, snap: snap, cfg: cfg}
}

// LoadSeeds 从 SQLite whitelist 表读取启用站点种子（唯一种子来源）。
func (c *Crawler) LoadSeeds(ctx context.Context) ([]string, error) {
	return c.st.SeedURLs(ctx)
}

// Run 读取白名单种子并抓取（忽略抓取间隔，用于手动全量抓取）。
func (c *Crawler) Run(ctx context.Context) (Stats, error) {
	seeds, err := c.LoadSeeds(ctx)
	if err != nil {
		return Stats{}, fmt.Errorf("load seeds: %w", err)
	}
	return c.CrawlSeeds(ctx, seeds)
}

// RunDue 只抓取到期的白名单站点（按 crawl_interval 调度），返回本次任务统计。
func (c *Crawler) RunDue(ctx context.Context) (Stats, error) {
	sites, err := c.st.DueSites(ctx, time.Now().Unix())
	if err != nil {
		return Stats{}, fmt.Errorf("load due sites: %w", err)
	}
	metrics.DueSites.Set(float64(len(sites)))
	if len(sites) == 0 {
		return Stats{}, nil
	}
	seeds := make([]string, 0, len(sites))
	for _, s := range sites {
		seeds = append(seeds, c.cfg.SeedScheme+"://"+s.Domain+"/")
	}
	stats, err := c.CrawlSeeds(ctx, seeds)
	if err != nil {
		metrics.CrawlRuns.WithLabelValues("error").Inc()
		return stats, err
	}
	metrics.CrawlRuns.WithLabelValues("ok").Inc()
	return stats, nil
}

// CrawlSeeds 抓取给定种子（测试可直接注入 httptest URL）。
// 每个站点使用独立 collector 并发执行（上限 Config.Concurrency），
// 站点内部仍串行并受 DomainDelay 约束——跨站并发是我们自己的吞吐，
// 域内串行是对目标站点的礼貌。
func (c *Crawler) CrawlSeeds(ctx context.Context, seeds []string) (Stats, error) {
	stats := Stats{Seeds: len(seeds)}
	if len(seeds) == 0 {
		return stats, nil
	}

	robotCache := robots.NewCache(c.cfg.UserAgent, c.cfg.Timeout)

	concurrency := c.cfg.Concurrency
	if concurrency <= 0 {
		concurrency = 1
	}
	if concurrency > len(seeds) {
		concurrency = len(seeds)
	}

	var (
		wg sync.WaitGroup
		mu sync.Mutex
	)
	sem := make(chan struct{}, concurrency)

	for _, seed := range seeds {
		select {
		case sem <- struct{}{}:
		case <-ctx.Done():
			wg.Wait()
			return stats, ctx.Err()
		}
		wg.Add(1)
		go func(seed string) {
			defer wg.Done()
			defer func() { <-sem }()
			one := c.crawlOne(ctx, seed, robotCache)
			mu.Lock()
			stats.Fetched += one.Fetched
			stats.Indexed += one.Indexed
			stats.Skipped += one.Skipped
			stats.Thin += one.Thin
			stats.ListPage += one.ListPage
			stats.Errors += one.Errors
			mu.Unlock()
		}(seed)
	}
	wg.Wait()
	return stats, ctx.Err()
}

// crawlOne 抓取单个站点，返回该站点的局部统计（避免共享状态竞态）。
func (c *Crawler) crawlOne(ctx context.Context, seed string, robotCache *robots.Cache) Stats {
	stats := Stats{Seeds: 1}
	host := parseHost(seed)

	// 白名单分类每站只查一次（原实现每页一次 SQL，并发下会争抢单连接）
	category, err := c.st.SiteCategory(ctx, host)
	if err != nil {
		slog.Warn("site category lookup failed", "host", host, "err", err)
	}

	// 允许的 host：种子自身 + www 变体。
	// 白名单的父域匹配（如 cnblogs.com → account.cnblogs.com）在重定向时会被连带放行，
	// 这里再收一道：跨子域的重定向不进（除非管理员把该子域单独加白名单）。
	allowedHost := map[string]bool{}
	if host != "" {
		allowedHost[host] = true
		if strings.HasPrefix(host, "www.") {
			allowedHost[strings.TrimPrefix(host, "www.")] = true
		} else {
			allowedHost["www."+host] = true
		}
	}

	pages := 0 // 单 collector 内串行访问（Async(false)），无需加锁

	col := colly.NewCollector(
		colly.UserAgent(c.cfg.UserAgent),
		colly.MaxDepth(c.cfg.MaxDepth),
		colly.Async(false),
	)
	col.SetRequestTimeout(c.cfg.Timeout)
	// colly 的 HTTP 重定向走 http.Client.CheckRedirect，不触发 OnRequest，
	// 只经过 checkFilters —— 必须用 AllowedDomains（精确匹配）才能拦住跨子域重定向。
	// 真实案例：cnblogs.com 重定向到 account.cnblogs.com/blogapply，白名单父域匹配放行了它。
	hosts := make([]string, 0, len(allowedHost))
	for h := range allowedHost {
		hosts = append(hosts, h)
	}
	col.AllowedDomains = hosts
	// robots 由本包按 sese-engine 策略显式检查（带缓存），关闭 colly 内置实现避免双重请求
	col.IgnoreRobotsTxt = true
	_ = col.Limit(&colly.LimitRule{DomainGlob: "*", Delay: c.cfg.DomainDelay})

	col.OnRequest(func(r *colly.Request) {
		reqHost := r.URL.Hostname()
		// 只允许种子 host 与 www 变体（拦截重定向到 login/account 等子域）
		if !allowedHost[reqHost] {
			r.Abort()
			stats.Skipped++
			metrics.CrawlPages.WithLabelValues("skipped").Inc()
			return
		}
		if pages >= c.cfg.MaxPagesPerHost {
			r.Abort()
			stats.Skipped++
			metrics.CrawlPages.WithLabelValues("skipped").Inc()
			return
		}
		// 白名单准入：未收录域名一律不抓（含出链扩展）
		ok, err := c.st.IsWhitelisted(ctx, reqHost)
		if err != nil || !ok {
			r.Abort()
			stats.Skipped++
			metrics.CrawlPages.WithLabelValues("skipped").Inc()
			return
		}
		// 黑名单拦截层
		blocked, err := c.st.IsBlacklisted(ctx, reqHost)
		if err != nil || blocked {
			r.Abort()
			stats.Skipped++
			metrics.CrawlPages.WithLabelValues("skipped").Inc()
			return
		}
		// robots.txt 检查（缓存，失败时放行）
		if !robotCache.Allowed(r.URL.String()) {
			r.Abort()
			stats.Skipped++
			metrics.CrawlPages.WithLabelValues("skipped").Inc()
			return
		}
		pages++
	})

	col.OnResponse(func(r *colly.Response) {
		// 中文站编码兜底：非 UTF-8 时按 GBK 解码（参考 sese-engine 虫.py::_解析文本）
		if body := decodeBody(r.Body); body != nil {
			r.Body = body
		}
		stats.Fetched++
	})

	col.OnHTML("html", func(e *colly.HTMLElement) {
		pageURL := e.Request.URL.String()
		pageHost := e.Request.URL.Hostname()

		// 正文提取 + simhash + 分词 + 标签（全部索引期完成，查询期只做过滤）
		res, err := c.proc.Process(pageURL, e.Response.Body, processor.Options{SiteCategory: category})
		if err != nil {
			slog.Warn("process failed", "url", pageURL, "err", err)
			stats.Errors++
			return
		}

		// 薄页（导航/营销页）不进索引：这类页面会稀释检索质量
		if res.WordCount < c.cfg.MinWordCount {
			stats.Thin++
			metrics.CrawlPages.WithLabelValues("thin").Inc()
			return
		}
		// 列表页：正文大半是链接（博客园首页、InfoQ 首页这类聚合页）。
		// 只在正文足够长时判断：短页面本来就被薄页阈值管着，
		// 再叠加密度会把「短正文 + 几个导航链接」的正常页误杀。
		minWordsForDensity := c.cfg.MinWordCount
		if minWordsForDensity < listPageMinWords {
			minWordsForDensity = listPageMinWords
		}
		if c.cfg.MaxLinkDensity > 0 && res.WordCount >= minWordsForDensity &&
			res.LinkDensity > c.cfg.MaxLinkDensity {
			stats.ListPage++
			metrics.CrawlPages.WithLabelValues("listpage").Inc()
			return
		}

		doc := index.Doc{
			URL:         pageURL,
			Domain:      pageHost,
			Title:       res.Title,
			Description: res.Description,
			Body:        res.Body,
			Labels:      res.Labels,
			PublishedAt: res.PublishedAt,
			FetchedAt:   res.FetchedAt,
		}
		if err := c.idx.IndexDocument(doc); err != nil {
			slog.Warn("index document failed", "url", doc.URL, "err", err)
			stats.Errors++
			metrics.CrawlPages.WithLabelValues("error").Inc()
			return
		}
		// 元数据 + 指纹 + 标签理由落 SQLite，供后台审核与误判反馈
		if _, err := c.st.UpsertDocument(ctx, store.Document{
			URL:          pageURL,
			Domain:       pageHost,
			Title:        res.Title,
			Description:  res.Description,
			Labels:       processor.LabelsJSON(res.Labels),
			LabelReasons: processor.ReasonsJSON(res.LabelReasons),
			WordCount:    res.WordCount,
			Simhash:      res.Simhash,
			PublishedAt:  res.PublishedAt,
			FetchedAt:    res.FetchedAt,
		}); err != nil {
			slog.Warn("persist document failed", "url", pageURL, "err", err)
		}
		// 原文快照落 KV：证据链/原文回溯的地基（单页上限 2MB，超限截断并标注）
		if c.snap != nil {
			raw := e.Response.Body
			if len(raw) > 2*1024*1024 {
				raw = raw[:2*1024*1024]
			}
			if err := c.snap.Put(kv.Key(urlkey.Key(pageURL)), raw); err != nil {
				slog.Warn("kv snapshot failed", "url", pageURL, "err", err)
			} else {
				metrics.SnapshotsWritten.Inc()
			}
		}

		// 出链落库：繁荣度统计器的输入（只保留 http/https，单页最多 500 条）
		var targets []string
		for _, href := range e.ChildAttrs("a[href]", "href") {
			abs := e.Request.AbsoluteURL(href)
			if abs == "" || !strings.HasPrefix(abs, "http") {
				continue
			}
			targets = append(targets, abs)
			if len(targets) >= 500 {
				break
			}
		}
		if err := c.st.ReplaceOutlinks(ctx, pageURL, pageHost, targets); err != nil {
			slog.Warn("persist outlinks failed", "url", pageURL, "err", err)
		}
		stats.Indexed++
		metrics.IndexedDocs.Inc()
		metrics.CrawlPages.WithLabelValues("ok").Inc()
	})

	col.OnError(func(r *colly.Response, err error) {
		stats.Errors++
		metrics.CrawlPages.WithLabelValues("error").Inc()
		slog.Debug("crawl error", "url", r.Request.URL.String(), "status", r.StatusCode, "err", err)
	})

	col.OnHTML("a[href]", func(e *colly.HTMLElement) {
		// 域内 BFS：只跟进同主机链接；colly 会按 MaxDepth 截断
		link := e.Request.AbsoluteURL(e.Attr("href"))
		if link == "" {
			return
		}
		if e.Request.URL.Hostname() != parseHost(link) {
			return
		}
		col.Visit(link)
	})

	// 抓取并回写状态（按本站增量判断，失败计数驱动退避）
	visitErr := col.Visit(seed)
	status := "ok"
	errMsg := ""
	switch {
	case visitErr != nil:
		status, errMsg = "error", visitErr.Error()
		slog.Debug("visit seed failed", "seed", seed, "err", visitErr)
	case stats.Indexed == 0:
		status = "no-document"
	}
	if host != "" {
		if err := c.st.TouchCrawlResult(ctx, host, status, errMsg); err != nil {
			slog.Warn("touch crawl result failed", "host", host, "err", err)
		}
	}
	return stats
}

// ---------- helpers ----------

// decodeBody 返回 UTF-8 编码后的正文；已是合法 UTF-8 时返回 nil 表示无需替换。
func decodeBody(body []byte) []byte {
	if isUTF8(body) {
		return nil
	}
	decoded, err := simplifiedchinese.GBK.NewDecoder().Bytes(body)
	if err != nil {
		return nil
	}
	return decoded
}
