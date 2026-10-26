// Package querysyntax 解析查询里的语法糖。
//
// 支持的写法（对齐常见搜索引擎的习惯，只取最小可用集）：
//
//	+词      必须包含（AND）
//	-词      必须排除（NOT）
//	"短语"   整体匹配，不拆词；中英文引号都认
//	site:域名 只搜这个站点（含子域）
//
// 其余内容按普通关键词处理，沿用原有的宽松召回：中文查询词之间是 OR，
// 由排序层决定先后。这样加上语法不会把原本能搜到的结果挡掉。
package querysyntax

import (
	"fmt"
	"strings"
	"unicode"
)

// Query 解析结果。
type Query struct {
	Raw      string   // 原始输入，用于日志与回显
	Text     string   // 去掉语法符号后的正文：供 BM25 打分与语言判定使用
	Terms    []string // 普通关键词
	Required []string // +词
	Excluded []string // -词
	Phrases  []string // "短语"
	Site     string   // site: 指定的域名（已小写、去协议与首尾斜杠）
}

// HasSyntax 是否用到了任何语法（用于前端提示与响应回显）。
func (q Query) HasSyntax() bool {
	return len(q.Required) > 0 || len(q.Excluded) > 0 || len(q.Phrases) > 0 || q.Site != ""
}

// Parse 解析查询串。解析失败从不报错——输入里的符号当成普通字符对待，
// 让人搜到东西永远比告诉用户「语法错误」重要。
func Parse(raw string) Query {
	q := Query{Raw: raw}
	rs := []rune(strings.TrimSpace(raw))
	var body []string
	addBody := func(s string) {
		if s = strings.TrimSpace(s); s != "" {
			body = append(body, s)
		}
	}

	for i := 0; i < len(rs); {
		r := rs[i]
		// 空白直接跳过：否则 default 分支会取到空词并原地打转（死循环）
		if unicode.IsSpace(r) {
			i++
			continue
		}
		switch {
		case isQuote(r):
			// 短语：吃到下一个引号为止。没闭合就当作到结尾。
			j := i + 1
			for j < len(rs) && !isQuote(rs[j]) {
				j++
			}
			phrase := strings.TrimSpace(string(rs[i+1 : j]))
			if phrase != "" {
				q.Phrases = append(q.Phrases, phrase)
				addBody(phrase)
			}
			i = j + 1

		case r == '+' || r == '-':
			// 取到空白为止；允许写成 +"北京 大学" 这种带引号的形式
			j := i + 1
			quoted := j < len(rs) && isQuote(rs[j])
			if quoted {
				j++
				for j < len(rs) && !isQuote(rs[j]) {
					j++
				}
				term := strings.TrimSpace(string(rs[i+2 : j]))
				q.appendTerm(r, term, addBody)
				i = j + 1
				continue
			}
			for j < len(rs) && !unicode.IsSpace(rs[j]) {
				j++
			}
			term := strings.TrimSpace(string(rs[i+1 : j]))
			q.appendTerm(r, term, addBody)
			i = j

		default:
			j := i
			for j < len(rs) && !unicode.IsSpace(rs[j]) {
				j++
			}
			word := string(rs[i:j])
			if site, ok := parseSite(word); ok {
				if q.Site == "" {
					q.Site = site
				} else {
					// 第二个 site: 当作普通词，避免静默丢掉用户输入
					q.Terms = append(q.Terms, word)
					addBody(word)
				}
				i = j
				continue
			}
			q.Terms = append(q.Terms, word)
			addBody(word)
			i = j
		}
	}

	q.Text = strings.Join(body, " ")
	return q
}

// appendTerm 收一个带 +/- 前缀的词。
//
// 只有 Required 会同时进正文：排除词一旦进正文，BM25 会把它当成「要匹配的内容」，
// 于是「+北京 -大学」会去给含「大学」的文档加分——正好反了。
func (q *Query) appendTerm(sign rune, term string, addBody func(string)) {
	term = strings.Trim(term, "+-")
	if term == "" {
		return
	}
	if sign == '+' {
		q.Required = append(q.Required, term)
		addBody(term)
		return
	}
	q.Excluded = append(q.Excluded, term)
}

// isQuote 兼容中英文引号：用户在中文输入法下打出来的是 “ ”。
func isQuote(r rune) bool {
	return r == '"' || r == '\u201c' || r == '\u201d'
}

// parseSite 识别 site:域名。返回 ok 表示这是 site 语法而不是普通词。
func parseSite(word string) (string, bool) {
	lower := strings.ToLower(word)
	if !strings.HasPrefix(lower, "site:") {
		return "", false
	}
	site := strings.TrimSpace(lower[len("site:"):])
	site = strings.TrimPrefix(site, "https://")
	site = strings.TrimPrefix(site, "http://")
	site = strings.Trim(site, "/")
	// 只保留域名部分，允许用户粘贴整条 URL
	if i := strings.IndexAny(site, "/?#"); i >= 0 {
		site = site[:i]
	}
	if site == "" || !strings.Contains(site, ".") {
		return "", false
	}
	return site, true
}

// String 便于日志。
func (q Query) String() string {
	var parts []string
	if q.Text != "" {
		parts = append(parts, "text="+q.Text)
	}
	if len(q.Required) > 0 {
		parts = append(parts, "required="+strings.Join(q.Required, ","))
	}
	if len(q.Excluded) > 0 {
		parts = append(parts, "excluded="+strings.Join(q.Excluded, ","))
	}
	if len(q.Phrases) > 0 {
		parts = append(parts, fmt.Sprintf("phrases=%d", len(q.Phrases)))
	}
	if q.Site != "" {
		parts = append(parts, "site="+q.Site)
	}
	return strings.Join(parts, " ")
}
