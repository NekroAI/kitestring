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
