package api

import (
	"testing"
	"time"
)

// 导出节奏：从没导过要导，24 小时内不重导，超过就再导。
// 这条判断决定「对象存储上每天多一份 JSONL」还是「一天几十份」。
func TestDueExport(t *testing.T) {
	now := time.Date(2026, 10, 29, 12, 0, 0, 0, time.UTC)
	cases := []struct {
		last string
		want bool
	}{
		{"", true},
		{now.Add(-23 * time.Hour).Format(time.RFC3339), false},
		{now.Add(-24 * time.Hour).Format(time.RFC3339), true},
		{now.Add(-48 * time.Hour).Format(time.RFC3339), true},
		{"不是时间", true}, // 解析不了就重新导一次，不能因为一条脏值永远不导出
	}
	for _, c := range cases {
		if got := dueExport(c.last, now); got != c.want {
			t.Fatalf("dueExport(%q)=%v want %v", c.last, got, c.want)
		}
	}
}
