// 地名 → 经纬度（给地图面板用）。
//
// 为什么需要这一步：必应的嵌入页（/maps/embed）**不认 q=**，它只接受 cp=纬度~经度。
// 只给 q 的话地图永远停在几内亚湾（0,0），用户看到的是「世界地图」，等于没定位。
// 谷歌那条可以用 q=，但 maps.google.com 在多数国内网络下打不开，
// 所以必应才是实际能用的那条，而它必须先有坐标。
//
// 用的是 Photon（komoot 托管的 OSM 地名索引），不是 Nominatim：后者在本机与多数国内
// 网络下直接超时（128.242.240.149:443 连不上），而 Photon 能通、免 key、支持中文。
//
// 为什么走服务端：按地名缓存（同一地名只解析一次），也不把用户 IP 交给对方。
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
	// 限 1 条——地图只需要一个中心点。
	geocodeSource = "https://photon.komoot.io/api/?limit=1&q="
	// 行政区划基本不变，缓存久一点没风险；上限 500 条，防被刷爆内存。
	geocodeTTL = 7 * 24 * time.Hour
	geocodeMax = 500
)

type geocodeEntry struct {
	lat, lon float64
	name     string
	found    bool
	at       time.Time
}

// geocodeClient 直连（不读 HTTP_PROXY）：与 relatedClient 同一个理由，
// 进程继承的系统代理在国内经常是失效的，走它反而全部超时。
var geocodeClient = &http.Client{
	Timeout:   6 * time.Second,
	Transport: &http.Transport{Proxy: nil},
}

var geocodeCache = struct {
	sync.Mutex
	m map[string]geocodeEntry
}{m: map[string]geocodeEntry{}}

// handleGeocode GET /api/geocode?q=上海
//
// 返回 {"found":true,"lat":31.23,"lon":121.47,"name":"上海市, 中国"}；
// 解析不出来时 found=false（地图面板据此提示「换个写法」，而不是画一张错的地图）。
func (s *Server) handleGeocode(w http.ResponseWriter, r *http.Request) {
	q := strings.TrimSpace(r.URL.Query().Get("q"))
	if q == "" || utf8.RuneCountInString(q) > 80 {
		writeJSON(w, http.StatusOK, map[string]any{"found": false})
		return
	}

	if e, ok := geocodeCached(q); ok {
		writeJSON(w, http.StatusOK, geocodeJSON(e))
		return
	}

	e, err := fetchGeocode(r.Context(), q)
	if err != nil {
		// 上游挂了不回 5xx：前端只关心「有没有定位到」，把失败当成没找到更省事。
		writeJSON(w, http.StatusOK, map[string]any{"found": false})
		return
	}
	geocodeStore(q, e)
	writeJSON(w, http.StatusOK, geocodeJSON(e))
}

func geocodeJSON(e geocodeEntry) map[string]any {
	if !e.found {
		return map[string]any{"found": false}
	}
	return map[string]any{"found": true, "lat": e.lat, "lon": e.lon, "name": e.name}
}

func geocodeCached(q string) (geocodeEntry, bool) {
	key := strings.ToLower(q)
	geocodeCache.Lock()
	defer geocodeCache.Unlock()
	e, ok := geocodeCache.m[key]
	if !ok || time.Since(e.at) > geocodeTTL {
		return geocodeEntry{}, false
	}
	return e, true
}

func geocodeStore(q string, e geocodeEntry) {
	key := strings.ToLower(q)
	geocodeCache.Lock()
	defer geocodeCache.Unlock()
	if len(geocodeCache.m) >= geocodeMax {
		geocodeCache.m = map[string]geocodeEntry{}
	}
	e.at = time.Now()
	geocodeCache.m[key] = e
}

// fetchGeocode 调 Photon（GeoJSON 风格）。坐标顺序是 [经度, 纬度]，别写反。
func fetchGeocode(ctx context.Context, q string) (geocodeEntry, error) {
	ctx, cancel := context.WithTimeout(ctx, 6*time.Second)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, geocodeSource+url.QueryEscape(q), nil)
	if err != nil {
		return geocodeEntry{}, err
	}
	req.Header.Set("User-Agent", "voidxo/1.0 (+https://github.com/zealis/voidxo)")
	req.Header.Set("Accept", "application/json")

	resp, err := geocodeClient.Do(req)
	if err != nil {
		return geocodeEntry{}, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return geocodeEntry{}, errUpstream{status: resp.StatusCode}
	}
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 64<<10))
	if err != nil {
		return geocodeEntry{}, err
	}

	var fc struct {
		Features []struct {
			Geometry struct {
				Coordinates []float64 `json:"coordinates"`
			} `json:"geometry"`
			Properties struct {
				Name    string `json:"name"`
				City    string `json:"city"`
				State   string `json:"state"`
				Country string `json:"country"`
			} `json:"properties"`
		} `json:"features"`
	}
	if err := json.Unmarshal(raw, &fc); err != nil {
		return geocodeEntry{}, err
	}
	if len(fc.Features) == 0 {
		return geocodeEntry{}, nil
	}
	f := fc.Features[0]
	// [经度, 纬度]：少一个都不画，宁可报没找到也不要把地图点到 (0,0)。
	if len(f.Geometry.Coordinates) < 2 {
		return geocodeEntry{}, nil
	}
	name := firstNonEmpty(f.Properties.Name, f.Properties.City, f.Properties.State, f.Properties.Country)
	return geocodeEntry{lat: f.Geometry.Coordinates[1], lon: f.Geometry.Coordinates[0], name: name, found: true}, nil
}

func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if v != "" {
			return v
		}
	}
	return ""
}
