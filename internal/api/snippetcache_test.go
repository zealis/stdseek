package api

import (
	"path/filepath"
	"testing"

	"voidxo/internal/kv"
)

// TestSnippetCacheInvalidatedByKVWrite 片段缓存最危险的失效场景：
// 快照被重新抓取覆盖后，缓存里的片段必须作废。
//
// 这条断言保护的是「拿过期正文当证据」——功能 D 的每条结论都要能回指原文，
// 缓存返回旧正文的片段等于让用户核对一份已经不存在的证据。
func TestSnippetCacheInvalidatedByKVWrite(t *testing.T) {
	dir := t.TempDir()
	snap, err := kv.Open(filepath.Join(dir, "kv"))
	if err != nil {
		t.Fatal(err)
	}
	defer snap.Close()
	c := newSnippetCache()

	const urlKey = "a.example/p1"
	html1 := `<html><body><article><p>` + repeat("单元测试能够独立运行、快速验证结果，这是它与手工回归的本质区别。", 4) +
		`</p></article></body></html>`
	if err := snap.Put(kv.Key(urlKey), []byte(html1)); err != nil {
		t.Fatal(err)
	}
	s1 := c.snippetsFor(snap.Version(), urlKey, "单元测试", html1)
	if len(s1) == 0 {
		t.Fatal("应生成片段")
	}
	// 第二次：命中缓存
	if got := c.snippetsFor(snap.Version(), urlKey, "单元测试", html1); len(got) != len(s1) || got[0] != s1[0] {
		t.Fatalf("缓存命中应返回相同片段，实际 %q vs %q", got, s1)
	}
	if c.hits.Load() != 1 {
		t.Fatalf("第二次应命中缓存，hits=%d", c.hits.Load())
	}

	// 快照被覆盖（重新抓取）：KV 代数 +1，缓存必须整表作废
	html2 := `<html><body><article><p>` + repeat("单元测试的内容已完全更新，现在讲的是另一套实践。", 4) +
		`</p></article></body></html>`
	if err := snap.Put(kv.Key(urlKey), []byte(html2)); err != nil {
		t.Fatal(err)
	}
	s2 := c.snippetsFor(snap.Version(), urlKey, "单元测试", html2)
	if len(s2) == 0 {
		t.Fatal("新快照也应生成片段")
	}
	if s2[0] == s1[0] && len(html1) != len(html2) {
		// 两版正文不同，片段理应不同；相同说明缓存没失效
		if !contains(s1[0], "已完全更新") && contains(s2[0], "已完全更新") {
			t.Fatalf("缓存未失效：仍返回旧正文的片段 %q", s2[0])
		}
	}
	if !contains(s2[0], "已完全更新") {
		t.Fatalf("应返回新正文的片段，实际 %q", s2[0])
	}

	// 删除快照后同样不能再命中
	if err := snap.Delete(kv.Key(urlKey)); err != nil {
		t.Fatal(err)
	}
	if got := c.snippetsFor(snap.Version(), urlKey, "单元测试", ""); len(got) != 0 {
		t.Fatalf("删除后缓存应作废，实际仍返回 %q", got)
	}
}

// TestSnippetCacheKeyNormalizesQuery 大小写不同的查询应共用缓存
// （buildSnippets 内部 ToLower，两者的片段完全相同）。
func TestSnippetCacheKeyNormalizesQuery(t *testing.T) {
	c := newSnippetCache()
	if c.key("a/p", "Go") != c.key("a/p", "  go  ") {
		t.Fatal("查询词应归一化后作 key")
	}
	if c.key("a/p", "go") == c.key("a/p2", "go") {
		t.Fatal("不同 URL 不应共用缓存条目")
	}
}

// TestSnippetCacheCachesEmpty 空结果也要缓存：命中不到查询词的页面
// 反复出现时，不该每次都白跑一遍剥标签+扫描才知道"没有"。
func TestSnippetCacheCachesEmpty(t *testing.T) {
	c := newSnippetCache()
	// 正文必须超过 snippetMinLen(200)：短正文会被整段返回，
	// 那样就测不到"无命中时返回空"这条路径。
	html := `<html><body><article><p>` + repeat("这段文字与查询毫无关系，讲的是别的事情，长度要够长才会走窗口扫描。", 12) +
		`</p></article></body></html>`
	got := c.snippetsFor(1, "a/p", "量子计算退相干", html)
	if len(got) != 0 {
		t.Fatalf("无关正文应无片段，实际 %q", got)
	}
	if c.hits.Load() != 0 {
		t.Fatal("首次应是 miss")
	}
	_ = c.snippetsFor(1, "a/p", "量子计算退相干", html)
	if c.hits.Load() != 1 {
		t.Fatalf("空结果也应被缓存并命中，hits=%d", c.hits.Load())
	}
}

// snippetsFor 复现 cachedSnippets 的缓存路径（不启动 HTTP）。
// gen 由调用方传入（生产里是 snap.Version()），这样测试才能模拟
// "快照变了"与"快照没变"两种情况。
func (c *snippetCache) snippetsFor(gen int64, urlKey, q, body string) []string {
	key := c.key(urlKey, q)
	if got, ok := c.get(gen, key); ok {
		return got
	}
	snips := buildSnippets(body, q, snippetMaxPerDoc)
	c.put(gen, key, snips)
	return snips
}

func repeat(s string, n int) string {
	out := ""
	for i := 0; i < n; i++ {
		out += s
	}
	return out
}

func contains(s, sub string) bool {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}

// TestSnippetCacheSkipsKVReadWhenHit 命中时必须能跳过 KV 读。
// 这是缓存前置的意义：命中时片段是现成的，没必要为了算一个 key
// 去读 80KB 快照。没有快照的老文档也不该每次搜索都付一次读存储的代价。
func TestSnippetCacheSkipsKVReadWhenHit(t *testing.T) {
	c := newSnippetCache()
	// 记住"这篇没有快照"
	c.rememberNoSnapshot(1, "a/none")
	// 命中：返回 nil（无快照），调用方据此跳过 KV 读
	snips, ok := c.lookup(1, "a/none")
	if !ok {
		t.Fatal("rememberNoSnapshot 后应能查到")
	}
	if len(snips) != 0 {
		t.Fatalf("无快照应返回空，实际 %q", snips)
	}
	// 代数变化后必须查不到（快照可能刚被抓上来）
	if _, ok := c.lookup(2, "a/none"); ok {
		t.Fatal("KV 代数变化后不应再命中")
	}
}
