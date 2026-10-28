// Package api 提供 chi HTTP 路由：检索、用户提交网站、管理后台（白名单/黑名单/审核队列）。
// 路径设计与规划书一致：/admin/sites /admin/review /admin/whitelist /admin/blacklist。
package api

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"

	"voidxo/internal/auth"
	"voidxo/internal/index"
	"voidxo/internal/kv"
	"voidxo/internal/mail"
	"voidxo/internal/metrics"
	"voidxo/internal/precheck"
	"voidxo/internal/processor"
	"voidxo/internal/querysyntax"
	"voidxo/internal/rank"
	"voidxo/internal/sitecat"
	"voidxo/internal/store"
	"voidxo/internal/suggest"
	"voidxo/internal/summarize"
	"voidxo/internal/urlkey"
	"voidxo/internal/usage"
	"voidxo/internal/webui"
)

// Server 聚合依赖。
type Server struct {
	st   *store.Store
	idx  *index.Index
	rk   *rank.Ranker
	snap *kv.Store
	// aiMu 串行化「AI 接口切换状态」的读改写：并发判定 4 个站，
	// 不加锁会同时判定为「刚切换」而连发几封通知邮件。
	aiMu sync.Mutex
	// Proc 正文处理器：AI 判定要抽 3 段正文摘录时用（从快照现取）。
	// nil 表示未接（测试或纯检索部署），此时判定只用标题与站点自述。
	Proc *processor.Processor
	// SubmitEnabled 控制 /api/submit 是否开放。
	// 红线：首批 20-50 站质量验证前默认关闭，避免审核队列成为烂尾功能。
	SubmitEnabled bool
	// AdminToken 为空时不校验后台令牌（仅测试/本地开发）；main 会强制生成。
	AdminToken string
	// limiter 限制 /api/* 的请求速率（默认 120 次/分钟/IP）
	limiter *rateLimiter
	// checkFn 预检实现，nil 时使用 precheck.Check（真实网络）；测试可注入。
	checkFn func(ctx context.Context, rawURL string) precheck.Result

	// 摘要链路（见 summary.go）。sumPrv 为 nil 时用抽取式；
	// sumCache 挡住"同一个页面被反复点开"造成的重复模型调用。
	sumMu    sync.Mutex
	sumPrv   summarize.Provider
	sumCache map[string]summaryEntry

	// scache 检索结果缓存（见 searchcache.go）。nil 表示禁用（测试默认不启用，
	// 避免断言依赖缓存行为）。
	scache *searchCache
	// snippet 证据片段缓存（见 snippetcache.go）。同样可禁。
	snippet *snippetCache

	// ReindexDomains 按站点重建索引（AI 改分类后让分类立刻可筛）。
	// nil 表示该能力未启用：分类仍会写回 SQLite，只是索引要等下次抓取。
	ReindexDomains func(ctx context.Context, domains []string) error

	// FetchPage 单页补抓：结果卡上「这页没抓过」时按需抓一页。
	// nil 表示该能力未启用（测试或不跑爬虫的部署），前端不显示补抓入口。
	// 只回传入索引的条数，不把爬虫的 Stats 类型漏进 api 包。
	FetchPage func(ctx context.Context, rawURL string) (indexed int, err error)

	// RecrawlSites 后台异常名单的「重新抓取」：按域名重跑一轮整站抓取。
	// 必须立即返回（内部异步执行），否则单站几十秒到几分钟的抓取会拖死请求。
	// 已有一轮在跑时返回 ErrCrawlBusy；nil 表示未接爬虫。
	RecrawlSites func(domains []string) error
	// fetchMu/fetchAt 同一 URL 的补抓冷却：按钮可能被连点，
	// 而每次补抓都是真实外网请求，按 URL 冷却比按 IP 限流更贴合。
	fetchMu sync.Mutex
	fetchAt map[string]time.Time

	// Usage 每日用量累加器。nil 表示不统计（测试或没接数据库）。
	Usage *usage.Tracker
	// Auth 账号服务。nil 表示未启用（测试）。
	Auth *auth.Service
	// Passkey 通行密钥（WebAuthn）。nil 表示未启用。
	// Geo 归属地解析器（用户配置的接口）。nil 表示未配置。
	Geo     *auth.GeoResolver
	Passkey *auth.PasskeyService
	// Captcha 人机验证（算术题 + HMAC 签名）。是否强制由功能开关 require_captcha 决定。
	Captcha *auth.Captcha
	// Turnstile Cloudflare 人机验证。nil 表示没配，此时回退到内置算术题。
	Turnstile *auth.Turnstile
	// TurnstileSiteKey 公开的 site key（下发给前端渲染组件用）。
	TurnstileSiteKey string
	// Mail 发信器。未配 SMTP 时只把正文写进日志（本地开发能拿到链接）。
	Mail *mail.Mailer
	// PublicURL 邮件里绝对链接用的公开地址（如 https://voidxo.com）；
	// 留空则从请求推断。
	PublicURL string

	// Suggester 搜索词补全（Bing）。nil 表示不启用，接口返回空列表。
	Suggester *suggest.Bing

	// GeoHeader 携带请求地区码的请求头名（如 CF-IPCountry）。
	// 默认空 = 不做地区统计：任意客户端都能伪造这个头，只有部署在可信 CDN 后面
	// 才该开启，否则排行榜会被刷成任何国家。
	GeoHeader string

	// AllowedOrigins 允许跨域调用 /api 的来源（启动参数 -cors）。
	// 为空表示不开跨域：默认只允许同源页面调用，避免别人的站点白嫖检索额度。
	// 填 "*" 表示放开任意来源（自建公开 API 时用，注意配合限流）。
	AllowedOrigins []string
}

// precheckUA 预检请求的 User-Agent。
const precheckUA = "voidxo-precheck/0.1 (+https://voidxo.example/bot)"

// runPrecheck 执行预检并返回结果与其 JSON 表示（序列化失败时 jsonStr 为空）。
func (s *Server) runPrecheck(ctx context.Context, rawURL string, blacklisted bool) (precheck.Result, string) {
	var res precheck.Result
	if s.checkFn != nil {
		res = s.checkFn(ctx, rawURL)
	} else {
		timeout := 5 * time.Second
		if dl, ok := ctx.Deadline(); ok {
			if d := time.Until(dl); d > 0 && d < timeout {
				timeout = d
			}
		}
		res = precheck.Check(ctx, rawURL, precheckUA, timeout)
	}
	res.URL = rawURL
	res.BlacklistHistory = blacklisted
	res.CheckedAt = time.Now().Unix()
	b, err := json.Marshal(res)
	if err != nil {
		slog.Warn("marshal precheck failed", "url", rawURL, "err", err)
		return res, ""
	}
	return res, string(b)
}

// securityHeaders 基础安全响应头。
//
// 重点是防点击劫持：本项目的后台全是「一次点击就生效」的开关和删除按钮，
// 一旦页面能被第三方用透明 iframe 套住，管理员的一次误点就等于替攻击者执行操作。
// 这类攻击不需要拿到会话，只需要骗一次点击，所以必须由服务端明确拒绝被嵌。
func securityHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h := w.Header()
		h.Set("X-Content-Type-Options", "nosniff")
		h.Set("Referrer-Policy", "strict-origin-when-cross-origin")
		// 例外：原文快照要被结果页自己的 sandbox iframe 引用（证据链展示），
		// 只放开同源；其余一切页面禁止被任何来源嵌入。
		if strings.HasPrefix(r.URL.Path, "/api/page") {
			h.Set("X-Frame-Options", "SAMEORIGIN")
			h.Set("Content-Security-Policy", "frame-ancestors 'self'")
		} else {
			h.Set("X-Frame-Options", "DENY")
			h.Set("Content-Security-Policy", "frame-ancestors 'none'")
		}
		next.ServeHTTP(w, r)
	})
}

// NewServer 构造 API 服务。
func NewServer(st *store.Store, idx *index.Index, snap *kv.Store, submitEnabled bool, adminToken string) *Server {
	return &Server{st: st, idx: idx, rk: rank.New(rank.DefaultWeights()), snap: snap,
		SubmitEnabled: submitEnabled, AdminToken: adminToken, limiter: newRateLimiter(120),
		scache: newSearchCache(), snippet: newSnippetCache(), fetchAt: make(map[string]time.Time)}
}

// DisableSearchCache 关闭检索缓存（测试与调参用：断言不应依赖缓存行为）。
func (s *Server) DisableSearchCache() { s.scache = nil }

// NewRouter 装配路由与中间件。
func NewRouter(s *Server) http.Handler {
	r := chi.NewRouter()
	r.Use(securityHeaders)
	r.Use(middleware.RequestID)
	r.Use(middleware.RealIP)
	r.Use(middleware.Recoverer)
	r.Use(middleware.Timeout(30 * time.Second))
	r.Use(metrics.Middleware)

	r.Get("/healthz", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
	})
	// Prometheus 指标（爬虫吞吐 / 检索延迟 / QPS）
	r.Method(http.MethodGet, "/metrics", metrics.Handler())

	r.Route("/api", func(r chi.Router) {
		// CORS 必须在限流之前：预检请求（OPTIONS）不该消耗调用方的速率额度。
		if len(s.AllowedOrigins) > 0 {
			r.Use(corsMiddleware(s.AllowedOrigins))
		}
		if s.limiter != nil {
			r.Use(s.limiter.middleware)
		}
		// IP 封禁：命中即 403。注册 / 登录 / 提交 / 反馈这些写入口都在 /api 下。
		r.Use(s.ipBanMiddleware)
		r.Get("/search", s.handleSearch)
		r.Get("/wiki", s.handleWiki)
		r.Get("/privacy", s.handlePrivacyMeta)
		r.Get("/site", s.handlePublicSite)
		r.Get("/related", s.handleRelated)
		r.Post("/search", s.handleSearchPost)
		r.Get("/suggest", s.handleSuggest)
		// 账号：注册 / 登录 / 找回密码 / 两步验证。这些接口都在限流之下。
		r.Route("/auth", func(r chi.Router) {
			r.Post("/register", s.handleRegister)
			r.Post("/login", s.handleLogin)
			r.Post("/logout", s.handleLogout)
			r.Get("/me", s.handleMe)
			r.Get("/sessions", s.handleMySessions)
			r.Post("/forgot", s.handleForgot)
			r.Post("/reset", s.handleReset)
			r.Get("/verify", s.handleVerifyEmail)
			r.Get("/captcha", s.handleCaptcha)
			// 通行密钥：注册需要登录，登录不需要
			r.Post("/passkey/register/begin", s.handlePasskeyRegisterBegin)
			r.Post("/passkey/register/finish", s.handlePasskeyRegisterFinish)
			r.Post("/passkey/login/begin", s.handlePasskeyLoginBegin)
			r.Post("/passkey/login/finish", s.handlePasskeyLoginFinish)
			r.Get("/passkeys", s.handleListPasskeys)
			r.Post("/passkey/delete", s.handleDeletePasskey)
			r.Post("/password", s.handleChangePassword)
			r.Post("/email", s.handleChangeEmail)
			r.Post("/totp/setup", s.handleTOTPSetup)
			r.Post("/totp/enable", s.handleTOTPEnable)
			r.Post("/totp/disable", s.handleTOTPDisable)
		})
		r.Post("/submit", s.handleSubmit)
		r.Get("/submission", s.handleSubmission)
		r.Post("/label-feedback", s.handleLabelFeedback)
		r.Get("/page", s.handlePageSnapshot)
		// 按需补抓单页：结果卡上「这页没抓过」的入口（只对白名单站点开放）。
		r.Post("/fetch-page", s.handleFetchPage)
		// 公开的功能开关：前端据此决定是否渲染原文摘要等入口。
		// 放在限流组内即可——它每次查一次 SQLite，不该被爬虫当免费接口刷。
		r.Get("/features", s.handlePublicFeatures)
		// 运行状态（文档数/站点数）：首页用它判断"索引是不是空的"
		r.Get("/status", s.handleStatus)
		// 每日用量（公开）：只含检索维度，抓取/入库/摘要留在后台接口。
		r.Get("/usage", s.handlePublicUsage)
		// 证据链摘要：默认折叠、按需调用（见 summary.go）。
		// 放限流组内：单次请求可能触发一次模型调用，是这里最贵的接口。
		r.Post("/summary", s.handleSummary)
	})

	// 后台接口统一挂令牌鉴权（见 admin.go）
	r.Mount("/admin", s.adminRouter())

	// 前端（embed.FS）兜底路由，必须最后注册
	r.Handle("/*", webui.Handler())
	return r
}

// ---------- 检索 ----------

// handleSearch GET /api/search?q=...&exclude=ai,platform&since=7d&page=1&size=10&fields=url,title
//
// 参数：
//   - q        查询词（必填，≤512 字符）
//   - exclude  排除型标签（ai/ads/personal/platform，或完整 label 名）；旧名 no 仍兼容
//   - since    时间窗：秒数（86400）或简写（30m/12h/7d/4w/1y），0 为全部
//   - page/size 分页；size 上限 50
//   - fields   只返回这些字段（逗号分隔，见 apiFieldNames），缺省返回全部
//   - pretty   非 0 时缩进输出，方便人眼与 curl 查看
func (s *Server) handleSearch(w http.ResponseWriter, r *http.Request) {
	s.searchWith(w, r, r.URL.Query())
}

// handleSearchPost POST /api/search：参数走 JSON 体，语义与 GET 完全一致。
//
// 给「不想让搜索词出现在地址栏」的场景用（浏览器历史、截图、投屏）。
// 代价是刷新与前进后退拿不到查询——所以网页端默认仍走 GET。
func (s *Server) handleSearchPost(w http.ResponseWriter, r *http.Request) {
	maxBody(w, r)
	var b struct {
		Q       string `json:"q"`
		Exclude string `json:"exclude"`
		Cat     string `json:"cat"`
		Since   string `json:"since"`
		Page    int    `json:"page"`
		Size    int    `json:"size"`
		Fields  string `json:"fields"`
		Pretty  bool   `json:"pretty"`
	}
	if err := json.NewDecoder(r.Body).Decode(&b); err != nil {
		writeErr(w, http.StatusBadRequest, "请求体不是合法 JSON")
		return
	}
	// 转成同一套 url.Values 再走下面的逻辑：两套解析必然会漂。
	query := url.Values{}
	query.Set("q", b.Q)
	if b.Exclude != "" {
		query.Set("exclude", b.Exclude)
	}
	if b.Cat != "" {
		query.Set("cat", b.Cat)
	}
	if b.Since != "" {
		query.Set("since", b.Since)
	}
	if b.Page > 0 {
		query.Set("page", strconv.Itoa(b.Page))
	}
	if b.Size > 0 {
		query.Set("size", strconv.Itoa(b.Size))
	}
	if b.Fields != "" {
		query.Set("fields", b.Fields)
	}
	if b.Pretty {
		query.Set("pretty", "1")
	}
	s.searchWith(w, r, query)
}

// searchWith 检索主流程，参数已经从 query string 或 JSON 体归一成 url.Values。
func (s *Server) searchWith(w http.ResponseWriter, r *http.Request, query url.Values) {
	q := query.Get("q")
	if strings.TrimSpace(q) == "" {
		writeErr(w, http.StatusBadRequest, "缺少查询参数 q")
		return
	}
	if len([]rune(q)) > 512 {
		writeErr(w, http.StatusBadRequest, "查询过长（上限 512 字符）")
		return
	}
	// 解析语法糖：+必须包含 / -排除 / "短语" / site:域名。
	// 解析结果分两路：body 决定 BM25 怎么打分，Required/Excluded/Phrases/Site
	// 是候选过滤的硬约束。
	parsed := querysyntax.Parse(q)
	queryText := parsed.Text
	// exclude 是主名字，no 是历史名：前端已经在用 no，两个都收着。
	rawExclude := query.Get("exclude")
	if rawExclude == "" {
		rawExclude = query.Get("no")
	}
	exclude := expandLabels(splitCSV(rawExclude))
	// 站点分类：只收白名单里登记过的 key，拼错的分类不会静默变成「0 结果」——
	// 用户以为是没内容，其实是他手打的参数错了。
	catFilter := validCats(splitCSV(query.Get("cat")))
	since := parseSince(query.Get("since"))
	page, _ := strconv.Atoi(query.Get("page"))
	size, _ := strconv.Atoi(query.Get("size"))
	fields := splitCSV(query.Get("fields"))
	pretty := query.Get("pretty") != "" && query.Get("pretty") != "0"

	if page <= 0 {
		page = 1
	}
	if size <= 0 || size > 50 {
		size = 10
	}
	// 先放大候选池再重排序（BM25 召回 → 可解释排序），排名质量依赖候选宽度
	pool := size * 5
	if pool < 20 {
		pool = 20
	}
	if pool > 100 {
		pool = 100
	}
	// 分段计时：HTTP 链路的耗时分布只有 debug 级别可见。
	// 曾据压测以为瓶颈在检索，加了缓存才发现检索只占 0.2ms、大头在后段——
	// 没有这组数字就只能靠猜。
	started := time.Now()
	retrievalStart := time.Now()
	hits, total, err := s.cachedSearch(index.Query{
		Q: queryText, Exclude: exclude, Cats: catFilter, SinceSec: since, Page: 1, Size: pool,
		Required: parsed.Required, Excluded: parsed.Excluded,
		Phrases: parsed.Phrases, Site: parsed.Site,
	})
	retrieval := time.Since(retrievalStart)
	metrics.ObserveSearch(time.Since(started))
	if err != nil {
		slog.Error("search failed", "err", err)
		writeErr(w, http.StatusInternalServerError, "检索失败")
		return
	}

	signalsStart := time.Now()
	domains := make([]string, 0, len(hits))
	keys := make([]string, 0, len(hits)*2)
	for _, h := range hits {
		domains = append(domains, h.Domain)
		keys = append(keys, h.Domain, urlkey.Key(h.URL))
	}
	// 四个信号查询分别计时：单连接（SetMaxOpenConns(1)）下并行无效，
	// 只能减少次数，所以要先知道该动哪一个。
	tCatStart := time.Now()
	cats, err := s.st.DomainCategories(r.Context(), domains)
	siteNames, nameErr := s.st.SiteNames(r.Context(), domains)
	tCatDur := time.Since(tCatStart)
	if err != nil {
		slog.Warn("domain categories failed", "err", err)
	}
	if nameErr != nil {
		slog.Warn("site names failed", "err", nameErr)
	}
	tProsStart := time.Now()
	prosperity, err := s.st.ProsperityScores(r.Context(), keys)
	tProsDur := time.Since(tProsStart)
	if err != nil {
		slog.Warn("prosperity lookup failed", "err", err)
	}
	// 折叠副本数：候选里有 N 个相同内容副本时，结果页要能解释「已聚合」
	tDupStart := time.Now()
	dups, err := s.st.DupCountsByURLs(r.Context(), hitURLs(hits))
	tDupDur := time.Since(tDupStart)
	if err != nil {
		slog.Warn("dup counts lookup failed", "err", err)
	}
	// 语料内反向链接数：深网发现的冷热信号
	tInbStart := time.Now()
	inbound, err := s.st.InboundCounts(r.Context(), hitURLs(hits))
	tInbDur := time.Since(tInbStart)
	if err != nil {
		slog.Warn("inbound counts lookup failed", "err", err)
	}
	// 首页标记：候选里路径为 / 的文档，供排序层对主题查询降权
	hubs := make(map[string]bool, 2)
	for _, h := range hits {
		if rank.IsHubURL(h.URL) {
			hubs[h.URL] = true
		}
	}
	signals := time.Since(signalsStart)
	rankStart := time.Now()
	// CorpusDocs 供排序层按语料规模缩放噪音闸门（小语料部署的必需品，见 minBM25For）
	corpusDocs := s.idx.DocCount()
	ranked := s.rk.Rank(queryText, hits, rank.Signals{Literal: queryText == "",
		Categories: cats, Prosperity: prosperity,
		HubPages: hubs, DupCount: dups, Inbound: inbound, CorpusDocs: corpusDocs}, time.Now())
	rankDur := time.Since(rankStart)
	// 排序层判定整批候选只是词面噪音时会返回空，此时 total 也要归零，
	// 否则结果页会显示「共 13 条」却一条都列不出来。
	if len(ranked) == 0 {
		total = 0
	}

	start := (page - 1) * size
	if start > len(ranked) {
		start = len(ranked)
	}
	end := start + size
	if end > len(ranked) {
		end = len(ranked)
	}
	items := make([]searchItem, 0, end-start)
	shown := make(map[string]bool, len(items))
	for _, res := range ranked[start:end] {
		items = append(items, searchItem{Hit: res.Hit, Score: res.Score,
			Reasons: res.Reasons, Breakdown: res.Breakdown, DupCount: dups[res.Hit.URL],
			SiteName: siteNames[res.Hit.Domain]})
		shown[res.Hit.URL] = true
	}
	// 证据片段：从 KV 快照取正文，定位查询词的上下文（证据链摘要的前置能力）。
	// 只对当前页做；快照缺失（老文档/未抓正文）时静默跳过，不影响检索本身。
	snippetsStart := time.Now()
	var kvDur, buildDur time.Duration
	if s.snap != nil {
		gen := s.snap.Version()
		for i := range items {
			key := urlkey.Key(items[i].URL)
			if key == "" {
				continue
			}
			// 先查缓存再读 KV：命中时片段是现成的，没必要为了算一个 key
			// 去读 80KB 快照（KV 读实测 2.6~3.7ms/10 篇）。
			// 缓存值为 nil 表示「这篇没有快照」，同样跳过读取。
			if snips, ok := s.snippet.lookup(gen, s.snippet.key(key, queryText)); ok {
				items[i].Snippets = snips
				continue
			}
			kvStart := time.Now()
			body, found, err := s.snap.Get(kv.Key(key))
			kvDur += time.Since(kvStart)
			if err != nil || !found {
				// 记住"没有快照"：老文档不该每次搜索都付一次 KV 读的代价
				s.snippet.rememberNoSnapshot(gen, s.snippet.key(key, queryText))
				continue
			}
			buildStart := time.Now()
			items[i].Snippets = s.cachedSnippets(s.snap, key, queryText, string(body))
			buildDur += time.Since(buildStart)
		}
	}
	snippets := time.Since(snippetsStart)
	s.markSnapshots(r.Context(), items)
	slog.Debug("search stages", "q", q, "candidates", len(hits), "shown", len(items),
		"retrieval_us", retrieval.Microseconds(),
		"signals_us", signals.Microseconds(),
		"rank_us", rankDur.Microseconds(),
		"snippets_us", snippets.Microseconds(),
		"cats_us", tCatDur.Microseconds(),
		"pros_us", tProsDur.Microseconds(),
		"dup_us", tDupDur.Microseconds(),
		"inb_us", tInbDur.Microseconds(),
		"kv_us", kvDur.Microseconds(),
		"build_us", buildDur.Microseconds(),
		"total_us", time.Since(started).Microseconds())

	// 深网发现：与查询相关但反向链接少的内容（独立博客/冷门讨论）。
	// 只在前 20 条候选里挑，且排除主结果已展示的 URL——不与主结果重复，
	// 也不为了凑数把弱相关内容塞进来（闸门在排序层）。
	var discover []searchItem
	if len(ranked) > 0 {
		for _, res := range s.rk.Discover(queryText, hits, rank.Signals{Literal: queryText == "",
			Categories: cats,
			Prosperity: prosperity, HubPages: hubs, DupCount: dups, Inbound: inbound,
			CorpusDocs: corpusDocs},
			time.Now(), shown, discoverLimit) {
			discover = append(discover, searchItem{Hit: res.Hit, Score: res.Score,
				Reasons: res.Reasons, Breakdown: res.Breakdown, DupCount: dups[res.Hit.URL],
				SiteName: siteNames[res.Hit.Domain]})
		}
		s.markSnapshots(r.Context(), discover)
	}
	// 标签理由是「可解释」的一半：只说被打了 ai_suspect 而不说为什么，用户无从判断真假，
	// 误判反馈也就变成了盲猜。只查当前页（≤50 条），一次 IN 查询。
	urls := make([]string, 0, len(items))
	for _, it := range items {
		if len(it.Labels) > 0 {
			urls = append(urls, it.URL)
		}
	}
	if reasons, err := s.st.LabelReasonsByURLs(r.Context(), urls); err != nil {
		slog.Warn("label reasons lookup failed", "err", err)
	} else {
		for i := range items {
			if m, ok := reasons[items[i].URL]; ok && len(m) > 0 {
				items[i].LabelReasons = m
			}
		}
	}

	resp := map[string]interface{}{
		"q":     q,
		"total": total,
		"page":  page,
		// fields 缺省时原样返回结构体（顺序稳定、体积最小）；指定了才按白名单
		// 裁剪成 map，没点名的字段一律不出现。
		"hits":     projectHits(items, fields),
		"discover": projectHits(discover, fields),
		// 时效意图：结果页据此提示「已按发布时间加权」，避免用户以为排序失灵
		"freshness_intent": rank.HasFreshnessIntent(queryText),
	}
	if len(fields) > 0 {
		// 回显实际生效的字段，调用方拼错时一眼能看出来
		resp["fields"] = fields
	}
	// 回显被识别的语法：前端据此展示「仅 php.net」「排除 大学」这类 chip，
	// API 调用方也能确认自己的写法真的被解析了而不是当成普通词。
	if parsed.HasSyntax() {
		resp["syntax"] = map[string]interface{}{
			"required": parsed.Required,
			"excluded": parsed.Excluded,
			"phrases":  parsed.Phrases,
			"site":     parsed.Site,
		}
	}
	// 用量统计放在响应组装之后：它只影响「看趋势」，不该拖慢检索。
	// 内存累加，不碰 SQLite（见 internal/usage 的说明）。
	//
	// 网页端会带 X-Voidxo-Client: web，其余调用方一律算 API 请求——
	// 靠自定义头判断比猜 Referer/UA 可靠，也不用往 URL 里塞无意义的参数。
	isAPI := r.Header.Get("X-Voidxo-Client") != "web"
	totalShown := len(items) + len(discover)
	s.Usage.AddSearch(totalShown, totalShown > 0, isAPI)
	if isAPI {
		// 只有外部调用才记来源：网页端是同源请求，浏览器不带 Origin，记了全是「未知」
		s.Usage.AddOrigin(originHost(r))
	}
	if s.GeoHeader != "" {
		s.Usage.AddCountry(strings.TrimSpace(r.Header.Get(s.GeoHeader)))
	}
	writeJSONOpt(w, http.StatusOK, resp, pretty)
}

// apiFieldNames 可在 fields 参数里点名的字段（与 JSON tag 一致）。
// 新增响应字段时记得同步这里，否则 API 调用方永远裁不到它。
var apiFieldNames = []string{
	"url", "domain", "title", "description", "excerpt",
	"site_name", "labels", "label_reasons", "category",
	"score", "reasons", "breakdown",
	"time_by_fetch", "sort_time", "prosperity",
	"snippets", "has_snapshot", "fetchable", "dup_count",
}

// projectHits 按 fields 裁剪结果。fields 为空时原样返回结构体——
// 不做"默认精简"，因为改默认字段是对既有调用方的破坏性变更。
func projectHits(items []searchItem, fields []string) interface{} {
	if len(fields) == 0 {
		return items
	}
	want := make(map[string]bool, len(fields))
	for _, f := range fields {
		want[strings.ToLower(strings.TrimSpace(f))] = true
	}
	out := make([]map[string]interface{}, 0, len(items))
	for _, it := range items {
		m := make(map[string]interface{}, len(fields))
		for name := range want {
			switch name {
			case "url":
				m["url"] = it.URL
			case "domain":
				m["domain"] = it.Domain
			case "title":
				m["title"] = it.Title
			case "description":
				m["description"] = it.Description
			case "excerpt":
				m["excerpt"] = it.Excerpt
			case "site_name":
				m["site_name"] = it.SiteName
			case "labels":
				m["labels"] = it.Labels
			case "label_reasons":
				m["label_reasons"] = it.LabelReasons
			case "score":
				m["score"] = it.Score
			case "reasons":
				m["reasons"] = it.Reasons
			case "breakdown":
				m["breakdown"] = it.Breakdown
			case "time_by_fetch":
				m["time_by_fetch"] = it.TimeByFetch
			case "sort_time":
				m["sort_time"] = it.SortTime
			case "prosperity":
				m["prosperity"] = it.Prosperity
			case "snippets":
				m["snippets"] = it.Snippets
			case "has_snapshot":
				m["has_snapshot"] = it.HasSnapshot
			case "fetchable":
				m["fetchable"] = it.Fetchable
			case "dup_count":
				m["dup_count"] = it.DupCount
			}
		}
		out = append(out, m)
	}
	return out
}

// parseSince 解析时间窗：支持秒数（86400）与简写（30m/12h/7d/4w/1y）。
// 简写是为了让调用方不用自己乘 86400——文档里也不用教人算。
// 无法解析时返回 0（= 不限时间），不报错：时间过滤放宽比让整个请求失败好。
func parseSince(raw string) int64 {
	s := strings.ToLower(strings.TrimSpace(raw))
	if s == "" {
		return 0
	}
	if n, err := strconv.ParseInt(s, 10, 64); err == nil {
		if n <= 0 {
			return 0
		}
		return n
	}
	if len(s) < 2 {
		return 0
	}
	n, err := strconv.ParseInt(s[:len(s)-1], 10, 64)
	if err != nil || n <= 0 {
		return 0
	}
	switch s[len(s)-1] {
	case 'm':
		return n * 60
	case 'h':
		return n * 3600
	case 'd':
		return n * 86400
	case 'w':
		return n * 7 * 86400
	case 'y':
		return n * 365 * 86400
	}
	return 0
}

// originHost 从 Origin / Referer 里取调用方域名。
//
// 这两个头是调用方自报的、可以伪造——但它们只用于「谁在调」的展示，
// 不参与任何鉴权判断，伪造顶多把自己那一行统计弄脏。
func originHost(r *http.Request) string {
	raw := r.Header.Get("Origin")
	if raw == "" {
		raw = r.Header.Get("Referer")
	}
	if raw == "" {
		return ""
	}
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" {
		return ""
	}
	return u.Hostname()
}

// expandLabels 把结果页开关简写映射为索引期写入的 labels 值。
// validCats 过滤出合法的站点分类 key（见 internal/sitecat）。
func validCats(in []string) []string {
	var out []string
	seen := map[string]bool{}
	for _, c := range in {
		c = strings.TrimSpace(c)
		if c == "" || seen[c] || !sitecat.Valid(c) {
			continue
		}
		seen[c] = true
		out = append(out, c)
	}
	return out
}

func expandLabels(in []string) []string {
	m := map[string]string{
		"ai":       "ai_suspect",
		"ads":      "ad_heavy",
		"personal": "personal",
		"platform": "ugc_platform",
	}
	var out []string
	for _, x := range in {
		if v, ok := m[x]; ok {
			out = append(out, v)
		} else if x != "" {
			out = append(out, x) // 允许直接传完整 label
		}
	}
	return out
}

// ---------- 用户提交 ----------

type submitReq struct {
	URL      string `json:"url"`
	Category string `json:"category"`
	Reason   string `json:"reason"`
	// 人机验证（弹窗里拿到后随提交一起发）
	Captcha        *auth.Challenge `json:"captcha"`
	Answer         string          `json:"answer"`
	TurnstileToken string          `json:"turnstile_token"`
}

// handlePageSnapshot GET /api/page?url=... 返回 KV 中的原文快照。
// 注意：返回的是不可信 HTML 字符串，前端渲染时必须放进 sandbox iframe，禁止直接 innerHTML。
func (s *Server) handlePageSnapshot(w http.ResponseWriter, r *http.Request) {
	raw := r.URL.Query().Get("url")
	key := urlkey.Key(raw)
	if key == "" {
		writeErr(w, http.StatusBadRequest, "url 不合法")
		return
	}
	if s.snap == nil {
		writeErr(w, http.StatusNotImplemented, "原文快照未启用")
		return
	}
	body, found, err := s.snap.Get(kv.Key(key))
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "读取快照失败")
		return
	}
	if !found {
		writeErr(w, http.StatusNotFound, "没有该页面的快照")
		return
	}
	writeJSON(w, http.StatusOK, map[string]interface{}{
		"url": raw, "key": key, "size": len(body), "html": string(body),
	})
}

// fetchPageReq POST /api/fetch-page 的入参。
type fetchPageReq struct {
	URL string `json:"url"`
}

// fetchCooldown 同一 URL 的补抓冷却。
// 按钮会被连点，而每次补抓都是真实外网请求；按 URL 冷却比按 IP 限流更贴合——
// 同一页被不同人点开，也该只抓一次。
const fetchCooldown = 2 * time.Minute

// handleFetchPage POST /api/fetch-page：按需补抓单个页面。
//
// 只允许白名单域名——这个接口会发起真实请求，放开等于给内网扫描当跳板。
// 抓完索引立即更新，但当前这页结果不会自动重排，前端只把按钮置为「已入库」。
func (s *Server) handleFetchPage(w http.ResponseWriter, r *http.Request) {
	if s.FetchPage == nil {
		writeErr(w, http.StatusNotImplemented, "补抓未启用")
		return
	}
	maxBody(w, r)
	var req fetchPageReq
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErr(w, http.StatusBadRequest, "请求体不是合法 JSON")
		return
	}
	u, err := url.Parse(strings.TrimSpace(req.URL))
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Hostname() == "" {
		writeErr(w, http.StatusBadRequest, "URL 不合法")
		return
	}
	host := u.Hostname()
	ok, err := s.st.IsWhitelisted(r.Context(), host)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "白名单查询失败")
		return
	}
	if !ok {
		writeErr(w, http.StatusForbidden, "只补抓白名单内的站点")
		return
	}
	if blocked, err := s.st.IsBlacklisted(r.Context(), host); err != nil || blocked {
		writeErr(w, http.StatusForbidden, "该站点已在黑名单")
		return
	}

	// 冷却按规范 URL 记，避免 /a 与 /a#x 各算一次
	key := urlkey.Canonical(u.String())
	s.fetchMu.Lock()
	if last, seen := s.fetchAt[key]; seen && time.Since(last) < fetchCooldown {
		s.fetchMu.Unlock()
		writeErr(w, http.StatusTooManyRequests, "这一页刚抓过，稍后再试")
		return
	}
	s.fetchAt[key] = time.Now()
	s.fetchMu.Unlock()

	// 单页抓取要真发请求（含解析正文与入索引），给足 30s
	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
	defer cancel()
	n, err := s.FetchPage(ctx, u.String())
	if err != nil {
		// 失败不占冷却：用户想立刻重试不该被自己的失败挡住
		s.fetchMu.Lock()
		delete(s.fetchAt, key)
		s.fetchMu.Unlock()
		writeErr(w, http.StatusBadGateway, "抓取失败："+err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]interface{}{"status": "ok", "indexed": n})
}

// searchItem 检索结果（扁平化便于前端渲染，保留 Hit 内嵌字段）。
type searchItem struct {
	index.Hit
	Score        float64           `json:"score"`
	Reasons      []string          `json:"reasons"`
	Breakdown    rank.Breakdown    `json:"breakdown"`
	LabelReasons map[string]string `json:"label_reasons,omitempty"` // 标签 → 判定理由，供结果页解释
	DupCount     int               `json:"dup_count,omitempty"`     // 被折叠的相同内容副本数
	// SiteName 网站名称（whitelist.site_name，爬虫抓首页时提取）。空串省略，前端回退域名。
	SiteName string `json:"site_name,omitempty"`
	// Snippets 查询词在正文里的上下文片段（证据链的基础，见 snippet.go）。
	Snippets []string `json:"snippets,omitempty"`
	// HasSnapshot 这页有没有原文快照。false 说明爬虫没抓过这页。
	HasSnapshot bool `json:"has_snapshot"`
	// Fetchable 能不能补抓：没快照 + 域名仍在白名单里。
	// 分开两个字段是因为索引里可能存在「已从白名单移除」的历史页面，
	// 那种页面给按钮只会让用户点了吃 403。
	Fetchable bool `json:"fetchable"`
}

// markSnapshots 标注哪些结果已有原文快照、哪些可以按需补抓。
//
// 用 Has 而不是复用片段循环里读到的 body：片段缓存命中时根本没读 KV，
// 而「有没有抓过」不能靠片段是否为空来推断（有快照也可能匹配不到查询词）。
// 快照整体未启用时一律按「有」处理：满屏补抓按钮比没有更糟。
//
// 白名单判断按域名去重：一页结果通常只来自 3~5 个站，没必要逐条查 SQLite。
func (s *Server) markSnapshots(ctx context.Context, items []searchItem) {
	if s.snap == nil {
		for i := range items {
			items[i].HasSnapshot = true
		}
		return
	}
	allowed := make(map[string]bool, len(items))
	for i := range items {
		key := urlkey.Key(items[i].URL)
		if key == "" {
			items[i].HasSnapshot = true
			continue
		}
		ok, err := s.snap.Has(kv.Key(key))
		items[i].HasSnapshot = err != nil || ok
		if items[i].HasSnapshot {
			continue
		}
		// 没快照：只有域名还在白名单里，补抓才不会一进去就被拒
		host := items[i].Domain
		ok, cached := allowed[host]
		if !cached {
			ok, _ = s.st.IsWhitelisted(ctx, host)
			allowed[host] = ok
		}
		items[i].Fetchable = ok
	}
}

// handleSubmit POST /api/submit。默认 403 关闭（红线 6）。
func (s *Server) handleSubmit(w http.ResponseWriter, r *http.Request) {
	if !s.SubmitEnabled {
		writeErr(w, http.StatusForbidden, "网站提交暂未开放（首批白名单质量验证中）")
		return
	}
	maxBody(w, r)
	var req submitReq
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErr(w, http.StatusBadRequest, "请求体不是合法 JSON")
		return
	}
	if err := s.checkCaptchaUnlessAdmin(r, req.Captcha, req.Answer, req.TurnstileToken, "submit"); err != nil {
		authErr(w, err)
		return
	}
	domain := domainOf(req.URL)
	if !validPublicDomain(domain) {
		writeErr(w, http.StatusBadRequest, "URL 不合法（需为公开域名）")
		return
	}
	raw := strings.TrimSpace(req.URL)
	if !strings.Contains(raw, "://") {
		raw = "https://" + raw
	}
	// 提交即预检：审核时不用再手动点一次；预检失败不阻断提交（结果里带 error）
	blacklisted, err := s.st.IsBlacklisted(r.Context(), domain)
	if err != nil {
		slog.Warn("blacklist check failed", "domain", domain, "err", err)
	}
	pctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	_, pre := s.runPrecheck(pctx, raw, blacklisted)
	cancel()

	// 分类：AI 打标开着时用户根本没得选（提交页不渲染那个下拉），
	// 所以不能信请求里的值——统一先记「其他」，等后台跑一次 AI 再写回。
	// 没开 AI 时才用用户选的，且必须是词表里的合法 key。
	cat := strings.TrimSpace(req.Category)
	if s.FeatureEnabled(r.Context(), "ai_labeling") || !sitecat.Valid(cat) {
		cat = sitecat.Other
	}
	_, ticket, err := s.st.CreateSubmission(r.Context(), store.ReviewItem{
		URL: req.URL, Domain: domain, Category: cat, Reason: req.Reason, Precheck: pre,
	})
	if err != nil {
		slog.Error("submit failed", "err", err)
		writeErr(w, http.StatusInternalServerError, "提交失败")
		return
	}
	// ticket 是一次性凭证：站点域名会暴露提交者的关注点，自增 id 又能被顺序遍历，
	// 所以对外只给随机串，不给 id。
	writeJSON(w, http.StatusOK, map[string]interface{}{"status": "pending", "ticket": ticket})
}

// discoverLimit 深网发现区最多展示几条。少而精：这个区的价值在于
// 「刚好相关又没被看见」，凑数会让它退化成第二个噪音列表。
const discoverLimit = 5

// hitURLs 从候选中取出 URL 列表，用于批量查标签理由与折叠副本数。
func hitURLs(hits []index.Hit) []string {
	out := make([]string, 0, len(hits))
	for _, h := range hits {
		out = append(out, h.URL)
	}
	return out
}

// handleSubmission GET /api/submission?ticket=vx... 查询提交进度。
// 只返回域名/分类/状态/时间：预检细节（robots、可达性）属运营内部信息，
// 且对提交者没有行动价值。
func (s *Server) handleSubmission(w http.ResponseWriter, r *http.Request) {
	ticket := strings.TrimSpace(r.URL.Query().Get("ticket"))
	if len(ticket) < 8 || len(ticket) > 40 {
		writeErr(w, http.StatusBadRequest, "查询码格式不正确")
		return
	}
	item, err := s.st.GetSubmissionByTicket(r.Context(), ticket)
	if errors.Is(err, sql.ErrNoRows) {
		writeErr(w, http.StatusNotFound, "没有找到该查询码对应的提交")
		return
	}
	if err != nil {
		slog.Error("submission lookup failed", "err", err)
		writeErr(w, http.StatusInternalServerError, "查询失败")
		return
	}
	resp := map[string]interface{}{
		"domain":      item.Domain,
		"category":    item.Category,
		"status":      item.Status,
		"status_text": submitStatusText(item.Status),
		"created_at":  item.CreatedAt.Unix(),
	}
	if item.ReviewedAt != nil {
		resp["reviewed_at"] = *item.ReviewedAt
	}
	writeJSON(w, http.StatusOK, resp)
}

// submitStatusText 把状态翻译成用户能理解的进度说明。
func submitStatusText(status string) string {
	switch status {
	case "pending":
		return "等待审核"
	case "approved":
		return "已通过，等待抓取入库"
	case "rejected":
		return "未通过审核"
	default:
		return status
	}
}

// ---------- 管理：白名单 ----------

func (s *Server) handleListWhitelist(w http.ResponseWriter, r *http.Request) {
	limit, offset := pageParams(r)
	cat := r.URL.Query().Get("cat")
	items, total, err := s.st.ListWhitelistIn(r.Context(), r.URL.Query().Get("kw"), cat, limit, offset)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "查询白名单失败")
		return
	}
	// 各分类计数：筛选下拉要显示「开发工具 42」，也让空分类能被发现（提交页填错的站）。
	counts, err := s.st.WhitelistCategoryCounts(r.Context())
	if err != nil {
		counts = map[string]int{}
	}
	writeJSON(w, http.StatusOK, map[string]interface{}{"total": total, "items": items, "cat_counts": counts})
}

type whitelistReq struct {
	Domain        string `json:"domain"`
	Category      string `json:"category"`
	CrawlInterval int    `json:"crawl_interval"`
	// SeedPath 抓取入口路径（默认 "/"）。平台站可指向文章列表页（/pick/、/blogs），
	// 避免首页导航与站务链接吃掉抓取配额。
	SeedPath string `json:"seed_path"`
}

func (s *Server) handleAddWhitelist(w http.ResponseWriter, r *http.Request) {
	maxBody(w, r)
	var req whitelistReq
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErr(w, http.StatusBadRequest, "请求体不是合法 JSON")
		return
	}
	// 容忍粘贴完整网址：管理员提交站点时的自然动作是复制地址栏里的 URL，
	// 而 domain 字段要的是纯域名。实测有人填 https://bbs1.org/ 被判不合法，
	// 但 bbs1.org 本身完全合法——报错只说「domain 不合法」，看不出是格式问题。
	// domainOf 会去协议、去路径、去端口并转小写，正好做这件事。
	req.Domain = domainOf(req.Domain)
	if !validPublicDomain(req.Domain) {
		writeErr(w, http.StatusBadRequest,
			"domain 不合法：需为公开域名（可填 bbs1.org，也可直接粘贴 https://bbs1.org/），禁止 IP 与内网地址")
		return
	}
	if req.Category == "" {
		req.Category = sitecat.Default
	}
	// 分类必须是词表里的 key：写错一个字母就会生成一个永远筛不到、
	// 也永远清不掉的分类（索引里它是独立的一个 keyword 值）。宁可当场报错。
	if !sitecat.Valid(req.Category) {
		writeErr(w, http.StatusBadRequest, "分类不合法："+req.Category)
		return
	}
	// 入口路径必须留在站内：禁止绝对 URL、协议相对路径与查询串（查询串通常被 robots 禁抓）
	if sp := strings.TrimSpace(req.SeedPath); sp != "" {
		if !strings.HasPrefix(sp, "/") || strings.HasPrefix(sp, "//") || strings.ContainsAny(sp, "?#") {
			writeErr(w, http.StatusBadRequest, "seed_path 需为站内绝对路径（如 /pick/），不支持外部地址或查询串")
			return
		}
	}
	if err := s.st.AddWhitelistWithPath(r.Context(), req.Domain, req.Category, req.CrawlInterval, req.SeedPath); err != nil {
		writeErr(w, http.StatusInternalServerError, "写入白名单失败")
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

// handleResetWhitelistIntervals POST /admin/whitelist/intervals {ids:[1,2], interval:0}
//
// interval=0 → 按各站分类重算（新闻 30 分钟、官方站 1 天…）；>0 → 统一设为该值。
// 为什么两种都留：按分类重算会让平台站/新闻站抓得更勤，管理员需要一条「全站退回 1 天」
// 的退路，否则一旦觉得太频繁就只能逐个站点重填。ids 为空表示整张表（一次性回填用）。
func (s *Server) handleResetWhitelistIntervals(w http.ResponseWriter, r *http.Request) {
	maxBody(w, r)
	var req struct {
		IDs      []int64 `json:"ids"`
		Interval int     `json:"interval"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErr(w, http.StatusBadRequest, "请求体不是合法 JSON")
		return
	}
	ctx := r.Context()
	if req.Interval < 0 {
		writeErr(w, http.StatusBadRequest, "抓取间隔不能为负数")
		return
	}
	if req.Interval > 0 {
		ids := req.IDs
		if len(ids) == 0 {
			items, _, err := s.st.ListWhitelist(ctx, "", 1000, 0)
			if err != nil {
				writeErr(w, http.StatusInternalServerError, "读取白名单失败")
				return
			}
			for _, it := range items {
				ids = append(ids, it.ID)
			}
		}
		n := 0
		for _, id := range ids {
			if err := s.st.SetWhitelistInterval(ctx, id, req.Interval); err == nil {
				n++
			}
		}
		writeJSON(w, http.StatusOK, map[string]any{"changed": n, "interval": req.Interval})
		return
	}
	n, err := s.st.ResetIntervalsByCategory(ctx, req.IDs)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "重算抓取间隔失败")
		return
	}
	slog.Info("whitelist intervals reset by category", "changed", n, "ids", len(req.IDs))
	writeJSON(w, http.StatusOK, map[string]any{"changed": n, "interval": 0})
}

func (s *Server) handleToggleWhitelist(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	maxBody(w, r)
	var body struct {
		Enabled bool `json:"enabled"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeErr(w, http.StatusBadRequest, "请求体不是合法 JSON")
		return
	}
	if err := s.st.SetWhitelistEnabled(r.Context(), id, body.Enabled); err != nil {
		writeStoreErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

// ---------- 管理：黑名单 ----------

func (s *Server) handleListBlacklist(w http.ResponseWriter, r *http.Request) {
	limit, offset := pageParams(r)
	items, total, err := s.st.ListBlacklist(r.Context(), r.URL.Query().Get("kw"), limit, offset)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "查询黑名单失败")
		return
	}
	writeJSON(w, http.StatusOK, map[string]interface{}{"total": total, "items": items})
}

func (s *Server) handleAddBlacklist(w http.ResponseWriter, r *http.Request) {
	maxBody(w, r)
	var req struct {
		Domain string `json:"domain"`
		Reason string `json:"reason"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErr(w, http.StatusBadRequest, "请求体不是合法 JSON")
		return
	}
	req.Domain = strings.TrimSpace(strings.ToLower(req.Domain))
	if !validPublicDomain(req.Domain) {
		writeErr(w, http.StatusBadRequest, "domain 不合法（需为公开域名）")
		return
	}
	if err := s.st.AddBlacklist(r.Context(), req.Domain, req.Reason); err != nil {
		writeErr(w, http.StatusInternalServerError, "写入黑名单失败")
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

func (s *Server) handleRemoveBlacklist(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	if err := s.st.RemoveBlacklist(r.Context(), id); err != nil {
		writeStoreErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

// ---------- 管理：审核队列 ----------

// handleReviewPrecheck POST /admin/review/{id}/precheck
// 对队列条目重新执行预检并写回，返回最新结果（审核页「重新预检」按钮）。
func (s *Server) handleReviewPrecheck(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	item, err := s.st.GetReview(r.Context(), id)
	if err != nil {
		writeStoreErr(w, err)
		return
	}
	if !validPublicDomain(item.Domain) {
		writeErr(w, http.StatusBadRequest, "该条目的域名不是公开域名，拒绝预检")
		return
	}
	raw := strings.TrimSpace(item.URL)
	if !strings.Contains(raw, "://") {
		raw = "https://" + raw
	}
	blacklisted, err := s.st.IsBlacklisted(r.Context(), item.Domain)
	if err != nil {
		slog.Warn("blacklist check failed", "domain", item.Domain, "err", err)
	}

	ctx, cancel := context.WithTimeout(r.Context(), 15*time.Second)
	defer cancel()
	res, js := s.runPrecheck(ctx, raw, blacklisted)
	if js != "" {
		if err := s.st.SetReviewPrecheck(r.Context(), id, js); err != nil {
			slog.Warn("persist precheck failed", "id", id, "err", err)
		}
	}
	writeJSON(w, http.StatusOK, map[string]interface{}{
		"id": id, "precheck": res, "blacklisted": blacklisted,
	})
}

func (s *Server) handleListReview(w http.ResponseWriter, r *http.Request) {
	limit, offset := pageParams(r)
	items, total, err := s.st.ListReview(r.Context(),
		r.URL.Query().Get("status"), r.URL.Query().Get("kw"), limit, offset)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "查询审核队列失败")
		return
	}
	writeJSON(w, http.StatusOK, map[string]interface{}{"total": total, "items": items})
}

func (s *Server) handleApproveReview(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	if err := s.st.ApproveReview(r.Context(), id); err != nil {
		writeStoreErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "approved"})
}

func (s *Server) handleRejectReview(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	maxBody(w, r)
	var body struct {
		Reason string `json:"reason"`
	}
	json.NewDecoder(r.Body).Decode(&body) // reason 可空
	if err := s.st.RejectReview(r.Context(), id, body.Reason); err != nil {
		writeStoreErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "rejected"})
}

// ---------- 工具 ----------

// corsMiddleware 处理跨域调用。只在启动参数 -cors 非空时挂上。
//
// 为什么默认关闭：检索接口是公开的、且没有密钥，放开跨域等于允许任意站点
// 直接拿我们的索引给别人做搜索框。需要嵌入时才显式列出允许的来源。
func corsMiddleware(origins []string) func(http.Handler) http.Handler {
	allowAll := false
	allowed := make(map[string]bool, len(origins))
	for _, o := range origins {
		o = strings.TrimSpace(o)
		if o == "*" {
			allowAll = true
			continue
		}
		if o != "" {
			allowed[o] = true
		}
	}
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			origin := r.Header.Get("Origin")
			if origin != "" && (allowAll || allowed[origin]) {
				w.Header().Set("Access-Control-Allow-Origin", origin)
				w.Header().Add("Vary", "Origin")
				w.Header().Set("Access-Control-Allow-Methods", "GET, POST, OPTIONS")
				w.Header().Set("Access-Control-Allow-Headers", "Content-Type, Authorization, X-Admin-Token")
				w.Header().Set("Access-Control-Max-Age", "600")
			}
			if r.Method == http.MethodOptions {
				w.WriteHeader(http.StatusNoContent)
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}

func writeJSON(w http.ResponseWriter, code int, v interface{}) {
	writeJSONOpt(w, code, v, false)
}

// writeJSONOpt 同 writeJSON，pretty 为 true 时缩进输出（调试与 curl 直观看）。
func writeJSONOpt(w http.ResponseWriter, code int, v interface{}, pretty bool) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(code)
	enc := json.NewEncoder(w)
	if pretty {
		enc.SetIndent("", "  ")
	}
	_ = enc.Encode(v)
}

func writeErr(w http.ResponseWriter, code int, msg string) {
	writeJSON(w, code, map[string]string{"error": msg})
}

func writeStoreErr(w http.ResponseWriter, err error) {
	if errors.Is(err, sql.ErrNoRows) {
		writeErr(w, http.StatusNotFound, "记录不存在")
		return
	}
	slog.Error("store operation failed", "err", err)
	writeErr(w, http.StatusInternalServerError, "服务内部错误")
}

func pathID(w http.ResponseWriter, r *http.Request) (int64, bool) {
	id, err := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
	if err != nil {
		writeErr(w, http.StatusBadRequest, "id 不合法")
		return 0, false
	}
	return id, true
}

func pageParams(r *http.Request) (limit, offset int) {
	page, _ := strconv.Atoi(r.URL.Query().Get("page"))
	size, _ := strconv.Atoi(r.URL.Query().Get("size"))
	if page <= 0 {
		page = 1
	}
	if size <= 0 || size > 100 {
		size = 20
	}
	return size, (page - 1) * size
}

func splitCSV(s string) []string {
	if s == "" {
		return nil
	}
	var out []string
	for _, x := range strings.Split(s, ",") {
		if x = strings.TrimSpace(x); x != "" {
			out = append(out, x)
		}
	}
	return out
}

// domainOf 从 URL 提取主机名（小写、去端口）。非法输入返回空串。
func domainOf(raw string) string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return ""
	}
	if !strings.Contains(raw, "://") {
		raw = "https://" + raw
	}
	rest := strings.SplitN(raw, "://", 2)[1]
	host := strings.SplitN(rest, "/", 2)[0]
	host = strings.SplitN(host, ":", 2)[0]
	return strings.ToLower(host)
}
