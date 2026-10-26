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
	"strconv"
	"strings"

	"time"
	"unicode"
	"voidxo/internal/lang"

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
	// ShingleHash 正文 shingle 指纹（跨站转载判定用，见 simhash.go）
	ShingleHash uint64 `json:"shingle_hash"`
	WordCount   int    `json:"word_count"`
	// LangRatio 正文中「中文 + 英文」字符占全部字母字符的比例。
	// 用来挡掉白名单站点上的多语言镜像页（nginx.org/ru、kubernetes.io/bn）：
	// readability 的 Article.Language 不做检测（实测俄语页返回空串），只能自己算。
	LangRatio float64 `json:"lang_ratio"`
	// Lang 文档语言（zh / en）。语言边界红线的硬过滤判据：
	// 纯中文查询只召回中文文档，不靠 cjk bigram 的天然成立。
	Lang string `json:"lang"`
	// LinkDensity 正文里链接文本的字符占比。
	// 列表页/聚合页/导航页的特征是「大部分文字都是可点击链接」，
	// 这类页面即便字数达标也不该进索引（真实语料：博客园首页、InfoQ 首页）。
	LinkDensity float64  `json:"link_density"`
	Keywords    []string `json:"keywords"`
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
		res.LinkDensity = linkDensity(article.Content)
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
	md, pub := metaFields(raw, rawURL)
	if res.Description == "" {
		res.Description = md
	}
	if res.PublishedAt == nil && pub != nil {
		res.PublishedAt = pub
	}

	// 标题增强：readability 只抓到站名段时退回 <title>。
	//
	// SPA 站点（vuejs.org 等）的 SSR 首屏里，readability 找到的第一个标题元素
	// 是页头站标，于是全站页面拿到同一个标题——实测 vuejs.org 71 篇标题全是
	// "Vue.js"：结果页出现一列同名结果，标题这个 BM25 加权字段也失去信号。
	// 而 <title> 里是完整的 "Reactivity Fundamentals | Vue.js"。
	//
	// 判据只认「站名段」这一种形态：readability 标题等于 <title> 按分隔符
	// 切分后的最后一段，且 <title> 还有别的段。不改成"<title> 更长就用"——
	// 那会让所有正常页面的标题都被强加站名后缀（"Go 语言规范 | 站名"），
	// 是无谓的大范围行为变更，还会稀释标题字段的 BM25 信号。
	if dt := docTitle(raw); dt != "" && isSiteSegment(res.Title, dt) {
		res.Title = dt
	}

	if res.Title == "" {
		res.Title = u.Hostname()
	}
	res.Body = truncate(res.Body, 200000)
	res.LangRatio = TargetLangRatio(res.Body)
	res.Lang = lang.DocLang(res.Title, res.Body)

	tokens, titleTokens := p.tokenize(res.Title, res.Body)
	res.WordCount = len(tokens)
	res.Simhash = Simhash(tokens, weightsFor(len(titleTokens), len(tokens)))
	// 正文词在 tokens 的尾部（tokenize 先拼标题再拼正文）
	bodyTokens := tokens
	if len(titleTokens) <= len(tokens) {
		bodyTokens = tokens[len(titleTokens):]
	}
	res.ShingleHash = shingleHash(bodyTokens)
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

// boilerplateSuffixes 站务页标题的结尾词（截掉站点名后缀后匹配）。
var boilerplateSuffixes = []string{
	"服务协议", "用户协议", "使用协议", "隐私政策", "隐私协议",
	"免责声明", "版权声明", "法律声明", "知识产权说明",
}

// articleLeadWords 教学/探讨型标题的开头词。站务页不会这样起名，
// 但「如何为开源项目写一份用户协议」会以「用户协议」结尾——单测实测的误判案例。
var articleLeadWords = []string{
	"如何", "怎么", "怎样", "为何", "为什么", "浅谈", "详解", "深入",
	"聊聊", "谈谈", "说说", "图解", "一文", "从零", "手把手",
}

// IsBoilerplateTitle 判断标题是否为站务页（服务协议/隐私政策/版权声明）。
//
// 这类页面的 URL 形态与文章一致（developer.aliyun.com/article/768092），
// article_pattern 拦不住，只能看标题。三条同时满足才判定：
//  1. 截掉「- 站点名」后缀后，以站务词结尾（只看结尾不看包含，
//     「对话亚马逊CTO：仍在招聘初级工程师」含「招聘」但结尾不是站务词）；
//  2. 长度 ≤ 30 字：技术文章标题通常更长（《微服务架构下的服务协议设计实践》）；
//  3. 不以教学/探讨型词开头：「如何为开源项目写一份用户协议」是文章不是站务页。
//
// 抓取（跳过不入索引）与运维清理（下架存量）共用同一判据。
func IsBoilerplateTitle(title string) bool {
	t := strings.TrimSpace(title)
	if i := strings.IndexAny(t, "-_|"); i > 0 {
		t = strings.TrimSpace(t[:i])
	}
	if len([]rune(t)) > 30 {
		return false
	}
	for _, lead := range articleLeadWords {
		if strings.HasPrefix(t, lead) {
			return false
		}
	}
	for _, suf := range boilerplateSuffixes {
		if strings.HasSuffix(t, suf) {
			return true
		}
	}
	return false
}

// TargetLangRatio 返回正文里「中文/英文」字符占全部字母字符的比例。
// 判据只看元音文字（unicode.IsLetter）：符号、数字、代码块不计入分母，
// 因此技术文档里的 {} / () / URL 不会稀释结果。
// 俄语（西里尔）、孟加拉语、韩文谚文、日文假名都会落到 other 里。
func TargetLangRatio(s string) float64 {
	var target, other int
	for _, r := range s {
		switch {
		case unicode.Is(unicode.Han, r): // 汉字（含扩展区）
			target++
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z':
			target++
		case unicode.IsLetter(r):
			other++
		}
	}
	total := target + other
	if total == 0 {
		return 1 // 没有字母（纯符号/数字/空页），不武断丢弃
	}
	return float64(target) / float64(total)
}

// linkDensity 计算 HTML 片段中链接文本占全部文本的比例。
// 用 readability 的正文 HTML（已去掉大部分导航）作分母，避免整页导航稀释判断。
func linkDensity(contentHTML string) float64 {
	if strings.TrimSpace(contentHTML) == "" {
		return 0
	}
	doc, err := goquery.NewDocumentFromReader(strings.NewReader(contentHTML))
	if err != nil {
		return 0
	}
	total := textRuneCount(doc.Selection)
	if total == 0 {
		return 0
	}
	var linked int
	doc.Find("a").Each(func(_ int, s *goquery.Selection) {
		linked += textRuneCount(s)
	})
	if linked >= total {
		return 1
	}
	return float64(linked) / float64(total)
}

// textRuneCount 统计可见文本的字符数（忽略空白）。
func textRuneCount(s *goquery.Selection) int {
	return len([]rune(strings.Join(strings.Fields(s.Text()), "")))
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

// 发布时间在真实站点上有四种写法，标准 meta 只覆盖第一种：
//  1. <meta property="article:published_time" content="..."> / JSON-LD datePublished
//  2. 微格式：<abbr class="published" title="2026-10-23T08:06:16+08:00">（阮一峰）
//  3. 语义元素：<time datetime="...">
//  4. 纯文本：<span>2025-12-05</span>、<li class="read-time">2026-10-23（美团 / InfoQ，
//     都是前端渲染出来的，只有 class 名可用）
//
// 全部按 class/attr 名锚定，不裸扫页面里的日期字符串——评论区日期会污染发布时间。
var publishedPatterns = []*regexp.Regexp{
	regexp.MustCompile(`(?i)<meta[^>]+(?:property|name|itemprop)=["'](?:article:published_time|og:published_time|pubdate|publishdate|datePublished|date)["'][^>]+content=["']([^"']+)["']`),
	regexp.MustCompile(`(?i)<meta[^>]+content=["']([^"']+)["'][^>]+(?:property|name|itemprop)=["'](?:article:published_time|og:published_time|pubdate|publishdate|datePublished|date)["']`),
	regexp.MustCompile(`(?i)"datePublished"\s*:\s*"([^"]+)"`),
	regexp.MustCompile(`(?i)<time[^>]+datetime=["']([^"']+)["']`),
}

// dateNodes 常见「发布时间」语义 class。真实站点上日期常嵌在这些元素里
// （阮一峰的 abbr.published、美团的 .post-date、InfoQ 的 ul.read-time>li），
// 用 DOM 选择器比正则可靠：文本可能被包在子节点里，正则会失配。
// dateLike 从元素文本里识别日期片段（2026-10-23 / 2026/10/23）。
var dateLike = regexp.MustCompile(`20\d{2}[-/]\d{1,2}[-/]\d{1,2}`)

var dateNodes = []string{
	".published", ".post-date", ".entry-date", ".article-date", ".read-time",
	"[itemprop=datePublished]", "[itemprop=datePublished] *",
}

// dateInURL 从路径里取日期：/2026/10/23/、/2026/10/ 都能用。
// 站点把日期写进 URL 是最稳的信号（美团 /2025/12/05/、阮一峰 /blog/2026/10/），
// 比在页面里找日期安全得多。只到月份时按当月 1 号记，粒度写进注释。
var dateInURL = regexp.MustCompile(`/(20\d{2})/(0[1-9]|1[0-2])(?:/(0[1-9]|[12]\d|3[01]))?/`)

var dateLayouts = []string{
	time.RFC3339, "2006-01-02T15:04:05Z07:00", "2006-01-02 15:04:05",
	"2006-01-02T15:04:05", "2006-01-02 15:04", "2006-01-02", "2006/01/02",
}

// metaFields 从原始 HTML 提取 description 与发布时间；页面里没有时按 URL 路径兜底。
func metaFields(raw []byte, pageURL string) (desc string, published *int64) {
	html := string(raw)
	doc, err := goquery.NewDocumentFromReader(strings.NewReader(html))
	if err == nil {
		desc = clean(doc.Find(`meta[name="description"]`).First().AttrOr("content", ""))
		if desc == "" {
			desc = clean(doc.Find(`meta[property="og:description"]`).First().AttrOr("content", ""))
		}
		for _, sel := range dateNodes {
			n := doc.Find(sel).First()
			if n.Length() == 0 {
				continue
			}
			for _, attr := range []string{"datetime", "title", "content"} {
				if v, ok := n.Attr(attr); ok {
					if ts, ok := parseDate(v); ok {
						return desc, &ts
					}
				}
			}
			// 元素文本常把日期和别的字粘在一起（InfoQ：`2026-10-23北京`，
			// 中间没有空白，按空格切字段会得到 `2026-10-23北京`，解析必失败）。
			// 直接用正则捞第一个日期片段。
			if m := dateLike.FindString(n.Text()); m != "" {
				if ts, ok := parseDate(m); ok {
					return desc, &ts
				}
			}
		}
	}
	for _, re := range publishedPatterns {
		if m := re.FindStringSubmatch(html); len(m) == 2 {
			if ts, ok := parseDate(m[1]); ok {
				return desc, &ts
			}
		}
	}
	if ts, ok := dateFromURL(pageURL); ok {
		return desc, &ts
	}
	return desc, nil
}

// dateFromURL 从 URL 路径提取日期。只有年月时按当月 1 号算——
// 粒度粗，但比「没有发布时间、退回抓取时间」准确得多。
func dateFromURL(pageURL string) (int64, bool) {
	m := dateInURL.FindStringSubmatch(pageURL)
	if len(m) < 3 {
		return 0, false
	}
	year, _ := strconv.Atoi(m[1])
	month, _ := strconv.Atoi(m[2])
	day := 1
	if len(m) > 3 && m[3] != "" {
		day, _ = strconv.Atoi(m[3])
	}
	// 未来日期一律不采信：URL 里的版本号/ID 常被误认为年份
	t := time.Date(year, time.Month(month), day, 0, 0, 0, 0, time.UTC)
	if t.Year() > time.Now().Year()+1 {
		return 0, false
	}
	return t.Unix(), true
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

// SiteName 从站点首页 HTML 提取网站名称：og:site_name → application-name → <title>。
// 提供给爬虫在种子页调用，结果存 whitelist.site_name 供搜索结果展示。
// <title> 是最后兜底（不少首页 title 是「xxx — 导航」这类），所以截短并做站务词过滤。
func SiteName(raw []byte) string {
	doc, err := goquery.NewDocumentFromReader(bytes.NewReader(raw))
	if err != nil {
		return ""
	}
	for _, sel := range []string{
		`meta[property="og:site_name"]`,
		`meta[name="og:site_name"]`,
		`meta[name="application-name"]`,
		`meta[name="site_name"]`,
	} {
		if v := clean(doc.Find(sel).First().AttrOr("content", "")); v != "" && !IsBoilerplateTitle(v) {
			return truncate(v, 40)
		}
	}
	if t := clean(doc.Find("title").First().Text()); t != "" && !IsBoilerplateTitle(t) {
		return truncate(t, 40)
	}
	return ""
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

// PickTitle 在「重新处理快照」的路径上选择标题。
//
// 为什么需要：processor 提取不到标题时会兜底成域名（res.Title = hostname），
// 这在爬虫路径上是正确的（确实没有更好的）。但 reindex/repair 是拿 KV 快照
// 重新跑提取，而快照分两种：
//   - HTML 快照（爬虫落的）：能提出真标题
//   - 纯文本快照（seed 导入的语料只有 body，没有 HTML 结构）：提不到，
//     兜底成域名——把表里原本正确的标题覆盖掉
//
// 实测代价：语料内文档 41% 标题退化成域名（134/329），而新抓取的只有 1%；
// 根因就是一次中断的 reindex 拿纯文本快照重新提标题。
//
// 规则：提取结果已退化（等于域名/过短）而表里现存标题没退化时，保留表里的。
// 两条都退化就认提取的（域名兜底），与爬虫路径行为一致。
func PickTitle(extracted, existing, host string) string {
	if DegradedTitle(extracted, host) && !DegradedTitle(existing, host) {
		return existing
	}
	return extracted
}

// DegradedTitle 标题是否已经退化到没有检索价值。
// 判定：等于域名（含去 www）、短于 4 字符。这两种标题在结果页
// 只能显示成一个域名或一个词，用户无法判断该不该点。
//
// 实测（1131 篇）：128 篇退化，且 og:title 一篇也救不回来
// （TestDegradedTitleRecoverable）——这些页面连元数据都缺失，
// 兜底链到 <h1> 就断了。所以没有为它加 og:title 提取：为假设的收益
// 加代码不如先量。退化标题的文档靠正文匹配，BM25 下排序自然偏低，
// 暂不过滤也不降权（误杀索引页会漏掉 CLI 参考这类有用内容）。
func DegradedTitle(title, host string) bool {
	t := strings.TrimSpace(title)
	if len([]rune(t)) < 4 {
		return true
	}
	if host == "" {
		return false
	}
	bare := strings.TrimPrefix(strings.TrimPrefix(host, "www."), "WWW.")
	t2 := strings.TrimPrefix(t, "www.")
	return t2 == bare || t2 == host
}

// docTitleRe <title> 元素。不用 DOM：title 里不会有嵌套标签，正则足够，
// 且 Process 已经要为 meta 字段扫一遍原始字节，不值得为此再解析整棵 DOM。
var docTitleRe = regexp.MustCompile(`(?is)<title[^>]*>(.*?)</title>`)

// docTitleScanLimit <title> 的扫描上限。
//
// title 在 <head> 里，必然出现在文档开头；而 KV 快照上限 2MB。
// 不加限制的代价实测：1137 篇的全量重建从 93 秒变成 14 分钟（9 倍）——
// 正则本身是线性的，但 2MB 的输入里绝大多数内容在 body 深处，
// 对找 <head> 里的元素毫无用处，白扫 2.2GB。
const docTitleScanLimit = 64 << 10

// docTitle 取 <title> 文本（只看开头 64KB）。
func docTitle(raw []byte) string {
	head := raw
	if len(head) > docTitleScanLimit {
		head = head[:docTitleScanLimit]
	}
	m := docTitleRe.FindSubmatch(head)
	if len(m) < 2 {
		return ""
	}
	return clean(string(m[1]))
}

// titleSep 站点标题的惯用分隔符：「页面标题 | 站名」「页面标题 - 站名」。
// 不含 '/'：标题里出现路径时会切出无意义片段。
func titleSep(r rune) bool {
	return r == '|' || r == '-' || r == '—' || r == '–' || r == '·' || r == '»' || r == '_'
}

// isSiteSegment short 是否只是 full 的站名段（按分隔符切分后的最后一段）。
//
// 这正是 SPA 站点标题退化的形态：readability 从页头抓到站标，
// <title> 却是「页面标题 | 站名」的完整形式。只查最后一段而不是任意一段：
// 正常页面 readability 返回的是首段（"Go 语言规范"），不该被改写成带站名的全称。
func isSiteSegment(short, full string) bool {
	if short == "" || full == "" || len(short) >= len(full) {
		return false
	}
	segs := strings.FieldsFunc(full, titleSep)
	if len(segs) < 2 {
		return false
	}
	return strings.TrimSpace(segs[len(segs)-1]) == strings.TrimSpace(short)
}
