package reindex

import (
	"context"
	"path/filepath"
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

	stats, err := Run(ctx, st, idx, snap, proc)
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
	stats, err := Run(context.Background(), st, idx, snap, proc)
	if err != nil {
		t.Fatal(err)
	}
	if stats.Scanned != 0 || stats.Indexed != 0 {
		t.Fatalf("stats=%+v", stats)
	}
}
