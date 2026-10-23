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
