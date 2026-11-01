package api

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"html"
	"io"
	"log/slog"
	"math"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/zealis/stdseek/internal/webui"
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
	// iconRenderVersion 图标「怎么发出去」的版本号，进 URL 的查询串。
	// 服务端改了处理方式（例如给 SVG 补宽高）就要 +1，否则客户端按 immutable
	// 缓存着旧的那份，改了等于没改。
	iconRenderVersion = "2"
)

// Branding 站点标识（公开接口返回，前端据此决定顶栏画默认章还是自定义图标）。
type Branding struct {
	HasIcon bool   `json:"has_icon"`
	IconURL string `json:"icon_url"`
	Version string `json:"version"`
	// Name 站点名称（后台「站点信息 → 网站名称」）。空则回默认名。
	// 顶栏与首屏的品牌字用它：以前这个名字只存库、界面上没有任何地方读，
	// 表现就是「改了网站名称、点保存、页面没变化」。
	Name string `json:"name"`
	// AppVersion / Commit 当前构建。前端拿它在控制台打一行版本横幅（见 main.ts），
	// 自托管用户贴日志时能直接看出跑的是哪一版。
	// 注意与上面的 Version 区分：那个是**图标文件的版本号**（做 URL 指纹用），
	// 两个都叫 version 会让人以为可以互换，改错一个就是缓存永远不刷新。
	AppVersion string `json:"app_version"`
	Commit     string `json:"commit"`
}

func (s *Server) brandDir() string { return filepath.Join(s.DataDir, "brand") }

// siteName 站点名称，空则用默认名。
func (s *Server) siteName(ctx context.Context) string {
	name, _ := s.st.GetSetting(ctx, "site.name")
	if name = strings.TrimSpace(name); name == "" {
		return defaultSiteName
	}
	return name
}

// brandFile 当前生效的图标文件名与版本号。
func (s *Server) brandFile(ctx context.Context) (name, ver string, ok bool) {
	name, _ = s.st.Setting(ctx, settingBrandIcon, "")
	ver, _ = s.st.Setting(ctx, settingBrandVersion, "")
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

func (s *Server) brandInfo(ctx context.Context) Branding {
	_, iconVer, ok := s.brandFile(ctx)
	// Version 仍是「图标的版本号」（前端用它拼 URL 指纹），AppVersion 才是产品版本。
	out := Branding{Version: iconVer, AppVersion: s.Version, Commit: s.Commit,
		Name: s.siteName(ctx)}
	if !ok {
		return out
	}
	out.HasIcon = true
	// 渲染版本直接拼在 v 里：URL 里不用 &，注入 HTML 的 JSON 也就不会被转义成 \u0026。
	out.IconURL = "/brand/icon?v=" + iconVer + "-" + iconRenderVersion
	return out
}

// injectBranding 把站点标识写进 HTML。
//
// 为什么要在服务端写：前端是拿 /api/branding 才知道图标和站名的，
// 网络慢的时候会出现「先看到默认的 </> 章和默认站名，再跳成自己的」——
// 越慢越明显，看起来像页面坏了。写进 HTML 后，Angular 第一次渲染就是对的。
//
// 同时替换首屏骨架里的站名（那是 index.html 里写死的），否则慢网下还会先闪一下
// 「StdSeek」。
func injectBranding(page []byte, b Branding) []byte {
	raw, err := json.Marshal(b)
	if err != nil {
		return page
	}
	// json.Marshal 默认把 < > & 转成 \u003c 之类，站名里带 </script> 也逃不出去。
	script := []byte("<script>window.__STDSEEK_BRANDING__=" + string(raw) + ";</script>")
	out := bytes.Replace(page, []byte("</head>"), append(script, []byte("</head>")...), 1)
	return injectBootTitle(out, b.Name)
}

// injectBootTitle 把首屏骨架的 <h1 class="boot-title"> 换成真实站名。
// 结构保持「主名 + 副标 span」，和真实 hero 一致：骨架存在的意义就是不跳动。
func injectBootTitle(page []byte, name string) []byte {
	name = strings.TrimSpace(name)
	if name == "" {
		return page
	}
	const open = `<h1 class="boot-title">`
	i := bytes.Index(page, []byte(open))
	if i < 0 {
		return page
	}
	j := bytes.Index(page[i:], []byte("</h1>"))
	if j < 0 {
		return page
	}
	main, sub := name, ""
	if k := strings.Index(name, " "); k > 0 {
		main, sub = name[:k], strings.TrimSpace(name[k+1:])
	}
	inner := html.EscapeString(main)
	if sub != "" {
		inner += " <span>" + html.EscapeString(sub) + "</span>"
	}
	// j 是相对 page[i:] 的偏移，所以「从这里接着抄」是 i+j，不是 i+len(open)+j。
	out := append([]byte{}, page[:i]...)
	out = append(out, []byte(open+inner)...)
	out = append(out, page[i+j:]...)
	return out
}

// handleBranding GET /api/branding
func (s *Server) handleBranding(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, s.brandInfo(r.Context()))
}

// handleBrandIcon GET /brand/icon、/favicon.ico、/favicon.svg
//
// 浏览器的标签页图标只认 favicon，不会去读 /api/branding，所以这两个路径
// 必须由服务端顶掉内嵌文件；没设自定义图标时原样交回给内嵌产物。
func (s *Server) handleBrandIcon(w http.ResponseWriter, r *http.Request) {
	name, ver, ok := s.brandFile(r.Context())
	if !ok {
		if r.URL.Path == "/brand/icon" {
			http.NotFound(w, r)
			return
		}
		webui.Handler().ServeHTTP(w, r)
		return
	}

	path := filepath.Join(s.brandDir(), name)
	st, err := os.Stat(path)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	// 图标最大 512KB，直接读进内存：SVG 要顺手补宽高（见 normalizeSVGSize），
	// 也就没法交给 ServeContent 边读边发。
	data, err := os.ReadFile(path)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	if strings.EqualFold(filepath.Ext(name), ".svg") {
		data = normalizeSVGSize(data)
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
	w.Header().Set("Content-Length", strconv.Itoa(len(data)))
	w.Header().Set("Last-Modified", st.ModTime().UTC().Format(http.TimeFormat))
	if r.Method == http.MethodHead {
		w.WriteHeader(http.StatusOK)
		return
	}
	_, _ = w.Write(data)
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
	writeJSON(w, http.StatusOK, map[string]any{"branding": s.brandInfo(r.Context())})
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
	writeJSON(w, http.StatusOK, map[string]any{"branding": s.brandInfo(r.Context())})
}

// SVG 里「只有 viewBox、没有 width/height」的情况。
//
// iOS Safari 的坑：这种 SVG 放进 <img> 时，WebKit 拿不到固有尺寸，结果是空白或一个破图标；
// 桌面 Chrome/Firefox 会自己按 viewBox 算，所以只在手机上出问题。后台允许传 SVG，
// 那就在服务端补上绝对宽高，用户不用知道这件事。
var (
	svgOpenRe      = regexp.MustCompile(`(?is)<svg\b[^>]*>`)
	svgWidthAttrRe = regexp.MustCompile(`(?is)\s+width\s*=\s*("[^"]*"|'[^']*')`)
	svgHeightRe    = regexp.MustCompile(`(?is)\s+height\s*=\s*("[^"]*"|'[^']*')`)
)

// normalizeSVGSize 给缺尺寸的 SVG 补 width/height（按 viewBox 等比缩到 512 以内）。
func normalizeSVGSize(data []byte) []byte {
	loc := svgOpenRe.FindIndex(data)
	if loc == nil {
		return data
	}
	tag := string(data[loc[0]:loc[1]])
	if isAbsoluteLen(svgAttr(tag, "width")) && isAbsoluteLen(svgAttr(tag, "height")) {
		return data
	}

	w, h := 512, 512
	if parts := strings.FieldsFunc(svgAttr(tag, "viewBox"), func(r rune) bool {
		return r == ' ' || r == ',' || r == '\t'
	}); len(parts) == 4 {
		if vw, err := strconv.ParseFloat(parts[2], 64); err == nil && vw > 0 {
			if vh, err := strconv.ParseFloat(parts[3], 64); err == nil && vh > 0 {
				scale := 512 / math.Max(vw, vh)
				w, h = int(math.Round(vw*scale)), int(math.Round(vh*scale))
			}
		}
	}

	clean := svgWidthAttrRe.ReplaceAllString(tag, "")
	clean = svgHeightRe.ReplaceAllString(clean, "")
	idx := strings.LastIndex(clean, ">")
	if idx < 0 {
		return data
	}
	if idx > 0 && clean[idx-1] == '/' {
		idx--
	}
	clean = clean[:idx] + fmt.Sprintf(` width="%d" height="%d"`, w, h) + clean[idx:]

	out := append([]byte{}, data[:loc[0]]...)
	out = append(out, []byte(clean)...)
	out = append(out, data[loc[1]:]...)
	return out
}

// svgAttr 读一个属性值（单双引号都认）。
func svgAttr(tag, name string) string {
	re := regexp.MustCompile(`(?is)\s+` + name + `\s*=\s*"([^"]*)"|\s+` + name + `\s*=\s*'([^']*)'`)
	if m := re.FindStringSubmatch(tag); m != nil {
		if m[1] != "" {
			return strings.TrimSpace(m[1])
		}
		return strings.TrimSpace(m[2])
	}
	return ""
}

// isAbsoluteLen 只看「是不是纯数字」：百分比与 em 在 Safari 里同样拿不到固有尺寸。
func isAbsoluteLen(v string) bool {
	if v == "" || strings.ContainsAny(v, "%") {
		return false
	}
	_, err := strconv.ParseFloat(strings.TrimSuffix(strings.ToLower(v), "px"), 64)
	return err == nil
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
