package hub

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"sync"
	"syscall"
	"time"

	"github.com/danielgtaylor/huma/v2"
)

// 附件存储（R-353）：附件、原型包、缩略图都写在 filesDir。
// 默认是数据库旁的 files，第一次用到时自动建好；用 ks hub serve --files-dir / KS_FILES_DIR 指定时（例如外置硬盘），
// 目录必须已经存在——绝不自动创建：外置盘没挂上时挂载点下建目录会悄悄写到系统盘。
// 检查「存在、是目录、能写」：不可用时拒收上传、日志写原因、设置页显示原因，Hub 其余功能照常。
// 改了目录、旧附件还没搬过去时，读取回落到原来的默认目录（只读，不往那里写），已有附件照常能看。
//
// 检查绝不阻塞 Hub 启动（R-367）：macOS 上 launchd 起的进程访问外置硬盘要隐私授权，没授权时文件操作会一直卡住、
// 等系统弹窗。所以检查在后台进行、最多等 filesProbeTimeout；超时就按「不可用」处理，那次检查返回后自动更新状态。
// 启动后先查一次（不等它），不可用时每 filesRecheckEvery 再查一次，上传前、打开设置页时也会查，目录恢复后无需重启。

var (
	filesProbeTimeout = 5 * time.Second  // 单次检查最多等多久
	filesRecheckEvery = 30 * time.Second // 不可用时多久重查一次
	filesStat         = os.Stat          // 测试替换：模拟没有授权、卡住
)

type filesState struct {
	mu       sync.Mutex
	strict   bool   // 显式指定的目录：不自动创建
	fallback string // 只读回落目录（原来的默认目录）
	reason   string // 最近一次检查不可用的原因；空 = 可用
	hint     string // 不可用时怎么处理（目前只有 macOS 的访问授权）
	checked  bool
	probing  chan struct{} // 正在进行的检查，结束时关闭；nil = 没有
	stalled  bool          // 正在进行的检查已超时（多半在等系统授权）
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

// CheckFiles 检查附件目录（最多等 filesProbeTimeout），返回不可用的原因。
func (h *Hub) CheckFiles() error { return h.checkFiles() }

// filesWatch 在 Hub 启动后检查附件目录，不可用时定期重查，恢复后上传自动可用（R-367）。
func (h *Hub) filesWatch(ctx context.Context) {
	if h.filesDir == "" {
		return
	}
	_ = h.checkFiles()
	t := time.NewTicker(filesRecheckEvery)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			h.files.mu.Lock()
			bad := h.files.reason != ""
			h.files.mu.Unlock()
			if bad {
				_ = h.checkFiles()
			}
		}
	}
}

// checkFiles 检查附件目录可不可用，返回不可用的原因（中文，直接显示给用户）；状态变化时写日志（带系统原始错误）。
// 检查在后台进行，最多等 filesProbeTimeout；上一次检查已超时还没返回时直接返回当前状态，不叠加等待。
func (h *Hub) checkFiles() error {
	h.files.mu.Lock()
	if h.files.probing != nil && h.files.stalled {
		defer h.files.mu.Unlock()
		return h.files.errLocked()
	}
	if h.files.probing == nil {
		done := make(chan struct{})
		h.files.probing = done
		dir, strict := h.filesDir, h.files.strict
		go func() {
			reason, hint, raw := probeFiles(dir, strict)
			h.files.mu.Lock()
			h.files.probing, h.files.stalled = nil, false
			h.recordFilesLocked(reason, hint, raw)
			h.files.mu.Unlock()
			close(done)
		}()
	}
	done := h.files.probing
	h.files.mu.Unlock()

	timer := time.NewTimer(filesProbeTimeout)
	defer timer.Stop()
	select {
	case <-done:
	case <-timer.C:
		h.files.mu.Lock()
		if h.files.probing == done { // 还没返回
			h.files.stalled = true
			reason, hint := stalledReason(h.filesDir)
			h.recordFilesLocked(reason, hint, fmt.Errorf("检查超过 %s 未返回", filesProbeTimeout))
		}
		h.files.mu.Unlock()
	}
	h.files.mu.Lock()
	defer h.files.mu.Unlock()
	return h.files.errLocked()
}

func (f *filesState) errLocked() error {
	if f.reason != "" {
		return errors.New(f.reason)
	}
	return nil
}

// recordFilesLocked 记下检查结果，状态变化时写日志。
func (h *Hub) recordFilesLocked(reason, hint string, raw error) {
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
	h.files.checked, h.files.reason, h.files.hint = true, reason, hint
}

// probeFiles 返回显示给用户的原因、处理办法和系统原始错误（只写日志）；原因为空表示可用。
// 可能卡住（等 macOS 授权），只在后台 goroutine 里调用。
func probeFiles(dir string, strict bool) (string, string, error) {
	if dir == "" {
		return "未配置附件目录", "", nil
	}
	st, err := filesStat(dir)
	if errors.Is(err, fs.ErrNotExist) {
		if strict {
			return fmt.Sprintf("附件目录 %s 不存在，外置硬盘可能未挂载", dir), "", err
		}
		if err := os.MkdirAll(dir, 0o700); err != nil {
			r, hint := permReason("无法创建附件目录 %s", dir, err)
			return r, hint, err
		}
		st, err = filesStat(dir)
	}
	if err != nil {
		r, hint := permReason("无法读取附件目录 %s", dir, err)
		return r, hint, err
	}
	if !st.IsDir() {
		return fmt.Sprintf("附件目录 %s 不是文件夹", dir), "", nil
	}
	f, err := os.CreateTemp(dir, ".probe-*")
	if err != nil {
		r, hint := permReason("附件目录 %s 无法写入", dir, err)
		return r, hint, err
	}
	f.Close()
	os.Remove(f.Name())
	return "", "", nil
}

// permReason 把常见的系统错误换成中文原因；macOS 上的 EPERM（Operation not permitted）是隐私授权拦下的，附上授权办法。
func permReason(format, dir string, err error) (string, string) {
	reason := fmt.Sprintf(format, dir)
	switch {
	case runtime.GOOS == "darwin" && errors.Is(err, syscall.EPERM):
		return reason + "（macOS 未允许访问）", authHint()
	case errors.Is(err, fs.ErrPermission):
		return reason + "（没有权限）", ""
	}
	return reason, ""
}

// stalledReason 是检查超时时的原因和处理办法。
func stalledReason(dir string) (string, string) {
	reason := fmt.Sprintf("检查附件目录 %s 超过 %d 秒无响应", dir, int(filesProbeTimeout.Seconds()))
	if runtime.GOOS == "darwin" {
		return reason + "，推测在等待 macOS 的访问授权", "这台 Mac 上如有询问是否允许访问的弹窗，选择允许。" + authHint()
	}
	return reason + "，外置硬盘可能无响应", ""
}

// authHint 说明怎么在 macOS 上给 Hub 授权：授权给固定启动器 ks-launcher（R-354），以后部署不用重授。
func authHint() string {
	return fmt.Sprintf("打开「系统设置 → 隐私与安全性 → 完全磁盘访问权限」，加入 %s 并打开开关；"+
		"或在「文件与文件夹」里允许 ks-launcher 访问「可移除的宗卷」。", launcherPath())
}

// launcherPath 是 Hub 常驻服务的固定启动器（与 ks install、deploy.sh 一致：$KS_HOME/bin/ks-launcher）。
func launcherPath() string {
	if h := os.Getenv("KS_HOME"); h != "" {
		return filepath.Join(h, "bin", "ks-launcher")
	}
	return "~/.kitestring/bin/ks-launcher"
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
	Hint       string `json:"hint,omitempty" doc:"不可用时怎么处理（如 macOS 的访问授权步骤）"`
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
		h.files.mu.Lock()
		v.Hint = h.files.hint
		h.files.mu.Unlock()
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
