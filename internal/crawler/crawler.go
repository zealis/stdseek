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
	"time"

	"github.com/gocolly/colly/v2"
	"github.com/temoto/robotstxt"
	"golang.org/x/text/encoding/simplifiedchinese"

	"voidxo/internal/index"
	"voidxo/internal/metrics"
	"voidxo/internal/processor"
	"voidxo/internal/store"
)

// Config 爬虫参数。
type Config struct {
	UserAgent       string        // 爬虫 UA，同时用于 robots 判定
	DomainDelay     time.Duration // 同域名两次请求的最小间隔（限流）
	MaxDepth        int           // 域内 BFS 最大深度
	MaxPagesPerHost int           // 单次运行单域名最多抓取页数
	Timeout         time.Duration
}

// DefaultConfig 默认参数（保守值，避免给站点压力）。
func DefaultConfig() Config {
	return Config{
		UserAgent:       "voidxo-spider/0.1 (+https://voidxo.example/bot)",
		DomainDelay:     2 * time.Second,
		MaxDepth:        2,
		MaxPagesPerHost: 10,
		Timeout:         15 * time.Second,
	}
}

// Stats 单次运行统计。
type Stats struct {
	Seeds   int
	Fetched int
	Indexed int
	Skipped int
	Errors  int
}

// Crawler 白名单爬虫。
type Crawler struct {
	st   *store.Store
	idx  *index.Index
	proc *processor.Processor
	cfg  Config
}

// New 构造爬虫。proc 负责正文提取、分词与标签打标。
func New(st *store.Store, idx *index.Index, proc *processor.Processor, cfg Config) *Crawler {
	if cfg.UserAgent == "" {
		cfg = DefaultConfig()
	}
	return &Crawler{st: st, idx: idx, proc: proc, cfg: cfg}
}

// LoadSeeds 从 SQLite whitelist 表读取启用站点种子（唯一种子来源）。
func (c *Crawler) LoadSeeds(ctx context.Context) ([]string, error) {
	return c.st.SeedURLs(ctx)
}

// Run 读取白名单种子并抓取。
func (c *Crawler) Run(ctx context.Context) (Stats, error) {
	seeds, err := c.LoadSeeds(ctx)
	if err != nil {
		return Stats{}, fmt.Errorf("load seeds: %w", err)
	}
	return c.CrawlSeeds(ctx, seeds)
}

// CrawlSeeds 抓取给定种子（测试可直接注入 httptest URL）。
func (c *Crawler) CrawlSeeds(ctx context.Context, seeds []string) (Stats, error) {
	stats := Stats{Seeds: len(seeds)}
	if len(seeds) == 0 {
		return stats, nil
	}

	robots := newRobotsCache(c.cfg)
	perHost := map[string]int{}

	col := colly.NewCollector(
		colly.UserAgent(c.cfg.UserAgent),
		colly.MaxDepth(c.cfg.MaxDepth),
		colly.Async(false),
	)
	col.SetRequestTimeout(c.cfg.Timeout)
	// robots 由本包按 sese-engine 策略显式检查（带缓存），关闭 colly 内置实现避免双重请求
	col.IgnoreRobotsTxt = true
	_ = col.Limit(&colly.LimitRule{DomainGlob: "*", Delay: c.cfg.DomainDelay})

	col.OnRequest(func(r *colly.Request) {
		host := r.URL.Hostname()
		if perHost[host] >= c.cfg.MaxPagesPerHost {
			r.Abort()
			stats.Skipped++
			metrics.CrawlPages.WithLabelValues("skipped").Inc()
			return
		}
		// 白名单准入：未收录域名一律不抓（含出链扩展）
		ok, err := c.st.IsWhitelisted(ctx, host)
		if err != nil || !ok {
			r.Abort()
			stats.Skipped++
			metrics.CrawlPages.WithLabelValues("skipped").Inc()
			return
		}
		// 黑名单拦截层
		blocked, err := c.st.IsBlacklisted(ctx, host)
		if err != nil || blocked {
			r.Abort()
			stats.Skipped++
			metrics.CrawlPages.WithLabelValues("skipped").Inc()
			return
		}
		// robots.txt 检查（缓存，失败时保守放行首页、禁止深层路径由 Allow 决定）
		if !robots.allowed(r.URL.String()) {
			r.Abort()
			stats.Skipped++
			metrics.CrawlPages.WithLabelValues("skipped").Inc()
			return
		}
		perHost[host]++
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
		host := e.Request.URL.Hostname()
		category, err := c.st.SiteCategory(ctx, host)
		if err != nil {
			slog.Warn("site category lookup failed", "host", host, "err", err)
		}
		// 正文提取 + simhash + 分词 + 标签（全部索引期完成，查询期只做过滤）
		res, err := c.proc.Process(pageURL, e.Response.Body, processor.Options{SiteCategory: category})
		if err != nil {
			slog.Warn("process failed", "url", pageURL, "err", err)
			stats.Errors++
			return
		}

		doc := index.Doc{
			URL:         pageURL,
			Domain:      host,
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
			Domain:       host,
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
		if err := c.st.ReplaceOutlinks(ctx, pageURL, host, targets); err != nil {
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

	for _, seed := range seeds {
		if err := ctx.Err(); err != nil {
			return stats, err
		}
		if err := col.Visit(seed); err != nil {
			stats.Errors++
			slog.Debug("visit seed failed", "seed", seed, "err", err)
		}
		// 回写最后抓取状态（白名单管理页展示）
		if host := parseHost(seed); host != "" {
			status := "ok"
			if stats.Indexed == 0 {
				status = "no-document"
			}
			_ = c.st.TouchCrawlResult(ctx, host, status, "")
		}
	}
	return stats, nil
}

// ---------- robots ----------

type robotsCache struct {
	cfg   Config
	cache map[string]*robotstxt.RobotsData
}

func newRobotsCache(cfg Config) *robotsCache {
	return &robotsCache{cfg: cfg, cache: map[string]*robotstxt.RobotsData{}}
}

// allowed 检查 URL 是否被 robots.txt 允许。无法获取 robots.txt 时放行（常见做法）。
func (rc *robotsCache) allowed(rawURL string) bool {
	host := parseHost(rawURL)
	if host == "" {
		return false
	}
	data, ok := rc.cache[host]
	if !ok {
		data = fetchRobots(rawURL, rc.cfg.UserAgent, rc.cfg.Timeout)
		rc.cache[host] = data
	}
	if data == nil {
		return true
	}
	return data.TestAgent(pathOf(rawURL), rc.cfg.UserAgent)
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
