package crawler

import (
	"context"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"
)

// fakeRT 按脚本回放响应，统计请求次数。
type fakeRT struct {
	resps []fakeResp
	calls int
}

type fakeResp struct {
	status int
	body   string
	err    error
}

func (f *fakeRT) RoundTrip(*http.Request) (*http.Response, error) {
	i := f.calls
	if i >= len(f.resps) {
		i = len(f.resps) - 1 // 测试写错时停在最后一个，别越界
	}
	f.calls++
	r := f.resps[i]
	if r.err != nil {
		return nil, r.err
	}
	return &http.Response{
		StatusCode: r.status,
		Header:     http.Header{},
		Body:       io.NopCloser(strings.NewReader(r.body)),
	}, nil
}

// 503 之后再试一次拿到 200：这正是重试层存在的意义。
func TestRetryTransportRetriesTransient(t *testing.T) {
	base := &fakeRT{resps: []fakeResp{{status: 503, body: "busy"}, {status: 200, body: "ok"}}}
	req, _ := http.NewRequest(http.MethodGet, "https://example.com/", nil)
	resp, err := (&retryTransport{base: base}).RoundTrip(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("503 后重试应拿到 200，实际 %d", resp.StatusCode)
	}
	if base.calls != 2 {
		t.Fatalf("应该恰好请求 2 次，实际 %d", base.calls)
	}
}

// 网络错误（连接拒绝、重置）同样值得再试一次。
func TestRetryTransportRetriesNetworkError(t *testing.T) {
	base := &fakeRT{resps: []fakeResp{
		{err: errors.New("dial tcp: connection refused")},
		{status: 200, body: "ok"},
	}}
	req, _ := http.NewRequest(http.MethodGet, "https://example.com/", nil)
	resp, err := (&retryTransport{base: base}).RoundTrip(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK || base.calls != 2 {
		t.Fatalf("网络错误后应重试成功，实际 status=%d calls=%d", resp.StatusCode, base.calls)
	}
}

// 403/404/429 是决定性回答，重试只会给站点加压。
func TestRetryTransportLeavesDecisiveAlone(t *testing.T) {
	for _, code := range []int{403, 404, 429} {
		base := &fakeRT{resps: []fakeResp{{status: code, body: "x"}, {status: 200, body: "y"}}}
		req, _ := http.NewRequest(http.MethodGet, "https://example.com/", nil)
		resp, _ := (&retryTransport{base: base}).RoundTrip(req)
		if resp.StatusCode != code || base.calls != 1 {
			t.Fatalf("HTTP %d 不该重试，实际 calls=%d", code, base.calls)
		}
	}
}

// POST 是接口调用，不重试。
func TestRetryTransportSkipsPOST(t *testing.T) {
	base := &fakeRT{resps: []fakeResp{{err: errors.New("boom")}, {status: 200, body: "ok"}}}
	req, _ := http.NewRequest(http.MethodPost, "https://example.com/api", nil)
	if _, err := (&retryTransport{base: base}).RoundTrip(req); err == nil {
		t.Fatal("POST 失败应原样返回")
	}
	if base.calls != 1 {
		t.Fatalf("POST 不该重试，实际 calls=%d", base.calls)
	}
}

// 上下文已取消（超时预算花完、进程在关）时再试必然立刻失败，直接放行。
func TestRetryTransportGivesUpWhenContextDone(t *testing.T) {
	base := &fakeRT{resps: []fakeResp{{err: errors.New("boom")}, {status: 200, body: "ok"}}}
	req, _ := http.NewRequest(http.MethodGet, "https://example.com/", nil)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	req = req.WithContext(ctx)
	if _, err := (&retryTransport{base: base}).RoundTrip(req); err == nil {
		t.Fatal("上下文取消时应返回原错误")
	}
	if base.calls != 1 {
		t.Fatalf("上下文取消后不该重试，实际 calls=%d", base.calls)
	}
}
