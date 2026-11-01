package precheck

import (
	"context"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"
)

func TestCheckReachableHTML(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/robots.txt", func(w http.ResponseWriter, _ *http.Request) {
		w.Write([]byte("User-agent: *\nDisallow: /secret\n"))
	})
	mux.HandleFunc("/", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Write([]byte("<html><body>ok</body></html>"))
	})
	mux.HandleFunc("/secret", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		w.Write([]byte("<html>secret</html>"))
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	res := Check(context.Background(), srv.URL+"/", "stdseek-test", 3*time.Second)
	if !res.Reachable || res.StatusCode != 200 {
		t.Fatalf("result=%+v", res)
	}
	if !res.IsHTML {
		t.Fatalf("content type not html: %q", res.ContentType)
	}
	if !res.RobotsFound || !res.RobotsAllowed {
		t.Fatalf("robots should be found and allowed: %+v", res)
	}

	blocked := Check(context.Background(), srv.URL+"/secret", "stdseek-test", 3*time.Second)
	if blocked.RobotsAllowed {
		t.Fatalf("disallowed path must be flagged: %+v", blocked)
	}
}

func TestCheckUnreachableAnd404(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.NotFound(w, nil)
	}))
	defer srv.Close()

	res := Check(context.Background(), srv.URL+"/missing", "stdseek-test", 3*time.Second)
	if res.Reachable {
		t.Fatalf("404 should not be reachable: %+v", res)
	}
	if res.StatusCode != 404 {
		t.Fatalf("status=%d", res.StatusCode)
	}

	// 关闭后的端口 → 连接失败
	dead := httptest.NewServer(http.NotFoundHandler())
	url := dead.URL
	dead.Close()
	res = Check(context.Background(), url+"/", "stdseek-test", time.Second)
	if res.Reachable || res.Error == "" {
		t.Fatalf("closed server should carry error: %+v", res)
	}
}

// TestExplainNetError 网络错误必须翻译成能照着做的中文说明。
//
// 真实起因：用户提交 v2ex.com 时看到
// 「无法连接：context deadline exceeded (Client.Timeout exceeded while awaiting headers)」，
// 既不知道原因也不知道怎么办。实际是该站在当时网络下不可达，
// 但原错误信息没有任何线索指向这个方向。
func TestExplainNetError(t *testing.T) {
	timeout := 15 * time.Second

	// 超时：http.Client 超时经 *url.Error 包装
	_, dialErr := net.DialTimeout("tcp", "192.0.2.1:81", 50*time.Millisecond)
	if dialErr == nil {
		t.Skip("本机网络行为异常，跳过超时用例")
	}
	got := explainNetError(&url.Error{Op: "Get", URL: "https://x/", Err: dialErr}, timeout)
	if !strings.Contains(got, "连接超时") || !strings.Contains(got, "不可达") {
		t.Errorf("超时应说明「不可达/被拦截」并给出排查方向，实际：%s", got)
	}
	if strings.Contains(got, "context deadline") {
		t.Errorf("不该把底层错误原文直接透出，实际：%s", got)
	}

	// DNS 查不到
	dns := &net.DNSError{IsNotFound: true, Err: "no such host", Name: "nope.example"}
	got = explainNetError(&url.Error{Op: "Get", URL: "https://nope.example/", Err: dns}, timeout)
	if !strings.Contains(got, "域名无法解析") {
		t.Errorf("DNS 失败应说明域名无法解析，实际：%s", got)
	}

	// 连接被拒绝
	got = explainNetError(&net.OpError{Op: "dial", Net: "tcp", Err: errors.New("connect: connection refused")}, timeout)
	if !strings.Contains(got, "连接被拒绝") {
		t.Errorf("明确的连接拒绝不该被当成超时，实际：%s", got)
	}

	// 未知错误：保留原文，不能吞掉信息
	got = explainNetError(errors.New("some weird failure"), timeout)
	if got != "some weird failure" {
		t.Errorf("无法分类时应原样返回，实际：%s", got)
	}

	if explainNetError(nil, timeout) != "" {
		t.Error("nil 错误应返回空串")
	}
}
