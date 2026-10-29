package hub

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/danielgtaylor/huma/v2"

	"github.com/NekroAI/kitestring/internal/roles"
	"github.com/NekroAI/kitestring/internal/store"
)

// 会话交接（R-224）：上下文太长时以压缩为主，换会话 / 换工具 / 换设备时才完整交接。
// 流程：用户在会话泳道或面板点「交接」→ 纸鸢通知旧会话用 handoff_write 写结构化交接（主调度先停派新活、等子任务交完）
// → 用户在项目目录运行 ks takeover <身份> → 新会话 ks role 声明身份时自动注入最新交接、回一句确认
// → 旧会话自动卸任：身份清除（Stop hook 不再拦）、标记卸任（ks wait 直接返回，不再被唤醒）。

const handoffFieldMax = 4000 // 每一栏的字数上限，超出截断

type handoffOut struct {
	Body struct {
		Handoff store.Handoff `json:"handoff"`
		Command string        `json:"command" doc:"新会话接任要运行的命令（ks takeover <身份>）"`
		Waiting int           `json:"waiting" doc:"主调度还在跑的子任务数：交接要等它们交完"`
	}
}

func (h *Hub) registerHandoffWeb(api huma.API) {
	huma.Register(api, huma.Operation{OperationID: "request-handoff", Method: http.MethodPost, Path: "/api/sessions/{key}/handoff", Tags: []string{"sessions"},
		Summary: "请会话交接（R-224）：通知它用 handoff_write 写交接，返回新会话接任要运行的命令"},
		func(ctx context.Context, in *keyPath) (*handoffOut, error) {
			return h.requestHandoff(ctx, in.Key)
		})
}

// liveAgents 列出会话还在跑的子任务名。
func liveAgents(x store.Session) []string {
	var out []string
	for _, t := range store.LiveTasks(x.Tasks, time.Now()) {
		if t.Kind == "agent" {
			name := t.Label
			if name == "" {
				name = t.AgentType
			}
			out = append(out, name)
		}
	}
	return out
}

// takeoverCmd：新会话接任的命令。
func takeoverCmd(role string) string { return "ks takeover " + role }

func (h *Hub) requestHandoff(ctx context.Context, key string) (*handoffOut, error) {
	x, err := h.store.GetSession(ctx, key)
	if err != nil {
		return nil, apiErr(err)
	}
	if x.Role == "" || x.ProjectID == "" {
		return nil, huma.Error409Conflict("这个会话没有身份，不需要交接")
	}
	if x.State == store.StateEnded {
		return nil, huma.Error409Conflict("会话已结束，没法再写交接；新会话直接 ks role " + x.Role + " 接任即可")
	}
	hd, err := h.store.RequestHandoff(ctx, x.ProjectID, x.Role, key)
	if err != nil {
		return nil, err
	}
	out := &handoffOut{}
	out.Body.Handoff, out.Body.Command = hd, takeoverCmd(x.Role)
	agents := liveAgents(x)
	if x.Role == roles.PM {
		out.Body.Waiting = len(agents)
	}
	if hd.Status == store.HandoffRequested {
		d, err := h.store.CreateDecision(ctx, store.Decision{
			Kind: store.KindRole, SessionKey: key, ProjectID: x.ProjectID, Question: "用户请你交接",
			Answer: handoffAsk(x.Role, x.ProjectName, out.Body.Waiting),
		})
		if err != nil {
			return nil, err
		}
		h.decisionsChanged(ctx, d)
	}
	h.sessionChanged(key)
	return out, nil
}

// handoffAsk：送给旧会话的交接通知。
func handoffAsk(role, project string, agents int) string {
	var b strings.Builder
	fmt.Fprintf(&b, "用户要把你在项目「%s」的%s身份交接给一个新会话（换会话接任）。请按顺序做：", project, roles.Display(role))
	n := 1
	if role == roles.PM {
		if agents > 0 {
			fmt.Fprintf(&b, "%d）先停派新活；你还有 %d 个子任务在跑，等它们交完再写交接（子任务没交完时 handoff_write 会被拒绝）；", n, agents)
		} else {
			fmt.Fprintf(&b, "%d）先停派新活；", n)
		}
		n++
	}
	fmt.Fprintf(&b, "%d）用 handoff_write 写结构化交接：doing 在做的（需求编号、做到哪）、waiting 等用户的（待答决策、待验收）、rules 用户定过的约定和规矩、next 下一步、notes 注意事项（坑、在跑的进程、别碰的东西）；没有 handoff_write 工具（MCP 是旧版）时改用命令 ks handoff，字段相同，例：ks handoff --doing \"R-12 做到联调\" --next \"交验收\" --notes \"别停 5199\"（也可 --file 交接.json 或 .md）；", n)
	fmt.Fprintf(&b, "%d）写完照常待命。用户会在项目目录运行 %s 开新会话，新会话接任后你自动卸任，之后不用再挂 ks wait。", n+1, takeoverCmd(role))
	return b.String()
}

type handoffWriteIn struct {
	Body struct {
		SessionKey string `json:"session_key" minLength:"1"`
		Doing      string `json:"doing" doc:"在做的：需求编号、做到哪"`
		Waiting    string `json:"waiting,omitempty" doc:"等用户的：待答决策、待验收、要用户决策的事"`
		Rules      string `json:"rules,omitempty" doc:"约定和规矩：用户定过的、团队里说好的"`
		Next       string `json:"next" doc:"下一步"`
		Notes      string `json:"notes,omitempty" doc:"注意事项：坑、在跑的进程、别碰的东西"`
	}
}

type messageOut struct {
	Body struct {
		Message string `json:"message"`
	}
}

func (h *Hub) registerHandoffAgent(api huma.API) {
	huma.Register(api, huma.Operation{OperationID: "agent-handoff-write", Method: http.MethodPost, Path: "/api/agent/handoff", Tags: []string{"agent"},
		Summary: "写交接（R-224）：存进 Hub、显示在会话时间线；新会话声明同一身份时自动注入"},
		func(ctx context.Context, in *handoffWriteIn) (*messageOut, error) {
			msg, err := h.writeHandoff(ctx, in)
			if err != nil {
				return nil, err
			}
			out := &messageOut{}
			out.Body.Message = msg
			return out, nil
		})
	huma.Register(api, huma.Operation{OperationID: "agent-state-note", Method: http.MethodGet, Path: "/api/agent/sessions/{key}/state-note", Tags: []string{"agent"},
		Summary: "会话在纸鸢里的关键状态（关联的需求、待答决策、在跑的子任务），压缩上下文后随身份提示词补注入（R-224）"},
		func(ctx context.Context, in *struct {
			Key    string `path:"key"`
			Source string `query:"source" doc:"SessionStart 的来源：compact（压缩后，补关键状态和子任务模型策略）、resume（恢复，只补子任务模型策略，R-228）；默认 compact"`
		}) (*messageOut, error) {
			text := ""
			if in.Source != "resume" {
				var err error
				if text, err = h.stateNote(ctx, in.Key); err != nil {
					return nil, apiErr(err)
				}
			}
			out := &messageOut{}
			out.Body.Message = text + h.roleNotes(ctx, in.Key)
			return out, nil
		})
}

func clip(s string) string {
	s = strings.TrimSpace(s)
	if r := []rune(s); len(r) > handoffFieldMax {
		return string(r[:handoffFieldMax]) + "…"
	}
	return s
}

func (h *Hub) writeHandoff(ctx context.Context, in *handoffWriteIn) (string, error) {
	b := in.Body
	x, err := h.store.GetSession(ctx, b.SessionKey)
	if err != nil {
		return "", apiErr(err)
	}
	if x.Role == "" || x.ProjectID == "" {
		return "", huma.Error409Conflict("你没有身份，不需要交接（交接给的是主调度、主助理等身份）")
	}
	if strings.TrimSpace(b.Doing) == "" && strings.TrimSpace(b.Next) == "" {
		return "", huma.Error400BadRequest("至少写上 doing（在做的）和 next（下一步）")
	}
	if x.Role == roles.PM {
		if a := liveAgents(x); len(a) > 0 {
			return "", huma.Error409Conflict(fmt.Sprintf("你还有 %d 个子任务在跑（%s）：先停派新活，等它们交完再写交接", len(a), strings.Join(a, "、")))
		}
	}
	hd, err := h.store.WriteHandoff(ctx, x.ProjectID, x.Role, x.Key, store.Handoff{Doing: clip(b.Doing), Waiting: clip(b.Waiting), Rules: clip(b.Rules), Next: clip(b.Next), Notes: clip(b.Notes)})
	if err != nil {
		return "", err
	}
	_ = hd
	h.sessionChanged(x.Key)
	h.broker.publish("sessions")
	return fmt.Sprintf("交接已存，用户在你的会话时间线上能看到。用户会在项目目录运行 %s 开新会话；新会话声明%s时自动读到这份交接，你随即卸任。在那之前照常待命；要补充就再调一次 handoff_write（覆盖这一份）。", takeoverCmd(x.Role), roles.Display(x.Role)), nil
}

// handoffText：新会话接任时注入的交接内容。
func handoffText(hd store.Handoff, role string) string {
	var b strings.Builder
	from := hd.FromTitle
	if from == "" {
		from = hd.FromSession
	}
	at := ""
	if hd.WrittenAt != nil {
		at = "，写于 " + hd.WrittenAt.Local().Format("01-02 15:04")
	}
	fmt.Fprintf(&b, "\n## 交接（上一任%s「%s」写的%s）\n\n", roles.Display(role), from, at)
	for _, s := range []struct{ k, v string }{{"在做的", hd.Doing}, {"等用户的", hd.Waiting}, {"约定和规矩", hd.Rules}, {"下一步", hd.Next}, {"注意事项", hd.Notes}} {
		if strings.TrimSpace(s.v) != "" {
			fmt.Fprintf(&b, "### %s\n%s\n\n", s.k, strings.TrimSpace(s.v))
		}
	}
	b.WriteString("上一任已自动卸任。读完先回用户一句确认：已接任" + roles.Display(role) + "、接手了哪几件事、下一步做什么；然后按交接继续。\n")
	return b.String()
}

// takeOver：会话声明身份后接任同项目同身份的最新交接；旧会话卸任（身份清除、标记卸任）并收到通知。返回要注入的交接内容。
func (h *Hub) takeOver(ctx context.Context, x store.Session, role string, demoted []string) (string, []string) {
	if role == "" || x.ProjectID == "" {
		return "", demoted
	}
	hd, err := h.store.TakeHandoff(ctx, x.ProjectID, role, x.Key)
	if err != nil {
		if !errors.Is(err, store.ErrNotFound) {
			h.log.Warn("读取交接失败", "err", err)
		}
		return "", demoted
	}
	old, err := h.store.GetSession(ctx, hd.FromSession)
	if err == nil {
		if old.Role == role {
			// 主调度 / 主助理以外的身份不做唯一性处理，这里替旧会话卸下
			if _, err := h.store.SetRole(ctx, old.Key, ""); err != nil {
				h.log.Warn("清除旧会话身份失败", "err", err)
			}
			found := false
			for _, k := range demoted {
				found = found || k == old.Key
			}
			if !found {
				demoted = append(demoted, old.Key)
			}
		}
		if err := h.store.SetRetired(ctx, old.Key, true); err != nil {
			h.log.Warn("标记旧会话卸任失败", "err", err)
		}
		h.pushRoles(ctx, old.DeviceID)
		h.sessionChanged(old.Key)
	}
	return handoffText(hd, role), demoted
}

// retiredNote：交接后卸任的旧会话收到的通知。
func retiredNote(project, role, by string) string {
	return fmt.Sprintf("你在项目「%s」的%s身份已交接给「%s」，你已卸任：不要再挂 ks wait，用一两句话收尾后结束本轮。之后纸鸢不会再唤醒你，也不会再拦你结束。", project, roles.Display(role), by)
}

// stateNote：压缩上下文后补注入的关键状态——关联的需求、待答决策、在跑的子任务（R-224）。都没有时返回空。
func (h *Hub) stateNote(ctx context.Context, key string) (string, error) {
	x, err := h.store.GetSession(ctx, key)
	if err != nil {
		return "", err
	}
	var reqs, decs, tasks []string
	for _, r := range x.Requirements {
		if r.Status == store.ReqDone || r.Status == store.ReqShelved {
			continue
		}
		reqs = append(reqs, fmt.Sprintf("%s「%s」%s", r.Code, r.Title, reqStatusName[r.Status]))
	}
	list, err := h.store.ListDecisions(ctx, []string{store.DecPending}, -1)
	if err != nil {
		return "", err
	}
	for _, d := range list {
		if d.SessionKey == key || d.OriginSession == key {
			decs = append(decs, fmt.Sprintf("%s「%s」", d.Code, oneLineClip(store.DecisionTitle(d), 60)))
		}
	}
	now := time.Now()
	for _, t := range store.LiveTasks(x.Tasks, now) {
		kind := "子任务"
		if t.Kind == "shell" {
			kind = "后台 shell"
		}
		label := t.Label
		if label == "" {
			label = t.AgentType
		}
		tasks = append(tasks, fmt.Sprintf("%s「%s」（已跑 %d 分钟）", kind, oneLineClip(label, 60), int(now.Sub(t.StartedAt).Minutes())))
	}
	if len(reqs)+len(decs)+len(tasks) == 0 {
		return "", nil
	}
	var b strings.Builder
	b.WriteString("[Kitestring] 上下文刚压缩过，补上你在纸鸢里的关键状态（以这里为准，摘要里漏了的照这里补）：\n")
	for _, s := range []struct {
		k  string
		vs []string
	}{{"关联的需求", reqs}, {"等用户答复的决策", decs}, {"在跑的子任务 / 后台命令", tasks}} {
		if len(s.vs) > 0 {
			fmt.Fprintf(&b, "- %s：%s\n", s.k, strings.Join(s.vs, "；"))
		}
	}
	return b.String(), nil
}

var reqStatusName = map[string]string{
	store.ReqDraft: "草稿", store.ReqReady: "待开始", store.ReqInProgress: "进行中", store.ReqBlocked: "受阻", store.ReqReview: "待验收",
}

func oneLineClip(s string, n int) string {
	s = strings.Join(strings.Fields(s), " ")
	if r := []rune(s); len(r) > n {
		return string(r[:n]) + "…"
	}
	return s
}

// projectPolicyNote：项目 projectID 的子任务模型策略对 role 的提示；项目取不到时按默认（均衡）。
func (h *Hub) projectPolicyNote(ctx context.Context, projectID, role string) string {
	policy := ""
	if projectID != "" {
		if p, err := h.store.GetProject(ctx, projectID); err == nil {
			policy = p.ModelPolicy
		}
	}
	return roles.PolicyNote(role, policy)
}
