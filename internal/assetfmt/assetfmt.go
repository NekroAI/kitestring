// Package assetfmt 校验资源库的字体（TTF / OTF / WOFF2）和 SVG 素材（R-387）：看文件头和结构，不只看扩展名。
// 推送端（internal/assetpush）推之前用它逐件校验；Hub 收文件时再查一遍，认出的类型存进附件的 mime。
package assetfmt

import (
	"bytes"
	"encoding/binary"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

const (
	MimeTTF   = "font/ttf"
	MimeOTF   = "font/otf"
	MimeWOFF2 = "font/woff2"
	MimeSVG   = "image/svg+xml"
)

// FontExts / SVGExts 是推送端按扩展名认的字体 / SVG 文件；扩展名只用来分类，内容另外校验。
var (
	FontExts = map[string]bool{".ttf": true, ".otf": true, ".woff2": true}
	SVGExts  = map[string]bool{".svg": true}
)

// IsFontMime：资源库收的字体类型。
func IsFontMime(m string) bool { return m == MimeTTF || m == MimeOTF || m == MimeWOFF2 }

// FontMime 按文件头认字体：TrueType（00 01 00 00 或 true）、CFF 的 OpenType（OTTO）、WOFF2（wOF2）；别的返回空。
func FontMime(head []byte) string {
	if len(head) < 4 {
		return ""
	}
	switch string(head[:4]) {
	case "\x00\x01\x00\x00", "true":
		return MimeTTF
	case "OTTO":
		return MimeOTF
	case "wOF2":
		return MimeWOFF2
	}
	return ""
}

// FontInfo 是字体文件读出的信息。
type FontInfo struct {
	Mime   string
	Tables int
}

// Font 读字体文件并校验结构：TTF / OTF 查表目录（表数合理、每张表在文件范围内、有 cmap / head 等必需表），
// WOFF2 查文件头（声明的长度与文件大小一致、表数合理）。
func Font(path string) (FontInfo, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return FontInfo{}, err
	}
	return FontBytes(b)
}

// FontBytes 同 Font，输入是整个文件内容。
func FontBytes(b []byte) (FontInfo, error) {
	m := FontMime(b)
	switch m {
	case MimeTTF, MimeOTF:
		return sfnt(b, m)
	case MimeWOFF2:
		return woff2(b)
	}
	return FontInfo{}, errors.New("不是 TTF、OTF 或 WOFF2 字体（文件头不对）")
}

var be = binary.BigEndian

func sfnt(b []byte, m string) (FontInfo, error) {
	if len(b) < 12 {
		return FontInfo{}, errors.New("字体文件太短")
	}
	n := int(be.Uint16(b[4:6]))
	if n == 0 || n > 200 {
		return FontInfo{}, fmt.Errorf("字体的表数不合理（%d）", n)
	}
	if len(b) < 12+16*n {
		return FontInfo{}, errors.New("字体的表目录不完整")
	}
	tags := map[string]bool{}
	for i := range n {
		r := b[12+16*i:]
		tag := string(r[:4])
		off, ln := int64(be.Uint32(r[8:12])), int64(be.Uint32(r[12:16]))
		if off+ln > int64(len(b)) {
			return FontInfo{}, fmt.Errorf("字体的 %s 表超出文件范围（文件可能不完整）", strings.TrimSpace(tag))
		}
		tags[tag] = true
	}
	need := []string{"cmap", "head", "hhea", "hmtx", "maxp", "name"}
	if m == MimeTTF {
		need = append(need, "glyf", "loca")
	} else if !tags["CFF "] && !tags["CFF2"] {
		return FontInfo{}, errors.New("OTF 字体缺少 CFF 表")
	}
	var miss []string
	for _, t := range need {
		if !tags[t] {
			miss = append(miss, t)
		}
	}
	if len(miss) > 0 {
		return FontInfo{}, errors.New("字体缺少必需的表：" + strings.Join(miss, "、"))
	}
	return FontInfo{Mime: m, Tables: n}, nil
}

func woff2(b []byte) (FontInfo, error) {
	if len(b) < 48 {
		return FontInfo{}, errors.New("WOFF2 文件太短")
	}
	flavor := string(b[4:8])
	if flavor != "\x00\x01\x00\x00" && flavor != "OTTO" && flavor != "true" && flavor != "ttcf" {
		return FontInfo{}, errors.New("WOFF2 里的字体类型不认识")
	}
	if l := be.Uint32(b[8:12]); int(l) != len(b) {
		return FontInfo{}, fmt.Errorf("WOFF2 声明的长度（%d 字节）和文件大小（%d 字节）不一致", l, len(b))
	}
	n := int(be.Uint16(b[12:14]))
	if n == 0 || n > 200 {
		return FontInfo{}, fmt.Errorf("字体的表数不合理（%d）", n)
	}
	if be.Uint16(b[14:16]) != 0 {
		return FontInfo{}, errors.New("WOFF2 文件头不对")
	}
	if c := be.Uint32(b[20:24]); c == 0 || int(c) > len(b) {
		return FontInfo{}, errors.New("WOFF2 的压缩数据长度不对")
	}
	return FontInfo{Mime: MimeWOFF2, Tables: n}, nil
}

// SVGInfo 是 SVG 读出的信息。Pixels 取 width / height（纯数字或 px），没有时取 viewBox 的宽高；都没有为空。
// Script：文件里有 <script>、on* 事件属性或 javascript: 链接——网页上只当图片显示（<img> 加载），不会执行，推送时提示一句。
type SVGInfo struct {
	Pixels []int
	Script bool
}

// MaxSVG 是校验时读的上限：SVG 是文本，比这还大的多半不是图标、插画。
const MaxSVG = 32 << 20

// LooksSVG 按文件开头粗认 SVG（Hub 收文件时先用它分类，存下后再用 SVG 完整校验）：
// 去掉 BOM、空白后以 <?xml、<!--、<!DOCTYPE svg 或 <svg 开头，且开头这段里有 <svg。
func LooksSVG(head []byte) bool {
	s := bytes.TrimPrefix(head, []byte("\xef\xbb\xbf"))
	s = bytes.TrimLeft(s, " \t\r\n")
	low := bytes.ToLower(s)
	if !(bytes.HasPrefix(low, []byte("<?xml")) || bytes.HasPrefix(low, []byte("<!--")) || bytes.HasPrefix(low, []byte("<!doctype svg")) || bytes.HasPrefix(low, []byte("<svg"))) {
		return false
	}
	return bytes.Contains(low, []byte("<svg"))
}

// SVGFile 读并校验 SVG 文件。
func SVGFile(path string) (SVGInfo, error) {
	f, err := os.Open(path)
	if err != nil {
		return SVGInfo{}, err
	}
	defer f.Close()
	return SVG(f)
}

// SVG 校验 SVG：是格式正确的 XML，根元素是 svg（命名空间 http://www.w3.org/2000/svg）。
func SVG(r io.Reader) (SVGInfo, error) {
	var info SVGInfo
	d := xml.NewDecoder(io.LimitReader(r, MaxSVG))
	d.Strict = true
	d.CharsetReader = func(label string, in io.Reader) (io.Reader, error) {
		if l := strings.ToLower(label); l == "utf-8" || l == "us-ascii" || l == "ascii" {
			return in, nil
		}
		return nil, fmt.Errorf("只认 UTF-8 编码的 SVG（文件写的是 %s）", label)
	}
	root := false
	for {
		tok, err := d.Token()
		if err == io.EOF {
			break
		}
		if err != nil {
			return info, errors.New("SVG 不是格式正确的 XML：" + err.Error())
		}
		se, ok := tok.(xml.StartElement)
		if !ok {
			continue
		}
		if !root {
			if se.Name.Local != "svg" {
				return info, fmt.Errorf("根元素是 <%s>，不是 <svg>", se.Name.Local)
			}
			if se.Name.Space != "http://www.w3.org/2000/svg" {
				return info, errors.New(`<svg> 缺少 xmlns="http://www.w3.org/2000/svg"`)
			}
			root = true
			info.Pixels = svgSize(se.Attr)
		}
		if strings.EqualFold(se.Name.Local, "script") {
			info.Script = true
		}
		for _, a := range se.Attr {
			k := strings.ToLower(a.Name.Local)
			if strings.HasPrefix(k, "on") || ((k == "href") && strings.HasPrefix(strings.ToLower(strings.TrimSpace(a.Value)), "javascript:")) {
				info.Script = true
			}
		}
	}
	if !root {
		return info, errors.New("文件里没有 <svg> 元素")
	}
	return info, nil
}

func svgSize(attrs []xml.Attr) []int {
	var w, h, vb string
	for _, a := range attrs {
		switch a.Name.Local {
		case "width":
			w = a.Value
		case "height":
			h = a.Value
		case "viewBox":
			vb = a.Value
		}
	}
	px := func(s string) (float64, bool) {
		s = strings.TrimSuffix(strings.TrimSpace(s), "px")
		v, err := strconv.ParseFloat(s, 64)
		return v, err == nil && v > 0 && v < 1e6
	}
	if x, ok := px(w); ok {
		if y, ok := px(h); ok {
			return []int{int(x + 0.5), int(y + 0.5)}
		}
	}
	f := strings.FieldsFunc(vb, func(r rune) bool { return r == ' ' || r == ',' || r == '\t' || r == '\n' })
	if len(f) == 4 {
		x, ok1 := px(f[2])
		y, ok2 := px(f[3])
		if ok1 && ok2 {
			return []int{int(x + 0.5), int(y + 0.5)}
		}
	}
	return nil
}

// Kind 按扩展名分：font / svg / 空。
func Kind(file string) string {
	ext := strings.ToLower(filepath.Ext(file))
	switch {
	case FontExts[ext]:
		return "font"
	case SVGExts[ext]:
		return "svg"
	}
	return ""
}
