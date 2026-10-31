package outbound

import (
	"net/http"
	"testing"
	"time"
)

// 默认必须是直连：不读 HTTP_PROXY/HTTPS_PROXY。
// 这条是 2026-10-31 那批「十个站同时 Bad Gateway」的回归测试。
func TestDefaultIsDirect(t *testing.T) {
	t.Cleanup(func() { _ = SetProxy("") })
	if err := SetProxy(""); err != nil {
		t.Fatalf("清空代理失败：%v", err)
	}
	if p := Proxy(); p != nil {
		t.Fatalf("默认应为直连，got %v", p)
	}
	req, _ := http.NewRequest(http.MethodGet, "https://example.com/", nil)
	if got, err := ProxyFunc(req); err != nil || got != nil {
		t.Fatalf("公网请求默认应直连，got %v err=%v", got, err)
	}
}

func TestSetProxyApplied(t *testing.T) {
	t.Cleanup(func() { _ = SetProxy("") })
	if err := SetProxy("127.0.0.1:7890"); err != nil { // 省略 scheme
		t.Fatalf("设置代理失败：%v", err)
	}
	if p := Proxy(); p == nil || p.String() != "http://127.0.0.1:7890" {
		t.Fatalf("代理未生效：%v", p)
	}
	req, _ := http.NewRequest(http.MethodGet, "https://example.com/", nil)
	got, err := ProxyFunc(req)
	if err != nil || got == nil || got.String() != "http://127.0.0.1:7890" {
		t.Fatalf("公网请求应走代理，got %v err=%v", got, err)
	}
}

// 内网地址永远直连，即使配了代理：本地调试站与 httptest 不该被代理拦死。
func TestLocalAlwaysDirect(t *testing.T) {
	t.Cleanup(func() { _ = SetProxy("") })
	if err := SetProxy("http://127.0.0.1:7890"); err != nil {
		t.Fatalf("设置代理失败：%v", err)
	}
	for _, raw := range []string{
		"http://localhost:8080/",
		"http://127.0.0.1:8081/",
		"http://127.0.0.5/",
		"http://10.0.0.7/",
		"http://192.168.1.10/",
		"http://172.20.3.4/",
		"http://[::1]:9000/",
		"http://nas.local/",
	} {
		req, err := http.NewRequest(http.MethodGet, raw, nil)
		if err != nil {
			t.Fatalf("构造请求失败 %s：%v", raw, err)
		}
		if got, _ := ProxyFunc(req); got != nil {
			t.Fatalf("%s 应直连，got %v", raw, got)
		}
	}
}

func TestSetProxyRejectsBadInput(t *testing.T) {
	t.Cleanup(func() { _ = SetProxy("") })
	if err := SetProxy("http://127.0.0.1:7890"); err != nil {
		t.Fatalf("设置代理失败：%v", err)
	}
	for _, raw := range []string{"ftp://127.0.0.1:21", "http://", ":::"} {
		if err := SetProxy(raw); err == nil {
			t.Fatalf("%q 应被拒绝", raw)
		}
	}
	// 拒绝后不应污染已有配置
	if p := Proxy(); p == nil || p.String() != "http://127.0.0.1:7890" {
		t.Fatalf("非法输入后代理被改坏：%v", p)
	}
}

func TestClientUsesPolicy(t *testing.T) {
	t.Cleanup(func() { _ = SetProxy("") })
	if err := SetProxy(""); err != nil {
		t.Fatalf("清空代理失败：%v", err)
	}
	c := Client(3 * time.Second)
	if c.Timeout != 3*time.Second {
		t.Fatalf("超时未设置：%v", c.Timeout)
	}
	tr, ok := c.Transport.(*http.Transport)
	if !ok {
		t.Fatalf("Transport 类型不对：%T", c.Transport)
	}
	if tr.Proxy == nil {
		t.Fatal("Transport 未挂代理策略")
	}
	// 用真实请求验证：公网地址在默认配置下不走代理
	req, _ := http.NewRequest(http.MethodGet, "https://example.com/", nil)
	if got, _ := tr.Proxy(req); got != nil {
		t.Fatalf("默认应直连，got %v", got)
	}
}
