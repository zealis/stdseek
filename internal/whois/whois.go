// Package whois 域名注册时间查询。
//
// 排序层给「老域名」一点信任度加权（主流搜索引擎对域名历史的通行做法），
// 前提是知道域名什么时候注册的。注册时间不在页面里，只能查 WHOIS——
// 直接跟 whois 服务器对话要处理各注册局的格式差异，所以走聚合 API：
// 默认 xxapi.cn（v2.xxapi.cn/api/whois，服务端自缓存 3 天、支持一千多后缀），
// 管理员可在后台换成自己的接口（whois.api_url，URL 里用 {domain} 占位）；
// 自定义接口取不到时自动退回默认，不让一个坏配置拖垮整条数据。
//
// 响应解析刻意写得宽容：约定是「JSON 里任意字段带着 ISO 格式的注册时间」，
// 先认常见的几个字段名（xxapi 的是 data.registration_time / "Registration Time"），
// 认不出就全表扫描第一个像注册日期的值——自定义接口的返回结构千奇百怪，
// 钉死字段名等于逼管理员改接口。
package whois

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"time"
)

// DefaultURL 默认查询接口（xxapi.cn，域名用 {domain} 占位）。
// 服务端自身带 3 天缓存，我们的存储层还有 90 天的检查间隔，双重降温。
const DefaultURL = "https://v2.xxapi.cn/api/whois?domain={domain}"

// dateRe ISO 日期（可带时间）。1990 年之前的值视为脏数据不采。
var dateRe = regexp.MustCompile(`\d{4}-\d{2}-\d{2}(T[\d:.]+Z?)?`)

// preferred 常见注册时间字段名（大小写/空格变体都算 xxapi 的返回形态）。
var preferred = []string{
	"registration_time", "registration date", "registration",
	"creation_date", "created_date", "creationdate", "created", "registered",
}

// Fetch 查询域名注册时间。apiURL 里的 {domain} 会被替换为查询域名。
func Fetch(ctx context.Context, client *http.Client, apiURL, domain string) (time.Time, error) {
	if client == nil {
		client = &http.Client{Timeout: 10 * time.Second}
	}
	target := strings.ReplaceAll(apiURL, "{domain}", url.QueryEscape(domain))
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, target, nil)
	if err != nil {
		return time.Time{}, err
	}
	req.Header.Set("User-Agent", "voidxo-whois/0.1")
	req.Header.Set("Accept", "application/json")
	resp, err := client.Do(req)
	if err != nil {
		return time.Time{}, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return time.Time{}, fmt.Errorf("whois api status %d", resp.StatusCode)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, 512<<10))
	if err != nil {
		return time.Time{}, err
	}
	return Parse(body)
}

// Parse 从 whois 接口的 JSON 响应里提取注册时间。
func Parse(body []byte) (time.Time, error) {
	var root map[string]any
	if err := json.Unmarshal(body, &root); err != nil {
		return time.Time{}, fmt.Errorf("whois 响应不是 JSON：%w", err)
	}
	// 先认常见字段（含嵌套 data 一层），再全表扫描兜底
	for _, key := range preferred {
		for _, scope := range []map[string]any{root, nestedData(root)} {
			if t := dateOf(scope, key); !t.IsZero() {
				return t, nil
			}
		}
	}
	if t, ok := scanDates(root); ok {
		return t, nil
	}
	return time.Time{}, fmt.Errorf("whois 响应里没有可识别的注册时间")
}

func nestedData(root map[string]any) map[string]any {
	if d, ok := root["data"].(map[string]any); ok {
		return d
	}
	return nil
}

// dateOf 取 scope[key]（含 xxapi 那种带空格的 "Registration Time" 变体）并解析。
func dateOf(scope map[string]any, key string) time.Time {
	if scope == nil {
		return time.Time{}
	}
	for k, v := range scope {
		if !strings.EqualFold(strings.ReplaceAll(strings.ToLower(k), " ", "_"), strings.ReplaceAll(strings.ToLower(key), " ", "_")) {
			continue
		}
		if s, ok := v.(string); ok {
			return parseDate(s)
		}
	}
	return time.Time{}
}

// scanDates 递归扫描（限 3 层深）：第一个能解析、且早于现在的日期视为注册时间。
// 自定义接口的返回结构千奇百怪，字段名对不上时靠它兜底。
func scanDates(scope map[string]any) (time.Time, bool) {
	return scanDatesDeep(scope, 3)
}

func scanDatesDeep(scope map[string]any, depth int) (time.Time, bool) {
	now := time.Now()
	for _, v := range scope {
		switch val := v.(type) {
		case string:
			if t := parseDate(val); !t.IsZero() && t.Before(now) {
				return t, true
			}
		case map[string]any:
			if depth > 1 {
				if t, ok := scanDatesDeep(val, depth-1); ok {
					return t, true
				}
			}
		}
	}
	return time.Time{}, false
}

// parseDate 认 RFC3339 与纯日期两种形态；解析失败或早于 1990 年返回零值。
func parseDate(s string) time.Time {
	m := dateRe.FindString(s)
	if m == "" {
		return time.Time{}
	}
	for _, layout := range []string{time.RFC3339, "2006-01-02T15:04:05", "2006-01-02"} {
		if t, err := time.Parse(layout, m); err == nil {
			if t.Year() >= 1990 && t.Before(time.Now()) {
				return t
			}
			return time.Time{}
		}
	}
	return time.Time{}
}
