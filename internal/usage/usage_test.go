package usage

import (
	"context"
	"path/filepath"
	"testing"

	"voidxo/internal/store"
)

func newStore(t *testing.T) *store.Store {
	t.Helper()
	st, err := store.Open(filepath.Join(t.TempDir(), "usage.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	return st
}

// 累加 → flush → 读回：三个计数各自独立，Hits 是累加值而不是次数。
func TestTrackerAddAndFlush(t *testing.T) {
	st := newStore(t)
	tr := New(st)

	tr.AddSearch(8, true, false)
	tr.AddSearch(3, true, true)
	tr.AddCrawl(12, 9)
	tr.AddSummary()
	tr.AddSummary()

	// 还没有落盘
	rows, err := st.ListUsage(context.Background(), "2000-01-01")
	if err != nil {
		t.Fatalf("list before flush: %v", err)
	}
	if len(rows) != 0 {
		t.Fatalf("flush 前不该有记录，得到 %d 行", len(rows))
	}

	if err := tr.Flush(context.Background()); err != nil {
		t.Fatalf("flush: %v", err)
	}

	rows, err = st.ListUsage(context.Background(), "2000-01-01")
	if err != nil {
		t.Fatalf("list after flush: %v", err)
	}
	if len(rows) != 1 {
		t.Fatalf("应有 1 行，得到 %d 行", len(rows))
	}
	got := rows[0]
	if got.Searches != 2 || got.Succeeded != 2 || got.API != 1 {
		t.Fatalf("检索计数不对: %+v", got)
	}
	if got.Hits != 11 || got.Crawls != 12 || got.Indexed != 9 || got.Summaries != 2 {
		t.Fatalf("其它计数不对: %+v", got)
	}

	// flush 之后再累加：应该叠加到同一天，而不是新起一行
	tr.AddSearch(1, false, false)
	if err := tr.Flush(context.Background()); err != nil {
		t.Fatalf("second flush: %v", err)
	}
	rows, _ = st.ListUsage(context.Background(), "2000-01-01")
	if len(rows) != 1 {
		t.Fatalf("同一天应仍为 1 行，得到 %d 行", len(rows))
	}
	if rows[0].Searches != 3 || rows[0].Succeeded != 2 || rows[0].Hits != 12 {
		t.Fatalf("第二次 flush 后应为 3 请求 / 2 命中 / 12 结果，得到 %+v", rows[0])
	}
}

// Snapshot 自己会先 flush，调用方不需要手动刷。
func TestSnapshotFlushesFirst(t *testing.T) {
	tr := New(newStore(t))
	tr.AddSearch(5, true, false)

	rows, err := tr.Snapshot(context.Background(), 7)
	if err != nil {
		t.Fatalf("snapshot: %v", err)
	}
	if len(rows) != 1 || rows[0].Searches != 1 || rows[0].Hits != 5 || rows[0].Succeeded != 1 {
		t.Fatalf("snapshot 应包含刚累加的数据，得到 %+v", rows)
	}
}

// nil 累加器（没接数据库/测试）不能 panic：调用点分布在检索与抓取热路径上。
func TestNilTrackerIsNoop(t *testing.T) {
	var tr *Tracker
	tr.AddSearch(1, false, false)
	tr.AddCrawl(1, 1)
	tr.AddSummary()
	if err := tr.Flush(context.Background()); err != nil {
		t.Fatalf("nil tracker flush: %v", err)
	}

	empty := New(nil)
	empty.AddSearch(1, true, false)
	rows, err := empty.Snapshot(context.Background(), 7)
	if err != nil || rows != nil {
		t.Fatalf("未接数据库时应返回空，得到 %v / %v", rows, err)
	}
}
