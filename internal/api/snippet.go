package api

import (
	"html"
	"sort"
	"strings"
	"unicode"
	"unicode/utf8"
)

// 证据片段（snippets）：把「查询词在正文里出现的位置」返回给前端。
//
// 这是证据链摘要的前置能力——AI 摘要的每条结论都要能回指原文片段，
// 那么第一步就是先把片段定位做出来。不依赖 LLM，也不改索引：
// 正文从 KV 快照取（/api/page 本来就读它），在 API 层做窗口扫描。
//
// 为什么不用 Bleve 的 Highlighter：那需要 body 字段开 IncludeTermVectors，
// 索引体积约涨三到五成，而片段只对当前页（≤50 条）需要，代价不划算。

const (
	// snippetWindow 单个片段的窗口大小（字符数）。中文约 2~3 句，够看清上下文。
	snippetWindow = 240
	// snippetMaxPerDoc 每篇文档最多返回几个片段。
	snippetMaxPerDoc = 2
	// snippetMinLen 正文短于该值时不切片（整段返回即可）。
	snippetMinLen = 200
)

// snippetTokens 从查询里提取用于片段匹配的词。
//
// 中文按 2-gram 切（「单元测试」→ 单元/元测/测试），因为正文里的写法
// 未必与查询逐字一致（「单测」），任何一段命中都算相关；
// 英文按空白切且要求长度 ≥3，避免 the/of 这类词把每个窗口都点亮。
func snippetTokens(q string) []string {
	var out []string
	seen := map[string]bool{}
	add := func(s string) {
		if s != "" && !seen[s] {
			seen[s] = true
			out = append(out, s)
		}
	}
	for _, field := range strings.Fields(strings.ToLower(q)) {
		runes := []rune(field)
		var latin strings.Builder
		flushLatin := func() {
			if latin.Len() >= 3 {
				add(latin.String())
			}
			latin.Reset()
		}
		var han []rune
		flushHan := func() {
			// 单字不成词，跳过；两字以上做 bigram
			for i := 0; i+1 < len(han); i++ {
				add(string(han[i : i+2]))
			}
			han = han[:0]
		}
		for _, r := range runes {
			if unicode.Is(unicode.Han, r) {
				flushLatin()
				han = append(han, r)
			} else if unicode.IsLetter(r) || unicode.IsDigit(r) || r == '-' || r == '.' || r == '#' || r == '+' {
				flushHan()
				latin.WriteRune(r)
			} else {
				flushLatin()
				flushHan()
			}
		}
		flushLatin()
		flushHan()
	}
	return out
}

// htmlToText 把快照 HTML 粗剥成纯文本。
//
// 为什么需要：KV 快照存的是**原始 HTML**（/api/page 要用它喂 sandbox iframe 渲染原文），
// 而片段需要的是可读文本。实测 tech.meituan.com 的快照 80KB、2794 个标签，
// 不剥的话片段开头全是 `<link rel="alternate" type="application/rss+xml" ...>`。
//
// 为什么用扫描而不是 html 解析器：正文提取（processor）已经在索引期做过一次，
// 这里只是给「当前页 ≤50 条结果」生成可读上下文，每篇解析一次 DOM 的代价
// （80KB 约 10ms × 10 条）比剥标签（约 1ms）高一个量级，收益不值。
//
// 为什么扫描而不是正则：两个全局正则在长串上要反复回滚。实测 67KB 规整页面
// 扫描快 4.9 倍（985μs → 201μs），到 2MB 的页面上差距进一步拉大。
// 与正则版的行为等价性由 TestStripTagsScanMatchesRegex 逐用例钉住，
// 覆盖大小写标签、属性里含 >、script/style 整段跳过、以及畸形 HTML
// （'<' 后面没有 '>' 时必须原样保留，不能把尾部静默丢掉）。
//
// 末尾做 HTML 实体解码（&amp; → &）：片段是要展示给用户看的，
// 带着 &amp; 会显得像没处理过的原始数据。代价是对输出再做一次全串扫描，
// 在已截到 5 万字符的量级上可以忽略。
func htmlToText(s string) string {
	return htmlToTextN(s, 0)
}

// snippetMaxTextRunes 参与片段扫描的正文上限。
//
// KV 快照最大 2MB（crawler 上限），其中九成是脚本与内联样式，而正文通常只有几千字。
// 不限长度的代价实测：单篇剥标签 147ms、窗口扫描 380ms，一页十条就是 5 秒级。
// 5 万字符约等于一篇长文的前 2/3，证据片段（前 2 个命中窗口）几乎总在这里。
const snippetMaxTextRunes = 50000

// htmlToTextN 剥标签，输出超过 maxRunes 个字符就停（<=0 表示不限制）。
// 提前终止不只是省 CPU：2MB 的页面里 90% 是标签，剥到 5 万字符输出时
// 往往只处理了几十万字节的输入，剩下的大半根本不用看。
func htmlToTextN(s string, maxRunes int) string {
	if !strings.Contains(s, "<") {
		return clipRunes(s, maxRunes) // 已经是纯文本（seed 导入的语料就是这种）
	}
	var out strings.Builder
	out.Grow(min(len(s), 1<<16))
	// outRunes 增量维护输出长度。不要用 out.String() 去查长度——
	// Builder.String() 会把已写内容整份拷贝，几万次迭代下这是 O(n²)：
	// 实测 2MB 页面因此从 20ms 劣化到近 1 秒。
	outRunes := 0
	for i := 0; i < len(s); {
		if s[i] != '<' {
			// 连续的非标签段整块拷贝，避免逐字符 Append
			j := strings.IndexByte(s[i:], '<')
			if j < 0 {
				seg := clipRunes(s[i:], maxRunes-outRunes)
				out.WriteString(seg)
				break
			}
			out.WriteString(s[i : i+j])
			outRunes += utf8.RuneCountInString(s[i : i+j])
			i += j
			if maxRunes > 0 && outRunes >= maxRunes {
				break
			}
			continue
		}
		if tag, ok := skipTag(s[i:]); ok {
			end := strings.IndexByte(s[i:], '>')
			if end < 0 {
				out.WriteString(s[i:])
				break
			}
			rest := s[i+end+1:]
			closeTag := "</" + tag + ">"
			if k := indexFold(rest, closeTag); k >= 0 {
				i = i + end + 1 + k + len(closeTag)
				continue
			}
			i += end + 1
			continue
		}
		end := strings.IndexByte(s[i:], '>')
		if end < 0 {
			// 畸形 HTML：'<' 后面没有 '>'，原样保留（与正则版一致）
			out.WriteString(s[i:])
			break
		}
		out.WriteByte(' ')
		outRunes++
		i += end + 1
		if maxRunes > 0 && outRunes >= maxRunes {
			break
		}
	}
	return strings.Join(strings.Fields(html.UnescapeString(out.String())), " ")
}

func clipRunes(s string, max int) string {
	if max <= 0 || len([]rune(s)) <= max {
		return s
	}
	r := []rune(s)
	return string(r[:max])
}

// skipTag 判断是否是 script/style/noscript 开标签，返回小写标签名。
func skipTag(s string) (string, bool) {
	for _, name := range []string{"script", "style", "noscript"} {
		if hasTagPrefix(s, name) {
			return name, true
		}
	}
	return "", false
}

// hasTagPrefix s 是否以 "<name" 开头（大小写不敏感，不要求闭合）。
func hasTagPrefix(s, name string) bool {
	if len(s) < len(name)+1 || s[0] != '<' {
		return false
	}
	for i := 0; i < len(name); i++ {
		c := s[i+1]
		if c >= 'A' && c <= 'Z' {
			c += 'a' - 'A'
		}
		if c != name[i] {
			return false
		}
	}
	return true
}

// indexFold 大小写不敏感的 Index（sub 必须已是小写），零分配。
func indexFold(s, sub string) int {
	n := len(sub)
	if n == 0 {
		return 0
	}
	for i := 0; i+n <= len(s); i++ {
		ok := true
		for j := 0; j < n; j++ {
			c := s[i+j]
			if c >= 'A' && c <= 'Z' {
				c += 'a' - 'A'
			}
			if c != sub[j] {
				ok = false
				break
			}
		}
		if ok {
			return i
		}
	}
	return -1
}

// buildSnippets 从正文里挑出与查询最相关的窗口，最多 limit 个且互不重叠。
//
// 打分只看「窗口内命中了多少个不同的查询词」，不做词频加权——
// 词频高的窗口往往是把关键词重复堆砌的导航/目录段，不是答案所在。
func buildSnippets(body, q string, limit int) []string {
	body = strings.TrimSpace(htmlToTextN(body, snippetMaxTextRunes))
	if body == "" {
		return nil
	}
	r := []rune(body)
	if len(r) <= snippetMinLen {
		return []string{string(r)}
	}
	toks := snippetTokens(q)
	if len(toks) == 0 {
		return nil
	}
	if limit <= 0 {
		limit = snippetMaxPerDoc
	}

	type cand struct {
		start, end, hits int
	}
	var cands []cand
	step := snippetWindow / 2
	for start := 0; start < len(r); start += step {
		end := start + snippetWindow
		if end > len(r) {
			end = len(r)
		}
		seg := strings.ToLower(string(r[start:end]))
		hits := 0
		for _, t := range toks {
			if strings.Contains(seg, t) {
				hits++
			}
		}
		if hits > 0 {
			cands = append(cands, cand{start, end, hits})
		}
		if end == len(r) {
			break
		}
	}
	if len(cands) == 0 {
		return nil
	}
	// 命中多的优先；同分取靠前的（摘要通常出现在正文前部）
	sort.SliceStable(cands, func(i, j int) bool {
		if cands[i].hits != cands[j].hits {
			return cands[i].hits > cands[j].hits
		}
		return cands[i].start < cands[j].start
	})

	var out []string
	var picked []cand
	for _, c := range cands {
		overlap := false
		for _, p := range picked {
			if c.start < p.end && p.start < c.end {
				overlap = true
				break
			}
		}
		if overlap {
			continue
		}
		picked = append(picked, c)
		out = append(out, clip(string(r[c.start:c.end])))
		if len(out) >= limit {
			break
		}
	}
	return out
}

// clip 压缩空白并标注截断（窗口可能落在词中间）。
func clip(s string) string {
	s = strings.Join(strings.Fields(s), " ")
	const max = 260
	r := []rune(s)
	if len(r) > max {
		return string(r[:max]) + "…"
	}
	return s
}
