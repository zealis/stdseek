package index

import (
	"path/filepath"
	"strings"
	"testing"
)

// TestExcerptStoredAndReturned 正文摘要必须能存进去、取得回。
//
// excerpt 的用途是结果页展示与证据链文本，**不参与排序**（排序层实验后否决，
// 见 rank.exactMatch 的注释）。它换过一种实现：最初设 Index=false 想做到
// "只存不索引"，实测 Bleve 在 Index=false 时连 Store 一并失效，取回空值，
// 必须 Index=true 才能真正存下。隔离检索影响靠两件事：IncludeInAll=false，
// 且 buildRequest 的 fieldClause 只用 title/description/body。
func TestExcerptStoredAndReturned(t *testing.T) {
	dir := t.TempDir()
	idx, err := Open(filepath.Join(dir, "bleve"))
	if err != nil {
		t.Fatal(err)
	}
	defer idx.Close()

	body := strings.Repeat("这是一段很长的正文内容，用于验证摘要截断行为。", 30)
	if err := idx.IndexDocument(Doc{
		URL: "https://vuejs.org/glossary/", Domain: "vuejs.org", Title: "Vue.js",
		Description: "Vue.js - The Progressive JavaScript Framework", Body: body,
	}); err != nil {
		t.Fatal(err)
	}

	hits, _, err := idx.Search(Query{Q: "正文内容", Page: 1, Size: 5})
	if err != nil {
		t.Fatal(err)
	}
	if len(hits) == 0 {
		t.Fatal("查询无命中")
	}
	got := hits[0].Excerpt
	if got == "" {
		t.Fatal("excerpt 未存回（Index=false 会让 Store 一起失效）")
	}
	if !strings.HasSuffix(got, "…") {
		t.Errorf("超长正文的摘要应以省略号结尾，实际 %q", got)
	}
	// 200 个字符 + 省略号
	if n := len([]rune(got)); n > excerptRunes+1 {
		t.Errorf("摘要长度 %d 超过上限 %d", n, excerptRunes+1)
	}
}

// TestExcerptEmptyBody 空正文不应产生摘要，也不应报错。
func TestExcerptEmptyBody(t *testing.T) {
	if got := excerptOf("", 200); got != "" {
		t.Errorf("空正文摘要应为空，实际 %q", got)
	}
	short := "只有一句话"
	if got := excerptOf(short, 200); got != short {
		t.Errorf("短文本应原样返回，实际 %q", got)
	}
	// 空白压缩
	if got := excerptOf("a\n\n  b\tc", 200); got != "a b c" {
		t.Errorf("空白应压缩为单空格，实际 %q", got)
	}
}
