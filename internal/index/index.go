// Package index 封装 Bleve 全文索引（纯 Go 内嵌，遵守红线：不手写倒排索引）。
// 字段与规划书对齐：title/body/labels/published_at/fetched_at/prosperity/domain。
package index

import (
	"fmt"
	"strings"
	"time"

	"github.com/blevesearch/bleve/v2"
	"github.com/blevesearch/bleve/v2/analysis/lang/cjk"
	"github.com/blevesearch/bleve/v2/mapping"
	"github.com/blevesearch/bleve/v2/search/query"

	"voidxo/internal/urlkey"
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
	// HasPublished 由 IndexDocument 自动维护（published_at 是否存在），
	// 用于区分「发布时间缺失」与「发布时间过旧」，避免时间窗误放行。
	HasPublished string  `json:"has_published"` // "true" / "false"
	FetchedAt    int64   `json:"fetched_at"`
	Prosperity   float64 `json:"prosperity"` // 由繁荣度统计器定时回写
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
	// Prosperity 繁荣度（反向链接统计器定时回写），0 表示尚未统计。
	Prosperity float64 `json:"prosperity"`
}

// Index 包装 bleve.Index。
type Index struct {
	idx bleve.Index
}

// buildMapping 构造文档 mapping。
// 中文字段用 cjk bigram 分析器；更细的中文分词（gse + 技术词典）留给二期
// 属第 3-4 周调优项，届时替换 analyzer 并重建索引。
func buildMapping() mapping.IndexMapping {
	doc := mapping.NewDocumentMapping()

	// 中文用 cjk 分析器（bigram）：standard analyzer 按「连续字母段」切分，
	// 中文没有空格，整句会变成一个 token——查询「术语表」只能整段匹配，
	// 真实评测集里中文查询因此大面积 0 结果。cjk 对 CJK 输出 bigram、对拉丁仍按词。
	text := func(field string) *mapping.FieldMapping {
		fm := mapping.NewTextFieldMapping()
		fm.Name = field
		fm.Analyzer = cjk.AnalyzerName
		fm.Store = true
		fm.IncludeTermVectors = true
		return fm
	}
	// 正文只索引不存储（Store=false）：原文回溯走 KV 快照，避免索引体积翻倍。
	bodyField := mapping.NewTextFieldMapping()
	bodyField.Name = "body"
	bodyField.Analyzer = cjk.AnalyzerName
	bodyField.Store = false
	bodyField.IncludeTermVectors = false
	keyword := func(field string) *mapping.FieldMapping {
		fm := mapping.NewKeywordFieldMapping()
		fm.Name = field
		fm.Store = true
		return fm
	}

	doc.AddFieldMappingsAt("title", text("title"))
	doc.AddFieldMappingsAt("description", text("description"))
	doc.AddFieldMappingsAt("body", bodyField)
	doc.AddFieldMappingsAt("labels", keyword("labels"))               // 硬过滤用 keyword，不分词
	doc.AddFieldMappingsAt("has_published", keyword("has_published")) // 时间过滤用
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
	m.DefaultAnalyzer = cjk.AnalyzerName
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
	if d.PublishedAt != nil {
		d.HasPublished = "true"
	} else {
		d.HasPublished = "false"
	}
	// 文档 ID 用归一化 key（去 scheme/query/fragment）而非原始 URL。
	// 真实语料里 InfoQ 的 /content-center/ 与 #about/#intro/#project 等锚点
	// 被当成 6 篇独立文档写入，内容完全相同——用 key 作 ID 让它们互相覆盖，
	// 只保留最后写入的一份（URL 字段仍是可访问的原始地址）。
	return i.idx.Index(urlkey.Key(d.URL), d)
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
// 查询语义：同一字段内所有词必须命中（AND），跨字段取并集（标题权重最高）；
// 若整条查询 0 命中，则用「前缀查询」重试一次（crashloop → crashloopbackoff 这类技术缩写）。
func (i *Index) Search(q Query) ([]Hit, int, error) {
	if q.Size <= 0 || q.Size > 50 {
		q.Size = 10
	}
	if q.Page <= 0 {
		q.Page = 1
	}

	res, err := i.idx.Search(i.buildRequest(q, false))
	if err != nil {
		return nil, 0, fmt.Errorf("bleve search: %w", err)
	}
	if res.Total == 0 && strings.TrimSpace(q.Q) != "" && hasLatinToken(q.Q) {
		res, err = i.idx.Search(i.buildRequest(q, true))
		if err != nil {
			return nil, 0, fmt.Errorf("bleve prefix search: %w", err)
		}
	}

	hits := make([]Hit, 0, len(res.Hits))
	for _, h := range res.Hits {
		// 文档 ID 是归一化 key（用于去重），对外展示/跳转必须用存储的原始 URL
		hit := Hit{URL: h.ID, Score: h.Score}
		if v, ok := h.Fields["url"].(string); ok && v != "" {
			hit.URL = v
		}
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
		if v, ok := h.Fields["prosperity"].(float64); ok {
			hit.Prosperity = v
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

// buildRequest 组装查询：terms 为 must（AND），labels/时间窗为 must/must_not。
// prefix=true 时对查询词做前缀匹配（仅用于 0 命中的兜底重试）。
func (i *Index) buildRequest(q Query, prefix bool) *bleve.SearchRequest {
	bq := bleve.NewBooleanQuery()
	if strings.TrimSpace(q.Q) != "" {
		bq.AddMust(fieldClause(q.Q, prefix))
	}
	for _, label := range q.Exclude {
		tq := bleve.NewTermQuery(label)
		tq.SetField("labels")
		bq.AddMustNot(tq)
	}
	if q.SinceSec > 0 {
		min := float64(time.Now().Unix() - q.SinceSec)
		// 时间窗是硬条件（规划书红线 8）：
		//   published_at 在窗内
		//   或 published_at 缺失（has_published=false）且 fetched_at 在窗内。
		// 不能简单对两字段取 OR——否则「两年前发布、今天收录」的旧文会被「1 天」放行。
		pub := bleve.NewNumericRangeQuery(&min, nil)
		pub.SetField("published_at")
		noPub := bleve.NewTermQuery("false")
		noPub.SetField("has_published")
		fetched := bleve.NewNumericRangeQuery(&min, nil)
		fetched.SetField("fetched_at")
		fallback := bleve.NewConjunctionQuery(noPub, fetched)
		bq.AddMust(bleve.NewDisjunctionQuery(pub, fallback))
	}
	req := bleve.NewSearchRequestOptions(bq, q.Size, (q.Page-1)*q.Size, false)
	req.Fields = []string{"url", "domain", "title", "description", "labels",
		"published_at", "fetched_at", "prosperity"}
	return req
}

// fieldClause 构造「每字段内 AND、跨字段 OR」的查询子句。
// 显式字段检索 + 字段权重：标题 3.0 / 摘要 1.5 / 正文 1.0。
// 不用 _all 聚合字段——否则 published_at 等无关字段存在与否会改变长度归一化，
// 导致同文本文档 BM25 不一致（已实测）。
func fieldClause(qs string, prefix bool) query.Query {
	boost := map[string]float64{"title": 3.0, "description": 1.5, "body": 1.0}
	tokens := strings.Fields(strings.ToLower(qs))
	if len(tokens) == 0 {
		tokens = []string{strings.ToLower(qs)}
	}

	// 每个查询词：在 title/description/body 任一字段命中即可（取最高权重）。
	// 词与词之间只要求命中 1 个。
	// 真实评测集（36 条中文查询打英文文档）暴露：中文查询词——「语言规范」「属性参考」
	// 「安装」——在英文文档里根本不存在，要求命中 60% 会让 "Rust 安装" 直接 0 结果，
	// 全站无结果率 52.8%。召回放宽后由 rank 的 BM25 归一化 + 相关性闸门压制噪音。
	bq := bleve.NewBooleanQuery()
	minShould := 1
	for _, tok := range tokens {
		var perField []query.Query
		for _, f := range []string{"title", "description", "body"} {
			if prefix {
				pq := bleve.NewPrefixQuery(tok)
				pq.SetField(f)
				pq.SetBoost(boost[f])
				perField = append(perField, pq)
				continue
			}
			mq := bleve.NewMatchQuery(tok)
			mq.SetField(f)
			mq.SetBoost(boost[f])
			mq.SetOperator(query.MatchQueryOperatorAnd) // 该词自身被分词后仍需全部命中
			perField = append(perField, mq)
		}
		bq.AddShould(bleve.NewDisjunctionQuery(perField...))
	}
	// 必须在 AddShould 之后设置：Bleve 的 SetMinShould 直接作用于 Should 子查询
	bq.SetMinShould(float64(minShould))
	return bq
}

// hasLatinToken 判断查询是否包含可用于前缀兜底的拉丁词（长度 ≥ 4）。
func hasLatinToken(qs string) bool {
	for _, tok := range strings.Fields(strings.ToLower(qs)) {
		if len(tok) >= 4 && isASCII(tok) {
			return true
		}
	}
	return false
}

func isASCII(s string) bool {
	for _, r := range s {
		if r > 127 {
			return false
		}
	}
	return true
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
