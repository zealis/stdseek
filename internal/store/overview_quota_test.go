package store

import (
	"context"
	"path/filepath"
	"testing"
)

// 配额计数：加、退、按天隔离、按天清理。
//
// 这几条里最容易写错的是「按天隔离」——如果只按 IP 存，
// 昨天的用量会压到今天头上，用户第二天一早就发现没额度了。
func TestOverviewQuota(t *testing.T) {
	ctx := context.Background()
	st, err := Open(filepath.Join(t.TempDir(), "q.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()

	const ip = "203.0.113.7"
	const today, tomorrow = "2026-10-30", "2026-10-31"

	// 第一次加：从无到 1
	for i := 1; i <= 3; i++ {
		n, err := st.OverviewQuotaTake(ctx, today, ip)
		if err != nil {
			t.Fatal(err)
		}
		if n != i {
			t.Fatalf("第 %d 次计数应为 %d，实际 %d", i, i, n)
		}
	}
	if n, err := st.OverviewQuotaPeek(ctx, today, ip); err != nil || n != 3 {
		t.Fatalf("peek=%d err=%v", n, err)
	}

	// 换一天从 0 开始
	if n, err := st.OverviewQuotaTake(ctx, tomorrow, ip); err != nil || n != 1 {
		t.Fatalf("新的一天应从 1 开始，got %d err=%v", n, err)
	}

	// 换 IP 互不影响
	if n, err := st.OverviewQuotaTake(ctx, today, "198.51.100.9"); err != nil || n != 1 {
		t.Fatalf("另一个 IP 应从 1 开始，got %d err=%v", n, err)
	}

	// 退回：只退这一天这一个 IP
	if err := st.OverviewQuotaRefund(ctx, today, ip); err != nil {
		t.Fatal(err)
	}
	if n, _ := st.OverviewQuotaPeek(ctx, today, ip); n != 2 {
		t.Fatalf("退款后应为 2，实际 %d", n)
	}

	// 退到 0 就不再往下走（不能出现负额度，那等于白送次数）
	for i := 0; i < 5; i++ {
		if err := st.OverviewQuotaRefund(ctx, today, ip); err != nil {
			t.Fatal(err)
		}
	}
	if n, _ := st.OverviewQuotaPeek(ctx, today, ip); n != 0 {
		t.Fatalf("不该退成负数，实际 %d", n)
	}

	// 清理：传今天只留今天
	if _, err := st.OverviewQuotaTake(ctx, "2026-01-01", ip); err != nil {
		t.Fatal(err)
	}
	if err := st.PruneOverviewQuota(ctx, today); err != nil {
		t.Fatal(err)
	}
	if n, _ := st.OverviewQuotaPeek(ctx, "2026-01-01", ip); n != 0 {
		t.Fatal("旧日期的计数应被清掉")
	}
	if n, _ := st.OverviewQuotaPeek(ctx, today, ip); n != 0 {
		t.Fatal("今天的记录不该被清理（已退到 0 但行还在，这里只验证不被误删）")
	}
}
