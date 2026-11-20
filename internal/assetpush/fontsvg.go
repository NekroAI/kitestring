package assetpush

import (
	"path/filepath"
	"sort"
	"strings"

	"github.com/NekroAI/kitestring/internal/assetfmt"
	"github.com/NekroAI/kitestring/internal/protocol"
)

// 字体与 SVG（R-387）：字体（.ttf / .otf / .woff2）是单独一类素材，网页上显示字形预览；SVG 归独立的图，网页上当图片显示。
// 两类都按内容校验（字体查文件头和表目录，SVG 查是不是根元素为 <svg> 的 XML），不只看扩展名。

// checkFontSVG 校验字体 / SVG 主文件的内容，错误和提示记进 c；是 SVG 时返回像素尺寸（width / height 或 viewBox）。
// 不是字体也不是 SVG 时 handled 为假，由调用方照常处理。
func checkFontSVG(file string, c *Check) (pixels []int, handled bool) {
	name := filepath.Base(file)
	switch assetfmt.Kind(file) {
	case "font":
		info, err := assetfmt.Font(file)
		if err != nil {
			c.Errors = append(c.Errors, "字体文件 "+name+" 校验不过："+err.Error())
			return nil, true
		}
		if want := fontExtMime[strings.ToLower(filepath.Ext(file))]; want != info.Mime {
			c.Warnings = append(c.Warnings, "字体 "+name+" 的扩展名和内容不符（内容是 "+fontLabel(info.Mime)+"）")
		}
		return nil, true
	case "svg":
		info, err := assetfmt.SVGFile(file)
		if err != nil {
			c.Errors = append(c.Errors, "SVG 文件 "+name+" 校验不过："+err.Error())
			return nil, true
		}
		if info.Script {
			c.Warnings = append(c.Warnings, "SVG "+name+" 里有脚本或事件属性；资源库只当图片显示，脚本不会执行")
		}
		return info.Pixels, true
	}
	return nil, false
}

var fontExtMime = map[string]string{".ttf": assetfmt.MimeTTF, ".otf": assetfmt.MimeOTF, ".woff2": assetfmt.MimeWOFF2}

func fontLabel(mime string) string {
	switch mime {
	case assetfmt.MimeTTF:
		return "TTF"
	case assetfmt.MimeOTF:
		return "OTF"
	}
	return "WOFF2"
}

// mainRoles 是 variants 里 files 按角色取主文件的顺序。
var mainRoles = []string{"model", "image", "texture", "audio", "sound", "font", "font_regular", "regular", "icon", "svg"}

// pickMainFile 从 files（角色 → 文件）里取主文件：先按 mainRoles；再取名字以 font 开头的角色（带 regular 的优先，
// 如 font_regular、font_bold 里取 font_regular）；再取第一个资源库收的文件类型（按角色名排序，跳过许可原文这类）；都没有取第一个。
func pickMainFile(files map[string]string) string {
	for _, role := range mainRoles {
		if f := files[role]; f != "" {
			return f
		}
	}
	keys := make([]string, 0, len(files))
	for k := range files {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	var font string
	for _, k := range keys {
		if strings.HasPrefix(strings.ToLower(k), "font") && files[k] != "" {
			if strings.Contains(strings.ToLower(k), "regular") {
				return files[k]
			}
			if font == "" {
				font = files[k]
			}
		}
	}
	if font != "" {
		return font
	}
	for _, k := range keys {
		if fileKind(files[k]) != "" {
			return files[k]
		}
	}
	if len(keys) > 0 {
		return files[keys[0]]
	}
	return ""
}

// kindLabel 是校验报告里的类型：GLB / 图片（SVG 写明）/ 音效 / 字体。
func kindLabel(kind, file string) string {
	switch kind {
	case protocol.AssetPart:
		return "GLB"
	case protocol.AssetImage:
		if assetfmt.Kind(file) == "svg" {
			return "图片 · SVG"
		}
		return "图片"
	case protocol.AssetAudio:
		return "音效"
	case protocol.AssetFont:
		return "字体 · " + strings.ToUpper(strings.TrimPrefix(filepath.Ext(file), "."))
	}
	return ""
}
