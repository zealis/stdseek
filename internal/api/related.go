// Bing 搜索联想代理。
//
// 用途：结果页右栏「深入了解 X」下面的相关搜索词。默认 5 条，
// 前端不够就隐藏；这里最多给 6 条（留一条给前端做「不够就少显示」的判断余量）。
//
// 为什么走服务端：一是统一开关控制（关闭后完全不请求 Bing），二是避免浏览器直连
// 第三方带出去路径、Referer 等信息。缓存 1 小时——联想词时效性不强，重复请求没必要。
package api

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"
	"unicode/utf8"
)

const (
	relatedSource = "https://api.bing.com/osjson.aspx?market=zh-CN&query="
	relatedTTL    = time.Hour
	relatedMax    = 6
)

type relatedEntry struct {
	items []string
	at    time.Time
}

// relatedClient 直连（不读 HTTP_PROXY），理由见 fetchRelated。
var relatedClient = &http.Client{
	Timeout:   4 * time.Second,
	Transport: &http.Transport{Proxy: nil},
}

var relatedCache = struct {
	sync.Mutex
	m map[string]relatedEntry
}{m: map[string]relatedEntry{}}

// handleRelated GET /api/related?q=词
//
// 返回 {"items":["...","..."]}；没有联想词时返回空数组（前端据此隐藏整块）。
func (s *Server) handleRelated(w http.ResponseWriter, r *http.Request) {
	if !s.FeatureEnabled(r.Context(), "related_links") {
		writeErr(w, http.StatusNotFound, "未开启相关搜索")
		return
	}
	q := strings.TrimSpace(r.URL.Query().Get("q"))
	if q == "" || utf8.RuneCountInString(q) > 60 {
		writeJSON(w, http.StatusOK, map[string]any{"items": []string{}})
		return
	}

	if items, ok := relatedCached(q); ok {
		writeJSON(w, http.StatusOK, map[string]any{"items": items})
		return
	}

	items, err := fetchRelated(r.Context(), q)
	if err != nil {
		// 上游挂了不是错误——前端拿空数组就不显示
		writeJSON(w, http.StatusOK, map[string]any{"items": []string{}})
		return
	}
	relatedStore(q, items)
	writeJSON(w, http.StatusOK, map[string]any{"items": items})
}

func relatedCached(q string) ([]string, bool) {
	relatedCache.Lock()
	defer relatedCache.Unlock()
	e, ok := relatedCache.m[q]
	if !ok || time.Since(e.at) > relatedTTL {
		return nil, false
	}
	return e.items, true
}

func relatedStore(q string, items []string) {
	relatedCache.Lock()
	defer relatedCache.Unlock()
	// 简单防涨：超过 500 条就整个清空（联想词缓存没有保留价值）
	if len(relatedCache.m) > 500 {
		relatedCache.m = map[string]relatedEntry{}
	}
	relatedCache.m[q] = relatedEntry{items: items, at: time.Now()}
}

// fetchRelated 解析 Bing 的 ["query", ["a","b",...]] 格式。
func fetchRelated(ctx context.Context, q string) ([]string, error) {
	ctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, relatedSource+url.QueryEscape(q), nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", "Mozilla/5.0 (compatible; voidxo/1.0)")
	// 显式直连：api.bing.com 在国内可直连，走进程继承的系统代理反而会因代理失效而失败。
	resp, err := relatedClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, errUpstream{status: resp.StatusCode}
	}
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 32<<10))
	if err != nil {
		return nil, err
	}

	var parsed []json.RawMessage
	if err := json.Unmarshal(raw, &parsed); err != nil {
		return nil, err
	}
	if len(parsed) < 2 {
		return nil, errUpstream{status: http.StatusNoContent}
	}
	var list []string
	if err := json.Unmarshal(parsed[1], &list); err != nil {
		return nil, err
	}

	// 去掉与查询词完全相同的项（点了等于重新搜同一个词，没有价值）
	out := make([]string, 0, relatedMax)
	for _, it := range list {
		t := strings.TrimSpace(it)
		if t == "" || strings.EqualFold(t, q) {
			continue
		}
		out = append(out, t)
		if len(out) >= relatedMax {
			break
		}
	}
	return out, nil
}
