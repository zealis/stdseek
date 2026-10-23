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
