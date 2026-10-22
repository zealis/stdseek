// Package precheck 对候选站点做提交前自动预检：
// 可达性、HTTP 状态、内容类型、robots.txt 是否允许。黑名单历史由 store 查询后合并。
package precheck

import (
	"context"
	"io"
	"net/http"
	"strings"
	"time"

	"voidxo/internal/robots"
)

// Result 预检结果（全部字段可直接展示给管理员）。
type Result struct {
	URL           string `json:"url"`
	FinalURL      string `json:"final_url"`
	Reachable     bool   `json:"reachable"`
	StatusCode    int    `json:"status_code"`
	ContentType   string `json:"content_type"`
	IsHTML        bool   `json:"is_html"`
	RobotsAllowed bool   `json:"robots_allowed"`
	RobotsFound   bool   `json:"robots_found"`
	// BlacklistHistory 该域名是否出现在黑名单（历史上被拒过）。
	BlacklistHistory bool  `json:"blacklist_history"`
	CheckedAt        int64 `json:"checked_at"`
	Error            string `json:"error,omitempty"`
}

// Check 对 url 执行预检。不会抛错：所有失败都体现在 Result 里。
func Check(ctx context.Context, rawURL, ua string, timeout time.Duration) Result {
	res := Result{URL: rawURL, RobotsAllowed: true}

	rc := robots.NewCache(ua, timeout)
	data := robots.Fetch(rawURL, ua, timeout)
	res.RobotsFound = data != nil
	res.RobotsAllowed = rc.Allowed(rawURL)

	client := &http.Client{Timeout: timeout}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		res.Error = "URL 不合法"
		return res
	}
	req.Header.Set("User-Agent", ua)
	resp, err := client.Do(req)
	if err != nil {
		res.Error = "无法连接：" + err.Error()
		return res
	}
	defer resp.Body.Close()

	res.FinalURL = resp.Request.URL.String()
	res.StatusCode = resp.StatusCode
	res.ContentType = resp.Header.Get("Content-Type")
	res.Reachable = resp.StatusCode >= 200 && resp.StatusCode < 400
	ct := strings.ToLower(res.ContentType)
	res.IsHTML = strings.Contains(ct, "text/html") || strings.Contains(ct, "application/xhtml")

	// 读取少量正文，触发连接完成（部分站点只有在读 body 时才暴露错误）
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 32*1024))
	return res
}
