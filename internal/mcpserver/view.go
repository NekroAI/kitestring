package mcpserver

import (
	"fmt"
	"strings"
	"time"

	"github.com/NekroAI/kitestring/internal/protocol"
	"github.com/NekroAI/kitestring/internal/roles"
	"github.com/NekroAI/kitestring/internal/summary"
)

// 需求的精简视图（R-57）：写操作只回一行确认，req_get 默认按行输出要点，req_list 每条一行。
// 完整 JSON 只在 req_get full = true 时返回。

type fileView struct {
	ID        string `json:"id"`
	Name      string `json:"name"`
	Mime      string `json:"mime"`
	Size      int64  `json:"size"`
	Path      string `json:"path"`       // 下载到本机的路径（R-221）
	PathError string `json:"path_error"` // 没能下载时的原因
}

// fileLine 是一个附件的一行说明：「名字（大小）：本机路径」。
func fileLine(f fileView) string {
	s := f.Name
	if s == "" {
		s = f.ID
	}
	if f.Size > 0 {
		s += "（" + sizeText(f.Size) + "）"
	}
	switch {
	case f.Path != "":
		s += "：" + f.Path
	case f.PathError != "":
		s += "：未能下载到本机（" + f.PathError + "），附件 ID " + f.ID
	}
	return s
}

func sizeText(n int64) string {
	switch {
	case n >= 1<<20:
		return fmt.Sprintf("%.1f MB", float64(n)/(1<<20))
	case n >= 1<<10:
		return fmt.Sprintf("%.1f KB", float64(n)/(1<<10))
	}
	return fmt.Sprintf("%d B", n)
}

// filesBlock 把一组附件写成缩进的若干行。
func filesBlock(b *strings.Builder, indent string, fs []fileView) {
	for _, f := range fs {
		b.WriteString(indent + "附件 " + fileLine(f) + "\n")
	}
}

type reqView struct {
	Notices          []string   `json:"notices"`
	Code             string     `json:"code"`
	Title            string     `json:"title"`
	Detail           string     `json:"detail"`
	Status           string     `json:"status"`
	Priority         string     `json:"priority"`
	Kind             string     `json:"kind"`
	Source           string     `json:"source"`
	Acceptance       string     `json:"acceptance"`
	DocLink          string     `json:"doc_link"`
	ProjectName      string     `json:"project_name"`
	NextActor        string     `json:"next_actor"`
	PendingDecisions int        `json:"pending_decisions"`
	Sessions         []string   `json:"sessions"`
	PlanCode         string     `json:"plan_code"`
	StageSeq         int        `json:"stage_seq"`
	StageTitle       string     `json:"stage_title"`
	StageStatus      string     `json:"stage_status"`
	BlockedOn        string     `json:"blocked_on"` // 受阻时在等的 R-x / D-x（R-243）
	BlockedReason    string     `json:"blocked_reason"`
	Files            []fileView `json:"files"`
	Evidence         []struct {
		ID      string     `json:"id"`
		Kind    string     `json:"kind"`
		Summary string     `json:"summary"`
		Link    string     `json:"link"`
		Files   []fileView `json:"files"`
		Voided  bool       `json:"voided"`
	} `json:"evidence"`
	Reviews []struct {
		SessionKey string     `json:"session_key"`
		Summary    string     `json:"summary"`
		Files      []fileView `json:"files"`
		Voided     bool       `json:"voided"`
	} `json:"reviews"`
	Advice *struct {
		Verdict string     `json:"verdict"`
		Reason  string     `json:"reason"`
		ByTitle string     `json:"by_title"`
		At      *time.Time `json:"at"`
	} `json:"advice"`
	Feedback []struct {
		Code         string     `json:"code"`
		Kind         string     `json:"kind"`
		Question     string     `json:"question"`
		Answer       string     `json:"answer"`
		AnswerReason string     `json:"answer_reason"`
		Status       string     `json:"status"`
		AnsweredAt   *time.Time `json:"answered_at"`
		AnswerFiles  []fileView `json:"answer_files"`
		AssignedBy   string     `json:"assigned_by"`
		Context      string     `json:"context"`
		CloseReason  string     `json:"close_reason"`
		FromRole     string     `json:"from_role"`
		ByTitle      string     `json:"by_title"`
		Blocks       []any      `json:"blocks"`
		Guest        *guestView `json:"guest"`
	} `json:"feedback"`
}

// guestView 是放入需求的访客留言的来源（R-281）：用户从分享的待收队列放进来的，不是用户自己写的。
type guestView struct {
	Name        string `json:"name"`
	Share       string `json:"share"`
	TargetKind  string `json:"target_kind"`
	TargetCode  string `json:"target_code"`
	TargetSeq   int    `json:"target_seq"`
	TargetTitle string `json:"target_title"`
	Original    string `json:"original"`
	Addend      string `json:"addend"`
}

// source：「来自「小王」（经分享链接「P-8 进展」），由用户放入，针对 D-12」（与 hub 送达文字同一写法）。
func (g guestView) source() string {
	s := "来自「" + g.Name + "」（经分享链接「" + g.Share + "」），由用户放入"
	switch g.TargetKind {
	case "dec":
		s += "，针对 " + g.TargetCode
	case "evidence":
		s += "，针对证据「" + g.TargetTitle + "」"
	case "stage":
		s += fmt.Sprintf("，针对 %s 阶段 %d", g.TargetCode, g.TargetSeq)
	}
	return s
}

// commentShown 是 req_get 默认列出的会话留言条数（最新的几条，R-249）；full = true 给全部。
const commentShown = 3

// shownComments：feedback 第 i 条是不是默认视图里列出的会话留言（feedback 时间倒序，取前 commentShown 条留言）。
func (r reqView) shownComments() map[int]bool {
	out := map[int]bool{}
	for i, f := range r.Feedback {
		if f.Kind == "comment" && len(out) < commentShown {
			out[i] = true
		}
	}
	return out
}

var reqStatusZh = map[string]string{"draft": "草稿", "ready": "待开始", "in_progress": "进行中", "blocked": "受阻", "review": "待验收", "done": "已完成", "shelved": "已搁置"}

func zh(st string) string {
	if s, ok := reqStatusZh[st]; ok {
		return s
	}
	return st
}

func (r reqView) evidenceCount() int {
	n := 0
	for _, e := range r.Evidence {
		if !e.Voided {
			n++
		}
	}
	return n
}

// line 是一行概要：R-12「标题」待验收 · P1 · 设计 · P-1 阶段 2。
func (r reqView) line() string {
	parts := []string{zh(r.Status), r.Priority}
	if r.Kind == "design" {
		parts = append(parts, "设计")
	}
	if r.PlanCode != "" {
		parts = append(parts, fmt.Sprintf("%s 阶段 %d", r.PlanCode, r.StageSeq))
	}
	if r.PendingDecisions > 0 {
		parts = append(parts, fmt.Sprintf("%d 个待决", r.PendingDecisions))
	}
	if r.Status == "blocked" && r.BlockedOn != "" {
		parts = append(parts, "在等 "+r.BlockedOn)
	}
	return fmt.Sprintf("%s「%s」%s", r.Code, r.Title, strings.Join(nonEmpty(parts), " · "))
}

func roleZh(role string) string {
	if role == "" {
		return "Agent"
	}
	if d := roles.Display(role); d != "" {
		return d
	}
	return role
}

var fbLabel = map[string]string{"feedback": "修改意见", "note": "留言", "assign": "指派给会话", "queue": "排队送达", "plan": "计划进展", "review_task": "提交评审"}

// String 是 req_get 的默认输出：要点按行，附图另以图片返回。
func (r reqView) String() string {
	var b strings.Builder
	b.WriteString(r.line() + "\n")
	if r.ProjectName != "" {
		fmt.Fprintf(&b, "项目：%s\n", r.ProjectName)
	}
	if r.StageTitle != "" {
		fmt.Fprintf(&b, "所属阶段：%s 阶段 %d「%s」\n", r.PlanCode, r.StageSeq, r.StageTitle)
	}
	for _, kv := range [][2]string{{"说明", r.Detail}, {"验收标准", r.Acceptance}, {"来源", r.Source}, {"文档", r.DocLink}} {
		if v := strings.TrimSpace(kv[1]); v != "" {
			fmt.Fprintf(&b, "%s：%s\n", kv[0], v)
		}
	}
	if len(r.Files) > 0 {
		b.WriteString("描述附件：\n")
		filesBlock(&b, "  ", r.Files)
	}
	if len(r.Sessions) > 0 {
		fmt.Fprintf(&b, "关联会话：%s\n", strings.Join(r.Sessions, "、"))
	}
	if n := r.evidenceCount(); n > 0 {
		fmt.Fprintf(&b, "证据（%d 条）：\n", n)
		for _, e := range r.Evidence {
			if e.Voided {
				continue
			}
			s := "  - [" + e.Kind + "] " + summary.Truncate(strings.Join(strings.Fields(e.Summary), " "), 160)
			if len(e.Files) > 0 {
				s += fmt.Sprintf("（%d 个附件）", len(e.Files))
			}
			if e.ID != "" {
				s += "（id " + e.ID + "）"
			}
			b.WriteString(s + "\n")
		}
	}
	if n := len(r.Evidence) - r.evidenceCount(); n > 0 {
		fmt.Fprintf(&b, "已作废的证据 %d 条（不列出）\n", n)
	}
	if a := r.Advice; a != nil {
		v := "建议退回"
		if a.Verdict == "pass" {
			v = "建议通过"
		}
		s := "主助理的验收建议（参考，不是用户原话；验收通过 / 退回只有用户能定）：" + v
		if a.Reason != "" {
			s += "，理由：" + a.Reason
		}
		b.WriteString(s + "\n")
	}
	if len(r.Reviews) > 0 {
		fmt.Fprintf(&b, "审查意见（%d 条，Rev 给的参考，不改状态）：\n", len(r.Reviews))
		for _, e := range r.Reviews {
			s := "  - " + summary.Truncate(strings.Join(strings.Fields(e.Summary), " "), 300)
			if e.Voided {
				s += "（上一轮）"
			}
			if len(e.Files) > 0 {
				s += fmt.Sprintf("（%d 个附件）", len(e.Files))
			}
			b.WriteString(s + "\n")
		}
	}
	shown, total := r.shownComments(), 0
	var cm []string
	for i, f := range r.Feedback {
		if f.Kind != "comment" {
			continue
		}
		total++
		if !shown[i] {
			continue
		}
		when := ""
		if f.AnsweredAt != nil {
			when = f.AnsweredAt.Local().Format("01-02 15:04") + " "
		}
		who := roleZh(f.FromRole)
		if f.ByTitle != "" {
			who += "「" + summary.Truncate(f.ByTitle, 30) + "」"
		}
		s := fmt.Sprintf("  - %s%s %s（%s）：%s", when, f.Code, who, f.AssignedBy, strings.ReplaceAll(strings.TrimSpace(f.Answer), "\n", "\n      ")) // 多行留言缩进，和列表对齐
		if len(f.Blocks) > 0 {
			s += fmt.Sprintf("（附 %d 个表达块，full = true 看全文）", len(f.Blocks))
		}
		if len(f.AnswerFiles) > 0 {
			var fl strings.Builder
			filesBlock(&fl, "      ", f.AnswerFiles)
			s += "\n" + strings.TrimRight(fl.String(), "\n")
		}
		cm = append(cm, s)
	}
	if total > 0 {
		head := fmt.Sprintf("会话留言（共 %d 条，新的在前）：\n", total)
		if total > len(cm) {
			head = fmt.Sprintf("会话留言（共 %d 条，这里列最新 %d 条；全部用 full = true）：\n", total, len(cm))
		}
		b.WriteString(head + strings.Join(cm, "\n") + "\n")
	}
	var fb []string
	for _, f := range r.Feedback {
		when := ""
		if f.AnsweredAt != nil {
			when = f.AnsweredAt.Local().Format("01-02 15:04") + " "
		}
		var s string
		switch f.Kind {
		case "decision", "":
			ans := "（待你答复）"
			if f.Answer != "" {
				ans = "答复：" + f.Answer
				if f.AnswerReason != "" {
					ans += "（用户的理由：" + f.AnswerReason + "）"
				}
			}
			s = fmt.Sprintf("  - %s%s 决策：%s → %s", when, f.Code, oneLine(f.Question), ans)
		case "assign", "queue", "plan", "review_task", "comment":
			continue // 流转记录，状态已在上面；会话留言单列在上面
		case "edit":
			s = fmt.Sprintf("  - %s%s 修改：%s（%s）%s", when, f.Code, roleZh(f.Context), f.AssignedBy, f.Answer)
		case "release":
			s = fmt.Sprintf("  - %s%s 放行：由 %s（%s）放行：%s", when, f.Code, roleZh(f.Context), f.AssignedBy, f.Answer)
			if f.CloseReason != "" {
				s += "（已撤回：" + f.CloseReason + "）"
			}
		case "note":
			if f.Guest == nil {
				s = fmt.Sprintf("  - %s%s %s：%s", when, f.Code, fbLabel[f.Kind], f.Answer)
				break
			}
			s = fmt.Sprintf("  - %s%s 访客留言：%s：%s", when, f.Code, f.Guest.source(), f.Answer)
			if f.Guest.Original != "" {
				s += "\n      访客原话：" + f.Guest.Original
			}
			if f.Guest.Addend != "" {
				s += "\n      用户附言：" + f.Guest.Addend
			}
		default:
			s = fmt.Sprintf("  - %s%s %s：%s", when, f.Code, fbLabel[f.Kind], f.Answer)
			if f.Kind == "feedback" && f.Context != "" {
				s += "\n      " + f.Context // 退回时随带的主助理验收建议（R-242）
			}
		}
		if len(f.AnswerFiles) > 0 {
			var fl strings.Builder
			filesBlock(&fl, "      ", f.AnswerFiles)
			s += "\n" + strings.TrimRight(fl.String(), "\n")
		}
		fb = append(fb, s)
	}
	if len(fb) > 0 {
		b.WriteString("用户意见与决策：\n" + strings.Join(fb, "\n") + "\n")
	}
	return b.String()
}

// images 是默认随 req_get 返回的图：描述、用户意见与列出的会话留言里的附图（不含 Agent 自己交的证据截图），最多 8 张。
func (r reqView) images() []string {
	var ids []string
	add := func(fs []fileView) {
		for _, f := range fs {
			if (f.Mime == "" || protocol.IsImage(f.Mime)) && len(ids) < 8 {
				ids = append(ids, f.ID)
			}
		}
	}
	add(r.Files)
	shown := r.shownComments()
	for i, f := range r.Feedback {
		if f.Kind == "comment" && !shown[i] {
			continue // 没列出的旧留言不带图
		}
		add(f.AnswerFiles)
	}
	return ids
}

type reqListView struct {
	Requirements []reqView `json:"requirements"`
}

func (l reqListView) String() string {
	if len(l.Requirements) == 0 {
		return "没有符合条件的需求。"
	}
	var b strings.Builder
	fmt.Fprintf(&b, "共 %d 条（细节用 req_get 查看）：\n", len(l.Requirements))
	for _, r := range l.Requirements {
		s := r.line()
		if r.ProjectName != "" {
			s += " · " + r.ProjectName
		}
		b.WriteString(s + "\n")
	}
	return b.String()
}

func nonEmpty(xs []string) []string {
	out := xs[:0]
	for _, x := range xs {
		if x != "" {
			out = append(out, x)
		}
	}
	return out
}

func oneLine(s string) string { return summary.Truncate(strings.Join(strings.Fields(s), " "), 200) }
