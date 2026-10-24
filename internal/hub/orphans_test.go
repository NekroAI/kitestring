package hub

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"path/filepath"
	"testing"

	"github.com/danielgtaylor/huma/v2"

	"github.com/NekroAI/kitestring/internal/protocol"
	"github.com/NekroAI/kitestring/internal/store"
)

// R-209：Hub 只转发自己列表里有的条目，设备离线不转发；daemon 回的结果写进停止记录。
func TestStopOrphanRelay(t *testing.T) {
	st, err := store.Open(filepath.Join(t.TempDir(), "hub.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	h := New(st, slog.New(slog.NewTextHandler(io.Discard, nil)))
	ctx := context.Background()
	dev, err := st.CreateDevice(ctx, "工作机", "hash")
	if err != nil {
		t.Fatal(err)
	}
	o := protocol.OrphanProc{ID: "47505@Sun Oct 18 01:27:31 2026", Pid: 47505, Start: "Sun Oct 18 01:27:31 2026", State: protocol.ProcOrphan, Cmd: "pnpm dev --port 5199", Cwd: "/p/nekro-qantrip/apps/cloud"}
	h.setOrphans(dev.ID, []protocol.OrphanProc{o})
	stop := func(id string, force bool) (*stopOrphanOut, error) {
		in := &stopOrphanIn{}
		in.Body.DeviceID, in.Body.ID, in.Body.Force = dev.ID, id, force
		return h.stopOrphan(ctx, in)
	}
	status := func(err error) int {
		var se huma.StatusError
		if errors.As(err, &se) {
			return se.GetStatus()
		}
		return 0
	}
	// 设备离线：不转发
	if _, err := stop(o.ID, false); status(err) != 409 {
		t.Fatalf("设备离线应返回 409：%v", err)
	}
	cctx, cancel := context.WithCancel(ctx)
	defer cancel()
	c := &deviceConn{dev: dev, out: make(chan protocol.Envelope, 4), done: cctx.Done(), cancel: cancel}
	h.mu.Lock()
	h.conns[dev.ID] = c
	h.mu.Unlock()
	// 列表里没有的进程（例如网页上拼出来的 pid）：不转发
	if _, err := stop("1@Mon Jan 1 00:00:00 2001", false); status(err) != 404 {
		t.Fatalf("列表里没有的应返回 404：%v", err)
	}
	if len(c.out) != 0 {
		t.Fatal("核对不通过时不该给 daemon 发停止")
	}
	// 假 daemon：收到停止就回“SIGTERM 后退出”
	go func() {
		e := <-c.out
		if e.Type != protocol.MsgStopProc || e.StopProc.Pid != o.Pid || e.StopProc.Start != o.Start || e.StopProc.Force {
			t.Errorf("转发内容不对：%+v", e.StopProc)
		}
		h.stopDone(protocol.StopResult{ReqID: e.StopProc.ReqID, Result: protocol.StopExited, Signal: "SIGTERM"})
	}()
	out, err := stop(o.ID, false)
	if err != nil {
		t.Fatal(err)
	}
	if r := out.Body; r.Result != protocol.StopExited || r.Signal != "SIGTERM" || r.DeviceName != "工作机" || r.By != "user" || r.Project != "cloud" || r.Pid != 47505 {
		t.Fatalf("停止记录不对：%+v", r)
	}
	list, err := h.listOrphans(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(list.Body.Stops) != 1 || len(list.Body.Orphans) != 1 || !list.Body.Orphans[0].DeviceOnline {
		t.Fatalf("列表 / 记录不对：%+v", list.Body)
	}
}

// R-209 退回意见：忽略只对孤儿；按 pid + 启动时间记，pid 复用的新进程不受影响；Hub 重启后仍有效；取消忽略、进程退出后失效；都留痕。
func TestIgnoreOrphan(t *testing.T) {
	path := filepath.Join(t.TempDir(), "hub.db")
	st, err := store.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	h := New(st, slog.New(slog.NewTextHandler(io.Discard, nil)))
	ctx := context.Background()
	dev, err := st.CreateDevice(ctx, "工作机", "hash")
	if err != nil {
		t.Fatal(err)
	}
	orphan := protocol.OrphanProc{ID: "47505@Sun Oct 18 01:27:31 2026", Pid: 47505, Start: "Sun Oct 18 01:27:31 2026", State: protocol.ProcOrphan, Cmd: "pnpm dev --port 5199"}
	detached := protocol.OrphanProc{ID: "300@Sat Oct 24 09:00:00 2026", Pid: 300, Start: "Sat Oct 24 09:00:00 2026", State: protocol.ProcDetached, Cmd: "node server.js"}
	h.setOrphans(dev.ID, []protocol.OrphanProc{orphan, detached})
	ignore := func(h *Hub, id string, undo bool) (*ignoreOrphanOut, error) {
		in := &ignoreOrphanIn{}
		in.Body.DeviceID, in.Body.ID, in.Body.Undo = dev.ID, id, undo
		return h.ignoreOrphan(ctx, in)
	}
	list := func(h *Hub) (vis, ign []string) {
		out, err := h.listOrphans(ctx, nil)
		if err != nil {
			t.Fatal(err)
		}
		for _, o := range out.Body.Orphans {
			vis = append(vis, o.ID)
		}
		for _, o := range out.Body.Ignored {
			ign = append(ign, o.ID)
		}
		return
	}
	if _, err := ignore(h, detached.ID, false); err == nil {
		t.Fatal("已脱离（会话还在）的不能忽略")
	}
	rec, err := ignore(h, orphan.ID, false)
	if err != nil {
		t.Fatal(err)
	}
	if rec.Body.By != "user" || rec.Body.DeviceName != "工作机" || rec.Body.Pid != 47505 {
		t.Fatalf("忽略没留痕：%+v", rec.Body)
	}
	if vis, ign := list(h); len(vis) != 1 || vis[0] != detached.ID || len(ign) != 1 || ign[0] != orphan.ID {
		t.Fatalf("忽略后应只剩已脱离的那个，被忽略的单独列：%v %v", vis, ign)
	}
	// Hub 重启：换一个 Hub、同一个库，daemon 重新上报后仍是忽略的
	st.Close()
	st2, err := store.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer st2.Close()
	h2 := New(st2, slog.New(slog.NewTextHandler(io.Discard, nil)))
	h2.setOrphans(dev.ID, []protocol.OrphanProc{orphan, detached})
	if vis, ign := list(h2); len(vis) != 1 || len(ign) != 1 {
		t.Fatalf("重启后忽略应仍有效：%v %v", vis, ign)
	}
	// 取消忽略
	if _, err := ignore(h2, orphan.ID, true); err != nil {
		t.Fatal(err)
	}
	if vis, ign := list(h2); len(vis) != 2 || len(ign) != 0 {
		t.Fatalf("取消忽略后应重新列出：%v %v", vis, ign)
	}
	var undoneBy string
	_ = st2.DB().QueryRow(`SELECT undone_by FROM proc_ignores WHERE proc_id = ?`, orphan.ID).Scan(&undoneBy)
	if undoneBy != "user" {
		t.Fatal("取消忽略没留痕")
	}
	// 再忽略；进程退出（列表里没了），pid 被复用的新进程（同 pid、不同启动时间）照常列出
	if _, err := ignore(h2, orphan.ID, false); err != nil {
		t.Fatal(err)
	}
	reused := orphan
	reused.ID, reused.Start = "47505@Sat Oct 24 12:00:00 2026", "Sat Oct 24 12:00:00 2026"
	h2.setOrphans(dev.ID, []protocol.OrphanProc{reused})
	if vis, ign := list(h2); len(vis) != 1 || vis[0] != reused.ID || len(ign) != 0 {
		t.Fatalf("pid 复用的新进程不该受忽略影响：%v %v", vis, ign)
	}
	var ended int
	_ = st2.DB().QueryRow(`SELECT COUNT(*) FROM proc_ignores WHERE ended_at IS NOT NULL`).Scan(&ended)
	if ended != 1 {
		t.Fatal("进程退出后忽略应失效")
	}
}
