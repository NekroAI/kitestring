package hub

import (
	"bytes"
	"fmt"
	"image"
	"image/color"
	"image/jpeg"
	"image/png"
	"os"
	"path/filepath"
	"strconv"
	"sync"
)

// 缩略图（R-246）：GET /api/files/{id}?w=480 返回宽 480 / 960 / 1920 的缩小版，首次请求时生成并落盘到附件目录的 thumbs/ 下，
// 原图不动、历史图片自然能用上。宽度只认固定档位（取不小于请求值的最近一档），避免被随意刷出一堆缓存。
// 纯 Go 没有 WebP 编码器（golang.org/x/image 只能解码），也不引入 cgo：不透明的图出 JPEG，带透明的出 PNG。
// 只处理 PNG / JPEG；GIF（可能是动图）、WebP（标准库解不了）、解码失败、转码后不比原图小，一律回落原图（原图不宽于档位时不放大，按原宽度转码）。

var thumbWidths = []int{480, 960, 1920}

// thumbWidth 把请求的宽度归到档位；非法值返回 0（按原图）。
func thumbWidth(q string) int {
	n, err := strconv.Atoi(q)
	if err != nil || n <= 0 {
		return 0
	}
	for _, w := range thumbWidths {
		if n <= w {
			return w
		}
	}
	return thumbWidths[len(thumbWidths)-1]
}

var thumbSem = make(chan struct{}, 2) // 同时最多生成 2 张，避免首次打开满是大图的页面时内存冲高
var thumbMu sync.Mutex
var thumbBusy = map[string]*sync.Mutex{}

func (h *Hub) thumbDir() string { return filepath.Join(h.filesDir, "thumbs") }

// thumbFile 返回缩略图的路径与类型；生成不了（需要回落原图）时 ok 为 false。
func (h *Hub) thumbFile(id, mime string, w int) (path, ctype string, ok bool) {
	if mime != "image/png" && mime != "image/jpeg" {
		return "", "", false
	}
	base := filepath.Join(h.thumbDir(), fmt.Sprintf("%s-%d", id, w))
	for _, ext := range []string{".jpg", ".png", ".none"} {
		if _, err := os.Stat(base + ext); err == nil {
			switch ext {
			case ".jpg":
				return base + ext, "image/jpeg", true
			case ".png":
				return base + ext, "image/png", true
			}
			return "", "", false // 之前判定过不用缩（原图不宽于档位或解码失败）
		}
	}
	// 同一张图同一档只生成一次
	thumbMu.Lock()
	mu := thumbBusy[base]
	if mu == nil {
		mu = &sync.Mutex{}
		thumbBusy[base] = mu
	}
	thumbMu.Unlock()
	mu.Lock()
	defer func() {
		mu.Unlock()
		thumbMu.Lock()
		delete(thumbBusy, base)
		thumbMu.Unlock()
	}()
	for _, ext := range []string{".jpg", ".png"} {
		if _, err := os.Stat(base + ext); err == nil {
			return h.thumbFile(id, mime, w)
		}
	}
	thumbSem <- struct{}{}
	defer func() { <-thumbSem }()
	data, err := os.ReadFile(h.filePath(id))
	if err != nil {
		return "", "", false
	}
	out, ext := makeThumb(data, w)
	_ = os.MkdirAll(h.thumbDir(), 0o700)
	if out == nil {
		_ = os.WriteFile(base+".none", nil, 0o600)
		return "", "", false
	}
	tmp, err := os.CreateTemp(h.thumbDir(), "tmp-*")
	if err != nil {
		return "", "", false
	}
	_, werr := tmp.Write(out)
	tmp.Close()
	if werr != nil || os.Rename(tmp.Name(), base+ext) != nil {
		os.Remove(tmp.Name())
		return "", "", false
	}
	if ext == ".jpg" {
		return base + ext, "image/jpeg", true
	}
	return base + ext, "image/png", true
}

// makeThumb 把 PNG / JPEG 缩到宽 w；不需要缩或失败返回 nil。
func makeThumb(data []byte, w int) ([]byte, string) {
	cfg, _, err := image.DecodeConfig(bytes.NewReader(data))
	if err != nil || cfg.Width < 1 || cfg.Width*cfg.Height > 100<<20 {
		return nil, ""
	}
	// 原图不宽于档位：不放大，按原宽度重新编码；转码后不比原图小就回落原图
	w = min(w, cfg.Width)
	src, _, err := image.Decode(bytes.NewReader(data))
	if err != nil {
		return nil, ""
	}
	dst := downscale(src, w)
	var buf bytes.Buffer
	ext := ".png"
	if dst.Opaque() {
		ext = ".jpg"
		if jpeg.Encode(&buf, dst, &jpeg.Options{Quality: 85}) != nil {
			return nil, ""
		}
	} else if png.Encode(&buf, dst) != nil {
		return nil, ""
	}
	if buf.Len() >= len(data) {
		return nil, ""
	}
	return buf.Bytes(), ext
}

// downscale 用面积平均把图缩到宽 w，高度等比。
func downscale(src image.Image, w int) *image.NRGBA {
	b := src.Bounds()
	sw, sh := b.Dx(), b.Dy()
	h := max(1, sh*w/sw)
	dst := image.NewNRGBA(image.Rect(0, 0, w, h))
	for y := 0; y < h; y++ {
		y0, y1 := y*sh/h, max(y*sh/h+1, (y+1)*sh/h)
		for x := 0; x < w; x++ {
			x0, x1 := x*sw/w, max(x*sw/w+1, (x+1)*sw/w)
			var r, g, bl, a, n uint64
			for yy := y0; yy < y1; yy++ {
				for xx := x0; xx < x1; xx++ {
					cr, cg, cb, ca := src.At(b.Min.X+xx, b.Min.Y+yy).RGBA()
					r, g, bl, a, n = r+uint64(cr), g+uint64(cg), bl+uint64(cb), a+uint64(ca), n+1
				}
			}
			// 累加的是预乘值，换回非预乘
			a /= n
			if a == 0 {
				continue
			}
			dst.SetNRGBA(x, y, color.NRGBA{
				R: uint8(min(0xffff, r/n*0xffff/a) >> 8),
				G: uint8(min(0xffff, g/n*0xffff/a) >> 8),
				B: uint8(min(0xffff, bl/n*0xffff/a) >> 8),
				A: uint8(a >> 8),
			})
		}
	}
	return dst
}
