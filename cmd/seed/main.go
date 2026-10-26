// voidxo-seed 用评测语料造一份"看起来像线上"的数据目录，用于端到端装配验证。
//
//	go run ./cmd/seed -corpus eval/corpus-real.jsonl -data ./data
//
// 为什么要它：`cmd/eval -corpus` 只在临时目录建 Bleve 索引，SQLite 与 KV 不参与，
// 排序信号（域名分类 / 繁荣度）也是直接从语料内存构造的。那条路径验证不了
// 「SQLite + Bleve + KV 三件套串起来」的真实装配，也验证不了 `-data` 口径
// 与 /api/search 的完整链路。
//
// 写入内容与线上 reindex 一致：documents 元数据进 SQLite、文档进 Bleve、
// 正文快照进 KV（/api/page 的证据链依赖它）；语料里的 category/prosperity
// 分别写进 whitelist 与 prosperity 表，让 DomainCategories/ProsperityScores
// 能像线上一样读出来。
package main

import (
	"context"
	"flag"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"time"

	"voidxo/internal/eval"
	"voidxo/internal/index"
	"voidxo/internal/kv"
	"voidxo/internal/store"
	"voidxo/internal/urlkey"
)

func main() {
	corpusPath := flag.String("corpus", "eval/corpus-real.jsonl", "语料 JSONL 路径")
	dataDir := flag.String("data", "./data", "输出数据目录（与 -data 口径一致）")
	flag.Parse()

	corpus, err := eval.LoadCorpus(*corpusPath)
	if err != nil {
		fmt.Fprintln(os.Stderr, "加载语料失败:", err)
		os.Exit(1)
	}
	if len(corpus) == 0 {
		fmt.Fprintln(os.Stderr, "语料为空")
		os.Exit(1)
	}
	if err := os.MkdirAll(*dataDir, 0o755); err != nil {
		fmt.Fprintln(os.Stderr, "创建目录失败:", err)
		os.Exit(1)
	}

	ctx := context.Background()
	now := time.Now()

	st, err := store.Open(filepath.Join(*dataDir, "voidxo.db"))
	if err != nil {
		fmt.Fprintln(os.Stderr, "打开元数据库失败（服务是否在运行？SQLite 需独占）:", err)
		os.Exit(1)
	}
	defer st.Close()

	idx, err := index.Open(filepath.Join(*dataDir, "bleve"))
	if err != nil {
		fmt.Fprintln(os.Stderr, "打开索引失败（服务是否在运行？Bleve 需独占）:", err)
		os.Exit(1)
	}
	defer idx.Close()

	snap, err := kv.Open(filepath.Join(*dataDir, "kv"))
	if err != nil {
		fmt.Fprintln(os.Stderr, "打开快照库失败:", err)
		os.Exit(1)
	}
	defer snap.Close()

	cats := map[string]string{}
	pros := map[string]float64{}
	var docCount int
	for _, d := range corpus {
		pub := now.AddDate(0, 0, -d.PublishedDaysAgo).Unix()
		labels := d.Labels
		if labels == nil {
			labels = []string{}
		}
		// domain 缺省从 URL 推：内网文档导入时逐条手填域名是没必要的摩擦，
		// 而漏填会静默降级（域名为空 → 同域多样性、繁荣度、站点分类全部失效）。
		if d.Domain == "" {
			if u, err := url.Parse(d.URL); err == nil {
				d.Domain = u.Hostname()
			}
		}
		if d.Domain == "" {
			fmt.Fprintf(os.Stderr, "跳过 %s：无法从 URL 解析域名，请显式提供 domain 字段\n", d.URL)
			continue
		}
		if err := idx.IndexDocument(index.Doc{
			URL: d.URL, Domain: d.Domain, Title: d.Title, Description: d.Description,
			Body: d.Body, Labels: labels, PublishedAt: &pub, FetchedAt: now.Unix(),
			Prosperity: d.Prosperity,
		}); err != nil {
			fmt.Fprintf(os.Stderr, "索引 %s 失败: %v\n", d.URL, err)
			os.Exit(1)
		}
		labelJSON := "[]"
		if len(labels) > 0 {
			labelJSON = toJSON(labels)
		}
		if _, err := st.UpsertDocument(ctx, store.Document{
			URL: d.URL, Domain: d.Domain, Title: d.Title, Description: d.Description,
			Labels: labelJSON, WordCount: len([]rune(d.Body)),
			PublishedAt: &pub, FetchedAt: now.Unix(),
		}); err != nil {
			fmt.Fprintf(os.Stderr, "写元数据 %s 失败: %v\n", d.URL, err)
			os.Exit(1)
		}
		// 正文快照：/api/page 的证据链从这里取原文
		if d.Body != "" {
			if err := snap.Put(kv.Key(urlKeyOf(d.URL)), []byte(d.Body)); err != nil {
				fmt.Fprintf(os.Stderr, "写快照 %s 失败: %v\n", d.URL, err)
				os.Exit(1)
			}
		}
		if d.Category != "" {
			cats[d.Domain] = d.Category
		}
		if d.Prosperity > 0 {
			pros[d.Domain] = d.Prosperity
		}
		docCount++
	}

	// 白名单：DomainCategories 从 whitelist 表读，缺了分类信号质量分就全丢
	for domain, cat := range cats {
		if err := st.AddWhitelist(ctx, domain, cat, 86400); err != nil {
			// 已存在不算错误
			fmt.Fprintf(os.Stderr, "警告：写白名单 %s 失败（可能已存在）: %v\n", domain, err)
		}
	}
	// 繁荣度：ProsperityScores 从 prosperity 表读
	if len(pros) > 0 {
		rows := make([]store.ProsperityRow, 0, len(pros))
		for domain, score := range pros {
			rows = append(rows, store.ProsperityRow{Key: domain, Score: score})
		}
		if err := st.ReplaceProsperity(ctx, rows); err != nil {
			fmt.Fprintf(os.Stderr, "写繁荣度失败: %v\n", err)
			os.Exit(1)
		}
	}

	fmt.Printf("完成：%d 篇文档 → %s（SQLite %d 站点分类 / %d 条繁荣度 / KV 快照）\n",
		docCount, *dataDir, len(cats), len(pros))
}

// urlKeyOf 归一化 URL 作为快照 key（与 crawler/reindex 写快照的口径一致）。
func urlKeyOf(raw string) string { return urlkey.Key(raw) }

// toJSON 极简字符串数组序列化，避免为一个字段引入编码依赖的分支逻辑。
func toJSON(xs []string) string {
	b := make([]byte, 0, len(xs)*8)
	b = append(b, '[')
	for i, x := range xs {
		if i > 0 {
			b = append(b, ',')
		}
		b = append(b, '"')
		b = append(b, []byte(x)...)
		b = append(b, '"')
	}
	b = append(b, ']')
	return string(b)
}
