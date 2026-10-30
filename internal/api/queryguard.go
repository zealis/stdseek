// 检索入口的输入防护。
//
// /search?q= 是本站唯一「把用户输入直接交给检索引擎」的地方，也是最容易被当成
// 放大器用的入口：一个字符数不设限的 q 会在 Bleve 里展开成上千个 term 的布尔查询，
// 一次请求的开销可以是正常查询的几十倍。
//
// 这里做四件事，全部在进索引之前完成：
//  1. 整个请求 URI 的长度上限（超了返回 414，连解析都不做）
//  2. 字符白/黑名单：控制字符、零宽字符、双向覆盖字符一律剥掉
//  3. 字符数与词数上限
//  4. 参数个数上限（fields/exclude/cat 这类逗号列表）
package api

import (
	"errors"
	"fmt"
	"net/http"
	"strings"
	"unicode"
	"unicode/utf8"
)

const (
	// maxQueryRunes 查询词上限。
	//
	// 为什么是 256 而不是「和地址栏一样长」：地址栏能放 2048 个字符，但那是 URL 的边界，
	// 不是检索的边界。Bleve 会把 q 分词成 term 逐个构造布尔查询，256 个中文字符已经
	// 展开成两百多个 term——真实用户的中位查询是 2~3 个词、不到 20 个字符，
	// 这个上限不会挡到任何人，却能挡住「把一次请求变成几十倍开销」的玩法。
	maxQueryRunes = 256
	// maxQueryTerms 空格分隔的词数上限。中文没有空格，这条主要针对机器拼出来的长串。
	maxQueryTerms = 32
	// maxURIBytes 整个请求行（含 query string）的上限，与主流浏览器地址栏的实际容量一致。
	maxURIBytes = 2048
	// maxPage 最多翻到第几页。再往后既没人看，每翻一页也都要跑一次完整排序。
	maxPage = 100
	// maxListArg fields/exclude/cat 这类逗号列表最多几项。
	maxListArg = 32
)

var errQueryEmpty = errors.New("请输入要搜索的关键词")

// normalizeQuery 清洗并校验查询词。返回的字符串就是交给检索的正文。
//
// 清洗与校验分开做：先剥掉不该进来的字符，再判断剩下的是否为空、是否超限——
// 顺序反过来的话，一串零宽字符能绕过长度检查，一串控制字符能出现在日志里把行拆坏。
func normalizeQuery(raw string) (string, error) {
	cleaned := strings.Join(strings.Fields(stripInvisible(raw)), " ")
	if cleaned == "" {
		return "", errQueryEmpty
	}
	if n := utf8.RuneCountInString(cleaned); n > maxQueryRunes {
		return "", fmt.Errorf("查询过长（上限 %d 个字符，当前 %d）", maxQueryRunes, n)
	}
	if n := len(strings.Fields(cleaned)); n > maxQueryTerms {
		return "", fmt.Errorf("关键词太多（上限 %d 个，当前 %d）", maxQueryTerms, n)
	}
	// 纯符号查询没有检索意义，却要跑完整条链路（分词、召回、排序）。
	// 注意 C++ / C# / .NET 这类要保留：判定条件是「含字母或数字」。
	if !hasSearchableRune(cleaned) {
		return "", errQueryEmpty
	}
	return cleaned, nil
}

// stripInvisible 去掉控制字符与不可见格式字符，空白统一成半角空格。
//
//	控制字符（C0/C1、DEL）：会把日志行拆坏，也能让同一个查询在缓存里出现多份
//	格式字符（Cf）：零宽空格、零宽连接符、双向覆盖（RLO/LRO）、BOM
//
// 双向覆盖那几个不删的话可以做「文字伪装」——把 q 显示成一串正常中文，
// 实际字节顺序完全不同，出现在结果页的查询回显里就是一次视觉欺骗。
func stripInvisible(s string) string {
	var b strings.Builder
	b.Grow(len(s))
	for _, r := range s {
		switch {
		case r == ' ' || r == '\t' || r == '\n' || r == '\r':
			b.WriteByte(' ')
		case r < 0x20, r == 0x7f:
			continue
		case r >= 0x80 && r <= 0x9f:
			continue
		case unicode.Is(unicode.Cf, r):
			continue
		default:
			b.WriteRune(r)
		}
	}
	return b.String()
}

// hasSearchableRune 是否含字母或数字（含汉字，汉字是 Letter）。
func hasSearchableRune(s string) bool {
	for _, r := range s {
		if unicode.IsLetter(r) || unicode.IsNumber(r) {
			return true
		}
	}
	return false
}

// checkURISize 请求行过长直接 414。
//
// 为什么单独做一次：Go 的 http.Server 只按 MaxHeaderBytes（默认 1MB）限制整个头部，
// 一条 800KB 的 URL 是能进来的——上限靠它等于没有。
func checkURISize(w http.ResponseWriter, r *http.Request) bool {
	if len(r.URL.RequestURI()) <= maxURIBytes {
		return true
	}
	writeErr(w, http.StatusRequestURITooLong, "请求地址过长")
	return false
}

// logSafe 把查询词变成可以安全写进日志的形式。
//
// 这里已经过 normalizeQuery，理论上不含换行；但这函数也可能被别的路径调用，
// 所以自己再挡一次：日志里出现换行 = 伪造出一条假日志。
func logSafe(q string) string {
	const keep = 64
	if utf8.RuneCountInString(q) > keep {
		q = string([]rune(q)[:keep]) + "…"
	}
	return strings.Map(func(r rune) rune {
		if r < 0x20 || r == 0x7f || unicode.Is(unicode.Cf, r) {
			return -1
		}
		return r
	}, q)
}

// listArg 解析逗号列表并限制项数：超了直接拒，不静默截断
// （静默截断会让「筛了 40 个分类」的人以为筛全了，其实只筛了前 32 个）。
func listArg(raw, name string) ([]string, error) {
	items := splitCSV(raw)
	if len(items) > maxListArg {
		return nil, fmt.Errorf("%s 参数项数过多（上限 %d）", name, maxListArg)
	}
	return items, nil
}

// noIndexPath 不该进搜索引擎索引的路径。
//
// /search 放第一位：搜索结果页是参数化的，每个 q 都能生成一页，
// 让它们进索引既是重复内容，也会把「别人搜什么」暴露给搜索引擎。
// 账号与后台页面同理。
func noIndexPath(p string) bool {
	for _, prefix := range []string{
		"/search", "/manage", "/admin", "/api/",
		"/login", "/register", "/reset", "/verify", "/settings",
	} {
		if strings.HasPrefix(p, prefix) {
			return true
		}
	}
	return false
}

// searchLimit /api/search 的独立限流。searchLimiter 为 nil（旧测试构造的 Server）时放行。
func (s *Server) searchLimit(next http.Handler) http.Handler {
	if s.searchLimiter == nil {
		return next
	}
	return s.searchLimiter.middleware(next)
}
