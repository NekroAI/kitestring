package hub

import (
	"context"
	"io/fs"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"
)

// R-367：附件目录检查不阻塞监听。没有授权（EPERM）或检查卡住（等 macOS 授权弹窗）时 Hub 照常监听、拒收上传、
// 设置页说明原因和怎么授权；目录恢复后自动恢复，不用重启。

// fakeFilesStat 让对 dir 的检查按 fn 返回，其它路径照常；测试结束恢复。
func fakeFilesStat(t *testing.T, dir string, fn func() (os.FileInfo, error)) {
	t.Helper()
	old, oldTimeout, oldEvery := filesStat, filesProbeTimeout, filesRecheckEvery
	filesProbeTimeout, filesRecheckEvery = 200*time.Millisecond, 50*time.Millisecond
	filesStat = func(p string) (os.FileInfo, error) {
		if p == dir {
			return fn()
		}
		return os.Stat(p)
	}
	t.Cleanup(func() { filesStat, filesProbeTimeout, filesRecheckEvery = old, oldTimeout, oldEvery })
}

// startHub 用 Start 在空闲端口上起 Hub（与 ks hub serve 一样），等到能连上为止；连不上说明启动被挡住了。
func startHub(t *testing.T, h *Hub) {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := ln.Addr().String()
	ln.Close()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- h.Start(ctx, addr, nil) }()
	t.Cleanup(func() { cancel(); <-done })
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if c, err := net.DialTimeout("tcp", addr, 100*time.Millisecond); err == nil {
			c.Close()
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatal("Hub 没有开始监听：启动被附件目录检查挡住了")
}

func waitFilesOK(t *testing.T, h *Hub) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		h.files.mu.Lock()
		ok := h.files.checked && h.files.reason == "" && h.files.probing == nil
		h.files.mu.Unlock()
		if ok {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatal("目录恢复后应自动变为可用")
}

func TestStorageNoPermissionStillListens(t *testing.T) {
	ext := filepath.Join(t.TempDir(), "ext")
	if err := os.Mkdir(ext, 0o700); err != nil {
		t.Fatal(err)
	}
	var mu sync.Mutex
	denied := true
	fakeFilesStat(t, ext, func() (os.FileInfo, error) {
		mu.Lock()
		defer mu.Unlock()
		if denied {
			return nil, &fs.PathError{Op: "stat", Path: ext, Err: syscall.EPERM}
		}
		return os.Stat(ext)
	})
	e := newStorageEnv(t, ext)
	startHub(t, e.h)
	if code, body := e.upload("a.txt", []byte("x")); code != http.StatusServiceUnavailable || !strings.Contains(body, "无法上传") {
		t.Fatalf("没有授权时应拒收上传：%d %s", code, body)
	}
	v := e.storage()
	if v.OK || !strings.Contains(v.Reason, ext) {
		t.Fatalf("设置页应显示不可用：%+v", v)
	}
	if runtime.GOOS == "darwin" {
		if !strings.Contains(v.Reason, "macOS 未允许访问") || !strings.Contains(v.Hint, "完全磁盘访问权限") || !strings.Contains(v.Hint, "ks-launcher") {
			t.Fatalf("macOS 上应说明怎么授权：%+v", v)
		}
	}
	// 授权之后：后台定期重查，自动恢复
	mu.Lock()
	denied = false
	mu.Unlock()
	waitFilesOK(t, e.h)
	e.uploadOK("a.txt", []byte("x"))
	if v := e.storage(); !v.OK || v.Hint != "" {
		t.Fatalf("恢复后应可用：%+v", v)
	}
}

func TestStorageStalledCheckStillListens(t *testing.T) {
	ext := filepath.Join(t.TempDir(), "ext")
	if err := os.Mkdir(ext, 0o700); err != nil {
		t.Fatal(err)
	}
	release := make(chan struct{})
	var once sync.Once
	t.Cleanup(func() { once.Do(func() { close(release) }) })
	fakeFilesStat(t, ext, func() (os.FileInfo, error) {
		<-release // 模拟等系统授权弹窗：一直不返回
		return os.Stat(ext)
	})
	e := newStorageEnv(t, ext) // 里面的 CheckFiles 最多等 filesProbeTimeout
	start := time.Now()
	startHub(t, e.h)
	if d := time.Since(start); d > 2*time.Second {
		t.Fatalf("启动不应等附件目录检查：%v", d)
	}
	// 检查卡住时：上传和设置页立即返回，不叠加等待
	t0 := time.Now()
	if code, body := e.upload("a.txt", []byte("x")); code != http.StatusServiceUnavailable || !strings.Contains(body, "无响应") {
		t.Fatalf("检查卡住时应拒收上传：%d %s", code, body)
	}
	v := e.storage()
	if v.OK || !strings.Contains(v.Reason, "无响应") {
		t.Fatalf("设置页应显示检查无响应：%+v", v)
	}
	if runtime.GOOS == "darwin" && (!strings.Contains(v.Reason, "推测") || !strings.Contains(v.Hint, "允许")) {
		t.Fatalf("macOS 上应提示可能在等授权：%+v", v)
	}
	if d := time.Since(t0); d > time.Second {
		t.Fatalf("上一次检查已超时，不应再等：%v", d)
	}
	// 弹窗点了允许：卡住的那次检查返回，状态自动恢复
	once.Do(func() { close(release) })
	waitFilesOK(t, e.h)
	e.uploadOK("b.txt", []byte("y"))
}
