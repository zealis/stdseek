package sitecat

import "testing"

// 最短分类间隔决定调度器的扫描粒度：它如果比 -crawl-every 还小，
// 设了 30 分钟的站点就永远等不到 30 分钟那次扫描。
// 这条测试的意义是把「改分类默认值」和「调度周期够不够细」绑在一起。
func TestMinInterval(t *testing.T) {
	got := MinInterval()
	if got != 1800 {
		t.Fatalf("最短分类间隔 = %d 秒，期望 1800（新闻阅读）。"+
			"改了这个值要同步检查 cmd/stdseek 的 -crawl-every 默认值", got)
	}

	// 必须是真实存在的最小值，不是随手写的常量
	min := DefaultInterval
	for _, c := range List {
		if c.Interval > 0 && c.Interval < min {
			min = c.Interval
		}
	}
	if got != min {
		t.Fatalf("MinInterval 与遍历结果不一致：%d vs %d", got, min)
	}
}
