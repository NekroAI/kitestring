package e2e

import (
	"context"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/NekroAI/kitestring/internal/assetpush"
	"github.com/NekroAI/kitestring/internal/client"
	"github.com/NekroAI/kitestring/internal/daemon"
	"github.com/NekroAI/kitestring/internal/hub"
	"github.com/NekroAI/kitestring/internal/protocol"
	"github.com/NekroAI/kitestring/internal/store"
)

// relay 是 daemon 与 Hub 之间可以随时断开的 TCP 中转，用来模拟离线。
type relay struct {
	ln     net.Listener
	target string
	mu     sync.Mutex
	down   bool
	conns  map[net.Conn]bool
}

func newRelay(t *testing.T, target string) *relay {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	r := &relay{ln: ln, target: target, conns: map[net.Conn]bool{}}
	t.Cleanup(func() { ln.Close(); r.setDown(true) })
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			r.mu.Lock()
			down := r.down
			if !down {
				r.conns[c] = true
			}
			r.mu.Unlock()
			if down {
				c.Close()
				continue
			}
			go func() {
				up, err := net.Dial("tcp", target)
				if err != nil {
					c.Close()
					return
				}
				r.mu.Lock()
				r.conns[up] = true
				r.mu.Unlock()
				go func() { _, _ = io.Copy(up, c); up.Close() }()
				_, _ = io.Copy(c, up)
				c.Close()
			}()
		}
	}()
	return r
}

func (r *relay) url() string { return "http://" + r.ln.Addr().String() }

func (r *relay) setDown(v bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.down = v
	if v {
		for c := range r.conns {
			c.Close()
		}
		r.conns = map[net.Conn]bool{}
	}
}

// setupRelay 同 setup，但 daemon 经 relay 连 Hub（可断开）。
func setupRelay(t *testing.T) (*env, *relay) {
	t.Helper()
	dir, err := os.MkdirTemp(sockTmp(), "ks-e2e-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	t.Setenv("KS_HOME", dir)
	st, err := store.Open(dir + "/hub.db")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	h := hub.New(st, log)
	h.SetFilesDir(dir + "/files")
	srv := httptest.NewServer(h.Handler(nil))
	t.Cleanup(srv.Close)
	jar, _ := cookiejar.New(nil)
	e := &env{t: t, srv: srv, web: &http.Client{Jar: jar}, store: st, hub: h}
	e.call("POST", "/api/auth/setup", map[string]any{"password": "password123"}, nil)
	var dev struct {
		Token string `json:"token"`
	}
	e.call("POST", "/api/devices", map[string]any{"name": "test-device"}, &dev)
	r := newRelay(t, srv.Listener.Addr().String())
	d, err := daemon.New(daemon.Config{HubURL: r.url(), Token: dev.Token}, log)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	e.cancel = cancel
	done := make(chan struct{})
	go func() { _ = d.Run(ctx); close(done) }()
	t.Cleanup(func() { cancel(); <-done })
	e.waitFor("daemon 连上 Hub", func() bool { return daemonConnected() })
	return e, r
}

func daemonConnected() bool {
	var st map[string]any
	err := client.New(time.Second).Do(context.Background(), "GET", "/status", nil, &st)
	return err == nil && st["connected"] == true
}

// copyFixture 把样例素材复制到临时目录（要改文件内容时用）。
func copyFixture(t *testing.T) string {
	t.Helper()
	dst := t.TempDir()
	src := filepath.Join("..", "assetpush", "testdata", "sample")
	err := filepath.Walk(src, func(p string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(src, p)
		if info.IsDir() {
			return os.MkdirAll(filepath.Join(dst, rel), 0o700)
		}
		b, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		return os.WriteFile(filepath.Join(dst, rel), b, 0o600)
	})
	if err != nil {
		t.Fatal(err)
	}
	return dst
}

func assetSession(t *testing.T, e *env, sid string) string {
	t.Helper()
	cwd, _ := os.Getwd()
	hook(t, "claude", map[string]any{"session_id": sid, "cwd": cwd, "hook_event_name": "UserPromptSubmit", "prompt": "推素材", "prompt_id": "p0"})
	key := "claude:" + sid
	e.waitFor("会话入库", func() bool { return e.session(key).ProjectID != "" })
	return key
}

func pushAssets(t *testing.T, dir, key, batch string) (protocol.AssetPushReply, error) {
	t.Helper()
	p, _, err := assetpush.Build(assetpush.Options{Manifest: filepath.Join(dir, "assets", "manifest.json"), Previews: []string{filepath.Join(dir, "previews")}, Batch: batch, SessionKey: key, Branch: "main", Commit: "abc1234"})
	if err != nil {
		t.Fatal(err)
	}
	var out protocol.AssetPushReply
	err = client.New(time.Minute).Do(context.Background(), "POST", "/assets/push", p, &out)
	return out, err
}

func countResults(r *protocol.AssetPushResult) map[string]int {
	m := map[string]int{}
	for _, it := range r.Items {
		m[it.Result]++
	}
	return m
}

func fileCount(t *testing.T, e *env) int {
	var n int
	if err := e.store.DB().QueryRow(`SELECT COUNT(*) FROM files`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

// R-351：推送、重复推送（内容有变记新版本、没变不新增）、按 sha256 去重、GLB 超限、存储不可用。
func TestAssetPush(t *testing.T) {
	e := setup(t)
	key := assetSession(t, e, "asset-1")
	dir := copyFixture(t)

	// 1. 首推：2 个零件 + 3 张独立的图，文件 2 个 GLB + 6 张图
	out, err := pushAssets(t, dir, key, "S1")
	if err != nil || out.Queued || out.Result == nil {
		t.Fatalf("首推应当场入库：%+v %v", out, err)
	}
	if c := countResults(out.Result); c["created"] != 5 || out.Result.Reused != 0 {
		t.Fatalf("首推应建 5 条素材：%+v", out.Result)
	}
	if n := fileCount(t, e); n != 8 {
		t.Fatalf("应存 8 个文件：%d", n)
	}
	var list hub.AssetListView
	e.call("GET", "/api/assets?batch=S1", nil, &list)
	if len(list.Assets) != 5 || len(list.Facets.Batches) != 1 || list.Facets.Counts["pending"] != 5 || len(list.Statuses) != 4 {
		t.Fatalf("网页列表：%+v", list)
	}
	var wheel store.AssetDetail
	for _, a := range list.Assets {
		if a.Key == "wheel" {
			e.call("GET", "/api/assets/"+a.Code, nil, &wheel)
		}
	}
	if wheel.Kind != "part" || wheel.SessionKey != key || len(wheel.Versions) != 1 || len(wheel.Latest.Previews) != 2 || wheel.Latest.File.Mime != "model/gltf-binary" || wheel.Latest.Commit != "abc1234" {
		t.Fatalf("wheel 详情：%+v", wheel)
	}
	if resp, body := e.download(t, "/api/files/"+wheel.Latest.File.ID); resp.StatusCode != 200 || string(body[:4]) != "glTF" {
		t.Fatalf("GLB 应能下载：%d", resp.StatusCode)
	}

	// 2. 原样重推：全部未变，文件都不重传
	out, err = pushAssets(t, dir, key, "S1")
	if err != nil || countResults(out.Result)["unchanged"] != 5 || out.Result.Reused != 8 || fileCount(t, e) != 8 {
		t.Fatalf("原样重推应全部未变、不重传：%+v %v", out.Result, err)
	}

	// 3. 改了 wheel.glb：只有 wheel 记为第 2 版，只多存 1 个文件；旧版本的文件还在
	glb := filepath.Join(dir, "assets", "wheel.glb")
	b, _ := os.ReadFile(glb)
	os.WriteFile(glb, append(b, 0, 0, 0, 0), 0o600)
	out, err = pushAssets(t, dir, key, "S1")
	if err != nil || countResults(out.Result)["updated"] != 1 || countResults(out.Result)["unchanged"] != 4 || fileCount(t, e) != 9 {
		t.Fatalf("改了一个 GLB：%+v %v", out.Result, err)
	}
	var w2 store.AssetDetail
	e.call("GET", "/api/assets/"+wheel.Code, nil, &w2)
	if w2.Version != 2 || len(w2.Versions) != 2 || w2.Versions[1].File.ID != wheel.Latest.File.ID || w2.Versions[0].File.ID == wheel.Latest.File.ID {
		t.Fatalf("新旧两版都在：%+v", w2.Versions)
	}
	if resp, _ := e.download(t, "/api/files/"+wheel.Latest.File.ID); resp.StatusCode != 200 {
		t.Fatal("旧版本的原文件应保留")
	}

	// 4. 另一批次同名素材是新素材，文件按 sha256 去重不再存
	out, err = pushAssets(t, dir, key, "S2")
	if err != nil || countResults(out.Result)["created"] != 5 || out.Result.Reused != 8 || fileCount(t, e) != 9 {
		t.Fatalf("另一批次：%+v %v", out.Result, err)
	}

	// 5. GLB 超过设置里的上限：拒收并说明，不排队
	var cur map[string]any
	e.call("GET", "/api/settings", nil, &cur)
	if cur["upload_glb_mb"] != float64(50) {
		t.Fatalf("GLB 默认上限 50 MB：%v", cur["upload_glb_mb"])
	}
	cur["upload_glb_mb"] = 1
	delete(cur, "notify_events")
	delete(cur, "summary_api_key_set")
	e.call("PUT", "/api/settings", cur, &cur)
	os.WriteFile(glb, append([]byte("glTF"), make([]byte, 2<<20)...), 0o600)
	if _, err := pushAssets(t, dir, key, "S1"); err == nil || !strings.Contains(err.Error(), "GLB 模型不能超过 1 MB") {
		t.Fatalf("GLB 超限应拒收：%v", err)
	}
	if jobs := assetQueue(t); len(jobs) != 0 {
		t.Fatalf("拒收的不应留在队列：%+v", jobs)
	}

	// 6. 附件目录不可用（外置硬盘没挂上）：拒收并说明原因，不排队
	os.WriteFile(glb, b, 0o600)
	files := filepath.Join(os.Getenv("KS_HOME"), "files")
	e.hub.ConfigureFilesDir(filepath.Join(os.Getenv("KS_HOME"), "no-disk"), "")
	if _, err := pushAssets(t, dir, key, "S1"); err == nil || !strings.Contains(err.Error(), "资源库暂不收素材") || !strings.Contains(err.Error(), "不存在") {
		t.Fatalf("存储不可用应拒收：%v", err)
	}
	if jobs := assetQueue(t); len(jobs) != 0 {
		t.Fatalf("存储不可用时当场推送不排队：%+v", jobs)
	}
	e.hub.ConfigureFilesDir(files, "")
	if out, err := pushAssets(t, dir, key, "S1"); err != nil || countResults(out.Result)["unchanged"] != 4 || countResults(out.Result)["updated"] != 1 { // wheel 改回了第 1 版的内容，相对最新版有变
		t.Fatalf("存储恢复后照常推送：%+v %v", out, err)
	}
}

func assetQueue(t *testing.T) []protocol.AssetJob {
	t.Helper()
	var out struct {
		Jobs []protocol.AssetJob `json:"jobs"`
	}
	if err := client.New(5*time.Second).Do(context.Background(), "GET", "/assets/queue", nil, &out); err != nil {
		t.Fatal(err)
	}
	return out.Jobs
}

// R-351：daemon 未连接 Hub 时推送排队，连上后自动补传；暂存的文件补传后清掉。
func TestAssetPushOffline(t *testing.T) {
	e, r := setupRelay(t)
	key := assetSession(t, e, "asset-off")
	dir := copyFixture(t)

	r.setDown(true)
	e.waitFor("daemon 断开", func() bool { return !daemonConnected() })
	out, err := pushAssets(t, dir, key, "S1")
	if err != nil || !out.Queued || out.Result != nil || out.JobID == "" {
		t.Fatalf("离线时应排队：%+v %v", out, err)
	}
	if jobs := assetQueue(t); len(jobs) != 1 || jobs[0].State != "queued" || jobs[0].Items != 5 {
		t.Fatalf("队列：%+v", jobs)
	}
	// 排队后改动本机文件不影响补传的内容（已复制进暂存区）
	os.WriteFile(filepath.Join(dir, "assets", "wheel.glb"), []byte("changed"), 0o600)
	spool, _ := os.ReadDir(filepath.Join(os.Getenv("KS_HOME"), "asset-spool"))
	if len(spool) != 8 {
		t.Fatalf("暂存区应有 8 个文件：%d", len(spool))
	}
	var list hub.AssetListView
	e.call("GET", "/api/assets", nil, &list)
	if len(list.Assets) != 0 {
		t.Fatal("离线时 Hub 上还没有素材")
	}

	r.setDown(false)
	e.waitFor("补传完成", func() bool { return len(assetQueue(t)) == 0 })
	e.call("GET", "/api/assets", nil, &list)
	if len(list.Assets) != 5 {
		t.Fatalf("补传后应有 5 条素材：%d", len(list.Assets))
	}
	for _, a := range list.Assets {
		if a.Key == "wheel" && (a.Latest.File == nil || a.Latest.File.Size < 100) {
			t.Fatalf("补传的应是排队时的文件：%+v", a.Latest.File)
		}
		if a.SessionKey != key {
			t.Fatalf("提交会话：%s", a.SessionKey)
		}
	}
	if spool, _ := os.ReadDir(filepath.Join(os.Getenv("KS_HOME"), "asset-spool")); len(spool) != 0 {
		t.Fatalf("补传后暂存区应清空：%d", len(spool))
	}
}
