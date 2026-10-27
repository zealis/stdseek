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
//
// client 走默认 Transport（会遵循系统代理）：这类 API 是「传 IP 查它的信息」，
// 请求方出口不影响结果正确性，可达性优先。进程继承了系统代理也能正常查。
func NewGeoResolver(api string) *GeoResolver {
	return &GeoResolver{api: strings.TrimSpace(api), client: &http.Client{Timeout: 5 * time.Second}}
}

// Ready 是否已配置。
func (g *GeoResolver) Ready() bool {
	return g != nil && g.api != ""
}

// GeoInfo 解析结果：归属地 + ASN + 信誉分 + 风险标记。
type GeoInfo struct {
	Label  string   `json:"label"`  // 地区：「国家 省份 城市」
	ISP    string   `json:"isp"`    // 网络运营商（中国移动 / Cloudflare, Inc. 等）
	ASN    int      `json:"asn"`
	Score  int      `json:"score"`
	Status string   `json:"status"` // good / suspicious / bad…
	Risk   []string `json:"risk"`   // datacenter / vpn / proxy / tor / abuser…
}

// Resolve 解析归属地与信誉信息。
//
// 失败返回零值（调用方展示为「未知」），不返回 error：归属地是锦上添花，
// 解析失败不值得让画像页报错。
func (g *GeoResolver) Resolve(ctx context.Context, ip string) GeoInfo {
	if g == nil || g.api == "" || strings.TrimSpace(ip) == "" {
		return GeoInfo{}
	}
	if strings.ContainsAny(ip, ":") {
		// 内网 / 保留地址：不查
		return GeoInfo{}
	}
	url := strings.ReplaceAll(g.api, "{ip}", ip)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return GeoInfo{}
	}
	resp, err := g.client.Do(req)
	if err != nil {
		return GeoInfo{}
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 16<<10))
	if err != nil {
		return GeoInfo{}
	}
	var d map[string]any
	if err := json.Unmarshal(raw, &d); err != nil {
		return GeoInfo{}
	}

	info := GeoInfo{Label: geoRegion(d), ISP: geoISP(d)}
	if f, ok := d["asn"].(float64); ok {
		info.ASN = int(f)
	}
	if f, ok := d["score"].(float64); ok {
		info.Score = int(f)
	}
	info.Status, _ = d["status"].(string)
	if flags, ok := d["flags"].(map[string]any); ok {
		for name, v := range flags {
			if b, ok := v.(bool); ok && b {
				info.Risk = append(info.Risk, name)
			}
		}
	}
	return info
}

// geoRegion 拼地区：国家 省 市。
func geoRegion(d map[string]any) string {
	keys := []string{"country", "country_name", "regionName", "region", "province", "city"}
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

// geoISP 取网络运营商，没有 isp 字段时退回 org。
func geoISP(d map[string]any) string {
	for _, k := range []string{"isp", "org", "as"} {
		if v, ok := d[k].(string); ok && strings.TrimSpace(v) != "" {
			return strings.TrimSpace(v)
		}
	}
	return ""
}
