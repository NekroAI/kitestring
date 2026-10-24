package hub

import (
	"context"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/danielgtaylor/huma/v2"

	"github.com/NekroAI/kitestring/internal/protocol"
	"github.com/NekroAI/kitestring/internal/store"
)

// 孤儿进程（R-209）：各设备 daemon 上报会话结束后还在跑（或已脱离 Agent）的后台进程，工作台列出，可确认后停止。
// 停止只经 Hub 发给进程所在设备的 daemon，由 daemon 核对 pid + 启动时间、确认是它记过的孤儿 / 已脱离进程后才发信号；
// Hub 这边也只转发自己列表里有的条目。每次停止都写进 proc_stops 留痕。

const stopWait = 15 * time.Second // SIGTERM 后 daemon 最多等 5 秒，留足余量

type deviceOrphans struct {
	list []protocol.OrphanProc
	at   time.Time
}

func (h *Hub) setOrphans(deviceID string, list []protocol.OrphanProc) {
	h.mu.Lock()
	h.orphans[deviceID] = deviceOrphans{list: list, at: time.Now()}
	h.mu.Unlock()
	h.broker.publish("orphans")
}

func (h *Hub) stopDone(r protocol.StopResult) {
	h.mu.Lock()
	ch := h.stops[r.ReqID]
	delete(h.stops, r.ReqID)
	h.mu.Unlock()
	if ch != nil {
		ch <- r
	}
}

// Orphan 是工作台上的一行。
type Orphan struct {
	protocol.OrphanProc
	DeviceID     string `json:"device_id"`
	DeviceName   string `json:"device_name"`
	DeviceOnline bool   `json:"device_online"`
	Local        bool   `json:"local" doc:"进程在 Hub 所在的这台机器上"`
	ProjectID    string `json:"project_id,omitempty"`
	ProjectName  string `json:"project_name,omitempty"`
	SessionTitle string `json:"session_title,omitempty"`
	SessionEnded bool   `json:"session_ended"`
}

type orphansOut struct {
	Body struct {
		Orphans []Orphan         `json:"orphans"`
		Stops   []store.ProcStop `json:"stops" doc:"最近 24 小时的停止记录（新的在前）"`
	}
}

func (h *Hub) listOrphans(ctx context.Context, _ *struct{}) (*orphansOut, error) {
	out := &orphansOut{}
	out.Body.Orphans = []Orphan{}
	devs, err := h.store.ListDevices(ctx)
	if err != nil {
		return nil, apiErr(err)
	}
	host, _ := os.Hostname()
	byID := map[string]store.Device{}
	for _, d := range devs {
		byID[d.ID] = d
	}
	h.mu.Lock()
	snap := make(map[string]deviceOrphans, len(h.orphans))
	online := map[string]bool{}
	for id, o := range h.orphans {
		snap[id] = o
		_, online[id] = h.conns[id]
	}
	h.mu.Unlock()
	for devID, set := range snap {
		dev, ok := byID[devID]
		if !ok || dev.Revoked {
			continue
		}
		for _, o := range set.list {
			x := Orphan{OrphanProc: o, DeviceID: devID, DeviceName: dev.Name, DeviceOnline: online[devID], Local: host != "" && strings.EqualFold(dev.Hostname, host)}
			h.enrichOrphan(ctx, &x)
			out.Body.Orphans = append(out.Body.Orphans, x)
		}
	}
	sortOrphans(out.Body.Orphans)
	stops, err := h.store.ProcStopsSince(ctx, time.Now().Add(-24*time.Hour))
	if err != nil {
		return nil, apiErr(err)
	}
	out.Body.Stops = stops
	return out, nil
}

// enrichOrphan 补上项目名与父会话标题：先认会话，认不到按设备上登记过的项目目录，再不行用目录名。
func (h *Hub) enrichOrphan(ctx context.Context, x *Orphan) {
	if x.SessionKey != "" {
		if s, err := h.store.GetSession(ctx, x.SessionKey); err == nil {
			x.ProjectID, x.ProjectName = s.ProjectID, s.ProjectName
			x.SessionTitle = s.Title
			if x.SessionTitle == "" {
				x.SessionTitle = s.LastPrompt
			}
			x.SessionEnded = s.EndedAt != nil
		}
	}
	if x.ProjectName == "" {
		x.ProjectID, x.ProjectName = h.store.ProjectForPath(ctx, x.DeviceID, x.Cwd)
	}
	if x.ProjectName == "" && x.Cwd != "" {
		x.ProjectName = filepath.Base(x.Cwd)
	}
}

func sortOrphans(list []Orphan) {
	// 孤儿在前，同类按开始时间（久的在前）
	rank := func(o Orphan) int {
		if o.State == protocol.ProcOrphan {
			return 0
		}
		return 1
	}
	for i := 1; i < len(list); i++ {
		for j := i; j > 0; j-- {
			a, b := list[j-1], list[j]
			if rank(a) < rank(b) || rank(a) == rank(b) && !b.StartedAt.Before(a.StartedAt) {
				break
			}
			list[j-1], list[j] = b, a
		}
	}
}

type stopOrphanIn struct {
	Body struct {
		DeviceID string `json:"device_id"`
		ID       string `json:"id" doc:"根进程“进程号@启动时间”，取自列表"`
		Force    bool   `json:"force,omitempty" doc:"true = SIGKILL（SIGTERM 后仍在运行、你确认强制时）"`
	}
}

type stopOrphanOut struct {
	Body store.ProcStop
}

func (h *Hub) stopOrphan(ctx context.Context, in *stopOrphanIn) (*stopOrphanOut, error) {
	b := in.Body
	h.mu.Lock()
	var target *protocol.OrphanProc
	for _, o := range h.orphans[b.DeviceID].list {
		if o.ID == b.ID {
			o := o
			target = &o
			break
		}
	}
	c := h.conns[b.DeviceID]
	h.mu.Unlock()
	if target == nil {
		return nil, huma.Error404NotFound("列表里没有这个进程（可能已经结束），刷新看看")
	}
	if c == nil {
		return nil, huma.Error409Conflict("设备离线：只能由进程所在设备的 daemon 停止")
	}
	dev, _ := h.store.GetDevice(ctx, b.DeviceID)
	reqID := protocol.NewID()
	ch := make(chan protocol.StopResult, 1)
	h.mu.Lock()
	h.stops[reqID] = ch
	h.mu.Unlock()
	c.send(protocol.Envelope{Type: protocol.MsgStopProc, StopProc: &protocol.StopProc{ReqID: reqID, Pid: target.Pid, Start: target.Start, Force: b.Force}})
	var res protocol.StopResult
	select {
	case res = <-ch:
	case <-time.After(stopWait):
		h.mu.Lock()
		delete(h.stops, reqID)
		h.mu.Unlock()
		res = protocol.StopResult{Result: protocol.StopFailed, Detail: "设备没有回音，不知道停没停下（可能刚断线）"}
	case <-ctx.Done():
		h.mu.Lock()
		delete(h.stops, reqID)
		h.mu.Unlock()
		return nil, ctx.Err()
	}
	x := Orphan{OrphanProc: *target, DeviceID: b.DeviceID}
	h.enrichOrphan(ctx, &x)
	rec, err := h.store.AddProcStop(context.WithoutCancel(ctx), store.ProcStop{
		DeviceID: b.DeviceID, DeviceName: dev.Name, ProcID: target.ID, Pid: target.Pid, Cmd: target.Cmd, Project: x.ProjectName,
		SessionKey: target.SessionKey, State: target.State, By: "user", Signal: res.Signal, Result: res.Result, Detail: res.Detail,
	})
	if err != nil {
		return nil, apiErr(err)
	}
	h.log.Info("停止后台进程", "device", dev.Name, "pid", target.Pid, "cmd", target.Cmd, "signal", res.Signal, "result", res.Result)
	h.broker.publish("orphans")
	return &stopOrphanOut{Body: rec}, nil
}

func (h *Hub) registerOrphansWeb(api huma.API) {
	huma.Register(api, huma.Operation{OperationID: "list-orphans", Method: http.MethodGet, Path: "/api/orphans", Tags: []string{"sessions"}, Summary: "会话结束后还在跑（或已脱离 Agent）的后台进程，与最近的停止记录（R-209）"}, h.listOrphans)
	huma.Register(api, huma.Operation{OperationID: "stop-orphan", Method: http.MethodPost, Path: "/api/orphans/stop", Tags: []string{"sessions"}, Summary: "停止一个孤儿 / 已脱离的后台进程：经 Hub 交给所在设备的 daemon 核对后发信号（R-209）"}, h.stopOrphan)
}
