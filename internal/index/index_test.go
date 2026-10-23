package index

import (
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func newTestIndex(t *testing.T) *Index {
	t.Helper()
	idx, err := Open(filepath.Join(t.TempDir(), "bleve"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { idx.Close() })
	return idx
}

func mustIndex(t *testing.T, idx *Index, docs ...Doc) {
	t.Helper()
	for _, d := range docs {
		if err := idx.IndexDocument(d); err != nil {
			t.Fatal(err)
		}
	}
}

// 时间窗是硬条件：旧发布 + 新收录不得混入「1 天」。
func TestTimeWindowIsHardFilter(t *testing.T) {
	idx := newTestIndex(t)
	now := time.Now().Unix()
	oldPub := time.Now().AddDate(-2, 0, 0).Unix()
	newPub := time.Now().Add(-time.Hour).Unix()

	mustIndex(t, idx,
		Doc{URL: "https://a/old", Domain: "a", Title: "timewindow old", Body: "timewindow stale",
			PublishedAt: &oldPub, FetchedAt: now},
		Doc{URL: "https://b/new", Domain: "b", Title: "timewindow fresh", Body: "timewindow recent",
			PublishedAt: &newPub, FetchedAt: now},
		Doc{URL: "https://c/nopub", Domain: "c", Title: "timewindow nopub", Body: "timewindow unknown",
			PublishedAt: nil, FetchedAt: newPub},
	)

	// 无时间过滤：3 条
	if _, total, err := idx.Search(Query{Q: "timewindow", Size: 10}); err != nil || total != 3 {
		t.Fatalf("without filter total=%d err=%v want 3", total, err)
	}

	// 1 天窗口：旧文必须被排除；无发布时间的按收录时间放行
	hits, total, err := idx.Search(Query{Q: "timewindow", SinceSec: 86400, Size: 10})
	if err != nil {
		t.Fatal(err)
	}
	if total != 2 {
		t.Fatalf("1-day window total=%d want 2 (old published must be excluded)", total)
	}
	for _, h := range hits {
		if h.URL == "https://a/old" {
			t.Fatal("two-year-old article leaked into 1-day window")
		}
		if h.URL == "https://c/nopub" && !h.TimeByFetch {
			t.Fatal("doc without published_at must be marked time_by_fetch")
		}
	}
}

func TestPrefixFallbackOnZeroHits(t *testing.T) {
	idx := newTestIndex(t)
	mustIndex(t, idx, Doc{URL: "https://k8s/doc", Domain: "k8s",
		Title: "CrashLoopBackOff 排查", Body: "pod restart", FetchedAt: time.Now().Unix()})

	if _, total, err := idx.Search(Query{Q: "crashloop", Size: 10}); err != nil || total != 1 {
		t.Fatalf("prefix fallback total=%d err=%v want 1", total, err)
	}
}

func TestMinShouldThreshold(t *testing.T) {
	idx := newTestIndex(t)
	now := time.Now().Unix()
	mustIndex(t, idx,
		Doc{URL: "https://a/all", Domain: "a", Title: "alpha beta gamma", FetchedAt: now},
		Doc{URL: "https://b/two", Domain: "b", Title: "alpha beta", FetchedAt: now},
		Doc{URL: "https://c/one", Domain: "c", Title: "alpha", FetchedAt: now},
	)
	_, total, err := idx.Search(Query{Q: "alpha beta gamma", Size: 10})
	if err != nil {
		t.Fatal(err)
	}
	// 3 词 → 至少命中 2 个：all 与 two 命中，one 被排除
	if total != 2 {
		t.Fatalf("min-should total=%d want 2", total)
	}
}

func TestLabelExclusion(t *testing.T) {
	idx := newTestIndex(t)
	now := time.Now().Unix()
	mustIndex(t, idx,
		Doc{URL: "https://a/normal", Domain: "a", Title: "label test", FetchedAt: now},
		Doc{URL: "https://b/ai", Domain: "b", Title: "label test ai", Labels: []string{"ai_suspect"}, FetchedAt: now},
	)
	hits, total, err := idx.Search(Query{Q: "label test", Exclude: []string{"ai_suspect"}, Size: 10})
	if err != nil {
		t.Fatal(err)
	}
	if total != 1 || len(hits) != 1 || hits[0].URL != "https://a/normal" {
		t.Fatalf("label exclusion failed: total=%d hits=%+v", total, hits)
	}
}

func TestTitleFieldBoost(t *testing.T) {
	idx := newTestIndex(t)
	now := time.Now().Unix()
	mustIndex(t, idx,
		Doc{URL: "https://a/body", Domain: "a", Title: "unrelated title", Body: "gmp scheduler internals", FetchedAt: now},
		Doc{URL: "https://b/title", Domain: "b", Title: "gmp scheduler", Body: "short note", FetchedAt: now},
	)
	hits, _, err := idx.Search(Query{Q: "gmp scheduler", Size: 10})
	if err != nil {
		t.Fatal(err)
	}
	if len(hits) < 2 || hits[0].URL != "https://b/title" {
		t.Fatalf("title-weighted doc should rank first: %+v", hits)
	}
}

// 同一页面的锚点变体必须合并成一条：真实语料里 InfoQ 的 /content-center/
// 与 #about/#intro/#project/#partner/#contactus 曾各自成为独立文档。
func TestAnchorVariantsCollapseToOneDoc(t *testing.T) {
	idx := newTestIndex(t)
	now := time.Now().Unix()
	urls := []string{
		"https://www.infoq.cn/content-center/",
		"https://www.infoq.cn/content-center/#about",
		"https://www.infoq.cn/content-center/#intro",
		"https://www.infoq.cn/content-center/#project",
	}
	for _, u := range urls {
		mustIndex(t, idx, Doc{URL: u, Domain: "www.infoq.cn", Title: "InfoQ 内容中心",
			Body: "content center page with enough text", FetchedAt: now})
	}
	hits, total, err := idx.Search(Query{Q: "content", Size: 10})
	if err != nil {
		t.Fatal(err)
	}
	if total != 1 {
		t.Fatalf("anchor variants should collapse to one document, total=%d hits=%d", total, len(hits))
	}
	if hits[0].URL == "" || !strings.Contains(hits[0].URL, "infoq.cn") {
		t.Fatalf("hit URL should stay a usable address: %q", hits[0].URL)
	}
}
