package processor

import (
	"fmt"
	"strings"
	"testing"
	"time"
)

func newTestProcessor(t *testing.T) *Processor {
	t.Helper()
	p, err := New()
	if err != nil {
		t.Fatalf("processor.New: %v", err)
	}
	return p
}

func hasLabel(labels []string, want string) bool {
	for _, l := range labels {
		if l == want {
			return true
		}
	}
	return false
}

func TestProcessExtractsPublishedTimeAndPersonalLabel(t *testing.T) {
	p := newTestProcessor(t)
	html := `<!DOCTYPE html><html><head>
		<title>深入理解 Go 调度器</title>
		<meta name="description" content="GMP 模型解析">
		<meta property="article:published_time" content="2026-08-15T10:30:00+08:00">
		</head><body><article>
		<h1>深入理解 Go 调度器</h1>
		<p>Go 运行时通过 GMP 模型调度 goroutine，本文从源码角度分析调度循环与抢占机制，并给出可复现的压测数据。</p>
		<p>当 P 数量固定时，M 的阻塞会让运行时创建新的 M 来接管本地队列，这就是所谓的手递手调度。</p>
		</article></body></html>`

	res, err := p.Process("https://blog.example.com/go-scheduler", []byte(html), Options{SiteCategory: "blog"})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(res.Title, "调度器") {
		t.Fatalf("title extraction failed: %q", res.Title)
	}
	want, _ := time.Parse(time.RFC3339, "2026-08-15T10:30:00+08:00")
	if res.PublishedAt == nil || *res.PublishedAt != want.Unix() {
		t.Fatalf("published_at not parsed: got %v want %d", res.PublishedAt, want.Unix())
	}
	if res.Simhash == 0 || res.WordCount == 0 || len(res.Keywords) == 0 {
		t.Fatalf("fingerprint empty: simhash=%d words=%d keywords=%v", res.Simhash, res.WordCount, res.Keywords)
	}
	if !hasLabel(res.Labels, LabelPersonal) {
		t.Fatalf("blog category should mark personal: %v", res.Labels)
	}
	if res.LabelReasons[LabelPersonal] == "" {
		t.Fatal("personal label must carry an explanation")
	}
}

func TestUGCPlatformLabel(t *testing.T) {
	p := newTestProcessor(t)
	html := `<html><head><title>某个技术问答</title></head><body>
		<article><p>这是一段回答内容，用来验证平台域名识别逻辑是否生效。</p></article></body></html>`
	res, err := p.Process("https://www.zhihu.com/question/123456/answer/789", []byte(html), Options{})
	if err != nil {
		t.Fatal(err)
	}
	if !hasLabel(res.Labels, LabelUGCPlatform) {
		t.Fatalf("zhihu should be ugc_platform: %v", res.Labels)
	}
	if !strings.Contains(res.LabelReasons[LabelUGCPlatform], "zhihu.com") {
		t.Fatalf("reason should name the platform: %q", res.LabelReasons[LabelUGCPlatform])
	}
}

func TestAdHeavyLabel(t *testing.T) {
	p := newTestProcessor(t)
	var b strings.Builder
	b.WriteString(`<html><head><title>某页面</title></head><body><article><p>正文内容在这里，讨论一个技术问题并给出解决方案。</p></article>`)
	for i := 0; i < 4; i++ {
		fmt.Fprintf(&b, `<div class="ad-slot-%d">广告位</div>`, i)
	}
	b.WriteString(`<a href="https://s.click.taobao.com/xyz">买它</a><a href="https://union-click.jd.com/abc">优惠</a>`)
	b.WriteString(`</body></html>`)

	res, err := p.Process("https://shop.example.com/post/1", []byte(b.String()), Options{})
	if err != nil {
		t.Fatal(err)
	}
	if !hasLabel(res.Labels, LabelAdHeavy) {
		t.Fatalf("ad-heavy page not labeled: %v", res.Labels)
	}
	if !strings.Contains(res.LabelReasons[LabelAdHeavy], "广告位节点") {
		t.Fatalf("reason should include node stats: %q", res.LabelReasons[LabelAdHeavy])
	}
}

func TestAISuspectRequiresMultipleSignals(t *testing.T) {
	p := newTestProcessor(t)

	var body strings.Builder
	body.WriteString("<p>以下是关于缓存设计的介绍。综上所述，缓存能提升性能。</p>")
	// 构造 25 个长度接近的句子，压低句长方差比
	for i := 0; i < 25; i++ {
		fmt.Fprintf(&body, "<p>缓存系统需要考虑失效策略与一致性问题的具体实现方案%d。</p>", i%5)
	}
	html := `<html><head><title>缓存设计指南</title></head><body><article>` + body.String() + `</article></body></html>`

	res, err := p.Process("https://example.com/cache", []byte(html), Options{})
	if err != nil {
		t.Fatal(err)
	}
	if !hasLabel(res.Labels, LabelAISuspect) {
		t.Fatalf("multi-signal page should be ai_suspect: %v reasons=%v", res.Labels, res.LabelReasons)
	}

	// 仅一个套话信号 + 句长自然，不应打标（保守策略，避免误伤独立作者）
	html2 := `<html><head><title>随笔</title></head><body><article>
		<p>总之，这个方案在我的机器上跑得不错。短句。这一句稍微长一点，用来打乱方差，让句式看起来更像人类写作。</p>
		</article></body></html>`
	res2, err := p.Process("https://example.com/note", []byte(html2), Options{})
	if err != nil {
		t.Fatal(err)
	}
	if hasLabel(res2.Labels, LabelAISuspect) {
		t.Fatalf("single weak signal must not label: %v", res2.LabelReasons)
	}
}

func TestSimhashNearDuplicate(t *testing.T) {
	// simhash 的汉明距离阈值针对长文档；短文本一条词差异就会翻转较多位，
	// 因此这里用接近真实正文长度的词序列做近重复判定。
	base := []string{"go", "调度器", "源码", "分析", "抢占", "机制", "压测", "goroutine",
		"本地队列", "工作窃取", "自旋", "系统调用", "阻塞", "网络轮询", "垃圾回收", "写屏障",
		"逃逸分析", "栈扩容", "内存分配", "对象头", "性能", "火焰图", "pprof", "基准测试",
		"延迟", "吞吐", "并发", "锁", "原子操作", "内存模型"}
	similar := append([]string{}, base...)
	similar[5] = "数据" // 只改一个词
	other := []string{"红烧肉", "做法", "五花肉", "冰糖", "小火", "慢炖", "收汁", "八角",
		"桂皮", "香叶", "老抽", "生抽", "料酒", "姜片", "葱段", "翻炒", "上色", "砂锅",
		"两小时", "软糯", "肥而不腻", "家常菜", "下饭", "厨房", "新手", "教程", "步骤",
		"食材", "调味", "火候"}
	a := Simhash(base, nil)
	b := Simhash(similar, nil)
	c := Simhash(other, nil)
	if !NearDuplicate(a, b) {
		t.Fatalf("similar docs should be near duplicate: %d", Hamming(a, b))
	}
	if NearDuplicate(a, c) {
		t.Fatalf("unrelated docs must not be near duplicate: %d", Hamming(a, c))
	}
}

func TestLinkDensity(t *testing.T) {
	// 列表页：整段正文都是链接文字
	var list strings.Builder
	list.WriteString(`<div class="content">`)
	for i := 0; i < 10; i++ {
		list.WriteString(`<p><a href="/post/` + fmt.Sprint(i) + `">这是第几篇博客文章的标题</a></p>`)
	}
	list.WriteString(`</div>`)
	if got := linkDensity(list.String()); got < 0.8 {
		t.Fatalf("list page density too low: %.2f", got)
	}

	// 文章页：长段落正文，只有少量行内链接
	article := `<div class="content"><p>` + strings.Repeat("这段话是真正的正文内容，讨论实现细节与权衡。", 30) +
		`详细见<a href="/ref">参考资料</a>。` + strings.Repeat("继续深入说明背景与结论。", 30) + `</p></div>`
	if got := linkDensity(article); got > 0.1 {
		t.Fatalf("article density too high: %.2f", got)
	}

	if got := linkDensity(""); got != 0 {
		t.Fatalf("empty content density=%v", got)
	}
}

// 真实阈值校准：列表页 > 0.4（会被过滤），文章页远低于阈值。
func TestLinkDensityThresholdSeparation(t *testing.T) {
	list := `<ul>` + strings.Repeat(`<li><a href="/x">文章标题示例</a></li>`, 20) + `</ul>`
	art := `<div><p>` + strings.Repeat("正文内容正文内容正文内容。", 100) + `<a href="/y">链接</a></p></div>`
	if linkDensity(list) <= 0.4 {
		t.Fatalf("list page should exceed threshold: %.2f", linkDensity(list))
	}
	if linkDensity(art) > 0.4 {
		t.Fatalf("article should stay under threshold: %.2f", linkDensity(art))
	}
}

func TestTargetLangRatio(t *testing.T) {
	cases := []struct {
		name     string
		text     string
		min, max float64
	}{
		{"chinese", strings.Repeat("这是一段中文技术文档内容，讨论实现细节与取舍。", 30), 0.99, 1.01},
		{"english", strings.Repeat("English technical document about implementation. ", 30), 0.99, 1.01},
		{"russian", strings.Repeat("Это русский технический текст о серверах. ", 30), 0, 0.01},
		{"japanese", strings.Repeat("これは日本語の技術文書です。", 30), 0, 0.6},
		{"mixed-with-terms", "Kubernetes 集群的调度器实现与 goroutine 抢占机制分析", 0.99, 1.01},
		{"code-only", "{}()[]=> 123 +- */", 1, 1},
		{"empty", "", 1, 1},
	}
	for _, c := range cases {
		got := TargetLangRatio(c.text)
		if got < c.min || got > c.max {
			t.Errorf("%s: ratio=%.3f, want [%.2f, %.2f]", c.name, got, c.min, c.max)
		}
	}
}

// 中文正文里夹英文术语不应被误判：两边都算目标语言。
func TestTargetLangRatioMixedTechnicalText(t *testing.T) {
	text := strings.Repeat("使用 Kubernetes 的 Deployment 控制器滚动更新 Pod，配合 Prometheus 监控。", 20)
	if got := TargetLangRatio(text); got < 0.99 {
		t.Fatalf("mixed technical text should stay high, got %.3f", got)
	}
}

// 发布时间在真实站点上有多种写法，标准 meta 只覆盖一种。
// 以下样例取自实际页面结构（阮一峰 / 美团 / InfoQ），三者原先都解析不出来。
func TestMetaFieldsDateVariants(t *testing.T) {
	cases := []struct {
		name    string
		url     string
		html    string
		wantDay string // 期望的日期（UTC+8 转换交给调用方，这里只看 UTC 日期）
	}{
		{
			name:    "微格式 abbr.published",
			url:     "https://www.ruanyifeng.com/blog/2026/10/weekly-issue-416.html",
			html:    `<a href="/blog/2026/10/"><abbr class="published" title="2026-10-23T08:06:16+08:00">2026-10-23</abbr></a>`,
			wantDay: "2026-10-23", // 2026-10-23T08:06:16+08:00 → UTC 仍是 23 日
		},
		{
			name:    "前端渲染 class=read-time",
			url:     "https://www.infoq.cn/article/xxx",
			html:    `<ul class="read-time" data-v-40b2462d><li data-v-40b2462d>2026-10-23<div class="time-sty"></div></li></ul>`,
			wantDay: "2026-10-23",
		},
		{
			name:    "纯文本 span",
			url:     "https://tech.meituan.com/2025/12/05/AI-Coding-Unit-Testing.html",
			html:    `<div class="post-date"><span>2025-12-05</span></div>`,
			wantDay: "2025-12-05",
		},
		{
			// InfoQ 的真实结构：日期与「北京」等字样直接相连，中间没有空白
			name:    "日期与文字粘连",
			url:     "https://www.infoq.cn/article/xxx",
			html:    `<div class="article-info"><ul class="read-time"><li>2026-10-23<div class="time-style">北京</div></li></ul></div>`,
			wantDay: "2026-10-23",
		},
		{
			name:    "URL 路径兜底（只有年月）",
			url:     "https://www.zhihu.example/blog/2026/03/some-post",
			html:    `<html><body>没有日期标记</body></html>`,
			wantDay: "2026-03-01",
		},
		{
			name:    "URL 路径兜底（年月日）",
			url:     "https://tech.example.com/2024/07/09/post.html",
			html:    `<html><body>没有日期标记</body></html>`,
			wantDay: "2024-07-09",
		},
	}
	for _, c := range cases {
		_, got := metaFields([]byte(c.html), c.url)
		if got == nil {
			t.Errorf("%s: 未解析出发布时间", c.name)
			continue
		}
		day := time.Unix(*got, 0).UTC().Format("2006-01-02")
		if day != c.wantDay {
			t.Errorf("%s: 日期=%s want %s", c.name, day, c.wantDay)
		}
	}
}

// 页面与 URL 都没有日期时必须返回 nil：宁可按收录时间展示，也不能编一个。
func TestMetaFieldsNoDateReturnsNil(t *testing.T) {
	_, got := metaFields([]byte(`<html><body>纯文档页</body></html>`),
		"https://go.dev/doc/effective_go")
	if got != nil {
		t.Fatalf("无日期时不应返回时间戳，实际 %d", *got)
	}
}

// URL 里的"未来日期"不可信：版本号/ID 常形似年月。
func TestDateFromURLRejectsFuture(t *testing.T) {
	future := time.Now().AddDate(3, 0, 0).Format("/2006/01/")
	if _, ok := dateFromURL("https://example.com" + future + "post"); ok {
		t.Fatal("未来日期应被拒绝")
	}
}

// TestPickTitle 重新处理快照时的标题选择规则。
//
// 背景事故：中断的 reindex 拿纯文本快照（seed 语料没有 HTML 结构）重新提标题，
// processor 兜底成域名，把表里 134 篇正确标题覆盖掉——语料内 41% 退化。
func TestPickTitle(t *testing.T) {
	host := "www.infoq.cn"
	cases := []struct {
		name             string
		extracted, exist string
		want             string
	}{
		{"提取退化、表里正确 → 保留表里", "www.infoq.cn", "中国卓越技术团队访谈录", "中国卓越技术团队访谈录"},
		{"提取正确、表里退化 → 用提取的", "Go 语言规范", "go.dev", "Go 语言规范"},
		{"两者都退化 → 认提取的（域名兜底）", "www.infoq.cn", "www.infoq.cn", "www.infoq.cn"},
		{"两者都正确 → 认提取的（重新提取更新）", "新标题", "旧标题", "新标题"},
		{"表里为空 → 用提取的", "www.infoq.cn", "", "www.infoq.cn"},
		{"提取过短(<4字符)也算退化", "Go", "Go 语言规范", "Go 语言规范"},
	}
	for _, c := range cases {
		if got := PickTitle(c.extracted, c.exist, host); got != c.want {
			t.Errorf("%s：PickTitle(%q, %q) = %q，想要 %q",
				c.name, c.extracted, c.exist, got, c.want)
		}
	}
}

// TestIsSiteSegment 标题增强的判据：readability 标题是否只是 <title> 的站名段。
//
// 只认「最后一段」这一种形态。正常页面 readability 返回的是首段或独立标题，
// 不能被改写成带站名的全称——那等于给所有文档强加站名后缀。
func TestIsSiteSegment(t *testing.T) {
	cases := []struct {
		short, full string
		want        bool
	}{
		{"Vue.js", "Reactivity Fundamentals | Vue.js", true}, // SPA 退化形态
		{"X", "A | B | X", true},                             // 多段，站名在末
		{"Go 语言规范", "Go 语言规范 | Go 技术圣经", false},              // 首段：正常页面，不动
		{"语言规范详解", "Go 语言规范 | Go 技术圣经", false},               // 独立标题，不动
		{"Vue.js", "Vue.js", false},                          // 相等：没有更具体的可用
		{"Vue.js", "Vue.js | Reactivity", false},             // 站名在前：已知边界，不触发
		{"Vue.js", "Vue.js", false},                          // 单段：无可增强
		{"", "A | B", false},                                 // 空标题交给后续兜底链
	}
	for _, c := range cases {
		if got := isSiteSegment(c.short, c.full); got != c.want {
			t.Errorf("isSiteSegment(%q, %q) = %v，想要 %v", c.short, c.full, got, c.want)
		}
	}
}

// TestTitleEnrichedFromDocTitle 集成：SPA 页面形态下标题被增强为 <title> 全称。
//
// 背景：vuejs.org 全部 71 篇的标题都曾是 "Vue.js"——readability 从 SSR 首屏的
// 页头抓到站标，而 <title> 里是 "Reactivity Fundamentals | Vue.js"。
// 结果页一列同名结果，标题这个 BM25 加权字段也失去信号。
func TestTitleEnrichedFromDocTitle(t *testing.T) {
	p, err := New()
	if err != nil {
		t.Fatal(err)
	}
	body := strings.Repeat("响应式基础是 Vue 的核心概念，ref 与 reactive 构成声明式状态管理的地基。", 8)

	spa := []byte(`<!doctype html><html><head><title>Reactivity Fundamentals | Vue.js</title>
		<meta property="og:title" content="Vue.js"></head>
		<body><header><a class="logo">Vue.js</a></header>
		<article><h1>Vue.js</h1><p>` + body + `</p></article></body></html>`)
	res, err := p.Process("https://vuejs.org/guide/essentials/reactivity-fundamentals", spa, Options{})
	if err != nil {
		t.Fatal(err)
	}
	if res.Title != "Reactivity Fundamentals | Vue.js" {
		t.Fatalf("SPA 页面应改用 <title> 完整形式，实际 %q", res.Title)
	}

	// 只有一段的 <title>（整站同名）无可增强，保持原样
	single := []byte(`<!doctype html><html><head><title>Vue.js</title></head>
		<body><article><h1>Vue.js</h1><p>` + body + `</p></article></body></html>`)
	res3, err := p.Process("https://vuejs.org/about/faq", single, Options{})
	if err != nil {
		t.Fatal(err)
	}
	if res3.Title != "Vue.js" {
		t.Fatalf("单段标题应保持原样，实际 %q", res3.Title)
	}
}

func TestSiteName(t *testing.T) {
	cases := []struct {
		name string
		html string
		want string
	}{
		{"og:site_name", `<html><head><meta property="og:site_name" content="Docker Docs"></head><body></body></html>`, "Docker Docs"},
		{"name fallback", `<html><head><meta name="application-name" content="Redis"></head></html>`, "Redis"},
		{"title last resort", `<html><head><title>Rust 编程语言</title></head></html>`, "Rust 编程语言"},
		{"boilerplate title skipped", `<html><head><title>用户协议</title></head></html>`, ""},
		{"empty", `<html><body></body></html>`, ""},
		{"truncate long", `<html><head><meta property="og:site_name" content="` + strings.Repeat("很", 50) + `"></head></html>`, strings.Repeat("很", 40)},
	}
	for _, c := range cases {
		if got := SiteName([]byte(c.html)); got != c.want {
			t.Errorf("%s: got %q want %q", c.name, got, c.want)
		}
	}
}

// 追踪器：≥2 类第三方统计脚本才打标——只有 GA 的站点不该被一刀切；
// 理由必须列出命中的脚本名（可解释红线）。
func TestTrackerLabel(t *testing.T) {
	p := newTestProcessor(t)
	html := `<html><head><title>t</title>` +
		`<script src="https://www.googletagmanager.com/gtag/js?id=G-X"></script>` +
		`<script src="https://hm.baidu.com/hm.js?abc"></script>` +
		`</head><body><article><p>` + strings.Repeat("正文内容，讨论一个技术细节。", 30) + `</p></article></body></html>`
	res, err := p.Process("https://site.example.com/a", []byte(html), Options{})
	if err != nil {
		t.Fatal(err)
	}
	if !hasLabel(res.Labels, LabelTracker) {
		t.Fatalf("two tracker families should label tracker: %v", res.Labels)
	}
	if !strings.Contains(res.LabelReasons[LabelTracker], "Google Analytics") ||
		!strings.Contains(res.LabelReasons[LabelTracker], "百度统计") {
		t.Fatalf("reason should name the trackers: %q", res.LabelReasons[LabelTracker])
	}

	// 单一追踪器：不判（几乎所有正规站点都有 GA，打了等于没打）
	html1 := `<html><head><title>t</title>` +
		`<script src="https://www.googletagmanager.com/gtag/js?id=G-X"></script>` +
		`</head><body><article><p>` + strings.Repeat("正文内容，讨论另一个技术细节。", 30) + `</p></article></body></html>`
	res1, err := p.Process("https://site.example.com/b", []byte(html1), Options{})
	if err != nil {
		t.Fatal(err)
	}
	if hasLabel(res1.Labels, LabelTracker) {
		t.Fatalf("single tracker family should not label: %v", res1.Labels)
	}
}
