// Package index 封装 Bleve 全文索引（纯 Go 内嵌，遵守红线：不手写倒排索引）。
// 字段与规划书对齐：title/body/labels/published_at/fetched_at/prosperity/domain。
package index

import (
	"fmt"
	"time"

	"github.com/blevesearch/bleve/v2"
	"github.com/blevesearch/bleve/v2/analysis/analyzer/standard"
	"github.com/blevesearch/bleve/v2/mapping"
	"github.com/blevesearch/bleve/v2/search/query"
)

// Doc 写入索引的文档。
type Doc struct {
	URL         string   `json:"url"`
	Domain      string   `json:"domain"`
	Title       string   `json:"title"`
	Description string   `json:"description"`
	Body        string   `json:"body"`
	Labels      []string `json:"labels"` // 索引期预计算，查询期只做过滤
	PublishedAt *int64   `json:"published_at"`
	FetchedAt   int64    `json:"fetched_at"`
	Prosperity  float64  `json:"prosperity"` // 由繁荣度统计器定时回写
}

// Hit 检索结果项。
type Hit struct {
	URL         string   `json:"url"`
	Domain      string   `json:"domain"`
	Title       string   `json:"title"`
	Description string   `json:"description"`
	Labels      []string `json:"labels"`
	Score       float64  `json:"score"`
	// TimeByFetch 为 true 表示该结果按收录时间（fetched_at）参与时间过滤/展示，
	// 遵守红线：published_at 缺失时如实标记「按收录时间」，不放宽口径。
	TimeByFetch bool  `json:"time_by_fetch"`
	SortTime    int64 `json:"sort_time"` // 实际参与时间过滤/排序的时间戳
}

// Index 包装 bleve.Index。
type Index struct {
	idx bleve.Index
}

// buildMapping 构造文档 mapping。
// 一期用 standard analyzer 打底；中文自定义分词（gojieba/gse + 技术词典）
// 属第 3-4 周调优项，届时替换 analyzer 并重建索引。
func buildMapping() mapping.IndexMapping {
	doc := mapping.NewDocumentMapping()

	text := func(field string) *mapping.FieldMapping {
		fm := mapping.NewTextFieldMapping()
		fm.Name = field
		fm.Analyzer = standard.Name
		fm.Store = true
		fm.IncludeTermVectors = true
		return fm
	}
	keyword := func(field string) *mapping.FieldMapping {
		fm := mapping.NewKeywordFieldMapping()
		fm.Name = field
		fm.Store = true
		return fm
	}

	doc.AddFieldMappingsAt("title", text("title"))
	doc.AddFieldMappingsAt("description", text("description"))
	doc.AddFieldMappingsAt("body", text("body"))
	doc.AddFieldMappingsAt("labels", keyword("labels")) // 硬过滤用 keyword，不分词
	doc.AddFieldMappingsAt("domain", keyword("domain"))
	doc.AddFieldMappingsAt("url", keyword("url"))

	num := mapping.NewNumericFieldMapping()
	num.Name = "prosperity"
	num.Store = true
	doc.AddFieldMappingsAt("prosperity", num)

	// 双时间戳：published_at（可空）与 fetched_at，时间过滤的硬条件字段
	for _, name := range []string{"published_at", "fetched_at"} {
		fm := mapping.NewNumericFieldMapping()
		fm.Name = name
		fm.Store = true
		doc.AddFieldMappingsAt(name, fm)
	}

	m := bleve.NewIndexMapping()
	m.DefaultMapping = doc
	m.DefaultAnalyzer = standard.Name
	return m
}

// Open 打开（不存在则创建）指定目录的 Bleve 索引。
func Open(path string) (*Index, error) {
	idx, err := bleve.Open(path)
	if err == bleve.ErrorIndexPathDoesNotExist {
		idx, err = bleve.New(path, buildMapping())
	}
	if err != nil {
		return nil, fmt.Errorf("open bleve %s: %w", path, err)
	}
	return &Index{idx: idx}, nil
}

// Close 关闭索引。
func (i *Index) Close() error { return i.idx.Close() }

// IndexDocument 以 URL 为文档 ID 写入/更新文档。
func (i *Index) IndexDocument(d Doc) error {
	if d.URL == "" {
		return fmt.Errorf("doc url empty")
	}
	return i.idx.Index(d.URL, d)
}

// Query 检索参数。
type Query struct {
	Q        string   // 查询串
	Exclude  []string // 排除的 labels，如 ["ai_suspect","ugc_platform"]
	SinceSec int64    // 时间窗：仅保留 SortTime >= now-SinceSec 的文档；0 表示不过滤
	Page     int      // 从 1 开始
	Size     int
}

// Search 执行检索：BM25 召回 + labels 排除 + 时间窗硬过滤。
// 字段加权排序（w1..w5 公式）属 internal/rank 的后续迭代，骨架阶段先用 Bleve 默认 BM25。
func (i *Index) Search(q Query) ([]Hit, int, error) {
	if q.Size <= 0 || q.Size > 50 {
		q.Size = 10
	}
	if q.Page <= 0 {
		q.Page = 1
	}

	var musts []query.Query
	if q.Q != "" {
		mq := bleve.NewMatchQuery(q.Q)
		musts = append(musts, mq)
	}
	bq := bleve.NewBooleanQuery()
	for _, m := range musts {
		bq.AddMust(m)
	}
	for _, label := range q.Exclude {
		tq := bleve.NewTermQuery(label)
		tq.SetField("labels")
		bq.AddMustNot(tq)
	}
	if q.SinceSec > 0 {
		min := float64(time.Now().Unix() - q.SinceSec)
		// 时间过滤对 published_at 与 fetched_at 取 OR：
		// published_at 在窗内，或 published_at 缺失而 fetched_at 在窗内。
		pq := bleve.NewNumericRangeQuery(&min, nil)
		pq.SetField("published_at")
		fq := bleve.NewNumericRangeQuery(&min, nil)
		fq.SetField("fetched_at")
		bq.AddMust(bleve.NewDisjunctionQuery(pq, fq))
	}

	req := bleve.NewSearchRequestOptions(bq, q.Size, (q.Page-1)*q.Size, false)
	req.Fields = []string{"url", "domain", "title", "description", "labels", "published_at", "fetched_at"}
	res, err := i.idx.Search(req)
	if err != nil {
		return nil, 0, fmt.Errorf("bleve search: %w", err)
	}

	hits := make([]Hit, 0, len(res.Hits))
	for _, h := range res.Hits {
		hit := Hit{URL: h.ID, Score: h.Score}
		if v, ok := h.Fields["domain"].(string); ok {
			hit.Domain = v
		}
		if v, ok := h.Fields["title"].(string); ok {
			hit.Title = v
		}
		if v, ok := h.Fields["description"].(string); ok {
			hit.Description = v
		}
		switch v := h.Fields["labels"].(type) {
		case []interface{}:
			for _, x := range v {
				if s, ok := x.(string); ok {
					hit.Labels = append(hit.Labels, s)
				}
			}
		case string:
			hit.Labels = []string{v}
		}
		pub, pubOK := numField(h.Fields["published_at"])
		fet, fetOK := numField(h.Fields["fetched_at"])
		switch {
		case pubOK:
			hit.SortTime = pub
		case fetOK:
			hit.SortTime = fet
			hit.TimeByFetch = true
		}
		hits = append(hits, hit)
	}
	return hits, int(res.Total), nil
}

func numField(v interface{}) (int64, bool) {
	switch n := v.(type) {
	case float64:
		return int64(n), true
	case int64:
		return n, true
	}
	return 0, false
}
