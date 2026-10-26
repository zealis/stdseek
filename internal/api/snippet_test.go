package api

import (
	"strings"
	"testing"
)

// TestSnippetTokens 查询分词：中文按 2-gram、英文要求长度 ≥3。
//
// 中文必须用 bigram 而不是整串：正文里未必与查询逐字一致（查询「单元测试」、
// 正文写「单测」），只要有一段命中就算相关。
// 英文必须过滤短词：the/of 这类会把每个窗口都点亮，片段就失去区分度。
func TestSnippetTokens(t *testing.T) {
	got := snippetTokens("单元测试")
	want := map[string]bool{"单元": true, "元测": true, "测试": true}
	if len(got) != len(want) {
		t.Fatalf("「单元测试」应产生 3 个 bigram，实际 %v", got)
	}
	for _, g := range got {
		if !want[g] {
			t.Errorf("意外的 token: %q（全部 %v）", g, got)
		}
	}
	// 单字不成词
	if v := snippetTokens("锁"); len(v) != 0 {
		t.Errorf("单字不应产生 token，实际 %v", v)
	}
	// 英文：to/of 被过滤，kubernetes 保留
	en := snippetTokens("to of kubernetes")
	if len(en) != 1 || en[0] != "kubernetes" {
		t.Errorf("英文应只保留 kubernetes，实际 %v", en)
	}
	// 中英混排
	mix := snippetTokens("react 快速开始")
	for _, must := range []string{"react", "快速", "速开", "开始"} {
		if !containsStr(mix, must) {
			t.Errorf("中英混排应含 %q，实际 %v", must, mix)
		}
	}
}

// TestBuildSnippets 片段必须来自正文、命中查询、非空且不重叠。
func TestBuildSnippets(t *testing.T) {
	filler := strings.Repeat("这里是一段与查询无关的填充文字，用来把命中位置推到后面。", 12)
	body := filler + "单元测试的价值在于用低成本验证逻辑正确性，安全网保护存量代码演进。" + filler

	sn := buildSnippets(body, "单元测试", 2)
	if len(sn) == 0 {
		t.Fatal("应至少产生一个片段")
	}
	found := false
	for _, s := range sn {
		if strings.Contains(s, "单元测试") || strings.Contains(s, "测试") {
			found = true
		}
	}
	if !found {
		t.Errorf("片段里应包含查询词，实际 %v", sn)
	}

	// 多个片段不能指向同一段正文
	if len(sn) > 1 && sn[0] == sn[1] {
		t.Errorf("片段不应重复: %v", sn)
	}

	// 短正文整段返回
	short := "很短的一段正文，含单元测试四个字。"
	if got := buildSnippets(short, "单元测试", 2); len(got) != 1 || got[0] != short {
		t.Errorf("短正文应整段返回，实际 %v", got)
	}

	// 无命中时不编造片段
	if got := buildSnippets(body, "量子计算编译器", 2); len(got) != 0 {
		t.Errorf("无命中应返回空，实际 %v", got)
	}
}

// TestHTMLToText 快照是原始 HTML，必须先剥成文本再切片。
//
// 真实案例：tech.meituan.com 的快照 80KB、2794 个标签，
// 不剥的话片段开头是 `<link rel="alternate" type="application/rss+xml" ...>`。
func TestHTMLToText(t *testing.T) {
	raw := `<!doctype html><html><head><meta charset="utf-8">
	<link rel="alternate" href="https://a/rss.xml"><style>:root{--x:#fff}</style>
	<script>var a = "<p>不该出现</p>";</script></head>
	<body><h1>单元测试</h1><p>正文 &amp; 实体</p></body></html>`
	got := htmlToText(raw)
	if strings.Contains(got, "<") || strings.Contains(got, "不该出现") {
		t.Errorf("应剥掉标签、style 与 script 内容，实际 %q", got)
	}
	if !strings.Contains(got, "单元测试") || !strings.Contains(got, "正文 & 实体") {
		t.Errorf("正文与实体解码丢失，实际 %q", got)
	}
	// 纯文本原样返回（seed 导入的语料是纯文本，不能被误处理）
	plain := "纯文本正文，没有标签。"
	if htmlToText(plain) != plain {
		t.Errorf("纯文本应原样返回，实际 %q", htmlToText(plain))
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
