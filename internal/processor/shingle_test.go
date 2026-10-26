package processor

import (
	"math/bits"
	"strings"
	"testing"
)

// TestShingleOnlyCoversLeadingTokens 指纹只覆盖正文前 shingleMaxTokens 个词。
//
// 这是采集站识别能成立的前提：采集站的改动集中在末尾（加推广页脚、截断），
// 正文主体一字不动。实测（对照集 reprint_test.go）加上限前加页脚只能召回 5/8，
// 加上限后 8/8，距离归零。
//
// 同时它也是一条边界声明：开头被改写时指纹会变，那是设计之内——
// 「改了开头的内容」本来就不该被当成同一篇。
func TestShingleOnlyCoversLeadingTokens(t *testing.T) {
	p, err := New()
	if err != nil {
		t.Fatal(err)
	}
	body := strings.TrimSpace(strings.Repeat(
		"分布式系统中的一致性哈希把节点和数据映射到一个虚拟环上 节点加入或退出时只影响相邻区间的数据迁移 ",
		40))

	orig := shingleHash(p.cut(body))

	withFooter := body + "\n\n本文由技术搬运工整理发布 转载请注明出处 更多精彩内容请关注公众号某某技术"
	if got := bits.OnesCount64(orig ^ shingleHash(p.cut(withFooter))); got != 0 {
		t.Errorf("末尾追加推广位后距离应为 0，实际 %d", got)
	}

	truncated := body[:len([]rune(body))*8/10]
	if got := bits.OnesCount64(orig ^ shingleHash(p.cut(truncated))); got != 0 {
		t.Errorf("截断末尾后距离应为 0，实际 %d", got)
	}

	// 开头插入一段导语：距离仍应小于跨站阈值（6），即照样判为重复。
	// 采集站常在正文前加「编者按 / 本文转载自」，只要主体没动就该认出来；
	// 实测插入约 30 词（占前 100 词的三成）距离 5 —— Simhash 的位投票
	// 对局部位移本就不敏感，而这在语义上是对的：两篇内容确实几乎相同。
	rewritten := "这是一段完全不同的开头文字讲的是数据库索引与查询优化的话题 " + body
	if got := bits.OnesCount64(orig ^ shingleHash(p.cut(rewritten))); got == 0 || got > 6 {
		t.Errorf("开头插入导语后距离应在 1~6（仍判重复），实际 %d", got)
	}

	// 换掉开头主体：距离必须明显变大，否则指纹对内容不敏感，
	// 会连「只在开头撞车」的无关文档都折到一起。
	swapped := "机器学习中的梯度下降通过反向传播逐层更新参数 学习率决定了每一步的步长 " +
		strings.Repeat("损失函数衡量预测值与真实值之间的差距 正则化项抑制过拟合 ", 30) +
		body[len([]rune(body))/2:]
	if got := bits.OnesCount64(orig ^ shingleHash(p.cut(swapped))); got < 10 {
		t.Errorf("换掉开头主体后距离应明显变大，实际 %d", got)
	}
}

// TestShingleHashNEmpty 空输入返回 0 哨兵：dedup 里 0 表示「尚未回填」，
// 已在 distance() 入口显式挡掉（见 dedup.go 的零值哨兵说明）。
func TestShingleHashNEmpty(t *testing.T) {
	if got := shingleHashN(nil, 100); got != 0 {
		t.Errorf("空输入应返回 0，实际 %d", got)
	}
}
