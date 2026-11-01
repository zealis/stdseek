// Package index 封装 Bleve 全文索引（纯 Go 内嵌，遵守红线：不手写倒排索引）。
// 字段与规划书对齐：title/body/labels/published_at/fetched_at/prosperity/domain。
package index

import (
	"fmt"
	"strings"
	"sync/atomic"
	"time"
	"unicode"

	"github.com/blevesearch/bleve/v2"
	"github.com/blevesearch/bleve/v2/analysis/lang/cjk"
	"github.com/blevesearch/bleve/v2/mapping"
	"github.com/blevesearch/bleve/v2/search/query"

	"github.com/zealis/stdseek/internal/lang"
	"github.com/zealis/stdseek/internal/urlkey"
)

// Doc 写入索引的文档。
type Doc struct {
	URL         string   `json:"url"`
	Domain      string   `json:"domain"`
	Title       string   `json:"title"`
	Description string   `json:"description"`
	Body        string   `json:"body"`
	Labels      []string `json:"labels"` // 索引期预计算，查询期只做过滤
	// Category 站点分类（sitecat 的 key），来自白名单的站点分类（人工选或 AI 判定）。
	// 页级标签（labels）说的是「这页怎么样」，分类说的是「这站是干嘛的」——
	// 用户筛「只要有设计素材的站」时，筛的是后者。
	Category string `json:"category"`
	// Lang 文档语言（zh/en）。语言边界红线：中文查询只召回中文内容，
	// 靠 cjk bigram 只是「天然成立」，语义召回能力上线后必须靠它硬过滤。
	Lang        string `json:"lang"`
	PublishedAt *int64 `json:"published_at"`
	// HasPublished 由 IndexDocument 自动维护（published_at 是否存在），
	// 用于区分「发布时间缺失」与「发布时间过旧」，避免时间窗误放行。
	HasPublished string  `json:"has_published"` // "true" / "false"
	FetchedAt    int64   `json:"fetched_at"`
	Prosperity   float64 `json:"prosperity"` // 由繁荣度统计器定时回写
	// Excerpt 正文摘要（前 200 字，只存不索引）。留空时 IndexDocument 自动截取。
	Excerpt string `json:"excerpt"`
}

// Hit 检索结果项。
type Hit struct {
	URL         string   `json:"url"`
	Domain      string   `json:"domain"`
	Title       string   `json:"title"`
	Description string   `json:"description"`
	Labels      []string `json:"labels"`
	Category    string   `json:"category"`
	Score       float64  `json:"score"`
	// TimeByFetch 为 true 表示该结果按收录时间（fetched_at）参与时间过滤/展示，
	// 遵守红线：published_at 缺失时如实标记「按收录时间」，不放宽口径。
	TimeByFetch bool  `json:"time_by_fetch"`
	SortTime    int64 `json:"sort_time"` // 实际参与时间过滤/排序的时间戳
	// Prosperity 繁荣度（反向链接统计器定时回写），0 表示尚未统计。
	Prosperity float64 `json:"prosperity"`
	// Excerpt 正文摘要（索引的 excerpt 字段，只存不索引）。
	// 很多站点（如 vuejs.org）的 title 全是站点默认名，功能名只出现在正文里，
	// 排序层需要它才能判断「这个页面是不是用户要的那个功能页」。
	Excerpt string `json:"excerpt"`
}

// Index 包装 bleve.Index。
type Index struct {
	idx bleve.Index
	// ver 索引代数：写入/删除成功后 +1。
	// 上层缓存（api 搜索结果缓存）靠它判断"结果可能变了"，
	// 不必自己在每个写入口维护失效逻辑——漏一个就是用户看到已删除的内容。
	ver atomic.Int64
}

// DocCount 索引内文档数。
//
// 排序层的绝对相关性闸门按语料规模缩放（BM25 的分值由 IDF 主导，
// 而 IDF 随文档数变化），所以每次排序都需要这个数字。
// Bleve 的 DocCount 读的是维护好的计数，不是全表扫描，可放心每请求调用。
func (i *Index) DocCount() int {
	n, err := i.idx.DocCount()
	if err != nil {
		return 0
	}
	return int(n)
}

// Version 当前索引代数。只增不减，具体数值无意义，只用于比较是否变化。
func (i *Index) Version() int64 { return i.ver.Load() }

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

	// 正文摘要：存储但不参与检索。
	//
	// 为什么不用 Index=false：Bleve 的 FieldMapping 里 Index=false 会让该字段
	// 完全不进倒排（连 Store 也一并失效，实测取回空值）。所以保持 Index=true，
	// 靠两点隔离它对检索的影响：
	//   1. IncludeInAll=false —— 不进 _all 聚合字段；
	//   2. 查询只用 title/description/body 三个字段（见 buildRequest 的 fieldClause），
	//      从不指定 excerpt。
	// 代价是正文前 200 字被重复索引一次，体积可忽略。
	excerptField := mapping.NewTextFieldMapping()
	excerptField.Name = "excerpt"
	excerptField.Store = true
	excerptField.Index = true
	excerptField.IncludeInAll = false
	excerptField.IncludeTermVectors = false

	doc.AddFieldMappingsAt("excerpt", excerptField)
	doc.AddFieldMappingsAt("title", text("title"))
	doc.AddFieldMappingsAt("description", text("description"))
	doc.AddFieldMappingsAt("body", bodyField)
	doc.AddFieldMappingsAt("labels", keyword("labels"))               // 硬过滤用 keyword，不分词
	doc.AddFieldMappingsAt("category", keyword("category"))         // 站点分类同样是硬过滤
	doc.AddFieldMappingsAt("has_published", keyword("has_published")) // 时间过滤用
	doc.AddFieldMappingsAt("lang", keyword("lang"))                   // 语言边界硬过滤
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
	d = prepare(d)
	// 文档 ID 用归一化 key（去 scheme/query/fragment）而非原始 URL。
	// 真实语料里 InfoQ 的 /content-center/ 与 #about/#intro/#project 等锚点
	// 被当成 6 篇独立文档写入，内容完全相同——用 key 作 ID 让它们互相覆盖，
	// 只保留最后写入的一份（URL 字段仍是可访问的原始地址）。
	if err := i.idx.Index(urlkey.Key(d.URL), d); err != nil {
		return err
	}
	i.ver.Add(1)
	return nil
}

// IndexDocuments 批量写入。返回成功写入的篇数与首个错误。
//
// 为什么要这个：逐条 IndexDocument 的吞吐**恒定**在 41 篇/秒（实测 1k 与 10k 同值），
// 建库 50 万篇要 3.4 小时。逐条写的成本不在分词，而在每条都单独走一次
// bleve 的 batch 提交流程。批量接口把这些开销摊到一批上。
//
// 分批而非一次性：50 万篇一次塞进 batch 会把整份语料留在内存里，
// 500 篇一批在内存与吞吐之间比较平衡（单批约 1MB 量级）。
func (i *Index) IndexDocuments(docs []Doc) (ok int, err error) {
	const chunk = 500
	for s := 0; s < len(docs); s += chunk {
		e := s + chunk
		if e > len(docs) {
			e = len(docs)
		}
		b := i.idx.NewBatch()
		n := 0
		for _, d := range docs[s:e] {
			if d.URL == "" {
				continue
			}
			d = prepare(d)
			if berr := b.Index(urlkey.Key(d.URL), d); berr != nil {
				return ok, fmt.Errorf("batch index %s: %w", d.URL, berr)
			}
			n++
		}
		if berr := i.idx.Batch(b); berr != nil {
			return ok, fmt.Errorf("batch commit: %w", berr)
		}
		ok += n
		if n > 0 {
			i.ver.Add(1)
		}
	}
	return ok, nil
}

// prepare 归一化文档字段。抽出来是因为单条与批量两条路径都要走同一套预处理，
// 各写一遍迟早漂移（漏了语言兜底 → 中文文档被红线 9 整批滤掉，是静默事故）。
func prepare(d Doc) Doc {
	if d.PublishedAt != nil {
		d.HasPublished = "true"
	} else {
		d.HasPublished = "false"
	}
	// 语言标签兜底为中文：本站中文技术内容优先，且调用方漏传时
	// 不该让文档从中文查询的结果里消失（语言边界是硬过滤，漏标=被滤掉）。
	if d.Lang != lang.Zh && d.Lang != lang.En {
		d.Lang = lang.Zh
	}
	// 正文摘要：只存不索引（mapping 里 IncludeInAll=false），不参与 BM25，
	// 否则与 body 全文重复计分。用途有三：结果页展示真实摘要、
	// 排序层判断「功能名是否出现在正文里」、证据链摘要的文本基础。
	// 调用方没有显式提供时从正文截取——绝大多数站点不需要特殊处理。
	if d.Excerpt == "" {
		d.Excerpt = excerptOf(d.Body, excerptRunes)
	}
	// 描述同样要封顶：有的博客把整篇正文塞进 meta description
	// （实测单条 1200~1600 字），搜索结果卡会被撑成一屏文字。
	//
	// 只截断，不用正文兜底：description 是参与检索的字段，拿正文填充会抬高
	// 本该靠标题命中的文档分数（TestTitleFieldBoost 就是这么挂的）。
	// 空描述在展示层用 excerpt 补，那是只存不索引的字段。
	if d.Description != "" {
		d.Description = excerptOf(d.Description, descRunes)
	}
	return d
}

// descRunes 结果卡展示用的描述上限。比 excerptRunes 更短：
// 卡片只给三行（约 200 字），留一点余量给窄屏即可。
const descRunes = 160

// excerptRunes 摘要长度上限（字符数，不是字节）。
// 200 字足够覆盖页面 H1 与首段，又不会把整页正文塞进索引存储。
const excerptRunes = 200

// excerptOf 截取正文前 n 个字符作为摘要，压缩空白。
func excerptOf(body string, n int) string {
	s := strings.Join(strings.Fields(body), " ")
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n]) + "…"
}

// DeleteDocument 按原始 URL 从索引移除文档（下架噪声页 / 纠错）。
// 与 IndexDocument 共用同一套归一化 key，保证删的就是写进去的那一份。
func (i *Index) DeleteDocument(rawURL string) error {
	if err := i.idx.Delete(urlkey.Key(rawURL)); err != nil {
		return err
	}
	i.ver.Add(1)
	return nil
}

// Has 报告某个 URL 是否在索引中（按规范 key 查找）。
// 评测用它发现「标注文档已不在库」：标注过期会被误读成召回退化，
// 真实案例是一条查询因为标注页被当成聚合页清掉而直接从 1.0 掉到 0。
func (i *Index) Has(rawURL string) (bool, error) {
	key := urlkey.Key(rawURL)
	if key == "" {
		return false, nil
	}
	doc, err := i.idx.Document(key)
	if err != nil {
		// bleve 对不存在的文档返回 nil,nil，走到这里说明是真错误
		return false, err
	}
	return doc != nil, nil
}

// enStopwords 英文虚词表：不含主题信息，不能作为「结果必须包含」的约束。
// 只收真正的功能词与检索里常见的空词，避免误伤技术词（如 doc/docs 这类可能指文档站）。
var enStopwords = map[string]bool{
	"a": true, "an": true, "the": true, "is": true, "are": true, "was": true, "were": true,
	"be": true, "of": true, "to": true, "in": true, "on": true, "at": true, "for": true,
	"and": true, "or": true, "not": true, "with": true, "from": true, "by": true,
	"how": true, "what": true, "why": true, "when": true, "where": true, "which": true,
	"who": true, "does": true, "do": true, "can": true, "it": true, "this": true, "that": true,
	"topic": true, "topics": true, "thing": true, "things": true, "stuff": true,
}

// opForToken 决定单个查询词内部的词元关系：含汉字取 OR（中文按 bigram 召回，
// 要求全部命中等于要求整句连续出现），其余取 AND（单个拉丁词或下划线标识符）。
func opForToken(tok string) query.MatchQueryOperator {
	if hasHan(tok) {
		return query.MatchQueryOperatorOr
	}
	return query.MatchQueryOperatorAnd
}

// hasHan 判断 token 是否含汉字。中文检索按 bigram 召回，单个词内部必须取 OR，
// 否则等于要求整句连续出现。
func hasHan(s string) bool {
	for _, r := range s {
		if unicode.Is(unicode.Han, r) {
			return true
		}
	}
	return false
}

// Query 检索参数。
type Query struct {
	Q        string   // 查询串（已剥掉语法符号，见 internal/querysyntax）
	Exclude  []string // 排除的 labels，如 ["ai_suspect","ugc_platform"]
	Cats     []string // 只要这些站点分类（OR），空表示不限
	SinceSec int64    // 时间窗：仅保留 SortTime >= now-SinceSec 的文档；0 表示不过滤
	Page     int      // 从 1 开始
	Size     int

	// 以下是查询语法产生的硬约束，缺省为空表示不启用。
	// 它们与 Q 的关系：Q 决定 BM25 怎么打分（宽松召回），这些决定哪些文档能入选。
	Required []string // +词：每个词都必须命中
	Excluded []string // -词：命中的文档直接剔除
	Phrases  []string // "短语"：作为连续短语匹配，不拆词
	Site     string   // site:域名 只保留该域名（含子域）的文档
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
			// 读出时再截一次：入库封顶只对新抓取生效，
			// 存量的长描述（实测有 1600 字的）不该继续被整段发给前端。
			hit.Description = excerptOf(v, descRunes)
		}
		if v, ok := h.Fields["excerpt"].(string); ok {
			hit.Excerpt = v
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
		if v, ok := h.Fields["category"].(string); ok {
			hit.Category = v
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
	// 站点分类：多选取并集（选了「设计创意 + 图片素材」就是想两样都要），
	// 单选就是等值过滤。用 term 而不是 match——category 是 keyword 字段，
	// 匹配走的是不分词的精确值。
	if len(q.Cats) == 1 {
		tq := bleve.NewTermQuery(q.Cats[0])
		tq.SetField("category")
		bq.AddMust(tq)
	} else if len(q.Cats) > 1 {
		any := bleve.NewDisjunctionQuery()
		for _, c := range q.Cats {
			tq := bleve.NewTermQuery(c)
			tq.SetField("category")
			any.AddQuery(tq)
		}
		bq.AddMust(any)
	}
	// 查询语法的硬约束。放在候选过滤层而不是排序层：写「+北京 -大学」的人
	// 要的是精确的集合，不是「北京优先、大学降权」。
	for _, term := range q.Required {
		bq.AddMust(fieldClause(term, false))
	}
	for _, term := range q.Excluded {
		bq.AddMustNot(fieldClause(term, false))
	}
	for _, ph := range q.Phrases {
		bq.AddMust(phraseClause(ph))
	}
	if q.Site != "" {
		bq.AddMust(siteClause(q.Site))
	}
	// 语言边界（规划书红线 9）：纯中文查询只召回中文文档。
	// 放在候选过滤层而不是排序层——降权保留仍会让英文内容漏进结果页。
	// 不提供开关：一旦可关，某次调参就会顺手关掉，且很难在指标上发现。
	// 中英混杂查询（「docker 构建镜像」）与纯英文查询不限制，那是术语桥的主形态。
	if lang.MustZh(q.Q) {
		lq := bleve.NewTermQuery(lang.Zh)
		lq.SetField("lang")
		bq.AddMust(lq)
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
	req.Fields = []string{"url", "domain", "title", "description", "excerpt", "labels", "category",
		"published_at", "fetched_at", "prosperity"}
	return req
}

// phraseClause 短语查询：作为一个整体匹配，不拆词。
//
// 用 MatchPhraseQuery 而不是把词用引号拼回原生查询串——后者依赖 Bleve 的
// QueryStringQuery 语法，用户输入里的括号、冒号会变成语法符号。
func phraseClause(ph string) query.Query {
	boost := map[string]float64{"title": 3.0, "description": 1.5, "body": 1.0}
	var perField []query.Query
	for _, f := range []string{"title", "description", "body"} {
		mq := bleve.NewMatchPhraseQuery(ph)
		mq.SetField(f)
		mq.SetBoost(boost[f])
		perField = append(perField, mq)
	}
	return bleve.NewDisjunctionQuery(perField...)
}

// siteClause 限定站点：精确域名、www 变体、以及它的子域。
//
// domain 是 keyword 字段（不分词），所以用 TermQuery 精确匹配；
// 子域（docs.php.net）用通配符补上——白名单里存在这种站，用户写 site:php.net
// 时显然也想搜到它们。
func siteClause(site string) query.Query {
	site = strings.ToLower(strings.TrimSpace(site))
	exact := bleve.NewTermQuery(site)
	exact.SetField("domain")
	www := bleve.NewTermQuery("www." + site)
	www.SetField("domain")
	sub := bleve.NewWildcardQuery("*." + site)
	sub.SetField("domain")
	return bleve.NewDisjunctionQuery(exact, www, sub)
}

// fieldClause 构造「每字段内 AND、跨字段 OR」的查询子句。
// 显式字段检索 + 字段权重：标题 3.0 / 摘要 1.5 / 正文 1.0。
// 不用 _all 聚合字段——否则 published_at 等无关字段存在与否会改变长度归一化，
// 导致同文本文档 BM25 不一致（已实测）。
func fieldClause(qs string, prefix bool) query.Query {
	// 标题权重高于正文。曾实验提到 8.0 想抵消 BM25 对长文档的惩罚
	// （「docker buildx 构建镜像」里 4393 词的 buildx/build 被 1114 词的 create 挤出前八），
	// 实测 NDCG 0.302→0.301 无变化、P95 延迟反涨 3 倍，已回退。
	// 该案例的根因是中文修饰词（「构建镜像」）在英文文档里不存在，
	// 无法区分 buildx 的同族子命令——需要术语对齐（镜像↔image），属跨语言范畴。
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
	// 拉丁词是主题词，命中它才算是「关于这个技术」的结果。
	// 真实反例：把中文段改成 OR 之后，「PHP 命名空间」里的「命名/空间」把大批
	// 中文文章拉进候选池，英文官方文档被挤出前 20（PHP 命名空间 Recall 1.0→0）。
	// 所以含拉丁词的查询要求候选至少命中一个拉丁词；纯中文查询不受影响。
	var latinClauses []query.Query
	// 算子按「词」判断，不能按「整个查询串有没有空格」判断。
	// 中文词没有空格，一个 token 就是一整段话，它在索引侧被切成多个 bigram：
	// 若用 AND，等价于要求整段话作为连续子串出现——「分布式系统怎么学习」对
	// 《什么是分布式系统，如何学习分布式系统》0 命中（"怎么"不在标题里）。
	// 早期版本只在「查询串完全无空格」时取 OR，于是中英混杂查询里的中文段
	// 仍然走 AND：「AI 缓存需要知道什么」要求「道什」「什么」同时出现，
	// 整段匹配失败，只剩 AI 在撑分，目标文档直接掉出前 20。
	// 拉丁 token 保持 AND：它是单个词或下划线标识符，收紧有利于精确匹配。
	for _, tok := range tokens {
		op := opForToken(tok)
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
			mq.SetOperator(op) // 该词自身被分词后的词元关系：含汉字取 OR，其余 AND
			perField = append(perField, mq)
		}
		dq := bleve.NewDisjunctionQuery(perField...)
		// 拉丁 token 视为查询主题词（技术专有名词），权重高于中文修饰词。
		// 真实语料扩到 227 篇（中文约占四成）后暴露：「docker 构建镜像」命中的
		// 中文文章靠「构建镜像」抢到前排，Docker 官方文档被挤出前 10，
		// 术语桥查询 NDCG 从 0.480 跌到 0.303。用户打出英文术语时，
		// 意图通常指向该技术的官方资源；中文文档仍可靠正文多词命中胜出。
		if len(tok) >= 2 && isASCII(tok) {
			dq.SetBoost(2)
			// 只把有主题意义的拉丁词放进必需子句。虚词（topic/the/what…）人人都有，
			// 让它们当必需项等于没有约束：实测「xyzzy nonexistent topic」靠 topic
			// 命中 13 条，噪音闸门因为 exact>0 也失效了。
			if !enStopwords[tok] {
				latinClauses = append(latinClauses, dq)
			}
		}
		bq.AddShould(dq)
	}
	if len(latinClauses) > 0 {
		bq.AddMust(bleve.NewDisjunctionQuery(latinClauses...))
	}
	// 术语扩展：中文查询打英文文档时补英文对应词（「命名空间」→ namespaces）。
	// 只进 should、不进 must：不会把原本命中的结果挡掉，只是给命中扩展词的结果加分。
	// 权重取字段Boost的一半：原权重下「鸿蒙内存泄漏怎么查」的「内存 → memory」
	// 会把英文内存文档拽到鸿蒙文章前面（纯中文组 NDCG 0.928→0.913）；
	// 减半后它只够在分数接近时打破平局，压不过强中文匹配。
	if !prefix {
		for _, en := range expandTerms(qs) {
			var perField []query.Query
			for _, f := range []string{"title", "description", "body"} {
				mq := bleve.NewMatchQuery(en)
				mq.SetField(f)
				mq.SetBoost(boost[f] * 0.5)
				mq.SetOperator(query.MatchQueryOperatorAnd)
				perField = append(perField, mq)
			}
			bq.AddShould(bleve.NewDisjunctionQuery(perField...))
		}
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
