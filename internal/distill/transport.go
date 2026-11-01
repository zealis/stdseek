package distill

import (
	"bytes"
	"io"
	"log/slog"
	"net/http"
)

// Transport 在原始 Transport 外面包一层：直连被拦或连不上时，改走蒸馏接口，
// 把结果合成一份最小 HTML 原样返回给调用方（colly 或直接 http.Client）。
//
// 为什么选 Transport 这一层，而不是在抓取流程里加分支：
// 抓取管线上有去样板、去重、分词、指纹、快照、出链、统计六七个环节，
// 在任何一个环节插「蒸馏版本」都要复制一遍后续逻辑；包在传输层，
// 蒸馏页面和正常页面走的是同一条路，行为与统计口径都一致。
type Transport struct {
	base http.RoundTripper
	cli  *Client
}

// Wrap 包装 base。cli 为 nil 时原样返回 base——开关关掉时零开销、零分支。
func Wrap(base http.RoundTripper, cli *Client) http.RoundTripper {
	if cli == nil {
		return base
	}
	return &Transport{base: base, cli: cli}
}

func (t *Transport) RoundTrip(req *http.Request) (*http.Response, error) {
	resp, err := t.base.RoundTrip(req)

	// 只蒸馏 GET 的页面请求：POST 是接口调用，换成本地合成的内容没有意义。
	if req.Method != http.MethodGet {
		return resp, err
	}
	status := 0
	if resp != nil {
		status = resp.StatusCode
	}
	if !shouldDistill(status, err) {
		return resp, err
	}

	page, derr := t.cli.Fetch(req.Context(), req.URL.String())
	if derr != nil || page.Text == "" {
		// 蒸馏也失败：把原始错误/响应原样交回去，让上层按「抓不到」处理。
		if derr != nil {
			slog.Debug("distill failed", "url", req.URL.String(), "status", status, "err", derr)
		}
		return resp, err
	}

	if resp != nil && resp.Body != nil {
		_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 4<<10))
		_ = resp.Body.Close()
	}
	body := synHTML(page)
	slog.Info("distilled page", "url", req.URL.String(), "status", status, "bytes", len(body))
	return &http.Response{
		Status:        "200 OK",
		StatusCode:    http.StatusOK,
		Proto:         "HTTP/1.1",
		ProtoMajor:    1,
		ProtoMinor:    1,
		Header:        http.Header{"Content-Type": []string{"text/html; charset=utf-8"}},
		Body:          io.NopCloser(bytes.NewReader(body)),
		ContentLength: int64(len(body)),
		Request:       req,
	}, nil
}
