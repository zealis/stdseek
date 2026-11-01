package crawler

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/PuerkitoBio/goquery"

	"github.com/zealis/stdseek/internal/index"
	"github.com/zealis/stdseek/internal/kv"
	"github.com/zealis/stdseek/internal/processor"
	"github.com/zealis/stdseek/internal/snapshot"
	"github.com/zealis/stdseek/internal/store"
	"github.com/zealis/stdseek/internal/ua"
	"github.com/zealis/stdseek/internal/urlkey"
)

func newTestDeps(t *testing.T) (*store.Store, *index.Index, *kv.Store) {
	t.Helper()
	dir := t.TempDir()
	st, err := store.Open(filepath.Join(dir, "test.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { st.Close() })
	idx, err := index.Open(filepath.Join(dir, "bleve"))
	if err != nil {
		t.Fatalf("open index: %v", err)
	}
	t.Cleanup(func() { idx.Close() })
	snap, err := kv.Open(filepath.Join(dir, "kv"))
	if err != nil {
		t.Fatalf("open kv: %v", err)
	}
	t.Cleanup(func() { snap.Close() })
	return st, idx, snap
}

func TestSeedURLsOnlyFromWhitelist(t *testing.T) {
	st, _, _ := newTestDeps(t)
	ctx := context.Background()

	if err := st.AddWhitelist(ctx, "go.dev", "official", 3600); err != nil {
		t.Fatal(err)
	}
	if err := st.AddWhitelist(ctx, "blocked.example", "blog", 3600); err != nil {
		t.Fatal(err)
	}
	if err := st.AddBlacklist(ctx, "blocked.example", "垃圾站"); err != nil {
		t.Fatal(err)
	}
	if err := st.AddWhitelist(ctx, "disabled.example", "blog", 3600); err != nil {
		t.Fatal(err)
	}
	// 禁用后不应出现在种子里
	items, _, err := st.ListWhitelist(ctx, "disabled.example", 10, 0)
	if err != nil || len(items) != 1 {
		t.Fatalf("list whitelist: %v %v", err, items)
	}
	if err := st.SetWhitelistEnabled(ctx, items[0].ID, false); err != nil {
		t.Fatal(err)
	}

	seeds, err := st.SeedURLs(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(seeds) != 2 {
		t.Fatalf("want 2 enabled seeds, got %v", seeds)
	}
	for _, s := range seeds {
		if s == "https://disabled.example/" {
			t.Fatalf("disabled site leaked into seeds: %v", seeds)
		}
	}

	// 域名匹配：子域应命中父域白名单
	ok, err := st.IsWhitelisted(ctx, "pkg.go.dev")
	if err != nil || !ok {
		t.Fatalf("subdomain should match whitelist: %v %v", ok, err)
	}
	ok, _ = st.IsWhitelisted(ctx, "evil.example")
	if ok {
		t.Fatal("unknown domain must not be whitelisted")
	}
}

func TestRunDueCrawlsOnlyDueSites(t *testing.T) {
	st, idx, snap := newTestDeps(t)
	ctx := context.Background()

	mux := http.NewServeMux()
	mux.HandleFunc("/robots.txt", func(w http.ResponseWriter, _ *http.Request) {
		w.Write([]byte("User-agent: *\nAllow: /\n"))
	})
	mux.HandleFunc("/", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Write([]byte(`<html><head><title>Due 测试</title></head><body>due crawl content</body></html>`))
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	// 白名单要带端口：RunDue 用 scheme://domain+path 拼种子 URL，parseHost 会剥掉
	// 端口，种子于是指向 80 端口——测试服务器不在那里（真实站点无显式端口，只影响测试）。
	// 这是 HEAD 上就存在的存量失败，不是本轮改动引入的。
	host := hostWithPort(srv.URL)
	if err := st.AddWhitelist(ctx, host, "blog", 3600); err != nil {
		t.Fatal(err)
	}
	proc, err := processor.New()
	if err != nil {
		t.Fatal(err)
	}
	cfg := DefaultConfig()
	cfg.SeedScheme = "http"
	cfg.DomainDelay = 10 * time.Millisecond
	cfg.MinWordCount = 1 // 测试页面正文短，薄页过滤另有用例
	cw := New(st, idx, proc, snapshot.NewLocal(snap), cfg)

	// 首次：从未抓取 → 到期
	stats, err := cw.RunDue(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if stats.Seeds != 1 || stats.Indexed == 0 {
		t.Fatalf("first run stats=%+v", stats)
	}

	// 立刻再跑：last_crawled_at 已更新且 interval=3600 → 不应再抓
	stats, err = cw.RunDue(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if stats.Seeds != 0 {
		t.Fatalf("second run should have no due site, got %+v", stats)
	}
}

func TestCrawlSeedsRespectsWhitelistAndIndexes(t *testing.T) {
	st, idx, snap := newTestDeps(t)
	ctx := context.Background()

	mux := http.NewServeMux()
	mux.HandleFunc("/robots.txt", func(w http.ResponseWriter, _ *http.Request) {
		w.Write([]byte("User-agent: *\nAllow: /\n"))
	})
	mux.HandleFunc("/", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Write([]byte(`<html><head><title>StdSeek 首页</title>
			<meta name="description" content="stdseek 测试页"></head>
			<body>hello stdseek <a href="/page2">page2</a>
			<a href="https://evil.example/out">out</a></body></html>`))
	})
	mux.HandleFunc("/page2", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Write([]byte(`<html><head><title>第二页</title></head><body>page two content</body></html>`))
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	host := parseHost(srv.URL)
	if err := st.AddWhitelist(ctx, host, "blog", 3600); err != nil {
		t.Fatal(err)
	}

	proc, err := processor.New()
	if err != nil {
		t.Fatalf("processor: %v", err)
	}
	cfg := DefaultConfig()
	cfg.DomainDelay = 10 * time.Millisecond
	cfg.MinWordCount = 1
	cw := New(st, idx, proc, snapshot.NewLocal(snap), cfg)

	stats, err := cw.CrawlSeeds(ctx, []string{srv.URL + "/"})
	if err != nil {
		t.Fatal(err)
	}
	if stats.Indexed < 2 {
		t.Fatalf("want at least 2 indexed pages, got %+v", stats)
	}
	// 站外链接不得被抓取（白名单外）
	if stats.Fetched > 2 {
		t.Fatalf("off-whitelist link was followed: %+v", stats)
	}

	// 原文快照应落 KV
	raw, ok, err := snap.Get(kv.Key(urlkey.Key(srv.URL + "/")))
	if err != nil || !ok || len(raw) == 0 {
		t.Fatalf("snapshot missing: ok=%v err=%v len=%d", ok, err, len(raw))
	}

	// 出链应落库，供繁荣度统计器使用
	outlinks, err := st.AllOutlinks(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(outlinks) < 2 {
		t.Fatalf("outlinks not persisted: %+v", outlinks)
	}

	hits, total, err := idx.Search(index.Query{Q: "hello", Size: 5})
	if err != nil {
		t.Fatal(err)
	}
	if total != 1 || len(hits) != 1 {
		t.Fatalf("want 1 hit for hello, got total=%d hits=%d", total, len(hits))
	}
	if hits[0].Domain != host {
		t.Fatalf("unexpected hit domain: %s", hits[0].Domain)
	}
	// published_at 缺失时必须标记按收录时间
	if !hits[0].TimeByFetch {
		t.Fatal("hit without published_at must be marked time_by_fetch")
	}
}

// 并发抓取多个站点：每个站点独立 collector，统计需正确汇总且无竞态（配合 -race）。
func TestCrawlSeedsConcurrentSites(t *testing.T) {
	st, idx, snap := newTestDeps(t)
	ctx := context.Background()

	newSite := func(title string) *httptest.Server {
		mux := http.NewServeMux()
		mux.HandleFunc("/robots.txt", func(w http.ResponseWriter, _ *http.Request) {
			w.Write([]byte("User-agent: *\nAllow: /\n"))
		})
		mux.HandleFunc("/", func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
			w.Write([]byte(`<html><head><title>` + title + `</title></head><body>concurrent crawl body</body></html>`))
		})
		srv := httptest.NewServer(mux)
		t.Cleanup(srv.Close)
		return srv
	}
	siteA := newSite("并发站点 A")
	siteB := newSite("并发站点 B")

	// 两个 server 的 hostname 相同（都是 127.0.0.1，只是端口不同），白名单按域名去重，
	// 因此只登记一条；并发语义由「两个 seed 各自独立 collector 并行」体现。
	if err := st.AddWhitelist(ctx, parseHost(siteA.URL), "blog", 3600); err != nil {
		t.Fatal(err)
	}

	proc, err := processor.New()
	if err != nil {
		t.Fatal(err)
	}
	cfg := DefaultConfig()
	cfg.SeedScheme = "http"
	cfg.DomainDelay = 10 * time.Millisecond
	cfg.Concurrency = 2
	cfg.MinWordCount = 1
	cw := New(st, idx, proc, snapshot.NewLocal(snap), cfg)

	stats, err := cw.CrawlSeeds(ctx, []string{siteA.URL + "/", siteB.URL + "/"})
	if err != nil {
		t.Fatal(err)
	}
	if stats.Seeds != 2 || stats.Fetched < 2 || stats.Indexed < 2 {
		t.Fatalf("concurrent stats=%+v", stats)
	}
	if stats.Errors != 0 {
		t.Fatalf("unexpected errors: %+v", stats)
	}

	// 站点状态应回写 ok（两个 seed 并发回写同一行，不能互相覆盖成空值）
	sites, _, err := st.ListWhitelist(ctx, "", 10, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(sites) != 1 {
		t.Fatalf("whitelist size=%d", len(sites))
	}
	if sites[0].LastStatus != "ok" {
		t.Fatalf("site %s last_status=%q", sites[0].Domain, sites[0].LastStatus)
	}
	if sites[0].FailCount != 0 {
		t.Fatalf("successful crawl should reset fail_count, got %d", sites[0].FailCount)
	}

	hits, total, err := idx.Search(index.Query{Q: "concurrent", Size: 10})
	if err != nil {
		t.Fatal(err)
	}
	if total < 2 || len(hits) < 2 {
		t.Fatalf("indexed docs=%d hits=%d", total, len(hits))
	}
}

// 薄页（导航/营销页）不入索引：真实抓取里 kubernetes.io 的 43 词导航页曾大量占用索引。
func TestThinPagesAreNotIndexed(t *testing.T) {
	st, idx, snap := newTestDeps(t)
	ctx := context.Background()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/robots.txt" {
			w.Write([]byte("User-agent: *\nAllow: /\n"))
			return
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Write([]byte(`<html><head><title>Short page</title></head><body>tiny</body></html>`))
	}))
	defer srv.Close()

	if err := st.AddWhitelist(ctx, parseHost(srv.URL), "blog", 3600); err != nil {
		t.Fatal(err)
	}
	proc, err := processor.New()
	if err != nil {
		t.Fatal(err)
	}
	cfg := DefaultConfig()
	cfg.SeedScheme = "http"
	cfg.DomainDelay = 10 * time.Millisecond
	cfg.MinWordCount = 150
	cw := New(st, idx, proc, snapshot.NewLocal(snap), cfg)

	stats, err := cw.CrawlSeeds(ctx, []string{srv.URL + "/"})
	if err != nil {
		t.Fatal(err)
	}
	if stats.Thin != 1 || stats.Indexed != 0 {
		t.Fatalf("thin page should be skipped: %+v", stats)
	}
	if _, total, err := idx.Search(index.Query{Q: "tiny", Size: 5}); err != nil || total != 0 {
		t.Fatalf("index should be empty, total=%d err=%v", total, err)
	}
	// 全部页面都是薄页时，状态记为 no-document，触发退避
	items, _, err := st.ListWhitelist(ctx, parseHost(srv.URL), 10, 0)
	if err != nil || len(items) != 1 {
		t.Fatalf("list: %v %v", err, items)
	}
	if items[0].LastStatus != "no-document" || items[0].FailCount != 1 {
		t.Fatalf("status=%q fail=%d", items[0].LastStatus, items[0].FailCount)
	}
}

// 重定向到其它子域（如 account.cnblogs.com）必须被拦：白名单父域匹配不该成为越界入口。
func TestRedirectToOtherSubdomainBlocked(t *testing.T) {
	st, idx, snap := newTestDeps(t)
	ctx := context.Background()

	mux := http.NewServeMux()
	mux.HandleFunc("/robots.txt", func(w http.ResponseWriter, _ *http.Request) {
		w.Write([]byte("User-agent: *\nAllow: /\n"))
	})
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		// 模拟主站把请求重定向到登录子域
		http.Redirect(w, r, "http://account."+r.Host+"/login", http.StatusFound)
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	if err := st.AddWhitelist(ctx, parseHost(srv.URL), "platform", 3600); err != nil {
		t.Fatal(err)
	}
	proc, err := processor.New()
	if err != nil {
		t.Fatal(err)
	}
	cfg := DefaultConfig()
	cfg.SeedScheme = "http"
	cfg.DomainDelay = 10 * time.Millisecond
	cfg.MinWordCount = 1
	cw := New(st, idx, proc, snapshot.NewLocal(snap), cfg)

	stats, err := cw.CrawlSeeds(ctx, []string{srv.URL + "/"})
	if err != nil {
		t.Fatal(err)
	}
	if stats.Indexed != 0 {
		t.Fatalf("cross-subdomain redirect must not be indexed: %+v", stats)
	}
	// colly 在 CheckRedirect 阶段拒绝跟随，表现为一次请求错误（不进入 OnRequest）
	if stats.Skipped+stats.Errors == 0 {
		t.Fatalf("blocked redirect should be recorded: %+v", stats)
	}
}

// 列表页（正文大半是链接）即便字数达标也不该入索引。
func TestListPagesAreNotIndexed(t *testing.T) {
	st, idx, snap := newTestDeps(t)
	ctx := context.Background()

	// 需要足够多的链接让正文词数超过 listPageMinWords（300），
	// 否则页面会先被"短页面不做密度判断"的规则放过。
	items := strings.Repeat(`<li><a href="/post">Go 调度器源码分析文章标题示例</a></li>`, 60)
	page := `<html><head><title>技术文章列表</title></head><body><article><h1>最新文章</h1><ul>` +
		items + `</ul></article></body></html>`

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/robots.txt" {
			w.Write([]byte("User-agent: *\nAllow: /\n"))
			return
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Write([]byte(page))
	}))
	defer srv.Close()

	if err := st.AddWhitelist(ctx, parseHost(srv.URL), "platform", 3600); err != nil {
		t.Fatal(err)
	}
	proc, err := processor.New()
	if err != nil {
		t.Fatal(err)
	}
	cfg := DefaultConfig()
	cfg.SeedScheme = "http"
	cfg.DomainDelay = 10 * time.Millisecond
	cfg.MinWordCount = 1 // 让页面先通过薄页检查，只被列表页规则拦下
	cfg.MaxDepth = 1
	cw := New(st, idx, proc, snapshot.NewLocal(snap), cfg)

	stats, err := cw.CrawlSeeds(ctx, []string{srv.URL + "/"})
	if err != nil {
		t.Fatal(err)
	}
	// 测试站点对所有路径返回同一列表页，因此会被多次抓取但全部判定为列表页
	if stats.Indexed != 0 || stats.ListPage == 0 {
		t.Fatalf("list page should be filtered out: %+v", stats)
	}
}

// 白名单站点上的多语言镜像页（/ru/、/bn/ 等）不该进中文技术索引。
func TestNonTargetLanguageSkipped(t *testing.T) {
	st, idx, snap := newTestDeps(t)
	ctx := context.Background()

	ruText := strings.Repeat("Это русский технический текст о серверах и сетях. ", 40)
	page := `<html><head><title>Русская документация</title></head><body><article><p>` +
		ruText + `</p></article></body></html>`

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/robots.txt" {
			w.Write([]byte("User-agent: *\nAllow: /\n"))
			return
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Write([]byte(page))
	}))
	defer srv.Close()

	if err := st.AddWhitelist(ctx, parseHost(srv.URL), "official", 3600); err != nil {
		t.Fatal(err)
	}
	proc, err := processor.New()
	if err != nil {
		t.Fatal(err)
	}
	cfg := DefaultConfig()
	cfg.SeedScheme = "http"
	cfg.DomainDelay = 10 * time.Millisecond
	cfg.MinWordCount = 0 // 关闭薄页判断，确保是语言规则拦下的
	cfg.MaxDepth = 1
	cw := New(st, idx, proc, snapshot.NewLocal(snap), cfg)

	stats, err := cw.CrawlSeeds(ctx, []string{srv.URL + "/"})
	if err != nil {
		t.Fatal(err)
	}
	if stats.Indexed != 0 || stats.LangSkip == 0 {
		t.Fatalf("non-target language should be skipped: %+v", stats)
	}
}

func TestIsBoilerplateTitle(t *testing.T) {
	cases := []struct {
		title string
		want  bool
	}{
		{"阿里云开发者社区用户服务协议-阿里云开发者社区", true},
		{"腾讯云开发者社区版权协议及知识产权说明-腾讯云开发者社区-腾讯云", true},
		{"服务协议", true},
		{"隐私政策", true},
		// 含站务词但属正常文章：不以站务词结尾
		{"对话亚马逊CTO：仍在招聘初级工程师，关注中国开放模型", false},
		{"如何为开源项目写一份用户协议", false},
		// 技术文章不受长度与结尾影响
		{"Redis 持久化机制详解", false},
		{"Vue 3.0 - 新特性一览", false},
	}
	for _, c := range cases {
		if got := processor.IsBoilerplateTitle(c.title); got != c.want {
			t.Errorf("isBoilerplateTitle(%q)=%v want %v", c.title, got, c.want)
		}
	}
}

// 锚点不是不同页面：`/post#a`、`/post#b`、`/post` 只应被抓一次，
// 且入库 URL 不带 fragment。真实事故：博客园 12 篇文章全部以
// `/p/xxx#commentform` 的形式入库，搜索结果链接带着莫名锚点。
func TestCrawlStripsFragmentFromURLs(t *testing.T) {
	st, idx, snap := newTestDeps(t)
	ctx := context.Background()

	var postHits int32
	mux := http.NewServeMux()
	mux.HandleFunc("/robots.txt", func(w http.ResponseWriter, _ *http.Request) {
		w.Write([]byte("User-agent: *\nAllow: /\n"))
	})
	mux.HandleFunc("/", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Write([]byte(`<html><head><title>列表页</title></head><body>
			<a href="/post#commentform">评论</a>
			<a href="/post#footer">页脚</a>
			<a href="/post">正文</a></body></html>`))
	})
	mux.HandleFunc("/post", func(w http.ResponseWriter, _ *http.Request) {
		atomic.AddInt32(&postHits, 1)
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Write([]byte(`<html><head><title>一篇文章</title></head><body>unique marker zanzibar</body></html>`))
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	// 白名单要带端口：RunDue 用 scheme://domain+path 拼种子 URL，parseHost 会剥掉
	// 端口，种子于是指向 80 端口——测试服务器不在那里（真实站点无显式端口，只影响测试）。
	// 这是 HEAD 上就存在的存量失败，不是本轮改动引入的。
	host := hostWithPort(srv.URL)
	if err := st.AddWhitelist(ctx, host, "blog", 3600); err != nil {
		t.Fatal(err)
	}
	proc, err := processor.New()
	if err != nil {
		t.Fatalf("processor: %v", err)
	}
	cfg := DefaultConfig()
	cfg.DomainDelay = 10 * time.Millisecond
	cfg.MinWordCount = 1
	cw := New(st, idx, proc, snapshot.NewLocal(snap), cfg)

	if _, err := cw.CrawlSeeds(ctx, []string{srv.URL + "/"}); err != nil {
		t.Fatal(err)
	}

	if n := atomic.LoadInt32(&postHits); n != 1 {
		t.Fatalf("同一页面的不同锚点应只抓一次，实际请求 %d 次", n)
	}
	hits, _, err := idx.Search(index.Query{Q: "zanzibar", Size: 5})
	if err != nil {
		t.Fatal(err)
	}
	if len(hits) != 1 {
		t.Fatalf("want 1 indexed doc, got %d", len(hits))
	}
	if strings.Contains(hits[0].URL, "#") {
		t.Fatalf("索引 URL 不应带 fragment: %s", hits[0].URL)
	}
	if want := srv.URL + "/post"; hits[0].URL != want {
		t.Fatalf("url=%q want %q", hits[0].URL, want)
	}
}

// hostWithPort 返回带端口的主机部分（127.0.0.1:39481），供本地测试服务器加白名单用。
func hostWithPort(raw string) string {
	u, err := url.Parse(raw)
	if err != nil {
		return raw
	}
	return u.Host
}

// TestIsAggregateURL BFS 阶段拦截聚合页，避免白占页数配额。
//
// 真实案例：tech.meituan.com 单次 25 页配额里 8 页是 /tags.html?tag=xxx
// （每页 13 词，最后被 thin 过滤），修复前 indexed 只有 11/25。
// 拦截后同样配额下 indexed 24/25。
//
// 反面用例同样重要：/tagging-guide 这类正常文章路径、日期路径、?id= 文章
// 都不能被误判成聚合页——误杀的代价是整站文章抓不到。
func TestIsAggregateURL(t *testing.T) {
	aggregate := []string{
		"https://tech.meituan.com/tags.html?tag=%E5%A4%A7%E6%A8%A1%E5%9E%8B",
		"https://a.example/tags/",
		"https://a.example/tag/kubernetes",
		"https://a.example/category/backend",
		"https://a.example/categories",
		"https://a.example/archive/2024",
		"https://a.example/posts?category=go",
		"https://a.example/list?sort=hot",
	}
	for _, u := range aggregate {
		if !isAggregateURL(u) {
			t.Errorf("应判为聚合页: %s", u)
		}
	}
	articles := []string{
		"https://a.example/tagging-guide",        // 含 tag 但不是聚合
		"https://a.example/categories-explained", // 同上
		"https://a.example/p/123",
		"https://go.dev/doc/effective_go",
		"https://a.example/2024/05/some-post", // 日期路径不是归档页
		"https://a.example/article?id=123",    // ?id= 是文章标识
		"https://a.example/blog/why-i-wrote-a-tagging-system",
	}
	for _, u := range articles {
		if isAggregateURL(u) {
			t.Errorf("不应判为聚合页（误杀文章）: %s", u)
		}
	}
}

// TestQuotaForScalesWithProsperity 动态配额的三条边界：
// 长尾站保底（不被饿死）、繁荣站多抓、有绝对上限（个别站不吃满整轮）。
func TestQuotaForScalesWithProsperity(t *testing.T) {
	c := &Crawler{cfg: Config{
		DynamicQuota: true, QuotaBase: 15, QuotaSpan: 40, QuotaMax: 80,
		MaxPagesPerHost: 25,
	}, pros: map[string]float64{
		"rich.example": 1.0, // 最繁荣
		"mid.example":  0.5, // 中等
		"lean.example": 0,   // 无繁荣度记录
	}}

	if got := c.quotaFor("lean.example"); got != 15 {
		t.Fatalf("长尾站应保底 15 页，实际 %d", got)
	}
	if got := c.quotaFor("mid.example"); got != 35 {
		t.Fatalf("中等站应为 15+0.5*40=35，实际 %d", got)
	}
	if got := c.quotaFor("rich.example"); got != 55 {
		t.Fatalf("最繁荣站应为 15+40=55，实际 %d", got)
	}

	// 关掉动态配额 → 退化为固定值
	c.cfg.DynamicQuota = false
	if got := c.quotaFor("rich.example"); got != 25 {
		t.Fatalf("关闭后应为固定 MaxPagesPerHost=25，实际 %d", got)
	}

	// 绝对上限兜底（配置写飞了也不能无限抓）
	c.cfg.DynamicQuota = true
	c.cfg.QuotaSpan = 100000
	if got := c.quotaFor("rich.example"); got != 80 {
		t.Fatalf("应被 QuotaMax=80 截断，实际 %d", got)
	}
}

// TestInterleaveSpreadsLongTail 长尾站必须按比例均匀铺开，不能堆在一端。
//
// 第一版算法是「先放完长尾再放繁荣」，实测（23 站 / 4 长尾）把长尾全堆在
// 序列前部，20 个滑动窗口里 13 个只剩繁荣站——等于没交错。这条断言就是
// 为了钉住那个教训：长尾站要覆盖整轮，且间隔接近平均值。
func TestInterleaveSpreadsLongTail(t *testing.T) {
	pros := map[string]float64{"a.com": 0.9, "b.com": 0.5, "c.com": 0.2}
	seeds := []string{
		"https://a.com/", "https://b.com/", "https://c.com/",
		"https://lean1.com/", "https://lean2.com/", "https://lean3.com/",
		"https://lean4.com/", "https://lean5.com/",
	}
	got := interleaveByProsperity(seeds, pros)

	if len(got) != len(seeds) {
		t.Fatalf("站点数不应变化：%d vs %d", len(got), len(seeds))
	}
	seen := map[string]bool{}
	for _, s := range got {
		seen[s] = true
	}
	for _, s := range seeds {
		if !seen[s] {
			t.Fatalf("站点丢失：%s", s)
		}
	}

	// 长尾站的位置
	var pos []int
	for i, s := range got {
		if pros[hostOf(s)] == 0 {
			pos = append(pos, i)
		}
	}
	if len(pos) != 5 {
		t.Fatalf("应有 5 个长尾站，实际 %d", len(pos))
	}
	// 覆盖整轮：第一个长尾不能太靠后，最后一个不能太靠前
	if pos[0] > len(got)/4 {
		t.Fatalf("第一个长尾站在 %d，序列前 1/4 没有长尾可抓", pos[0])
	}
	if pos[len(pos)-1] < len(got)*3/4 {
		t.Fatalf("最后一个长尾站在 %d，序列后 1/4 没有长尾可抓", pos[len(pos)-1])
	}
	// 间隔均匀：最大间隔不超过平均值 + 1
	avg := float64(len(got)) / float64(len(pos))
	prev := -1
	for _, p := range append(pos, len(got)-1) {
		gap := float64(p - prev)
		if gap > avg+1 {
			t.Fatalf("长尾站间隔 %.0f 超过平均 %.1f（位置 %v）", gap, avg, pos)
		}
		prev = p
	}

	// 边界：无繁荣度数据 / 只有一类 / 站点太少 → 原样返回
	if out := interleaveByProsperity(seeds, nil); len(out) != len(seeds) {
		t.Fatal("无繁荣度数据应原样返回")
	}
	if out := interleaveByProsperity([]string{"https://a.com/"}, map[string]float64{"a.com": 1}); len(out) != 1 {
		t.Fatal("站点太少应原样返回")
	}
	allRich := []string{"https://a.com/", "https://b.com/", "https://c.com/"}
	if out := interleaveByProsperity(allRich, map[string]float64{"a.com": 1, "b.com": 1, "c.com": 1}); len(out) != 3 {
		t.Fatal("只有一类站点应原样返回")
	}
}

func hostOf(seed string) string {
	u, err := url.Parse(seed)
	if err != nil {
		return ""
	}
	return u.Hostname()
}

// 归因优先级：哪条证据最能解释「零入库」。归错类会让管理员做反方向的修复。
func TestClassifyCrawl(t *testing.T) {
	cases := []struct {
		name       string
		in         crawlOutcome
		wantStatus string
		wantKind   string
	}{
		{"种子页超时", crawlOutcome{VisitErr: errors.New("dial tcp: i/o timeout")}, "error", store.CrawlIssueUnreachable},
		{"种子页 404", crawlOutcome{VisitErr: errors.New("Not Found"), LastStatus: 404}, "error", store.CrawlIssueUnreachable},
		{"有页入库即正常", crawlOutcome{Indexed: 3, HTTPErrors: 1, LastStatus: 500}, "ok", ""},
		{"整站 403", crawlOutcome{Fetched: 4, HTTPErrors: 4, LastStatus: 403}, "error", store.CrawlIssueUnreachable},
		{"robots 拦了链接", crawlOutcome{Fetched: 1, RobotsBlocked: 9}, "robots", store.CrawlIssueRobots},
		{"正文解析失败", crawlOutcome{Fetched: 3, ParseFails: 2, FirstParseErr: "empty body"}, "error", store.CrawlIssueStructure},
		{"抓到但没有正文", crawlOutcome{Fetched: 5}, "no-document", store.CrawlIssueStructure},
		{"一页都没抓到", crawlOutcome{}, "no-document", store.CrawlIssueUnreachable},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			status, kind, detail := classifyCrawl(c.in)
			if status != c.wantStatus || kind != c.wantKind {
				t.Fatalf("got %q/%q (%s), want %q/%q", status, kind, detail, c.wantStatus, c.wantKind)
			}
			if kind != "" && detail == "" {
				t.Fatal("异常必须带说明，否则后台只能看到一个类别")
			}
		})
	}
}

// 整站失败进名单、连续失败累加、恢复后自动移出。
func TestCrawlIssueRecordedAndCleared(t *testing.T) {
	st, idx, snap := newTestDeps(t)
	ctx := context.Background()

	var down atomic.Bool
	down.Store(true)
	mux := http.NewServeMux()
	mux.HandleFunc("/robots.txt", func(w http.ResponseWriter, _ *http.Request) {
		w.Write([]byte("User-agent: *\nAllow: /\n"))
	})
	mux.HandleFunc("/", func(w http.ResponseWriter, _ *http.Request) {
		if down.Load() {
			http.Error(w, "bad gateway", http.StatusBadGateway)
			return
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Write([]byte(`<html><head><title>恢复后的文章</title></head><body>
			stdseek 的爬虫在站点恢复之后重新抓到了这一页的正文内容，长度足够进入检索索引。</body></html>`))
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	host := parseHost(srv.URL)
	if err := st.AddWhitelist(ctx, host, "blog", 3600); err != nil {
		t.Fatal(err)
	}
	proc, err := processor.New()
	if err != nil {
		t.Fatalf("processor: %v", err)
	}
	cfg := DefaultConfig()
	cfg.DomainDelay = 10 * time.Millisecond
	cfg.MinWordCount = 1
	cw := New(st, idx, proc, snapshot.NewLocal(snap), cfg)

	if _, err := cw.CrawlSeeds(ctx, []string{srv.URL + "/"}); err != nil {
		t.Fatal(err)
	}
	issues, total, err := st.ListCrawlIssues(ctx, "", "", 10, 0)
	if err != nil {
		t.Fatal(err)
	}
	if total != 1 || len(issues) != 1 {
		t.Fatalf("want 1 issue, got total=%d items=%+v", total, issues)
	}
	if issues[0].Kind != store.CrawlIssueUnreachable || issues[0].Occurrences != 1 {
		t.Fatalf("unexpected issue: %+v", issues[0])
	}
	if issues[0].Domain != host {
		t.Fatalf("issue domain=%q want %q", issues[0].Domain, host)
	}

	// 再失败一轮：同一域名只一行，次数累加
	if _, err := cw.CrawlSeeds(ctx, []string{srv.URL + "/"}); err != nil {
		t.Fatal(err)
	}
	issues, total, _ = st.ListCrawlIssues(ctx, "", "", 10, 0)
	if total != 1 || issues[0].Occurrences != 2 {
		t.Fatalf("second failure should bump occurrences, got total=%d %+v", total, issues)
	}

	// 站点恢复：名单自动清空（名单只讲现状）
	down.Store(false)
	stats, err := cw.CrawlSeeds(ctx, []string{srv.URL + "/"})
	if err != nil {
		t.Fatal(err)
	}
	if stats.Indexed == 0 {
		t.Fatalf("recovered site should be indexed again: %+v", stats)
	}
	if _, total, _ := st.ListCrawlIssues(ctx, "", "", 10, 0); total != 0 {
		t.Fatalf("issue should be cleared after recovery, got %d", total)
	}
}

// robots 禁止抓取：连种子页的请求都不该发出，且要归到「规则变更」而不是「无法访问」。
func TestCrawlIssueForRobotsDisallow(t *testing.T) {
	st, idx, snap := newTestDeps(t)
	ctx := context.Background()

	var hits int32
	mux := http.NewServeMux()
	mux.HandleFunc("/robots.txt", func(w http.ResponseWriter, _ *http.Request) {
		w.Write([]byte("User-agent: *\nDisallow: /\n"))
	})
	mux.HandleFunc("/", func(w http.ResponseWriter, _ *http.Request) {
		atomic.AddInt32(&hits, 1)
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Write([]byte(`<html><head><title>不该被访问</title></head><body>robots 禁止抓取</body></html>`))
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	host := parseHost(srv.URL)
	if err := st.AddWhitelist(ctx, host, "blog", 3600); err != nil {
		t.Fatal(err)
	}
	proc, err := processor.New()
	if err != nil {
		t.Fatalf("processor: %v", err)
	}
	cfg := DefaultConfig()
	cfg.DomainDelay = 10 * time.Millisecond
	cfg.MinWordCount = 1
	cw := New(st, idx, proc, snapshot.NewLocal(snap), cfg)

	if _, err := cw.CrawlSeeds(ctx, []string{srv.URL + "/"}); err != nil {
		t.Fatal(err)
	}
	if n := atomic.LoadInt32(&hits); n != 0 {
		t.Fatalf("robots 禁止时不应请求页面，实际请求了 %d 次", n)
	}
	issues, total, err := st.ListCrawlIssues(ctx, "", "", 10, 0)
	if err != nil {
		t.Fatal(err)
	}
	if total != 1 || issues[0].Kind != store.CrawlIssueRobots {
		t.Fatalf("want robots issue, got total=%d %+v", total, issues)
	}
	list, _, err := st.ListWhitelist(ctx, "", 10, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 1 || list[0].LastStatus != "robots" {
		t.Fatalf("whitelist status should be robots, got %+v", list)
	}
}

// 「浏览器 UA」开关：默认自报身份，只有明确打开才换 Chrome 标识。
// 读不到配置（或值写坏）一律按关处理——宁可自报身份，也不偷偷伪装。
func TestUserAgentFrom(t *testing.T) {
	const base = "stdseek-spider/0.1 (+https://stdseek.com/bot)"

	cases := []struct {
		name    string
		setting string
		want    string
	}{
		{"显式关", "false", base},
		{"空值", "", base},
		{"打开", "true", ua.Browser},
		{"打开（1）", "1", ua.Browser},
		{"打开（大写）", "TRUE", ua.Browser},
		{"值写坏了按关处理", "yes please", base},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := userAgentFrom(base, c.setting); got != c.want {
				t.Fatalf("got %q want %q", got, c.want)
			}
		})
	}
}

// 端到端读开关：真的从 settings 读，而不只是测纯函数。
// 这条能兜住「featureDefs 的落库键改了、crawler 没改」那类漂移。
func TestUserAgentReadsFeatureFlag(t *testing.T) {
	st, idx, _ := newTestDeps(t)
	c := New(st, idx, nil, nil, DefaultConfig())
	ctx := context.Background()

	if got := c.userAgent(); !strings.HasPrefix(got, ua.Bot) {
		t.Fatalf("默认应自报爬虫标识，got %q", got)
	}

	if err := st.SetSetting(ctx, ua.BrowserFeatureKey, "true"); err != nil {
		t.Fatalf("写开关失败：%v", err)
	}
	if got := c.userAgent(); got != ua.Browser {
		t.Fatalf("开关打开后应换成浏览器 UA，got %q", got)
	}
}

// 浏览器 UA 模式：先按浏览器身份抓，一页都没入库就用爬虫身份重来一次。
//
// 这是掘金那类站点的真实形态——对浏览器返回 SPA 空壳、对爬虫返回服务端渲染的
// 全文。只认一种身份要么漏抓、要么被反爬挡住，所以要两条路都走一遍。
func TestBrowserUAFallsBackToSpider(t *testing.T) {
	st, idx, snap := newTestDeps(t)
	ctx := context.Background()
	if err := st.SetSetting(ctx, ua.BrowserFeatureKey, "true"); err != nil {
		t.Fatal(err)
	}

	const article = `<html><head><title>掘金风格的正文页</title></head><body>
		<p>这一页只有在爬虫身份下才会返回服务端渲染的正文，浏览器身份拿到的是一个空壳，</p>
		<p>所以爬虫必须先按浏览器身份试一次，失败后再退回自己的标识重新抓一遍。</p>
		</body></html>`

	mux := http.NewServeMux()
	mux.HandleFunc("/robots.txt", func(w http.ResponseWriter, _ *http.Request) {
		w.Write([]byte("User-agent: *\nAllow: /\n"))
	})
	mux.HandleFunc("/post/1", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		if strings.Contains(r.UserAgent(), "Mozilla") {
			w.Write([]byte(`<html><body><div id="app">loading</div></body></html>`))
			return
		}
		w.Write([]byte(article))
	})
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		if strings.Contains(r.UserAgent(), "Mozilla") {
			w.Write([]byte(`<html><body><div id="app">loading</div></body></html>`))
			return
		}
		w.Write([]byte(`<html><body><a href="/post/1">一篇文章</a></body></html>`))
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	host := parseHost(srv.URL)
	if err := st.AddWhitelist(ctx, host, "blog", 3600); err != nil {
		t.Fatal(err)
	}
	proc, err := processor.New()
	if err != nil {
		t.Fatalf("processor: %v", err)
	}
	cfg := DefaultConfig()
	cfg.DomainDelay = 5 * time.Millisecond
	cfg.MinWordCount = 1
	cw := New(st, idx, proc, snapshot.NewLocal(snap), cfg)

	stats, err := cw.CrawlSeeds(ctx, []string{srv.URL + "/"})
	if err != nil {
		t.Fatal(err)
	}
	if stats.Indexed == 0 {
		t.Fatalf("浏览器 UA 抓空后应回退爬虫 UA 并入库，got %+v", stats)
	}
	if _, total, _ := st.ListCrawlIssues(ctx, "", "", 10, 0); total != 0 {
		t.Fatalf("回退成功后不该留异常记录，got %d", total)
	}
}

// 两种身份都抓不到时，才记异常；且只记一条（两次尝试不产生两条）。
func TestBothUserAgentsFailKeepsSingleIssue(t *testing.T) {
	st, idx, snap := newTestDeps(t)
	ctx := context.Background()
	if err := st.SetSetting(ctx, ua.BrowserFeatureKey, "true"); err != nil {
		t.Fatal(err)
	}

	mux := http.NewServeMux()
	mux.HandleFunc("/robots.txt", func(w http.ResponseWriter, _ *http.Request) {
		w.Write([]byte("User-agent: *\nAllow: /\n"))
	})
	mux.HandleFunc("/", func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "go away", http.StatusForbidden)
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	host := parseHost(srv.URL)
	if err := st.AddWhitelist(ctx, host, "blog", 3600); err != nil {
		t.Fatal(err)
	}
	proc, err := processor.New()
	if err != nil {
		t.Fatalf("processor: %v", err)
	}
	cfg := DefaultConfig()
	cfg.DomainDelay = 5 * time.Millisecond
	cfg.MinWordCount = 1
	cw := New(st, idx, proc, snapshot.NewLocal(snap), cfg)

	if _, err := cw.CrawlSeeds(ctx, []string{srv.URL + "/"}); err != nil {
		t.Fatal(err)
	}
	issues, total, err := st.ListCrawlIssues(ctx, "", "", 10, 0)
	if err != nil {
		t.Fatal(err)
	}
	if total != 1 {
		t.Fatalf("两次都失败应只留 1 条异常，got %d", total)
	}
	if !strings.Contains(issues[0].Detail, "403") {
		t.Fatalf("异常说明应带状态码：%s", issues[0].Detail)
	}
}

// 正文链接 vs 站点外框链接：只靠 URL 形态、锚文本长度、DOM 位置区分，不用路径词表。
//
// 站点改版、换 CMS、换语言都不影响这三样——而「导航词表」换一个站就得再加一行。
func TestLinkPriorityPrefersContent(t *testing.T) {
	content := []struct {
		url     string
		textLen int
	}{
		{"https://www.example.com/thread-65673.htm", 14},
		{"https://www.example.com/posts/2024/go-generics", 12},
		{"https://www.example.com/2024/10/01/how-to-fix-nginx-502", 20},
	}
	chrome := []struct {
		name     string
		url      string
		textLen  int
		inChrome bool
	}{
		{"首页", "https://www.example.com/", 2, true},
		{"规则页", "https://www.example.com/rules.htm", 2, true},
		{"用户中心", "https://www.example.com/user-1.htm", 4, true},
		{"登录页", "https://www.example.com/login", 2, true},
		{"图标链接", "https://www.example.com/tools.htm", 0, true},
	}
	for _, c := range content {
		cs := linkPriority(c.url, c.textLen, false)
		for _, n := range chrome {
			if ns := linkPriority(n.url, n.textLen, n.inChrome); cs <= ns {
				t.Errorf("正文 %s（%d 分）应排在 %s（%d 分）前面", c.url, cs, n.name, ns)
			}
		}
	}

	// 同一个 URL：标题样的长锚文本要高于入口样的短锚文本
	long := linkPriority("https://www.example.com/post-1", 16, false)
	short := linkPriority("https://www.example.com/post-1", 2, false)
	if long <= short {
		t.Fatalf("长锚文本该得分更高：long=%d short=%d", long, short)
	}
}

// 翻页模板靠「同模板 + 页码连号」认出来，与 URL 怎么写无关。
//
// 这条规则替代了原来硬编码的翻页 URL 正则（/index-2.htm、/page/3 ...）：
// 那种枚举换个站就得再加一条，而分页条的形态在所有 CMS 里都一样。
func TestPaginationTemplates(t *testing.T) {
	html := `<html><body>
		<nav><a href="/">首页</a><a href="/rules.htm">规则</a></nav>
		<div class="pagination">
			<a href="/index-1-1.htm">1</a><a href="/index-1-2.htm">2</a>
			<a href="/index-1-3.htm">3</a><a href="/index-1-4.htm">4</a>
		</div>
		<div class="list">
			<a href="/thread-65673.htm">帖子一</a><a href="/thread-65679.htm">帖子二</a>
			<a href="/thread-65691.htm">帖子三</a>
		</div>
	</body></html>`
	doc, err := goquery.NewDocumentFromReader(strings.NewReader(html))
	if err != nil {
		t.Fatal(err)
	}
	got := paginationTemplates(doc.Selection, func(h string) string {
		return "https://example.com" + h
	}, func(string) bool { return true })

	if !got["example.com/index-#-#.htm"] {
		t.Fatalf("分页条应被认成翻页模板，实际得到 %v", got)
	}
	// 文章 ID 是跳跃的（65673、65679、65691），不是页码——认错的话整站文章都抓不到
	if got["example.com/thread-#.htm"] {
		t.Fatal("文章 ID 不能当成页码")
	}

	// 连续编号的文章列表（/post-1、/post-2、/post-3）也不是分页条：
	// 锚文本是标题而不是页码。这一条放过去，整站文章会被当成「清单的第 N 屏」全部跳过。
	blog := `<html><body><div class="list">
		<a href="/post-1">Go 调度器是怎么工作的</a>
		<a href="/post-2">一次线上 502 的排查记录</a>
		<a href="/post-3">从零实现一个反向代理</a>
	</div></body></html>`
	bdoc, err := goquery.NewDocumentFromReader(strings.NewReader(blog))
	if err != nil {
		t.Fatal(err)
	}
	bgot := paginationTemplates(bdoc.Selection, func(h string) string {
		return "https://example.com" + h
	}, func(string) bool { return true })
	if len(bgot) != 0 {
		t.Fatalf("连续编号的文章列表不能判成翻页模板，实际得到 %v", bgot)
	}

	// 锚文本恰好也是纯数字的条目列表（漫画/图集常见：编号徽标 + 标题各一个链接）。
	// 编号之间夹着标题链接，成不了簇——这条判据与语言和站点无关。
	items := `<html><body><div class="items">
		<div class="item"><a href="/comic-1">1</a><a href="/comic-1">海贼王第一话</a></div>
		<div class="item"><a href="/comic-2">2</a><a href="/comic-2">海贼王第二话</a></div>
		<div class="item"><a href="/comic-3">3</a><a href="/comic-3">海贼王第三话</a></div>
	</div></body></html>`
	idoc, err := goquery.NewDocumentFromReader(strings.NewReader(items))
	if err != nil {
		t.Fatal(err)
	}
	igot := paginationTemplates(idoc.Selection, func(h string) string {
		return "https://example.com" + h
	}, func(string) bool { return true })
	if len(igot) != 0 {
		t.Fatalf("编号条目列表（编号间夹标题链接）不能判成翻页模板，实际得到 %v", igot)
	}

	// 编号之间没有别的链接、但夹着条目摘要正文（>4 字），同样不成簇。
	mixed := `<html><body><div>
		<a href="/post-1">1</a><p>这是第一篇文章的摘要，写得还挺长的。</p>
		<a href="/post-2">2</a><p>这是第二篇文章的摘要，同样不短。</p>
		<a href="/post-3">3</a>
	</div></body></html>`
	mdoc, err := goquery.NewDocumentFromReader(strings.NewReader(mixed))
	if err != nil {
		t.Fatal(err)
	}
	mgot := paginationTemplates(mdoc.Selection, func(h string) string {
		return "https://example.com" + h
	}, func(string) bool { return true })
	if len(mgot) != 0 {
		t.Fatalf("编号间夹正文的列表不能判成翻页模板，实际得到 %v", mgot)
	}

	// 同模板的页码被其他链接隔开（列表与「热门/最新」混排）也不成簇。
	scatter := `<html><body>
		<a href="/cat-1.htm">1</a><a href="/hot.htm">热门</a>
		<a href="/cat-2.htm">2</a><a href="/new.htm">最新</a>
		<a href="/cat-3.htm">3</a>
	</body></html>`
	sdoc, err := goquery.NewDocumentFromReader(strings.NewReader(scatter))
	if err != nil {
		t.Fatal(err)
	}
	sgot := paginationTemplates(sdoc.Selection, func(h string) string {
		return "https://example.com" + h
	}, func(string) bool { return true })
	if len(sgot) != 0 {
		t.Fatalf("被其他链接隔开的页码不能判成翻页模板，实际得到 %v", sgot)
	}

	// 反向验证：真分页条的常见变体仍要认出来——当前页是普通文本（不是链接）、
	// 页码间夹省略号。间隔只有几个字，簇不散。
	pager := `<html><body><div class="pager">
		<a href="/list-1.htm">1</a><span class="cur">2</span>
		<a href="/list-3.htm">3</a><span>…</span>
		<a href="/list-9.htm">9</a>
	</div></body></html>`
	pdoc, err := goquery.NewDocumentFromReader(strings.NewReader(pager))
	if err != nil {
		t.Fatal(err)
	}
	pgot := paginationTemplates(pdoc.Selection, func(h string) string {
		return "https://example.com" + h
	}, func(string) bool { return true })
	if !pgot["example.com/list-#.htm"] {
		t.Fatalf("带当前页文本与省略号的分页条应被认出，实际得到 %v", pgot)
	}
}

// 登录/注册页有正文（登录须知、服务条款），词数和链接密度都拦不住，
// 只有表单结构（密码框）能可靠区分。
func TestIsAuthPage(t *testing.T) {
	login := `<html><body><form><input name="user"><input type="password" name="pwd">
		<button>登录</button></form><p>登录须知：请使用注册邮箱登录，忘记密码可找回。</p></body></html>`
	article := `<html><body><article><h1>Go 调度器</h1><p>正文内容。</p></article></body></html>`

	parse := func(h string) *goquery.Selection {
		d, err := goquery.NewDocumentFromReader(strings.NewReader(h))
		if err != nil {
			t.Fatal(err)
		}
		return d.Selection
	}

	if !isAuthPage(parse(login), 60) {
		t.Error("带密码框的短页面应判成登录页")
	}
	// 首页侧栏挂个登录框很常见：正文够长就不能整页判成功能页
	if isAuthPage(parse(login), 800) {
		t.Error("正文很长的页面不该因为侧栏有登录框就被判成功能页")
	}
	if isAuthPage(parse(article), 60) {
		t.Error("没有表单的文章页不该判成功能页")
	}
}

// 假论坛：首页里导航链接排在正文前面，文章正文够长、彼此不同（避免被 simhash 判成转载）。
//
// navs 是站点外框（放在 <nav> 里、锚文本只有两个字），articles 是正文链接（长标题）。
func newForumFixture(t *testing.T, navs, articles []string) *httptest.Server {
	t.Helper()
	var home strings.Builder
	home.WriteString(`<html><head><title>论坛首页</title></head><body><nav>`)
	for _, n := range navs {
		home.WriteString(`<a href="` + n + `">规则</a>`)
	}
	home.WriteString(`</nav><main>`)
	for _, a := range articles {
		home.WriteString(`<a href="` + a + `">帖子标题：这是一篇很长的帖子标题</a>`)
	}
	home.WriteString(`</main></body></html>`)
	index := home.String()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/robots.txt" {
			w.Write([]byte("User-agent: *\nAllow: /\n"))
			return
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		if r.URL.Path == "/" {
			w.Write([]byte(index))
			return
		}
		// 文章页：每页正文都要有差异，否则会被 shingle 指纹判成跨站转载
		var b strings.Builder
		b.WriteString(`<html><head><title>帖子 ` + r.URL.Path + `</title></head><body><article><h1>帖子</h1><p>`)
		for i := 0; i < 12; i++ {
			b.WriteString(`第 ` + strconv.Itoa(i) + ` 段 ` + r.URL.Path + ` 讲的是调度器与运行时内存分配的实现细节。`)
		}
		b.WriteString(`</p></article></body></html>`)
		w.Write([]byte(b.String()))
	}))
	t.Cleanup(srv.Close)
	return srv
}

// 单站配额只有几页时，先抓什么决定这个站能不能收到正文。
// 真实案例：按 DOM 顺序抓，配额全落在导航页上，0 篇帖子入库。
func TestCrawlPrioritizesArticlesOverNav(t *testing.T) {
	st, idx, snap := newTestDeps(t)
	ctx := context.Background()

	navs := []string{"/rules.htm", "/user-1.htm", "/about", "/help", "/links.htm", "/category/backend"}
	articles := []string{"/thread-65673.htm", "/thread-65701.htm", "/thread-65888.htm"}
	srv := newForumFixture(t, navs, articles)

	if err := st.AddWhitelist(ctx, parseHost(srv.URL), "forum", 3600); err != nil {
		t.Fatal(err)
	}
	proc, err := processor.New()
	if err != nil {
		t.Fatalf("processor: %v", err)
	}
	cfg := DefaultConfig()
	cfg.SeedScheme = "http"
	cfg.DomainDelay = 10 * time.Millisecond
	cfg.MinWordCount = 30
	cfg.MaxDepth = 3
	cfg.DynamicQuota = false
	cfg.MaxPagesPerHost = 4 // 首页 + 3 页：正好只够抓 3 篇文章，抓导航就一篇不剩
	cw := New(st, idx, proc, snapshot.NewLocal(snap), cfg)

	stats, err := cw.CrawlSeeds(ctx, []string{srv.URL + "/"})
	if err != nil {
		t.Fatal(err)
	}

	urls := make([]string, len(articles))
	for i, a := range articles {
		urls[i] = urlkey.Canonical(srv.URL + a)
	}
	known, err := st.KnownURLs(ctx, urls)
	if err != nil {
		t.Fatal(err)
	}
	for _, u := range urls {
		if _, ok := known[u]; !ok {
			t.Fatalf("%s 没入库（stats=%+v）——配额被导航页吃掉了", u, stats)
		}
	}
}

// 论坛首页每轮都给同一批置顶帖：已收录的必须让位给没抓过的，
// 否则单站配额每轮都花在同样的页面上，新帖永远进不来。
func TestCrawlPrefersUncrawledLinks(t *testing.T) {
	st, idx, snap := newTestDeps(t)
	ctx := context.Background()

	articles := []string{"/thread-1.htm", "/thread-2.htm", "/thread-3.htm", "/thread-4.htm"}
	srv := newForumFixture(t, nil, articles)
	host := parseHost(srv.URL)
	if err := st.AddWhitelist(ctx, host, "forum", 3600); err != nil {
		t.Fatal(err)
	}

	// 前两篇当「刚抓过」
	oldAt := time.Now().Add(-time.Hour).Unix()
	for _, a := range articles[:2] {
		if _, err := st.UpsertDocument(ctx, store.Document{
			URL: urlkey.Canonical(srv.URL + a), Domain: host,
			Title: "旧帖", WordCount: 100, FetchedAt: oldAt,
		}); err != nil {
			t.Fatal(err)
		}
	}

	proc, err := processor.New()
	if err != nil {
		t.Fatalf("processor: %v", err)
	}
	cfg := DefaultConfig()
	cfg.SeedScheme = "http"
	cfg.DomainDelay = 10 * time.Millisecond
	cfg.MinWordCount = 30
	cfg.MaxDepth = 3
	cfg.DynamicQuota = false
	cfg.MaxPagesPerHost = 3 // 首页 + 2 页：只够抓两篇，必须落在没抓过的上面
	cw := New(st, idx, proc, snapshot.NewLocal(snap), cfg)

	if _, err := cw.CrawlSeeds(ctx, []string{srv.URL + "/"}); err != nil {
		t.Fatal(err)
	}

	urls := make([]string, len(articles))
	for i, a := range articles {
		urls[i] = urlkey.Canonical(srv.URL + a)
	}
	known, err := st.KnownURLs(ctx, urls)
	if err != nil {
		t.Fatal(err)
	}
	for _, u := range urls[2:] {
		if _, ok := known[u]; !ok {
			t.Fatalf("%s 是新帖却没入库（known=%v）——配额又被已收录页占走了", u, known)
		}
	}
	if at := known[urls[0]]; at != oldAt {
		t.Fatalf("已收录的 %s 被重抓了（fetched_at %d → %d）", urls[0], oldAt, at)
	}
}

// 论坛「排序 / 只看某人」链接是同一帖的视图，不该被当成新页面再抓一遍。
func TestContentURLStripsViewParams(t *testing.T) {
	same := map[string]string{
		"https://www.example.com/thread-65667.htm?orderby=asc&user=71758": "https://www.example.com/thread-65667.htm",
		"https://example.com/p/123?sort=date":                             "https://example.com/p/123",
		"https://example.com/post-9?view=print":                           "https://example.com/post-9",
	}
	for in, want := range same {
		if got := contentURL(in); got != want {
			t.Errorf("contentURL(%s) = %s，期望 %s", in, got, want)
		}
	}

	// 这几个都必须原样保留：?id= 是内容身份；作者页末段没有数字
	// （不同作者不能被塌成一页）；无查询串的 URL 不该被动。
	keep := []string{
		"https://example.com/article.php?id=123",
		"https://example.com/author.php?user=5",
		"https://example.com/p/123?id=456",
		"https://www.example.com/thread-65667.htm",
	}
	for _, in := range keep {
		if got := contentURL(in); got != in {
			t.Errorf("contentURL(%s) = %s，应当原样保留", in, got)
		}
	}
}
