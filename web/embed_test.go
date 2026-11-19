package web

import (
	"net/http/httptest"
	"testing"
	"testing/fstest"
)

func TestAcceptsBrotli(t *testing.T) {
	for in, want := range map[string]bool{"gzip, deflate, br": true, "br;q=0": false, "gzip": false, "BR;q=0.5": true, "": false, "zstd, br, gzip": true} {
		if got := acceptsBrotli(in); got != want {
			t.Errorf("acceptsBrotli(%q) = %v", in, got)
		}
	}
}

// 静态文件（R-369）：浏览器接受 br 且有预压缩文件时返回 .br；Range 请求、不接受 br 时交回原处理。
func TestServeBrotli(t *testing.T) {
	sub := fstest.MapFS{"assets/a.js": {Data: []byte("export{}")}, "assets/a.js.br": {Data: []byte("BR")}}
	rec := httptest.NewRecorder()
	req := httptest.NewRequest("GET", "/assets/a.js", nil)
	req.Header.Set("Accept-Encoding", "gzip, br")
	if !serveBrotli(rec, req, sub, "assets/a.js") || rec.Body.String() != "BR" || rec.Header().Get("Content-Encoding") != "br" || rec.Header().Get("Vary") != "Accept-Encoding" {
		t.Fatalf("应返回预压缩文件：%v %q", rec.Header(), rec.Body.String())
	}
	req.Header.Set("Range", "bytes=0-1")
	if serveBrotli(httptest.NewRecorder(), req, sub, "assets/a.js") {
		t.Fatal("Range 请求不用 br")
	}
	req = httptest.NewRequest("GET", "/assets/a.js", nil)
	req.Header.Set("Accept-Encoding", "gzip")
	if serveBrotli(httptest.NewRecorder(), req, sub, "assets/a.js") {
		t.Fatal("不接受 br 时不用")
	}
	req.Header.Set("Accept-Encoding", "br")
	if serveBrotli(httptest.NewRecorder(), req, sub, "assets/b.js") {
		t.Fatal("没有 .br 时不用")
	}
}
