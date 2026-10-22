// Package processor 负责「抓取之后、入索引之前」的处理管线：
// 正文提取（go-readability）→ simhash 指纹 → 中文分词（gse）→ 标签分类（索引期打标）。
//
// 设计约束：
//   - 标签在索引期预计算（规划书功能 G），查询期只做过滤；
//   - ai_suspect 只做过滤开关，不参与排序降权；
//   - 每个标签必须携带可解释理由，供结果页展示与误判反馈。
package processor

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/url"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/PuerkitoBio/goquery"
	"github.com/go-ego/gse"
	readability "github.com/go-shiori/go-readability"
)

// Options 处理参数（站点分类来自白名单，用于 personal/platform 判定）。
type Options struct {
	SiteCategory string
}

// Result 处理结果。
type Result struct {
	Title        string            `json:"title"`
	Description  string            `json:"description"`
	Body         string            `json:"body"`
	Labels       []string          `json:"labels"`
	LabelReasons map[string]string `json:"label_reasons"`
	PublishedAt  *int64            `json:"published_at"` // 解析失败为 nil，检索降级用 fetched_at 并如实标记
	FetchedAt    int64             `json:"fetched_at"`
	Simhash      uint64            `json:"simhash"`
	WordCount    int               `json:"word_count"`
	Keywords     []string          `json:"keywords"`
}

// Processor 持有分词器（字典加载较贵，进程内复用）。
type Processor struct {
	seg gse.Segmenter
}

// New 构造处理器并加载中文词典。
// 必须用 LoadDictEmbed 加载编译期内嵌词典：gse.New() 走文件系统路径，
// -trimpath 打包成单二进制后会找不到 GOPATH 里的词典（实测启动失败）。
func New() (*Processor, error) {
	var seg gse.Segmenter
	if err := seg.LoadDictEmbed("zh"); err != nil {
		return nil, fmt.Errorf("load gse dict: %w", err)
	}
	seg.AlphaNum = true // 保留 kubernetes.io / v1.28 这类技术符号
	return &Processor{seg: seg}, nil
}

// Process 处理单个页面。
func (p *Processor) Process(rawURL string, raw []byte, opts Options) (Result, error) {
	u, err := url.Parse(rawURL)
	if err != nil {
		return Result{}, fmt.Errorf("parse url: %w", err)
	}

	res := Result{
		FetchedAt:    time.Now().Unix(),
		Labels:       []string{},
		LabelReasons: map[string]string{},
	}

	article, artErr := readability.FromReader(bytes.NewReader(raw), u)
	if artErr == nil {
		res.Title = clean(article.Title)
		res.Description = clean(article.Excerpt)
		res.Body = clean(article.TextContent)
		if article.PublishedTime != nil {
			ts := article.PublishedTime.Unix()
			res.PublishedAt = &ts
		}
	}

	// 兜底：readability 失败或抽空时，退回 DOM 文本提取
	if strings.TrimSpace(res.Body) == "" {
		title, desc, body := fallbackExtract(raw)
		if res.Title == "" {
			res.Title = title
		}
		if res.Description == "" {
			res.Description = desc
		}
		res.Body = body
	}

	// meta 补全：description / published_at
	md, pub := metaFields(raw)
	if res.Description == "" {
		res.Description = md
	}
	if res.PublishedAt == nil && pub != nil {
		res.PublishedAt = pub
	}

	if res.Title == "" {
		res.Title = u.Hostname()
	}
	res.Body = truncate(res.Body, 200000)

	tokens, titleTokens := p.tokenize(res.Title, res.Body)
	res.WordCount = len(tokens)
	res.Simhash = Simhash(tokens, weightsFor(len(titleTokens), len(tokens)))
	res.Keywords = topKeywords(tokens, 10)

	res.Labels, res.LabelReasons = computeLabels(labelInput{
		URL:          u,
		Title:        res.Title,
		Body:         res.Body,
		RawHTML:      string(raw),
		SiteCategory: opts.SiteCategory,
	})
	return res, nil
}

// tokenize 返回正文词序列 + 标题词序列（标题词权重更高，参与 simhash 加权）。
func (p *Processor) tokenize(title, body string) (all []string, titleTokens []string) {
	tb := p.cut(title)
	bb := p.cut(body)
	titleTokens = tb
	all = append(all, tb...)
	all = append(all, bb...)
	return all, titleTokens
}

// cut 分词并清洗：去停用符号、去单字噪声（保留英文/数字短标识符）。
func (p *Processor) cut(s string) []string {
	if strings.TrimSpace(s) == "" {
		return nil
	}
	if len([]rune(s)) > 100000 {
		s = string([]rune(s)[:100000])
	}
	segs := p.seg.Cut(s, true)
	out := make([]string, 0, len(segs))
	for _, w := range segs {
		w = strings.TrimSpace(strings.ToLower(w))
		if w == "" || isPunct(w) {
			continue
		}
		if len([]rune(w)) == 1 && !isASCII(w) {
			continue // 过滤单个汉字（虚词/噪声）
		}
		if len([]rune(w)) > 32 {
			continue
		}
		out = append(out, w)
	}
	return out
}

// weightsFor 返回与 tokens 对应的权重：标题词 3.0，正文词 1.0。
func weightsFor(nTitle, nAll int) []float64 {
	w := make([]float64, nAll)
	for i := range w {
		if i < nTitle {
			w[i] = 3.0
		} else {
			w[i] = 1.0
		}
	}
	return w
}

// topKeywords 按词频取前 n 个关键词（用于域名画像与「深网发现」）。
func topKeywords(tokens []string, n int) []string {
	freq := map[string]int{}
	for _, t := range tokens {
		if len([]rune(t)) < 2 {
			continue
		}
		freq[t]++
	}
	type kv struct {
		k string
		v int
	}
	list := make([]kv, 0, len(freq))
	for k, v := range freq {
		list = append(list, kv{k, v})
	}
	sort.Slice(list, func(i, j int) bool {
		if list[i].v != list[j].v {
			return list[i].v > list[j].v
		}
		return list[i].k < list[j].k
	})
	if len(list) > n {
		list = list[:n]
	}
	out := make([]string, len(list))
	for i, x := range list {
		out[i] = x.k
	}
	return out
}

// ---------- DOM / meta ----------

// fallbackExtract 在 readability 失败时用 goquery 抽 title/description/正文。
func fallbackExtract(raw []byte) (title, desc, body string) {
	doc, err := goquery.NewDocumentFromReader(bytes.NewReader(raw))
	if err != nil {
		return "", "", ""
	}
	title = clean(doc.Find("title").First().Text())
	desc = clean(doc.Find(`meta[name="description"]`).First().AttrOr("content", ""))
	doc.Find("script,style,noscript,svg").Remove()
	body = clean(doc.Find("body").Text())
	return title, desc, body
}

var publishedPatterns = []*regexp.Regexp{
	regexp.MustCompile(`(?i)<meta[^>]+(?:property|name)=["'](?:article:published_time|og:published_time|pubdate|publishdate|date)["'][^>]+content=["']([^"']+)["']`),
	regexp.MustCompile(`(?i)<meta[^>]+content=["']([^"']+)["'][^>]+(?:property|name)=["'](?:article:published_time|og:published_time|pubdate|publishdate|date)["']`),
	regexp.MustCompile(`(?i)"datePublished"\s*:\s*"([^"]+)"`),
	regexp.MustCompile(`(?i)<time[^>]+datetime=["']([^"']+)["']`),
}

var dateLayouts = []string{
	time.RFC3339, "2006-01-02T15:04:05Z07:00", "2006-01-02 15:04:05",
	"2006-01-02T15:04:05", "2006-01-02 15:04", "2006-01-02", "2006/01/02",
}

// metaFields 从原始 HTML 提取 description 与发布时间。
func metaFields(raw []byte) (desc string, published *int64) {
	html := string(raw)
	doc, err := goquery.NewDocumentFromReader(strings.NewReader(html))
	if err == nil {
		desc = clean(doc.Find(`meta[name="description"]`).First().AttrOr("content", ""))
		if desc == "" {
			desc = clean(doc.Find(`meta[property="og:description"]`).First().AttrOr("content", ""))
		}
	}
	for _, re := range publishedPatterns {
		if m := re.FindStringSubmatch(html); len(m) == 2 {
			if ts, ok := parseDate(m[1]); ok {
				return desc, &ts
			}
		}
	}
	return desc, nil
}

func parseDate(s string) (int64, bool) {
	s = strings.TrimSpace(s)
	for _, layout := range dateLayouts {
		if t, err := time.Parse(layout, s); err == nil {
			return t.Unix(), true
		}
	}
	return 0, false
}

// ---------- 文本工具 ----------

func clean(s string) string {
	s = strings.Join(strings.Fields(s), " ")
	return strings.TrimSpace(s)
}

func truncate(s string, max int) string {
	r := []rune(s)
	if len(r) <= max {
		return s
	}
	return string(r[:max])
}

func isASCII(s string) bool {
	for _, r := range s {
		if r > 127 {
			return false
		}
	}
	return true
}

func isPunct(s string) bool {
	for _, r := range s {
		if !strings.ContainsRune(" \t\n\r,.;:!?，。；：！？、（）()【】[]{}<>《》\"'`~@#$%^&*+=|\\/-", r) {
			return false
		}
	}
	return true
}

// LabelsJSON 把标签序列化为 SQLite 存储用 JSON。
func LabelsJSON(labels []string) string {
	b, err := json.Marshal(labels)
	if err != nil {
		return "[]"
	}
	return string(b)
}

// ReasonsJSON 把标签理由序列化（用于可解释展示与误判反馈）。
func ReasonsJSON(reasons map[string]string) string {
	b, err := json.Marshal(reasons)
	if err != nil {
		return "{}"
	}
	return string(b)
}
