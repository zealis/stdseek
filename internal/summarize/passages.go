package summarize

import (
	"math"
	"regexp"
	"sort"
	"strings"
	"unicode"
)

// 段落抽取：把正文切成句子，按「与查询的相关度」挑出编号候选段落。
//
// 这一步不依赖 LLM，是整条链路的护城河：模型只能在被选中的段落里作答，
// 所以「模型编造原文」在结构上就不可能发生——它拿不到正文之外的任何材料。

const (
	// passageMinRunes 句子短于该值且不含标点时视为导航噪声丢弃。
	// 实测典型噪声：tech.meituan.com 的「首页 历史文章 技术沙龙 关于我们」。
	passageMinRunes = 12
	// passageMaxRunes 单段上限。超过就截断——候选段落是给人读的，
	// 600 字已经足够覆盖一个完整论述，再长会把编号列表撑爆上下文。
	passageMaxRunes = 600
	// passageMinScore 入选门槛。低于它的句子只是"沾了个查询词"，
	// 拿它当结论会得到「Gofmt will do that for you.」这种碎片。
	// 门槛挡住的不是长尾，是"没有这块内容"——此时应当返回空，让用户看到
	// 「未找到可靠来源」，而不是一段看起来像摘要的废话。
	passageMinScore = 0.15
	// passageMinLen 完整句子的最短长度（字符）。英文碎片句
	// （"Gofmt will do that for you."）通常在这里被挡掉。
	passageMinLen = 20
)

// SelectPassages 选出与查询最相关的段落，最多 max 段，按**原文顺序**返回。
//
// 为什么要恢复原文顺序而不是按分数排：摘要读起来要像"按文章脉络讲的"，
// 按分数排会把结论打乱成互不相干的句子；分数只用于挑选，不用于排列。
//
// expand 可选：把查询词扩展成其他语言的写法（中英术语词典）。
// 只对**中英混杂查询**生效——纯中文查询必须只匹配中文内容（红线 9），
// 否则「单元测试」会去命中英文页里的 unit test，把中文用户导向英文摘要。
func SelectPassages(query, body string, max int, expand func(string) []string) []Passage {
	if max <= 0 {
		max = 8
	}
	body = strings.TrimSpace(body)
	if body == "" {
		return nil
	}
	fields := queryFields(query)
	grams := queryTokens(query)
	if len(fields) == 0 && len(grams) == 0 {
		return nil
	}
	if expand == nil || !hasLatin(query) {
		expand = nil
	}

	type cand struct {
		pos   int // 句子的原始序号，用于恢复原文顺序
		text  string
		score float64
	}
	var cands []cand
	seen := make(map[string]bool)
	for i, s := range splitSentences(body) {
		s = strings.TrimSpace(s)
		r := []rune(s)
		if len(r) < passageMinLen {
			continue
		}
		// 短且无标点 → 导航/按钮文字，不是正文句子
		if len(r) < passageMinRunes && !hasSentencePunct(s) {
			continue
		}
		if len(r) > passageMaxRunes {
			s = string(r[:passageMaxRunes]) + "…"
		}
		// 完全相同的句子只保留第一次出现（页头页尾常重复标题）
		key := strings.ToLower(s)
		if seen[key] {
			continue
		}
		sc := scoreSentence(s, fields, grams, expand)
		if sc < passageMinScore {
			continue
		}
		seen[key] = true
		cands = append(cands, cand{pos: i, text: s, score: sc})
	}
	if len(cands) == 0 {
		return nil
	}

	sort.SliceStable(cands, func(i, j int) bool {
		if cands[i].score != cands[j].score {
			return cands[i].score > cands[j].score
		}
		return cands[i].pos < cands[j].pos // 同分取靠前的（结论常出现在开头）
	})
	if len(cands) > max {
		cands = cands[:max]
	}
	sort.SliceStable(cands, func(i, j int) bool { return cands[i].pos < cands[j].pos })

	out := make([]Passage, len(cands))
	for i, c := range cands {
		out[i] = Passage{ID: i + 1, Text: c.text, Score: math.Round(c.score*1000) / 1000}
	}
	return out
}

// scoreSentence 句子与查询的相关度。
//
// 两级信号，权重差 4 倍：
//   - fields（整串概念）：查询「单元测试」在句中以完整形态出现，是强信号
//   - grams（bigram 兜底）：正文写「单测」「测试流程」时还能沾上
//
// 只看 grams 会踩到一个真实的坑：一句「针对上述三个常见痛点，本文提出采用
// 不同的单元测试策略」会命中 单元/元测/测试 三个 gram，比真正讲清事情的
// 「单元测试能够独立运行、快速验证结果」得分还高——前者是过渡句，后者才是答案。
//
// 两个修饰因子：
//   - 含数字/英文/代码符号 → 信息密度高，×1.25
//   - 过渡句开场（针对上述/本文/接下来…）→ ×0.5，这类句子几乎不含事实
func scoreSentence(sent string, fields, grams []string, expand func(string) []string) float64 {
	low := strings.ToLower(sent)
	hitFields := 0
	for _, f := range fields {
		if fieldHit(low, f, expand) {
			hitFields++
		}
	}
	hitGrams := 0
	for _, g := range grams {
		if fieldHit(low, g, expand) {
			hitGrams++
		}
	}
	if hitFields == 0 && hitGrams == 0 {
		return 0
	}
	// 按**覆盖率**而不是命中数：查询「go 语言规范」有两个概念，英文页只命中 go 一个，
	// 覆盖率 0.5 说明这段只沾了个弱词。用绝对命中数会让「含 go 的任意英文句子」都得满分。
	raw := 0.7*ratio(hitFields, len(fields)) + 0.3*ratio(hitGrams, len(grams))
	sc := raw / normLen(sent)
	if hasInfoDensity(sent) {
		sc *= 1.25
	}
	if transitionalRe.MatchString(sent) {
		sc *= 0.5
	}
	// 感叹句多是营销/呼吁（"Help us shape the future of Go!"），不是事实陈述。
	// 实测这类句子在英文页里稳定占掉一个名额，压下去能让位给真正的论述句。
	if t := strings.TrimSpace(sent); strings.HasSuffix(t, "!") || strings.HasSuffix(t, "！") {
		sc *= 0.6
	}
	return sc
}

func ratio(hit, total int) float64 {
	if total == 0 {
		return 0
	}
	return float64(hit) / float64(total)
}

// normLen 长度归一的底数，按语言折算到"可比的单元数"。
//
// 直接按字符数开方会让英文句子系统性吃亏：同样一句结论，中文 25 字、
// 英文 75 字符，英文得分只有中文的 57%（√25 vs √75）。实测后果是
// 「Rust 社区」打英文 Community 页面时，覆盖率 100% 的段落也过不了门槛。
// 英文按 3 字符折 1 个单元（平均词长 4~5，扣掉空格），两边就落在同一量级。
func normLen(s string) float64 {
	n := float64(len([]rune(s)))
	han := 0
	for _, r := range s {
		if unicode.Is(unicode.Han, r) {
			han++
		}
	}
	if han*2 < int(n) { // 汉字不到一半 → 以拉丁为主
		n /= 3
	}
	if n < 1 {
		n = 1
	}
	return math.Sqrt(n)
}

// fieldHit 一个查询概念是否出现在句子里，命中任一变体即算命中。
//
// 跨语言场景（「Rust 社区」→ Community 页面）全靠这一步：
// 中文修饰词在英文正文里不存在，不扩展就永远是一半覆盖率。
func fieldHit(text, f string, expand func(string) []string) bool {
	if containsWord(text, f) {
		return true
	}
	if expand == nil {
		return false
	}
	for _, v := range expand(f) {
		if v != "" && containsWord(text, strings.ToLower(v)) {
			return true
		}
	}
	return false
}

// hasLatin 查询里是否含拉丁字母（区分「纯中文查询」与「中英混杂查询」）。
func hasLatin(s string) bool {
	for _, r := range s {
		if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') {
			return true
		}
	}
	return false
}

// containsWord 子串匹配，但对纯拉丁词要求词边界。
//
// 为什么必须做：查「go 语言规范」时，英文页里的 "Gofmt will do that for you."
// 会因为子串含 go 而命中——这类误判在英文技术文本里遍地都是
// （go to / category / django）。中文词没有词边界概念，仍按子串匹配。
func containsWord(text, w string) bool {
	if !isLatinWord(w) {
		return strings.Contains(text, w)
	}
	for i := 0; ; {
		j := strings.Index(text[i:], w)
		if j < 0 {
			return false
		}
		j += i
		var before, after byte = ' ', ' '
		if j > 0 {
			before = text[j-1]
		}
		if j+len(w) < len(text) {
			after = text[j+len(w)]
		}
		if !isWordChar(before) && !isWordChar(after) {
			return true
		}
		i = j + len(w)
	}
}

// isLatinWord 是否纯 ASCII 字母数字（含 . - # + 这类技术符号）。
func isLatinWord(s string) bool {
	if s == "" {
		return false
	}
	for i := 0; i < len(s); i++ {
		c := s[i]
		if !isWordChar(c) && c != '.' && c != '-' && c != '#' && c != '+' {
			return false // 含中文或其他字节
		}
		if c >= 0x80 {
			return false
		}
	}
	return true
}

func isWordChar(c byte) bool {
	return (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') || (c >= '0' && c <= '9') || c == '_'
}

// transitionalRe 过渡句的典型开场。这些句子在讲"接下来要讲什么"，
// 而不是在讲事实，作为摘要结论毫无价值。
var transitionalRe = regexp.MustCompile(`^\s*(针对上述|综上所述|综上|由此可见|本文|本小节|接下来|下面(我们|来)?|如上所述)`)

// hasInfoDensity 句子里是否有具体信息（数字、英文术语、代码符号）。
// 这是"信息量"的廉价代理：技术文章里讲清事实的句子几乎总带着版本号、参数或术语。
func hasInfoDensity(s string) bool {
	hasLatin := false
	for _, r := range s {
		if r >= '0' && r <= '9' {
			return true
		}
		if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') {
			hasLatin = true
		}
	}
	return hasLatin
}

// queryFields 查询的概念单元：按空白切，跳过单字符。
// 「单元测试」整体是一个概念，不该因为 bigram 切成三个而拿三份分。
func queryFields(q string) []string {
	var out []string
	seen := map[string]bool{}
	for _, f := range strings.Fields(strings.ToLower(q)) {
		f = strings.TrimSpace(f)
		if len([]rune(f)) < 2 || seen[f] {
			continue
		}
		seen[f] = true
		out = append(out, f)
	}
	return out
}

// splitSentences 切句。
//
// 中文按 。！？；与换行断；英文句点只在**后面是空白或结尾**时才算句号——
// 否则 v1.28、Node.js、asp.net 会被切碎，而这些在技术文本里到处都是。
func splitSentences(text string) []string {
	r := []rune(text)
	var out []string
	start := 0
	flush := func(end int) {
		if s := strings.TrimSpace(string(r[start:end])); s != "" {
			out = append(out, s)
		}
		start = end
	}
	for i := 0; i < len(r); i++ {
		switch r[i] {
		case '。', '！', '？', '；', '\n', '!', '?', ';':
			flush(i + 1)
		case '.':
			if i+1 >= len(r) || unicode.IsSpace(r[i+1]) {
				flush(i + 1)
			}
		}
	}
	if start < len(r) {
		flush(len(r))
	}
	return out
}

// queryTokens 查询词切分，与 api 层片段匹配保持同一套规则：
// 中文按 2-gram（「单元测试」→ 单元/元测/测试），英文按空白切且长度 ≥3。
func queryTokens(q string) []string {
	var out []string
	seen := map[string]bool{}
	add := func(s string) {
		if s != "" && !seen[s] {
			seen[s] = true
			out = append(out, s)
		}
	}
	for _, field := range strings.Fields(strings.ToLower(q)) {
		var latin strings.Builder
		var han []rune
		flushLatin := func() {
			if latin.Len() >= 3 {
				add(latin.String())
			}
			latin.Reset()
		}
		flushHan := func() {
			for i := 0; i+1 < len(han); i++ {
				add(string(han[i : i+2]))
			}
			han = han[:0]
		}
		for _, r := range []rune(field) {
			switch {
			case unicode.Is(unicode.Han, r):
				flushLatin()
				han = append(han, r)
			case unicode.IsLetter(r) || unicode.IsDigit(r) || r == '-' || r == '.' || r == '#' || r == '+':
				flushHan()
				latin.WriteRune(r)
			default:
				flushLatin()
				flushHan()
			}
		}
		flushLatin()
		flushHan()
	}
	return out
}

func hasSentencePunct(s string) bool {
	return strings.ContainsAny(s, "。！？；，、,.!?;:：")
}
