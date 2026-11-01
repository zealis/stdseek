// 瞬时失败重试，抄自 Scrapy 的 RetryMiddleware（Heritrix、Crawlee 同样标配）。
//
// 此前一次 502 或一次网络抖动就直接判死：这一页烧掉单站配额，落在种子页上
// 更是把整站挂进「无法访问」名单——管理员收到的是噪声，站点只是打了个喷嚏。
// 所有生产级爬虫都对「值得再试的失败」多试一次，我们也一样。
package crawler

import (
	"context"
	"io"
	"math/rand/v2"
	"net/http"
	"time"
)

// retryStatus 这类响应值得再试一次：请求超时（408）与网关/服务端临时异常
// （500/502/503/504）。429 不在内——那是「太快了」的信号，重试只会更糟，
// 放慢节奏是限速层（throttle）的事；403/404 是决定性回答，不是抖动。
func retryStatus(code int) bool {
	switch code {
	case http.StatusRequestTimeout,
		http.StatusInternalServerError,
		http.StatusBadGateway,
		http.StatusServiceUnavailable,
		http.StatusGatewayTimeout:
		return true
	}
	return false
}

// retryTransport 包在真实传输外面：瞬时失败退避后重试一次。
//
// 只重试一次（Scrapy 默认两次，但我们的单站配额只有几十页，一次足够滤掉
// 抖动，再多就是给故障站点加倍施压）。退避 400~800ms 随机——同批站点
// 整齐地撞同一时刻只会制造下一波抖动。
type retryTransport struct {
	base http.RoundTripper
}

func (t *retryTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	resp, err := t.base.RoundTrip(req)
	// 只重试幂等的 GET。上下文已取消（客户端超时预算花完）时再试必然立刻失败，
	// 也一并放行——超时预算由外层统一管，这里不去动它。
	if req.Method != http.MethodGet || req.Context().Err() != nil {
		return resp, err
	}
	if err == nil && (resp == nil || !retryStatus(resp.StatusCode)) {
		return resp, err
	}
	// 失败的响应体读完再关，连接才能复用（这层在抓取热路径上）
	if resp != nil && resp.Body != nil {
		_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 4<<10))
		_ = resp.Body.Close()
	}
	if werr := sleepCtx(req.Context(), retryBackoff()); werr != nil {
		return resp, err
	}
	return t.base.RoundTrip(req)
}

// retryBackoff 重试前的退避时长。
func retryBackoff() time.Duration {
	return time.Duration(400+rand.IntN(400)) * time.Millisecond
}

// sleepCtx 睡 d；ctx 取消时提前返回。
func sleepCtx(ctx context.Context, d time.Duration) error {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
		return nil
	}
}
