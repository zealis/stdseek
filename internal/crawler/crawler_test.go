package crawler

import (
	"context"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	"voidxo/internal/index"
	"voidxo/internal/processor"
	"voidxo/internal/store"
)

func newTestDeps(t *testing.T) (*store.Store, *index.Index) {
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
	return st, idx
}

func TestSeedURLsOnlyFromWhitelist(t *testing.T) {
	st, _ := newTestDeps(t)
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

func TestCrawlSeedsRespectsWhitelistAndIndexes(t *testing.T) {
	st, idx := newTestDeps(t)
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
	cw := New(st, idx, proc, cfg)

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
