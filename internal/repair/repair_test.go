package repair

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"voidxo/internal/index"
	"voidxo/internal/kv"
	"voidxo/internal/store"
	"voidxo/internal/urlkey"
)

// TestRepairSkipsFoldedAndFillsMissing 补齐的三条边界：
// 已折叠的副本不补（补回去等于撤销去重结论）、索引已有的不补、
// KV 没有快照的只计数不硬造。
func TestRepairSkipsFoldedAndFillsMissing(t *testing.T) {
	dir := t.TempDir()
	st, err := store.Open(filepath.Join(dir, "t.db"))
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
	ctx := context.Background()

	// 正文要超过 100 字，否则会被薄内容过滤挡掉（与 crawler 的 MinWordCount 对齐）
	html := `<!doctype html><html><head><title>单元测试实践</title></head><body><article>` +
		`<p>单元测试能够独立运行、快速验证结果，并且可以无限次重复执行，这是它与手工回归测试的本质区别。</p>` +
		`<p>在持续集成的环境里，单元测试是每次提交的第一道闸门：代码合入前跑完全部测试，` +
		`任何一条断言失败都会阻断合并，避免把缺陷带到主干分支上。</p>` +
		`<p>编写单元测试时应当遵循一个原则：测试的行为必须与实现解耦，只验证公开接口的输入输出，` +
		`这样重构内部结构时才不需要同步修改大量测试代码。</p>` +
		`</article></body></html>`

	now := time.Now().Unix()
	docs := []store.Document{
		{URL: "https://a.example/p1", Domain: "a.example", Title: "单元测试实践", WordCount: 100},
		{URL: "https://a.example/p2", Domain: "a.example", Title: "已折叠的副本"},
		{URL: "https://b.example/p3", Domain: "b.example", Title: "没有快照的"},
	}
	for _, d := range docs {
		d.FetchedAt = now
		if _, err := st.UpsertDocument(ctx, d); err != nil {
			t.Fatal(err)
		}
	}
	// p2 标记为副本（模拟 dedupe 的结论）
	if err := st.SetDupInfo(ctx, "https://a.example/p2", "https://a.example/p1", 0); err != nil {
		t.Fatal(err)
	}
	// 只有 p1 有 KV 快照
	if err := snap.Put(kv.Key(urlkey.Key("https://a.example/p1")), []byte(html)); err != nil {
		t.Fatal(err)
	}
	// 索引里只有 p2（副本，本就不该在索引里，这里只用来验证它被跳过）
	if err := idx.IndexDocument(index.Doc{
		URL: "https://a.example/p2", Domain: "a.example", Title: "已折叠的副本",
		Body:      "这是一段足够长的正文内容，用来让文档通过索引层的最小长度检查并占用索引位置。",
		FetchedAt: now,
	}); err != nil {
		t.Fatal(err)
	}

	stats, err := Run(ctx, st, idx, snap)
	if err != nil {
		t.Fatal(err)
	}
	// checked 只数非副本（p1、p3），p2 被跳过
	if stats.Checked != 2 {
		t.Fatalf("checked 应只数非副本：%d", stats.Checked)
	}
	if stats.Repaired != 1 {
		t.Fatalf("应补入 1 篇（p1）：%d", stats.Repaired)
	}
	if stats.NoSnapshot != 1 {
		t.Fatalf("p3 没有快照，应计数 1：%d", stats.NoSnapshot)
	}
	// p1 现在必须在索引里
	if has, err := idx.Has("https://a.example/p1"); err != nil || !has {
		t.Fatalf("p1 应已补入索引（has=%v err=%v）", has, err)
	}
	// 幂等：再跑一次不应重复补
	again, err := Run(ctx, st, idx, snap)
	if err != nil {
		t.Fatal(err)
	}
	if again.Repaired != 0 {
		t.Fatalf("第二次运行应全部命中（repaired=0），实际 %d", again.Repaired)
	}
}
