// stdseek-tune 排序权重网格扫描。
//
//	go run ./cmd/tune -corpus eval/corpus-real.jsonl \
//	     -queries zh=eval/queries-zh.jsonl -queries real=eval/queries-real.jsonl
//
// 一次建索引，对网格里每组权重重跑排序（不重建索引），输出综合分最高的若干组。
// 综合分 = 2×min(各组 NDCG@10) + Σ(各组 NDCG@10)，防止权重偏向条数多的那组。
//
// 注意过拟合：查询集只有 100 条，网格扫描挑出的"最优"很可能吃到标注噪声。
// 输出里同时给出两组的分项指标，选权重时看是否两组同时改善，而不是只看总分。
package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"sort"
	"strings"
	"time"

	"github.com/zealis/stdseek/internal/eval"
	"github.com/zealis/stdseek/internal/rank"
)

func main() {
	corpusPath := flag.String("corpus", "eval/corpus-real.jsonl", "语料 JSONL 路径")
	top := flag.Int("top", 15, "打印综合分最高的前 N 组")
	sortBy := flag.String("sortby", "score", "排序依据：score 综合分 / 组名（只按该组 NDCG 排，用于留一组交叉验证）")
	all := flag.Bool("all", false, "打印全部候选（按分数降序）")
	grid := flag.String("grid", "coarse", "候选网格：coarse 粗扫 324 组 / fine 细化 40 组（看时效等单档敏感度）")
	folds := flag.Int("folds", 0, "k 折交叉验证：每折用其余折挑权重、在留出折上对比默认权重（0 表示不做）")
	seed := flag.Int64("seed", 20261024, "交叉验证分折随机种子")
	var qf queryFlags
	flag.Var(&qf, "queries", "查询集，格式 组名=路径，可重复；默认 zh/real 两组")
	flag.Parse()

	corpus, err := eval.LoadCorpus(*corpusPath)
	if err != nil {
		fmt.Fprintln(os.Stderr, "加载语料失败:", err)
		os.Exit(1)
	}
	groups := map[string][]eval.Query{}
	names := qf.names()
	if len(names) == 0 {
		names = []string{"zh", "real"}
		for _, n := range names {
			qf.set(n, "eval/queries-"+n+".jsonl")
		}
	}
	for _, n := range names {
		p := qf.path(n)
		qs, err := eval.LoadQueries(p)
		if err != nil {
			fmt.Fprintln(os.Stderr, "加载查询集失败:", err)
			os.Exit(1)
		}
		groups[n] = qs
	}

	cands := eval.WeightsGrid()
	if *grid == "fine" {
		cands = eval.WeightsFine()
	}
	fmt.Printf("语料 %d 篇，候选权重 %d 组，查询集 %d 个\n", len(corpus), len(cands), len(groups))
	for _, n := range names {
		fmt.Printf("  %s: %d 条\n", n, len(groups[n]))
	}

	if *folds > 1 {
		if err := crossValidate(corpus, groups, names, cands, *folds, *seed); err != nil {
			fmt.Fprintln(os.Stderr, "交叉验证失败:", err)
			os.Exit(1)
		}
		return
	}

	start := time.Now()
	res, err := eval.RunWeightSweep(context.Background(), corpus, groups, cands, time.Now())
	if err != nil {
		fmt.Fprintln(os.Stderr, "扫描失败:", err)
		os.Exit(1)
	}
	if *sortBy != "score" {
		// 留一组交叉验证：只用某一组挑权重，再看它在另一组上的表现。
		// 用两组的总分挑出来的最优等于用掉了两组的信息，不能证明泛化。
		g := *sortBy
		sort.Slice(res, func(i, j int) bool { return res[i].ByGroup[g].NDCG10 > res[j].ByGroup[g].NDCG10 })
	} else {
		sort.Slice(res, func(i, j int) bool { return res[i].Score > res[j].Score })
	}
	fmt.Printf("扫描耗时 %.1fs\n\n", time.Since(start).Seconds())

	// 表头：先列各组指标，再列综合分
	fmt.Printf("%-52s", "权重")
	for _, n := range names {
		fmt.Printf("%14s", n+" Recall/NDCG")
	}
	fmt.Printf("%10s\n", "综合分")
	limit := *top
	if *all || limit > len(res) {
		limit = len(res)
	}
	for _, r := range res[:limit] {
		fmt.Printf("%-52s", eval.FormatWeights(r.W))
		for _, n := range names {
			rep := r.ByGroup[n]
			fmt.Printf("%14s", fmt.Sprintf("%.3f/%.3f", rep.Recall20, rep.NDCG10))
		}
		fmt.Printf("%10.4f\n", r.Score)
	}

	// 基线对照：默认权重在网格里的位置
	fmt.Println("\n对照（当前默认权重）：")
	w := rank.DefaultWeights()
	for _, r := range res {
		if sameWeights(r.W, w) {
			fmt.Printf("%-52s", eval.FormatWeights(r.W))
			for _, n := range names {
				rep := r.ByGroup[n]
				fmt.Printf("%14s", fmt.Sprintf("%.3f/%.3f", rep.Recall20, rep.NDCG10))
			}
			fmt.Printf("%10.4f", r.Score)
			rank := 1
			for _, o := range res {
				if o.Score > r.Score {
					rank++
				}
			}
			fmt.Printf("   （第 %d/%d 名）\n", rank, len(res))
			break
		}
	}
}

// crossValidate k 折交叉验证：每折只用"其余折"挑权重，再在留出的折上
// 与默认权重正面对比。
//
// 为什么必须有这一步：网格里 324 组权重在 100 条查询上挑出的最优，
// NDCG 差异只有 0.007~0.013，完全落在小样本的噪声范围内。
// 只有"调参挑出来的权重在没参与调参的折上也赢"才算真提升。
func crossValidate(corpus []eval.CorpusDoc, groups map[string][]eval.Query,
	names []string, cands []rank.Weights, k int, seed int64) error {
	ctx := context.Background()
	now := time.Now()
	// 每组独立分折（分层），避免某一折里全是术语桥查询
	foldsOf := map[string][][]eval.Query{}
	for _, n := range names {
		foldsOf[n] = eval.SplitFolds(groups[n], k, seed)
	}

	fmt.Printf("\n%d 折交叉验证（seed=%d）：用其余 %d 折挑权重，在留出折上验证\n", k, seed, k-1)
	fmt.Printf("%-6s %-52s %26s %26s\n", "折", "本折挑出的权重", "调参权重 NDCG", "默认权重 NDCG")
	base := rank.DefaultWeights()
	var winTuned, winBase, tie float64
	for f := 0; f < k; f++ {
		train := map[string][]eval.Query{}
		test := map[string][]eval.Query{}
		for _, n := range names {
			train[n] = eval.MergeFolds(foldsOf[n], f)
			test[n] = foldsOf[n][f]
		}
		res, err := eval.RunWeightSweep(ctx, corpus, train, cands, now)
		if err != nil {
			return err
		}
		best := res[0]
		for _, r := range res {
			if r.Score > best.Score {
				best = r
			}
		}
		got, err := eval.RunWeightSweep(ctx, corpus, test, []rank.Weights{best.W, base}, now)
		if err != nil {
			return err
		}
		tn, bn := got[0], got[1]
		d := tn.Score - bn.Score
		switch {
		case d > 1e-9:
			winTuned++
		case d < -1e-9:
			winBase++
		default:
			tie++
		}
		fmt.Printf("%-6d %-52s %26.4f %26.4f  %+0.4f\n", f+1, eval.FormatWeights(best.W), tn.Score, bn.Score, d)
	}
	fmt.Printf("\n调参权重胜 %0.0f 折 / 默认权重胜 %0.0f 折 / 持平 %0.0f 折\n", winTuned, winBase, tie)
	if winTuned <= winBase {
		fmt.Println("结论：调参挑出的权重在留出折上不占优 —— 视为过拟合，不改动默认权重。")
	} else {
		fmt.Println("结论：调参挑出的权重在留出折上占优，可以采信（仍需人工看两组是否同时改善）。")
	}
	return nil
}

func sameWeights(a, b rank.Weights) bool {
	return a.BM25 == b.BM25 && a.Exact == b.Exact && a.Prosperity == b.Prosperity &&
		a.Freshness == b.Freshness && a.Quality == b.Quality &&
		a.HalfLifeDays == b.HalfLifeDays && a.DomainDecay == b.DomainDecay
}

// queryFlags 解析 -queries zh=path 形式的重复 flag。
type queryFlags struct {
	order []string
	m     map[string]string
}

func (q *queryFlags) String() string { return strings.Join(q.order, ",") }

func (q *queryFlags) Set(v string) error {
	parts := strings.SplitN(v, "=", 2)
	if len(parts) != 2 {
		return fmt.Errorf("-queries 需要 组名=路径 格式，得到 %q", v)
	}
	q.set(parts[0], parts[1])
	return nil
}

func (q *queryFlags) set(name, path string) {
	if q.m == nil {
		q.m = map[string]string{}
	}
	if _, ok := q.m[name]; !ok {
		q.order = append(q.order, name)
	}
	q.m[name] = path
}

func (q *queryFlags) names() []string { return q.order }

func (q *queryFlags) path(name string) string { return q.m[name] }
