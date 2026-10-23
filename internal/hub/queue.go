package hub

import (
	"context"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/NekroAI/kitestring/internal/store"
)

// 待开始的需求排进会话的待办（R-66）。
// 一条待开始的需求，归它关联的会话里最近活跃、还没结束 / 没被你收起的那个；没有这样的会话就是“没人接手”，
// 在网页上提示你交出，不自动派给别的会话。
// 会话空闲（一轮结束或挂起等待）时，把排给它的需求整批送过去（最多 maxBatch 条），让它先通读、
// 合并能一起做的、标出重复和依赖再动手；挂着 ks wait 的会话会被立刻唤醒，其它会话在你下次和它对话时收到。
// 只有你放到待开始（或交出）的需求会排进来：Agent 自己记下的需求停在草稿，等你确认。

const maxBatch = 10

var prioRank = map[string]int{"P0": 0, "P1": 1, "P2": 2, "P3": 3}

// queueOwner 返回待开始需求排给哪个会话；没有可接手的会话时返回空。
func (h *Hub) queueOwner(ctx context.Context, r store.Requirement) string {
	var best store.Session
	for _, k := range r.Sessions {
		x, err := h.store.GetSession(ctx, k)
		if err != nil || x.State == store.StateEnded || x.State == store.StateLost || x.DoneAt != nil {
			continue
		}
		if best.Key == "" || x.LastActiveAt.After(best.LastActiveAt) {
			best = x
		}
	}
	return best.Key
}

// queued 列出排给 sessionKey、还没送出的待开始需求，按优先级、编号排序。
func (h *Hub) queued(ctx context.Context, sessionKey string) ([]store.Requirement, error) {
	all, err := h.store.ListRequirements(ctx, "", []string{store.ReqReady})
	if err != nil {
		return nil, err
	}
	off := map[string]bool{}
	var out []store.Requirement
	for _, r := range all {
		if r.OfferedTo != "" || r.StageStatus == store.StageLocked {
			continue
		}
		if r.ProjectID != "" {
			on, seen := off[r.ProjectID]
			if !seen {
				p, err := h.store.GetProject(ctx, r.ProjectID)
				on = err == nil && !p.AutoDispatch
				off[r.ProjectID] = on
			}
			if on {
				continue
			}
		}
		if h.queueOwner(ctx, r) == sessionKey {
			out = append(out, r)
		}
	}
	sort.SliceStable(out, func(i, j int) bool {
		a, b := prioRank[out[i].Priority], prioRank[out[j].Priority]
		if a != b {
			return a < b
		}
		return codeNum(out[i].Code) < codeNum(out[j].Code)
	})
	return out, nil
}

func codeNum(code string) int {
	n, _ := strconv.Atoi(strings.TrimPrefix(code, "R-"))
	return n
}

// brief 把说明压成一行，最多 n 个字。
func brief(s string, n int) string {
	s = strings.Join(strings.Fields(s), " ")
	if utf8.RuneCountInString(s) <= n {
		return s
	}
	return string([]rune(s)[:n]) + "…"
}

// dispatch 把排给会话的待开始需求整批送过去。会话已结束 / 被收起时不送。
func (h *Hub) dispatch(ctx context.Context, sessionKey string) {
	if sessionKey == "" {
		return
	}
	x, err := h.store.GetSession(ctx, sessionKey)
	if err != nil || x.State == store.StateEnded || x.State == store.StateLost || x.DoneAt != nil {
		return
	}
	list, err := h.queued(ctx, sessionKey)
	if err != nil || len(list) == 0 {
		return
	}
	if len(list) > maxBatch {
		list = list[:maxBatch]
	}
	for _, r := range list {
		parts := []string{r.Priority}
		if r.Kind == "design" {
			parts = append(parts, "设计类（需要时交方案图让用户选）")
		}
		if d := brief(r.Detail, 60); d != "" {
			parts = append(parts, d)
		}
		if _, err := h.store.CreateDecision(ctx, store.Decision{
			Kind: store.KindQueue, SessionKey: x.Key, ProjectID: r.ProjectID, RequirementID: r.ID, Question: r.Title, Answer: strings.Join(parts, " · "),
		}); err != nil {
			h.log.Warn("排队送达失败", "err", err)
			continue
		}
		_ = h.store.SetOffered(ctx, r.ID, x.Key)
	}
	h.pushDecisions(ctx, x.DeviceID)
	h.broker.publish("requirements", "decisions", "sessions")
}

// kickDelay：你连着把几条需求放到待开始时攒一攒，让空闲的会话一次收到整批，而不是被第一条叫醒。
var kickDelay = 4 * time.Second

// SetKickDelay 调整攒批的等待时间（测试用）。
func SetKickDelay(d time.Duration) { kickDelay = d }

// kick 在需求变为待开始（或关联了会话）后调用：排给的会话如果已经空闲，稍等片刻后整批送过去。
// 正在运行的会话等这一轮结束再送，不打断它。
func (h *Hub) kick(ctx context.Context, ref string) {
	r, err := h.store.GetRequirement(ctx, ref)
	if err != nil || r.Status != store.ReqReady || r.OfferedTo != "" {
		return
	}
	owner := h.queueOwner(ctx, r)
	if owner == "" {
		return
	}
	x, err := h.store.GetSession(ctx, owner)
	if err != nil || x.State == store.StateRunning || x.State == store.StateBackground {
		return
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	if t := h.kicks[owner]; t != nil {
		t.Reset(kickDelay)
		return
	}
	h.kicks[owner] = time.AfterFunc(kickDelay, func() {
		h.mu.Lock()
		delete(h.kicks, owner)
		h.mu.Unlock()
		ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
		defer cancel()
		h.dispatch(ctx, owner)
	})
}
