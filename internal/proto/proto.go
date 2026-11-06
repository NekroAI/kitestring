// Package proto 是可交互原型包（R-197）：Agent 交的 HTML 页面加图片等资源打成的 zip。
// 本机侧把目录 / 单个 .html / .zip 打成包并自检（ks proto check、MCP 交原型前）；hub 侧校验包、找入口、按路径读文件。
package proto

import (
	"archive/zip"
	"bytes"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
)

// 默认上限：zip 包 20 MB（设置里可调）、最多 500 个文件、解压后 100 MB。
const (
	DefaultMaxMB = 20
	MaxFiles     = 500
	MaxUnpacked  = 100 << 20
)

// Info 是校验后的包信息。
type Info struct {
	Entry    string   // 入口页面（包里的路径）
	Files    int      // 文件数（不含目录）
	Unpacked int64    // 解压后总大小
	External []string // 引用的外部地址（去重，最多 20 个），只提示不拦
}

// Validate 校验 zip：文件数、解压后大小、路径安全，确定入口。entry 为空时自动找：
// 根目录的 index.html → 根目录唯一的 .html → 唯一一个顶层目录里的 index.html。
func Validate(zr *zip.Reader, entry string) (Info, error) {
	var info Info
	var htmls []string
	names := map[string]bool{}
	for _, f := range zr.File {
		if f.FileInfo().IsDir() {
			continue
		}
		n := f.Name
		if !SafeName(n) {
			return info, fmt.Errorf("包里有不安全的路径：%s", n)
		}
		info.Files++
		info.Unpacked += int64(f.UncompressedSize64)
		names[n] = true
		if IsHTML(n) {
			htmls = append(htmls, n)
		}
	}
	if info.Files == 0 {
		return info, errors.New("原型包是空的")
	}
	if info.Files > MaxFiles {
		return info, fmt.Errorf("原型包最多 %d 个文件，这个有 %d 个", MaxFiles, info.Files)
	}
	if info.Unpacked > MaxUnpacked {
		return info, fmt.Errorf("原型包解压后不能超过 %d MB，这个有 %.1f MB", MaxUnpacked>>20, float64(info.Unpacked)/(1<<20))
	}
	entry = strings.TrimPrefix(strings.TrimSpace(entry), "./")
	if entry != "" {
		if !names[entry] {
			return info, fmt.Errorf("入口 %s 不在包里", entry)
		}
		if !IsHTML(entry) {
			return info, fmt.Errorf("入口要是 .html 页面：%s", entry)
		}
		info.Entry = entry
	} else {
		info.Entry = findEntry(names, htmls)
		if info.Entry == "" {
			return info, errors.New("找不到入口页面：根目录放 index.html，或用 entry 指定")
		}
	}
	for _, f := range zr.File {
		if f.FileInfo().IsDir() || !(IsHTML(f.Name) || strings.HasSuffix(strings.ToLower(f.Name), ".css")) || f.UncompressedSize64 > 4<<20 {
			continue
		}
		rc, err := f.Open()
		if err != nil {
			continue
		}
		b, _ := io.ReadAll(io.LimitReader(rc, 4<<20))
		rc.Close()
		info.External = appendExternal(info.External, b)
	}
	return info, nil
}

func findEntry(names map[string]bool, htmls []string) string {
	if names["index.html"] {
		return "index.html"
	}
	var root []string
	for _, h := range htmls {
		if !strings.Contains(h, "/") {
			root = append(root, h)
		}
	}
	if len(root) == 1 {
		return root[0]
	}
	tops := map[string]bool{}
	for n := range names {
		if i := strings.Index(n, "/"); i > 0 {
			tops[n[:i]] = true
		} else {
			tops[""] = true
		}
	}
	if len(tops) == 1 {
		for t := range tops {
			if t != "" && names[t+"/index.html"] {
				return t + "/index.html"
			}
		}
	}
	return ""
}

// SafeName：包里的路径不能是绝对路径、不能含 ..、反斜杠或空段。
func SafeName(n string) bool {
	if n == "" || strings.HasPrefix(n, "/") || strings.Contains(n, "\\") || strings.ContainsRune(n, 0) {
		return false
	}
	for _, s := range strings.Split(n, "/") {
		if s == "" || s == "." || s == ".." {
			return false
		}
	}
	return true
}

// IsHTML 判断是不是 HTML 页面。
func IsHTML(n string) bool {
	e := strings.ToLower(path.Ext(n))
	return e == ".html" || e == ".htm"
}

var externalRe = regexp.MustCompile(`(?i)(?:src|href)\s*=\s*["']?(https?://[^"'\s>]+)|url\(\s*["']?(https?://[^"')\s]+)|@import\s+["'](https?://[^"']+)`)

func appendExternal(out []string, b []byte) []string {
	for _, m := range externalRe.FindAllSubmatch(b, -1) {
		u := ""
		for _, g := range m[1:] {
			if len(g) > 0 {
				u = string(g)
				break
			}
		}
		if u == "" || len(out) >= 20 {
			continue
		}
		dup := false
		for _, x := range out {
			if x == u {
				dup = true
				break
			}
		}
		if !dup {
			out = append(out, u)
		}
	}
	return out
}

// Lookup 在包里按网址路径找文件：空路径或以 / 结尾时找该目录下的 index.html。
func Lookup(zr *zip.Reader, p string) *zip.File {
	p = strings.TrimPrefix(p, "/")
	if p == "" || strings.HasSuffix(p, "/") {
		p += "index.html"
	}
	for _, f := range zr.File {
		if f.Name == p && !f.FileInfo().IsDir() {
			return f
		}
	}
	// 目录名没带斜杠时也找一下它的 index.html
	for _, f := range zr.File {
		if f.Name == p+"/index.html" {
			return f
		}
	}
	return nil
}

// Guard 是注入原型页面的小脚本：点外部链接不跳转，在页面底部提示一句；表单提交到外部同样拦下。
// 原型运行在无来源的沙箱里，脚本只做提示，不和纸鸢通信。
const Guard = `<script>(function(){var tip;function say(){if(!document.body)return;if(!tip){tip=document.createElement('div');tip.setAttribute('style','position:fixed;left:50%;bottom:16px;transform:translateX(-50%);z-index:2147483647;background:rgba(30,26,20,.92);color:#fff;font:13px/1.5 system-ui,sans-serif;padding:6px 12px;border-radius:8px;pointer-events:none;transition:opacity .2s');document.body.appendChild(tip)}tip.textContent='原型里的外部链接已停用';tip.style.opacity='1';clearTimeout(tip._t);tip._t=setTimeout(function(){tip.style.opacity='0'},1800)}function ext(u){try{var x=new URL(u,location.href);return x.protocol!=='javascript:'&&(x.host!==location.host||x.pathname.indexOf(location.pathname.split('/').slice(0,3).join('/')+'/')!==0)}catch(e){return false}}document.addEventListener('click',function(e){var a=e.target&&e.target.closest&&e.target.closest('a[href]');if(!a)return;var h=a.getAttribute('href')||'';if(h.charAt(0)==='#')return;if(ext(a.href)||a.target==='_blank'||a.target==='_top'||a.target==='_parent'){e.preventDefault();say()}},true);document.addEventListener('submit',function(e){var f=e.target;if(f&&f.action&&ext(f.action)){e.preventDefault();say()}},true)})();</script>`

// Inject 把 Guard 插到 HTML 的 <head> 后面（没有 head 时放最前面）。
func Inject(b []byte) []byte {
	lower := bytes.ToLower(b)
	at := 0
	if i := bytes.Index(lower, []byte("<head")); i >= 0 {
		if j := bytes.IndexByte(lower[i:], '>'); j >= 0 {
			at = i + j + 1
		}
	} else if i := bytes.Index(lower, []byte("<html")); i >= 0 {
		if j := bytes.IndexByte(lower[i:], '>'); j >= 0 {
			at = i + j + 1
		}
	} else if i := bytes.Index(lower, []byte("<!doctype")); i >= 0 {
		if j := bytes.IndexByte(lower[i:], '>'); j >= 0 {
			at = i + j + 1
		}
	}
	out := make([]byte, 0, len(b)+len(Guard))
	out = append(out, b[:at]...)
	out = append(out, Guard...)
	return append(out, b[at:]...)
}

// skipDir：打包目录时跳过的目录。
var skipDir = map[string]bool{".git": true, "node_modules": true, ".DS_Store": true, "__MACOSX": true}

// Pack 把本机的目录、单个 .html 或 .zip 变成一个 zip 文件路径。返回的 cleanup 删掉临时文件（.zip 原样用时什么也不做）。
func Pack(src string) (zipPath string, cleanup func(), err error) {
	cleanup = func() {}
	st, err := os.Stat(src)
	if err != nil {
		return "", cleanup, fmt.Errorf("读不到原型 %s：%w", src, err)
	}
	if !st.IsDir() && strings.EqualFold(filepath.Ext(src), ".zip") {
		return src, cleanup, nil
	}
	if !st.IsDir() && !IsHTML(src) {
		return "", cleanup, fmt.Errorf("原型要是目录、.html 或 .zip：%s", src)
	}
	tmp, err := os.CreateTemp("", "ks-proto-*.zip")
	if err != nil {
		return "", cleanup, err
	}
	cleanup = func() { os.Remove(tmp.Name()) }
	zw := zip.NewWriter(tmp)
	add := func(name, file string) error {
		w, err := zw.Create(name)
		if err != nil {
			return err
		}
		f, err := os.Open(file)
		if err != nil {
			return err
		}
		defer f.Close()
		_, err = io.Copy(w, f)
		return err
	}
	if st.IsDir() {
		var files []string
		err = filepath.WalkDir(src, func(p string, d fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if d.IsDir() {
				if p != src && (skipDir[d.Name()] || strings.HasPrefix(d.Name(), ".")) {
					return filepath.SkipDir
				}
				return nil
			}
			if strings.HasPrefix(d.Name(), ".") || !d.Type().IsRegular() {
				return nil
			}
			files = append(files, p)
			if len(files) > MaxFiles {
				return fmt.Errorf("原型目录里文件超过 %d 个", MaxFiles)
			}
			return nil
		})
		sort.Strings(files)
		for _, p := range files {
			if err != nil {
				break
			}
			rel, _ := filepath.Rel(src, p)
			err = add(filepath.ToSlash(rel), p)
		}
	} else {
		err = add(filepath.Base(src), src)
	}
	if cerr := zw.Close(); err == nil {
		err = cerr
	}
	if cerr := tmp.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		cleanup()
		return "", func() {}, err
	}
	return tmp.Name(), cleanup, nil
}

// Report 是 ks proto check 的结果。
type Report struct {
	Info
	Size int64 // zip 大小
}

// Check 在本机检查一个原型（目录、.html 或 .zip）：打包、校验入口和大小、列出外部引用。
func Check(src, entry string, maxMB int) (Report, error) {
	var r Report
	if maxMB <= 0 {
		maxMB = DefaultMaxMB
	}
	z, cleanup, err := Pack(src)
	if err != nil {
		return r, err
	}
	defer cleanup()
	zr, err := zip.OpenReader(z)
	if err != nil {
		return r, fmt.Errorf("不是有效的 zip：%w", err)
	}
	defer zr.Close()
	st, _ := os.Stat(z)
	r.Size = st.Size()
	r.Info, err = Validate(&zr.Reader, entry)
	if err != nil {
		return r, err
	}
	if r.Size > int64(maxMB)<<20 {
		return r, fmt.Errorf("原型包 %.1f MB，超过上限 %d MB（可在设置里调整）", float64(r.Size)/(1<<20), maxMB)
	}
	return r, nil
}
