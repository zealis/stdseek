package reindex

import (
	"context"
	"path/filepath"
	"strconv"
	"testing"
	"time"

	"voidxo/internal/index"
	"voidxo/internal/kv"
	"voidxo/internal/processor"
	"voidxo/internal/store"
)

func TestReindexFromSnapshots(t *testing.T) {
	dir := t.TempDir()
	st, err := store.Open(filepath.Join(dir, "s.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	idx, err := index.Open(filepath.Join(dir, "bleve"))
	if err != nil {
		t.Fatal(err)
	}
	defer idx.Close()
	snap, err := kv.Open(filepath.Join(dir, "kv"))
	if err != nil {
		t.Fatal(err)
	}
	defer snap.Close()
	proc, err := processor.New()
	if err != nil {
		t.Fatal(err)
	}

	ctx := context.Background()
	if err := st.AddWhitelist(ctx, "blog.example", "blog", 86400); err != nil {
		t.Fatal(err)
	}
	// 旧文档：http 协议 + 三天前的抓取时间 + 无发布时间
	oldFetched := time.Now().Add(-72 * time.Hour).Unix()
	oldURL := "http://blog.example/a"
	if _, err := st.UpsertDocument(ctx, store.Document{
		URL: oldURL, Domain: "blog.example", Title: "旧标题", FetchedAt: oldFetched,
	}); err != nil {
		t.Fatal(err)
	}

	html := []byte(`<html><head><title>Reindex 测试页</title>
		<meta name="description" content="重建索引"></head>
		<body><article><p>reindex content 用于验证从 KV 快照重建索引。</p></article></body></html>`)
	if err := snap.Put(kv.Key("blog.example/a"), html); err != nil {
		t.Fatal(err)
	}

	stats, err := Run(ctx, st, idx, snap, proc, 2)
	if err != nil {
		t.Fatal(err)
	}
	if stats.Scanned != 1 || stats.Indexed != 1 || stats.Uncovered != 0 {
		t.Fatalf("stats=%+v", stats)
	}

	hits, total, err := idx.Search(index.Query{Q: "reindex", Size: 10})
	if err != nil {
		t.Fatal(err)
	}
	if total != 1 || len(hits) != 1 {
		t.Fatalf("total=%d hits=%d", total, len(hits))
	}
	// 协议应沿用 documents 中的原始 http URL
	if hits[0].URL != oldURL {
		t.Fatalf("url=%q want %q", hits[0].URL, oldURL)
	}
	// 无发布时间 → 按收录时间；三天前的收录不应进入「1 天」窗口
	if _, total, err := idx.Search(index.Query{Q: "reindex", SinceSec: 86400, Size: 10}); err != nil || total != 0 {
		t.Fatalf("1-day window total=%d err=%v want 0", total, err)
	}
	if _, total, err := idx.Search(index.Query{Q: "reindex", SinceSec: 7 * 86400, Size: 10}); err != nil || total != 1 {
		t.Fatalf("7-day window total=%d err=%v want 1", total, err)
	}

	// 元数据应保留原抓取时间，并重新打上 personal 标签（白名单分类 blog）
	doc, ok, err := st.GetDocument(ctx, oldURL)
	if err != nil || !ok {
		t.Fatalf("document missing: ok=%v err=%v", ok, err)
	}
	if doc.FetchedAt != oldFetched {
		t.Fatalf("fetched_at drifted: %d want %d", doc.FetchedAt, oldFetched)
	}
	if doc.Labels != `["personal"]` {
		t.Fatalf("labels=%s want [\"personal\"]", doc.Labels)
	}
}

func TestReindexEmptyKV(t *testing.T) {
	dir := t.TempDir()
	st, _ := store.Open(filepath.Join(dir, "s.db"))
	defer st.Close()
	idx, _ := index.Open(filepath.Join(dir, "bleve"))
	defer idx.Close()
	snap, _ := kv.Open(filepath.Join(dir, "kv"))
	defer snap.Close()
	proc, err := processor.New()
	if err != nil {
		t.Fatal(err)
	}
	stats, err := Run(context.Background(), st, idx, snap, proc, 2)
	if err != nil {
		t.Fatal(err)
	}
	if stats.Scanned != 0 || stats.Indexed != 0 {
		t.Fatalf("stats=%+v", stats)
	}
}

// TestReindexKeepsTitleFromTextSnapshot 事故回归：
// seed 语料的 KV 快照是纯文本（没有 HTML 结构），重新提取只会得到域名兜底，
// 绝不能把表里正确的标题覆盖掉。这条测试就是那次 134 篇降级事故的哨兵。
func TestReindexKeepsTitleFromTextSnapshot(t *testing.T) {
	dir := t.TempDir()
	st, _ := store.Open(filepath.Join(dir, "s.db"))
	defer st.Close()
	idx, _ := index.Open(filepath.Join(dir, "bleve"))
	defer idx.Close()
	snap, _ := kv.Open(filepath.Join(dir, "kv"))
	defer snap.Close()
	proc, _ := processor.New()

	ctx := context.Background()
	const u = "https://www.infoq.cn/minibook/xxx"
	if _, err := st.UpsertDocument(ctx, store.Document{
		URL: u, Domain: "www.infoq.cn", Title: "中国卓越技术团队访谈录",
	}); err != nil {
		t.Fatal(err)
	}
	// 纯文本快照：seed 导入的语料只有正文
	text := "企业动态 行业深度 大前端 后端 架构 AI 大数据 软件工程 云计算 发布于：2021-12-15 08:00" +
		"本期《中国卓越技术团队访谈录》精选了京东、优酷、网易数帆等技术团队在前沿技术探索方面的实践经验及心得体会。"
	if err := snap.Put(kv.Key("www.infoq.cn/minibook/xxx"), []byte(text)); err != nil {
		t.Fatal(err)
	}

	stats, err := Run(ctx, st, idx, snap, proc, 4)
	if err != nil {
		t.Fatal(err)
	}
	if stats.Indexed != 1 {
		t.Fatalf("stats=%+v", stats)
	}
	doc, ok, err := st.GetDocument(ctx, u)
	if err != nil || !ok {
		t.Fatalf("document missing: %v", err)
	}
	if doc.Title != "中国卓越技术团队访谈录" {
		t.Fatalf("标题被文本快照降级了：%q", doc.Title)
	}
	// 索引侧也必须一致（标题是 BM25 加权字段）
	hits, total, err := idx.Search(index.Query{Q: "卓越技术团队", Size: 5})
	if err != nil || total != 1 {
		t.Fatalf("total=%d err=%v", total, err)
	}
	if hits[0].Title != "中国卓越技术团队访谈录" {
		t.Fatalf("索引标题=%q", hits[0].Title)
	}
}

// TestReindexSubdomainCategory 子域名分类：白名单里是 example.com，
// 快照的 host 是 tech.example.com，标签判定必须仍然命中（复刻 SQL 最长后缀匹配）。
func TestReindexSubdomainCategory(t *testing.T) {
	dir := t.TempDir()
	st, _ := store.Open(filepath.Join(dir, "s.db"))
	defer st.Close()
	idx, _ := index.Open(filepath.Join(dir, "bleve"))
	defer idx.Close()
	snap, _ := kv.Open(filepath.Join(dir, "kv"))
	defer snap.Close()
	proc, _ := processor.New()

	ctx := context.Background()
	if err := st.AddWhitelist(ctx, "example.com", "blog", 86400); err != nil {
		t.Fatal(err)
	}
	const u = "https://tech.example.com/post/1"
	// 必须先有 documents 行：KV 有快照但表里没记录会被当孤儿跳过
	//（防已下架文档复活，见 reindex.go 注释）。
	if _, err := st.UpsertDocument(ctx, store.Document{
		URL: u, Domain: "tech.example.com", Title: "子域分类测试",
	}); err != nil {
		t.Fatal(err)
	}
	html := []byte(`<html><head><title>子域分类测试</title></head><body><article><p>` +
		`tech.example.com 是 example.com 的子域名，标签判定应命中白名单分类。` +
		`</p></article></body></html>`)
	if err := snap.Put(kv.Key("tech.example.com/post/1"), html); err != nil {
		t.Fatal(err)
	}

	if _, err := Run(ctx, st, idx, snap, proc, 2); err != nil {
		t.Fatal(err)
	}
	doc, ok, err := st.GetDocument(ctx, u)
	if err != nil || !ok {
		t.Fatalf("document missing: %v", err)
	}
	if doc.Labels != `["personal"]` {
		t.Fatalf("子域名未命中 blog 分类，labels=%s", doc.Labels)
	}
}

// TestReindexParallelKeepsAll 并行版不能丢任务：50 篇跑 4 个 worker，
// 全部进索引、元数据齐全。丢一篇就说明流水线有 race 或 channel 用法错误。
func TestReindexParallelKeepsAll(t *testing.T) {
	dir := t.TempDir()
	st, _ := store.Open(filepath.Join(dir, "s.db"))
	defer st.Close()
	idx, _ := index.Open(filepath.Join(dir, "bleve"))
	defer idx.Close()
	snap, _ := kv.Open(filepath.Join(dir, "kv"))
	defer snap.Close()
	proc, _ := processor.New()

	ctx := context.Background()
	const n = 50
	for i := 0; i < n; i++ {
		key := "example.com/p" + itoa(i)
		html := []byte(`<html><head><title>并行测试 ` + itoa(i) + `</title></head><body><article><p>` +
			"并行重建不能丢文档，这篇是第 " + itoa(i) + " 篇，讨论一致性哈希与分布式容错机制的具体实现。" +
			`</p></article></body></html>`)
		if err := snap.Put(kv.Key(key), html); err != nil {
			t.Fatal(err)
		}
		if _, err := st.UpsertDocument(ctx, store.Document{
			URL: "https://" + key, Domain: "example.com", Title: "t" + itoa(i),
		}); err != nil {
			t.Fatal(err)
		}
	}

	stats, err := Run(ctx, st, idx, snap, proc, 4)
	if err != nil {
		t.Fatal(err)
	}
	if stats.Scanned != n || stats.Indexed != n || stats.Errors != 0 {
		t.Fatalf("stats=%+v", stats)
	}
	for i := 0; i < n; i++ {
		if _, ok, err := st.GetDocument(ctx, "https://example.com/p"+itoa(i)); err != nil || !ok {
			t.Fatalf("第 %d 篇元数据缺失", i)
		}
	}
}

func itoa(i int) string { return strconv.Itoa(i) }
