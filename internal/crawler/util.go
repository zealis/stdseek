package crawler

import (
	"net/url"
	"strings"
	"unicode/utf8"
)

// parseHost 返回 URL 的主机名（小写、去端口）；解析失败返回空串。
func parseHost(raw string) string {
	u, err := url.Parse(raw)
	if err != nil || u.Hostname() == "" {
		return ""
	}
	return strings.ToLower(u.Hostname())
}

func isUTF8(b []byte) bool { return utf8.Valid(b) }
