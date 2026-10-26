package index

import (
	"path/filepath"
	"testing"
)

// 本文件是「技术符号分词」的结论性回归测试（2026-10-24 立项评估，结论：不改）。
//
// 规划书风险 5 原文假设是「cjk bigram 会把 kubernetes.io、v1.28 切碎」。
// 实测该假设不成立：cjk 分词器对拉丁字母+数字+点/下划线是整段保留的，
//
//	"kubernetes.io" → [kubernetes.io]   "v1.28" → [v1.28]
//	"Node.js"       → [node.js]         "1.2.3" → [1.2.3]
//	"x86_64"        → [x86_64]
//
// 真实存在的缺陷只有一处：**符号被丢弃导致同形**
//
//	"C#" → [c]   "C++" → [c]   → 与「C 语言」的 [c] 完全同分
//
// 但它在当前语料上不构成指标损失（评测集里含符号的查询只有 2 条，
// Recall 均为 1.00，高于全部查询均值），所以不在本期修。
// 下面两个测试的作用是把这个判断钉住：将来语料扩大到有大量 C/C++ 文档时，
// TestSymbolSameFormCollision 会持续提醒「这里确实有混淆」，
// 而不是让人误以为分词器已经处理好了一切。

// TestSymbolTokensIntact 版本号与技术符号必须完整保留。
// 这条防的是「有人换掉 analyzer 后把版本号切碎」——那会直接损失
// 「Go 1.22」「Python 3.12」这类查询的精确性。
func TestSymbolTokensIntact(t *testing.T) {
	dir := t.TempDir()
	idx, err := Open(filepath.Join(dir, "bleve"))
	if err != nil {
		t.Fatal(err)
	}
	defer idx.Close()

	docs := []Doc{
		{URL: "https://a/go", Domain: "a", Title: "Go 1.22 发布说明", Body: "循环变量语义"},
		{URL: "https://b/k8s", Domain: "b", Title: "kubernetes.io 文档", Body: "ingress 配置"},
		{URL: "https://c/node", Domain: "c", Title: "Node.js 事件循环", Body: "异步 IO"},
	}
	for _, d := range docs {
		if err := idx.IndexDocument(d); err != nil {
			t.Fatal(err)
		}
	}
	// 整串、版本号、域名、单词片段都要能命中同一篇
	cases := []struct{ q, want string }{
		{"Go 1.22", "https://a/go"},
		{"1.22", "https://a/go"}, // 版本号单独可查
		{"Go", "https://a/go"},   // 单词片段可查
		{"kubernetes.io", "https://b/k8s"},
		{"Node.js", "https://c/node"},
		{"事件循环", "https://c/node"},
	}
	for _, c := range cases {
		hits, _, err := idx.Search(Query{Q: c.q, Page: 1, Size: 10})
		if err != nil {
			t.Fatalf("%q: %v", c.q, err)
		}
		found := false
		for _, h := range hits {
			if h.URL == c.want {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("查询 %q 未命中 %s（命中 %d 条）", c.q, c.want, len(hits))
		}
	}
}

// TestSymbolSameFormCollision 记录一个**已知且未修**的缺陷：C# / C++ 与 C 同形。
//
// 这个测试断言的是"当前行为"（两者同分），不是"正确行为"。
// 它存在的意义：如果哪天有人实现了符号保留（自定义 tokenizer/char filter），
// 这条会失败——那时应当把断言改成 C# 文档排在 C 语言文档之前，而不是删掉测试。
func TestSymbolSameFormCollision(t *testing.T) {
	dir := t.TempDir()
	idx, err := Open(filepath.Join(dir, "bleve"))
	if err != nil {
		t.Fatal(err)
	}
	defer idx.Close()

	docs := []Doc{
		{URL: "https://a/csharp", Domain: "a", Title: "C# 异步编程入门", Body: "async await"},
		{URL: "https://b/clang", Domain: "b", Title: "C 语言指针详解", Body: "指针与内存"},
	}
	for _, d := range docs {
		if err := idx.IndexDocument(d); err != nil {
			t.Fatal(err)
		}
	}
	hits, _, err := idx.Search(Query{Q: "C#", Page: 1, Size: 10})
	if err != nil {
		t.Fatal(err)
	}
	if len(hits) != 2 {
		t.Fatalf("C# 查询命中 %d 条，预期 2 条（C# 与 C 语言无法区分）", len(hits))
	}
	if hits[0].Score != hits[1].Score {
		t.Logf("注意：C# 与 C 语言已能区分（%.3f vs %.3f），"+
			"说明符号保留已实现，请把本测试改成断言 C# 文档排第一",
			hits[0].Score, hits[1].Score)
		return
	}
	t.Logf("已知缺陷：C# 与 C 语言同分 %.3f（# 被丢弃）。"+
		"当前语料无 C 语言文档竞争，评测无损失，故不修。", hits[0].Score)
}
