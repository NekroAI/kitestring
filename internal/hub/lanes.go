package hub

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"sync"
	"time"

	"github.com/danielgtaylor/huma/v2"

	"github.com/NekroAI/kitestring/internal/protocol"
	"github.com/NekroAI/kitestring/internal/store"
)

// 工作台泳道时间线（R-205）：每条事件只更新它所在那一分钟（和长命令 / 待命 / 子任务名单），
// 300ms 内的变化合并成一条 lane 推送，只带变了的格子；网页按分钟合并，不再整条会话重发。

type laneState struct {
	mu      sync.Mutex
	tracker *store.LaneTracker

	qmu    sync.Mutex
	mins   map[string]store.LaneMinute
	spans  map[string]store.LaneSpan
	agents map[string]store.LaneAgent
	tmr    *time.Timer
}

func newLaneState() *laneState {
	return &laneState{tracker: store.NewLaneTracker(), mins: map[string]store.LaneMinute{}, spans: map[string]store.LaneSpan{}, agents: map[string]store.LaneAgent{}}
}

// LanePush 是一条实时推送：变了的格子、时段与子任务。
type LanePush struct {
	Minutes []store.LaneMinute `json:"m"`
	Spans   []store.LaneSpan   `json:"s"`
	Agents  []store.LaneAgent  `json:"g"`
}

var lanePushDelay = 300 * time.Millisecond

// laneEvent 把一条（新落库的）事件记进时间线汇总。失败只记日志，不影响事件本身的处理。
func (h *Hub) laneEvent(ctx context.Context, key string, ev protocol.Event) {
	l := h.lanes
	l.mu.Lock()
	e := l.tracker.Apply(key, ev)
	l.mu.Unlock()
	if e.Unmatched != "" {
		// hub 重启过、内存里没有这次调用的开始：去事件记录里找
		if pre, ok := h.store.PreToolStart(ctx, key, e.Unmatched, ev.Time); ok {
			label := toolLabel(pre.ToolName, pre.ToolBrief)
			if sp, ok := store.LongSpan(key, pre.AgentID, e.Unmatched, label, pre.Time, ev.Time); ok {
				e.Spans = append(e.Spans, sp)
			}
		}
	}
	w, err := h.store.ApplyLane(ctx, e)
	if err != nil {
		h.log.Warn("记录泳道时间线失败", "err", err)
		return
	}
	if e.Started != "" {
		// 20 秒后还没结束：推一次“还在跑的长命令”，网页从开始画到现在
		ref := e.Started
		time.AfterFunc(store.LongTool+50*time.Millisecond, func() {
			l.mu.Lock()
			sp, ok := l.tracker.Open(key, ref)
			l.mu.Unlock()
			if ok {
				h.lanePush(store.LaneWrite{Spans: []store.LaneSpan{sp}})
			}
		})
	}
	h.lanePush(w)
}

// lanePush 把变化攒 300ms 再推；同一格只推最新值。
func (h *Hub) lanePush(w store.LaneWrite) {
	if w.Minute == nil && len(w.Spans) == 0 && len(w.Agents) == 0 {
		return
	}
	l := h.lanes
	l.qmu.Lock()
	defer l.qmu.Unlock()
	if m := w.Minute; m != nil {
		l.mins[fmt.Sprintf("%s\x00%s\x00%d", m.Key, m.Agent, m.Minute)] = *m
	}
	for _, s := range w.Spans {
		l.spans[s.Key+"\x00"+s.Kind+"\x00"+s.Ref] = s
	}
	for _, a := range w.Agents {
		l.agents[a.Key+"\x00"+a.Agent] = a
	}
	if l.tmr != nil {
		return
	}
	l.tmr = time.AfterFunc(lanePushDelay, func() {
		l.qmu.Lock()
		p := LanePush{Minutes: []store.LaneMinute{}, Spans: []store.LaneSpan{}, Agents: []store.LaneAgent{}}
		for _, m := range l.mins {
			p.Minutes = append(p.Minutes, m)
		}
		for _, s := range l.spans {
			p.Spans = append(p.Spans, s)
		}
		for _, a := range l.agents {
			p.Agents = append(p.Agents, a)
		}
		l.mins, l.spans, l.agents = map[string]store.LaneMinute{}, map[string]store.LaneSpan{}, map[string]store.LaneAgent{}
		l.tmr = nil
		l.qmu.Unlock()
		h.broker.publishLane(p)
	})
}

func (b *broker) publishLane(p LanePush) {
	data, _ := json.Marshal(p)
	b.send(func(int64) string { return fmt.Sprintf("event: lane\ndata: %s\n\n", data) })
}

// laneMaintenance：启动时和之后每小时把 30 天前的分钟压成小时级，丢掉一天还没结束的工具调用。
func (h *Hub) laneMaintenance(ctx context.Context) {
	run := func() {
		if n, err := h.store.CompactLanes(ctx, time.Now()); err != nil {
			h.log.Warn("压缩泳道时间线失败", "err", err)
		} else if n > 0 {
			h.log.Info("已把 30 天前的泳道时间线压成小时级", "minutes", n)
		}
		h.lanes.mu.Lock()
		h.lanes.tracker.Expire(time.Now().Add(-24 * time.Hour))
		h.lanes.mu.Unlock()
	}
	run()
	t := time.NewTicker(time.Hour)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			run()
		}
	}
}

// ---------- 接口 ----------

type lanesIn struct {
	From int64 `query:"from" doc:"范围起点，Unix 毫秒；不填为一小时前。最早 30 天前"`
	To   int64 `query:"to" doc:"范围终点，Unix 毫秒；不填为现在"`
	Step int   `query:"step" enum:"1,60" default:"1" doc:"格子大小（分钟）：1 每分钟（近 1 小时 / 6 小时 / 1 天档），60 每小时（近 1 周档）"`
}

type lanesOut struct{ Body store.LaneData }

func (h *Hub) getLanes(ctx context.Context, in *lanesIn) (*lanesOut, error) {
	now := time.Now()
	to := now.Add(time.Minute) // 含当前这一分钟
	if in.To > 0 && in.To < to.UnixMilli() {
		to = time.UnixMilli(in.To)
	}
	from := now.Add(-time.Hour)
	if in.From > 0 {
		from = time.UnixMilli(in.From)
	}
	if from.Before(now.Add(-store.LaneKeep - 24*time.Hour)) {
		return nil, huma.Error400BadRequest("from 最早 30 天前")
	}
	if !from.Before(to) {
		return nil, huma.Error400BadRequest("from 要早于 to")
	}
	d, err := h.store.Lanes(ctx, from, to, in.Step == 60)
	if err != nil {
		return nil, err
	}
	h.lanes.mu.Lock()
	open := h.lanes.tracker.Pending(now)
	h.lanes.mu.Unlock()
	d.Spans = append(d.Spans, open...)
	return &lanesOut{Body: d}, nil
}

func (h *Hub) registerLanesWeb(api huma.API) {
	huma.Register(api, huma.Operation{OperationID: "get-lanes", Method: http.MethodGet, Path: "/api/lanes", Tags: []string{"sessions"},
		Summary: "工作台泳道时间线：每个会话、每个子任务每分钟的调用次数，长命令与待命时段，子任务名单（R-205）"}, h.getLanes)
}
