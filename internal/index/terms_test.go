package index

import (
	"sort"
	"strings"
	"testing"
	"time"
)

func TestExpandTerms(t *testing.T) {
	cases := []struct {
		q    string
		want []string
	}{
		// 中文查询打英文文档：补上英文对应词
		{"PHP 命名空间", []string{"namespaces"}},
		{"docker 构建镜像", []string{"build", "image"}},
		{"kubernetes 社区", []string{"community"}},
		// 多字词优先：同时含「单元测试」和「测试」时只扩展前者。
		// 注意这里必须带一个拉丁词——纯中文查询不扩展（见下一条与 expandTerms 注释）。
		{"php 单元测试怎么写", []string{"unit-testing"}},
		// **纯中文查询不扩展英文词**（红线 9：中文 query 只召回中文内容）。
		// 虽然索引层的 lang=zh 硬过滤会挡住英文文档，但扩出来的英文词仍会匹配到
		// 中文文档里的英文片段（代码块、专有名词），引入词面噪音。
		// 实测：放开纯中文扩展后，纯中文组 NDCG@10 0.834 → 0.828。
		{"单元测试怎么写", nil},
		// 纯英文查询不扩展（没有翻译需求）
		{"namespaces", nil},
		{"docker buildx", nil},
		// 无词典命中
		{"鸿蒙长列表滑动卡顿", nil},
	}
	for _, c := range cases {
		got := expandTerms(c.q)
		sort.Strings(got)
		want := append([]string(nil), c.want...)
		sort.Strings(want)
		if strings.Join(got, ",") != strings.Join(want, ",") {
			t.Errorf("expandTerms(%q)=%v want %v", c.q, got, want)
		}
	}
}

// 扩展词必须真的改变检索结果：「PHP 命名空间」在加扩展前，
// 真正的 language.namespaces.php 排不进前十（被 php.net 通用页压住）。
func TestExpansionPromotesEnglishDoc(t *testing.T) {
	idx := newTestIndex(t)
	now := time.Now().Unix()
	mustIndex(t, idx,
		Doc{URL: "https://www.php.net/", Domain: "www.php.net", Title: "PHP",
			Description: "PHP 官方站", Body: "php php php php homepage", FetchedAt: now},
		Doc{URL: "https://www.php.net/manual/en/language.namespaces.php", Domain: "www.php.net",
			Title:       "PHP: Namespaces - Manual",
			Description: "PHP namespaces reference",
			Body:        "php manual namespaces overview declaring namespaces", FetchedAt: now},
	)

	hits, _, err := idx.Search(Query{Q: "PHP 命名空间", Page: 1, Size: 5})
	if err != nil {
		t.Fatal(err)
	}
	if len(hits) == 0 || !strings.Contains(hits[0].URL, "namespaces") {
		t.Fatalf("扩展后 namespaces 手册应排第一，实际 top1=%s", hits[0].URL)
	}
}

// TestExpandTermsMultiValue 一个中文词映射到多个英文时全部返回。
//
// 「响应式」是这类歧义的典型：Vue 的响应式系统是 reactivity，
// CSS 的响应式布局是 responsive。旧词典只收 responsive，于是
// 「vue 响应式基础」被扩成 responsive，目标页 Reactivity Fundamentals 排不上来
// （real-v2 该查询 NDCG 0.00）。硬选一个等于系统性做错一类查询。
func TestExpandTermsMultiValue(t *testing.T) {
	got := expandTerms("vue 响应式基础")
	want := map[string]bool{"reactivity": true, "responsive": true, "fundamentals": true}
	for _, w := range want {
		_ = w
	}
	seen := map[string]bool{}
	for _, g := range got {
		seen[g] = true
	}
	for k := range want {
		if !seen[k] {
			t.Errorf("expandTerms(\"vue 响应式基础\") 缺少 %q，实际 %v", k, got)
		}
	}
}

// TestExpandTermsDocsVocabulary 2026-10-24 补入的现代文档词汇。
// 每一条都对应一个真实失败的评测查询，不是凭感觉加的。
func TestExpandTermsDocsVocabulary(t *testing.T) {
	cases := map[string][]string{
		"redis 扫描迭代":    {"scan", "iteration"},
		"node 流处理":      {"stream"},
		"node 废弃的 API":  {"deprecated"},
		"python 堆上分配对象": {"heap", "allocation", "objects"},
		"nginx 上游服务器模块": {"upstream", "server", "module"},
		"vue 单文件组件":     {"single-file", "sfc", "components"},
		"vue 组合式 API":   {"composition"},
		"redis 概率数据类型":  {"probabilistic", "data-types"},
	}
	for q, need := range cases {
		got := expandTerms(q)
		seen := map[string]bool{}
		for _, g := range got {
			seen[g] = true
		}
		for _, n := range need {
			if !seen[n] {
				t.Errorf("expandTerms(%q) 缺少 %q，实际 %v", q, n, got)
			}
		}
	}
}

// TestTermVariantsMultiValue 排序层的 TermVariants 同样要返回全部变体。
// 它喂给 exactMatch，决定「中文查询词在英文标题里写成什么」。
func TestTermVariantsMultiValue(t *testing.T) {
	got := TermVariants("响应式")
	seen := map[string]bool{}
	for _, g := range got {
		seen[g] = true
	}
	if !seen["reactivity"] || !seen["responsive"] {
		t.Fatalf("TermVariants(\"响应式\") 应同时含 reactivity 与 responsive，实际 %v", got)
	}
	// 连字符词要展开成空格版（真实标题写法 "Single-File Components"）
	sfc := TermVariants("单文件")
	joined := strings.Join(sfc, ",")
	if !strings.Contains(joined, "single-file") || !strings.Contains(joined, "single file") {
		t.Fatalf("TermVariants(\"单文件\") 应含连字符与空格两种形态，实际 %v", sfc)
	}
}
