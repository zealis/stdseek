package api

import (
	"bytes"
	"io"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"voidxo/internal/webui"
)

// 站点标识：只做「换一个图标」这一件事。
//
// 为什么存文件而不是塞进前端产物：dist 是 go:embed 编译期快照，改图标等于
// 改代码 + 重新构建 + 重启进程——项目里最容易踩的那类「看着没变」。
// 存到 <data>/brand/ 下，上传即生效，重启也还在。
const (
	settingBrandIcon    = "brand.icon"    // 文件名（brand/ 下），空 = 用自带图标
	settingBrandVersion = "brand.version" // 秒级时间戳，用于缓存失效
	maxBrandBytes       = 512 << 10       // 512KB：favicon 用不了这么大
)

// Branding 站点标识（公开接口返回，前端据此决定顶栏画默认章还是自定义图标）。
type Branding struct {
	HasIcon bool   `json:"has_icon"`
	IconURL string `json:"icon_url"`
	Version string `json:"version"`
}

func (s *Server) brandDir() string { return filepath.Join(s.DataDir, "brand") }

// brandFile 当前生效的图标文件名与版本号。
func (s *Server) brandFile(r *http.Request) (name, ver string, ok bool) {
	name, _ = s.st.Setting(r.Context(), settingBrandIcon, "")
	ver, _ = s.st.Setting(r.Context(), settingBrandVersion, "")
	if name == "" {
		return "", ver, false
	}
	// 只认 basename：settings 里的值理论上只由本进程写入，但拼路径前
	// 挡一层能避免以后有人从别的地方灌值造成目录穿越。
	name = filepath.Base(name)
	if _, err := os.Stat(filepath.Join(s.brandDir(), name)); err != nil {
		return "", ver, false
	}
	return name, ver, true
}

func (s *Server) brandInfo(r *http.Request) Branding {
	_, ver, ok := s.brandFile(r)
	if !ok {
		return Branding{Version: ver}
	}
	return Branding{HasIcon: true, IconURL: "/brand/icon?v=" + ver, Version: ver}
}

// handleBranding GET /api/branding
func (s *Server) handleBranding(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, s.brandInfo(r))
}

// handleBrandIcon GET /brand/icon、/favicon.ico、/favicon.svg
//
// 浏览器的标签页图标只认 favicon，不会去读 /api/branding，所以这两个路径
// 必须由服务端顶掉内嵌文件；没设自定义图标时原样交回给内嵌产物。
func (s *Server) handleBrandIcon(w http.ResponseWriter, r *http.Request) {
	name, ver, ok := s.brandFile(r)
	if !ok {
		if r.URL.Path == "/brand/icon" {
			http.NotFound(w, r)
			return
		}
		webui.Handler().ServeHTTP(w, r)
		return
	}

	f, err := os.Open(filepath.Join(s.brandDir(), name))
	if err != nil {
		http.NotFound(w, r)
		return
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil {
		http.NotFound(w, r)
		return
	}

	etag := `"` + ver + `"`
	w.Header().Set("ETag", etag)
	if r.Header.Get("If-None-Match") == etag {
		w.WriteHeader(http.StatusNotModified)
		return
	}
	w.Header().Set("Content-Type", brandContentType(name))
	w.Header().Set("X-Content-Type-Options", "nosniff")
	// 直接打开图标地址时不执行 SVG 内嵌脚本（作为 <img>/favicon 加载本来也不执行）。
	w.Header().Set("Content-Security-Policy", "default-src 'none'; style-src 'unsafe-inline'")
	if r.URL.Query().Get("v") != "" {
		w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
	} else {
		// 浏览器对 /favicon.ico 缓存很固执：no-cache 让它每次带 ETag 来问，
		// 命中就 304，比永远显示旧图标好。
		w.Header().Set("Cache-Control", "no-cache")
	}
	http.ServeContent(w, r, name, st.ModTime(), f)
}

// handleUploadBrandIcon POST /admin/branding/icon（multipart，字段名 file）
func (s *Server) handleUploadBrandIcon(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, maxBrandBytes+64<<10)
	if err := r.ParseMultipartForm(maxBrandBytes); err != nil {
		writeErr(w, http.StatusBadRequest, "读取上传内容失败（图标不能超过 512 KB）")
		return
	}
	file, hdr, err := r.FormFile("file")
	if err != nil {
		writeErr(w, http.StatusBadRequest, "没有收到文件")
		return
	}
	defer file.Close()
	data, err := io.ReadAll(io.LimitReader(file, maxBrandBytes+1))
	if err != nil {
		writeErr(w, http.StatusBadRequest, "读取文件失败")
		return
	}
	if len(data) == 0 {
		writeErr(w, http.StatusBadRequest, "文件是空的")
		return
	}
	if len(data) > maxBrandBytes {
		writeErr(w, http.StatusBadRequest, "图标不能超过 512 KB")
		return
	}
	ext, _, ok := sniffIcon(data)
	if !ok {
		writeErr(w, http.StatusBadRequest, "只支持 PNG / ICO / SVG / WebP / JPEG")
		return
	}

	dir := s.brandDir()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		writeErr(w, http.StatusInternalServerError, "创建图标目录失败")
		return
	}
	ver := strconv.FormatInt(time.Now().Unix(), 10)
	name := "icon-" + ver + ext
	if err := os.WriteFile(filepath.Join(dir, name), data, 0o644); err != nil {
		writeErr(w, http.StatusInternalServerError, "保存图标失败")
		return
	}

	old, _ := s.st.Setting(r.Context(), settingBrandIcon, "")
	if err := s.st.SetSetting(r.Context(), settingBrandIcon, name); err != nil {
		_ = os.Remove(filepath.Join(dir, name))
		writeErr(w, http.StatusInternalServerError, "保存配置失败")
		return
	}
	if err := s.st.SetSetting(r.Context(), settingBrandVersion, ver); err != nil {
		slog.Warn("brand version write failed", "err", err)
	}
	// 旧文件删掉：图标只有一份，留着只是占空间且容易搞混「哪个在生效」。
	if old != "" && old != name {
		_ = os.Remove(filepath.Join(dir, filepath.Base(old)))
	}
	slog.Info("brand icon updated", "file", name, "bytes", len(data), "upload", hdr.Filename)
	writeJSON(w, http.StatusOK, map[string]any{"branding": s.brandInfo(r)})
}

// handleClearBrandIcon POST /admin/branding/icon/clear：恢复自带图标。
func (s *Server) handleClearBrandIcon(w http.ResponseWriter, r *http.Request) {
	old, _ := s.st.Setting(r.Context(), settingBrandIcon, "")
	_ = s.st.SetSetting(r.Context(), settingBrandIcon, "")
	_ = s.st.SetSetting(r.Context(), settingBrandVersion, "")
	if old != "" {
		_ = os.Remove(filepath.Join(s.brandDir(), filepath.Base(old)))
	}
	slog.Info("brand icon cleared")
	writeJSON(w, http.StatusOK, map[string]any{"branding": s.brandInfo(r)})
}

// sniffIcon 按文件头判断类型，不信任扩展名与客户端给的 Content-Type。
func sniffIcon(data []byte) (ext, ctype string, ok bool) {
	switch {
	case bytes.HasPrefix(data, []byte("\x89PNG\r\n\x1a\n")):
		return ".png", "image/png", true
	case bytes.HasPrefix(data, []byte{0x00, 0x00, 0x01, 0x00}):
		return ".ico", "image/x-icon", true
	case bytes.HasPrefix(data, []byte{0xFF, 0xD8, 0xFF}):
		return ".jpg", "image/jpeg", true
	case len(data) >= 12 && string(data[:4]) == "RIFF" && string(data[8:12]) == "WEBP":
		return ".webp", "image/webp", true
	}
	head := data
	if len(head) > 512 {
		head = head[:512]
	}
	// SVG 是文本：没有魔数，只能看开头有没有 <svg。大小写都要认（<SVG 合法）。
	if strings.Contains(strings.ToLower(strings.TrimSpace(string(head))), "<svg") {
		return ".svg", "image/svg+xml", true
	}
	return "", "", false
}

func brandContentType(name string) string {
	switch strings.ToLower(filepath.Ext(name)) {
	case ".png":
		return "image/png"
	case ".ico":
		return "image/x-icon"
	case ".jpg", ".jpeg":
		return "image/jpeg"
	case ".webp":
		return "image/webp"
	case ".svg":
		return "image/svg+xml"
	}
	return "application/octet-stream"
}
