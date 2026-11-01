package api

import (
	"sort"
	"strings"
	"testing"

	"github.com/zealis/stdseek/internal/kv"
)

// BenchmarkRealSnapshot 用 data/kv 里的真实快照测剥标签。
// 构造的样本（67KB 规整 HTML）只能说明正则的常态开销，
// 真实站点页面有深层嵌套、超长属性、注释与 CDATA，正则行为完全不同。
func BenchmarkRealSnapshot(b *testing.B) {
	store, err := kv.Open("../../data/kv")
	if err != nil {
		b.Skip("无法打开 data/kv:", err)
	}
	defer store.Close()

	type snap struct {
		key string
		raw string
	}
	var all []snap
	_ = store.Scan(kv.Key(""), func(key string, val []byte) error {
		all = append(all, snap{key: key, raw: string(val)})
		return nil
	})
	if len(all) == 0 {
		b.Skip("data/kv 为空")
	}
	// 按大小降序，取最大的 6 篇——线上结果页的耗时由最大那几个决定，
	// 取平均值会低估（均值 72KB，但个别站点快照能到几百万字节）。
	sort.Slice(all, func(i, j int) bool { return len(all[i].raw) > len(all[j].raw) })
	snaps := all[:6]
	for i, s := range snaps {
		b.Logf("第 %d 大: %d 字节 %s", i+1, len(s.raw), strings.TrimPrefix(s.key, "page:"))
	}

	total := 0
	for _, s := range snaps {
		total += len(s.raw)
	}
	b.Logf("%d 篇真实快照，合计 %d 字节，平均 %d 字节/篇", len(snaps), total, total/len(snaps))
	// 只剥标签
	b.Run("strip", func(b *testing.B) {
		for n := 0; n < b.N; n++ {
			for _, s := range snaps {
				_ = htmlToText(s.raw)
			}
		}
	})
	// 完整片段构建（含剥标签 + 窗口扫描）
	b.Run("buildSnippets", func(b *testing.B) {
		for n := 0; n < b.N; n++ {
			for _, s := range snaps {
				_ = buildSnippets(s.raw, "单元测试", 2)
			}
		}
	})
	// 剥完标签后的纯文本规模
	totalChars := 0
	for _, s := range snaps {
		totalChars += len([]rune(htmlToText(s.raw)))
	}
	b.Logf("剥完标签平均 %d 字符/篇", totalChars/len(snaps))
}

var errStopScan = errStop{}

type errStop struct{}

func (errStop) Error() string { return "stop scan" }
