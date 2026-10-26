// Package lang 做查询与文档的语言判定，用于守护「语言边界」这条产品红线。
//
// 红线内容（规划书第八节第 9 条）：中文 query 只召回中文内容，中英混杂 query
// 才允许跨语言召回。此前这条边界靠 cjk bigram 的字面匹配「天然成立」——
// 中文查询不含拉丁 token 就打不到英文正文，但没有任何一行代码在守护它。
// 一旦引入语义召回（向量检索、查询翻译、双语索引），这个保证就会失效，
// 中文查询开始召回英文内容，而用户完全看不出为什么。
//
// 因此这里提供显式判定，并由索引层做硬过滤：语言不匹配的候选直接丢弃，
// 不做「降权保留」（降权后仍会漏进结果页），也**不提供关闭开关**——
// 一旦可关，某次调参就会顺手关掉，且很难在指标上发现。
package lang

import (
	"strings"
	"unicode"
)

// 语言标签。只有两种取值：宁可不区分中英程度，也要让边界可判定。
const (
	Zh = "zh"
	En = "en"
)

// Detect 判定查询语言：
//
//	zh   纯中文（不含任何拉丁词）——只召回中文内容
//	en   纯拉丁（如 docker buildx）——不限制（见 DocLang 注释）
//	mix  中英混杂（如「docker 构建镜像」）——不限制，允许跨语言召回
//	none 没有字母（纯数字/符号/空串）——不限制
//
// 为什么英文查询不设限制：边界要保护的是「中文查询别被英文内容淹没」。
// 反过来，用户输入 docker 时看到 kubernetes 中文文档通常正是他想要的；
// 且术语桥类查询（go 语言规范）本身就是混合形态，收紧英文查询会让
// 「官方英文文档 + 中文修饰词」这一主要用法受损。
func Detect(query string) string {
	var han, latin int
	for _, r := range query {
		switch {
		case unicode.Is(unicode.Han, r):
			han++
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z':
			latin++
		}
	}
	switch {
	case han > 0 && latin == 0:
		return Zh
	case latin > 0 && han == 0:
		return En
	case han > 0 && latin > 0:
		return "mix"
	default:
		return "none"
	}
}

// DocLang 判定文档语言。阈值取 0.2：技术文档常带大量英文术语
// （《分布式系统》正文里 Kubernetes、Raft 随处可见），用 0.5 会把这些
// 中文文章误判成英文，正好破坏要守护的边界。
//
// 没有字母的页面（纯数字/符号/空正文）归到中文：本站是中文技术内容优先，
// 且这类页面本就是噪声，归到哪边都不会有真实用户受损。
func DocLang(title, body string) string {
	han, latin := letterCounts(title + " " + body)
	if han+latin == 0 {
		return Zh // 无字母 = 无证据，按本站中文优先
	}
	if float64(han)/float64(han+latin) >= 0.2 {
		return Zh
	}
	return En
}

// letterCounts 统计中文字数与拉丁字母数。分母只数「字母」：数字、标点、
// 空格不参与，否则中英混排的技术文本（kubernetes.io / v1.28）会被大量
// 非字母字符稀释，导致误判。
func letterCounts(s string) (han, latin int) {
	for _, r := range s {
		switch {
		case unicode.Is(unicode.Han, r):
			han++
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z':
			latin++
		}
	}
	return han, latin
}

// MustZh 判断该查询是否要求「只召回中文内容」。
func MustZh(query string) bool { return Detect(query) == Zh }

// IsZh 报告标签是否为中文（供查询层使用）。
func IsZh(tag string) bool { return strings.TrimSpace(tag) == Zh }
