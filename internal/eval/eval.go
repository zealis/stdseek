// Package eval 提供检索质量评测：读取语料与查询集，构建临时索引，
// 用真实 rank 管线跑分，输出 Recall@20 / NDCG@10 / MRR@10 / 无结果率 / P95。
//
// 语料与查询格式见 cmd/eval 与 eval/*.jsonl；相关性为分级标注（2 高度相关 / 1 相关）。
package eval

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"sort"
	"time"

	"voidxo/internal/index"
	"voidxo/internal/rank"
	"voidxo/internal/store"
	"voidxo/internal/urlkey"
)

// CorpusDoc 评测语料中的一条文档。
type CorpusDoc struct {
	URL              string   `json:"url"`
	Domain           string   `json:"domain"`
	Category         string   `json:"category"` // 白名单分类，参与质量分
	Title            string   `json:"title"`
	Description      string   `json:"description"`
	Body             string   `json:"body"`
	Labels           []string `json:"labels"`
	PublishedDaysAgo int      `json:"published_days_ago"`
	Prosperity       float64  `json:"prosperity"`
}

// Query 一条评测查询，Relevant 为 URL → 相关性等级（0 可省略）。
type Query struct {
	Q        string         `json:"q"`
	Relevant map[string]int `json:"relevant"`
}

// QueryReport 单条查询结果。
type QueryReport struct {
	Query     string   `json:"query"`
	Hits      int      `json:"hits"`
	FirstRel  int      `json:"first_relevant_rank"` // 0 表示前 10 无相关
	Recall20  float64  `json:"recall_at_20"`
	NDCG10    float64  `json:"ndcg_at_10"`
	MRR       float64  `json:"mrr_at_10"`
	LatencyMS float64  `json:"latency_ms"`
	TopURLs   []string `json:"top_urls"`
}

// Report 汇总报告。
type Report struct {
	Queries      int           `json:"queries"`
	Recall20     float64       `json:"recall_at_20"`
	NDCG10       float64       `json:"ndcg_at_10"`
	MRR10        float64       `json:"mrr_at_10"`
	NoResultRate float64       `json:"no_result_rate"`
	P95MS        float64       `json:"p95_ms"`
	Details      []QueryReport `json:"details"`
}

// LoadCorpus 读取 JSONL 语料。
func LoadCorpus(path string) ([]CorpusDoc, error) {
	var out []CorpusDoc
	err := eachJSONL(path, func(line []byte) error {
		var d CorpusDoc
		if err := json.Unmarshal(line, &d); err != nil {
			return err
		}
		out = append(out, d)
		return nil
	})
	return out, err
}

// LoadQueries 读取 JSONL 查询集。
func LoadQueries(path string) ([]Query, error) {
	var out []Query
	err := eachJSONL(path, func(line []byte) error {
		var q Query
		if err := json.Unmarshal(line, &q); err != nil {
			return err
		}
		// 标注里的 URL 可能带锚点或 utm 参数，而检索结果已是规范 URL。
		// 不归一就会把「命中了但写法不同」判成未命中——实测一条查询因此从 1.0 掉到 0。
		norm := make(map[string]int, len(q.Relevant))
		for u, rel := range q.Relevant {
			norm[urlkey.Canonical(u)] = rel
		}
		q.Relevant = norm
		out = append(out, q)
		return nil
	})
	return out, err
}

func eachJSONL(path string, fn func([]byte) error) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 1024*1024), 8*1024*1024)
	for sc.Scan() {
		line := sc.Bytes()
		if len(line) == 0 {
			continue
		}
		if err := fn(line); err != nil {
			return fmt.Errorf("%s: %w", path, err)
		}
	}
	return sc.Err()
}

// Run 构建临时索引并执行全部查询，返回汇总报告。
func Run(corpus []CorpusDoc, queries []Query) (Report, error) {
	dir, err := os.MkdirTemp("", "voidxo-eval-")
	if err != nil {
		return Report{}, err
	}
	defer os.RemoveAll(dir)

	idx, err := index.Open(filepath.Join(dir, "bleve"))
	if err != nil {
		return Report{}, err
	}
	defer idx.Close()

	now := time.Now()
	categories := map[string]string{}
	prosperity := map[string]float64{}
	for _, d := range corpus {
		pub := now.AddDate(0, 0, -d.PublishedDaysAgo).Unix()
		if err := idx.IndexDocument(index.Doc{
			URL: d.URL, Domain: d.Domain, Title: d.Title, Description: d.Description,
			Body: d.Body, Labels: d.Labels, PublishedAt: &pub, FetchedAt: now.Unix(),
			Prosperity: d.Prosperity,
		}); err != nil {
			return Report{}, err
		}
		if d.Category != "" {
			categories[d.Domain] = d.Category
		}
		if d.Prosperity > 0 {
			prosperity[d.Domain] = d.Prosperity
		}
	}

	return runQueriesWith(context.Background(), idx, queries, func(context.Context, []index.Hit) rank.Signals {
		return rank.Signals{Categories: categories, Prosperity: prosperity, CorpusDocs: idx.DocCount()}
	}, rank.DefaultWeights(), now)
}

// RunWithIndex 在既有索引上执行全部查询，信号（域名分类 / 繁荣度）也取自线上存储，
// 排序结果与真实请求完全一致——这是唯一有参考价值的跑分口径。
//
// 为什么需要它：Run 走「语料 JSONL 重建索引」，语料是导出时重跑 processor 得到的，
// 正文与索引里的并不总是一致，BM25 基准随之偏移。真实案例：「docker 构建镜像」
// 线上 top1 就是标注文档（2.88 分），重建索引后掉到第 2、NDCG 归零——
// 指标退化是评测管线的假象，不是系统退化。
//
// 需先停止服务：Bleve 与 Pebble 都是单进程独占。
func RunWithIndex(ctx context.Context, idx *index.Index, st *store.Store, queries []Query, now time.Time) (Report, error) {
	return runQueriesWith(ctx, idx, queries, func(ctx context.Context, hits []index.Hit) rank.Signals {
		domains := make([]string, 0, len(hits))
		keys := make([]string, 0, len(hits)*2)
		for _, h := range hits {
			domains = append(domains, h.Domain)
			keys = append(keys, h.Domain, urlkey.Key(h.URL))
		}
		cats, err := st.DomainCategories(ctx, domains)
		if err != nil {
			cats = nil
		}
		pros, err := st.ProsperityScores(ctx, keys)
		if err != nil {
			pros = nil
		}
		hubs := make(map[string]bool, 2)
		for _, h := range hits {
			if rank.IsHubURL(h.URL) {
				hubs[h.URL] = true
			}
		}
		// 折叠副本数：让评测与线上看到同一套理由
		dupCounts := map[string]int{}
		if urls, err := st.DupCountsByURLs(ctx, hitURLs(hits)); err == nil {
			dupCounts = urls
		}
		return rank.Signals{Categories: cats, Prosperity: pros, HubPages: hubs, DupCount: dupCounts,
			CorpusDocs: idx.DocCount()}
	}, rank.DefaultWeights(), now)
}

// hitURLs 从候选中取出 URL 列表，供批量查询折叠副本数。
func hitURLs(hits []index.Hit) []string {
	out := make([]string, 0, len(hits))
	for _, h := range hits {
		out = append(out, h.URL)
	}
	return out
}

// MissingTargets 返回「标注了、但索引里找不到」的目标 URL。
// 指标下跌时先看这个：标注文档被治理掉或 URL 变了，会把数据缺口伪装成召回退化。
func MissingTargets(idx *index.Index, queries []Query) []string {
	var out []string
	seen := map[string]bool{}
	for _, q := range queries {
		for u := range q.Relevant {
			if seen[u] {
				continue
			}
			seen[u] = true
			ok, err := idx.Has(u)
			if err != nil || ok {
				continue
			}
			out = append(out, fmt.Sprintf("%s → %s", q.Q, u))
		}
	}
	return out
}

// signalFn 为候选集构造排序信号（域名分类 + 繁荣度）。
type signalFn func(ctx context.Context, hits []index.Hit) rank.Signals

// runQueries 逐条执行：召回 → 信号 → 可解释排序 → 指标汇总。
func runQueries(ctx context.Context, idx *index.Index, queries []Query, sig signalFn, now time.Time) (Report, error) {
	return runQueriesWith(ctx, idx, queries, sig, rank.DefaultWeights(), now)
}

// runQueriesWith 与 runQueries 相同，但允许指定排序权重（调参用）。
func runQueriesWith(ctx context.Context, idx *index.Index, queries []Query, sig signalFn,
	w rank.Weights, now time.Time) (Report, error) {
	rk := rank.New(w)
	rep := Report{Queries: len(queries)}
	var latencies []float64

	for _, q := range queries {
		start := time.Now()
		hits, _, err := idx.Search(index.Query{Q: q.Q, Page: 1, Size: 50})
		if err != nil {
			return rep, fmt.Errorf("query %q: %w", q.Q, err)
		}
		ranked := rk.Rank(q.Q, hits, sig(ctx, hits), now)
		elapsed := float64(time.Since(start).Microseconds()) / 1000
		latencies = append(latencies, elapsed)

		urls := make([]string, 0, len(ranked))
		for _, r := range ranked {
			urls = append(urls, r.Hit.URL)
		}

		qr := QueryReport{Query: q.Q, Hits: len(urls), LatencyMS: elapsed, TopURLs: firstN(urls, 5)}
		qr.Recall20 = RecallAtK(urls, q.Relevant, 20)
		qr.NDCG10 = NDCGAtK(urls, q.Relevant, 10)
		qr.MRR, qr.FirstRel = MRRAtK(urls, q.Relevant, 10)
		if len(urls) == 0 {
			rep.NoResultRate++
		}
		rep.Recall20 += qr.Recall20
		rep.NDCG10 += qr.NDCG10
		rep.MRR10 += qr.MRR
		rep.Details = append(rep.Details, qr)
	}

	if n := float64(len(queries)); n > 0 {
		rep.Recall20 /= n
		rep.NDCG10 /= n
		rep.MRR10 /= n
		rep.NoResultRate /= n
	}
	rep.P95MS = percentile(latencies, 0.95)
	return rep, nil
}

// RecallAtK 前 k 条命中的相关文档数 / 全部相关文档数。
func RecallAtK(urls []string, relevant map[string]int, k int) float64 {
	if len(relevant) == 0 {
		return 0
	}
	seen := map[string]bool{}
	for i, u := range urls {
		if i >= k {
			break
		}
		if relevant[u] > 0 {
			seen[u] = true
		}
	}
	return float64(len(seen)) / float64(len(relevant))
}

// NDCGAtK 分级增益 NDCG（gain = 2^rel - 1，折扣 log2(i+2)）。
func NDCGAtK(urls []string, relevant map[string]int, k int) float64 {
	if len(relevant) == 0 {
		return 0
	}
	var dcg float64
	for i, u := range urls {
		if i >= k {
			break
		}
		if rel := relevant[u]; rel > 0 {
			dcg += (math.Pow(2, float64(rel)) - 1) / math.Log2(float64(i)+2)
		}
	}

	grades := make([]int, 0, len(relevant))
	for _, g := range relevant {
		if g > 0 {
			grades = append(grades, g)
		}
	}
	sort.Sort(sort.Reverse(sort.IntSlice(grades)))
	var idcg float64
	for i, g := range grades {
		if i >= k {
			break
		}
		idcg += (math.Pow(2, float64(g)) - 1) / math.Log2(float64(i)+2)
	}
	if idcg == 0 {
		return 0
	}
	return dcg / idcg
}

// MRRAtK 返回 MRR 与首个相关结果的排名（0 表示前 k 无相关）。
func MRRAtK(urls []string, relevant map[string]int, k int) (float64, int) {
	for i, u := range urls {
		if i >= k {
			break
		}
		if relevant[u] > 0 {
			return 1 / float64(i+1), i + 1
		}
	}
	return 0, 0
}

func percentile(vals []float64, p float64) float64 {
	if len(vals) == 0 {
		return 0
	}
	s := append([]float64(nil), vals...)
	sort.Float64s(s)
	idx := int(math.Ceil(p*float64(len(s)))) - 1
	if idx < 0 {
		idx = 0
	}
	if idx >= len(s) {
		idx = len(s) - 1
	}
	return s[idx]
}

func firstN(s []string, n int) []string {
	if len(s) < n {
		return s
	}
	return s[:n]
}
