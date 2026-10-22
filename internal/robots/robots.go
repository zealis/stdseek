// Package robots 提供带缓存的 robots.txt 检查，供爬虫与站点预检共用。
package robots

import (
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/temoto/robotstxt"
)

// Cache 按 host 缓存 robots.txt 解析结果。
type Cache struct {
	ua      string
	timeout time.Duration
	mu      sync.Mutex
	data    map[string]*robotstxt.RobotsData
}

// NewCache 构造缓存；ua 同时用于 robots 的 User-Agent 匹配。
func NewCache(ua string, timeout time.Duration) *Cache {
	return &Cache{ua: ua, timeout: timeout, data: map[string]*robotstxt.RobotsData{}}
}

// Allowed 判断 URL 是否被 robots.txt 允许。无法获取 robots.txt 时放行（常见做法）。
func (c *Cache) Allowed(rawURL string) bool {
	host := Host(rawURL)
	if host == "" {
		return false
	}
	c.mu.Lock()
	data, ok := c.data[host]
	c.mu.Unlock()
	if !ok {
		data = Fetch(rawURL, c.ua, c.timeout)
		c.mu.Lock()
		c.data[host] = data
		c.mu.Unlock()
	}
	if data == nil {
		return true
	}
	return data.TestAgent(Path(rawURL), c.ua)
}

// Fetch 拉取并解析 robots.txt；失败返回 nil。
func Fetch(anyURL, ua string, timeout time.Duration) *robotstxt.RobotsData {
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

// Host 返回 URL 的主机名（小写、去端口）；解析失败返回空串。
func Host(raw string) string {
	u, err := url.Parse(raw)
	if err != nil {
		return ""
	}
	return strings.ToLower(u.Hostname())
}

// Path 返回请求路径（含 query），robots 判定需要 path。
func Path(raw string) string {
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
