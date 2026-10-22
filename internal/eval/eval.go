// Package eval 提供检索质量评测：读取语料与查询集，构建临时索引，
// 用真实 rank 管线跑分，输出 Recall@20 / NDCG@10 / MRR@10 / 无结果率 / P95。
//
// 语料与查询格式见 cmd/eval 与 eval/*.jsonl；相关性为分级标注（2 高度相关 / 1 相关）。
package eval

import (
	"bufio"
	"encoding/json"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"sort"
	"time"

	"voidxo/internal/index"
	"voidxo/internal/rank"
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

	rk := rank.New(rank.DefaultWeights())
	rep := Report{Queries: len(queries)}
	var latencies []float64

	for _, q := range queries {
		start := time.Now()
		hits, _, err := idx.Search(index.Query{Q: q.Q, Page: 1, Size: 50})
		if err != nil {
			return Report{}, fmt.Errorf("query %q: %w", q.Q, err)
		}
		ranked := rk.Rank(q.Q, hits, rank.Signals{Categories: categories, Prosperity: prosperity}, now)
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
