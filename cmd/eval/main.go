// voidxo-eval 检索质量跑分工具。
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
	"encoding/json"
	"flag"
	"fmt"
	"os"

	"voidxo/internal/eval"
)

func main() {
	corpusPath := flag.String("corpus", "eval/corpus.jsonl", "语料 JSONL 路径")
	queriesPath := flag.String("queries", "eval/queries.jsonl", "查询集 JSONL 路径")
	jsonOut := flag.String("json", "", "把完整报告写入该 JSON 文件（可选）")
	verbose := flag.Bool("v", true, "逐条打印查询结果")
	flag.Parse()

	corpus, err := eval.LoadCorpus(*corpusPath)
	if err != nil {
		fmt.Fprintln(os.Stderr, "加载语料失败:", err)
		os.Exit(1)
	}
	queries, err := eval.LoadQueries(*queriesPath)
	if err != nil {
		fmt.Fprintln(os.Stderr, "加载查询集失败:", err)
		os.Exit(1)
	}
	if len(corpus) == 0 || len(queries) == 0 {
		fmt.Fprintln(os.Stderr, "语料或查询集为空")
		os.Exit(1)
	}

	rep, err := eval.Run(corpus, queries)
	if err != nil {
		fmt.Fprintln(os.Stderr, "跑分失败:", err)
		os.Exit(1)
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

func truncate(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n-1]) + "…"
}
