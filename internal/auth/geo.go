// IP 归属地解析。
//
// 不内置任何 IP 库：用户在后台配一个「URL 里含 {ip}」的接口
// （例如 https://ip-api.com/json/{ip}?fields=country,regionName,city,isp），
// 返回 JSON 后从常见字段里取值拼成「国家 省份 城市 运营商」。
// 缓存在 geo_cache 表，同一个 IP 只查一次。
package auth

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"time"
)

// GeoResolver 归属地解析器。
type GeoResolver struct {
	api    string // 含 {ip} 的接口地址
	client *http.Client
}

// NewGeoResolver 构造。api 为空时处于未配置状态。
func NewGeoResolver(api string) *GeoResolver {
	return &GeoResolver{api: strings.TrimSpace(api), client: &http.Client{Timeout: 5 * time.Second}}
}

// Ready 是否已配置。
func (g *GeoResolver) Ready() bool {
	return g != nil && g.api != ""
}

// Resolve 解析归属地，返回「国家 省份 城市 运营商」一类的文本。
//
// 失败返回空串（调用方展示为「未知」），不返回 error：归属地是锦上添花，
// 解析失败不值得让画像页报错。
func (g *GeoResolver) Resolve(ctx context.Context, ip string) string {
	if g == nil || g.api == "" || strings.TrimSpace(ip) == "" {
		return ""
	}
	if strings.ContainsAny(ip, ":") {
		// 内网 / 保留地址：不查
		return ""
	}
	url := strings.ReplaceAll(g.api, "{ip}", ip)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return ""
	}
	resp, err := g.client.Do(req)
	if err != nil {
		return ""
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 16<<10))
	if err != nil {
		return ""
	}
	var d map[string]any
	if err := json.Unmarshal(raw, &d); err != nil {
		return ""
	}
	return geoLabel(d)
}

// geoLabel 从 JSON 里按常见字段名找值，拼成一段可读文本。
func geoLabel(d map[string]any) string {
	keys := []string{"country", "country_name", "regionName", "region", "province", "city", "isp", "org"}
	var parts []string
	seen := map[string]bool{}
	for _, k := range keys {
		v, ok := d[k]
		if !ok {
			continue
		}
		s, ok := v.(string)
		if !ok || s == "" || seen[s] {
			continue
		}
		seen[s] = true
		parts = append(parts, s)
	}
	return strings.Join(parts, " ")
}
