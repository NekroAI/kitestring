package hub

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime"
	"net/http"
	"os"
	"path/filepath"
	"strings"

	"github.com/NekroAI/kitestring/internal/protocol"
	"github.com/NekroAI/kitestring/internal/store"
)

// 附件：上传后存放在 Hub 数据目录下，按 ID 读取；网页端用登录会话、设备端用设备令牌访问。
// 不限类型（R-221）：PNG / JPEG / GIF / WebP 图片与 MP4 / WebM / MOV 录屏在页面里直接显示，
// 其它文件（包括 SVG、HTML）一律以下载方式返回（Content-Disposition: attachment，并禁止脚本），不在本站同源里执行。

// 大小上限在设置里调（R-72），默认图片 10 MB、录屏 50 MB、其它文件 20 MB。

// sniff 判断文件类型。iPhone 录屏是 .mov（QuickTime），标准库认不出，按 ftyp 头里的 qt 品牌补上。
func sniff(head []byte) string {
	if m := http.DetectContentType(head); m != "application/octet-stream" {
		return m
	}
	if len(head) >= 12 && bytes.Equal(head[4:8], []byte("ftyp")) && bytes.Equal(head[8:12], []byte("qt  ")) {
		return "video/quicktime"
	}
	return "application/octet-stream"
}

// extMime 是常见文件按扩展名的类型；系统的类型表因机器而异，常用的这里写死。
var extMime = map[string]string{
	".txt": "text/plain; charset=utf-8", ".log": "text/plain; charset=utf-8", ".md": "text/markdown; charset=utf-8",
	".csv": "text/csv; charset=utf-8", ".tsv": "text/tab-separated-values; charset=utf-8", ".json": "application/json",
	".jsonl": "application/x-ndjson", ".ndjson": "application/x-ndjson", ".yaml": "application/yaml", ".yml": "application/yaml",
	".xml": "application/xml", ".html": "text/html; charset=utf-8", ".htm": "text/html; charset=utf-8", ".svg": "image/svg+xml",
	".pdf": "application/pdf", ".zip": "application/zip", ".gz": "application/gzip", ".tar": "application/x-tar",
}

// fileMime 定附件的类型：内容认得出是图片 / 录屏的按内容；否则按扩展名（但扩展名冒充图片 / 录屏的不认）；都不行按内容猜。
func fileMime(name string, head []byte) string {
	m := sniff(head)
	if protocol.InlineMime(m) {
		return m
	}
	ext := strings.ToLower(filepath.Ext(name))
	t := extMime[ext]
	if t == "" && ext != "" {
		t = mime.TypeByExtension(ext)
	}
	if t != "" {
		if base, _, err := mime.ParseMediaType(t); err == nil && !protocol.InlineMime(base) {
			return t
		}
	}
	return m
}

// SetFilesDir 设置附件存放目录。
func (h *Hub) SetFilesDir(dir string) { h.filesDir = dir }

func (h *Hub) filePath(id string) string { return filepath.Join(h.filesDir, id) }

// handleUpload 接收 multipart 字段 file，返回 FileRef。
func (h *Hub) handleUpload(uploader func(*http.Request) string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if h.filesDir == "" {
			writeErr(w, http.StatusServiceUnavailable, "未配置附件目录")
			return
		}
		set, err := h.Settings(r.Context())
		if err != nil {
			writeErr(w, http.StatusInternalServerError, err.Error())
			return
		}
		maxImage, maxVideo, maxFile := set.Upload.bytes()
		r.Body = http.MaxBytesReader(w, r.Body, max(maxImage, maxVideo, maxFile)+1<<20)
		f, hdr, err := r.FormFile("file")
		if err != nil {
			var tooBig *http.MaxBytesError
			if errors.As(err, &tooBig) {
				writeErr(w, http.StatusRequestEntityTooLarge, fmt.Sprintf("文件太大：图片不能超过 %d MB、录屏 %d MB、其它文件 %d MB（可在设置里调整）", maxImage>>20, maxVideo>>20, maxFile>>20))
				return
			}
			writeErr(w, http.StatusBadRequest, "没有收到文件")
			return
		}
		defer f.Close()
		if err := os.MkdirAll(h.filesDir, 0o700); err != nil {
			writeErr(w, http.StatusInternalServerError, err.Error())
			return
		}
		tmp, err := os.CreateTemp(h.filesDir, "upload-*")
		if err != nil {
			writeErr(w, http.StatusInternalServerError, err.Error())
			return
		}
		defer os.Remove(tmp.Name())
		sum := sha256.New()
		head := make([]byte, 512)
		n, _ := io.ReadFull(f, head)
		head = head[:n]
		typ := fileMime(hdr.Filename, head)
		size, err := io.Copy(io.MultiWriter(tmp, sum), io.MultiReader(strings.NewReader(string(head)), f))
		tmp.Close()
		if err != nil {
			writeErr(w, http.StatusBadRequest, "读取文件失败")
			return
		}
		limit, what := maxFile, "文件"
		switch {
		case protocol.IsImage(typ):
			limit, what = maxImage, "图片"
		case protocol.IsVideo(typ):
			limit, what = maxVideo, "录屏"
		}
		if size > limit {
			msg := fmt.Sprintf("%s不能超过 %d MB（可在设置里调整）", what, limit>>20)
			writeErr(w, http.StatusRequestEntityTooLarge, msg)
			return
		}
		name := filepath.Base(hdr.Filename)
		if name == "." || name == "/" || name == "" {
			name = "file"
			if ext := protocol.MediaExt(typ); ext != "" {
				name = strings.SplitN(typ, "/", 2)[0] + ext
			}
		}
		ref, err := h.store.CreateFile(r.Context(), name, typ, hex.EncodeToString(sum.Sum(nil)), size, uploader(r))
		if err != nil {
			writeErr(w, http.StatusInternalServerError, err.Error())
			return
		}
		if err := os.Rename(tmp.Name(), h.filePath(ref.ID)); err != nil {
			writeErr(w, http.StatusInternalServerError, err.Error())
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(ref)
	}
}

// handleDownload 按 ID 返回附件内容。图片、录屏在页面里直接显示（带 ?download 时也按下载）；其它文件一律按下载返回。
func (h *Hub) handleDownload(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	ref, err := h.store.GetFile(r.Context(), id)
	if errors.Is(err, store.ErrNotFound) || strings.ContainsAny(id, "/\\.") {
		writeErr(w, http.StatusNotFound, "附件不存在")
		return
	}
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	f, err := os.Open(h.filePath(ref.ID))
	if err != nil {
		writeErr(w, http.StatusNotFound, "附件文件丢失")
		return
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	disp := "inline"
	if !protocol.InlineMime(ref.Mime) || r.URL.Query().Has("download") {
		disp = "attachment"
		// 万一浏览器还是在本站打开了它（例如 HTML、SVG），也不许执行脚本、不许读本站的数据
		w.Header().Set("Content-Security-Policy", "sandbox; default-src 'none'")
	}
	if cd := mime.FormatMediaType(disp, map[string]string{"filename": ref.Name}); cd != "" {
		w.Header().Set("Content-Disposition", cd)
	} else {
		w.Header().Set("Content-Disposition", disp)
	}
	w.Header().Set("Content-Type", ref.Mime)
	w.Header().Set("Cache-Control", "private, max-age=31536000, immutable")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	// ServeContent 支持 Range 请求，Safari 播放视频需要。
	http.ServeContent(w, r, "", st.ModTime(), f)
}
