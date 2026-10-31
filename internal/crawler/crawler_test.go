package crawler

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"voidxo/internal/index"
	"voidxo/internal/kv"
	"voidxo/internal/processor"
	"voidxo/internal/snapshot"
	"voidxo/internal/store"
	"voidxo/internal/ua"
	"voidxo/internal/urlkey"
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
		w.Write([]byte(`<html><head><title>维空首页</title>
			<meta name="description" content="voidxo 测试页"></head>
			<body>hello voidxo <a href="/page2">page2</a>
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
			voidxo 的爬虫在站点恢复之后重新抓到了这一页的正文内容，长度足够进入检索索引。</body></html>`))
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
	const base = "voidxo-spider/0.1 (+https://stdseek.com/bot)"

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

// 403 只报状态码不够：管理员还得知道「谁在拦我们」。
// 这条钉住从响应头到异常说明的整条链路（colly.Response.Headers → crawlOutcome → detail）。
func TestCrawlIssueCarriesBlockHint(t *testing.T) {
	st, idx, snap := newTestDeps(t)
	ctx := context.Background()

	mux := http.NewServeMux()
	mux.HandleFunc("/robots.txt", func(w http.ResponseWriter, _ *http.Request) {
		w.Write([]byte("User-agent: *\nAllow: /\n"))
	})
	mux.HandleFunc("/", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Server", "cloudflare")
		w.Header().Set("Cf-Mitigated", "challenge")
		http.Error(w, "blocked", http.StatusForbidden)
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
	if total != 1 {
		t.Fatalf("want 1 issue, got total=%d", total)
	}
	d := issues[0].Detail
	for _, want := range []string{"403", "server=cloudflare", "cf-mitigated=challenge"} {
		if !strings.Contains(d, want) {
			t.Fatalf("异常说明缺少 %q：%s", want, d)
		}
	}
}

// 头字段的取值规则：只挑三个、空值跳过、超长截断。
func TestBlockHint(t *testing.T) {
	cases := []struct {
		name string
		h    http.Header
		want string
	}{
		{"空头", http.Header{}, ""},
		{"只有无关头", http.Header{"Content-Type": {"text/html"}}, ""},
		{"cloudflare 挑战", http.Header{"Server": {"cloudflare"}, "Cf-Mitigated": {"challenge"}},
			"（server=cloudflare，cf-mitigated=challenge）"},
		{"限流等待", http.Header{"Retry-After": {"30"}}, "（retry-after=30）"},
		{"超长截断", http.Header{"Server": {strings.Repeat("x", 80)}},
			"（server=" + strings.Repeat("x", 40) + "）"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := blockHint(c.h); got != c.want {
				t.Fatalf("got %q want %q", got, c.want)
			}
		})
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
