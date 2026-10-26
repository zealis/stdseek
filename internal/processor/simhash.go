package processor

import (
	"hash/fnv"
	"math/bits"
	"strings"
)

// Simhash 64 位内容指纹：用于转载/采集站去重（对应规划书「simhash 自研 200 行」）。
// 算法：对每个词取 64 位哈希，按词频加权，逐位投票；1 多则该位为 1。

// Simhash 计算 tokens 的加权 simhash。weights 与 tokens 等长，值越大越重要
// （标题词应高于正文词）。
func Simhash(tokens []string, weights []float64) uint64 {
	if len(tokens) == 0 {
		return 0
	}
	var vector [64]float64
	h := fnv.New64a()
	for i, tok := range tokens {
		if tok == "" {
			continue
		}
		h.Reset()
		h.Write([]byte(tok))
		sum := h.Sum64()
		w := 1.0
		if i < len(weights) {
			w = weights[i]
		}
		for b := 0; b < 64; b++ {
			if sum&(1<<uint(b)) != 0 {
				vector[b] += w
			} else {
				vector[b] -= w
			}
		}
	}
	var out uint64
	for b := 0; b < 64; b++ {
		if vector[b] > 0 {
			out |= 1 << uint(b)
		}
	}
	return out
}

// Hamming 返回两个指纹的汉明距离（0-64）。
func Hamming(a, b uint64) int {
	return bits.OnesCount64(a ^ b)
}

// DuplicateThreshold 近重复判定阈值。
// 业界常取 3，但那是针对数百词的长文档；实测 30 词文档仅改一个词就会翻转 4 位，
// 因此这里放宽到 6，避免短文档漏判（无关文档通常差 20 位以上）。
var DuplicateThreshold = 6

// NearDuplicate 判断是否为近重复内容。
func NearDuplicate(a, b uint64) bool {
	return Hamming(a, b) <= DuplicateThreshold
}

// ---------- 正文 shingle 指纹（跨站转载判定） ----------

// shingleStopwords 去停用词表。只收「没有信息量」的虚词：
// 技术文档里「内存」「请求」「数据」这类常见但承载语义的词一旦删掉，
// 两篇讲不同主题的文章反而会更像——这是手工停用词表最常见的翻车方式。
var shingleStopwords = func() map[string]bool {
	m := make(map[string]bool, 128)
	for _, w := range strings.Fields(`的 了 是 在 和 与 或 也 就 都 而 及 其 这 那 有 我 你 他 它
		一个 一些 我们 他们 可以 如果 因为 所以 但是 然后 这个 那个 什么 怎么 如何 已经 还是 只是
		the a an of to in for on with and or is are was were be been it this that these those
		as at by from not no you we they he she can will would should if then than
		use used using when which what how all any but its their our`) {
		m[w] = true
	}
	return m
}()

// ShingleN shingle 长度（词数）。
// 取 3 是权衡：2-gram 区分度不够（两篇讲同一主题的文章会大量重合），
// 5-gram 对局部改写太敏感（末尾加一段推荐位就能让指纹大幅漂移）。
const ShingleN = 3

// shingleHash 由正文词序列算 3-gram shingle 指纹。
//
// 与 Simhash 的分工（实测于 337 篇真实语料，跨站两两比较 53517 对）：
//   - Simhash（词袋 + 标题加权）：无关页面距离落在 2~10，真重复也在 0~3 —— 两者重叠，
//     阈值只能压到 1，等于只认「逐字节相同」的搬运。
//   - shingleHash（去标题 + 去停用词 + 保序）：无关页面全部 ≥14，模拟搬运（改标题、加页脚）
//     ≤8 —— 中间隔了 6 位，阈值可以放到 8。
//
// 不用标题是因为采集站必改标题；不用词袋是因为词袋丢掉了顺序，
// 「同一批术语、不同论述」的两篇文章在词袋下几乎无法区分。
// shingleMaxTokens 参与指纹计算的正文词数上限（只取前 100 个词）。
//
// 为什么设上限：合成对照集实测（24 篇真实文档 × 3 种搬运手法，见 reprint_test.go）
// 全篇 shingle 对「末尾追加」过于敏感——采集站最典型的两种手法
// （加推广页脚、截断末尾）距离分别是 9 和 14，而跨站阈值只有 6，
// 加页脚端到端只能召回 5/8。而这两类改动都发生在正文末尾，不影响开头。
//
// 上限扫描（正例=搬运变体距离，越小越好；负例=无关文档距离，越大越好）：
//
//	上限    加页脚  截末尾  插广告  负例最小  同站最小
//	不限       9      14      8       20       24
//	400        9      14      9       23       26
//	100        0       0     11       22       24
//
// 取 100：两种最主要的搬运手法距离归零（完全免疫，端到端召回 8/8），
// 且区分度**不降反升**——「同站最小」是同站点文档的困难负例（同系列、写法雷同），
// 100 词下仍为 24，说明前 100 个词足以区分同系列文档，
// 担心的「开头套话撞车」在实测中没有发生。
//
// 代价：认不出段落重排/洗稿（距离 8~16）。那是保序指纹的固有边界，
// 要认出来得换 MinHash 采样类方案，不在这条路的范围内。
const shingleMaxTokens = 100

// shingleHash 由正文词序列算 3-gram shingle 指纹（只取前 shingleMaxTokens 个词）。
// maxTokens <= 0 表示不限（仅测试/对照用）。
func shingleHash(bodyTokens []string) uint64 {
	return shingleHashN(bodyTokens, shingleMaxTokens)
}

func shingleHashN(bodyTokens []string, maxTokens int) uint64 {
	if len(bodyTokens) == 0 {
		return 0
	}
	// 先裁词再去停用词：上限是「正文前 N 个词」，不是「去停用词后的前 N 个」。
	// 后者会让停用词多的文档实际覆盖更长的正文，等价于上限随文档浮动。
	if maxTokens > 0 && len(bodyTokens) > maxTokens {
		bodyTokens = bodyTokens[:maxTokens]
	}
	clean := make([]string, 0, len(bodyTokens))
	for _, t := range bodyTokens {
		if !shingleStopwords[t] {
			clean = append(clean, t)
		}
	}
	if len(clean) < ShingleN {
		return Simhash(clean, nil)
	}
	grams := make([]string, 0, len(clean)-ShingleN+1)
	for i := 0; i+ShingleN <= len(clean); i++ {
		grams = append(grams, strings.Join(clean[i:i+ShingleN], "\x1f"))
	}
	return Simhash(grams, nil)
}
