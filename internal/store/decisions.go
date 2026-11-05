package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/NekroAI/kitestring/internal/blocks"
	"github.com/NekroAI/kitestring/internal/protocol"
)

// 决策状态（requirements.md 4.2）。
const (
	DecPending   = "pending"   // 待答复决策
	DecAnswered  = "answered"  // 已决策待接收
	DecDelivered = "delivered" // 已接收
	DecClosed    = "closed"    // 已关闭（不再注入）
)

// 决策类型：agent 提出的决策；用户对需求的修改意见、留言（创建即为“已决策待接收”，沿用同一条送达通道）。
// 修改意见会把需求退回进行中并开启新一轮；留言只送达，不改需求状态。
const (
	KindDecision   = "decision"
	KindFeedback   = "feedback"
	KindNote       = "note"
	KindAssign     = "assign"      // 你把需求交给某个会话开始处理
	KindPlan       = "plan"        // 计划进展（开始处理、阶段通过 / 退回、未获批准），送达计划的负责会话
	KindQueue      = "queue"       // 会话空闲时整批送达的待开始需求（R-66）
	KindRole       = "role"        // 会话身份变更通知，例如 PM 被新会话接管（R-82）
	KindReviewTask = "review_task" // 你把需求交给审查员审查（R-88）：只送达，不改需求状态
	KindRelease    = "release"     // 放行记录（R-166）：PM / Adv 把草稿改为待开始，answer 是理由，assigned_by 是放行的会话；只留痕，不送达
	KindTell       = "tell"        // 会话之间的团队消息（R-135 / R-116）：开发交验收、给 PM 留言、开发上线；只送给 Agent，不进你的收件箱
	KindEdit       = "edit"        // 修改记录（R-134）：Agent 改了需求的标题 / 描述 / 验收标准 / 优先级 / 类型，blocks 里是改前改后；只留痕，不送达
	KindComment    = "comment"     // 会话留言（R-249）：会话在需求下写的细节，answer 是正文、answer_files 是附图、blocks 是表达块，assigned_by / from_role 是留言的会话和它当时的身份；进时间线，不送达
)

// Decision 是一条待用户决策的问题。
type Decision struct {
	ID              string         `json:"id"`
	Code            string         `json:"code"`
	SessionKey      string         `json:"session_key,omitempty"`
	OriginSession   string         `json:"origin_session,omitempty"`
	AssignedBy      string         `json:"assigned_by,omitempty"`   // 交出需求的会话（PM 指派时为 PM，R-86）；你在网页上交出时为空
	SessionState    string         `json:"session_state,omitempty"` // 目标会话当前状态
	DeviceName      string         `json:"device_name,omitempty"`
	ProjectID       string         `json:"project_id,omitempty"`
	ProjectName     string         `json:"project_name,omitempty"`
	RequirementID   string         `json:"requirement_id,omitempty"`
	RequirementCode string         `json:"requirement_code,omitempty"`
	PlanID          string         `json:"plan_id,omitempty"`
	PlanCode        string         `json:"plan_code,omitempty"`
	Question        string         `json:"question"`
	Context         string         `json:"context"`
	Options         []string       `json:"options"`
	OptionFiles     [][]FileRef    `json:"option_files"`     // 每个选项的效果图（与 options 一一对应），用于方案对比
	Approval        bool           `json:"approval"`         // 请你批准（选项固定为“批准 / 拒绝”，两种都可以附言）
	ContextFiles    []FileRef      `json:"context_files"`    // 问题本身附带的图（例如待批准的方案图）
	Blocks          []blocks.Block `json:"blocks,omitempty"` // 说明里附的表达块（R-97）
	// 要决策的点（R-112）：每个点是一个 options 块，标题是问题；用户逐点选择，Choices 与之一一对应（-1 = 没选）。
	Points  []blocks.Block `json:"points,omitempty"`
	Choices []int          `json:"choices,omitempty"`
	// Summary 是方案要点，每条一行（通常以需求编号开头）；有它时网页把完整说明折叠起来。
	Summary          []string   `json:"summary,omitempty"`
	Recommendation   string     `json:"recommendation"`
	RecOption        int        `json:"rec_option" doc:"推荐的是第几个选项（从 0 开始，-1 = 没有）：提问时明确给出，或推荐文字与某个选项完全相同（R-239）"`
	RecPick          *int       `json:"-"`           // 只用于创建：提问方明确给的推荐序号；CreateDecision 据此和推荐文字定 RecOption
	ShortTitle       string     `json:"short_title"` // 模型生成的短标题
	Kind             string     `json:"kind" enum:"decision,feedback,note,assign,plan,queue,role,review_task,tell,release,edit,comment"`
	AnswerFiles      []FileRef  `json:"answer_files"`
	RequirementTitle string     `json:"requirement_title,omitempty"`
	Status           string     `json:"status"`
	Answer           string     `json:"answer"`
	Verdict          string     `json:"verdict" enum:",approved,rejected" doc:"批准类决策的结论（R-239）：作答时明确记下，approved 批准 / rejected 拒绝（含超时视为拒绝，另见 timed_out）；其它决策为空"`
	AnswerReason     string     `json:"answer_reason,omitempty" doc:"用户作答时附的一句话理由（可选，R-157）：没选推荐项时为什么；随答复送给提问的 Agent"`
	CreatedAt        time.Time  `json:"created_at"`
	AnsweredAt       *time.Time `json:"answered_at,omitempty"`
	DeliveredAt      *time.Time `json:"delivered_at,omitempty"`
	DeliveredTurn    string     `json:"delivered_turn,omitempty"`
	CloseReason      string     `json:"close_reason,omitempty"` // 撤回 / 关闭时写的原因（R-136）
	ReplacedBy       string     `json:"replaced_by,omitempty"`  // 取代它的决策编号，如 D-142（R-136）
	// 风险档与参谋代答（R-163）：Risk 是提问方标的风险档（low / medium / high，空 = 没标，按 high 算）；
	// ProxyBy 非空表示是 Adv 在你的授权窗口内代答的：ProxyRisk 是代答时取的风险档（提问方与参谋判断中更高的一档），
	// ProxyAnswer 是代答原文；OverriddenAt 非空表示你推翻了代答，Answer 换成你的答复
	Risk string `json:"risk,omitempty" enum:"low,medium,high"`
	// Hard 是提问方标的硬规则类别（R-210）：product / design / semantics / permission / gate / irreversible / acceptance；
	// 标了就永不代答、风险档按 high。空 = 没标
	Hard         string     `json:"hard,omitempty" enum:"product,design,semantics,permission,gate,irreversible,acceptance"`
	ProxyBy      string     `json:"proxy_by,omitempty"`
	ProxyReason  string     `json:"proxy_reason,omitempty"`
	ProxyRisk    string     `json:"proxy_risk,omitempty"`
	ProxyAnswer  string     `json:"proxy_answer,omitempty"`
	ProxyAt      *time.Time `json:"proxy_at,omitempty"`
	OverriddenAt *time.Time `json:"overridden_at,omitempty"`
	// 决策后果链（R-154）：FromDecision 是提问方声明的来源决策（D-x），Overturns 为真表示这条推翻了它的结论，否则是接着它往下问
	FromDecision string `json:"from_decision,omitempty"`
	Overturns    bool   `json:"overturns,omitempty"`
	// Agent 的请求（R-169）：RequestKind 非空表示这是一条请求，也是批准类（approval = true）。
	// devs = PM 推荐开独立开发会话：RequestCount 是要开几个；ExpiresAt 到点仍没作答按规则视为拒绝，TimedOut 记下这种结局。
	// status = Agent 申请改需求状态（R-188）：RequestFrom 是申请时的状态，RequestTo 是目标状态；不限时。
	// 批准前需求状态被改动过，申请自动作废：Voided 为真，答复送回申请方。
	RequestKind  string     `json:"request_kind,omitempty" enum:"devs,status"`
	RequestCount int        `json:"request_count,omitempty"`
	ExpiresAt    *time.Time `json:"expires_at,omitempty"`
	TimedOut     bool       `json:"timed_out,omitempty"`
	RequestFrom  string     `json:"request_from,omitempty" doc:"改状态申请（R-188）：申请时需求的状态"`
	RequestTo    string     `json:"request_to,omitempty" doc:"改状态申请（R-188）：申请改成的状态"`
	Voided       bool       `json:"voided,omitempty" doc:"改状态申请（R-188）：批准前需求状态已被改动，申请自动作废"`
	// ByTitle：写这条记录的会话当时的标题（R-195）——修改、放行、指派记 assigned_by 的，Agent 提的决策记提问会话的；旧记录为空
	ByTitle string `json:"by_title,omitempty" doc:"写这条记录的会话当时的标题快照（R-195）：修改 / 放行 / 指派是 assigned_by，决策是提问的会话"`
	// 团队消息（R-191）：AssignedBy 是发消息的会话，FromRole 是它当时的身份；FromAgent 是发消息的子任务的 agent_id，
	// 主线程发的为空；ReplyTo 是这条回复的那条团队消息的 ID
	FromRole  string `json:"from_role,omitempty" doc:"团队消息（R-191）：发消息的会话当时的身份；Agent 提的决策（R-245 起）是提问会话当时的身份"`
	FromAgent string `json:"from_agent,omitempty" doc:"团队消息（R-191）：发消息的子任务的 agent_id；会话主线程发的为空"`
	ReplyTo   string `json:"reply_to,omitempty" doc:"团队消息（R-191）：这条回复的团队消息 ID"`
	// 团队消息（R-249）：FYI 为真是「只告知」，不唤醒对方挂着的 ks wait、不清除待命超时（取代原来用标题 QuietTellQuestion 区分）；
	// BroadcastID 非空表示这条是一次广播拆给某个收件人的那一行，同一次广播的各行共用它
	FYI         bool   `json:"fyi,omitempty" doc:"团队消息（R-249）：只告知，不唤醒对方挂着的 ks wait"`
	BroadcastID string `json:"broadcast_id,omitempty" doc:"团队消息（R-249）：一次广播拆成每个收件人一行，同一次广播共用这个 ID"`
}

// snapDecision 给刚写入的记录补上会话标题快照（R-195）。
func snapDecision(ctx context.Context, t *sql.Tx, id, key string) error {
	title, _ := sessionSnap(ctx, t, key)
	if title == "" {
		return nil
	}
	_, err := t.ExecContext(ctx, `UPDATE decisions SET by_title = ? WHERE id = ?`, title, id)
	return err
}

// RequestDevs：PM 推荐开独立开发会话（R-169）。
const RequestDevs = "devs"

// RequestStatus：Agent 申请改需求状态（R-188）。
const RequestStatus = "status"

// VoidAnswer 是作废的改状态申请写进 answer 的文字；送给申请方的完整说明由 hub 拼。
const VoidAnswer = "已作废"

func decCode(seq int) string { return "D-" + strconv.Itoa(seq) }

const decSelect = `SELECT x.id, x.seq, COALESCE(x.session_key, ''), COALESCE(x.origin_session, ''), COALESCE(s.state, ''), COALESCE(dv.name, ''),
	COALESCE(x.project_id, ''), COALESCE(p.name, ''), COALESCE(x.requirement_id, ''), COALESCE(r.seq, 0), COALESCE(r.title, ''),
	x.question, x.context, x.blocks, x.points, x.summary, x.choices, x.options, x.option_files, x.approval, x.context_files, x.recommendation, x.short_title, x.kind, x.answer_files, x.status, x.answer, x.created_at, x.answered_at, x.delivered_at, x.delivered_turn,
	COALESCE(x.plan_id, ''), COALESCE(pl.seq, 0), x.assigned_by, x.close_reason, x.replaced_by,
	x.risk, x.proxy_by, x.proxy_reason, x.proxy_risk, x.proxy_answer, x.proxy_at, x.overridden_at, x.from_decision, x.overturns, x.answer_reason,
	x.request_kind, x.request_count, x.expires_at, x.timed_out, x.request_from, x.request_to, x.request_voided, x.by_title, x.from_role, x.from_agent, x.reply_to, x.verdict, x.rec_option, x.hard, x.fyi, x.broadcast_id
	FROM decisions x
	LEFT JOIN sessions s ON s.key = x.session_key
	LEFT JOIN devices dv ON dv.id = s.device_id
	LEFT JOIN projects p ON p.id = x.project_id
	LEFT JOIN requirements r ON r.id = x.requirement_id
	LEFT JOIN plans pl ON pl.id = x.plan_id`

func scanDec(row interface{ Scan(...any) error }) (Decision, error) {
	var d Decision
	var seq, rseq, pseq int
	var options, optFiles, ctxFiles, created, files, bs, points, summary, choices string
	var answered, delivered sql.NullString
	var proxyAt, overAt, expires string
	err := row.Scan(&d.ID, &seq, &d.SessionKey, &d.OriginSession, &d.SessionState, &d.DeviceName, &d.ProjectID, &d.ProjectName, &d.RequirementID, &rseq, &d.RequirementTitle,
		&d.Question, &d.Context, &bs, &points, &summary, &choices, &options, &optFiles, &d.Approval, &ctxFiles, &d.Recommendation, &d.ShortTitle, &d.Kind, &files, &d.Status, &d.Answer, &created, &answered, &delivered, &d.DeliveredTurn,
		&d.PlanID, &pseq, &d.AssignedBy, &d.CloseReason, &d.ReplacedBy,
		&d.Risk, &d.ProxyBy, &d.ProxyReason, &d.ProxyRisk, &d.ProxyAnswer, &proxyAt, &overAt, &d.FromDecision, &d.Overturns, &d.AnswerReason,
		&d.RequestKind, &d.RequestCount, &expires, &d.TimedOut, &d.RequestFrom, &d.RequestTo, &d.Voided, &d.ByTitle, &d.FromRole, &d.FromAgent, &d.ReplyTo, &d.Verdict, &d.RecOption, &d.Hard, &d.FYI, &d.BroadcastID)
	d.ExpiresAt = parseNullTS(sql.NullString{String: expires, Valid: expires != ""})
	d.Code = decCode(seq)
	d.ProxyAt = parseNullTS(sql.NullString{String: proxyAt, Valid: proxyAt != ""})
	d.OverriddenAt = parseNullTS(sql.NullString{String: overAt, Valid: overAt != ""})
	if pseq > 0 {
		d.PlanCode = planCode(pseq)
	}
	d.AnswerFiles = parseFiles(files)
	if rseq > 0 {
		d.RequirementCode = reqCode(rseq)
	}
	d.Options = []string{}
	_ = json.Unmarshal([]byte(options), &d.Options)
	d.OptionFiles = [][]FileRef{}
	_ = json.Unmarshal([]byte(optFiles), &d.OptionFiles)
	d.ContextFiles = parseFiles(ctxFiles)
	if b := blocks.Decode[[]blocks.Block](bs); b != nil {
		d.Blocks = *b
	}
	if p := blocks.Decode[[]blocks.Block](points); p != nil {
		d.Points = *p
	}
	if x := blocks.Decode[[]string](summary); x != nil {
		d.Summary = *x
	}
	if c := blocks.Decode[[]int](choices); c != nil {
		d.Choices = *c
	}
	d.CreatedAt = parseTS(created)
	d.AnsweredAt = parseNullTS(answered)
	d.DeliveredAt = parseNullTS(delivered)
	return d, err
}

// CreateDecision 新建待答复决策项。
func (s *Store) CreateDecision(ctx context.Context, d Decision) (Decision, error) {
	if strings.TrimSpace(d.Question) == "" {
		return d, errors.New("问题不能为空")
	}
	opts, _ := json.Marshal(d.Options)
	id := protocol.NewID()
	err := s.tx(ctx, func(t *sql.Tx) error {
		var seq int
		if err := t.QueryRowContext(ctx, `SELECT COALESCE(MAX(seq), 0) + 1 FROM decisions`).Scan(&seq); err != nil {
			return err
		}
		n := now()
		if d.Kind == KindFeedback || d.Kind == KindNote || d.Kind == KindAssign || d.Kind == KindPlan || d.Kind == KindQueue || d.Kind == KindRole || d.Kind == KindReviewTask || d.Kind == KindTell {
			// 修改意见：用户写下即为答复，直接进入“已决策待接收”。
			_, err := t.ExecContext(ctx, `INSERT INTO decisions(id, seq, session_key, origin_session, project_id, requirement_id, plan_id, question, context, options, recommendation, kind, status, answer, answer_files, answered_at, created_at, assigned_by, from_role, from_agent, reply_to, fyi, broadcast_id) VALUES(?, ?, ?, ?, ?, ?, ?, ?, ?, '[]', '', ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
				id, seq, nullStr(d.SessionKey), nullStr(d.SessionKey), nullStr(d.ProjectID), nullStr(d.RequirementID), nullStr(d.PlanID), d.Question, d.Context, d.Kind, DecAnswered, d.Answer, filesJSON(d.AnswerFiles), n, n, d.AssignedBy, d.FromRole, d.FromAgent, d.ReplyTo, d.Kind == KindTell && d.FYI, d.BroadcastID)
			if err != nil {
				return err
			}
			if d.SessionKey != "" && !(d.Kind == KindTell && d.FYI) {
				// 点名送给它的（指派、排队送达、留言、团队消息、身份通知）：待命超时的开发恢复正常（R-229）
				if _, err := t.ExecContext(ctx, `UPDATE sessions SET idle_at = '' WHERE key = ? AND idle_at <> ''`, d.SessionKey); err != nil {
					return err
				}
			}
			return snapDecision(ctx, t, id, d.AssignedBy)
		}
		optFiles := d.OptionFiles
		if optFiles == nil {
			optFiles = [][]FileRef{}
		}
		of, _ := json.Marshal(optFiles)
		expires := ""
		if d.ExpiresAt != nil {
			expires = ts(*d.ExpiresAt)
		}
		rec := RecOption(d.Options, d.Recommendation, d.RecPick)
		if d.Approval {
			rec = -1
		}
		_, err := t.ExecContext(ctx, `INSERT INTO decisions(id, seq, session_key, origin_session, project_id, requirement_id, question, context, blocks, points, summary, options, option_files, approval, context_files, recommendation, status, created_at, risk, from_decision, overturns, request_kind, request_count, expires_at, request_from, request_to, rec_option, hard, from_role) VALUES(?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			id, seq, nullStr(d.SessionKey), nullStr(d.SessionKey), nullStr(d.ProjectID), nullStr(d.RequirementID), d.Question, d.Context, blocks.Encode(d.Blocks), blocks.Encode(d.Points), blocks.Encode(d.Summary), string(opts), string(of), d.Approval, filesJSON(d.ContextFiles), d.Recommendation, DecPending, n, d.Risk, d.FromDecision, d.FromDecision != "" && d.Overturns, d.RequestKind, d.RequestCount, expires, d.RequestFrom, d.RequestTo, rec, d.Hard, d.FromRole)
		if err != nil {
			return err
		}
		return snapDecision(ctx, t, id, d.SessionKey)
	})
	if err != nil {
		return d, err
	}
	return s.GetDecision(ctx, id)
}

// GetDecision 按 ID 或 D-编号读取决策。
func (s *Store) GetDecision(ctx context.Context, ref string) (Decision, error) {
	var row *sql.Row
	if seq, ok := parseRef(ref, "D"); ok {
		row = s.db.QueryRowContext(ctx, decSelect+` WHERE x.seq = ?`, seq)
	} else {
		row = s.db.QueryRowContext(ctx, decSelect+` WHERE x.id = ?`, ref)
	}
	d, err := scanDec(row)
	if errors.Is(err, sql.ErrNoRows) {
		return d, ErrNotFound
	}
	return d, err
}

// ListDecisions 列出决策，statuses 为空表示全部。
func (s *Store) ListDecisions(ctx context.Context, statuses []string, limit int) ([]Decision, error) {
	q := decSelect
	var args []any
	if len(statuses) > 0 {
		q += ` WHERE x.status IN (?` + strings.Repeat(", ?", len(statuses)-1) + `)`
		for _, st := range statuses {
			args = append(args, st)
		}
	}
	q += ` ORDER BY x.seq DESC LIMIT ?`
	args = append(args, limit)
	return s.queryDecs(ctx, q, args...)
}

func (s *Store) queryDecs(ctx context.Context, q string, args ...any) ([]Decision, error) {
	rows, err := s.db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Decision{}
	for rows.Next() {
		d, err := scanDec(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, d)
	}
	return out, rows.Err()
}

// AnswerDecision 记录用户答复，状态进入“已决策待接收”。已接收的决策允许改答，会重新进入待接收。
func (s *Store) AnswerDecision(ctx context.Context, ref, answer string, files []FileRef) (Decision, error) {
	return s.AnswerDecisionPoints(ctx, ref, answer, files, nil, "", "")
}

// AnswerDecisionPoints 作答；choices 是逐点的选择（R-112），没有要决策的点时为 nil；
// reason 是可选的一句话理由（R-157），去空白、最多 200 字，改答时以最新一次为准。
// verdict 是批准类决策的结论（VerdictApproved / VerdictRejected，R-239），由作答方明确给出；
// 旧客户端没给时按所选选项（答复开头的「批准 / 拒绝」）定一次并存下。非批准类决策忽略。
func (s *Store) AnswerDecisionPoints(ctx context.Context, ref, answer string, files []FileRef, choices []int, reason, verdict string) (Decision, error) {
	d, err := s.GetDecision(ctx, ref)
	if err != nil {
		return d, err
	}
	if d.Status == DecClosed {
		return d, fmt.Errorf("%s 已关闭", d.Code)
	}
	if d.TimedOut {
		return d, fmt.Errorf("%s 已超时，按规则视为拒绝；需要的话请 Agent 重新提", d.Code)
	}
	if strings.TrimSpace(answer) == "" && len(files) == 0 {
		return d, errors.New("答复不能为空")
	}
	// 你给参谋代答过的决策重新作答 = 推翻代答（R-163）：记下推翻时间，代答原文留在 proxy_answer
	n := now()
	_, reason = NormShelveReason("", reason)
	verdict = answerVerdict(d, answer, verdict)
	_, err = s.db.ExecContext(ctx, `UPDATE decisions SET status = ?, answer = ?, answer_reason = ?, answer_files = ?, choices = ?, answered_at = ?, delivered_at = NULL, delivered_turn = '', verdict = ?,
		overridden_at = CASE WHEN proxy_by <> '' THEN ? ELSE overridden_at END WHERE id = ?`, DecAnswered, answer, reason, filesJSON(files), blocks.Encode(choices), n, verdict, n, d.ID)
	if err != nil {
		return d, err
	}
	if d.SessionKey != "" { // 答复送回提问方：待命超时的开发恢复正常（R-229）
		if _, err := s.ClearIdle(ctx, d.SessionKey); err != nil {
			return d, err
		}
	}
	return s.GetDecision(ctx, d.ID)
}

// answerVerdict：批准类决策这次作答的结论。明确给出的为准；没给（旧客户端）时按所选选项文字定一次。
func answerVerdict(d Decision, answer, verdict string) string {
	if !d.Approval {
		return ""
	}
	if verdict == VerdictApproved || verdict == VerdictRejected {
		return verdict
	}
	return legacyVerdict(answer)
}

// ExpireRequest：限时请求到点仍没作答，按规则视为拒绝（R-169）——写入 answer、标记 timed_out，进入“已决策待接收”送给提问方。
// 已作答、已关闭、没到期的不动，返回 ok = false。
func (s *Store) ExpireRequest(ctx context.Context, id, answer string) (Decision, bool, error) {
	d0, err := s.GetDecision(ctx, id)
	if err != nil {
		return d0, false, err
	}
	if d0.RequestKind == "" || d0.ExpiresAt == nil || time.Now().Before(*d0.ExpiresAt) || d0.Status != DecPending {
		return d0, false, nil
	}
	res, err := s.db.ExecContext(ctx, `UPDATE decisions SET status = ?, answer = ?, timed_out = 1, verdict = ?, answered_at = ?, delivered_at = NULL, delivered_turn = ''
		WHERE id = ? AND status = ?`, DecAnswered, answer, VerdictRejected, now(), d0.ID, DecPending)
	if err != nil {
		return Decision{}, false, err
	}
	k, _ := res.RowsAffected()
	d, err := s.GetDecision(ctx, id)
	return d, k > 0, err
}

// PendingStatusRequest 返回需求上还在等用户批准的改状态申请（R-188）；没有时 ok = false。
func (s *Store) PendingStatusRequest(ctx context.Context, reqID string) (Decision, bool, error) {
	list, err := s.queryDecs(ctx, decSelect+` WHERE x.status = ? AND x.request_kind = ? AND x.requirement_id = ? ORDER BY x.seq LIMIT 1`, DecPending, RequestStatus, reqID)
	if err != nil || len(list) == 0 {
		return Decision{}, false, err
	}
	return list[0], true, nil
}

// voidStatusRequests 在事务里作废需求上还在等的改状态申请（需求状态被改动了，R-188），返回作废的决策 ID。
// 作废 = 进入「已决策待接收」、标 request_voided，送回申请方。
func voidStatusRequests(ctx context.Context, t *sql.Tx, reqID string) ([]string, error) {
	rows, err := t.QueryContext(ctx, `SELECT id FROM decisions WHERE requirement_id = ? AND request_kind = ? AND status = ?`, reqID, RequestStatus, DecPending)
	if err != nil {
		return nil, err
	}
	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return nil, err
		}
		ids = append(ids, id)
	}
	rows.Close()
	n := now()
	for _, id := range ids {
		if _, err := t.ExecContext(ctx, `UPDATE decisions SET status = ?, answer = ?, request_voided = 1, answered_at = ?, delivered_at = NULL, delivered_turn = '' WHERE id = ? AND status = ?`,
			DecAnswered, VoidAnswer, n, id, DecPending); err != nil {
			return nil, err
		}
	}
	return ids, nil
}

// VoidStatusRequest 作废一条还在等的改状态申请（用户批准时发现需求状态已变，R-188）。
func (s *Store) VoidStatusRequest(ctx context.Context, id string) (Decision, error) {
	_, err := s.db.ExecContext(ctx, `UPDATE decisions SET status = ?, answer = ?, request_voided = 1, answered_at = ?, delivered_at = NULL, delivered_turn = '' WHERE id = ? AND status = ? AND request_kind = ?`,
		DecAnswered, VoidAnswer, now(), id, DecPending, RequestStatus)
	if err != nil {
		return Decision{}, err
	}
	return s.GetDecision(ctx, id)
}

// PendingRequests 列出还在等用户作答的限时请求（projectID 为空表示全部）。
func (s *Store) PendingRequests(ctx context.Context, projectID, kind string) ([]Decision, error) {
	q := decSelect + ` WHERE x.status = ? AND x.request_kind <> ''`
	args := []any{DecPending}
	if projectID != "" {
		q += ` AND x.project_id = ?`
		args = append(args, projectID)
	}
	if kind != "" {
		q += ` AND x.request_kind = ?`
		args = append(args, kind)
	}
	return s.queryDecs(ctx, q+` ORDER BY x.seq`, args...)
}

// MarkDelivered 标记决策已注入（幂等）。
func (s *Store) MarkDelivered(ctx context.Context, id, sessionKey, turn string, at time.Time) (bool, error) {
	res, err := s.db.ExecContext(ctx, `UPDATE decisions SET status = ?, delivered_at = ?, delivered_turn = ? WHERE id = ? AND status = ? AND session_key = ?`,
		DecDelivered, ts(at), turn, id, DecAnswered, sessionKey)
	if err != nil {
		return false, err
	}
	n, _ := res.RowsAffected()
	return n > 0, nil
}

// ReassignDecision 把决策转交给另一个会话（来源会话已结束时由用户操作，D15）。
func (s *Store) ReassignDecision(ctx context.Context, ref, sessionKey string) (Decision, error) {
	d, err := s.GetDecision(ctx, ref)
	if err != nil {
		return d, err
	}
	status := d.Status
	if status == DecDelivered {
		status = DecAnswered // 转交后需要重新注入
	}
	_, err = s.db.ExecContext(ctx, `UPDATE decisions SET session_key = ?, status = ? WHERE id = ?`, sessionKey, status, d.ID)
	if err != nil {
		return d, err
	}
	return s.GetDecision(ctx, d.ID)
}

// ResendDecision 让已接收的决策重新进入待接收（Web 上手动重发）。
func (s *Store) ResendDecision(ctx context.Context, ref string) (Decision, error) {
	d, err := s.GetDecision(ctx, ref)
	if err != nil {
		return d, err
	}
	if d.Status != DecDelivered {
		return d, fmt.Errorf("%s 尚未接收，无需重发", d.Code)
	}
	_, err = s.db.ExecContext(ctx, `UPDATE decisions SET status = ? WHERE id = ?`, DecAnswered, d.ID)
	if err != nil {
		return d, err
	}
	return s.GetDecision(ctx, d.ID)
}

// SetDecisionTitle 写入模型生成的短标题。
func (s *Store) SetDecisionTitle(ctx context.Context, id, title string) error {
	_, err := s.db.ExecContext(ctx, `UPDATE decisions SET short_title = ? WHERE id = ?`, title, id)
	return err
}

// DecisionsBetween 返回时间窗内提出或作答的决策，供时段回顾使用。
func (s *Store) DecisionsBetween(ctx context.Context, from, to time.Time) ([]Decision, error) {
	return s.queryDecs(ctx, decSelect+` WHERE (x.created_at >= ? AND x.created_at <= ?) OR (x.answered_at >= ? AND x.answered_at <= ?) ORDER BY x.seq`, ts(from), ts(to), ts(from), ts(to))
}

// CloseDecision 关闭决策，不再注入。reason 与 replacedBy（取代它的决策编号）可为空，写入后在时间线上显示（R-136）。
// 写了 replacedBy 时，目标必须仍待用户答复、且不会形成回环；检查与关闭在同一事务里（R-168），
// 防止两个会话同时互相撤回、两条都关闭而用户面板上什么都不剩。
func (s *Store) CloseDecision(ctx context.Context, ref, reason, replacedBy string) (Decision, error) {
	d, err := s.GetDecision(ctx, ref)
	if err != nil {
		return d, err
	}
	replacedBy = strings.TrimSpace(replacedBy)
	err = s.tx(ctx, func(t *sql.Tx) error {
		if replacedBy != "" {
			code, err := checkReplacement(ctx, t, d, replacedBy)
			if err != nil {
				return err
			}
			replacedBy = code
		}
		_, err := t.ExecContext(ctx, `UPDATE decisions SET status = ?, close_reason = ?, replaced_by = ? WHERE id = ?`, DecClosed, strings.TrimSpace(reason), replacedBy, d.ID)
		return err
	})
	if err != nil {
		return d, err
	}
	return s.GetDecision(ctx, d.ID)
}

// checkReplacement 在事务里校验 d 被 target 取代是否成立，返回 target 的规范编号。
func checkReplacement(ctx context.Context, t *sql.Tx, d Decision, target string) (string, error) {
	type row struct {
		id, status, replacedBy string
		seq                    int
	}
	load := func(ref string) (row, error) {
		var r row
		var q string
		var arg any
		if seq, ok := parseRef(ref, "D"); ok {
			q, arg = `WHERE seq = ?`, seq
		} else {
			q, arg = `WHERE id = ?`, ref
		}
		err := t.QueryRowContext(ctx, `SELECT id, seq, status, replaced_by FROM decisions `+q, arg).Scan(&r.id, &r.seq, &r.status, &r.replacedBy)
		return r, err
	}
	tg, err := load(target)
	if errors.Is(err, sql.ErrNoRows) {
		return "", fmt.Errorf("取代它的决策 %s 不存在，请先确认编号；不确定就不要注明 replaced_by", target)
	}
	if err != nil {
		return "", err
	}
	code := fmt.Sprintf("D-%d", tg.seq)
	if tg.id == d.ID {
		return "", errors.New("不能被自己取代")
	}
	// 回环：顺着 replaced_by 一路走，走回本决策就是环（含多跳）。
	cur, seen := tg, map[string]bool{tg.id: true}
	for cur.replacedBy != "" {
		nx, err := load(cur.replacedBy)
		if err != nil {
			break
		}
		if nx.id == d.ID {
			return "", fmt.Errorf("%s 已（直接或间接）被 %s 取代，再指向它会形成回环，请先确认；改指向仍待答的决策，或不注明取代", code, d.Code)
		}
		if seen[nx.id] {
			break
		}
		seen[nx.id] = true
		cur = nx
	}
	if tg.status != DecPending {
		st := "已不在等用户答复"
		switch tg.status {
		case DecClosed:
			st = "已关闭 / 已撤回"
		case DecAnswered, DecDelivered:
			st = "用户已答复"
		}
		return "", fmt.Errorf("取代它的决策 %s %s，不能再作为取代目标：被替换的那条已经不开着，用户面板上不会剩下问题。请先确认，改指向仍待答的决策，或不注明取代", code, st)
	}
	return code, nil
}

// WithdrawDecision 是 Agent 撤回自己提的、还没答复的决策（R-136）：只能撤回本会话提的 decision 类、
// 状态为待答复决策的那条；已答复、已接收或已关闭的不能撤回。
func (s *Store) WithdrawDecision(ctx context.Context, ref, sessionKey, reason, replacedBy string) (Decision, error) {
	d, err := s.GetDecision(ctx, ref)
	if err != nil {
		return d, err
	}
	owner := d.OriginSession
	if owner == "" {
		owner = d.SessionKey
	}
	switch {
	case d.Kind != KindDecision:
		return d, fmt.Errorf("%s 不是决策，不能撤回", d.Code)
	case sessionKey == "" || owner != sessionKey:
		return d, fmt.Errorf("%s 不是本会话提的，只能撤回自己提的决策", d.Code)
	case d.Status == DecClosed:
		return d, fmt.Errorf("%s 已关闭", d.Code)
	case d.Status != DecPending:
		return d, fmt.Errorf("%s 用户已答复，不能撤回", d.Code)
	}
	return s.CloseDecision(ctx, ref, reason, replacedBy)
}

// DecisionsForDevice 返回目标会话位于该设备的待注入决策：“已决策待接收”的全部，
// 以及 since 之后已接收的（供会话恢复 / 压缩时补注入，D15）。
// 不排除已结束的会话：同一会话被恢复（resume，session_id 不变）时仍应收到答复。
func (s *Store) DecisionsForDevice(ctx context.Context, deviceID string, since time.Time) ([]Decision, error) {
	return s.queryDecs(ctx, decSelect+` WHERE s.device_id = ? AND (x.status = ? OR (x.status = ? AND x.delivered_at >= ?)) ORDER BY x.seq`,
		deviceID, DecAnswered, DecDelivered, ts(since))
}

// SentTells 列出会话 key 发出的团队消息（R-249，ks team sent）：最近的在前，最多 limit 行（一次广播的每个收件人各一行）。
func (s *Store) SentTells(ctx context.Context, key string, limit int) ([]Decision, error) {
	return s.queryDecs(ctx, decSelect+` WHERE x.kind = ? AND x.assigned_by = ? ORDER BY x.seq DESC LIMIT ?`, KindTell, key, limit)
}

// BroadcastTells 列出同一次广播拆出的各行（R-249），按编号升序。
func (s *Store) BroadcastTells(ctx context.Context, broadcastID string) ([]Decision, error) {
	if broadcastID == "" {
		return []Decision{}, nil
	}
	return s.queryDecs(ctx, decSelect+` WHERE x.kind = ? AND x.broadcast_id = ? ORDER BY x.seq`, KindTell, broadcastID)
}

// WaitDeliveredSince 列出送给会话 key、经 ks wait 送达（delivered_turn = ks-wait）、要动手（不是只告知）、挂在需求上、
// 送达时间不早于 since 的记录，按编号升序（R-278：紧接着重挂等待时重列还没开始处理的指派）。
func (s *Store) WaitDeliveredSince(ctx context.Context, key string, since time.Time) ([]Decision, error) {
	return s.queryDecs(ctx, decSelect+` WHERE x.session_key = ? AND x.status = ? AND x.delivered_turn = ? AND x.fyi = 0
		AND COALESCE(x.requirement_id, '') <> '' AND x.delivered_at >= ? ORDER BY x.seq`, key, DecDelivered, "ks-wait", ts(since))
}
