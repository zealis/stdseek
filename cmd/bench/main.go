// bench 索引规模压测：回答规划书风险 4——Bleve 在多大规模开始掉队。
//
// 只测「检索延迟 + 索引吞吐 + 体积」，不测排序（rank 是纯内存计算，
// 开销与候选数线性相关，与总文档量无关）。用真实语料的正文片段做合成文档，
// 不重复粘贴同一段文本——否则倒排表高度重复，压缩率与查询开销都会虚低。
package main

import (
	"bufio"
	"encoding/json"
	"flag"
	"fmt"
	"math/rand"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"voidxo/internal/index"
)

type seedDoc struct {
	URL         string   `json:"url"`
	Domain      string   `json:"domain"`
	Title       string   `json:"title"`
	Description string   `json:"description"`
	Body        string   `json:"body"`
	Labels      []string `json:"labels"`
}

func main() {
	var (
		n       = flag.Int("n", 1000, "合成文档数")
		corpus  = flag.String("corpus", "eval/corpus-real.jsonl", "种子语料")
		queries = flag.String("queries", "eval/queries-zh.jsonl,eval/queries-real.jsonl", "查询集（逗号分隔）")
		size    = flag.Int("size", 10, "每次检索返回条数")
		repeat  = flag.Int("repeat", 3, "每条查询重复次数（取最快一次，排除调度抖动）")
		batchN  = flag.Int("batch", 500, "批量写入的批大小（与 reindex 同路径）；0 表示逐条写入（与 crawler 同路径）")
		keep    = flag.Bool("keep", false, "保留临时索引目录")
		verbose = flag.Bool("v", false, "逐条打印慢查询")
		// dataDir 指定时直接打开已有索引测查询，不建合成索引。
		// 用途：对比"合成语料的命中集"与"真实抓取语料"的延迟差异。
		dataDir = flag.String("data", "", "打开已有 data 目录（只测查询，需先停服务）")
	)
	flag.Parse()

	if *dataDir != "" {
		measureExisting(*dataDir, *queries, *size, *repeat, *verbose)
		return
	}

	seeds, err := loadSeeds(*corpus)
	if err != nil {
		fail("加载种子语料失败: %v", err)
	}
	if len(seeds) == 0 {
		fail("种子语料为空")
	}
	qs, err := loadQueries(*queries)
	if err != nil {
		fail("加载查询集失败: %v", err)
	}

	dir, err := os.MkdirTemp("", "voidxo-bench-*")
	if err != nil {
		fail("创建临时目录失败: %v", err)
	}
	if !*keep {
		defer os.RemoveAll(dir)
	}

	fmt.Printf("规模 %d 篇 · 种子 %d 篇 · 查询 %d 条 · size=%d\n", *n, len(seeds), len(qs), *size)

	idx, err := index.Open(filepath.Join(dir, "bleve"))
	if err != nil {
		fail("打开索引失败: %v", err)
	}

	// ---- 索引 ----
	rng := rand.New(rand.NewSource(42))
	t0 := time.Now()
	var totalBodyBytes int
	// batch<=0 走逐条写入（与生产 crawler 同路径），>0 走批量（与 reindex 同路径）。
	// 两条路都测，才知道批量的收益是否真实存在。
	if *batchN > 0 {
		buf := make([]index.Doc, 0, *batchN)
		flush := func() {
			if len(buf) == 0 {
				return
			}
			if _, err := idx.IndexDocuments(buf); err != nil {
				fail("批量索引失败: %v", err)
			}
			buf = buf[:0]
		}
		for i := 0; i < *n; i++ {
			d := synth(seeds, i, rng)
			totalBodyBytes += len(d.Body)
			buf = append(buf, d)
			if len(buf) >= *batchN {
				flush()
			}
			if (i+1)%20000 == 0 {
				fmt.Printf("  已索引 %d/%d 篇（%.0fs）\n", i+1, *n, time.Since(t0).Seconds())
			}
		}
		flush()
	} else {
		for i := 0; i < *n; i++ {
			d := synth(seeds, i, rng)
			totalBodyBytes += len(d.Body)
			if err := idx.IndexDocument(d); err != nil {
				fail("索引第 %d 篇失败: %v", i, err)
			}
			if (i+1)%20000 == 0 {
				fmt.Printf("  已索引 %d/%d 篇（%.0fs）\n", i+1, *n, time.Since(t0).Seconds())
			}
		}
	}
	indexDur := time.Since(t0)
	idxSize := dirSize(dir)

	// ---- 查询 ----
	// 预热：Bleve 首次查询会打开 reader、建词典缓存，计入延迟会失真
	for i := 0; i < 20 && i < len(qs); i++ {
		_, _, _ = idx.Search(index.Query{Q: qs[i], Page: 1, Size: *size})
	}

	type slow struct {
		q    string
		ms   float64
		hits int
	}
	var lat []float64
	var slows []slow
	queryErrs := 0
	for _, q := range qs {
		best := time.Hour
		hits := 0
		for r := 0; r < *repeat; r++ {
			st := time.Now()
			h, total, err := idx.Search(index.Query{Q: q, Page: 1, Size: *size})
			d := time.Since(st)
			if err != nil {
				queryErrs++
				continue
			}
			if d < best {
				best = d
			}
			hits = total
			_ = h
		}
		if best == time.Hour {
			continue
		}
		ms := float64(best.Microseconds()) / 1000
		lat = append(lat, ms)
		slows = append(slows, slow{q: q, ms: ms, hits: hits})
	}
	sort.Float64s(lat)

	fmt.Println()
	fmt.Printf("索引：%.1fs（%.0f 篇/秒）  体积 %s（%s/篇）  正文合计 %.1f MB\n",
		indexDur.Seconds(), float64(*n)/indexDur.Seconds(),
		humanSize(idxSize), humanSize(idxSize/int64(max(1, *n))),
		float64(totalBodyBytes)/1024/1024)
	fmt.Printf("查询：P50 %.1fms · P95 %.1fms · P99 %.1fms · max %.1fms · 错误 %d\n",
		pct(lat, 0.50), pct(lat, 0.95), pct(lat, 0.99), pct(lat, 1.0), queryErrs)

	if *verbose {
		sort.Slice(slows, func(i, j int) bool { return slows[i].ms > slows[j].ms })
		fmt.Println("\n最慢查询：")
		for i, s := range slows {
			if i >= 8 {
				break
			}
			fmt.Printf("  %6.1fms  %-26s 命中 %d\n", s.ms, trunc(s.q, 26), s.hits)
		}
	}

	// 延迟到底由"总文档数"决定还是"命中集大小"决定？这个分桶能直接看出来。
	// 结论影响优化方向：若由总文档数决定，只能换引擎；若由命中数决定，
	// 优化手段是缓存高频查询、限制参与排序的候选数。
	fmt.Println("\n按命中集规模分桶：")
	type bucket struct {
		label  string
		lo, hi int
	}
	for _, b := range []bucket{
		{"命中 <100", 0, 100},
		{"100 ~ 1k", 100, 1000},
		{"1k ~ 1万", 1000, 10000},
		{">1万", 10000, 1 << 30},
	} {
		var sum float64
		var cnt int
		var worst float64
		for _, s := range slows {
			if s.hits >= b.lo && s.hits < b.hi {
				sum += s.ms
				cnt++
				if s.ms > worst {
					worst = s.ms
				}
			}
		}
		if cnt == 0 {
			continue
		}
		fmt.Printf("  %-12s %3d 条  平均 %6.1fms  最慢 %6.1fms\n", b.label, cnt, sum/float64(cnt), worst)
	}

	// 单字/高频词是最坏情况：命中集极大，Bleve 要扫全量 posting
	fmt.Println("\n最坏情况（高频词）：")
	for _, q := range []string{"go", "的", "使用", "安装"} {
		st := time.Now()
		_, total, err := idx.Search(index.Query{Q: q, Page: 1, Size: *size})
		ms := float64(time.Since(st).Microseconds()) / 1000
		if err != nil {
			fmt.Printf("  %-6s 失败: %v\n", q, err)
			continue
		}
		fmt.Printf("  %-6s %7.1fms  命中 %d\n", q, ms, total)
	}

	if err := idx.Close(); err != nil {
		fmt.Fprintf(os.Stderr, "关闭索引失败: %v\n", err)
	}
}

// synth 第 i 篇合成文档：从种子语料里取一段连续正文，长度与起点随 i 变化。
// 标题加序号只是为了 URL/ID 唯一，不改语义——检索仍按原词的分布走。
func synth(seeds []seedDoc, i int, rng *rand.Rand) index.Doc {
	s := seeds[i%len(seeds)]
	r := []rune(s.Body)
	body := s.Body
	if len(r) > 600 {
		// 起点与长度都随 i 变化，让同一篇种子在不同位置切成不同片段
		start := (i * 137) % (len(r) - 400)
		n := 300 + rng.Intn(700)
		if start+n > len(r) {
			n = len(r) - start
		}
		body = string(r[start : start+n])
	}
	domain := fmt.Sprintf("bench-%d.example", i%500)
	return index.Doc{
		URL:         fmt.Sprintf("https://%s/p/%d", domain, i),
		Domain:      domain,
		Title:       fmt.Sprintf("%s %d", s.Title, i),
		Description: s.Description,
		Body:        body,
		Labels:      s.Labels,
		Lang:        "zh",
		FetchedAt:   time.Now().Unix(),
	}
}

func loadSeeds(path string) ([]seedDoc, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	var out []seedDoc
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 1<<20), 8<<20)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" {
			continue
		}
		var d seedDoc
		if err := json.Unmarshal([]byte(line), &d); err != nil {
			return nil, err
		}
		if d.Body == "" {
			continue
		}
		out = append(out, d)
	}
	return out, sc.Err()
}

func loadQueries(paths string) ([]string, error) {
	var out []string
	for _, p := range strings.Split(paths, ",") {
		p = strings.TrimSpace(p)
		if p == "" {
			continue
		}
		f, err := os.Open(p)
		if err != nil {
			return nil, err
		}
		sc := bufio.NewScanner(f)
		sc.Buffer(make([]byte, 1<<20), 8<<20)
		for sc.Scan() {
			line := strings.TrimSpace(sc.Text())
			if line == "" {
				continue
			}
			var q struct {
				Q string `json:"q"`
			}
			if err := json.Unmarshal([]byte(line), &q); err != nil {
				f.Close()
				return nil, err
			}
			if q.Q != "" {
				out = append(out, q.Q)
			}
		}
		err = sc.Err()
		f.Close()
		if err != nil {
			return nil, err
		}
	}
	return out, nil
}

func dirSize(root string) int64 {
	var n int64
	_ = filepath.Walk(root, func(_ string, info os.FileInfo, err error) error {
		if err == nil && !info.IsDir() {
			n += info.Size()
		}
		return nil
	})
	return n
}

func pct(sorted []float64, p float64) float64 {
	if len(sorted) == 0 {
		return 0
	}
	i := int(float64(len(sorted)-1) * p)
	return sorted[i]
}

func humanSize(b int64) string {
	const unit = 1024
	if b < unit {
		return fmt.Sprintf("%d B", b)
	}
	div, exp := int64(unit), 0
	for n := b / unit; n >= unit; n /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f %cB", float64(b)/float64(div), "KMGTPE"[exp])
}

func trunc(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n]) + "…"
}

// fmtDur 输入纳秒，按量级选单位。
// 千篇级索引的查询在微秒级，一律显示 ms 会变成 0.00，看不出差别。
func fmtDur(ns float64) string {
	switch {
	case ns < 1e3:
		return fmt.Sprintf("%.0fns", ns)
	case ns < 1e6:
		return fmt.Sprintf("%.0fμs", ns/1e3)
	default:
		return fmt.Sprintf("%.2fms", ns/1e6)
	}
}

func max(a, b int) int {
	if a > b {
		return a
	}
	return b
}

func fail(format string, args ...interface{}) {
	fmt.Fprintf(os.Stderr, format+"\n", args...)
	os.Exit(1)
}

// measureExisting 打开已有索引（真实抓取的 data 目录）测查询延迟。
//
// 与合成语料的关键差别是**命中集规模**：合成语料是 337 篇文章的片段复制，
// 词汇分布集中，一个查询动辄命中几千上万；真实语料里不同站点主题分散，
// 命中集通常小一个数量级。而延迟主要由命中集决定，所以这组数字才是日常表现。
func measureExisting(dataDir, queryPaths string, size, repeat int, verbose bool) {
	dir := dataDir
	if st, err := os.Stat(filepath.Join(dataDir, "bleve")); err != nil || !st.IsDir() {
		fail("%s 下没有 bleve 索引（需先抓取）", dataDir)
	}
	qs, err := loadQueries(queryPaths)
	if err != nil {
		fail("加载查询集失败: %v", err)
	}
	idx, err := index.Open(filepath.Join(dir, "bleve"))
	if err != nil {
		fail("打开索引失败（服务是否在运行？Bleve 独占）: %v", err)
	}
	defer idx.Close()

	fmt.Printf("真实索引 %s · 查询 %d 条 · size=%d\n", filepath.Join(dir, "bleve"), len(qs), size)
	fmt.Printf("索引体积 %s\n\n", humanSize(dirSize(filepath.Join(dir, "bleve"))))

	for i := 0; i < 20 && i < len(qs); i++ {
		_, _, _ = idx.Search(index.Query{Q: qs[i], Page: 1, Size: size})
	}

	type record struct {
		q    string
		ms   float64
		hits int
	}
	var recs []record
	var lat []float64
	// 每条查询跑 iter 次取总耗时平均。
	// 不用「单次计时取最小」：千篇级索引的检索在几十微秒，
	// Windows 上 Go 的计时精度不够，单次采样会得出 0ns 这种无法解释的数。
	const iter = 20
	for _, q := range qs {
		it := iter
		st := time.Now()
		for k := 0; k < it; k++ {
			if _, _, err := idx.Search(index.Query{Q: q, Page: 1, Size: size}); err != nil {
				it--
			}
		}
		d := time.Since(st)
		if it <= 0 {
			continue
		}
		_, hits, _ := idx.Search(index.Query{Q: q, Page: 1, Size: size})
		ns := float64(d.Nanoseconds()) / float64(it)
		lat = append(lat, ns)
		recs = append(recs, record{q: q, ms: ns, hits: hits})
	}
	sort.Float64s(lat)
	fmt.Printf("查询：P50 %s · P95 %s · P99 %s · max %s\n",
		fmtDur(pct(lat, 0.50)), fmtDur(pct(lat, 0.95)), fmtDur(pct(lat, 0.99)), fmtDur(pct(lat, 1.0)))

	// 命中集分布：这是与合成语料对比的核心指标
	var hitCounts []int
	for _, r := range recs {
		hitCounts = append(hitCounts, r.hits)
	}
	sort.Ints(hitCounts)
	if len(hitCounts) > 0 {
		fmt.Printf("命中集：P50 %d · P95 %d · max %d\n",
			hitCounts[len(hitCounts)/2], hitCounts[len(hitCounts)*95/100], hitCounts[len(hitCounts)-1])
	}

	fmt.Println("\n按命中集规模分桶：")
	type bucket struct {
		label  string
		lo, hi int
	}
	for _, b := range []bucket{
		{"命中 <100", 0, 100},
		{"100 ~ 1k", 100, 1000},
		{"1k ~ 1万", 1000, 10000},
		{">1万", 10000, 1 << 30},
	} {
		var sum, worst float64
		cnt := 0
		for _, r := range recs {
			if r.hits >= b.lo && r.hits < b.hi {
				sum += r.ms
				cnt++
				if r.ms > worst {
					worst = r.ms
				}
			}
		}
		if cnt == 0 {
			continue
		}
		fmt.Printf("  %-12s %3d 条  平均 %8s  最慢 %8s\n", b.label, cnt, fmtDur(sum/float64(cnt)), fmtDur(worst))
	}

	if verbose {
		sort.Slice(recs, func(i, j int) bool { return recs[i].ms > recs[j].ms })
		fmt.Println("\n最慢查询：")
		for i, r := range recs {
			if i >= 8 {
				break
			}
			fmt.Printf("  %8s  命中 %6d  %s\n", fmtDur(r.ms), r.hits, trunc(r.q, 30))
		}
	}
}
