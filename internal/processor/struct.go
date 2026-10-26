package processor

import (
	"regexp"
	"sort"
	"strings"
)

// ---------- DOM 结构模板指纹（采集站识别的路径三） ----------
//
// 正文指纹（shingle）认不出的场景：段落重排、洗稿。而采集站还有另一个特征——
// **同一套模板批量生成**。sese-engine 的 html结构特征就是抓这个：
// 抽 DOM 树、跳过内容标签、只留容器骨架。跨站比对时，结构相同 = 同一套模板。
//
// 实测分离度（235 篇真实快照采样，路径数 ≥ 15）：
//
//	同站距离  最小 0   P25 0    中位 0    （模板完全一致）
//	跨站距离  最小 14  P25 26   中位 29
//
// 中间隔 14 位，所以「跨站但结构极近」是强信号——真实语料里跨站最小才 14。
//
// 未入库：当前白名单没有采集站样本，没有消费方，入库等于白白多一次全量重算。
// 接入真实样本后若需要高频判定，再考虑加列（那时连阈值一起定）。

// structMaxDepth 结构树深度上限。
//
// 超深嵌套会产生大量长路径，把浅层骨架（真正代表模板的部分）淹没。
// 8 层足够容纳绝大多数页面的主体结构。
const structMaxDepth = 8

// structMinPaths 有效结构的最小路径数。
//
// 必须有这个门槛：SPA 空壳页面（JS 渲染，HTML 里只有骨架）结构全都一样，
// 实测跨站距离为 0 的对全是这类（路径数 10）——云厂商和资讯平台会被判成同一套模板。
// 低于门槛返回 0 哨兵，与 ShingleHash 的零值哨兵同口径（0 = 无可用指纹）。
const structMinPaths = 15

// structSkipTags 内容标签：承载文本，不代表模板结构。
// 沿用 sese-engine 的列表（a/p/li/ul/span/img/br/svg），补几个纯内联标签。
var structSkipTags = map[string]bool{
	"a": true, "p": true, "li": true, "ul": true, "span": true,
	"img": true, "br": true, "svg": true, "b": true, "i": true,
	"em": true, "strong": true, "code": true, "pre": true,
	"small": true, "u": true, "s": true, "sub": true, "sup": true,
}

// structVoidTags 自闭合标签，不入栈。
var structVoidTags = map[string]bool{
	"br": true, "img": true, "input": true, "meta": true, "link": true,
	"hr": true, "source": true, "area": true, "base": true, "col": true,
	"embed": true, "param": true, "track": true, "wbr": true,
}

var structTagRe = regexp.MustCompile(`<(/)?([a-zA-Z][a-zA-Z0-9]*)((?:\s[^>]*)?)(/?)>`)
var structSkipBlockRe = regexp.MustCompile(`(?is)<(script|style|noscript)\b[^>]*>.*?</(script|style|noscript)>`)

// StructHash 由 HTML 算 DOM 结构模板指纹。
//
// 做法：扫标签维护栈，每个容器节点产生一条「根到该节点」的路径
// （如 html>body>div>article），路径集合去重后取 Simhash。
//
// 为什么用路径而不是标签序列：嵌套关系才是模板的形状，
// 「html>body>div」和「html>div>body」在序列里一样、在路径里不同。
// 为什么去重：同一结构重复 100 次不该让它在 Simhash 里权重翻 100 倍——
// 要回答的是「有哪些结构模式」，不是「重复了多少次」。
//
// 结构信息不足（纯文本快照、SPA 空壳）时返回 0。
func StructHash(raw []byte) uint64 {
	paths := StructPaths(raw)
	if len(paths) < structMinPaths {
		return 0
	}
	return Simhash(paths, nil)
}

// StructPaths 提取结构路径集合（已排序去重，深度截断）。
// 导出的目的是让诊断与将来的判定逻辑能直接看到「这个页面的骨架长什么样」。
func StructPaths(raw []byte) []string {
	cleaned := structSkipBlockRe.ReplaceAll(raw, nil)
	var stack []string
	seen := map[string]bool{}
	var out []string
	for _, m := range structTagRe.FindAllSubmatch(cleaned, -1) {
		tag := strings.ToLower(string(m[2]))
		if len(m[1]) > 0 { // 闭标签：弹栈
			for i := len(stack) - 1; i >= 0; i-- {
				if stack[i] == tag {
					stack = stack[:i]
					break
				}
			}
			continue
		}
		if len(m[4]) > 0 || structVoidTags[tag] {
			continue
		}
		stack = append(stack, tag)
		// 超深时仍要继续维护栈（否则闭标签弹不干净），只是不再记录路径
		if len(stack) > structMaxDepth {
			continue
		}
		if structSkipTags[tag] {
			continue
		}
		path := strings.Join(stack, ">")
		if !seen[path] {
			seen[path] = true
			out = append(out, path)
		}
	}
	sort.Strings(out) // 顺序确定，Simhash 才可复现
	return out
}
