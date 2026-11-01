// stdseek-eval 检索质量跑分工具。
//
// 用法：
//
//	go run ./cmd/eval -corpus eval/corpus.jsonl -queries eval/queries.jsonl
//
// 语料行格式（JSONL）：
//
//	{"url":"...","domain":"...","category":"official","title":"...","description":"...",
//	 "body":"...","labels":["ai_suspect"],"published_days_ago":5,"prosperity":12000}
//
// 查询行格式（JSONL，相关性分级：2 高度相关 / 1 相关）：
//
//	{"q":"crashloopbackoff 排查","relevant":{"https://a":2,"https://b":1}}
//
// 指标：Recall@20 / NDCG@10 / MRR@10 / 无结果率 / P95 延迟（毫秒）。
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/zealis/stdseek/internal/embed"
	"github.com/zealis/stdseek/internal/eval"
	"github.com/zealis/stdseek/internal/index"
	"github.com/zealis/stdseek/internal/store"
)

func main() {
	corpusPath := flag.String("corpus", "eval/corpus.jsonl", "语料 JSONL 路径")
	queriesPath := flag.String("queries", "eval/queries.jsonl", "查询集 JSONL 路径")
	dataDir := flag.String("data", "", "直接评测既有索引所在数据目录（口径与线上一致；需先停止服务，与 -corpus 二选一）")
	jsonOut := flag.String("json", "", "把完整报告写入该 JSON 文件（可选）")
	verbose := flag.Bool("v", true, "逐条打印查询结果")
	// 向量检索对照实验开关：给出模型目录才会跑 bm25/vector/hybrid 三组对比。
	// 语义向量只有在「词汇召回不到的查询」上才有价值，所以要并排看三组，
	// 而不是只看混合总分——否则无法判断增益来自补召回还是排序抖动。
	modelDir := flag.String("model", "", "embedding 模型目录；给出则跑向量检索对照实验（需同时用 -corpus）")
	modes := flag.String("modes", "bm25,vector,hybrid", "对照实验的模式，逗号分隔")
	vecW := flag.Float64("vecw", 1.0, "语义相似度权重（rank.Weights.Vector）")
	docTokens := flag.Int("doctok", 256, "文档向量化的最大 token 数")
	maxBody := flag.Int("maxbody", 400, "文档正文参与向量化的最大字符数（0 表示不限）")
	vecHitsN := flag.Int("vechits", 50, "向量召回补充的候选条数")
	vecCache := flag.String("veccache", "tmpdata/vec-cache.json", "语料向量缓存路径（调参时避免每次重算）；空串禁用")
	flag.Parse()

	queries, err := eval.LoadQueries(*queriesPath)
	if err != nil {
		fmt.Fprintln(os.Stderr, "加载查询集失败:", err)
		os.Exit(1)
	}
	if len(queries) == 0 {
		fmt.Fprintln(os.Stderr, "查询集为空")
		os.Exit(1)
	}

	var rep eval.Report
	if *dataDir != "" {
		// 既有索引口径：文档与排序信号都来自线上存储，跑分即线上表现
		ctx := context.Background()
		st, err := store.OpenDefault(*dataDir)
		if err != nil {
			fmt.Fprintln(os.Stderr, "打开元数据库失败:", err)
			os.Exit(1)
		}
		defer st.Close()
		idx, err := index.Open(filepath.Join(*dataDir, "bleve"))
		if err != nil {
			fmt.Fprintln(os.Stderr, "打开索引失败（服务是否仍在运行？Bleve 为单进程独占）:", err)
			os.Exit(1)
		}
		defer idx.Close()
		// 标注文档可能已被治理下线（例如聚合页清理），那种情况下指标天然偏低，
		// 不是检索退化——直接提示出来，避免对着错的目标调排序。
		if missing := eval.MissingTargets(idx, queries); len(missing) > 0 {
			fmt.Fprintf(os.Stderr, "警告：%d 个标注文档不在索引中（标注已过期，指标会被低估）：\n", len(missing))
			for _, m := range missing {
				fmt.Fprintln(os.Stderr, "  ", m)
			}
		}
		rep, err = eval.RunWithIndex(ctx, idx, st, queries, time.Now())
		if err != nil {
			fmt.Fprintln(os.Stderr, "跑分失败:", err)
			os.Exit(1)
		}
	} else {
		corpus, err := eval.LoadCorpus(*corpusPath)
		if err != nil {
			fmt.Fprintln(os.Stderr, "加载语料失败:", err)
			os.Exit(1)
		}
		if len(corpus) == 0 {
			fmt.Fprintln(os.Stderr, "语料为空")
			os.Exit(1)
		}
		if *modelDir != "" {
			if err := runVectorModes(corpus, queries, *verbose, *modelDir, *modes, *vecW, *docTokens, *vecHitsN, *maxBody, *vecCache); err != nil {
				fmt.Fprintln(os.Stderr, "向量对照实验失败:", err)
				os.Exit(1)
			}
			return
		}
		rep, err = eval.Run(corpus, queries)
		if err != nil {
			fmt.Fprintln(os.Stderr, "跑分失败:", err)
			os.Exit(1)
		}
	}

	if *verbose {
		fmt.Printf("%-28s %5s %6s %7s %6s %8s\n", "查询", "命中", "首相关", "Recall", "NDCG", "耗时ms")
		for _, d := range rep.Details {
			fmt.Printf("%-28s %5d %6d %7.2f %6.2f %8.2f\n",
				truncate(d.Query, 26), d.Hits, d.FirstRel, d.Recall20, d.NDCG10, d.LatencyMS)
		}
		fmt.Println()
	}

	fmt.Printf("查询数: %d\n", rep.Queries)
	fmt.Printf("Recall@20: %.3f\n", rep.Recall20)
	fmt.Printf("NDCG@10:   %.3f\n", rep.NDCG10)
	fmt.Printf("MRR@10:    %.3f\n", rep.MRR10)
	fmt.Printf("无结果率:  %.1f%%\n", rep.NoResultRate*100)
	fmt.Printf("P95 延迟:  %.2f ms\n", rep.P95MS)

	if *jsonOut != "" {
		b, err := json.MarshalIndent(rep, "", "  ")
		if err != nil {
			fmt.Fprintln(os.Stderr, "序列化报告失败:", err)
			os.Exit(1)
		}
		if err := os.WriteFile(*jsonOut, b, 0o644); err != nil {
			fmt.Fprintln(os.Stderr, "写入报告失败:", err)
			os.Exit(1)
		}
		fmt.Println("报告已写入:", *jsonOut)
	}
}

// runVectorModes 并排跑多模式对比，打印对照表。
func runVectorModes(corpus []eval.CorpusDoc, queries []eval.Query, verbose bool,
	modelDir, modeList string, vecW float64, docTokens, vecHits, maxBody int, cachePath string) error {
	var modes []eval.Mode
	for _, s := range strings.Split(modeList, ",") {
		if s = strings.TrimSpace(s); s != "" {
			modes = append(modes, eval.Mode(s))
		}
	}
	if len(modes) == 0 {
		return fmt.Errorf("-modes 为空")
	}
	fmt.Printf("加载模型 %s ...\n", modelDir)
	m, err := embed.Load(modelDir)
	if err != nil {
		return err
	}
	fmt.Printf("语料 %d 篇，文档向量截断 %d token ...\n", len(corpus), docTokens)
	reps, err := eval.RunModes(context.Background(), corpus, queries, m, modes, vecW, vecHits, docTokens, maxBody, cachePath, time.Now())
	if err != nil {
		return err
	}
	fmt.Printf("\n%-8s %10s %10s %10s %9s %10s\n", "模式", "Recall@20", "NDCG@10", "MRR@10", "无结果率", "P95ms")
	for _, md := range modes {
		r := reps[string(md)]
		fmt.Printf("%-8s %10.3f %10.3f %10.3f %8.1f%% %10.2f\n",
			md, r.Recall20, r.NDCG10, r.MRR10, r.NoResultRate*100, r.P95MS)
	}
	// 逐条列出「最后一个模式」相对基线的变化，定位被救回与被带偏的查询
	base, ok := reps[string(eval.ModeBM25)]
	var last eval.Mode
	for _, md := range modes {
		if md != eval.ModeBM25 {
			last = md
		}
	}
	cur, ok2 := reps[string(last)]
	if ok && ok2 && verbose {
		fmt.Printf("\n%s 模式相对纯词汇的变化（按 NDCG 差值排序）：\n", last)
		type delta struct {
			q string
			d float64
			r float64
		}
		var ds []delta
		for i, d := range cur.Details {
			if i >= len(base.Details) {
				break
			}
			ds = append(ds, delta{d.Query, d.NDCG10 - base.Details[i].NDCG10, d.Recall20 - base.Details[i].Recall20})
		}
		sort.Slice(ds, func(i, j int) bool { return ds[i].d < ds[j].d })
		for _, d := range ds {
			mark := " "
			if d.d > 0.001 {
				mark = "+"
			} else if d.d < -0.001 {
				mark = "-"
			}
			fmt.Printf("  %s %-34s NDCG %+0.3f  Recall %+0.3f\n", mark, truncate(d.q, 32), d.d, d.r)
		}
	}
	return nil
}

func truncate(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n-1]) + "…"
}
