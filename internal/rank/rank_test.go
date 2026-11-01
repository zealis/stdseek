package rank

import (
	"math"
	"strings"
	"testing"
	"time"

	"github.com/zealis/stdseek/internal/index"
)

func fixedNow() time.Time { return time.Date(2026, 10, 23, 0, 0, 0, 0, time.UTC) }

func daysAgo(d int) int64 { return fixedNow().Add(-time.Duration(d) * 24 * time.Hour).Unix() }

func reasonContains(rs []string, sub string) bool {
	for _, r := range rs {
		if strings.Contains(r, sub) {
			return true
		}
	}
	return false
}

func TestExactMatchBeatsHigherBM25(t *testing.T) {
	r := New(DefaultWeights())
	hits := []index.Hit{
		{URL: "https://a.example/1", Domain: "a.example", Title: "容器编排实践", Score: 3.0,
			SortTime: daysAgo(3)},
		{URL: "https://b.example/2", Domain: "b.example", Title: "Kubernetes CrashLoopBackOff 排查", Score: 2.0,
			SortTime: daysAgo(3)},
	}
	out := r.Rank("crashloopbackoff", hits, Signals{}, fixedNow())
	if out[0].Hit.URL != "https://b.example/2" {
		t.Fatalf("exact title match should rank first, got %s", out[0].Hit.URL)
	}
	if !reasonContains(out[0].Reasons, "标题精确匹配") {
		t.Fatalf("missing exact-match reason: %v", out[0].Reasons)
	}
}

func TestFreshnessOrdering(t *testing.T) {
	r := New(DefaultWeights())
	hits := []index.Hit{
		{URL: "https://old.example/1", Domain: "old.example", Title: "go 调度器", Score: 2.0,
			SortTime: daysAgo(365)},
		{URL: "https://new.example/1", Domain: "new.example", Title: "go 调度器", Score: 2.0,
			SortTime: daysAgo(1)},
	}
	out := r.Rank("go 调度器", hits, Signals{}, fixedNow())
	if out[0].Hit.URL != "https://new.example/1" {
		t.Fatalf("newer doc should rank first: %s", out[0].Hit.URL)
	}
	if !reasonContains(out[0].Reasons, "发布时间较新") {
		t.Fatalf("missing freshness reason: %v", out[0].Reasons)
	}
}

func TestAISuspectDoesNotPenalize(t *testing.T) {
	r := New(DefaultWeights())
	base := index.Hit{URL: "https://x.example/1", Domain: "x.example", Title: "缓存设计",
		Score: 2.0, SortTime: daysAgo(5)}
	tagged := base
	tagged.URL = "https://y.example/2"
	tagged.Domain = "y.example"
	tagged.Labels = []string{"ai_suspect"}

	out := r.Rank("缓存设计", []index.Hit{base, tagged}, Signals{}, fixedNow())
	if len(out) != 2 {
		t.Fatalf("want 2 results, got %d", len(out))
	}
	// 两个文档除标签外完全一致：分数必须相同（ai_suspect 不降权）
	if out[0].Score != out[1].Score {
		t.Fatalf("ai_suspect must not affect score: %v vs %v", out[0].Score, out[1].Score)
	}
	var taggedRes Result
	for _, res := range out {
		if res.Hit.URL == "https://y.example/2" {
			taggedRes = res
		}
	}
	if !reasonContains(taggedRes.Reasons, "不参与降权") {
		t.Fatalf("ai_suspect must be explained as filter-only: %v", taggedRes.Reasons)
	}
}

func TestDomainDiversityDecay(t *testing.T) {
	r := New(DefaultWeights())
	hits := []index.Hit{
		{URL: "https://same.example/1", Domain: "same.example", Title: "go 并发 a", Score: 3.0, SortTime: daysAgo(2)},
		{URL: "https://same.example/2", Domain: "same.example", Title: "go 并发 b", Score: 2.9, SortTime: daysAgo(2)},
		{URL: "https://other.example/1", Domain: "other.example", Title: "go 并发 c", Score: 1.5, SortTime: daysAgo(2)},
	}
	out := r.Rank("go 并发", hits, Signals{}, fixedNow())
	if out[1].Hit.Domain != "other.example" {
		t.Fatalf("second same-domain result should be pushed down, got %s", out[1].Hit.Domain)
	}
	if !reasonContains(out[2].Reasons, "多样性下调") {
		t.Fatalf("missing diversity reason on third result: %v", out[2].Reasons)
	}
}

// 裸域与 www 必须算同一个站：抓取会留下两条 host（ruanyifeng.com 1 篇 +
// www.ruanyifeng.com 24 篇），按原始 host 分组会让同站内容各享一份多样性额度。
func TestDomainDiversityTreatsWWWAsSameSite(t *testing.T) {
	r := New(DefaultWeights())
	hits := []index.Hit{
		{URL: "https://example.com/1", Domain: "example.com", Title: "go 并发 a", Score: 3.0, SortTime: daysAgo(2)},
		{URL: "https://www.example.com/2", Domain: "www.example.com", Title: "go 并发 b", Score: 2.9, SortTime: daysAgo(2)},
		{URL: "https://other.example/1", Domain: "other.example", Title: "go 并发 c", Score: 1.5, SortTime: daysAgo(2)},
	}
	out := r.Rank("go 并发", hits, Signals{}, fixedNow())
	if out[1].Hit.Domain != "other.example" {
		t.Fatalf("www 变体应与裸域同组被衰减，实际第二名是 %s", out[1].Hit.Domain)
	}
	if out[2].Breakdown.Diversity >= 1 {
		t.Fatalf("www 变体的多样性系数应小于 1，实际 %v", out[2].Breakdown.Diversity)
	}
}

func TestTimeByFetchIsExplained(t *testing.T) {
	r := New(DefaultWeights())
	hits := []index.Hit{
		{URL: "https://n.example/1", Domain: "n.example", Title: "无发布时间", Score: 1.0,
			SortTime: daysAgo(2), TimeByFetch: true},
	}
	out := r.Rank("无发布时间", hits, Signals{}, fixedNow())
	if !reasonContains(out[0].Reasons, "按收录时间") {
		t.Fatalf("time_by_fetch must be disclosed: %v", out[0].Reasons)
	}
}

func TestProsperitySignalRanksFirst(t *testing.T) {
	r := New(DefaultWeights())
	hits := []index.Hit{
		{URL: "https://low.example/1", Domain: "low.example", Title: "go 调度器", Score: 2.0, SortTime: daysAgo(2)},
		{URL: "https://high.example/1", Domain: "high.example", Title: "go 调度器", Score: 2.0, SortTime: daysAgo(2)},
	}
	sig := Signals{Prosperity: map[string]float64{"high.example": 200000, "low.example": 10}}
	out := r.Rank("go 调度器", hits, sig, fixedNow())
	if out[0].Hit.Domain != "high.example" {
		t.Fatalf("high prosperity should rank first: %s", out[0].Hit.Domain)
	}
	if !reasonContains(out[0].Reasons, "繁荣度") {
		t.Fatalf("missing prosperity reason: %v", out[0].Reasons)
	}
}

// TestQualityOnlyRewardsRelevant 质量分只在"相关"的前提下兑现。
//
// 场景取自真实语料：PostgreSQL About 页只顺带提过一次 Rust（BM25 0.005），
// 但因为是官方站且新，靠质量分排到过第二名。
//
// 旧公式 base = (relPart + qualPart)·(0.25 + 0.75·rel) 把质量项最多只压到 25%，
// 压不住这种"不相关但很体面"的页面；改成 base = relPart + qualPart·rel 后，
// 完全不相关（rel=0）的文档拿不到任何质量加成。
// 该改动实测：337 篇术语桥 NDCG@10 0.482 → 0.541，纯中文 0.821 → 0.827。
func TestQualityOnlyRewardsRelevant(t *testing.T) {
	r := New(DefaultWeights())
	hits := []index.Hit{
		// 相关但默默无闻：标题精确命中，个人博客、旧、繁荣度低
		{URL: "https://blog.example/rust", Domain: "blog.example", Title: "rust 所有权",
			Score: 0.5, SortTime: daysAgo(400)},
		// 不相关但体面：正文只顺带提过一次，官方站、新、繁荣度高
		{URL: "https://official.example/about", Domain: "official.example", Title: "关于我们",
			Description: "本站偶尔提到 rust", Score: 0.005, SortTime: daysAgo(1)},
	}
	sig := Signals{
		Categories: map[string]string{"official.example": "official", "blog.example": "blog"},
		Prosperity: map[string]float64{"official.example": 500000, "blog.example": 10},
	}
	out := r.Rank("rust 所有权", hits, sig, fixedNow())
	if len(out) < 2 {
		t.Fatalf("expected both hits, got %d", len(out))
	}
	if out[0].Hit.URL != "https://blog.example/rust" {
		t.Fatalf("相关的朴素页面应排在体面但不相关的页面之前，实际 top1=%s（分数 %0.3f vs %0.3f）",
			out[0].Hit.URL, out[0].Score, out[1].Score)
	}
}

// TestQualityBreaksTiesAmongRelevant 质量分在相关性相当时仍然要起作用——
// 上一条测试只证明"不相关不给加成"，这条防止它被理解成"质量分作废"。
func TestQualityBreaksTiesAmongRelevant(t *testing.T) {
	r := New(DefaultWeights())
	hits := []index.Hit{
		{URL: "https://blog.example/1", Domain: "blog.example", Title: "go 调度器", Score: 2.0, SortTime: daysAgo(2)},
		{URL: "https://go.dev/1", Domain: "go.dev", Title: "go 调度器", Score: 2.0, SortTime: daysAgo(2)},
	}
	sig := Signals{
		Categories: map[string]string{"go.dev": "official", "blog.example": "blog"},
		Prosperity: map[string]float64{"go.dev": 200000, "blog.example": 200000},
	}
	out := r.Rank("go 调度器", hits, sig, fixedNow())
	if out[0].Hit.Domain != "go.dev" {
		t.Fatalf("相关性相同时官方站应靠质量分胜出，实际 top1=%s", out[0].Hit.Domain)
	}
}

// TestExactMatchCrossLangOnlyForMixedQuery 术语变体只能用于中英混杂查询。
//
// 两个方向都必须锁住：
//  1. 「Rust 社区」这种混杂查询，标题是 Community 时应当得分（词典里社区→community），
//     否则目标页 rust-lang.org/community/ 拿 0 分，被同站首页靠品牌词压过（实测首相关第 8）。
//  2. 纯中文「社区」查询不能靠 Community 得分——那是跨语言，红线 9 不允许；
//     实测无条件启用时纯中文组 NDCG@10 0.834→0.816。
func TestExactMatchCrossLangOnlyForMixedQuery(t *testing.T) {
	// 混杂查询：中文词走英文变体命中
	if got := exactMatch("rust 社区", []string{"rust", "社区"}, "Community", "", "rust-lang.org"); got <= 0 {
		t.Errorf("混杂查询「rust 社区」应命中标题 Community，实际 %.3f", got)
	}
	// 纯中文查询：不许跨语言
	if got := exactMatch("社区", []string{"社区"}, "Community", "", "rust-lang.org"); got != 0 {
		t.Errorf("纯中文查询「社区」不应靠 Community 得分（红线 9），实际 %.3f", got)
	}
	// 中文字面命中仍然有效（不受影响）
	if got := exactMatch("社区", []string{"社区"}, "Rust 社区", "", "rust-lang.org"); got <= 0 {
		t.Errorf("中文字面命中应得分，实际 %.3f", got)
	}
}

// TestBrandTokenInDomainIgnored 品牌词已体现在域名里时不计入精确匹配。
//
// 真实案例「nginx 安全公告」：目标页 nginx.org/en/security_advisories.html 标题是
// Security Advisories（命中「安全公告」→security），首页 nginx.org/ 标题只有 nginx。
// 旧算法里 nginx 权重 2、安全公告权重 1 → 首页 Exact 0.4 > 目标页 0.2，
// 首页第 1、目标页第 7。域名已限定站点后，标题里的品牌词不含区分度。
func TestBrandTokenInDomainIgnored(t *testing.T) {
	toks := []string{"nginx", "安全公告"}
	// 域名含品牌词 → nginx 被跳过，只剩「安全公告」→security 命中标题
	target := exactMatch("nginx 安全公告", toks, "Security Advisories", "", "nginx.org")
	// 同上，但标题只有品牌名 → 一个词都不命中
	home := exactMatch("nginx 安全公告", toks, "nginx", "", "nginx.org")
	if home != 0 {
		t.Errorf("首页标题只有品牌词，应得 0（品牌词被跳过），实际 %.3f", home)
	}
	if target <= home {
		t.Errorf("目标页 %.3f 应高于首页 %.3f", target, home)
	}
	// 品牌词不属于该域名时仍然计分（换站点搜索时它是有意义的主题词）
	other := exactMatch("nginx 安全公告", toks, "nginx 入门", "", "example.com")
	if other <= 0 {
		t.Errorf("跨站点时品牌词应计分，实际 %.3f", other)
	}
}

// TestNoiseGateUnaffectedByBrandSkip 噪音闸门必须用「不跳品牌词」的分数。
//
// 闸门问的是「有没有任何词面匹配」；若用跳过的分数，品牌词命中会被抹掉，
// 「React 快速开始」这类查询整批被判噪音返回空（实测无结果率 5.1%→10.3%）。
func TestNoiseGateUnaffectedByBrandSkip(t *testing.T) {
	toks := []string{"react", "快速开始"}
	// 排序口径：react 被域名跳过，标题只有 React → 0 分
	if v := exactMatch("react 快速开始", toks, "React", "", "react.dev"); v != 0 {
		t.Errorf("排序口径应跳过品牌词，实际 %.3f", v)
	}
	// 闸门口径：品牌词命中仍算有交集
	if v := exactMatchFull("react 快速开始", toks, "React", ""); v <= 0 {
		t.Errorf("闸门口径应保留品牌词命中，实际 %.3f", v)
	}
}

// TestTermFormVariants 术语形态变体：同一概念在真实标题里的不同写法都要能匹配。
//
// 真实案例：「React 快速开始」目标页 react.dev/learn 标题是 "Quick Start – React"，
// 词典值 quickstart（无空格）字面匹配不上，该查询 NDCG 一直是 0。
// 连字符词自动展开（getting-started → getting started → gettingstarted），
// 另有 enAltForms 补充个别无分隔写法（quickstart → quick start）。
func TestTermFormVariants(t *testing.T) {
	// quickstart 应生成带空格形态
	forms := index.TermVariants("快速开始")
	want := map[string]bool{"quick start": false, "quickstart": false}
	for _, f := range forms {
		if _, ok := want[f]; ok {
			want[f] = true
		}
	}
	for k, hit := range want {
		if !hit {
			t.Errorf("「快速开始」应包含形态 %q，实际 %v", k, forms)
		}
	}
	// 连字符词两种形态都要有
	gs := index.TermVariants("入门")
	if !containsStr(gs, "getting-started") || !containsStr(gs, "getting started") {
		t.Errorf("「入门」应同时含连字符与空格形态，实际 %v", gs)
	}
	// 纯英文 token 不产生变体
	if v := index.TermVariants("react"); len(v) != 0 {
		t.Errorf("英文 token 不应有变体，实际 %v", v)
	}
}

func containsStr(xs []string, s string) bool {
	for _, x := range xs {
		if x == s {
			return true
		}
	}
	return false
}

func TestQualityByCategory(t *testing.T) {
	r := New(DefaultWeights())
	hits := []index.Hit{
		{URL: "https://blog.example/1", Domain: "blog.example", Title: "go 教程", Score: 1.0, SortTime: daysAgo(1)},
		{URL: "https://go.dev/1", Domain: "go.dev", Title: "go 教程", Score: 1.0, SortTime: daysAgo(1)},
	}
	out := r.Rank("go 教程", hits, Signals{Categories: map[string]string{"go.dev": "official", "blog.example": "blog"}}, fixedNow())
	if out[0].Hit.Domain != "go.dev" {
		t.Fatalf("official category should outrank blog: %s", out[0].Hit.Domain)
	}
	if !reasonContains(out[0].Reasons, "官方文档") {
		t.Fatalf("missing official-doc reason: %v", out[0].Reasons)
	}
}

// 整批候选的原始 BM25 都极低、且没有任何标题/摘要精确匹配时判为纯噪音。
// 真实案例："xyzzy nonexistent topic" 曾返回 13 条，top1 是 Go 文档（BM25 0.0028）。
func TestRankDropsPureNoiseQuery(t *testing.T) {
	r := New(DefaultWeights())
	hits := []index.Hit{
		{URL: "https://go.dev/doc/", Domain: "go.dev", Title: "Documentation", Score: 0.0028, SortTime: daysAgo(1)},
		{URL: "https://go.dev/community/", Domain: "go.dev", Title: "Community", Score: 0.0025, SortTime: daysAgo(1)},
	}
	if out := r.Rank("xyzzy nonexistent topic", hits, Signals{}, fixedNow()); len(out) != 0 {
		t.Fatalf("纯噪音查询应返回空，实际 %d 条", len(out))
	}
}

// 高频专有名词的 IDF 低，单靠它命中的 BM25 天然偏低——不能因此判为噪音。
// 真实案例："Vue 术语表" 被单用 BM25 阈值误杀，而它原本 Recall 1.00。
func TestRankKeepsLowBM25WhenTitleMatches(t *testing.T) {
	r := New(DefaultWeights())
	hits := []index.Hit{
		{URL: "https://vuejs.org/glossary/", Domain: "vuejs.org", Title: "Vue Glossary", Score: 0.05, SortTime: daysAgo(1)},
	}
	out := r.Rank("Vue 术语表", hits, Signals{}, fixedNow())
	if len(out) == 0 {
		t.Fatal("标题含主题词的低 BM25 结果不应被判为噪音")
	}
}

// 时效意图识别：「最新一期」这类查询词面无法表达时间，只能靠排序抬升时间维度。
func TestHasFreshnessIntent(t *testing.T) {
	yes := []string{"科技爱好者周刊最新一期", "最近的 AI 论文", "latest release notes", "newest rust version"}
	for _, q := range yes {
		if !HasFreshnessIntent(q) {
			t.Errorf("%q 应识别为时效意图", q)
		}
	}
	// 单字「新」太宽：「新特性」「新版本文档」是在讲某个版本，不是要最新内容
	no := []string{"Vue 新特性", "docker 新版本文档", "鸿蒙性能优化", "单元测试"}
	for _, q := range no {
		if HasFreshnessIntent(q) {
			t.Errorf("%q 不应识别为时效意图", q)
		}
	}
}

// 时效意图必须能改变排序结果，而不只是让分数好看一点。
// 构造：旧文档相关性更高（BM25 1.0 vs 0.4）、但不算太旧（30 天 vs 3 天）。
// 普通查询旧文档胜出（相关性主导）；带「最新」时新文档反超（时间主导）。
//
// 时间跨度为什么从原来的 120天/10天 收窄到 30天/3天：
// 半衰期 30 天，120 天前的文档时效分只剩 0.5^4=0.0625，等于"时效信号几乎归零"，
// 那种构造下只要时效权重略抬高就会翻盘，把「相关性主导」这个设计意图
// 固化成了「任何情况下旧文档都必须赢」，与产品实际不符（周刊类系列内容、
// 同标题不同期，用户本来就期望新的在前）。收窄到 30天/3天后，
// 这个用例只验证它真正该验证的东西：时效意图能否翻转排序。
func TestFreshnessIntentFlipsOrdering(t *testing.T) {
	r := New(DefaultWeights())
	hits := []index.Hit{
		{URL: "https://same.example/old", Domain: "same.example", Title: "科技爱好者周刊",
			Score: 1.0, SortTime: daysAgo(30)},
		{URL: "https://same.example/new", Domain: "same.example", Title: "科技爱好者周刊",
			Score: 0.4, SortTime: daysAgo(3)},
	}

	top := func(q string) string {
		out := r.Rank(q, hits, Signals{}, fixedNow())
		if len(out) != 2 {
			t.Fatalf("%q: 结果数=%d", q, len(out))
		}
		return out[0].Hit.URL
	}

	if got := top("科技爱好者周刊"); got != "https://same.example/old" {
		t.Fatalf("普通查询应由相关性更高的旧文档胜出，实际 top1=%s", got)
	}
	if got := top("科技爱好者周刊最新一期"); got != "https://same.example/new" {
		t.Fatalf("时效意图下应由新文档胜出，实际 top1=%s", got)
	}
	// 时效意图不进单条理由：它是查询级信息，由结果页统一提示
	for _, res := range r.Rank("科技爱好者周刊最新一期", hits, Signals{}, fixedNow()) {
		if reasonContains(res.Reasons, "时效意图") {
			t.Fatal("时效意图不应作为单条结果的理由")
		}
	}
}

// 深网发现：与查询相关、但反向链接少的内容应排在高链接内容之前。
func TestDiscoverPrefersLowInbound(t *testing.T) {
	r := New(DefaultWeights())
	hits := []index.Hit{
		{URL: "https://hot.example/popular", Domain: "hot.example", Title: "单元测试实践",
			Description: "热门文章", Score: 1.0, SortTime: daysAgo(1)},
		{URL: "https://quiet.example/buried", Domain: "quiet.example", Title: "单元测试的几个坑",
			Description: "冷门文章", Score: 0.8, SortTime: daysAgo(2)},
	}
	sig := Signals{
		Categories: map[string]string{"hot.example": "platform", "quiet.example": "blog"},
		Inbound:    map[string]int{"https://hot.example/popular": 25, "https://quiet.example/buried": 0},
	}
	out := r.Discover("单元测试", hits, sig, fixedNow(), map[string]bool{"https://hot.example/popular": true}, 5)
	if len(out) == 0 {
		t.Fatal("发现区不应为空")
	}
	if out[0].Hit.URL != "https://quiet.example/buried" {
		t.Fatalf("低反向链接的冷门内容应排第一，实际 %s", out[0].Hit.URL)
	}
	// 理由必须可解释：说明为什么它在这里
	if !reasonContains(out[0].Reasons, "无人引用") && !reasonContains(out[0].Reasons, "反向链接较少") {
		t.Fatalf("缺少冷热解释理由: %v", out[0].Reasons)
	}
}

// 主结果已展示的 URL 必须排除，否则发现区会变成主结果的复制品。
func TestDiscoverExcludesShown(t *testing.T) {
	r := New(DefaultWeights())
	hits := []index.Hit{
		{URL: "https://a.example/one", Domain: "a.example", Title: "单元测试", Score: 1.0, SortTime: daysAgo(1)},
	}
	exclude := map[string]bool{"https://a.example/one": true}
	if out := r.Discover("单元测试", hits, Signals{}, fixedNow(), exclude, 5); len(out) != 0 {
		t.Fatalf("主结果已展示的 URL 不应再进发现区，实际 %d 条", len(out))
	}
}

// 整批噪音判定：候选批最高 BM25 都低于阈值时，整个查询不出发现区。
// 注意判据是「整批」而不是「单条」——逐条卡绝对值会把 BM25 天然偏低的
// 被埋住内容全部滤掉，那个区就没东西可捞了。
func TestDiscoverRejectsIrrelevant(t *testing.T) {
	r := New(DefaultWeights())
	hits := []index.Hit{
		{URL: "https://a.example/noise", Domain: "a.example", Title: "完全无关的页面",
			Description: "随便说说", Score: 0.02, SortTime: daysAgo(1)},
	}
	if out := r.Discover("单元测试", hits, Signals{}, fixedNow(), nil, 5); len(out) != 0 {
		t.Fatalf("噪音候选不应进发现区，实际 %d 条", len(out))
	}
}

// TestMinBM25ForScalesWithCorpus 噪音闸门随语料规模缩放。
//
// 事故：阈值写死 0.1，而 BM25 分值由 IDF 主导、随文档数变化。
// 2 篇内网文档时「回滚」只有 0.073、「工单」0.068，全部被判成噪音——
// 小语料部署表现为"什么都搜不到"。企业内网导入几十到几百篇正好落在这个区间。
func TestMinBM25ForScalesWithCorpus(t *testing.T) {
	// 未知规模：退回基准
	if got := minBM25For(0); math.Abs(got-minBM25Base) > 1e-9 {
		t.Errorf("minBM25For(0)=%v want %v", got, minBM25Base)
	}
	// 极小语料：1~2 篇被下限夹到 0.2 倍（实测 2 篇时 BM25 只有 0.07 量级，
	// 基准 0.1 会把「回滚」这种真实命中判成噪音）
	if got := minBM25For(2); math.Abs(got-minBM25Base*0.2) > 1e-9 {
		t.Errorf("minBM25For(2)=%v want %v", got, minBM25Base*0.2)
	}
	// 10 篇：log(10)/log(1000)=0.333，未触下限
	if got := minBM25For(10); math.Abs(got-minBM25Base*0.3333) > 1e-3 {
		t.Errorf("minBM25For(10)=%v want ≈%v", got, minBM25Base*0.3333)
	}
	// 千篇及以上：等于基准阈值（评测基线一分数不变的前提）
	for _, docs := range []int{1000, 100000} {
		if got := minBM25For(docs); math.Abs(got-minBM25Base) > 1e-9 {
			t.Errorf("minBM25For(%d)=%v want %v", docs, got, minBM25Base)
		}
	}
	// 单调不降：文档越多阈值越高（夹取区间内）
	if minBM25For(50) > minBM25For(200) || minBM25For(200) > minBM25For(800) {
		t.Error("阈值应随语料规模单调不降")
	}
}

// TestRankNoiseGateSmallCorpus 同一条弱命中，在小语料下放行、在千篇语料下仍被挡。
func TestRankNoiseGateSmallCorpus(t *testing.T) {
	r := New(DefaultWeights())
	now := time.Now()
	hits := []index.Hit{{
		URL: "https://wiki.internal/deploy/checklist", Domain: "wiki.internal",
		Title: "上线检查清单", Description: "发布前的必查项", Score: 0.073,
	}}
	// 2 篇：阈值 0.02，放行
	if got := r.Rank("回滚", hits, Signals{CorpusDocs: 2}, now); len(got) != 1 {
		t.Fatalf("小语料应放行弱命中，实际返回 %d 条", len(got))
	}
	// 千篇：阈值 0.1，同一条被判噪音（与修复前行为一致，保证基线不变）
	if got := r.Rank("回滚", hits, Signals{CorpusDocs: 1200}, now); len(got) != 0 {
		t.Fatalf("千篇语料上 0.073 应被判噪音，实际返回 %d 条", len(got))
	}
}

// 域名时间理由：WHOIS 年数 ≥3 才出现（质量分有对应加权），不足不提——
// 加权了却不说明、或没加权却说加分，都是不可解释。
func TestExplainWhoisYears(t *testing.T) {
	mk := func(y int) []string {
		h := index.Hit{Domain: "old.example.com", Labels: []string{}}
		b := Breakdown{Exact: 0.9, Freshness: 0.3, Quality: quality("blog", "d", y)}
		return explain("q", h, b, map[string]string{"old.example.com": "blog"},
			map[string]int{"old.example.com": y}, map[string]int{}, time.Unix(1700000000, 0))
	}
	if r := mk(12); !hasPrefix(r, "域名已注册 12 年") {
		t.Fatalf("12y should explain domain age: %v", r)
	}
	if r := mk(2); hasPrefix(r, "域名已注册") {
		t.Fatalf("2y should not claim age bonus: %v", r)
	}
	// 加权真实落在质量分上：12 年比 0 年高
	if quality("blog", "d", 12) <= quality("blog", "d", 0) {
		t.Fatal("old domain should score higher quality")
	}
}

func hasPrefix(list []string, prefix string) bool {
	for _, x := range list {
		if strings.HasPrefix(x, prefix) {
			return true
		}
	}
	return false
}
