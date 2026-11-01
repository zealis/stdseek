package eval

import (
	"context"
	"fmt"
	"math/rand"
	"os"
	"path/filepath"
	"sort"
	"time"

	"github.com/zealis/stdseek/internal/index"
	"github.com/zealis/stdseek/internal/rank"
)

// SweepResult 一组权重在两个查询集上的表现。
type SweepResult struct {
	W       rank.Weights
	ByGroup map[string]Report // 组名（zh / real）→ 报告
	Score   float64           // 选择用的综合分，见 scoreOf
	MinNDCG float64           // 两组里较差的那个 NDCG，防止偏科
	SumNDCG float64
}

// RunWeightSweep 一次建索引、多组权重复用，扫描排序权重。
//
// 为什么不逐组调 cmd/eval：每组都要重建一次 Bleve 索引（337 篇约 15s），
// 扫 300 组就是 75 分钟；这里建一次索引后只重跑排序，一组 100 条查询约 0.3s。
func RunWeightSweep(ctx context.Context, corpus []CorpusDoc, groups map[string][]Query,
	cands []rank.Weights, now time.Time) ([]SweepResult, error) {
	dir, err := os.MkdirTemp("", "stdseek-tune-")
	if err != nil {
		return nil, err
	}
	defer os.RemoveAll(dir)

	idx, err := index.Open(filepath.Join(dir, "bleve"))
	if err != nil {
		return nil, err
	}
	defer idx.Close()

	categories := map[string]string{}
	prosperity := map[string]float64{}
	for _, d := range corpus {
		pub := now.AddDate(0, 0, -d.PublishedDaysAgo).Unix()
		if err := idx.IndexDocument(index.Doc{
			URL: d.URL, Domain: d.Domain, Title: d.Title, Description: d.Description,
			Body: d.Body, Labels: d.Labels, PublishedAt: &pub, FetchedAt: now.Unix(),
			Prosperity: d.Prosperity,
		}); err != nil {
			return nil, err
		}
		if d.Category != "" {
			categories[d.Domain] = d.Category
		}
		if d.Prosperity > 0 {
			prosperity[d.Domain] = d.Prosperity
		}
	}
	sig := func(context.Context, []index.Hit) rank.Signals {
		return rank.Signals{Categories: categories, Prosperity: prosperity}
	}

	// 组名排序，保证输出稳定可复现
	names := make([]string, 0, len(groups))
	for n := range groups {
		names = append(names, n)
	}
	sort.Strings(names)

	out := make([]SweepResult, 0, len(cands))
	for _, w := range cands {
		sr := SweepResult{W: w, ByGroup: map[string]Report{}}
		for _, n := range names {
			rep, err := runQueriesWith(ctx, idx, groups[n], sig, w, now)
			if err != nil {
				return nil, err
			}
			sr.ByGroup[n] = rep
			sr.SumNDCG += rep.NDCG10
			if sr.MinNDCG == 0 || rep.NDCG10 < sr.MinNDCG {
				sr.MinNDCG = rep.NDCG10
			}
		}
		sr.Score = scoreOf(sr)
		out = append(out, sr)
	}
	return out, nil
}

// scoreOf 权重选择的目标函数。
//
// 用 min(两组 NDCG) 为主、和为辅：只优化总和会让权重偏向条数多的那组
// （纯中文 61 条 vs 术语桥 39 条），把术语桥牺牲掉换总分上涨——
// 正是本轮要修的"偏科"问题。乘 2 再相加是为了让 min 主导、和只作同分时的 tie-break。
func scoreOf(sr SweepResult) float64 {
	return sr.MinNDCG*2 + sr.SumNDCG
}

// WeightsGrid 生成权重候选网格。维度刻意保持少量：
// 查询集只有 100 条，网格太密必然过拟合到标注噪声。
func WeightsGrid() []rank.Weights {
	var out []rank.Weights
	for _, bm25 := range []float64{0.5, 1.0, 1.5, 2.0} {
		for _, exact := range []float64{1.0, 2.0, 3.0} {
			for _, pros := range []float64{0.25, 0.5, 1.0} {
				for _, fresh := range []float64{0.3, 0.6, 1.0} {
					for _, qual := range []float64{0.2, 0.4, 0.8} {
						w := rank.DefaultWeights()
						w.BM25 = bm25
						w.Exact = exact
						w.Prosperity = pros
						w.Freshness = fresh
						w.Quality = qual
						out = append(out, w)
					}
				}
			}
		}
	}
	return out
}

// SplitFolds 把查询集按 seed 打乱后切成 k 折（尽量均等）。
//
// 用于交叉验证：只在一部分折上挑权重、在留出的折上验证。
// 查询集只有 100 条，网格扫描挑出的"最优"很容易吃到标注噪声，
// 不交叉验证就无法区分"真提升"和"过拟合"。
func SplitFolds(queries []Query, k int, seed int64) [][]Query {
	if k <= 1 {
		return [][]Query{queries}
	}
	q := make([]Query, len(queries))
	copy(q, queries)
	rng := rand.New(rand.NewSource(seed))
	rng.Shuffle(len(q), func(i, j int) { q[i], q[j] = q[j], q[i] })
	folds := make([][]Query, k)
	for i, item := range q {
		folds[i%k] = append(folds[i%k], item)
	}
	return folds
}

// MergeFolds 把除 skip 之外的折合并成一个查询集（skip<0 表示全合并）。
func MergeFolds(folds [][]Query, skip int) []Query {
	var out []Query
	for i, f := range folds {
		if i == skip {
			continue
		}
		out = append(out, f...)
	}
	return out
}

// WeightsFine 粗扫之后的细化网格：固定粗扫收敛的方向（BM25=1、Exact 偏低），
// 只细调时效与两个质量项。
//
// 单独存在的理由：粗扫最优取 Freshness=1.0，但它会让「10 天前、相关性只有一半」
// 的文档压过「120 天前、相关性满分」的文档——时效从加分项变成主导项，
// 与设计意图不符。这里逐档看时效的敏感度，好在指标与语义之间取平衡。
func WeightsFine() []rank.Weights {
	var out []rank.Weights
	for _, exact := range []float64{1.0, 1.5} {
		for _, pros := range []float64{0.25, 0.5} {
			for _, fresh := range []float64{0.3, 0.4, 0.6, 0.8, 1.0} {
				for _, qual := range []float64{0.2, 0.4} {
					w := rank.DefaultWeights()
					w.BM25 = 1.0
					w.Exact = exact
					w.Prosperity = pros
					w.Freshness = fresh
					w.Quality = qual
					out = append(out, w)
				}
			}
		}
	}
	return out
}

// FormatWeights 紧凑打印一组权重，便于复制回 DefaultWeights。
func FormatWeights(w rank.Weights) string {
	return fmt.Sprintf("BM25=%.2f Exact=%.2f Pros=%.2f Fresh=%.2f Qual=%.2f HL=%v Decay=%.2f",
		w.BM25, w.Exact, w.Prosperity, w.Freshness, w.Quality, w.HalfLifeDays, w.DomainDecay)
}
