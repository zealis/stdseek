package distill

import (
	"bytes"
	"io"
	"log/slog"
	"net/http"
	"strings"
)

// Transport 在原始 Transport 外面包一层：直连被拦或连不上时，改走阅读器，
// 把取回的整页 HTML 当成 200 响应交给调用方（colly 或直接 http.Client）。
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
		// 200 的 HTML 还要过一道「空壳检测」：SPA 站点直连拿到的只是挂载点，
		// 正文在 JavaScript 里——这正是阅读器（真浏览器）才取得回的东西。
		if status == http.StatusOK && resp != nil && resp.Body != nil {
			return t.maybeShell(req, resp)
		}
		return resp, err
	}

	page, derr := t.cli.Fetch(req.Context(), req.URL.String())
	if derr != nil || len(page.HTML) == 0 {
		// 蒸馏也失败：把原始错误/响应原样交回去，让上层按「抓不到」处理。
		// 同时按站点记一笔——后台据此标出「连阅读器都过不去」的站。
		t.cli.recordFail(req.URL.Hostname(), derr)
		if derr != nil {
			slog.Debug("distill failed", "url", req.URL.String(), "status", status, "err", derr)
		}
		return resp, err
	}

	// 原始响应体要读完再关，否则连接无法复用（这层在抓取热路径上）。
	if resp != nil && resp.Body != nil {
		_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 4<<10))
		_ = resp.Body.Close()
	}
	t.cli.recordOK(req.URL.Hostname())
	slog.Info("distilled page", "url", req.URL.String(), "status", status, "bytes", len(page.HTML), "title", page.Title)
	return t.ok(req, page), nil
}

// maybeShell 判这一页是不是前端框架的空壳；是就交给阅读器渲染，不是原样放行。
//
// 响应体在这里读进内存再包回去：管线本来就整页读，只是换了位置，
// 没有二次网络读取。误判的代价是有限的一次阅读器请求（有每轮预算与
// MinGap 间隔管着），渲染出来还是薄页的话照样被薄页规则拦住，
// 不会污染索引；漏判的代价是整站空壳、一篇收不进。
func (t *Transport) maybeShell(req *http.Request, resp *http.Response) (*http.Response, error) {
	data, rerr := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	_ = resp.Body.Close()
	if rerr != nil {
		return nil, rerr
	}
	if !htmlContent(resp.Header.Get("Content-Type"), data) ||
		!shellish(data) || !t.cli.takeShell() {
		return buffered(resp, data), nil
	}
	page, derr := t.cli.Fetch(req.Context(), req.URL.String())
	if derr != nil || len(page.HTML) == 0 {
		// 渲染也拿不到东西：按原样放行，让管线按薄页规则拦它。
		t.cli.recordFail(req.URL.Hostname(), derr)
		return buffered(resp, data), nil
	}
	t.cli.recordOK(req.URL.Hostname())
	slog.Info("distilled shell", "url", req.URL.String(), "bytes", len(page.HTML), "title", page.Title)
	return t.ok(req, page), nil
}

// ok 把阅读器取回的整页 HTML 合成 200 响应交给管线。
func (t *Transport) ok(req *http.Request, page Page) *http.Response {
	return &http.Response{
		Status:        "200 OK",
		StatusCode:    http.StatusOK,
		Proto:         "HTTP/1.1",
		ProtoMajor:    1,
		ProtoMinor:    1,
		Header:        http.Header{"Content-Type": []string{"text/html; charset=utf-8"}},
		Body:          io.NopCloser(bytes.NewReader(page.HTML)),
		ContentLength: int64(len(page.HTML)),
		Request:       req,
	}
}

// buffered 把已读进内存的响应体包回响应：body 只能读一次，读过的必须还回去。
func buffered(resp *http.Response, data []byte) *http.Response {
	resp.Body = io.NopCloser(bytes.NewReader(data))
	resp.ContentLength = int64(len(data))
	return resp
}

// htmlContent 判响应是不是 HTML 页面。Content-Type 没写时按内容嗅探：
// HTML 以「<」开头（doctype 或标签），JSON 与二进制都不是。
func htmlContent(ct string, data []byte) bool {
	if strings.Contains(ct, "text/html") || strings.Contains(ct, "xhtml") {
		return true
	}
	if ct != "" {
		return false
	}
	trimmed := bytes.TrimLeft(data[:min(len(data), 64)], " \t\r\n")
	return bytes.HasPrefix(trimmed, []byte("<"))
}
