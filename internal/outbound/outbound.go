// Package outbound 统一决定抓取与预检请求从哪个出口出去。
//
// 以前各处的 http.Client 都用默认 Transport，等于继承了 HTTP_PROXY / HTTPS_PROXY。
// 桌面上的代理软件一断，所有站在同一分钟集体失败，错误是代理回的「Bad Gateway」，
// 记进异常名单就成了十个站「无法访问」——真实原因却是本机出口。
// （2026-10-31 实测：10 个站在 08:50:51–55 连续失败，detail 全是 502。）
//
// 所以出口收成一处：**默认直连**，只有显式给了 -outbound-proxy 才走代理；
// 回环与内网地址永远直连，本地调试站和 httptest 不该被代理拦死。
package outbound

import (
	"fmt"
	"net"
	"net/http"
	"net/url"
	"strings"
	"sync/atomic"
	"time"
)

var current atomic.Pointer[url.URL]

// SetProxy 设置出网代理。空串表示直连（并忽略环境变量里的代理）。
// 允许省略 scheme（"127.0.0.1:7890" 当作 http 代理）。
func SetProxy(raw string) error {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		current.Store(nil)
		return nil
	}
	if !strings.Contains(raw, "://") {
		raw = "http://" + raw
	}
	u, err := url.Parse(raw)
	if err != nil {
		return fmt.Errorf("代理地址不合法：%w", err)
	}
	if u.Host == "" {
		return fmt.Errorf("代理地址缺少主机名：%s", raw)
	}
	switch u.Scheme {
	case "http", "https", "socks5", "socks5h":
	default:
		return fmt.Errorf("不支持的代理协议：%s", u.Scheme)
	}
	current.Store(u)
	return nil
}

// Proxy 返回当前代理，nil 表示直连。
func Proxy() *url.URL { return current.Load() }

// IsLocalHost 判定回环 / 内网 / 链路本地地址。
func IsLocalHost(host string) bool {
	if host == "" {
		return false
	}
	if host == "localhost" || host == "::1" {
		return true
	}
	if ip := net.ParseIP(host); ip != nil {
		return ip.IsLoopback() || ip.IsPrivate() || ip.IsLinkLocalUnicast()
	}
	return strings.HasSuffix(host, ".local") || strings.HasSuffix(host, ".internal")
}

// ProxyFunc 供 http.Transport 使用：内网直连，其余按当前配置。
func ProxyFunc(req *http.Request) (*url.URL, error) {
	if req == nil || req.URL == nil {
		return nil, nil
	}
	if IsLocalHost(req.URL.Hostname()) {
		return nil, nil
	}
	return current.Load(), nil
}

// Transport 返回带本包代理策略的 Transport（其余参数沿用 DefaultTransport）。
func Transport() *http.Transport {
	t := http.DefaultTransport.(*http.Transport).Clone()
	t.Proxy = ProxyFunc
	return t
}

// Client 返回使用本包代理策略的客户端。
func Client(timeout time.Duration) *http.Client {
	return &http.Client{Timeout: timeout, Transport: Transport()}
}
