package api

import (
	"path/filepath"
	"testing"

	"voidxo/internal/index"
)

// TestSearchCacheInvalidatedByIndexWrite 缓存最危险的失效场景：
// 噪声页被删除后，用户不能在缓存 TTL 内继续看到它。
// 这条断言保护的是"内容下线但还能搜到"，比"缓存没命中"严重得多。
func TestSearchCacheInvalidatedByIndexWrite(t *testing.T) {
	dir := t.TempDir()
	idx, err := index.Open(filepath.Join(dir, "bleve"))
	if err != nil {
		t.Fatal(err)
	}
	defer idx.Close()
	c := newSearchCache()

	q := index.Query{Q: "单元测试", Page: 1, Size: 10}

	// 第一次：空索引，命中 0
	hits, total, err := searchVia(idx, c, q)
	if err != nil {
		t.Fatal(err)
	}
	if total != 0 || len(hits) != 0 {
		t.Fatalf("空索引应 0 命中，实际 total=%d", total)
	}
	// 再查一次：应命中缓存（代数没变）
	if _, _, err := searchVia(idx, c, q); err != nil {
		t.Fatal(err)
	}
	if c.hits.Load() != 1 {
		t.Fatalf("第二次应命中缓存，hits=%d", c.hits.Load())
	}

	// 写入一篇文档：索引代数 +1，缓存必须整表作废
	if err := idx.IndexDocument(index.Doc{
		URL: "https://a.example/ut", Domain: "a.example",
		Title: "单元测试实践", Body: "单元测试能够独立运行、快速验证结果。",
	}); err != nil {
		t.Fatal(err)
	}
	hits, total, err = searchVia(idx, c, q)
	if err != nil {
		t.Fatal(err)
	}
	if total == 0 {
		t.Fatal("写入后必须能搜到新文档——缓存未失效就会漏掉它")
	}
	if len(hits) == 0 || hits[0].URL == "" {
		t.Fatalf("命中结果异常：%+v", hits)
	}

	// 删除后同样必须立刻消失
	if err := idx.DeleteDocument("https://a.example/ut"); err != nil {
		t.Fatal(err)
	}
	if _, total, err := searchVia(idx, c, q); err != nil {
		t.Fatal(err)
	} else if total != 0 {
		t.Fatalf("删除后仍在缓存里被搜到（total=%d）——用户会看到已下线的内容", total)
	}
}

// TestSearchCacheKeyIgnoresExcludeOrder 排除标签的顺序不影响语义，
// 不能因此各存一份（no=ai,ads 与 no=ads,ai 是同一查询）。
func TestSearchCacheKeyIgnoresExcludeOrder(t *testing.T) {
	c := newSearchCache()
	a := c.key(1, index.Query{Q: "q", Exclude: []string{"ai_suspect", "ugc_platform"}, Page: 1, Size: 10})
	b := c.key(1, index.Query{Q: "q", Exclude: []string{"ugc_platform", "ai_suspect"}, Page: 1, Size: 10})
	if a != b {
		t.Fatalf("排除项顺序不同应视为同一查询：%q vs %q", a, b)
	}
	// 反向：不同查询集不能撞 key
	other := c.key(1, index.Query{Q: "q", Exclude: []string{"ai_suspect"}, Page: 1, Size: 10})
	if a == other {
		t.Fatal("不同的排除项集合不应共用缓存条目")
	}
}

// TestSearchCacheDoesNotLeakMutation 缓存返回的切片被调用方修改后，
// 不能污染缓存里的原始结果（否则第二次查询会拿到被改过的 Score）。
func TestSearchCacheDoesNotLeakMutation(t *testing.T) {
	dir := t.TempDir()
	idx, err := index.Open(filepath.Join(dir, "bleve"))
	if err != nil {
		t.Fatal(err)
	}
	defer idx.Close()
	if err := idx.IndexDocument(index.Doc{
		URL: "https://a.example/p", Domain: "a.example",
		Title: "Go 并发模型", Body: "goroutine 与 channel 是 Go 并发的两块基石。",
	}); err != nil {
		t.Fatal(err)
	}
	c := newSearchCache()
	q := index.Query{Q: "go 并发", Page: 1, Size: 10}

	hits1, _, err := searchVia(idx, c, q)
	if err != nil || len(hits1) == 0 {
		t.Fatalf("首次检索失败: %v", err)
	}
	want := hits1[0].Score
	hits1[0].Score = -1 // 模拟排序层就地改写
	hits1[0].Title = "被改过"

	hits2, _, err := searchVia(idx, c, q)
	if err != nil || len(hits2) == 0 {
		t.Fatalf("二次检索失败: %v", err)
	}
	if hits2[0].Score != want {
		t.Fatalf("缓存被调用方污染：Score 从 %.3f 变成 %.3f", want, hits2[0].Score)
	}
}

// searchVia 复现 cachedSearch 的缓存路径（不启动 HTTP）。
func searchVia(idx *index.Index, c *searchCache, q index.Query) ([]index.Hit, int, error) {
	gen := idx.Version()
	key := c.key(gen, q)
	if e, ok := c.get(gen, key); ok {
		out := make([]index.Hit, len(e.hits))
		copy(out, e.hits)
		return out, e.total, nil
	}
	hits, total, err := idx.Search(q)
	if err != nil {
		return nil, 0, err
	}
	c.put(gen, key, hits, total)
	return hits, total, nil
}
