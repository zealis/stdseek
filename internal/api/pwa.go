package api

import (
	"bytes"
	"context"
	"crypto/sha256"
	_ "embed"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"html"
	"image"
	_ "image/gif"
	_ "image/jpeg"
	_ "image/png"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"voidxo/internal/webui"
)

// sw.js 是 service worker 的源码模板，不是静态发布的文件。
// 放在后端是因为它要在响应时注入「预缓存清单」——那份清单来自当前二进制的
// 前端产物（main-*.js 这些哈希文件名），每次构建都不一样；
// 让前端去生成它，就得在构建脚本里再维护一套产物清单，迟早对不上。
//
//go:embed sw.js
var swSource string

// pwaAssetRe 抓 index.html 里的脚本与样式引用。
// 只认「相对文件名」（不含 / 和 :），这样 canonical、外链之类不会被误收。
var pwaAssetRe = regexp.MustCompile(`(?:src|href)="([A-Za-z0-9._-]+\.(?:js|css))"`)

// PWA 主题色：与 index.html 的 <meta name="theme-color"> 保持一致。
// manifest 只能写一个颜色，取浅色那份——深色用户看到启动画面闪一下白，
// 比反过来（浅色用户看到一块黑）更容易接受。
const (
	pwaThemeLight = "#f9fafb"
	pwaBackdrop   = "#f9fafb"
)

// pwaShell 返回当前构建的 service worker 版本号与预缓存清单。
//
// 版本号的算法：资源名（已经带内容哈希）+ 站名 + 图标版本 + 产品版本。
// 任何一项变了版本就变，浏览器据此装新 SW、换新的 shell 缓存目录。
// 不用时间戳：同样的构建在不同实例之间要能算出同一个版本，否则每台机器
// 都会认为对方的缓存是新的。
func (s *Server) pwaShell(ctx context.Context) (string, []string) {
	b := s.brandInfo(ctx)

	var assets []string
	seen := make(map[string]bool)
	add := func(u string) {
		if u == "" || seen[u] {
			return
		}
		seen[u] = true
		assets = append(assets, u)
	}

	if page, err := webui.IndexHTML(); err == nil {
		for _, m := range pwaAssetRe.FindAllStringSubmatch(string(page), -1) {
			add("/" + m[1])
		}
	}
	// 离线页与 manifest 自己也要在断网时取得到，否则回退过去是一片空白。
	add("/offline.html")
	add("/manifest.webmanifest")
	// 两个内置图标始终进清单：manifest 的 icons 里无条件列了 favicon.svg，
	// 清单缺它的话，离线时那张 icon 就是空的。
	add("/favicon.ico")
	add("/favicon.svg")
	if b.HasIcon {
		add(b.IconURL)
	}
	sort.Strings(assets)

	h := sha256.New()
	h.Write([]byte(strings.Join(assets, "\n")))
	h.Write([]byte(b.Name))
	h.Write([]byte(b.AppVersion))
	h.Write([]byte(b.Version))
	return hex.EncodeToString(h.Sum(nil))[:12], assets
}

// handleManifest GET /manifest.webmanifest
//
// 动态生成而不是放静态文件：站名与图标都能在后台改，静态 manifest 会让
// 装了 PWA 的人一直看到旧名字（Android 甚至会按旧名字建文件夹）。
func (s *Server) handleManifest(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	b := s.brandInfo(ctx)
	info := s.seoInfo(ctx)

	type icon struct {
		Src     string `json:"src"`
		Sizes   string `json:"sizes"`
		Type    string `json:"type"`
		Purpose string `json:"purpose"`
	}

	// 内置 SVG 始终保留：它是矢量，任何尺寸都清晰，也是没传图标时的唯一来源。
	icons := []icon{{Src: "/favicon.svg", Sizes: "any", Type: "image/svg+xml", Purpose: "any"}}
	if b.HasIcon {
		if name, _, ok := s.brandFile(ctx); ok {
			sz := "any"
			// SVG 之外要给出真实像素尺寸：某些安装器（三星浏览器、部分安卓启动器）
			// 遇到 "any" 的位图会直接跳过这个图标。
			if !strings.EqualFold(path.Ext(name), ".svg") {
				if data, err := os.ReadFile(filepath.Join(s.brandDir(), name)); err == nil {
					iw, ih := iconPixels(data, path.Ext(name))
					sz = fmt.Sprintf("%dx%d", iw, ih)
				}
			}
			icons = append([]icon{{Src: b.IconURL, Sizes: sz, Type: brandContentType(name), Purpose: "any"}}, icons...)
		}
	}

	m := map[string]any{
		"id":               "/",
		"name":             b.Name,
		"short_name":       shortName(b.Name),
		"description":      info.Description,
		"lang":             "zh-CN",
		"dir":              "ltr",
		"start_url":        "/",
		"scope":            "/",
		"display":          "standalone",
		"background_color": pwaBackdrop,
		"theme_color":      pwaThemeLight,
		"icons":            icons,
	}

	w.Header().Set("Content-Type", "application/manifest+json; charset=utf-8")
	w.Header().Set("Cache-Control", "no-cache")
	_ = json.NewEncoder(w).Encode(m)
}

// handleServiceWorker GET /sw.js
func (s *Server) handleServiceWorker(w http.ResponseWriter, r *http.Request) {
	ver, assets := s.pwaShell(r.Context())
	list, _ := json.Marshal(assets)

	body := strings.NewReplacer(
		"__VX_VERSION__", ver,
		"__VX_PRECACHE__", string(list),
	).Replace(swSource)

	w.Header().Set("Content-Type", "text/javascript; charset=utf-8")
	// 绝不能进缓存：浏览器判断「SW 有没有更新」靠的就是这次请求的字节比对，
	// 给它 immutable（静态 js 的默认策略）会让更新永远发不出去。
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Service-Worker-Allowed", "/")
	_, _ = w.Write([]byte(body))
}

// handleOfflinePage GET /offline.html
//
// 断网时的兜底页。刻意做成一个自包含的静态页（内联样式，不依赖 Angular、
// 不依赖外部 CSS）：它要在「什么都下载不到」的前提下正常显示。
func (s *Server) handleOfflinePage(w http.ResponseWriter, r *http.Request) {
	b := s.brandInfo(r.Context())
	icon := "/favicon.svg"
	if b.HasIcon {
		icon = b.IconURL
	}
	page := fmt.Sprintf(offlinePageHTML,
		html.EscapeString(b.Name),
		html.EscapeString(icon),
		html.EscapeString(b.Name),
	)

	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("X-Robots-Tag", "noindex")
	_, _ = w.Write([]byte(page))
}

// shortName 主屏图标下面那行字，按字数截断。
// Android 大约显示 12 个字符，超出会被省略号硬切，不如自己截得干净。
func shortName(name string) string {
	name = strings.TrimSpace(name)
	r := []rune(name)
	if len(r) <= 12 {
		return name
	}
	return string(r[:12])
}

// iconPixels 读位图图标的实际尺寸；读不出来就按扩展名给个保守值。
func iconPixels(data []byte, ext string) (int, int) {
	if cfg, _, err := image.DecodeConfig(bytes.NewReader(data)); err == nil && cfg.Width > 0 && cfg.Height > 0 {
		return cfg.Width, cfg.Height
	}
	if strings.EqualFold(ext, ".ico") {
		return 256, 256
	}
	return 512, 512
}

// offlinePageHTML 离线页模板。参数依次是：站名（title）、图标地址、站名（正文）。
const offlinePageHTML = `<!doctype html>
<html lang="zh-CN">
  <head>
    <meta charset="utf-8" />
    <meta name="viewport" content="width=device-width, initial-scale=1, viewport-fit=cover" />
    <meta name="theme-color" media="(prefers-color-scheme: light)" content="#f9fafb" />
    <meta name="theme-color" media="(prefers-color-scheme: dark)" content="#0d131c" />
    <meta name="robots" content="noindex" />
    <title>离线 · %s</title>
    <style>
      :root {
        color-scheme: light dark;
        --bg: #f9fafb;
        --fg: #101828;
        --muted: #667085;
        --line: rgba(16, 24, 40, 0.12);
        --accent: #0f766e;
        --accent-fg: #ffffff;
      }
      @media (prefers-color-scheme: dark) {
        :root {
          --bg: #0d131c;
          --fg: #e6edf5;
          --muted: #93a0b4;
          --line: rgba(230, 237, 245, 0.16);
          --accent: #2dd4bf;
          --accent-fg: #062a26;
        }
      }
      * { box-sizing: border-box; }
      html, body { height: 100%%; }
      body {
        margin: 0;
        display: flex;
        align-items: center;
        justify-content: center;
        padding: 32px 24px;
        background: var(--bg);
        color: var(--fg);
        font: 400 15px/1.7 -apple-system, BlinkMacSystemFont, "Segoe UI", "PingFang SC",
          "Hiragino Sans GB", "Microsoft YaHei", sans-serif;
        text-align: center;
      }
      main { max-width: 340px; }
      img { width: 48px; height: 48px; border-radius: 12px; }
      h1 { margin: 18px 0 6px; font-size: 19px; font-weight: 500; letter-spacing: -0.01em; }
      p { margin: 0; color: var(--muted); font-size: 14px; }
      button {
        margin-top: 22px;
        padding: 9px 22px;
        border: 0;
        border-radius: 10px;
        background: var(--accent);
        color: var(--accent-fg);
        font: inherit;
        font-weight: 500;
        cursor: pointer;
      }
      button:active { opacity: 0.85; }
    </style>
  </head>
  <body>
    <main>
      <img src="%s" alt="" />
      <h1>%s</h1>
      <p>当前离线，搜索需要联网。</p>
      <button type="button" onclick="location.reload()">重试</button>
    </main>
  </body>
</html>
`
