package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"strings"
	"time"

	"github.com/NekroAI/kitestring/internal/protocol"
)

// 访客留言（R-281）：访客在分享页上对需求、决策、证据、阶段的留言。
// 留言先进待收队列（pending），不直接写进需求；所有者原样或改写放入（placed）后才成为那条需求的留言（kind = note，guest 列记来源），
// 也可以忽略（ignored）。访客的写接口只能新建这里的待收留言，别的什么都写不了。

// 访客留言针对的对象。
const (
	GuestOnReq      = "req"
	GuestOnDec      = "dec"
	GuestOnEvidence = "evidence"
	GuestOnStage    = "stage"
	GuestOnAsset    = "asset" // 资源库素材的某一版（R-365）；带文件时是访客上传
)

// 访客对素材的留言 / 上传被放入（收下）成了什么（R-365）。
const (
	PlacedReview     = "review"     // 留言放入为意见（返工意见或只送意见）
	PlacedVersion    = "version"    // 上传收下为新版本
	PlacedAttachment = "attachment" // 上传收下为附件
)

// 访客留言的状态。
const (
	GuestPending = "pending" // 待收
	GuestPlaced  = "placed"  // 已放入
	GuestIgnored = "ignored" // 已忽略
)

// GuestTextMax 是一条访客留言的字数上限。
const GuestTextMax = 2000

// ErrGuestHandled：这条访客留言已经放入或忽略过了。
var ErrGuestHandled = errors.New("这条访客留言已处理")

// GuestOrigin 是放入需求的访客留言的来源，存在决策的 guest 列（JSON）。
type GuestOrigin struct {
	Comment string    `json:"comment" doc:"访客留言 ID"`
	Name    string    `json:"name" doc:"访客名字（访客链接的名字）"`
	Share   string    `json:"share" doc:"经由的分享名称"`
	At      time.Time `json:"at" doc:"访客留言的时间"`
	// 针对的对象：挂在需求本身时 target_kind = req、其余为空
	TargetKind  string `json:"target_kind" enum:"req,dec,evidence,stage,asset"`
	TargetCode  string `json:"target_code,omitempty" doc:"决策编号 D-x；阶段是所属计划编号 P-x"`
	TargetSeq   int    `json:"target_seq,omitempty" doc:"阶段：第几阶段"`
	TargetTitle string `json:"target_title,omitempty" doc:"证据的结论第一句、阶段标题"`
	Original    string `json:"original,omitempty" doc:"改写放入时访客的原话；原样放入时为空"`
	Addend      string `json:"addend,omitempty" doc:"所有者放入时写的附言"`
}

// ShareComment 是一条访客留言。
type ShareComment struct {
	ID        string `json:"id"`
	ShareID   string `json:"share_id"`
	ShareName string `json:"share_name"`
	LinkID    string `json:"link_id"`
	GuestName string `json:"guest_name" doc:"访客名字（访客链接的名字）"`
	ProjectID string `json:"project_id,omitempty"`
	// 针对的对象
	TargetKind  string `json:"target_kind" enum:"req,dec,evidence,stage,asset"`
	TargetRef   string `json:"target_ref" doc:"需求 / 决策 / 证据 / 阶段 / 素材的 ID"`
	TargetCode  string `json:"target_code,omitempty" doc:"决策编号 D-x；阶段是所属计划编号 P-x；素材是 A-x"`
	TargetSeq   int    `json:"target_seq,omitempty" doc:"阶段：第几阶段"`
	TargetTitle string `json:"target_title,omitempty" doc:"证据的结论第一句、阶段标题、决策的问题"`
	// 所属需求（挂在阶段上时为空）
	RequirementID    string     `json:"requirement_id,omitempty"`
	RequirementCode  string     `json:"requirement_code,omitempty"`
	RequirementTitle string     `json:"requirement_title,omitempty"`
	PlanID           string     `json:"plan_id,omitempty"`
	PlanTitle        string     `json:"plan_title,omitempty"`
	Text             string     `json:"text"`
	Status           string     `json:"status" enum:"pending,placed,ignored" doc:"pending 待收 / placed 已放入 / ignored 已忽略"`
	DecisionCode     string     `json:"decision_code,omitempty" doc:"放入后成为的需求留言编号 D-x"`
	CreatedAt        time.Time  `json:"created_at"`
	HandledAt        *time.Time `json:"handled_at,omitempty"`
	// 资源库（R-365）：target_kind = asset 时
	AssetKey     string   `json:"asset_key,omitempty" doc:"素材的 key"`
	AssetKind    string   `json:"asset_kind,omitempty" enum:"part,image,audio," doc:"part 零件 / image 独立的图 / audio 音效"`
	AssetBatch   string   `json:"asset_batch,omitempty"`
	AssetVersion int      `json:"asset_version,omitempty" doc:"访客看的是第几版"`
	AssetLatest  int      `json:"asset_latest,omitempty" doc:"素材现在的最新版本"`
	File         *FileRef `json:"file,omitempty" doc:"访客上传的文件；没有时是一条留言"`
	PlacedAs     string   `json:"placed_as,omitempty" enum:"review,version,attachment," doc:"放入成了什么：review 意见 / version 新版本 / attachment 附件"`
	ResultRef    string   `json:"result_ref,omitempty" doc:"收下后的版本号（v3）或附件 ID、审核记录 ID"`
	// 挂在模型动作某一帧的留言（R-392）：帧和当时画面的截图，放入为意见时一并带上
	Frame *AssetFrame `json:"frame,omitempty" doc:"留言挂在模型动作的哪一帧"`
	Shot  *FileRef    `json:"shot,omitempty" doc:"留言附的当前画面截图"`
}

const shareCommentSelect = `SELECT c.id, c.share_id, COALESCE(sh.name, ''), c.link_id, c.guest_name, COALESCE(NULLIF(r.project_id, ''), NULLIF(pl.project_id, ''), sh.project_id, ''),
	c.target_kind, c.target_ref, c.requirement_id, COALESCE(r.seq, 0), COALESCE(r.title, ''), c.plan_id, COALESCE(pl.seq, 0), COALESCE(pl.title, ''), c.stage_seq,
	c.text, c.status, COALESCE(d.seq, 0), c.created_at, c.handled_at,
	COALESCE(dx.seq, 0), COALESCE(dx.question, ''), COALESCE(ev.payload, ''), COALESCE(ev.summary, ''), COALESCE(ps.title, ''),
	COALESCE(a.seq, 0), COALESCE(a.key, ''), COALESCE(a.kind, ''), COALESCE(a.batch, ''), COALESCE(a.version, 0), COALESCE(a.project_id, ''),
	c.asset_version, c.file_id, COALESCE(fl.name, ''), COALESCE(fl.mime, ''), COALESCE(fl.size, 0), c.placed_as, c.result_ref,
	c.frame, c.shot_id, COALESCE(fs.name, ''), COALESCE(fs.mime, ''), COALESCE(fs.size, 0)
	FROM share_comments c
	LEFT JOIN shares sh ON sh.id = c.share_id
	LEFT JOIN requirements r ON r.id = c.requirement_id
	LEFT JOIN plans pl ON pl.id = c.plan_id
	LEFT JOIN decisions d ON d.id = c.decision_id
	LEFT JOIN decisions dx ON c.target_kind = 'dec' AND dx.id = c.target_ref
	LEFT JOIN evidence ev ON c.target_kind = 'evidence' AND ev.id = c.target_ref
	LEFT JOIN plan_stages ps ON c.target_kind = 'stage' AND ps.id = c.target_ref
	LEFT JOIN assets a ON c.target_kind = 'asset' AND a.id = c.target_ref
	LEFT JOIN files fl ON c.file_id <> '' AND fl.id = c.file_id
	LEFT JOIN files fs ON c.shot_id <> '' AND fs.id = c.shot_id`

func scanShareComment(row interface{ Scan(...any) error }) (ShareComment, error) {
	var c ShareComment
	var rseq, pseq, dseq, xseq, aseq int
	var created, handled, xq, evVerdict, evSummary, stTitle, aproj, fileID, frame, shotID string
	var f, shot FileRef
	err := row.Scan(&c.ID, &c.ShareID, &c.ShareName, &c.LinkID, &c.GuestName, &c.ProjectID,
		&c.TargetKind, &c.TargetRef, &c.RequirementID, &rseq, &c.RequirementTitle, &c.PlanID, &pseq, &c.PlanTitle, &c.TargetSeq,
		&c.Text, &c.Status, &dseq, &created, &handled, &xseq, &xq, &evVerdict, &evSummary, &stTitle,
		&aseq, &c.AssetKey, &c.AssetKind, &c.AssetBatch, &c.AssetLatest, &aproj,
		&c.AssetVersion, &fileID, &f.Name, &f.Mime, &f.Size, &c.PlacedAs, &c.ResultRef,
		&frame, &shotID, &shot.Name, &shot.Mime, &shot.Size)
	if err != nil {
		return c, err
	}
	c.Frame = parseFrame(frame)
	if shotID != "" {
		shot.ID = shotID
		c.Shot = &shot
	}
	if fileID != "" {
		f.ID = fileID
		c.File = &f
	}
	if rseq > 0 {
		c.RequirementCode = reqCode(rseq)
	}
	if dseq > 0 {
		c.DecisionCode = decCode(dseq)
	}
	c.CreatedAt = parseTS(created)
	c.HandledAt = parseNullTS(sql.NullString{String: handled, Valid: handled != ""})
	switch c.TargetKind {
	case GuestOnDec:
		if xseq > 0 {
			c.TargetCode = decCode(xseq)
		}
		c.TargetTitle = shareLine(xq, 60)
	case GuestOnEvidence:
		c.TargetTitle = evidenceLine(evVerdict, evSummary)
	case GuestOnStage:
		if pseq > 0 {
			c.TargetCode = planCode(pseq)
		}
		c.TargetTitle = stTitle
	case GuestOnAsset:
		if aseq > 0 {
			c.TargetCode = AssetCode(aseq)
		}
		c.TargetTitle = c.AssetKey
		if aproj != "" {
			c.ProjectID = aproj
		}
		c.TargetSeq = 0
	default:
		c.TargetSeq = 0
	}
	return c, nil
}

// evidenceLine：证据的一句话——结构化证据的结论，老格式取正文第一行。
func evidenceLine(payload, summary string) string {
	var p struct {
		Verdict string `json:"verdict"`
	}
	if payload != "" && json.Unmarshal([]byte(payload), &p) == nil && strings.TrimSpace(p.Verdict) != "" {
		return shareLine(p.Verdict, 60)
	}
	return shareLine(summary, 60)
}

// GuestCommentIn 是访客写的一条留言（hub 已核对过对象在分享范围里）。
type GuestCommentIn struct {
	ShareID, LinkID, GuestName string
	TargetKind, TargetRef      string
	RequirementID              string
	PlanID                     string
	StageSeq                   int
	Text                       string
	AssetVersion               int         // 素材的第几版（R-365）
	FileID                     string      // 访客上传的文件（R-365）；带文件时正文可以为空
	Frame                      *AssetFrame // 挂在模型动作的哪一帧（R-392）
	ShotID                     string      // 当前画面的截图（R-392）
}

// CreateShareComment 记一条待收的访客留言。
func (s *Store) CreateShareComment(ctx context.Context, in GuestCommentIn) (ShareComment, error) {
	in.Text = strings.TrimSpace(in.Text)
	if in.Text == "" && in.FileID == "" {
		return ShareComment{}, errors.New("留言不能为空")
	}
	id := protocol.NewID()
	_, err := s.db.ExecContext(ctx, `INSERT INTO share_comments(id, share_id, link_id, guest_name, target_kind, target_ref, requirement_id, plan_id, stage_seq, text, status, created_at, asset_version, file_id, frame, shot_id) VALUES(?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		id, in.ShareID, in.LinkID, in.GuestName, in.TargetKind, in.TargetRef, in.RequirementID, in.PlanID, in.StageSeq, in.Text, GuestPending, now(), in.AssetVersion, in.FileID, frameJSON(CleanFrame(in.Frame)), in.ShotID)
	if err != nil {
		return ShareComment{}, err
	}
	return s.GetShareComment(ctx, id)
}

// GetShareComment 取一条访客留言。
func (s *Store) GetShareComment(ctx context.Context, id string) (ShareComment, error) {
	c, err := scanShareComment(s.db.QueryRowContext(ctx, shareCommentSelect+` WHERE c.id = ?`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return c, ErrNotFound
	}
	return c, err
}

func (s *Store) shareComments(ctx context.Context, where string, args ...any) ([]ShareComment, error) {
	rows, err := s.db.QueryContext(ctx, shareCommentSelect+` WHERE `+where, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []ShareComment{}
	for rows.Next() {
		c, err := scanShareComment(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

// PendingShareComments 列出待收的访客留言（全部分享），早的在前。
func (s *Store) PendingShareComments(ctx context.Context) ([]ShareComment, error) {
	return s.shareComments(ctx, `c.status = ? ORDER BY c.created_at`, GuestPending)
}

// LinkComments 列出一条访客链接写过的留言，新的在前（访客页上「你的留言」）。
func (s *Store) LinkComments(ctx context.Context, linkID string, limit int) ([]ShareComment, error) {
	return s.shareComments(ctx, `c.link_id = ? ORDER BY c.created_at DESC LIMIT ?`, linkID, limit)
}

// PendingShareCounts 返回每个分享待收的访客留言条数。
func (s *Store) PendingShareCounts(ctx context.Context) (map[string]int, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT share_id, COUNT(*) FROM share_comments WHERE status = ? GROUP BY share_id`, GuestPending)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]int{}
	for rows.Next() {
		var id string
		var n int
		if err := rows.Scan(&id, &n); err != nil {
			return nil, err
		}
		out[id] = n
	}
	return out, rows.Err()
}

// RecentLinkComments：一条访客链接在 since 之后写了几条留言（限流用）。
func (s *Store) RecentLinkComments(ctx context.Context, linkID string, since time.Time) (int, error) {
	var n int
	err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM share_comments WHERE link_id = ? AND created_at >= ?`, linkID, ts(since)).Scan(&n)
	return n, err
}

// IgnoreShareComment 忽略一条待收的访客留言。
func (s *Store) IgnoreShareComment(ctx context.Context, id string) (ShareComment, error) {
	res, err := s.db.ExecContext(ctx, `UPDATE share_comments SET status = ?, handled_at = ? WHERE id = ? AND status = ?`, GuestIgnored, now(), id, GuestPending)
	if err != nil {
		return ShareComment{}, err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		if _, err := s.GetShareComment(ctx, id); err != nil {
			return ShareComment{}, err
		}
		return ShareComment{}, ErrGuestHandled
	}
	return s.GetShareComment(ctx, id)
}

// PlaceShareComment 把一条待收的访客留言放入需求 r：写成一条需求留言（kind = note，送达 target 会话），guest 列记来源。
// text 是放进去的正文（原样放入时就是访客原话），origin 由调用方填好（改写时带原话、附言）。
func (s *Store) PlaceShareComment(ctx context.Context, c ShareComment, r Requirement, target, text string, origin GuestOrigin) (Decision, error) {
	// 先占住这条留言（并发下只放入一次），写不成再放回去
	res, err := s.db.ExecContext(ctx, `UPDATE share_comments SET status = ?, handled_at = ? WHERE id = ? AND status = ?`, GuestPlaced, now(), c.ID, GuestPending)
	if err != nil {
		return Decision{}, err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return Decision{}, ErrGuestHandled
	}
	d, err := s.CreateDecision(ctx, Decision{Kind: KindNote, SessionKey: target, ProjectID: r.ProjectID, RequirementID: r.ID, Question: r.Title, Answer: text})
	if err != nil {
		_, _ = s.db.ExecContext(ctx, `UPDATE share_comments SET status = ?, handled_at = '' WHERE id = ?`, GuestPending, c.ID)
		return Decision{}, err
	}
	g, _ := json.Marshal(origin)
	if _, err := s.db.ExecContext(ctx, `UPDATE decisions SET guest = ? WHERE id = ?`, string(g), d.ID); err != nil {
		return Decision{}, err
	}
	if _, err := s.db.ExecContext(ctx, `UPDATE share_comments SET decision_id = ? WHERE id = ?`, d.ID, c.ID); err != nil {
		return Decision{}, err
	}
	return s.GetDecision(ctx, d.ID)
}

// EvidenceOwner 返回证据所属的需求 ID（作废的证据不算，访客页上也不显示）。
func (s *Store) EvidenceOwner(ctx context.Context, id string) (string, error) {
	var rid string
	err := s.db.QueryRowContext(ctx, `SELECT requirement_id FROM evidence WHERE id = ? AND voided = 0`, id).Scan(&rid)
	if errors.Is(err, sql.ErrNoRows) {
		return "", ErrNotFound
	}
	return rid, err
}

// StagePlace 返回阶段所属的计划 ID 和阶段序号。
func (s *Store) StagePlace(ctx context.Context, id string) (string, int, error) {
	var planID string
	var seq int
	err := s.db.QueryRowContext(ctx, `SELECT plan_id, seq FROM plan_stages WHERE id = ?`, id).Scan(&planID, &seq)
	if errors.Is(err, sql.ErrNoRows) {
		return "", 0, ErrNotFound
	}
	return planID, seq, err
}

// ClaimShareComment 把一条待收的访客留言占住记为已放入（并发下只放入一次）；占不到返回 ErrGuestHandled（R-365 素材的留言和上传用）。
// 后面的写入失败时用 ReleaseShareComment 放回去。
func (s *Store) ClaimShareComment(ctx context.Context, id string) error {
	res, err := s.db.ExecContext(ctx, `UPDATE share_comments SET status = ?, handled_at = ? WHERE id = ? AND status = ?`, GuestPlaced, now(), id, GuestPending)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		if _, err := s.GetShareComment(ctx, id); err != nil {
			return err
		}
		return ErrGuestHandled
	}
	return nil
}

// ReleaseShareComment 把占住的访客留言放回待收。
func (s *Store) ReleaseShareComment(ctx context.Context, id string) {
	_, _ = s.db.ExecContext(ctx, `UPDATE share_comments SET status = ?, handled_at = '' WHERE id = ?`, GuestPending, id)
}

// SetShareCommentResult 记下访客对素材的留言 / 上传放入成了什么（R-365）。
func (s *Store) SetShareCommentResult(ctx context.Context, id, as, ref string) error {
	_, err := s.db.ExecContext(ctx, `UPDATE share_comments SET placed_as = ?, result_ref = ? WHERE id = ?`, as, ref, id)
	return err
}

// RecentLinkUploads：一条访客链接在 since 之后上传了几个文件、合计多少字节（限流用，R-365）。
func (s *Store) RecentLinkUploads(ctx context.Context, linkID string, since time.Time) (n int, bytes int64, err error) {
	err = s.db.QueryRowContext(ctx, `SELECT COUNT(*), COALESCE(SUM(f.size), 0) FROM share_comments c LEFT JOIN files f ON f.id = c.file_id
		WHERE c.link_id = ? AND c.file_id <> '' AND c.created_at >= ?`, linkID, ts(since)).Scan(&n, &bytes)
	return n, bytes, err
}
