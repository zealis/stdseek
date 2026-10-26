// Package precheck 对候选站点做提交前自动预检：
// 可达性、HTTP 状态、内容类型、robots.txt 是否允许。黑名单历史由 store 查询后合并。
package precheck

import (
	"context"
	"crypto/x509"
	"errors"
	"fmt"
	"io"
	"net"
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
	BlacklistHistory bool   `json:"blacklist_history"`
	CheckedAt        int64  `json:"checked_at"`
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
		res.Error = "无法连接：" + explainNetError(err, timeout)
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

// explainNetError 把 Go 的网络错误翻译成能照着做的中文说明。
//
// 为什么需要：直接把底层错误展示给用户时，他看到的是
// 「context deadline exceeded (Client.Timeout exceeded while awaiting headers)」——
// 这既说不清是什么原因，也不知道下一步该干什么。真实案例：用户提交 v2ex.com
// 时遇到这个报错，实际原因是该站在当前网络下不可达（TCP 都建不起来），
// 但报错里没有任何线索能指向这个方向。
//
// 分类依据是 net 包的类型断言，不是字符串匹配——后者在不同 Go 版本下会漂移。
func explainNetError(err error, timeout time.Duration) string {
	if err == nil {
		return ""
	}
	secs := timeout.Seconds()

	// DNS：域名根本不存在，或本地解析不了
	var dnsErr *net.DNSError
	if errors.As(err, &dnsErr) {
		if dnsErr.IsNotFound {
			return "域名无法解析（DNS 查不到这个域名），请检查是否拼错"
		}
		return "域名解析失败：" + dnsErr.Err
	}

	// TLS/证书问题：站点可达但握手失败。
	//
	// 这里不能用「带 Timeout() 方法的接口」做类型断言——net.Error 也满足那个形状，
	// 于是所有超时都会被误判成 TLS 握手失败（比原文更误导）。第一版就是这么写的，
	// 靠 TestExplainNetError 才暴露出来。改用具体的错误类型 + 特征字符串。
	var certErr x509.UnknownAuthorityError
	if errors.As(err, &certErr) {
		return "TLS 证书校验失败（证书链不受信任）"
	}
	var hostErr x509.HostnameError
	if errors.As(err, &hostErr) {
		return "TLS 证书与域名不匹配"
	}
	if strings.Contains(err.Error(), "tls:") {
		return "TLS 握手失败（站点不支持当前协议或加密套件）"
	}

	// 超时：最常见也最容易被误读的一类
	var ne net.Error
	if errors.As(err, &ne) && ne.Timeout() {
		return fmt.Sprintf("连接超时（%.0f 秒内没有响应）：可能是该站在当前网络不可达、"+
			"被防火墙拦截，或站点屏蔽了爬虫；可先用浏览器确认能否打开，再检查是否要走代理", secs)
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return fmt.Sprintf("连接超时（%.0f 秒内没有响应）：网络不可达或被拦截", secs)
	}

	// 连接被明确拒绝/重置
	var opErr *net.OpError
	if errors.As(err, &opErr) && opErr.Op == "dial" {
		return "连接被拒绝（端口不通或站点已下线）"
	}

	return err.Error()
}
