// Package urlkey 统一 URL 归一化规则（索引文档 ID、KV 快照、繁荣度 key 共用），
// 避免各包重复实现导致同一页面被拆成多条。
package urlkey

import (
	"net"
	"net/url"
	"strings"
)

// Key 归一化 URL 为 key：小写、去 scheme/query/fragment、去末尾斜杠。
//
//	https://Go.dev/doc/?x=1  → go.dev/doc
//	https://go.dev:443/      → go.dev        （默认端口归一）
//	http://127.0.0.1:8001/   → 127.0.0.1:8001（非默认端口保留）
//
// 早期版本用 Hostname() 丢掉了端口，导致同主机不同端口的两个服务
// 在索引里互相覆盖（并发测试里两个 httptest 站点被合并成一条）。
func Key(raw string) string {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || u.Host == "" {
		return ""
	}
	host := normalizeHost(u)
	path := strings.TrimRight(u.EscapedPath(), "/")
	if path == "" || path == "/" {
		return host
	}
	return host + path
}

// Canonical 返回用于入库/展示的规范 URL：去 fragment、主机小写、去默认端口，
// 路径与查询串保持原样。
//
// 为什么必须去 fragment：`#commentform`、`#http_response_addtrailers_headers` 这类
// 锚点是页内定位，不是不同内容。但 Key 会把它们抹平（同一 key），于是同一个页面
// 既能以带锚点的形式入库、又和干净 URL 抢同一个索引 ID——真实数据里博客园
// 12 篇文章全部以 `/p/xxx#commentform` 的形式出现，搜索结果链接带着莫名的锚点。
//
// 为什么保留查询串：`?p=123`、`?id=456` 这种站点用查询串标识文章，抹掉会指向错误的页面。
// 代价是「仅查询串不同」的两个 URL 仍会各占一行 documents，但它们本来就共用一个
// 索引 ID（Key 去 query），属已知的既有行为，不在归一化里假装解决。
func Canonical(raw string) string {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || u.Host == "" {
		return strings.TrimSpace(raw)
	}
	u.Fragment = ""
	u.RawFragment = ""
	host := normalizeHost(u)
	u.Host = host
	if u.Path == "" {
		u.Path = "/"
	}
	if q := stripTracking(u.RawQuery); q != u.RawQuery {
		u.RawQuery = q
		u.ForceQuery = false
	}
	return u.String()
}

// trackingParams 只清公认的投放追踪参数，其余查询串一律保留——
// `?p=123`、`?id=456` 这类是文章身份，删了就指向错误页面。
// 白名单式删除比「只保留白名单参数」安全：宁可漏清，不可清掉内容标识。
var trackingParams = map[string]bool{
	"gclid": true, "fbclid": true, "msclkid": true, "spm": true, "ref_src": true,
}

// stripTracking 去掉 utm_* 与上表中的追踪参数，保留其余（含顺序）。
func stripTracking(rawQuery string) string {
	if rawQuery == "" {
		return ""
	}
	parts := strings.Split(rawQuery, "&")
	kept := parts[:0]
	for _, p := range parts {
		key := p
		if i := strings.IndexByte(p, '='); i >= 0 {
			key = p[:i]
		}
		lower := strings.ToLower(key)
		if strings.HasPrefix(lower, "utm_") || trackingParams[lower] {
			continue
		}
		kept = append(kept, p)
	}
	return strings.Join(kept, "&")
}

// normalizeHost 小写化主机名，并去掉 http:80 / https:443 这类默认端口。
func normalizeHost(u *url.URL) string {
	host := strings.ToLower(u.Host)
	h, port, err := net.SplitHostPort(host)
	if err != nil {
		return host
	}
	switch {
	case port == "":
		return h
	case port == "80" && u.Scheme == "http":
		return h
	case port == "443" && u.Scheme == "https":
		return h
	}
	return net.JoinHostPort(h, port)
}

// Keys 返回 URL 的全部路径前缀（host → host/a → host/a/b），与 sese-engine 的 URL 树一致。
func Keys(raw string) []string {
	k := Key(raw)
	if k == "" {
		return nil
	}
	parts := strings.Split(k, "/")
	out := make([]string, 0, len(parts))
	cur := parts[0]
	out = append(out, cur)
	for _, p := range parts[1:] {
		if p == "" {
			continue
		}
		cur = cur + "/" + p
		out = append(out, cur)
	}
	return out
}
