// Package webui 把前端构建产物（web/ → internal/webui/dist）通过 go:embed 打进单二进制。
package webui

import (
	"embed"
	"io/fs"
	"net/http"
	"strings"
)

//go:embed all:dist
var assets embed.FS

// Handler 提供静态资源与 SPA 路由回退。
// 约定：/api/* 与 /admin/* 是后端接口，未命中时返回 404 而不回退到 index.html；
// 其余路径（/、/search、/manage/*）交给前端路由。
func Handler() http.Handler {
	sub, err := fs.Sub(assets, "dist")
	if err != nil {
		return http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			http.Error(w, "web assets unavailable", http.StatusInternalServerError)
		})
	}
	files := http.FileServer(http.FS(sub))

	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		p := r.URL.Path
		if strings.HasPrefix(p, "/api/") || strings.HasPrefix(p, "/admin/") {
			w.Header().Set("Content-Type", "application/json; charset=utf-8")
			w.WriteHeader(http.StatusNotFound)
			_, _ = w.Write([]byte(`{"error":"not found"}`))
			return
		}
		if strings.HasPrefix(p, "/assets/") {
			w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
			files.ServeHTTP(w, r)
			return
		}
		// 静态文件（favicon 等）直接返回
		if p != "/" {
			if _, err := fs.Stat(sub, strings.TrimPrefix(p, "/")); err == nil {
				files.ServeHTTP(w, r)
				return
			}
		}
		// SPA 回退
		index, err := fs.ReadFile(sub, "index.html")
		if err != nil {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Header().Set("Cache-Control", "no-cache")
		_, _ = w.Write(index)
	})
}
