// Package dedup 做内容级去重：同一个页面被多个 URL 收录（版本镜像、转载、
// 采集站搬运）时，只保留一个「正本」，其余折叠掉。
//
// 为什么必须有：站内的 URL 归一化只能处理「同一 URL 的不同写法」，
// 处理不了「同一篇文章挂在两个地址上」。实测语料里 python 3.15/3.16 两个版本的
// 模块索引页正文几乎一致，三份都进了索引；不折叠的话同一个答案会在结果页
// 占多个位置，用户以为搜到了三个来源。
//
// 判定用 64 位 simhash 的汉明距离，分带（banding）只用来缩小比较范围——
// 301 篇时全量两两比较也只要几毫秒，但 10 万篇时是 50 亿次，必须靠分带粗筛。
package dedup

import (
	"context"
	"fmt"
	"math/bits"
	"strconv"

	"github.com/zealis/stdseek/internal/index"
	"github.com/zealis/stdseek/internal/store"
)

// MaxDistance 同站判定阈值。真实语料（301 篇）实测：
//
//	真重复：python 3.13/3.15/3.16 模块索引 d=0~2、php.net/index.php vs / d=0、
//	        nginx njs 索引 d=0、思否鸿蒙（五）两版 d=0
//	误  判：MDN「HTML 参考」vs「HTML 指南」d=3
//
// 即「同站不同页面」在这个指纹实现里就落在 d=3，所以同站阈值取 2。
const MaxDistance = 2

// CrossSiteDistance 跨站阈值。
//
// 与同站不同，跨站用的是正文 shingle 指纹（去标题、去停用词、保序），
// 因为采集站搬运必然改标题、加页脚，用带标题加权的词袋指纹根本认不出来。
//
// 阈值 6 的依据（337 篇真实语料、53517 个跨站对实测）：
//   - 词袋 Simhash（旧）：无关页面距离落在 2~10，真重复 0~3，**两者重叠**，
//     所以旧阈值只能压到 1——等于只承认「逐字节相同」的搬运。
//   - shingle（新）：无关页面全部 ≥14，模拟搬运（改标题、正文尾部加推广位）
//     ≤8，中间隔 6 位余量。
//
// 取 6 而不是 8：留一点保守空间，同时 6 < 8（分带段数）满足鸽笼条件，
// 保证「距离 ≤6 的一对」必有一段分带完全相同，粗筛不会漏。
const CrossSiteDistance = 6

// bandCountSame 同站分带段数。阈值 2 < 4，鸽笼成立。
const bandCountSame = 4

// bandCountCross 跨站分带段数。阈值 6 < 8，鸽笼成立。
const bandCountCross = 8

// distance 按站点关系选指纹：同站比词袋（版本镜像场景），跨站比 shingle（转载场景）。
func distance(a, b store.Fingerprint) int {
	if a.Domain == b.Domain {
		return Distance(a.Simhash, b.Simhash)
	}
	// 跨站走 shingle。任一为 0 必须显式挡掉：
	// 0 是「还没回填新指纹」的哨兵，而汉明距离下 0 与 0 的距离恰好也是 0 ——
	// 不挡的话，一次中断的 reindex 会把几千篇互不相关的文档判成同一篇的副本。
	// 真实事故：reindex 回填到一半时跑 dedupe，vuejs/react/rust/php/redis 的
	// 272 篇被整体折叠到一篇 PostgreSQL 7.1 文档上。
	if a.ShingleHash == 0 || b.ShingleHash == 0 {
		return 64 // 不可能小于任何阈值，等价于「不判重复」
	}
	return Distance(a.ShingleHash, b.ShingleHash)
}

// threshold 按是否同站返回距离阈值。
func threshold(a, b store.Fingerprint) int {
	if a.Domain == b.Domain {
		return MaxDistance
	}
	return CrossSiteDistance
}

// bandCount 兼容旧名（同站分带数）。
const bandCount = bandCountSame

// Bands 返回文档的 4 个分带键（同站粗筛用）。
func Bands(h uint64) [bandCountSame]string {
	var out [bandCountSame]string
	for i := 0; i < bandCountSame; i++ {
		out[i] = fmt.Sprintf("b%d:%04x", i, (h>>(16*i))&0xffff)
	}
	return out
}

// BandsN 把 64 位切成 n 段，返回每段的键。
//
// 段数决定可支持的最大阈值：距离 < n 时，n 段里至少有一段完全相同（鸽笼原理）。
// 跨站阈值 6 因此需要 8 段（每段 8 位）。
func BandsN(h uint64, n int) []string {
	width := 64 / n
	out := make([]string, n)
	for i := 0; i < n; i++ {
		shift := uint(i * width)
		mask := uint64(1)<<uint(width) - 1
		if i == n-1 {
			mask = ^uint64(0) >> shift // 最后一段收掉余数位
		}
		out[i] = fmt.Sprintf("b%d:%x", i, (h>>shift)&mask)
	}
	return out
}

// Distance 返回两个指纹的汉明距离。
func Distance(a, b uint64) int { return bits.OnesCount64(a ^ b) }

// Stats 一次去重的结果。
type Stats struct {
	Clusters int // 命中的重复簇数量
	Folded   int // 被折叠（从索引移除）的文档数
}

// Cluster 把文档聚成簇，返回「代表元下标 → 成员下标」（含单例簇）。
//
// 两段式：先用分带（LSH）粗筛出候选对，再用选定指纹的真实距离做判定。
// 同站与跨站各有一套指纹和阈值，桶键用前缀 s/h 区分。
//
// 为什么是纯函数、不碰数据库：采集站识别一直没有真实样本可验（白名单里没有
// CSDN/简书这类站点），所以要用合成的搬运对照集来量化召回率与误判率——
// 那要求能直接喂一批 Fingerprint 进来跑聚类。见 reprint_test.go。
func Cluster(docs []store.Fingerprint) map[int][]int {
	clusters := map[int][]int{}
	if len(docs) == 0 {
		return clusters
	}

	// 分带索引：band → 文档下标。
	// 两套指纹各建一套桶（前缀 s/h 区分）：同站候选靠词袋桶，跨站候选靠 shingle 桶。
	// 桶只是粗筛，真正的判定在 distance() 里按站点关系选指纹。
	byBand := make(map[string][]int, len(docs)*(bandCountSame+bandCountCross))
	for i, d := range docs {
		if d.Simhash != 0 {
			for k, b := range BandsN(d.Simhash, bandCountSame) {
				key := "s" + strconv.Itoa(k) + ":" + b
				byBand[key] = append(byBand[key], i)
			}
		}
		if d.ShingleHash != 0 {
			for k, b := range BandsN(d.ShingleHash, bandCountCross) {
				key := "h" + strconv.Itoa(k) + ":" + b
				byBand[key] = append(byBand[key], i)
			}
		}
	}

	parent := make([]int, len(docs))
	for i := range parent {
		parent[i] = i
	}
	var find func(int) int
	find = func(x int) int {
		for parent[x] != x {
			parent[x] = parent[parent[x]]
			x = parent[x]
		}
		return x
	}
	union := func(a, b int) {
		ra, rb := find(a), find(b)
		if ra != rb {
			parent[rb] = ra
		}
	}

	for i, d := range docs {
		if d.Simhash == 0 && d.ShingleHash == 0 {
			continue
		}
		seen := map[int]bool{}
		consider := func(j int) {
			if j <= i || seen[j] {
				return
			}
			seen[j] = true
			if distance(d, docs[j]) <= threshold(d, docs[j]) {
				union(i, j)
			}
		}
		if d.Simhash != 0 {
			for k, b := range BandsN(d.Simhash, bandCountSame) {
				for _, j := range byBand["s"+strconv.Itoa(k)+":"+b] {
					consider(j)
				}
			}
		}
		if d.ShingleHash != 0 {
			for k, b := range BandsN(d.ShingleHash, bandCountCross) {
				for _, j := range byBand["h"+strconv.Itoa(k)+":"+b] {
					consider(j)
				}
			}
		}
	}

	for i := range docs {
		r := find(i)
		clusters[r] = append(clusters[r], i)
	}
	return clusters
}

// Apply 扫描全部文档，把重复内容折叠到正本上。
//
// 前提：调用时所有文档都在索引里（crawl 或 reindex 之后）。这样「正本」被选中后
// 无需重新入索引，只需删除其余副本。若正本此前已被折叠过，说明上一轮的选择
// 与本轮不同，此时它会留在索引外——所以正本选择必须稳定：按发布时间、
// 正文长度、收录时间、URL 依次比较，不依赖遍历顺序。
func Apply(ctx context.Context, st *store.Store, idx *index.Index) (Stats, error) {
	var stats Stats
	docs, err := st.Fingerprints(ctx)
	if err != nil {
		return stats, err
	}
	if len(docs) < 2 {
		return stats, nil
	}

	clusters := Cluster(docs)

	for _, members := range clusters {
		if len(members) < 2 {
			// 单例簇要清掉上一轮的陈旧标记：上一轮被折叠、这一轮不再重复的文档
			// 已经由 reindex 重新入索引，标记不清就会「既在索引里、又写着是副本」。
			clearStale(ctx, st, docs[members[0]])
			continue
		}
		canonical := pickCanonical(docs, members)
		// 完全链接判定：单链聚类会把「A~B~C」但 A 与 C 距离 5 的文档并成一簇，
		// 只要求每个成员离正本够近才算重复。真实事故：nodejs 交互页通过中间文档
		// 被并进 go.dev FAQ 的簇，距离 5 仍被折叠。
		kept := []int{canonical}
		for _, m := range members {
			if m == canonical {
				continue
			}
			if distance(docs[m], docs[canonical]) > threshold(docs[m], docs[canonical]) {
				clearStale(ctx, st, docs[m])
				continue
			}
			kept = append(kept, m)
		}
		if len(kept) < 2 {
			continue
		}
		stats.Clusters++
		for _, m := range kept {
			if m == canonical {
				continue
			}
			if err := st.SetDupInfo(ctx, docs[m].URL, docs[canonical].URL, 0); err != nil {
				return stats, err
			}
			if err := idx.DeleteDocument(docs[m].URL); err != nil {
				return stats, err
			}
			stats.Folded++
		}
		if err := st.SetDupInfo(ctx, docs[canonical].URL, "", len(kept)-1); err != nil {
			return stats, err
		}
	}
	return stats, nil
}

// clearStale 清掉文档上一轮的副本标记（幂等，重复执行无副作用）。
func clearStale(ctx context.Context, st *store.Store, f store.Fingerprint) {
	_ = st.SetDupInfo(ctx, f.URL, "", 0)
}

// pickCanonical 选正本：越早发布越可能是源头；同时间取正文更长的
// （镜像页常被裁掉图片说明）；再相同取更早收录；最后用 URL 保证确定性。
func pickCanonical(docs []store.Fingerprint, members []int) int {
	best := members[0]
	for _, m := range members[1:] {
		if better(docs[m], docs[best]) {
			best = m
		}
	}
	return best
}

func better(a, b store.Fingerprint) bool {
	ap, bp := pubOrMax(a.PublishedAt), pubOrMax(b.PublishedAt)
	if ap != bp {
		return ap < bp
	}
	if a.WordCount != b.WordCount {
		return a.WordCount > b.WordCount
	}
	if a.FetchedAt != b.FetchedAt {
		return a.FetchedAt < b.FetchedAt
	}
	return a.URL < b.URL
}

// pubOrMax 把「没有发布时间」排到最后：宁可让有明确发布时间的当正本。
func pubOrMax(p *int64) int64 {
	if p == nil {
		return 1<<62 - 1
	}
	return *p
}
