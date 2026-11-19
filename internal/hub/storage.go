package hub

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"net/http"
	"os"
	"path/filepath"
	"sync"

	"github.com/danielgtaylor/huma/v2"
)

// 附件存储（R-353）：附件、原型包、缩略图都写在 filesDir。
// 默认是数据库旁的 files，第一次用到时自动建好；用 ks hub serve --files-dir / KS_FILES_DIR 指定时（例如外置硬盘），
// 目录必须已经存在——绝不自动创建：外置盘没挂上时挂载点下建目录会悄悄写到系统盘。
// 启动时和每次上传前检查一次「存在、是目录、能写」；不可用时拒收上传、日志写原因、设置页显示原因，Hub 其余功能照常。
// 改了目录、旧附件还没搬过去时，读取回落到原来的默认目录（只读，不往那里写），已有附件照常能看。

type filesState struct {
	mu       sync.Mutex
	strict   bool   // 显式指定的目录：不自动创建
	fallback string // 只读回落目录（原来的默认目录）
	reason   string // 最近一次检查不可用的原因；空 = 可用
	checked  bool
}

// ConfigureFilesDir 指定附件目录（--files-dir / KS_FILES_DIR）：目录必须已存在，不自动创建。
// fallback 是原来的默认目录，找不到的旧附件从那里只读。
func (h *Hub) ConfigureFilesDir(dir, fallback string) {
	h.filesDir = dir
	h.files.mu.Lock()
	h.files.strict = true
	if fallback != "" && filepath.Clean(fallback) != filepath.Clean(dir) {
		h.files.fallback = fallback
	}
	h.files.mu.Unlock()
}

// CheckFiles 检查附件目录，启动时调用。
func (h *Hub) CheckFiles() error { return h.checkFiles() }

// checkFiles 检查附件目录可不可用，返回不可用的原因（中文，直接显示给用户）；状态变化时写日志（带系统原始错误）。
func (h *Hub) checkFiles() error {
	h.files.mu.Lock()
	defer h.files.mu.Unlock()
	reason, raw := h.probeFilesLocked()
	if !h.files.checked || reason != h.files.reason {
		switch {
		case reason != "":
			h.log.Warn("附件目录不可用，拒收上传", "dir", h.filesDir, "reason", reason, "err", raw)
		case h.files.checked:
			h.log.Info("附件目录已恢复可用", "dir", h.filesDir)
		default:
			h.log.Info("附件目录", "dir", h.filesDir)
		}
	}
	h.files.checked, h.files.reason = true, reason
	if reason != "" {
		return errors.New(reason)
	}
	return nil
}

// probeFilesLocked 返回显示给用户的原因和系统原始错误（只写日志）；都为空表示可用。
func (h *Hub) probeFilesLocked() (string, error) {
	dir := h.filesDir
	if dir == "" {
		return "未配置附件目录", nil
	}
	st, err := os.Stat(dir)
	if errors.Is(err, fs.ErrNotExist) {
		if h.files.strict {
			return fmt.Sprintf("附件目录 %s 不存在，外置硬盘可能未挂载", dir), err
		}
		if err := os.MkdirAll(dir, 0o700); err != nil {
			return fmt.Sprintf("无法创建附件目录 %s%s", dir, permHint(err)), err
		}
		st, err = os.Stat(dir)
	}
	if err != nil {
		return fmt.Sprintf("无法读取附件目录 %s%s", dir, permHint(err)), err
	}
	if !st.IsDir() {
		return fmt.Sprintf("附件目录 %s 不是文件夹", dir), nil
	}
	f, err := os.CreateTemp(dir, ".probe-*")
	if err != nil {
		return fmt.Sprintf("附件目录 %s 无法写入%s", dir, permHint(err)), err
	}
	f.Close()
	os.Remove(f.Name())
	return "", nil
}

// permHint 把常见的系统错误换成中文补充说明。
func permHint(err error) string {
	if errors.Is(err, fs.ErrPermission) {
		return "（没有权限）"
	}
	return ""
}

// readPath 返回读取某个附件用的路径：附件目录里没有时回落到旧的默认目录（R-353）。
func (h *Hub) readPath(id string) string {
	p := h.filePath(id)
	h.files.mu.Lock()
	fb := h.files.fallback
	h.files.mu.Unlock()
	if fb == "" {
		return p
	}
	if _, err := os.Stat(p); err == nil {
		return p
	}
	if q := filepath.Join(fb, id); fileExists(q) {
		return q
	}
	return p
}

func fileExists(p string) bool {
	st, err := os.Stat(p)
	return err == nil && !st.IsDir()
}

// StorageView 是设置页「附件存储」显示的内容。
type StorageView struct {
	Dir        string `json:"dir" doc:"附件目录（附件、原型包、缩略图都在这里）"`
	Configured bool   `json:"configured" doc:"是否用 --files-dir / KS_FILES_DIR 指定；false 为数据库旁的默认目录"`
	OK         bool   `json:"ok" doc:"目录可用（存在且能写）；不可用时拒收上传"`
	Reason     string `json:"reason,omitempty" doc:"不可用的原因"`
	UsedBytes  int64  `json:"used_bytes" doc:"目录里文件的总大小（字节）"`
	Files      int    `json:"files" doc:"目录里的文件数（含缩略图）"`
}

type storageOut struct{ Body StorageView }

// storageView 检查一次目录并统计已用量。
func (h *Hub) storageView() StorageView {
	v := StorageView{Dir: h.filesDir}
	h.files.mu.Lock()
	v.Configured = h.files.strict
	h.files.mu.Unlock()
	if err := h.checkFiles(); err != nil {
		v.Reason = err.Error()
		return v
	}
	v.OK = true
	_ = filepath.WalkDir(h.filesDir, func(_ string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return nil
		}
		if info, err := d.Info(); err == nil {
			v.UsedBytes += info.Size()
			v.Files++
		}
		return nil
	})
	return v
}

func (h *Hub) registerStorage(api huma.API) {
	huma.Register(api, huma.Operation{OperationID: "get-storage", Method: http.MethodGet, Path: "/api/storage", Tags: []string{"settings"},
		Summary: "附件存储：目录、是否可用及原因、已用量"},
		func(ctx context.Context, _ *empty) (*storageOut, error) {
			return &storageOut{Body: h.storageView()}, nil
		})
}
