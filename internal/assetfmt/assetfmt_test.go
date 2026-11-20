package assetfmt

import (
	"strings"
	"testing"
)

func TestFont(t *testing.T) {
	for _, m := range []string{MimeTTF, MimeOTF, MimeWOFF2} {
		b := SynthFont(m)
		if got := FontMime(b); got != m {
			t.Fatalf("%s: FontMime = %q", m, got)
		}
		info, err := FontBytes(b)
		if err != nil || info.Mime != m {
			t.Fatalf("%s: %v %+v", m, err, info)
		}
	}
	// 只看扩展名会放过的：PNG 改名 .ttf
	if _, err := FontBytes([]byte("\x89PNG\r\n\x1a\n0000000000000000")); err == nil {
		t.Fatal("PNG 不该当字体")
	}
	// 文件头对、结构坏
	ttf := SynthFont(MimeTTF)
	if _, err := FontBytes(ttf[:len(ttf)-20]); err == nil || !strings.Contains(err.Error(), "超出文件范围") {
		t.Fatalf("截断的 TTF：%v", err)
	}
	bad := append([]byte{}, ttf...)
	copy(bad[12:], "xxxx") // 第一张表 cmap 改名，缺 cmap
	if _, err := FontBytes(bad); err == nil || !strings.Contains(err.Error(), "cmap") {
		t.Fatalf("缺 cmap：%v", err)
	}
	if _, err := FontBytes([]byte("\x00\x01\x00\x00\x00\x00")); err == nil {
		t.Fatal("太短")
	}
	w := SynthFont(MimeWOFF2)
	if _, err := FontBytes(append(w, 0)); err == nil || !strings.Contains(err.Error(), "不一致") {
		t.Fatalf("WOFF2 长度不符：%v", err)
	}
	otf := SynthFont(MimeOTF)
	copy(otf[12:], "XFF ")
	if _, err := FontBytes(otf); err == nil || !strings.Contains(err.Error(), "CFF") {
		t.Fatalf("OTF 缺 CFF：%v", err)
	}
}

func TestSVG(t *testing.T) {
	ok := []struct {
		src string
		px  []int
	}{
		{`<svg xmlns="http://www.w3.org/2000/svg" width="64" height="32"><rect/></svg>`, []int{64, 32}},
		{"\ufeff<?xml version=\"1.0\"?>\n<!-- icon -->\n<!DOCTYPE svg>\n<svg xmlns=\"http://www.w3.org/2000/svg\" viewBox=\"0 0 24 12\"/>", []int{24, 12}},
		{`<svg xmlns="http://www.w3.org/2000/svg" width="100%" viewBox="0,0,10,20"></svg>`, []int{10, 20}},
		{`<svg xmlns="http://www.w3.org/2000/svg"></svg>`, nil},
	}
	for _, c := range ok {
		if !LooksSVG([]byte(c.src)) {
			t.Fatalf("LooksSVG 认不出：%s", c.src)
		}
		info, err := SVG(strings.NewReader(c.src))
		if err != nil || info.Script || len(info.Pixels) != len(c.px) || (len(c.px) == 2 && (info.Pixels[0] != c.px[0] || info.Pixels[1] != c.px[1])) {
			t.Fatalf("%s: %v %+v", c.src, err, info)
		}
	}
	bad := map[string]string{
		`<html><svg xmlns="http://www.w3.org/2000/svg"/></html>`:                        "根元素",
		`<svg width="1"></svg>`:                                                         "xmlns",
		`<svg xmlns="http://www.w3.org/2000/svg"><g></svg>`:                             "XML",
		`<?xml version="1.0"?><!-- 只有注释 -->`:                                            "没有 <svg>",
		`<?xml version="1.0" encoding="GBK"?><svg xmlns="http://www.w3.org/2000/svg"/>`: "UTF-8",
	}
	for src, want := range bad {
		if _, err := SVG(strings.NewReader(src)); err == nil || !strings.Contains(err.Error(), want) {
			t.Fatalf("%s：要报 %s，得 %v", src, want, err)
		}
	}
	if LooksSVG([]byte("\x89PNG\r\n")) || LooksSVG([]byte("<html><body>")) || LooksSVG([]byte(`<?xml version="1.0"?><note/>`)) {
		t.Fatal("不是 SVG 的认成了 SVG")
	}
	info, err := SVG(strings.NewReader(ScriptSVG))
	if err != nil || !info.Script || info.Pixels[0] != 64 {
		t.Fatalf("带脚本的 SVG：%v %+v", err, info)
	}
	if Kind("a/B.WOFF2") != "font" || Kind("x.svg") != "svg" || Kind("x.png") != "" {
		t.Fatal("Kind")
	}
}
