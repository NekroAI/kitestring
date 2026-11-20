package assetfmt

import "encoding/binary"

// 测试用的合成字体（各包的单测共用）：只有表目录和占位表数据，够过结构校验，不能真的渲染。

// SynthFont 生成 mime 对应的字体文件：font/ttf、font/otf 或 font/woff2。
func SynthFont(mime string) []byte {
	if mime == MimeWOFF2 {
		b := make([]byte, 64)
		copy(b, "wOF2")
		copy(b[4:], "\x00\x01\x00\x00")
		binary.BigEndian.PutUint32(b[8:], uint32(len(b)))
		binary.BigEndian.PutUint16(b[12:], 9)
		binary.BigEndian.PutUint32(b[16:], 4096)
		binary.BigEndian.PutUint32(b[20:], 16)
		return b
	}
	sig, tags := "\x00\x01\x00\x00", []string{"cmap", "glyf", "head", "hhea", "hmtx", "loca", "maxp", "name", "post"}
	if mime == MimeOTF {
		sig, tags = "OTTO", []string{"CFF ", "cmap", "head", "hhea", "hmtx", "maxp", "name", "post"}
	}
	n := len(tags)
	b := make([]byte, 12+16*n)
	copy(b, sig)
	binary.BigEndian.PutUint16(b[4:], uint16(n))
	for i, tag := range tags {
		r := b[12+16*i:]
		copy(r, tag)
		binary.BigEndian.PutUint32(r[8:], uint32(len(b)+8*i))
		binary.BigEndian.PutUint32(r[12:], 8)
	}
	return append(b, make([]byte, 8*n)...)
}

// ScriptSVG 是带脚本的 SVG（<script>、onload、javascript: 链接）：校验能过并标出有脚本；网页上当图片显示时一律不执行。
// 脚本执行了会把页面标题改成 PWNED，并请求 /pwned。
const ScriptSVG = `<?xml version="1.0" encoding="UTF-8"?>
<svg xmlns="http://www.w3.org/2000/svg" xmlns:xlink="http://www.w3.org/1999/xlink" width="64" height="48" onload="document.title='PWNED';fetch('/pwned')">
  <script>document.title='PWNED';fetch('/pwned')</script>
  <a xlink:href="javascript:document.title='PWNED'"><rect width="64" height="48" fill="#3a7"/></a>
</svg>`
