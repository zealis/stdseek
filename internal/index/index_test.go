package index

import (
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/blevesearch/bleve/v2/search/query"
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
	// 只要求命中 1 个词：三篇都含 alpha，全部召回（排序负责把更匹配的排前面）
	if total != 3 {
		t.Fatalf("min-should total=%d want 3", total)
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

// cjk 分析器按 bigram 切分中文：「术语表」索引为 术语/语表，查询「术语」应命中。
// standard analyzer 会把没有空格的中文整段当成一个 token，导致这类查询 0 结果。
func TestChineseBigramMatching(t *testing.T) {
	idx := newTestIndex(t)
	now := time.Now().Unix()
	mustIndex(t, idx, Doc{
		URL: "https://vuejs.org/glossary/", Domain: "vuejs.org", Title: "Vue 术语表",
		Description: "前端框架常用术语解释", Body: "响应式 渲染 组件 术语 说明 依赖 注入", FetchedAt: now,
	})
	cases := []string{"术语表", "术语", "响应式"}
	for _, q := range cases {
		_, total, err := idx.Search(Query{Q: q, Size: 5})
		if err != nil {
			t.Fatalf("%s: %v", q, err)
		}
		if total != 1 {
			t.Fatalf("query %q should hit the doc, total=%d", q, total)
		}
	}
	// 不相关的中文查询不应命中
	if _, total, _ := idx.Search(Query{Q: "量子计算", Size: 5}); total != 0 {
		t.Fatalf("unrelated query should not hit, total=%d", total)
	}
}

// 算子必须按「词」判断：中文段（哪怕和中英混杂）走 OR，拉丁词走 AND。
// 旧实现按「整个查询串有没有空格」判断，导致「AI 缓存需要知道什么」里的
// 中文段被当成一个 AND 词——要求「道什」「什么」都出现，整段失配。
func TestOpPerToken(t *testing.T) {
	cases := []struct {
		tok  string
		want query.MatchQueryOperator
	}{
		{"缓存需要知道什么", query.MatchQueryOperatorOr},              // 纯中文长句
		{"缓存", query.MatchQueryOperatorOr},                    // 中文短词
		{"AI缓存", query.MatchQueryOperatorOr},                  // 混写，仍按汉字处理
		{"docker", query.MatchQueryOperatorAnd},               // 拉丁主题词
		{"buildx", query.MatchQueryOperatorAnd},               // 拉丁子命令
		{"response_addtrailers", query.MatchQueryOperatorAnd}, // 下划线标识符
	}
	for _, c := range cases {
		if got := opForToken(c.tok); got != c.want {
			t.Errorf("opForToken(%q)=%v want %v", c.tok, got, c.want)
		}
	}
}

// 只由虚词组成的拉丁查询不能把「必需项」变成无约束：
// 「xyzzy nonexistent topic」曾靠 topic 命中 13 条，噪音闸门也用不上。
func TestStopwordOnlyLatinQueryDoesNotRelax(t *testing.T) {
	idx := newTestIndex(t)
	now := time.Now().Unix()
	mustIndex(t, idx, Doc{URL: "https://example.com/topic", Domain: "example.com",
		Title: "Topic", Body: "some topic text here", FetchedAt: now})

	hits, _, err := idx.Search(Query{Q: "xyzzy nonexistent topic", Page: 1, Size: 10})
	if err != nil {
		t.Fatal(err)
	}
	// topic 是虚词，被排除在必需子句外；xyzzy/nonexistent 无命中 → 不应召回该页
	if len(hits) != 0 {
		t.Fatalf("仅虚词命中的查询不应召回结果，实际 %d 条，top=%s", len(hits), hits[0].URL)
	}
}

// 语言边界红线：纯中文查询不得召回英文文档。
// 这条边界此前靠 cjk bigram「天然成立」——中文查询打不到英文正文；
// 一旦引入语义召回就会失效，所以这里用显式硬过滤兜住，且不允许关闭。
// 为了让英文文档也能被字面命中（否则测不到过滤本身），它的正文里放了中文词。
func TestChineseQueryFiltersOutEnglishDocs(t *testing.T) {
	idx := newTestIndex(t)
	now := time.Now().Unix()
	mustIndex(t, idx,
		Doc{URL: "https://zh.example/a", Domain: "zh.example", Title: "单元测试实践",
			Description: "中文文章", Body: "单元测试 覆盖率 断言 mock", Lang: "zh", FetchedAt: now},
		Doc{URL: "https://en.example/b", Domain: "en.example", Title: "Unit Testing",
			Description: "英文文章", Body: "单元测试 unit testing coverage assert mock",
			Lang: "en", FetchedAt: now},
	)

	hits, _, err := idx.Search(Query{Q: "单元测试", Page: 1, Size: 10})
	if err != nil {
		t.Fatal(err)
	}
	for _, h := range hits {
		if strings.Contains(h.URL, "en.example") {
			t.Fatalf("纯中文查询召回了英文文档: %s", h.URL)
		}
	}
	if len(hits) == 0 {
		t.Fatal("中文文档应正常命中")
	}
}

// 中英混杂查询不受边界限制：这是「术语桥」的主要形态，
// 收紧它会让「官方英文文档 + 中文修饰词」的用法受损。
func TestMixedQueryBypassesLanguageFilter(t *testing.T) {
	idx := newTestIndex(t)
	now := time.Now().Unix()
	mustIndex(t, idx, Doc{URL: "https://en.example/manual", Domain: "en.example",
		Title: "PHP: Namespaces - Manual", Description: "php namespaces reference",
		Body: "php manual namespaces overview declaring namespaces", Lang: "en", FetchedAt: now})

	// 「PHP 命名空间」是混合查询，必须能召回英文手册（靠术语扩展命中）
	hits, _, err := idx.Search(Query{Q: "PHP 命名空间", Page: 1, Size: 10})
	if err != nil {
		t.Fatal(err)
	}
	if len(hits) == 0 || !strings.Contains(hits[0].URL, "en.example") {
		t.Fatalf("混合查询应能召回英文文档，实际 %d 条", len(hits))
	}
}
