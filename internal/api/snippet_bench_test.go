package api

import (
	"fmt"
	"html"
	"regexp"
	"strings"
	"testing"
)

// fakePage 构造一个接近真实规模的快照：80KB HTML、约 2800 个标签
// （tech.meituan.com 的实测规模），带 script/style/noscript 与大量行内标签。
func fakePage() string {
	var b strings.Builder
	b.WriteString(`<!doctype html><html><head><meta charset="utf-8">`)
	b.WriteString(`<title>单元测试与 AI 编程的协同进化</title>`)
	b.WriteString(`<link rel="stylesheet" href="/a.css"><style>.x{color:red}</style>`)
	b.WriteString(`<script>window.data={a:1,b:2};function f(){return 1}</script>`)
	b.WriteString(`</head><body><div class="wrap"><header><nav>`)
	for i := 0; i < 60; i++ {
		fmt.Fprintf(&b, `<a href="/p/%d">导航链接 %d</a>`, i, i)
	}
	b.WriteString(`</nav></header><main><article>`)
	para := `<p>单元测试能够独立运行、快速验证结果，并且可以无限次重复执行，` +
		`这是它与手工回归测试的本质区别，也是持续集成里第一道闸门。</p>`
	for i := 0; i < 220; i++ {
		fmt.Fprintf(&b, `<h2>小节 %d</h2>%s<p>更多说明文字，包含 <code>go test</code> 与 <b>覆盖率</b> 指标。</p>`,
			i, para)
	}
	b.WriteString(`</article></main><footer><noscript>请开启 JavaScript</noscript>`)
	for i := 0; i < 40; i++ {
		fmt.Fprintf(&b, `<a href="/f/%d">页脚 %d</a>`, i, i)
	}
	b.WriteString(`</footer></div></body></html>`)
	return b.String()
}

func BenchmarkHTMLToText(b *testing.B) {
	page := fakePage()
	b.Logf("输入规模: %d 字节", len(page))
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = htmlToText(page)
	}
}

// stripTagsScan 手写扫描：一次遍历，遇 '<' 找 '>'，跳过 script/style 段。
// 与正则版的区别是常量级更小——全局正则在长串上要反复回滚匹配。
var (
	reScriptStyle = regexp.MustCompile(`(?is)<(script|style|noscript)[^>]*>.*?</(script|style|noscript)>`)
	reTag         = regexp.MustCompile(`(?s)<[^>]*>`)
)

func regexStrip(s string) string {
	if !strings.Contains(s, "<") {
		return s
	}
	s = reScriptStyle.ReplaceAllString(s, " ")
	s = reTag.ReplaceAllString(s, " ")
	s = html.UnescapeString(s)
	return strings.Join(strings.Fields(s), " ")
}

// TestStripTagsScanMatchesRegex 手写版必须与正则版结果一致，
// 否则替换就是在改行为——剥标签的差异会直接改变证据片段的位置。
func TestStripTagsScanMatchesRegex(t *testing.T) {
	page := fakePage()
	a := regexStrip(page)
	c := htmlToText(page)
	if a != c {
		// 找出第一个差异点，方便定位
		i := 0
		for i < len(a) && i < len(c) && a[i] == c[i] {
			i++
		}
		t.Fatalf("两版剥标签结果不一致（长度 %d vs %d，首个差异在 %d）:\n正则: …%s\n扫描: …%s",
			len(a), len(c), i, a[max(0, i-40):i+40], c[max(0, i-40):i+40])
	}
	// 单测构造的页面也要覆盖：无标签、纯文本、嵌套、属性含 '>'
	for _, tc := range []string{
		"plain text no tags",
		"<p>a</p>",
		`<div title="a>b">属性里有大于号</div>`,
		"<script>var x = '</scr' + 'ipt>';</script>after",
		"<STYLE>.a{}</STYLE>ok",
		"",
		"尾部没有闭合 < 就结束了",
		"<script>未闭合的脚本",
		"正常段落。< 后面直接是文字",
	} {
		if got, want := htmlToText(tc), regexStrip(tc); got != want {
			t.Fatalf("输入 %q：扫描版 %q != 正则版 %q", tc, got, want)
		}
	}
}

func max(a, b int) int {
	if a > b {
		return a
	}
	return b
}
