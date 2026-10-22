package processor

import (
	"hash/fnv"
	"math/bits"
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
