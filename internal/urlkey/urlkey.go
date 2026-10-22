// Package urlkey 统一 URL 归一化规则（繁荣度 key、域名提取共用），避免各包重复实现。
package urlkey

import (
	"net/url"
	"strings"
)

// Key 归一化 URL 为 key：小写、去 scheme/query/fragment、去末尾斜杠。
// 例：https://Go.dev/doc/?x=1 → go.dev/doc
func Key(raw string) string {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || u.Host == "" {
		return ""
	}
	host := strings.ToLower(u.Hostname())
	path := strings.TrimRight(u.EscapedPath(), "/")
	if path == "" || path == "/" {
		return host
	}
	return host + path
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
