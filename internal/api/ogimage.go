package api

import (
	"bytes"
	"context"
	"image"
	"image/color"
	_ "image/jpeg"
	"image/png"
	_ "image/png"
	"math"
	"net/http"
	"os"
	"path/filepath"
	"sync"
)

// 分享卡片大图。
//
// 为什么运行时画：仓库里放一张 1200×630 的静态图，改一次品牌色就得重新做图；
// 而且后台换了图标之后，卡片还挂着旧 logo。这里现画——渐变底 + 用户上传的图标，
// 没传图标就画 favicon 那个 V 折线。结果按图标版本缓存，不会每次请求都画。
var (
	ogMu   sync.Mutex
	ogKey  string
	ogData []byte
)

const ogETagPrefix = `"og-`

func (s *Server) handleOGImage(w http.ResponseWriter, r *http.Request) {
	data, etag := s.ogImage(r.Context())
	w.Header().Set("Content-Type", "image/png")
	w.Header().Set("Cache-Control", "public, max-age=604800")
	w.Header().Set("ETag", etag)
	if r.Header.Get("If-None-Match") == etag {
		w.WriteHeader(http.StatusNotModified)
		return
	}
	_, _ = w.Write(data)
}

// ogImage 返回当前应当展示的卡片图与它的 ETag（版本号变了图就变了）。
func (s *Server) ogImage(ctx context.Context) ([]byte, string) {
	name, ver, hasIcon := s.brandFile(ctx)
	key := ver
	if hasIcon {
		key += "/" + name
	}
	ogMu.Lock()
	defer ogMu.Unlock()
	if ogKey == key && ogData != nil {
		return ogData, ogETagPrefix + key + `"`
	}
	var icon image.Image
	if hasIcon {
		// 只解 PNG/JPEG：SVG/WebP 标准库解不了，解不了就退回画折线，不能因此 500。
		if f, err := os.Open(filepath.Join(s.brandDir(), name)); err == nil {
			icon, _, _ = image.Decode(f)
			f.Close()
		}
	}
	ogData = renderOG(1200, 630, icon)
	ogKey = key
	return ogData, ogETagPrefix + key + `"`
}

// renderOG 画一张卡片图：对角渐变底 + 细点阵 + 品牌图标（没有就画 V 折线）。
func renderOG(width, height int, icon image.Image) []byte {
	img := image.NewRGBA(image.Rect(0, 0, width, height))

	from := color.RGBA{0x10, 0x1c, 0x1b, 0xff}
	to := color.RGBA{0x2f, 0x4f, 0x4f, 0xff}
	for y := 0; y < height; y++ {
		for x := 0; x < width; x++ {
			t := (float64(x)/float64(width) + float64(y)/float64(height)) / 2
			img.SetRGBA(x, y, mixRGBA(from, to, t))
		}
	}

	// 极淡的点阵：大片纯色在小尺寸卡片里容易显得像加载失败。
	ink := color.RGBA{0xf8, 0xf8, 0xf0, 0xff}
	for y := 34; y < height; y += 46 {
		for x := 34; x < width; x += 46 {
			blendPixel(img, x, y, ink, 0.06)
		}
	}

	if icon != nil {
		drawCentered(img, icon, 300)
	} else {
		// 与 favicon 同形的 V：两条圆头粗线。
		strokeSegment(img, 470, 168, 600, 452, 38, ink)
		strokeSegment(img, 600, 452, 730, 168, 38, ink)
	}

	var buf bytes.Buffer
	_ = png.Encode(&buf, img)
	return buf.Bytes()
}

func mixRGBA(a, b color.RGBA, t float64) color.RGBA {
	if t < 0 {
		t = 0
	} else if t > 1 {
		t = 1
	}
	mix := func(x, y uint8) uint8 { return uint8(float64(x) + (float64(y)-float64(x))*t) }
	return color.RGBA{mix(a.R, b.R), mix(a.G, b.G), mix(a.B, b.B), 0xff}
}

func blendPixel(img *image.RGBA, x, y int, c color.RGBA, alpha float64) {
	if !image.Pt(x, y).In(img.Rect) || alpha <= 0 {
		return
	}
	if alpha > 1 {
		alpha = 1
	}
	dst := img.RGBAAt(x, y)
	img.SetRGBA(x, y, color.RGBA{
		R: uint8(float64(dst.R)*(1-alpha) + float64(c.R)*alpha),
		G: uint8(float64(dst.G)*(1-alpha) + float64(c.G)*alpha),
		B: uint8(float64(dst.B)*(1-alpha) + float64(c.B)*alpha),
		A: 0xff,
	})
}

// strokeSegment 画一条带圆头的粗线。按「到线段的距离」逐像素上色，
// 圆头是免费的，边缘用 1px 过渡做抗锯齿。
func strokeSegment(img *image.RGBA, x0, y0, x1, y1, radius float64, c color.RGBA) {
	minX := int(math.Floor(math.Min(x0, x1) - radius - 1))
	maxX := int(math.Ceil(math.Max(x0, x1) + radius + 1))
	minY := int(math.Floor(math.Min(y0, y1) - radius - 1))
	maxY := int(math.Ceil(math.Max(y0, y1) + radius + 1))
	for y := minY; y <= maxY; y++ {
		for x := minX; x <= maxX; x++ {
			d := distToSegment(float64(x)+0.5, float64(y)+0.5, x0, y0, x1, y1)
			blendPixel(img, x, y, c, radius-d+0.5)
		}
	}
}

func distToSegment(px, py, x0, y0, x1, y1 float64) float64 {
	dx, dy := x1-x0, y1-y0
	if dx == 0 && dy == 0 {
		return math.Hypot(px-x0, py-y0)
	}
	t := ((px-x0)*dx + (py-y0)*dy) / (dx*dx + dy*dy)
	if t < 0 {
		t = 0
	} else if t > 1 {
		t = 1
	}
	return math.Hypot(px-(x0+t*dx), py-(y0+t*dy))
}

// drawCentered 把图标按比例缩进 maxSide 的方框并居中。双线性采样，够用。
func drawCentered(img *image.RGBA, src image.Image, maxSide int) {
	sb := src.Bounds()
	sw, sh := sb.Dx(), sb.Dy()
	if sw <= 0 || sh <= 0 {
		return
	}
	scale := math.Min(float64(maxSide)/float64(sw), float64(maxSide)/float64(sh))
	dw := int(float64(sw) * scale)
	dh := int(float64(sh) * scale)
	ox := (img.Rect.Dx() - dw) / 2
	oy := (img.Rect.Dy() - dh) / 2
	for y := 0; y < dh; y++ {
		for x := 0; x < dw; x++ {
			sx := sb.Min.X + int(float64(x)/scale)
			sy := sb.Min.Y + int(float64(y)/scale)
			if sx >= sb.Max.X {
				sx = sb.Max.X - 1
			}
			if sy >= sb.Max.Y {
				sy = sb.Max.Y - 1
			}
			r, g, b, a := src.At(sx, sy).RGBA()
			if a == 0 {
				continue // 透明像素不覆盖底图
			}
			blendPixel(img, ox+x, oy+y, color.RGBA{uint8(r >> 8), uint8(g >> 8), uint8(b >> 8), 0xff}, float64(a)/65535)
		}
	}
}
