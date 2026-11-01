// Package crawler 实现白名单策展爬虫骨架。
//
// 红线：种子只能来自 whitelist 表（管理员提交 + 用户提交审核通过），
// 出链仅在「同站 / 同白名单域名」内跟进，禁止开放 BFS。
// 参考 sese-engine 虫.py 的已验证策略：robots 检查、按域名限流、utf8→gbk 兜底、重定向记录。
package crawler

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/gocolly/colly/v2"
	"golang.org/x/text/encoding/simplifiedchinese"

	"github.com/zealis/stdseek/internal/ua"

	"github.com/zealis/stdseek/internal/index"
	"github.com/zealis/stdseek/internal/kv"
	"github.com/zealis/stdseek/internal/labels"
	"github.com/zealis/stdseek/internal/metrics"
	"github.com/zealis/stdseek/internal/outbound"
	"github.com/zealis/stdseek/internal/processor"
	"github.com/zealis/stdseek/internal/robots"
	"github.com/zealis/stdseek/internal/snapshot"
	"github.com/zealis/stdseek/internal/store"
	"github.com/zealis/stdseek/internal/urlkey"
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
	MaxPagesPerHost int           // 单次运行单域名最多抓取页数（DynamicQuota 关闭时使用）
	// DynamicQuota 按站点繁荣度动态决定单站抓取页数。
	//
	// 抄自 sese-engine 的反垄断兴趣衰减（上网.py 重整函数）：
	//	限制 = 繁荣度*500 + 50
	//	兴趣 = 0.1**(1/限制) ** 已访问次数
	// 即「访问越多兴趣越低，但繁荣站容忍更多访问」。stdseek 的白名单是固定的，
	// 对应的形态是抓取配额：繁荣站多抓（内容多、值得索引），
	// 长尾站保底（QuotaBase，不被饿死——深网发现区要有东西可捞）。
	DynamicQuota bool
	// QuotaBase 保底页数：繁荣度为 0 的站点也至少抓这么多。
	QuotaBase int
	// QuotaSpan 繁荣度折算的额外页数。繁荣度归一化到 0~1，
	// 满繁荣度的站点配额 = QuotaBase + QuotaSpan。
	QuotaSpan int
	// QuotaMax 单站绝对上限，防止个别站吃满整轮时间。
	QuotaMax int
	// MinWordCount 入索引的最小正文词数（分词后 token 数）。
	// 低于该值视为导航页/营销页：不索引、不落库、不写出链，避免污染检索结果。
	MinWordCount int
	// MinLangRatio 正文中「中英文」字符占全部字母的最低比例（0 表示不判断）。
	// 白名单站点常带多语言镜像路径（/ru/、/bn/），这些页面不该进中文技术索引。
	MinLangRatio float64
	// MaxLinkDensity 正文中链接文本占比上限（0 表示不判断）。
	// 真实语料实测呈双峰分布：列表页 >=0.5，文章页 <=0.3，0.4-0.5 区间为空，
	// 因此 0.5 是安全阈值。
	MaxLinkDensity float64
	// Concurrency 同时抓取的站点数。域内始终串行（受 DomainDelay 约束），
	// 跨站并发才能让几十个站点的一轮抓取在可接受时间内完成。
	Concurrency int
	Timeout     time.Duration
}

// userAgent 当前该用的 UA。
//
// 每次抓取时读一次开关，后台改完下一轮就生效，不需要重启进程。
// 读取失败按「关」处理：宁可自报身份，也不要在读不到配置时偷偷伪装。
func (c *Crawler) userAgent() string {
	v, err := c.st.Setting(context.Background(), ua.BrowserFeatureKey, "false")
	if err != nil {
		return c.cfg.UserAgent
	}
	return userAgentFrom(c.cfg.UserAgent, v)
}

// userAgentFrom 是 userAgent 的纯逻辑部分，方便测试。
func userAgentFrom(base, setting string) string {
	if on, err := strconv.ParseBool(setting); err == nil && on {
		return ua.Browser
	}
	return base
}

// DefaultConfig 默认参数（保守值，避免给站点压力）。
func DefaultConfig() Config {
	return Config{
		// 只报名字，地址由启动时补（见 cmd/stdseek 的 crawlUserAgent）。
		// 以前这里写死 "stdseek-spider/0.1 (+https://stdseek.example/bot)"，
		// stdseek.example 是个从来不存在的占位域名——按爬虫规范，bot 标识里的
		// 联系地址必须真实可访问，有的站点/WAF 见到这种标识直接 403
		// （2026-10-30 排查 linux.do 时发现：香港节点能连通，但首页返回 403）。
		UserAgent:       ua.Bot,
		SeedScheme:      "https",
		DomainDelay:     2 * time.Second,
		MaxDepth:        3,
		MaxPagesPerHost: 25,
		DynamicQuota:    true,
		// QuotaBase 15 而不是 10：繁荣度表只覆盖「语料内有人引用」的域名
		// （当前 19/23），像美团技术团队这样内容多但无人引用的站会落进保底档。
		// 15 页/小时对定时增量够用，也不会让首轮建库显得太薄。
		QuotaBase:      15,
		QuotaSpan:      40,
		QuotaMax:       80,
		MinWordCount:   150,
		MinLangRatio:   0.5,
		MaxLinkDensity: 0.5,
		Concurrency:    4,
		Timeout:        15 * time.Second,
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
	LangSkip int // 非中英文页面，未入索引
	// Boilerplate 站务内容（服务协议/隐私政策/版权声明），形态是文章但内容非技术。
	// 这类页面 URL 形态与文章一致（阿里云 /article/768092），article_pattern 拦不住。
	Boilerplate int
	Errors      int
}

// Crawler 白名单爬虫。
type Crawler struct {
	st   *store.Store
	idx  *index.Index
	proc *processor.Processor
	snap snapshot.Store // 原文快照（可为 nil，测试时可不落 KV）
	cfg  Config
	// pros 域名 → 繁荣度（0~1），CrawlSeeds 开始时加载一次。
	// 表里只有域名级数据（当前 19 行），全量加载比每站查一次 SQL 便宜，
	// 也避免并发抓取时争抢单连接。
	// 读写用 prosMu 保护：FetchPage 可能在定时抓取跑着的时候被前台触发。
	prosMu sync.RWMutex
	pros   map[string]float64
}

// setPros 整表替换繁荣度（只在每轮抓取开始时调用一次）。
func (c *Crawler) setPros(m map[string]float64) {
	c.prosMu.Lock()
	c.pros = m
	c.prosMu.Unlock()
}

// prosperity 取某个域名的繁荣度，0 表示没有数据。
func (c *Crawler) prosperity(host string) float64 {
	c.prosMu.RLock()
	defer c.prosMu.RUnlock()
	return c.pros[host]
}

// snapshotEnabled 后台开关：抓取时是否保存原文快照。每页读一次 SQLite——
// 一次主键查询相对这页正在发生的 HTTP 抓取与正文解析可忽略，换来「改完即生效」，
// 不必为了让开关生效重启进程。
// 读失败按开启处理：宁可多占一点磁盘，也不能因为一次查询失败悄悄断掉证据链。
func (c *Crawler) snapshotEnabled(ctx context.Context) bool {
	v, err := c.st.Setting(ctx, store.SettingSnapshotEnabled, "true")
	if err != nil {
		return true
	}
	return v != "false"
}

// New 构造爬虫。proc 负责正文提取、分词与标签打标；snap 保存原文快照。
func New(st *store.Store, idx *index.Index, proc *processor.Processor, snap snapshot.Store, cfg Config) *Crawler {
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
		seeds = append(seeds, c.cfg.SeedScheme+"://"+s.Domain+store.NormalizeSeedPath(s.SeedPath))
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

	// 繁荣度全量加载一次：动态配额与长尾调度都要用它。
	// 表里只有域名级数据（当前 19 行），一次查询搞定；逐站查会让并发 goroutine
	// 争抢单连接（SQLite MaxOpenConns=1）。
	pros := c.loadProsperity(ctx)
	c.setPros(pros)
	// 交错排序：把繁荣站与长尾站打散，保证任意时刻的并发窗口里都有长尾站在跑
	// （抄 sese-engine 的「预期繁荣网站比例」——它在 BFS 队列裁剪时强制保留
	//   一定比例的不繁荣网站；stdseek 白名单固定，所以体现在调度顺序上）。
	seeds = interleaveByProsperity(seeds, pros)

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
			one := c.crawlOne(ctx, seed, 0)
			mu.Lock()
			stats.Fetched += one.Fetched
			stats.Indexed += one.Indexed
			stats.Skipped += one.Skipped
			stats.Thin += one.Thin
			stats.ListPage += one.ListPage
			stats.LangSkip += one.LangSkip
			stats.Boilerplate += one.Boilerplate
			stats.Errors += one.Errors
			mu.Unlock()
		}(seed)
	}
	wg.Wait()
	return stats, ctx.Err()
}

// FetchPage 只抓指定页面本身，不跟进链接——结果卡上「这页没抓过，补一下」走这条路。
//
// 为什么不直接 CrawlSeeds([]string{u})：那会按站点配额抓 15~80 页，
// 用户点一下换来的是一次整站抓取（几十秒，还让目标站点白挨一轮压力）。
// 这里把取页上限压到 1，白名单 / 黑名单 / robots / 正文提取 / 去重 / 快照
// 全部沿用 crawlOne 的既有路径，行为与定时抓取完全一致。
func (c *Crawler) FetchPage(ctx context.Context, rawURL string) (Stats, error) {
	u, err := url.Parse(strings.TrimSpace(rawURL))
	if err != nil {
		return Stats{}, fmt.Errorf("URL 解析失败：%w", err)
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return Stats{}, fmt.Errorf("只能抓取 http/https 页面")
	}
	if u.Hostname() == "" {
		return Stats{}, fmt.Errorf("URL 缺少域名")
	}
	stats := c.crawlOne(ctx, u.String(), 1)
	if err := ctx.Err(); err != nil {
		return stats, err
	}
	return stats, nil
}

// parseManualLabels 解析人工锁定的标签 JSON。存的是 JSON 字符串，
// 解析失败就当没锁定——宁可按规则重算，也不能让页面变成「没有标签」的静默错误。
func parseManualLabels(labelsJSON, reasonsJSON string) ([]string, map[string]string) {
	var ls []string
	if err := json.Unmarshal([]byte(labelsJSON), &ls); err != nil {
		return nil, nil
	}
	rs := map[string]string{}
	_ = json.Unmarshal([]byte(reasonsJSON), &rs)
	// 空数组是合法的人工值（管理员判定这一页不该有标签），要原样返回，
	// 不能因为「没解析出东西」退回到规则结果。
	if ls == nil {
		ls = []string{}
	}
	return ls, rs
}

// crawlOne 抓取单个站点，返回该站点的局部统计（避免共享状态竞态）。
//
// 浏览器 UA 模式（后台「功能开关 → 浏览器 UA」）下先按浏览器标识抓；
// **一页都没入库就用爬虫标识重来一次**，两次都空才算失败。
// 理由：有的站点对浏览器身份返回 SPA 空壳、对爬虫身份返回服务端渲染的全文
// （掘金实测：Chrome UA 79KB 零链接，爬虫 UA 350KB 二十篇文章），
// 只认其中一种身份都会漏抓；先浏览器后爬虫，兼顾「过粗规则」和「拿到正文」。
func (c *Crawler) crawlOne(ctx context.Context, seed string, maxPages int) Stats {
	used := c.userAgent()
	stats, out := c.crawlWith(ctx, seed, maxPages, used)
	if used != c.cfg.UserAgent && stats.Indexed == 0 && ctx.Err() == nil {
		slog.Info("browser UA crawled nothing, retrying with spider UA",
			"seed", seed, "fetched", stats.Fetched, "errors", stats.Errors)
		retryStats, retryOut := c.crawlWith(ctx, seed, maxPages, c.cfg.UserAgent)
		stats, out = mergeStats(stats, retryStats), retryOut
	}
	// 只在两次尝试都结束后写一次状态：否则一轮抓取会被记成「连续失败两次」，
	// 后台看到的退避次数与真实轮次对不上。
	if out.NoRecord || out.Host == "" {
		return stats
	}
	status, kind, detail := classifyCrawl(out)
	c.saveCrawlOutcome(ctx, out.Host, status, kind, detail)
	return stats
}

// mergeStats 把两次尝试的计数相加：第一次的请求是真实发生的，
// 只报第二次会让日志看起来「只抓了几页」，掩盖了重试成本。
func mergeStats(a, b Stats) Stats {
	return Stats{
		Seeds:       b.Seeds, // 同一站点抓了两遍，站点数不该翻倍
		Fetched:     a.Fetched + b.Fetched,
		Indexed:     a.Indexed + b.Indexed,
		Skipped:     a.Skipped + b.Skipped,
		Thin:        a.Thin + b.Thin,
		ListPage:    a.ListPage + b.ListPage,
		LangSkip:    a.LangSkip + b.LangSkip,
		Boilerplate: a.Boilerplate + b.Boilerplate,
		Errors:      a.Errors + b.Errors,
	}
}

// crawlWith 用指定的 User-Agent 抓一遍（robots 判定也用同一个身份）。
//
// maxPages > 0 时用它代替本站配额作为上限——按需补抓走这条路，
// 一次点击只取一页，不能变成整站抓取（配额 15~80 页、几十秒、对方站点白挨打）。
func (c *Crawler) crawlWith(ctx context.Context, seed string, maxPages int, usedUA string) (Stats, crawlOutcome) {
	stats := Stats{Seeds: 1}
	host := parseHost(seed)
	// robots 按身份缓存：robotstxt 的规则可以按 UA 分组，换个身份必须换一份。
	robotCache := robots.NewCache(usedUA, c.cfg.Timeout)

	// 本 collector 的取页上限：默认按繁荣度动态配额，补抓时压到指定页数。
	pageLimit := c.quotaFor(host)
	if maxPages > 0 && maxPages < pageLimit {
		pageLimit = maxPages
	}

	// 白名单分类每站只查一次（原实现每页一次 SQL，并发下会争抢单连接）
	category, err := c.st.SiteCategory(ctx, host)
	if err != nil {
		slog.Warn("site category lookup failed", "host", host, "err", err)
	}
	// 站点级标签（AI/人工按整站判的）同样每站查一次，写索引时并进页级标签。
	siteLabels, err := c.st.SiteLabels(ctx, host)
	if err != nil {
		slog.Warn("site labels lookup failed", "host", host, "err", err)
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

	// 文章 URL 形态收口（可选）：平台站的频道页/标签页/空间页/作者页正文是摘要列表，
	// 链接密度拦不住，会把配额吃光——阿里云 23 篇入库文档里 0 篇是文章，全是 /group/xxx/。
	// 种子页不受约束（列表页本来就不是文章形态），编译失败时忽略并告警。
	var artRe *regexp.Regexp
	if pat, err := c.st.SiteArticlePattern(ctx, host); err != nil {
		slog.Warn("article pattern lookup failed", "host", host, "err", err)
	} else if pat != "" {
		if re, err := regexp.Compile(pat); err != nil {
			slog.Warn("article pattern ignored: invalid regexp", "host", host, "pattern", pat, "err", err)
		} else {
			artRe = re
		}
	}

	pages := 0 // 单 collector 内串行访问（Async(false)），无需加锁

	// 归因计数：一次整站抓完要回答「为什么这站没东西入库」，
	// 光看 stats.Skipped 分不清是 robots 拦了还是结构变了。
	var (
		robotsBlocked int
		httpErrors    int
		lastStatus    int
		lastErr       string
		parseFails    int
		firstParseErr string
	)

	col := colly.NewCollector(
		colly.UserAgent(usedUA),
		colly.MaxDepth(c.cfg.MaxDepth),
		colly.Async(false),
	)
	col.SetRequestTimeout(c.cfg.Timeout)
	// 本地/内网地址不走代理：Go 默认传输会应用 HTTP_PROXY/HTTPS_PROXY 环境变量，
	// 而本机自建的测试服务（httptest）会被代理拦死——实测环境里带沙箱代理时
	// 整批抓取测试全部 Errors。公网抓取仍尊重代理设置。
	col.WithTransport(outbound.Transport())
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
			slog.Debug("visit rejected", "url", r.URL.String(), "reason", "host-not-allowed")
			return
		}
		if pages >= pageLimit {
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
			robotsBlocked++
			stats.Skipped++
			metrics.CrawlPages.WithLabelValues("skipped").Inc()
			return
		}
		// 文章形态收口：第一个请求是种子页（列表页），放行；
		// 之后只跟进匹配文章正则的链接，频道页/标签页/作者页在这里被挡掉。
		if artRe != nil && pages > 0 && !artRe.MatchString(r.URL.Path) {
			r.Abort()
			stats.Skipped++
			metrics.CrawlPages.WithLabelValues("skipped").Inc()
			slog.Debug("visit rejected", "url", r.URL.String(), "path", r.URL.Path, "reason", "not-article")
			return
		}
		pages++
		// Debug 级记录每页访问与深度：排查「配额被导航页吃掉」时的唯一线索
		slog.Debug("visit", "url", r.URL.String(), "depth", r.Depth, "page", pages)
	})

	col.OnResponse(func(r *colly.Response) {
		// 中文站编码兜底：非 UTF-8 时按 GBK 解码（参考 sese-engine 虫.py::_解析文本）
		if body := decodeBody(r.Body); body != nil {
			r.Body = body
		}
		stats.Fetched++
	})

	col.OnHTML("html", func(e *colly.HTMLElement) {
		// 入库用规范 URL：去 fragment 后与索引 ID（urlkey.Key）指向同一个页面，
		// 否则搜索结果会给用户一个带 #commentform 的链接。
		pageURL := urlkey.Canonical(e.Request.URL.String())
		pageHost := e.Request.URL.Hostname()
		slog.Debug("html parsed", "url", pageURL, "path", e.Request.URL.Path)

		// 种子页（站点首页/列表入口）顺带提取网站名称，存 whitelist 供结果页展示。
		// UpdateSiteName 只在空值时写入，这里每轮抓取都尝试不亏。
		if pageURL == urlkey.Canonical(seed) {
			if name := processor.SiteName(e.Response.Body); name != "" {
				if err := c.st.UpdateSiteName(ctx, host, name); err != nil {
					slog.Debug("site name update failed", "host", host, "err", err)
				} else {
					slog.Debug("site name captured", "host", host, "name", name)
				}
			}
			// 站点描述（og:description / meta description）：AI 分类要吃「网站怎么介绍自己」。
			if desc := processor.SiteDescription(e.Response.Body); desc != "" {
				if err := c.st.UpdateSiteDesc(ctx, host, desc); err != nil {
					slog.Debug("site desc update failed", "host", host, "err", err)
				}
			}
		}

		// 文章形态收口：列表页/首页（含种子页）只用来提出链，不进索引。
		// 美团首页正文 28773 词，不拦会把 BM25 的候选池撑爆。
		if artRe != nil && !artRe.MatchString(e.Request.URL.Path) {
			return
		}

		// 正文提取 + simhash + 分词 + 标签（全部索引期完成，查询期只做过滤）
		res, err := c.proc.Process(pageURL, e.Response.Body, processor.Options{SiteCategory: category})
		if err != nil {
			slog.Warn("process failed", "url", pageURL, "err", err)
			stats.Errors++
			parseFails++
			if firstParseErr == "" {
				firstParseErr = err.Error()
			}
			return
		}

		// 薄页（导航/营销页）不进索引：这类页面会稀释检索质量
		if res.WordCount < c.cfg.MinWordCount {
			slog.Debug("page skipped", "url", pageURL, "reason", "thin", "words", res.WordCount)
			stats.Thin++
			metrics.CrawlPages.WithLabelValues("thin").Inc()
			return
		}
		// 非中英文页面（白名单站点上的 /ru/、/bn/ 等语言镜像）
		if c.cfg.MinLangRatio > 0 && res.LangRatio < c.cfg.MinLangRatio {
			slog.Debug("page skipped", "url", pageURL, "reason", "lang", "lang_ratio", res.LangRatio)
			stats.LangSkip++
			metrics.CrawlPages.WithLabelValues("lang").Inc()
			return
		}
		// 站务内容：URL 形态是文章页，但内容是服务协议/隐私政策/版权声明。
		// 真实案例：developer.aliyun.com/article/768092「阿里云开发者社区用户服务协议」
		// 在「服务协议」查询里排第一，把技术结果全压下去。
		if processor.IsBoilerplateTitle(res.Title) {
			slog.Debug("page skipped", "url", pageURL, "reason", "boilerplate", "title", res.Title)
			stats.Boilerplate++
			metrics.CrawlPages.WithLabelValues("boilerplate").Inc()
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
			slog.Debug("page skipped", "url", pageURL, "reason", "listpage",
				"link_density", res.LinkDensity, "words", res.WordCount)
			stats.ListPage++
			metrics.CrawlPages.WithLabelValues("listpage").Inc()
			return
		}

		// 页级标签（这一页的内容特征）+ 站点级标签（整站特征）合并后再入索引：
		// 用户排除「UGC 平台」时，要的是整站都不出现，而不是这一页恰好没命中。
		allLabels, allReasons := labels.Merge(res.Labels, res.LabelReasons, siteLabels)
		// 人工改过的标签优先：用户反馈说这一页判错了、管理员按反馈修过，
		// 再用同一条规则重算一遍把它改回去，等于那次修正从未发生。
		if manLabels, manReasons, ok := c.st.ManualDocLabels(ctx, pageURL); ok {
			allLabels, allReasons = parseManualLabels(manLabels, manReasons)
		}
		doc := index.Doc{
			URL:         pageURL,
			Domain:      pageHost,
			Title:       res.Title,
			Description: res.Description,
			Body:        res.Body,
			Labels:      allLabels,
			Category:    category,
			Lang:        res.Lang, // 语言边界硬过滤的判据
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
			Labels:       processor.LabelsJSON(allLabels),
			LabelReasons: processor.ReasonsJSON(allReasons),
			WordCount:    res.WordCount,
			Simhash:      res.Simhash,
			ShingleHash:  res.ShingleHash,
			PublishedAt:  res.PublishedAt,
			FetchedAt:    res.FetchedAt,
		}); err != nil {
			slog.Warn("persist document failed", "url", pageURL, "err", err)
		}
		// 原文快照落 KV：证据链/原文回溯的地基（单页上限 2MB，超限截断并标注）。
		// 后台可关（功能开关 → 原文快照），关掉只停止新增，已有快照照样能查——
		// 否则用户一关开关，历史结果的引用编号就全成了死链。
		if c.snap != nil && c.snapshotEnabled(ctx) {
			raw := e.Response.Body
			if len(raw) > 2*1024*1024 {
				raw = raw[:2*1024*1024]
			}
			if err := c.snap.Put(ctx, kv.Key(urlkey.Key(pageURL)), raw); err != nil {
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
		httpErrors++
		// OnError 的 Response 在传输层失败时可能是空壳，取字段前先判空
		urlStr := ""
		if r != nil {
			if r.StatusCode > 0 {
				lastStatus = r.StatusCode
			}
			if r.Request != nil {
				urlStr = r.Request.URL.String()
			}
		}
		if err != nil {
			lastErr = err.Error()
		}
		metrics.CrawlPages.WithLabelValues("error").Inc()
		slog.Debug("crawl error", "url", urlStr, "status", lastStatus, "err", err)
	})

	col.OnHTML("a[href]", func(e *colly.HTMLElement) {
		// 域内 BFS：只跟进本站链接；colly 会按 MaxDepth 截断。
		// 用 allowedHost 集合而不是「与当前页 host 严格相等」：种子是裸域时
		// 页面里的绝对链接常写成 www 变体（ruanyifeng.com/blog/ 的文章链接全是
		// https://www.ruanyifeng.com/...），严格比较会把整站文章全部丢掉——
		// 实测该站 0 篇入库，而 OnRequest 的准入本来是允许 www 变体的。
		link := e.Request.AbsoluteURL(e.Attr("href"))
		if link == "" {
			return
		}
		// 站内 http 链接升级为 https：老站点常把绝对链接写成 http://
		// （阮一峰 /blog/ 的文章链接全是 http://www.ruanyifeng.com/...，直接访问 403），
		// 而它们只服务 https。以「当前页面就是 https」为条件，
		// 这样 httptest 起的 http 站点（单元测试）不会被误升级。
		if e.Request.URL.Scheme == "https" && strings.HasPrefix(link, "http://") {
			link = "https://" + strings.TrimPrefix(link, "http://")
		}
		if !allowedHost[parseHost(link)] {
			return
		}
		// 聚合页（标签/分类/归档/翻页）不进 BFS：它们几乎必然被后面的
		// thin/listpage 过滤掉，但**已经消耗了单站页数配额**。
		// 真实案例：tech.meituan.com 单次 25 页配额里，8 页是
		// /tags.html?tag=大模型 之类的标签页（每页 13 词），
		// 真正的内容页反而抓不到，indexed 只有 11/25。
		if isAggregateURL(link) {
			return
		}
		// 去锚点后再跟进：`/p/123#commentform` 和 `/p/123` 是同一页，
		// 带锚点访问会让 colly 的已访问集合记成两个 URL，同一篇文章被抓两遍。
		col.Visit(urlkey.Canonical(link))
	})

	// robots.txt 先判种子页：被禁抓时连请求都不发。
	//
	// 为什么单独判一次而不是等 colly 的 Abort：Abort 只给一个笼统错误，
	// 归因会落到「无法访问」，而真实原因恰恰是站点改了爬虫规则——
	// 这两种情况要做的处理完全相反（一个等站方恢复，一个换入口或放弃收录）。
	if !robotCache.Allowed(seed) {
		metrics.CrawlPages.WithLabelValues("skipped").Inc()
		stats.Skipped++
		if host != "" && maxPages == 0 {
			return stats, crawlOutcome{Host: host, Seed: seed, SeedRobotsBlocked: true}
		}
		return stats, crawlOutcome{NoRecord: true}
	}

	// 抓取并回写状态（按本站增量判断，失败计数驱动退避）
	visitErr := col.Visit(seed)
	if host != "" {
		// 按需补抓单页（maxPages > 0）不改站点级异常名单：
		// 一个页面 404 不能说明整站坏了，否则用户点一次「补抓」就能让站点进名单。
		if maxPages > 0 {
			status, errMsg := "ok", ""
			if visitErr != nil {
				status, errMsg = "error", visitErr.Error()
			} else if stats.Indexed == 0 {
				status = "no-document"
			}
			if err := c.st.TouchCrawlResult(ctx, host, status, errMsg); err != nil {
				slog.Warn("touch crawl result failed", "host", host, "err", err)
			}
			return stats, crawlOutcome{NoRecord: true}
		}
		return stats, crawlOutcome{
			Host:          host,
			Seed:          seed,
			Indexed:       stats.Indexed,
			Fetched:       stats.Fetched,
			RobotsBlocked: robotsBlocked,
			HTTPErrors:    httpErrors,
			LastStatus:    lastStatus,
			LastErr:       lastErr,
			ParseFails:    parseFails,
			FirstParseErr: firstParseErr,
			VisitErr:      visitErr,
		}
	}
	return stats, crawlOutcome{Host: host, Seed: seed}
}

// crawlOutcome 一次整站抓取的归因输入。
type crawlOutcome struct {
	Host string
	Seed string
	// SeedRobotsBlocked 种子页本身被 robots.txt 禁止（不是「某些链接被拦」）。
	SeedRobotsBlocked bool
	// NoRecord 这条路径自己写过了状态（按需补抓单页），调用方不要再写。
	NoRecord      bool
	Indexed       int
	Fetched       int
	RobotsBlocked int
	HTTPErrors    int
	LastStatus    int
	LastErr       string
	ParseFails    int
	FirstParseErr string
	VisitErr      error
}

// classifyCrawl 把一次整站抓取归成「状态 + 异常类别 + 说明」。
//
// 顺序即优先级，判断依据是「哪条证据最能解释零入库」：
//  1. 种子页就抓不到 → 无法访问（连站点都进不去，后面的计数没意义）；
//  2. 有页入库 → 正常；
//  3. 有 HTTP/传输错误 → 无法访问（403/503/超时，站点或 CDN 在拦我们）；
//  4. 有链接被 robots 拦 → 抓取规则变更（站点明确表态不让抓）；
//  5. 有页面解析失败 → 页面结构变化（拿得到 HTML，抽不出正文）；
//  6. 抓到了但全被过滤 → 页面结构变化（正文选择器对不上了，或整站都是列表页）。
//
// status 沿用 whitelist.last_status 的既有取值（ok/error/no-document），
// 额外用 robots 表示种子页被 robots.txt 拦下。
func classifyCrawl(o crawlOutcome) (status, kind, detail string) {
	switch {
	case o.SeedRobotsBlocked:
		return "robots", store.CrawlIssueRobots, "robots.txt 禁止抓取种子页 " + o.Seed
	case o.VisitErr != nil:
		// HTTP 错误优先于错误文案：colly 的文案是「Not Found」这类英文短句，
		// 而「HTTP 404」对管理员更直接（403 是拦爬虫，404 是页面没了）。
		if o.LastStatus >= 400 {
			return "error", store.CrawlIssueUnreachable, fmt.Sprintf("种子页返回 HTTP %d", o.LastStatus)
		}
		return "error", store.CrawlIssueUnreachable, "种子页抓取失败：" + shortErr(o.VisitErr.Error())
	case o.Indexed > 0:
		return "ok", "", ""
	case o.HTTPErrors > 0:
		d := fmt.Sprintf("%d 个页面请求失败", o.HTTPErrors)
		if o.LastStatus > 0 {
			d += fmt.Sprintf("（最近 HTTP %d）", o.LastStatus)
		}
		if o.LastErr != "" {
			d += "：" + shortErr(o.LastErr)
		}
		return "error", store.CrawlIssueUnreachable, d
	case o.RobotsBlocked > 0:
		return "robots", store.CrawlIssueRobots,
			fmt.Sprintf("robots.txt 拦截了 %d 个链接，站点可能修改了爬虫规则", o.RobotsBlocked)
	case o.ParseFails > 0:
		return "error", store.CrawlIssueStructure,
			fmt.Sprintf("%d 个页面正文解析失败：%s", o.ParseFails, shortErr(o.FirstParseErr))
	case o.Fetched > 0:
		return "no-document", store.CrawlIssueStructure,
			fmt.Sprintf("抓到 %d 页但没有可索引正文，页面结构可能已变化（或整站被判为薄页/列表页）", o.Fetched)
	default:
		return "no-document", store.CrawlIssueUnreachable,
			"没有抓到任何页面，请检查抓取入口与文章规则"
	}
}

// saveCrawlOutcome 回写站点最后抓取状态，并维护异常名单。
// kind 为空表示这轮正常：顺带把该站从名单里删掉（恢复是自动的）。
func (c *Crawler) saveCrawlOutcome(ctx context.Context, host, status, kind, detail string) {
	if err := c.st.TouchCrawlResult(ctx, host, status, detail); err != nil {
		slog.Warn("touch crawl result failed", "host", host, "err", err)
	}
	if kind == "" {
		if err := c.st.ClearCrawlIssue(ctx, host); err != nil {
			slog.Warn("clear crawl issue failed", "host", host, "err", err)
		}
		return
	}
	if err := c.st.RecordCrawlIssue(ctx, host, kind, detail); err != nil {
		slog.Warn("record crawl issue failed", "host", host, "err", err)
	}
	// 异常要能在日志里直接看到：定时抓取跑在后台，没人会盯着后台页面等它出现
	slog.Info("crawl issue recorded", "host", host, "kind", kind, "detail", detail)
}

// shortErr 把底层错误压成一行短说明：直接落库的报错可能带多层 URL、
// 换行与完整调用链，后台表格里显示不下，也没人读得懂。
func shortErr(s string) string {
	s = strings.Join(strings.Fields(s), " ")
	const max = 160
	if r := []rune(s); len(r) > max {
		return string(r[:max]) + "…"
	}
	return s
}

// ---------- helpers ----------

// aggregateQuery 聚合页的查询参数名（标签/分类/归档/排序/筛选）。
// 这类 URL 是列表，不是文章；跟在 BFS 里只会白占页数配额。
var aggregateQuery = regexp.MustCompile(`[?&](tag|tags|categor(y|ies)|archive|month|year|sort|filter|order)=`)

// aggregatePath 聚合页的路径形态：/tags.html、/tag/xxx、/category/backend、/archive/2024。
// 判据刻意收窄到「整段就是这些词」，避免误伤 /tagging-guide 这类正常文章路径。
var aggregatePath = regexp.MustCompile(`/(tags?|categor(y|ies)|archives?)(\.html?|\.php|/|$)`)

// isAggregateURL 判断链接是否指向聚合页（标签/分类/归档/筛选）。
//
// 为什么要在 BFS 阶段就拦：这些页面几乎都会被 thin（正文 13 词）或
// listpage（链接密度高）过滤，但过滤发生在**抓取之后**，配额已经花掉了。
func isAggregateURL(raw string) bool {
	u, err := url.Parse(raw)
	if err != nil {
		return false
	}
	// u.RawQuery 不含前导 '?'，补齐后再匹配，否则 `?tag=` 这类永远匹配不到
	if u.RawQuery != "" && aggregateQuery.MatchString("?"+u.RawQuery) {
		return true
	}
	return aggregatePath.MatchString(strings.ToLower(u.Path))
}

// boilerplateSuffixes 站务页标题的结尾词（截掉站点名后缀后匹配）。
var boilerplateSuffixes = []string{
	"服务协议", "用户协议", "使用协议", "隐私政策", "隐私协议",
	"免责声明", "版权声明", "法律声明", "知识产权说明",
}

// articleLeadWords 教学/探讨型标题的开头词。站务页不会这样起名，
// 但「如何为开源项目写一份用户协议」会以「用户协议」结尾——
// 单测实测的误判案例，靠开头词排除。
var articleLeadWords = []string{
	"如何", "怎么", "怎样", "为何", "为什么", "浅谈", "详解", "深入",
	"聊聊", "谈谈", "说说", "图解", "一文", "从零", "手把手",
}

// isBoilerplateTitle 判断标题是否为站务页（服务协议/隐私政策/版权声明）。
//
// 三条同时满足才判定：
//  1. 截掉「- 站点名」后缀后，以站务词结尾（只看结尾不看包含，
//     「对话亚马逊CTO：仍在招聘初级工程师」这类正常文章含「招聘」但结尾不是站务词）；
//  2. 长度 ≤ 30 字：技术文章标题通常更长（《微服务架构下的服务协议设计实践》）；
//  3. 不以教学/探讨型词开头：「如何为开源项目写一份用户协议」是文章不是站务页。
func isBoilerplateTitle(title string) bool {
	t := strings.TrimSpace(title)
	if i := strings.IndexAny(t, "-_|"); i > 0 {
		t = strings.TrimSpace(t[:i])
	}
	if len([]rune(t)) > 30 {
		return false
	}
	for _, lead := range articleLeadWords {
		if strings.HasPrefix(t, lead) {
			return false
		}
	}
	for _, suf := range boilerplateSuffixes {
		if strings.HasSuffix(t, suf) {
			return true
		}
	}
	return false
}

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

// quotaFor 该站本轮允许抓取的页数。
//
// 动态配额（抄 sese-engine 反垄断兴趣衰减）：
//
//	配额 = QuotaBase + 繁荣度 * QuotaSpan，上限 QuotaMax
//
// 繁荣站多抓（内容多、值得索引），长尾站保底 QuotaBase 不被饿死。
// 关掉 DynamicQuota 时退化为固定的 MaxPagesPerHost。
func (c *Crawler) quotaFor(host string) int {
	if !c.cfg.DynamicQuota {
		return c.cfg.MaxPagesPerHost
	}
	base, span, max := c.cfg.QuotaBase, c.cfg.QuotaSpan, c.cfg.QuotaMax
	if base <= 0 {
		base = 10
	}
	q := base
	if p := c.prosperity(host); p > 0 {
		q += int(p * float64(span))
	}
	if max > 0 && q > max {
		q = max
	}
	if q < 1 {
		q = 1
	}
	return q
}

// loadProsperity 读全表繁荣度并归一化到 0~1。
//
// 归一化用最大值而不是固定值：站点数量变化时不必改代码，
// 最繁荣的站永远拿满 QuotaSpan。
func (c *Crawler) loadProsperity(ctx context.Context) map[string]float64 {
	out := map[string]float64{}
	if !c.cfg.DynamicQuota {
		return out
	}
	domains, err := c.st.WhitelistDomains(ctx)
	if err != nil {
		slog.Warn("prosperity preload failed, fallback to fixed quota", "err", err)
		return out
	}
	raw, err := c.st.ProsperityScores(ctx, domains)
	if err != nil {
		slog.Warn("prosperity preload failed, fallback to fixed quota", "err", err)
		return out
	}
	var maxScore float64
	for _, v := range raw {
		if v > maxScore {
			maxScore = v
		}
	}
	if maxScore <= 0 {
		return out
	}
	for k, v := range raw {
		if v > 0 {
			out[k] = v / maxScore
		}
	}
	return out
}

// interleaveByProsperity 把长尾站按比例均匀撒进繁荣站之间。
//
// 为什么不是简单 shuffle：随机打散下，并发窗口（默认 4）很可能连续几个位置
// 都是繁荣站；而定时抓取是有时限的，排在后面不等于不会被抓，但抓完时
// 繁荣站已经吃掉了大部分时间预算。
//
// 为什么不是「先放完长尾再放繁荣」：第一版就是这么写的，结果长尾站
// （当前 4 个）全部堆在序列前部，20 个滑动窗口里 13 个只剩繁荣站——
// 等于没交错。改成按比例均匀铺开：长尾占比 4/23，就每 5~6 个繁荣站插一个。
//
// 对应 sese-engine 的「预期繁荣网站比例」——它在 BFS 队列裁剪时按比例
// 强制保留不繁荣网站；stdseek 白名单固定，所以体现在调度顺序上。
//
// 注意：长尾站数量少于 total/4 时，不可能做到「任意 4 连续都含长尾」
// （4 个站铺 23 个位置，平均间隔 5.75）。均匀铺开是能达到的最优解，
// 由 TestInterleaveSpreadsLongTail 的间隔上界兜住。
func interleaveByProsperity(seeds []string, pros map[string]float64) []string {
	if len(seeds) < 3 || len(pros) == 0 {
		return seeds
	}
	host := func(seed string) string { return parseHost(seed) }

	var rich, lean []string
	for _, s := range seeds {
		if pros[host(s)] > 0 {
			rich = append(rich, s)
		} else {
			lean = append(lean, s)
		}
	}
	if len(lean) == 0 || len(rich) == 0 {
		return seeds // 只有一类，交错无意义
	}

	// Bresenham 式比例分配：第 i 步该不该放长尾，看「已放长尾数 + 1」
	// 是否已经落后于目标比例 * (i+1)。这样任意前缀里长尾的占比都接近目标，
	// 不会出现「先放完一类再放另一类」。
	//
	// 前两版都栽在这里：第一版先放长尾再放繁荣（长尾全堆前部），
	// 第二版按繁荣站计数插入（长尾多于繁荣站时长尾全堆尾部）。
	total := len(rich) + len(lean)
	leanRatio := float64(len(lean)) / float64(total)
	out := make([]string, 0, total)
	leanOut := 0
	for i := 0; i < total; i++ {
		switch {
		case len(lean) > 0 && float64(leanOut+1) <= leanRatio*float64(i+1):
			out = append(out, lean[0])
			lean = lean[1:]
			leanOut++
		case len(rich) > 0:
			out = append(out, rich[0])
			rich = rich[1:]
		case len(lean) > 0:
			out = append(out, lean[0])
			lean = lean[1:]
			leanOut++
		}
	}
	ensureLeanEarly(out, leanSet(seeds, pros))
	return out
}

// leanSet 哪些站点属于长尾（无繁荣度记录）。
func leanSet(seeds []string, pros map[string]float64) map[string]bool {
	out := map[string]bool{}
	for _, s := range seeds {
		if pros[parseHost(s)] == 0 {
			out[s] = true
		}
	}
	return out
}

// ensureLeanEarly 保证第一个长尾站落在第 2 位。
//
// 按比例分配是数学正确的，但长尾占比低时（当前 4/23 = 17%）首个长尾会落在
// 第 6 位左右——意味着第一轮并发窗口（前 4 个）可能全是繁荣站，
// 长尾要多等一整轮。定时抓取每小时一轮，这个代价不值得省：
// 把第一个长尾换到第 2 位，比例只被扰动一次，覆盖却有保证。
func ensureLeanEarly(out []string, lean map[string]bool) {
	if len(out) < 3 {
		return
	}
	first := -1
	for i, s := range out {
		if lean[s] {
			first = i
			break
		}
	}
	if first > 1 {
		out[1], out[first] = out[first], out[1]
	}
}
