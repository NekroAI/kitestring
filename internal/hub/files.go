package hub

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"

	"github.com/NekroAI/kitestring/internal/store"
)

// 附件（图片）：上传后存放在 Hub 数据目录下，按 ID 读取；网页端用登录会话、设备端用设备令牌访问。

const maxFileSize = 10 << 20

var allowedMime = map[string]string{"image/png": ".png", "image/jpeg": ".jpg", "image/gif": ".gif", "image/webp": ".webp"}

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
		r.Body = http.MaxBytesReader(w, r.Body, maxFileSize+1<<20)
		f, hdr, err := r.FormFile("file")
		if err != nil {
			writeErr(w, http.StatusBadRequest, "没有收到文件，或文件超过 10 MB")
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
		mime := http.DetectContentType(head)
		if _, ok := allowedMime[mime]; !ok {
			tmp.Close()
			writeErr(w, http.StatusUnsupportedMediaType, "只支持 PNG、JPEG、GIF、WebP 图片")
			return
		}
		size, err := io.Copy(io.MultiWriter(tmp, sum), io.MultiReader(strings.NewReader(string(head)), f))
		tmp.Close()
		if err != nil {
			writeErr(w, http.StatusBadRequest, "读取文件失败")
			return
		}
		if size > maxFileSize {
			writeErr(w, http.StatusRequestEntityTooLarge, "图片不能超过 10 MB")
			return
		}
		name := filepath.Base(hdr.Filename)
		if name == "." || name == "/" || name == "" {
			name = "image" + allowedMime[mime]
		}
		ref, err := h.store.CreateFile(r.Context(), name, mime, hex.EncodeToString(sum.Sum(nil)), size, uploader(r))
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

// handleDownload 按 ID 返回附件内容。
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
	w.Header().Set("Content-Type", ref.Mime)
	w.Header().Set("Cache-Control", "private, max-age=31536000, immutable")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	_, _ = io.Copy(w, f)
}
