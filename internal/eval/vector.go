package eval

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"hash/fnv"
	"os"
	"path/filepath"
	"sort"
	"time"

	"github.com/zealis/stdseek/internal/embed"
	"github.com/zealis/stdseek/internal/index"
	"github.com/zealis/stdseek/internal/lang"
	"github.com/zealis/stdseek/internal/rank"
	"github.com/zealis/stdseek/internal/urlkey"
)

// VecCorpus 语料向量表：全量驻留内存，查询时暴力扫余弦。
//
// 为什么不做 ANN 索引：语料量级是千级（本期 316 篇），384 维全量点积
// 单次约 12 万次乘加（<1ms），比维护 HNSW 更简单也更准；规模上万后再换。
type VecCorpus struct {
	Keys []string    // urlkey.Key(URL)
	URLs []string    // 原始 URL，召回后用于回填结果
	Vecs [][]float32 // 已 L2 归一化
	Dim  int
}

// Match 一次向量召回的结果。
type Match struct {
	URLKey string
	URL    string
	// Cos 原始余弦（未截断），用于阈值实验与结果展示。
	Cos float64
}

// BuildVecCorpus 对整份语料算向量。文档取「标题 + 摘要 + 正文前 N 字符」拼接，
// 截断到 docTokens：正文动辄上千 token，全算既慢（512 token 约 1.85s/篇）
// 又摊薄标题语义。
//
// 必须走 EmbedPassage（带 "passage: " 前缀）：e5 的查询与文档是两个指令空间，
// 不加前缀时纯向量排序的 NDCG@10 从 0.929 掉到 0.430。
func BuildVecCorpus(m *embed.Model, corpus []CorpusDoc, docTokens, maxBodyRunes int) *VecCorpus {
	vc := &VecCorpus{Dim: m.Hidden}
	for _, d := range corpus {
		k := urlkey.Key(d.URL)
		if k == "" {
			continue
		}
		text := vecText(d, maxBodyRunes)
		v := m.EmbedPassage(text, docTokens)
		if len(v) == 0 {
			continue
		}
		vc.Keys = append(vc.Keys, k)
		vc.URLs = append(vc.URLs, d.URL)
		vc.Vecs = append(vc.Vecs, v)
	}
	return vc
}

// vecText 参与向量化的文本：标题、摘要、正文前 maxBodyRunes 字符（0 表示不限）。
//
// 正文只取前段而不是全部：与离线 spike 的口径一致（spike 用 body.slice(0,400)），
// 且长正文的尾部往往是页脚、评论、导航，对语义是噪声。
func vecText(d CorpusDoc, maxBodyRunes int) string {
	s := d.Title
	if d.Description != "" {
		s += "\n" + d.Description
	}
	body := d.Body
	if maxBodyRunes > 0 && len([]rune(body)) > maxBodyRunes {
		body = string([]rune(body)[:maxBodyRunes])
	}
	if body != "" {
		s += "\n" + body
	}
	return s
}

// Search 暴力近邻，返回余弦最高的 k 条。
func (vc *VecCorpus) Search(q []float32, k int) []Match {
	if len(q) == 0 || len(vc.Vecs) == 0 {
		return nil
	}
	type scored struct {
		i   int
		cos float64
	}
	sims := make([]scored, 0, len(vc.Vecs))
	for i, v := range vc.Vecs {
		if len(v) != len(q) {
			continue
		}
		var dot float64
		for j := range q {
			dot += float64(q[j]) * float64(v[j])
		}
		sims = append(sims, scored{i, dot}) // 两侧已归一化，点积即余弦
	}
	sort.Slice(sims, func(a, b int) bool { return sims[a].cos > sims[b].cos })
	if k > len(sims) {
		k = len(sims)
	}
	out := make([]Match, 0, k)
	for _, s := range sims[:k] {
		out = append(out, Match{URLKey: vc.Keys[s.i], URL: vc.URLs[s.i], Cos: s.cos})
	}
	return out
}

// Get 取单个文档的向量（按 urlkey），不存在返回 nil。
func (vc *VecCorpus) Get(urlKey string) []float32 {
	for i, k := range vc.Keys {
		if k == urlKey {
			return vc.Vecs[i]
		}
	}
	return nil
}

// Mode 检索模式。
type Mode string

const (
	ModeBM25   Mode = "bm25"   // 纯词汇（现状基线）
	ModeVector Mode = "vector" // 语义召回 + 完整 rank 管线（含繁荣度/时效/多样性）
	ModeHybrid Mode = "hybrid" // 词汇召回 ∪ 语义召回，融合排序
	// ModeRaw 语义召回 + 纯余弦排序，不过 rank 管线。
	//
	// 它与离线 spike 的口径一致，存在的唯一目的是给向量质量本身做体检：
	// raw 高但 vector/hybrid 低 → 向量没问题，是融合方式（权重/多样性重排）拖后腿；
	// raw 也低 → 向量或语料有问题，先别谈融合。
	ModeRaw Mode = "raw"
)

// RunModes 在同一份语料与查询集上跑多个检索模式，返回各模式的报告。
//
// 为什么要并排跑而不是只跑 hybrid：词汇与语义的增益分布在不同查询上，
// 只看混合总分无法判断「哪部分查询被救回、哪部分被带偏」，
// 也无法定位阈值（vecFloor / minVector）该往哪调。
func RunModes(ctx context.Context, corpus []CorpusDoc, queries []Query,
	m *embed.Model, modes []Mode, vecW float64, vecHits, docTokens, maxBodyRunes int,
	cachePath string, now time.Time) (map[string]Report, error) {
	dir, err := os.MkdirTemp("", "stdseek-evalvec-")
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
	byURL := map[string]CorpusDoc{}
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
		byURL[d.URL] = d
	}

	vc := loadVecCache(cachePath, corpus, docTokens, maxBodyRunes)
	if vc == nil {
		vc = BuildVecCorpus(m, corpus, docTokens, maxBodyRunes)
		if cachePath != "" {
			if err := saveVecCache(cachePath, vc, corpus, docTokens, maxBodyRunes); err != nil {
				fmt.Fprintf(os.Stderr, "警告：向量缓存写入失败（%v），继续跑\n", err)
			}
		}
	}
	if len(vc.Vecs) == 0 {
		return nil, fmt.Errorf("语料向量为空（模型目录或语料有问题）")
	}

	out := map[string]Report{}
	for _, mode := range modes {
		rep, err := runMode(ctx, idx, queries, vc, m, byURL, categories, prosperity, mode, vecW, vecHits, now)
		if err != nil {
			return nil, err
		}
		out[string(mode)] = rep
	}
	return out, nil
}

// runMode 单模式执行：召回 → 构造信号（含向量）→ 排序 → 指标。
func runMode(ctx context.Context, idx *index.Index, queries []Query, vc *VecCorpus,
	m *embed.Model, byURL map[string]CorpusDoc, categories map[string]string,
	prosperity map[string]float64, mode Mode, vecW float64, vecHits int, now time.Time) (Report, error) {
	w := rank.DefaultWeights()
	w.Vector = vecW
	useVec := mode != ModeBM25 && mode != ModeRaw && vecW > 0
	if mode == ModeVector || mode == ModeRaw {
		// 纯语义模式下没有 BM25 可依赖，把词汇权重清零，
		// 否则 minBM25 闸门与 BM25 分量会让结果退化成「词汇排序 + 噪声」。
		w.BM25 = 0
		w.Exact = 0
	}
	rk := rank.New(w)

	rep := Report{Queries: len(queries)}
	var latencies []float64
	for _, q := range queries {
		start := time.Now()
		var hits []index.Hit
		var qv []float32

		if mode == ModeBM25 {
			h, _, err := idx.Search(index.Query{Q: q.Q, Page: 1, Size: 50})
			if err != nil {
				return rep, err
			}
			hits = h
		} else if mode != ModeRaw && !lang.MustZh(q.Q) {
			// 语义路由：只对纯中文查询启用向量。
			//
			// 实测（337 篇语料，术语桥 39 条）向量在跨语言查询上是净伤害：
			// 混合后 Recall@20 0.859 → 0.628、NDCG@10 0.541 → 0.410——
			// 术语桥查询（「React 快速开始」这类中英混排）依赖词面精确匹配，
			// 而 e5 的语义相似度在这类查询上会把"语义相近但不是答案"的文档
			// 拉进候选池并挤掉真正命中的（纯向量在该组只有 0.474/0.342）。
			// 纯中文查询则相反：混合 NDCG@10 0.869 → 0.882。
			// 所以按查询语言路由，而不是无条件混合。
			h, _, err := idx.Search(index.Query{Q: q.Q, Page: 1, Size: 50})
			if err != nil {
				return rep, err
			}
			hits = h
		} else {
			qv = m.EmbedQuery(q.Q)
			matches := vc.Search(qv, vecHits)
			if mode == ModeRaw {
				// 纯余弦排序：直接按召回顺序输出，跳过 rank 管线
				urls := make([]string, 0, len(matches))
				for _, mm := range matches {
					urls = append(urls, mm.URL)
				}
				elapsed := float64(time.Since(start).Microseconds()) / 1000
				latencies = append(latencies, elapsed)
				qr := QueryReport{Query: q.Q, Hits: len(urls), LatencyMS: elapsed, TopURLs: firstN(urls, 5)}
				qr.Recall20 = RecallAtK(urls, q.Relevant, 20)
				qr.NDCG10 = NDCGAtK(urls, q.Relevant, 10)
				qr.MRR, qr.FirstRel = MRRAtK(urls, q.Relevant, 10)
				rep.Recall20 += qr.Recall20
				rep.NDCG10 += qr.NDCG10
				rep.MRR10 += qr.MRR
				rep.Details = append(rep.Details, qr)
				continue
			}
			vecHits := vc.Hits(matches, byURL, now)
			if mode == ModeVector {
				hits = vecHits
			} else {
				bm, _, err := idx.Search(index.Query{Q: q.Q, Page: 1, Size: 50})
				if err != nil {
					return rep, err
				}
				hits = mergeHits(bm, vecHits)
			}
		}

		sig := rank.Signals{Categories: categories, Prosperity: prosperity}
		if useVec {
			sig.QueryVec = qv
			sig.Vectors = make(map[string][]float32, len(hits))
			for _, h := range hits {
				if v := vc.Get(urlkey.Key(h.URL)); v != nil {
					sig.Vectors[urlkey.Key(h.URL)] = v
				}
			}
		}
		ranked := rk.Rank(q.Q, hits, sig, now)
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

// vecCacheMeta 缓存指纹：语料内容、截断参数任一变化都必须重算。
type vecCacheMeta struct {
	DocTokens    int      `json:"doc_tokens"`
	MaxBodyRunes int      `json:"max_body_runes"`
	CorpusHash   uint64   `json:"corpus_hash"`
	Keys         []string `json:"keys"`
	URLs         []string `json:"urls"`
	Vecs         [][]float32
}

// corpusHash 语料指纹：URL 与正文长度参与，内容变了就失效。
func corpusHash(corpus []CorpusDoc) uint64 {
	h := fnv.New64a()
	for _, d := range corpus {
		fmt.Fprintf(h, "%s|%d|%d|", d.URL, len(d.Body), len(d.Title))
	}
	return h.Sum64()
}

// loadVecCache 命中则返回缓存，否则返回 nil（调用方重算）。
// 337 篇 × 256 token 约 5 分钟，调阈值时每次重算完全没法迭代。
func loadVecCache(path string, corpus []CorpusDoc, docTokens, maxBodyRunes int) *VecCorpus {
	if path == "" {
		return nil
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil
	}
	var meta vecCacheMeta
	dec := json.NewDecoder(bytes.NewReader(raw))
	if err := dec.Decode(&meta); err != nil {
		return nil
	}
	if meta.DocTokens != docTokens || meta.MaxBodyRunes != maxBodyRunes ||
		meta.CorpusHash != corpusHash(corpus) || len(meta.Vecs) != len(meta.Keys) {
		return nil
	}
	var dim int
	if len(meta.Vecs) > 0 {
		dim = len(meta.Vecs[0])
	}
	return &VecCorpus{Keys: meta.Keys, URLs: meta.URLs, Vecs: meta.Vecs, Dim: dim}
}

func saveVecCache(path string, vc *VecCorpus, corpus []CorpusDoc, docTokens, maxBodyRunes int) error {
	meta := vecCacheMeta{
		DocTokens: docTokens, MaxBodyRunes: maxBodyRunes, CorpusHash: corpusHash(corpus),
		Keys: vc.Keys, URLs: vc.URLs, Vecs: vc.Vecs,
	}
	b, err := json.Marshal(meta)
	if err != nil {
		return err
	}
	return os.WriteFile(path, b, 0o644)
}

// Hits 把向量召回结果补成 index.Hit，供排序管线消费。
// 纯语义召回的候选没有 BM25 分（Score 置 0），相关性完全由向量分给出。
func (vc *VecCorpus) Hits(ms []Match, byURL map[string]CorpusDoc, now time.Time) []index.Hit {
	out := make([]index.Hit, 0, len(ms))
	for _, m := range ms {
		d, ok := byURL[m.URL]
		if !ok {
			continue
		}
		pub := now.AddDate(0, 0, -d.PublishedDaysAgo).Unix()
		out = append(out, index.Hit{
			URL: m.URL, Domain: d.Domain, Title: d.Title, Description: d.Description,
			Labels: d.Labels, Score: 0, SortTime: pub, Prosperity: d.Prosperity,
		})
	}
	return out
}

// mergeHits 合并两路召回，词汇结果在前（保留其 BM25 分），语义补充在后去重。
func mergeHits(bm, vec []index.Hit) []index.Hit {
	seen := map[string]bool{}
	out := make([]index.Hit, 0, len(bm)+len(vec))
	for _, h := range bm {
		if seen[h.URL] {
			continue
		}
		seen[h.URL] = true
		out = append(out, h)
	}
	for _, h := range vec {
		if seen[h.URL] {
			continue
		}
		seen[h.URL] = true
		out = append(out, h)
	}
	return out
}
