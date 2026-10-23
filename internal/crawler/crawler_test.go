package crawler

import (
	"context"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	"voidxo/internal/index"
	"voidxo/internal/kv"
	"voidxo/internal/processor"
	"voidxo/internal/store"
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

	host := parseHost(srv.URL)
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
	cw := New(st, idx, proc, snap, cfg)

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
	cw := New(st, idx, proc, snap, cfg)

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
	cw := New(st, idx, proc, snap, cfg)

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
	cw := New(st, idx, proc, snap, cfg)

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
	cw := New(st, idx, proc, snap, cfg)

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
