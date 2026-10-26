// Package suggest 提供搜索词补全。
//
// 数据源是 Bing 的 qsonhs 接口（搜索引擎框用的那个，没有官方文档）：它返回的是
// JSONP——一段 `window.bing.sug({...})` 的 JS。这里在服务端取回来剥壳成 JSON，
// 而不是让浏览器插 <script>：
//
//  1. 那个接口只走 http，https 页面直连会被当混合内容拦掉；
//  2. 走服务端可以用一次 10 分钟缓存挡住绝大部分按键，不必每个字母都打到 Bing；
//  3. 用户的 IP 与搜索词不直接暴露给第三方。
package suggest

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"
)

// maxItems 一次最多返回几条：补全只是提示，列太长反而挡住搜索结果。
const maxItems = 8

// cacheMax 缓存键上限。前缀查询的重复率极高（同一个词被反复键入），
// 简单 map + 满了清空就够，不值得引入 LRU。
const cacheMax = 512

type cacheEntry struct {
	items []string
	at    time.Time
}

// Bing 是 Bing 补全的客户端。
type Bing struct {
	client *http.Client
	ttl    time.Duration

	mu    sync.Mutex
	cache map[string]cacheEntry
}

// NewBing 构造客户端。超时压到 2 秒：补全慢于这个数就已经打断输入了，
// 不如直接放弃这一轮。
func NewBing() *Bing {
	return &Bing{
		client: &http.Client{Timeout: 2 * time.Second},
		ttl:    10 * time.Minute,
		cache:  make(map[string]cacheEntry),
	}
}

// Suggest 返回某个前缀的补全列表。任何失败都返回空列表 + 错误，
// 调用方按「没有补全」处理——补不出来不该让搜索框报错。
func (b *Bing) Suggest(ctx context.Context, q string) ([]string, error) {
	q = strings.TrimSpace(q)
	if q == "" {
		return nil, nil
	}

	b.mu.Lock()
	if e, ok := b.cache[q]; ok && time.Since(e.at) < b.ttl {
		b.mu.Unlock()
		return e.items, nil
	}
	b.mu.Unlock()

	items, err := b.fetch(ctx, q)
	if err != nil {
		return nil, err
	}

	b.mu.Lock()
	if len(b.cache) >= cacheMax {
		b.cache = make(map[string]cacheEntry, cacheMax)
	}
	b.cache[q] = cacheEntry{items: items, at: time.Now()}
	b.mu.Unlock()

	return items, nil
}

func (b *Bing) fetch(ctx context.Context, q string) ([]string, error) {
	u := "https://api.bing.com/qsonhs.aspx?type=cb&cb=window.bing.sug&q=" + url.QueryEscape(q)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return nil, err
	}
	// 不带浏览器 UA 时这个接口偶尔回空体，加一个常见的。
	req.Header.Set("User-Agent", "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/131.0 Safari/537.36")
	req.Header.Set("Accept-Language", "zh-CN,zh;q=0.9,en;q=0.8")

	resp, err := b.client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("bing suggest: status %d", resp.StatusCode)
	}
	// 补全响应很小，限个上限防止异常大包
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 256<<10))
	if err != nil {
		return nil, err
	}

	return parseSuggestions(raw)
}

// parseSuggestions 解析 JSONP 响应体，按出现顺序取前 maxItems 条并去重。
// 单独抽出来是为了能用固定样本测，不必联网。
func parseSuggestions(raw []byte) ([]string, error) {
	var payload struct {
		AS struct {
			Results []struct {
				Suggests []struct {
					Txt string `json:"Txt"`
				} `json:"Suggests"`
			} `json:"Results"`
		} `json:"AS"`
	}
	if err := json.Unmarshal(stripJSONP(raw), &payload); err != nil {
		return nil, err
	}

	seen := make(map[string]bool, maxItems)
	out := make([]string, 0, maxItems)
	for _, r := range payload.AS.Results {
		for _, s := range r.Suggests {
			txt := strings.TrimSpace(s.Txt)
			if txt == "" || seen[txt] {
				continue
			}
			seen[txt] = true
			out = append(out, txt)
			if len(out) >= maxItems {
				return out, nil
			}
		}
	}
	return out, nil
}

// stripJSONP 从 `window.bing.sug({...} /* pageview_candidate */);` 里取出 `{...}`。
//
// 不能用「第一个 ( 到最后一个 )」——真实响应在回调参数后面还挂着注释和分号，
// 那样会把 `/* pageview_candidate */` 一起切进去，JSON 直接解析失败。
// 这里按花括号深度扫到配对的那个 }，并跳过字符串字面量里的括号。
func stripJSONP(raw []byte) []byte {
	start := bytes.IndexByte(raw, '(')
	if start < 0 {
		return raw
	}
	open := bytes.IndexByte(raw[start:], '{')
	if open < 0 {
		return raw
	}
	open += start

	depth := 0
	inStr := false
	escaped := false
	for i := open; i < len(raw); i++ {
		c := raw[i]
		if inStr {
			switch {
			case escaped:
				escaped = false
			case c == '\\':
				escaped = true
			case c == '"':
				inStr = false
			}
			continue
		}
		switch c {
		case '"':
			inStr = true
		case '{':
			depth++
		case '}':
			depth--
			if depth == 0 {
				return raw[open : i+1]
			}
		}
	}
	return raw
}
