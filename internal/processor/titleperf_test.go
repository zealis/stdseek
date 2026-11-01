package processor

import (
	"fmt"
	"testing"
	"time"

	"github.com/zealis/stdseek/internal/kv"
)

// TestDocTitleCost 量 docTitle 在大快照上的成本：全量扫描 vs 只看开头 64KB。
//
// 背景：给 SPA 标题增强时直接对整份快照跑正则，1137 篇的重建从 93 秒
// 变成 14 分钟。这条测试把差距固定成数字，防止将来又被"顺手扫全量"拖慢。
func TestDocTitleCost(t *testing.T) {
	st, err := kv.Open("../../data/kv")
	if err != nil {
		t.Skip("无法打开 data/kv:", err)
	}
	defer st.Close()

	type snap struct {
		key string
		raw []byte
	}
	var all []snap
	_ = st.Scan(kv.Key(""), func(key string, val []byte) error {
		if len(val) > 200<<10 { // 只留 >200KB 的，头条成本才明显
			all = append(all, snap{key: key, raw: val})
		}
		return nil
	})
	if len(all) < 3 {
		t.Skip("大快照样本不足")
	}
	if len(all) > 8 {
		all = all[:8]
	}

	var fullNs, boundedNs int64
	for _, s := range all {
		// 全量
		t0 := time.Now()
		const n = 5
		for i := 0; i < n; i++ {
			m := docTitleRe.FindSubmatch(s.raw)
			_ = m
		}
		full := time.Since(t0).Nanoseconds() / n

		// 限 64KB
		t1 := time.Now()
		for i := 0; i < n; i++ {
			_ = docTitle(s.raw)
		}
		bounded := time.Since(t1).Nanoseconds() / n

		fullNs += full
		boundedNs += bounded
		t.Logf("%-46s %5dKB  全量 %7.2fms  限64KB %6.3fms  快 %.0f 倍",
			s.key[:min(46, len(s.key))], len(s.raw)>>10,
			float64(full)/1e6, float64(bounded)/1e6, float64(full)/float64(max(bounded, 1)))
	}
	t.Logf("合计：全量 %.1fms vs 限64KB %.2fms（%d 篇）",
		float64(fullNs)/1e6, float64(boundedNs)/1e6, len(all))
	if fullNs > 0 && boundedNs*10 > fullNs {
		t.Errorf("限界后没有显著收益（全量 %dns vs 限界 %dns），说明瓶颈不在正则扫描", fullNs, boundedNs)
	}
	_ = fmt.Sprint()
}
