package hub

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"mime/multipart"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/NekroAI/kitestring/internal/store"
)

// R-353：附件目录可指定（外置硬盘），不可用时拒收上传、不往别处写。

type storageEnv struct {
	t      *testing.T
	h      *Hub
	st     *store.Store
	srv    *httptest.Server
	c      *http.Client
	base   string // 数据库所在目录
	defDir string // 数据库旁的默认 files
}

// newStorageEnv 起一个 Hub；dir 非空时按 --files-dir 指定，空时用默认目录。
func newStorageEnv(t *testing.T, dir string) *storageEnv {
	t.Helper()
	base := t.TempDir()
	st, err := store.Open(filepath.Join(base, "hub.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	h := New(st, slog.New(slog.NewTextHandler(io.Discard, nil)))
	def := filepath.Join(base, "files")
	if dir != "" {
		h.ConfigureFilesDir(dir, def)
	} else {
		h.SetFilesDir(def)
	}
	_ = h.CheckFiles() // 与 ks hub serve 启动时一样
	srv := httptest.NewServer(h.Handler(nil))
	t.Cleanup(srv.Close)
	jar, _ := cookiejar.New(nil)
	e := &storageEnv{t: t, h: h, st: st, srv: srv, c: &http.Client{Jar: jar}, base: base, defDir: def}
	resp, err := e.c.Post(srv.URL+"/api/auth/setup", "application/json", strings.NewReader(`{"password":"password123"}`))
	if err != nil || resp.StatusCode != 200 {
		t.Fatalf("设置密码失败：%v %v", err, resp)
	}
	resp.Body.Close()
	return e
}

func (e *storageEnv) upload(name string, data []byte) (int, string) {
	e.t.Helper()
	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	fw, _ := mw.CreateFormFile("file", name)
	fw.Write(data)
	mw.Close()
	resp, err := e.c.Post(e.srv.URL+"/api/files", mw.FormDataContentType(), &buf)
	if err != nil {
		e.t.Fatal(err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, string(raw)
}

func (e *storageEnv) uploadOK(name string, data []byte) store.FileRef {
	e.t.Helper()
	code, body := e.upload(name, data)
	if code != 200 {
		e.t.Fatalf("上传应成功，实际 %d：%s", code, body)
	}
	var ref store.FileRef
	if err := json.Unmarshal([]byte(body), &ref); err != nil {
		e.t.Fatal(err)
	}
	return ref
}

func (e *storageEnv) get(path string) (int, []byte) {
	e.t.Helper()
	resp, err := e.c.Get(e.srv.URL + path)
	if err != nil {
		e.t.Fatal(err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, raw
}

func (e *storageEnv) storage() StorageView {
	e.t.Helper()
	code, raw := e.get("/api/storage")
	if code != 200 {
		e.t.Fatalf("GET /api/storage → %d：%s", code, raw)
	}
	var v StorageView
	if err := json.Unmarshal(raw, &v); err != nil {
		e.t.Fatal(err)
	}
	return v
}

func TestStorageConfiguredDir(t *testing.T) {
	ext := filepath.Join(t.TempDir(), "ext-files")
	if err := os.Mkdir(ext, 0o700); err != nil {
		t.Fatal(err)
	}
	e := newStorageEnv(t, ext)
	ref := e.uploadOK("a.png", testPNG(1200, 800, 255))
	if !fileExists(filepath.Join(ext, ref.ID)) {
		t.Fatal("附件应落在指定目录")
	}
	if _, err := os.Stat(e.defDir); err == nil {
		t.Fatal("指定目录后不应再建默认目录")
	}
	if code, _ := e.get("/api/files/" + ref.ID); code != 200 {
		t.Fatalf("读附件 %d", code)
	}
	// 缩略图也在指定目录下
	if code, _ := e.get("/api/files/" + ref.ID + "?w=480"); code != 200 {
		t.Fatalf("读缩略图 %d", code)
	}
	if ents, _ := os.ReadDir(filepath.Join(ext, "thumbs")); len(ents) == 0 {
		t.Fatal("缩略图应落在指定目录的 thumbs 下")
	}
	v := e.storage()
	if !v.OK || !v.Configured || v.Dir != ext || v.Reason != "" {
		t.Fatalf("存储状态不对：%+v", v)
	}
	if v.UsedBytes <= 0 || v.Files < 2 {
		t.Fatalf("已用量应计入附件和缩略图：%+v", v)
	}
}

func TestStorageMissingDirRejectsUpload(t *testing.T) {
	mount := filepath.Join(t.TempDir(), "Volumes", "DataSSD") // 外置盘没挂上：挂载点本身都不在
	ext := filepath.Join(mount, "kitestring-files")
	e := newStorageEnv(t, ext)
	code, body := e.upload("a.txt", []byte("hello"))
	if code != http.StatusServiceUnavailable || !strings.Contains(body, "不存在，外置硬盘可能未挂载") || !strings.Contains(body, ext) {
		t.Fatalf("目录不存在时应拒收并说明原因，实际 %d：%s", code, body)
	}
	if _, err := os.Stat(mount); err == nil {
		t.Fatal("不应替用户建出挂载点下的目录")
	}
	if _, err := os.Stat(e.defDir); err == nil {
		t.Fatal("不应悄悄改写到默认目录")
	}
	// 没有记下附件
	var n int
	if err := e.st.DB().QueryRow(`SELECT COUNT(*) FROM files`).Scan(&n); err != nil || n != 0 {
		t.Fatalf("被拒的上传不应入库：%d %v", n, err)
	}
	v := e.storage()
	if v.OK || !strings.Contains(v.Reason, "不存在，外置硬盘可能未挂载") || v.UsedBytes != 0 {
		t.Fatalf("设置页应显示不可用：%+v", v)
	}
	// 其它接口照常
	if code, _ := e.get("/api/settings"); code != 200 {
		t.Fatalf("Hub 其余功能应照常：%d", code)
	}
	// 硬盘挂上后不用重启即恢复
	if err := os.MkdirAll(ext, 0o700); err != nil {
		t.Fatal(err)
	}
	e.uploadOK("a.txt", []byte("hello"))
	if v := e.storage(); !v.OK {
		t.Fatalf("目录恢复后应可用：%+v", v)
	}
}

func TestStorageUnplugWhileRunning(t *testing.T) {
	ext := filepath.Join(t.TempDir(), "ext")
	if err := os.Mkdir(ext, 0o700); err != nil {
		t.Fatal(err)
	}
	e := newStorageEnv(t, ext)
	ref := e.uploadOK("a.png", testPNG(1200, 800, 255))
	// 运行中把盘拔掉
	gone := ext + "-unplugged"
	if err := os.Rename(ext, gone); err != nil {
		t.Fatal(err)
	}
	if code, body := e.upload("b.txt", []byte("x")); code != http.StatusServiceUnavailable || !strings.Contains(body, "无法上传") {
		t.Fatalf("拔盘后上传应被拒：%d %s", code, body)
	}
	if code, raw := e.get("/api/files/" + ref.ID + "?w=480"); code != http.StatusServiceUnavailable || !strings.Contains(string(raw), "不存在，外置硬盘可能未挂载") {
		t.Fatalf("拔盘后读附件应说明存储不可用：%d %s", code, raw)
	}
	if _, err := os.Stat(ext); err == nil {
		t.Fatal("拔盘后不应在原路径重新建目录（含缩略图目录）")
	}
}

func TestStorageNotWritable(t *testing.T) {
	if runtime.GOOS == "windows" || os.Geteuid() == 0 {
		t.Skip("只读目录权限在 Windows / root 下不生效")
	}
	ext := filepath.Join(t.TempDir(), "ro")
	if err := os.Mkdir(ext, 0o500); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.Chmod(ext, 0o700) })
	e := newStorageEnv(t, ext)
	if code, body := e.upload("a.txt", []byte("x")); code != http.StatusServiceUnavailable || !strings.Contains(body, "无法写入") {
		t.Fatalf("不可写时应拒收：%d %s", code, body)
	}
	if v := e.storage(); v.OK || !strings.Contains(v.Reason, "无法写入") {
		t.Fatalf("设置页应显示无法写入：%+v", v)
	}
}

func TestStorageNotADir(t *testing.T) {
	p := filepath.Join(t.TempDir(), "file")
	if err := os.WriteFile(p, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	e := newStorageEnv(t, p)
	if v := e.storage(); v.OK || !strings.Contains(v.Reason, "不是文件夹") {
		t.Fatalf("%+v", v)
	}
}

// 改到新目录、旧附件还在默认目录：照常能看（只读回落），新附件写到新目录
func TestStorageLegacyFallback(t *testing.T) {
	ext := filepath.Join(t.TempDir(), "ext")
	if err := os.Mkdir(ext, 0o700); err != nil {
		t.Fatal(err)
	}
	e := newStorageEnv(t, ext)
	old, err := e.st.CreateFile(context.Background(), "old.png", "image/png", "x", 0, "user")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(e.defDir, 0o700); err != nil {
		t.Fatal(err)
	}
	png := testPNG(1200, 800, 255)
	if err := os.WriteFile(filepath.Join(e.defDir, old.ID), png, 0o600); err != nil {
		t.Fatal(err)
	}
	if code, raw := e.get("/api/files/" + old.ID); code != 200 || !bytes.Equal(raw, png) {
		t.Fatalf("旧附件应照常能看：%d", code)
	}
	if code, _ := e.get("/api/files/" + old.ID + "?w=480"); code != 200 {
		t.Fatalf("旧附件的缩略图 %d", code)
	}
	if ents, _ := os.ReadDir(filepath.Join(e.defDir, "thumbs")); len(ents) != 0 {
		t.Fatal("缩略图不应写回旧目录")
	}
	if ents, _ := os.ReadDir(filepath.Join(ext, "thumbs")); len(ents) == 0 {
		t.Fatal("旧附件的缩略图应写到新目录")
	}
}

// 默认目录（不指定时）照旧：不存在就自动建好
func TestStorageDefaultDirAutoCreate(t *testing.T) {
	e := newStorageEnv(t, "")
	ref := e.uploadOK("a.txt", []byte("hello"))
	if !fileExists(filepath.Join(e.defDir, ref.ID)) {
		t.Fatal("默认目录下应有附件")
	}
	if v := e.storage(); !v.OK || v.Configured || v.Dir != e.defDir || v.Files != 1 || v.UsedBytes != 5 {
		t.Fatalf("%+v", v)
	}
}
