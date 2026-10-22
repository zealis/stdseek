package crawler

import (
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/temoto/robotstxt"
)

// parseHost 返回 URL 的主机名（小写、去端口）；解析失败返回空串。
func parseHost(raw string) string {
	u, err := url.Parse(raw)
	if err != nil || u.Hostname() == "" {
		return ""
	}
	return strings.ToLower(u.Hostname())
}

// pathOf 返回请求路径（含 query），robotstxt 判定需要 path。
func pathOf(raw string) string {
	u, err := url.Parse(raw)
	if err != nil {
		return "/"
	}
	p := u.Path
	if p == "" {
		p = "/"
	}
	if u.RawQuery != "" {
		p += "?" + u.RawQuery
	}
	return p
}

// fetchRobots 拉取并解析 robots.txt；失败返回 nil（调用方放行）。
func fetchRobots(anyURL, ua string, timeout time.Duration) *robotstxt.RobotsData {
	u, err := url.Parse(anyURL)
	if err != nil {
		return nil
	}
	robotsURL := u.Scheme + "://" + u.Host + "/robots.txt"
	client := &http.Client{Timeout: timeout}
	req, err := http.NewRequest(http.MethodGet, robotsURL, nil)
	if err != nil {
		return nil
	}
	req.Header.Set("User-Agent", ua)
	resp, err := client.Do(req)
	if err != nil {
		return nil
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil // 无 robots.txt（404 等）视为全站允许
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, 512*1024))
	if err != nil {
		return nil
	}
	data, err := robotstxt.FromBytes(body)
	if err != nil {
		return nil
	}
	return data
}

func isUTF8(b []byte) bool { return utf8.Valid(b) }
