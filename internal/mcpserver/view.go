package mcpserver

import (
	"fmt"
	"strings"
	"time"

	"github.com/NekroAI/kitestring/internal/summary"
)

// 需求的精简视图（R-57）：写操作只回一行确认，req_get 默认按行输出要点，req_list 每条一行。
// 完整 JSON 只在 req_get full = true 时返回。

type fileView struct {
	ID   string `json:"id"`
	Mime string `json:"mime"`
}

type reqView struct {
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
	Files            []fileView `json:"files"`
	Evidence         []struct {
		Kind    string     `json:"kind"`
		Summary string     `json:"summary"`
		Link    string     `json:"link"`
		Files   []fileView `json:"files"`
		Voided  bool       `json:"voided"`
	} `json:"evidence"`
	Feedback []struct {
		Code        string     `json:"code"`
		Kind        string     `json:"kind"`
		Question    string     `json:"question"`
		Answer      string     `json:"answer"`
		Status      string     `json:"status"`
		AnsweredAt  *time.Time `json:"answered_at"`
		AnswerFiles []fileView `json:"answer_files"`
	} `json:"feedback"`
}

var reqStatusZh = map[string]string{"draft": "草稿", "ready": "待开始", "in_progress": "进行中", "blocked": "阻塞", "review": "待验收", "done": "已完成", "shelved": "已搁置"}

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
	return fmt.Sprintf("%s「%s」%s", r.Code, r.Title, strings.Join(nonEmpty(parts), " · "))
}

var fbLabel = map[string]string{"feedback": "修改意见", "note": "留言", "assign": "交给会话", "queue": "排队送达", "plan": "计划进展"}

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
			b.WriteString(s + "\n")
		}
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
			}
			s = fmt.Sprintf("  - %s%s 决策：%s → %s", when, f.Code, oneLine(f.Question), ans)
		case "assign", "queue", "plan":
			continue // 流转记录，状态已在上面
		default:
			s = fmt.Sprintf("  - %s%s %s：%s", when, f.Code, fbLabel[f.Kind], f.Answer)
		}
		if len(f.AnswerFiles) > 0 {
			s += fmt.Sprintf("（附图 %d 张）", len(f.AnswerFiles))
		}
		fb = append(fb, s)
	}
	if len(fb) > 0 {
		b.WriteString("用户意见与决策：\n" + strings.Join(fb, "\n") + "\n")
	}
	return b.String()
}

// images 是默认随 req_get 返回的图：描述与用户意见里的附图（不含 Agent 自己交的证据截图），最多 8 张。
func (r reqView) images() []string {
	var ids []string
	add := func(fs []fileView) {
		for _, f := range fs {
			if (f.Mime == "" || strings.HasPrefix(f.Mime, "image/")) && len(ids) < 8 {
				ids = append(ids, f.ID)
			}
		}
	}
	add(r.Files)
	for _, f := range r.Feedback {
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
