// Package webui 把前端构建产物（web/ → internal/webui/dist）通过 go:embed 打进单二进制。
package webui

import (
	"embed"
	"io/fs"
	"mime"
	"net/http"
	"path"
	"strings"
)

//go:embed all:dist
var assets embed.FS

// Handler 提供静态资源与 SPA 路由回退。
// 约定：/api/* 与 /admin/* 是后端接口，未命中时返回 404 而不回退到 index.html；
// 其余路径先按「精确文件 → 目录下的 index.html → 首页」三级解析。
//
// 不用 http.FileServer：它对目录请求会 301 到带尾斜杠的地址，而 embed.FS 不接受
// 尾斜杠路径，容易在 /search 这类预渲染目录上返回空响应。
// distFS 复用一个子文件系统；失败时返回 nil，由调用方降级。
func distFS() fs.FS {
	sub, err := fs.Sub(assets, "dist")
	if err != nil {
		return nil
	}
	return sub
}

// IndexHTML 返回内嵌的 index.html 原始字节。
// 服务端要往 <head> 里注入 Open Graph 等标签（见 api/seo.go），必须读原文件再改，
// 不能走 Handler()——那条路会把整个响应写出去。
func IndexHTML() ([]byte, error) {
	sub := distFS()
	if sub == nil {
		return nil, fs.ErrNotExist
	}
	return fs.ReadFile(sub, "index.html")
}

func Handler() http.Handler {
	sub, err := fs.Sub(assets, "dist")
	if err != nil {
		return http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			http.Error(w, "web assets unavailable", http.StatusInternalServerError)
		})
	}

	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		p := r.URL.Path
		if strings.HasPrefix(p, "/api/") || strings.HasPrefix(p, "/admin/") {
			w.Header().Set("Content-Type", "application/json; charset=utf-8")
			w.WriteHeader(http.StatusNotFound)
			_, _ = w.Write([]byte(`{"error":"not found"}`))
			return
		}

		// 历史路径：旧 Qwik 前端把产物放 /assets（样式）与 /build（chunk）。
		// 现用 Angular，产物是带哈希的根路径文件（main-*.js、chunk-*.js、styles-*.css），
		// 走下面的精确文件分支；这里保留是为了兼容 /assets/ 下的 favicon 等静态资源。
		// 注意：fs.ReadFile 不接受前导斜杠，必须先去掉。
		if strings.HasPrefix(p, "/assets/") || strings.HasPrefix(p, "/build/") {
			w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
			if serveFile(sub, w, strings.TrimPrefix(p, "/")) {
				return
			}
			http.NotFound(w, r)
			return
		}

		rel := strings.Trim(p, "/")
		if rel == "" {
			serveFile(sub, w, "index.html")
			return
		}

		// 1) 精确文件（favicon、robots.txt 等）
		if serveFile(sub, w, rel) {
			return
		}
		// 2) 预渲染目录：/search → search/index.html
		if serveFile(sub, w, rel+"/index.html") {
			return
		}
		// 3) SPA 回退
		w.Header().Set("Cache-Control", "no-cache")
		if !serveFile(sub, w, "index.html") {
			http.NotFound(w, r)
		}
	})
}

func serveFile(sub fs.FS, w http.ResponseWriter, name string) bool {
	data, err := fs.ReadFile(sub, name)
	if err != nil {
		return false
	}
	ext := path.Ext(name)
	if ct := mime.TypeByExtension(ext); ct != "" {
		w.Header().Set("Content-Type", ct)
	}
	switch ext {
	case ".js", ".css":
		// Angular 的产物都带内容哈希（main-UCNKG3IY.js、chunk-*.js、styles-*.css），
		// 内容变了文件名就变，所以可以放心 immutable 一年。
		// 以前只给 /assets/ 与 /build/ 设了缓存头，根路径这些带哈希的文件反而没设，
		// 每次进站都要重新下载一遍全部 chunk。
		w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
	case ".html":
		w.Header().Set("Cache-Control", "no-cache")
	}
	_, _ = w.Write(data)
	return true
}
