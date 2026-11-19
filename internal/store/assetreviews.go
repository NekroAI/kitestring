package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/NekroAI/kitestring/internal/protocol"
)

// 资源库的审核（R-351 第 4 步）：每个素材标「通过」或「返工」并写意见；意见用 kind = asset_review 的一条送达提交素材的会话，
// 那个会话已结束时送同项目主调度（D-1714）。审核记录的作者区分你 / 会话 / 访客（D-1782，访客功能留给 R-365）。

// KindAssetReview 是送给会话的审核意见（R-351）：answer 是写好的一段（结论、素材、意见），answer_files 是附图；只送达，不改需求状态。
const KindAssetReview = "asset_review"

// QuietKind：哪些记录可以标「只告知」（fyi）——团队消息（R-249），以及资源库里「通过」的审核意见（不用再动手，
// 不唤醒待命的会话，下次被唤醒时一并送达；会话已结束也不进「待处理」的待转交，设计原则第 12 条）。
func QuietKind(kind string) bool { return kind == KindTell || kind == KindAssetReview }

// 审核意见的收件人是谁（asset_reviews.sent_as）。
const (
	SentToSubmitter = "submitter" // 提交素材的会话
	SentToPM        = "pm"        // 提交的会话已结束，转给同项目主调度
)

// AssetAuthor 是审核意见的作者，与提交者同一种表示（user 你 / session 会话 / guest 分享页访客）。
type AssetAuthor = AssetSubmitter

// AssetNote 是只送意见、不改状态的一条（R-365：放入访客的意见时由你选「只送意见」）。
const AssetNote = "note"

// AssetReview 是一次审核。
type AssetReview struct {
	ID       string      `json:"id"`
	AssetID  string      `json:"asset_id"`
	Version  int         `json:"version" doc:"审核时的最新版本；访客的意见是访客看的那一版"`
	Verdict  string      `json:"verdict" enum:"approved,rework,note" doc:"approved 通过 / rework 返工 / note 只送意见（访客的意见，不改状态）"`
	Comment  string      `json:"comment"`
	Files    []FileRef   `json:"files"`
	Author   AssetAuthor `json:"author" doc:"写意见的人：user 你 / session 会话 / guest 分享页访客（由你放入，R-365）"`
	Via      string      `json:"via,omitempty" doc:"访客的意见：经由的分享名称"`
	Addend   string      `json:"addend,omitempty" doc:"访客的意见：你放入时写的附言"`
	SentTo   string      `json:"sent_to,omitempty" doc:"意见送给的会话键；空 = 没有可送达的会话"`
	SentAs   string      `json:"sent_as,omitempty" enum:"submitter,pm," doc:"submitter 提交素材的会话 / pm 提交的会话已结束，转给同项目主调度"`
	SentName string      `json:"sent_name,omitempty" doc:"收件会话当时的标题"`
	Frame    *AssetFrame `json:"frame,omitempty" doc:"意见挂在模型动作的哪一帧（R-392）；没挂时为空"`
	// 送达情况取自那条送达记录
	DecisionCode string     `json:"decision_code,omitempty"`
	Delivery     string     `json:"delivery,omitempty" enum:"answered,delivered,closed," doc:"answered 待送达 / delivered 已送达 / closed 已关闭；没有送达记录时为空"`
	DeliveredAt  *time.Time `json:"delivered_at,omitempty"`
	CreatedAt    time.Time  `json:"created_at"`
}

// NewAssetReview 是一次审核的参数。
type NewAssetReview struct {
	Asset   string // ID 或 A-x
	Verdict string // approved / rework
	From    string // 审核时看到的状态；不为空时要求当前仍是它
	Version int    // 审核时看到的最新版本；不为 0 时要求仍是最新
	Comment string
	Files   []FileRef
	Author  AssetAuthor
	// 访客的意见（R-365）：由你放入；Verdict 可以是 rework（改返工；本来就是返工时只追加意见）或 note（只送意见、不改状态）
	Via      string
	Addend   string
	Version0 int         // 访客看的那一版，记在审核记录上
	Frame    *AssetFrame // 意见挂在哪一帧（R-392）
}

// AssetFrame 是意见挂在模型动作的哪一帧（R-392）：动作名、第几秒、模型是第几版。
type AssetFrame struct {
	Anim string  `json:"anim" doc:"动作名（GLB 里的动画名）"`
	T    float64 `json:"t" doc:"第几秒"`
	V    int     `json:"v" doc:"挂意见时看的模型版本"`
}

// Text 是送给会话、显示在意见里的写法：「动作 run · 0.80 秒（v3）」。
func (f *AssetFrame) Text() string {
	if f == nil {
		return ""
	}
	return fmt.Sprintf("动作 %s · %.2f 秒（v%d）", f.Anim, f.T, f.V)
}

// CleanFrame 规整一帧：动作名去空白、最长 200 字，秒数取非负、保留到毫秒；动作名为空时返回 nil。
func CleanFrame(f *AssetFrame) *AssetFrame {
	if f == nil {
		return nil
	}
	name := strings.TrimSpace(f.Anim)
	if name == "" {
		return nil
	}
	if r := []rune(name); len(r) > 200 {
		name = string(r[:200])
	}
	t := f.T
	if t < 0 || t != t || t > 1e6 {
		t = 0
	}
	t = float64(int64(t*1000+0.5)) / 1000
	v := f.V
	if v < 0 {
		v = 0
	}
	return &AssetFrame{Anim: name, T: t, V: v}
}

func frameJSON(f *AssetFrame) string {
	if f == nil {
		return ""
	}
	b, _ := json.Marshal(f)
	return string(b)
}

func parseFrame(s string) *AssetFrame {
	if s == "" {
		return nil
	}
	var f AssetFrame
	if json.Unmarshal([]byte(s), &f) != nil || f.Anim == "" {
		return nil
	}
	return &f
}

// ErrAssetStale 是审核时素材已有新版本或状态已变。
var ErrAssetStale = errors.New("素材已变化")

// ReviewAsset 记一次审核并改素材状态：返工必须写意见或附图；状态变化要在 AssetCanMove 里。
func (s *Store) ReviewAsset(ctx context.Context, n NewAssetReview) (AssetReview, Asset, error) {
	var r AssetReview
	guest := n.Author.Kind == SubmitterGuest
	if n.Verdict != AssetApproved && n.Verdict != AssetRework && !(guest && n.Verdict == AssetNote) {
		return r, Asset{}, fmt.Errorf("审核结论只能是 approved（通过）或 rework（返工）：%s", n.Verdict)
	}
	if guest && n.Verdict == AssetApproved {
		return r, Asset{}, errors.New("访客的意见不能作为通过")
	}
	n.Comment = strings.TrimSpace(n.Comment)
	if n.Verdict == AssetRework && n.Comment == "" && len(n.Files) == 0 {
		return r, Asset{}, errors.New("返工需写明修改意见")
	}
	d, err := s.GetAsset(ctx, n.Asset)
	if err != nil {
		return r, Asset{}, err
	}
	if n.Version != 0 && n.Version != d.Version {
		return r, Asset{}, fmt.Errorf("%w：%s 已有新版本 v%d，刷新后查看", ErrAssetStale, d.Code, d.Version)
	}
	at := now()
	err = s.tx(ctx, func(t *sql.Tx) error {
		var cur string
		var version int
		if err := t.QueryRowContext(ctx, `SELECT status, version FROM assets WHERE id = ?`, d.ID).Scan(&cur, &version); err != nil {
			return err
		}
		if (n.From != "" && cur != n.From) || (n.Version != 0 && version != n.Version) {
			return fmt.Errorf("%w：状态已变化，刷新后查看", ErrAssetStale)
		}
		// 访客的意见：返工时任何状态都可以改成返工（已是返工只追加意见）；只送意见不改状态
		move := n.Verdict != AssetNote && !(guest && cur == AssetRework)
		if move && !guest && !AssetCanMove(cur, n.Verdict) {
			return fmt.Errorf("%w：%s → %s", ErrAssetMove, cur, n.Verdict)
		}
		if move {
			if _, err := t.ExecContext(ctx, `UPDATE assets SET status = ?, status_at = ?, updated_at = ? WHERE id = ?`, n.Verdict, at, at, d.ID); err != nil {
				return err
			}
			d.Status = n.Verdict
		}
		v := version
		if guest && n.Version0 > 0 {
			v = n.Version0
		}
		r = AssetReview{ID: protocol.NewID(), AssetID: d.ID, Version: v, Verdict: n.Verdict, Comment: n.Comment, Files: n.Files, Author: n.Author, Via: n.Via, Addend: n.Addend, Frame: CleanFrame(n.Frame)}
		if r.Files == nil {
			r.Files = []FileRef{}
		}
		_, err := t.ExecContext(ctx, `INSERT INTO asset_reviews(id, asset_id, version, verdict, comment, files, author_kind, author_ref, author_name, via, addend, created_at, frame) VALUES(?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			r.ID, d.ID, v, n.Verdict, n.Comment, filesJSON(r.Files), n.Author.Kind, n.Author.Ref, n.Author.Name, n.Via, n.Addend, at, frameJSON(r.Frame))
		return err
	})
	if err != nil {
		return r, Asset{}, err
	}
	r.CreatedAt = parseTS(at)
	return r, d.Asset, nil
}

// LinkAssetReview 记下审核意见送给了谁、用的哪条送达记录。
func (s *Store) LinkAssetReview(ctx context.Context, id, decisionID, sentTo, sentAs, sentName string) error {
	_, err := s.db.ExecContext(ctx, `UPDATE asset_reviews SET decision_id = ?, sent_to = ?, sent_as = ?, sent_name = ? WHERE id = ?`, decisionID, sentTo, sentAs, sentName, id)
	return err
}

const reviewCols = `r.id, r.asset_id, r.version, r.verdict, r.comment, r.files, r.author_kind, r.author_ref, r.author_name, r.sent_to, r.sent_as, r.sent_name, r.via, r.addend, r.frame,
	COALESCE(x.seq, 0), COALESCE(x.status, ''), x.delivered_at, r.created_at`

func scanReview(row interface{ Scan(...any) error }) (AssetReview, string, error) {
	var r AssetReview
	var files, created, frame string
	var seq int
	var delivered sql.NullString
	err := row.Scan(&r.ID, &r.AssetID, &r.Version, &r.Verdict, &r.Comment, &files, &r.Author.Kind, &r.Author.Ref, &r.Author.Name, &r.SentTo, &r.SentAs, &r.SentName, &r.Via, &r.Addend, &frame,
		&seq, &r.Delivery, &delivered, &created)
	if err != nil {
		return r, "", err
	}
	r.Files = parseFiles(files)
	r.Frame = parseFrame(frame)
	if seq > 0 {
		r.DecisionCode = decCode(seq)
	}
	r.DeliveredAt = parseNullTS(delivered)
	r.CreatedAt = parseTS(created)
	return r, created, nil
}

// ListAssetReviews 列出素材的审核记录，新的在前。
func (s *Store) ListAssetReviews(ctx context.Context, assetID string) ([]AssetReview, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT `+reviewCols+` FROM asset_reviews r LEFT JOIN decisions x ON x.id = r.decision_id WHERE r.asset_id = ? ORDER BY r.created_at DESC, r.rowid DESC`, assetID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []AssetReview{}
	for rows.Next() {
		r, _, err := scanReview(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// AssetCounts 数各状态的素材（全部项目），侧边栏的待审数用。
func (s *Store) AssetCounts(ctx context.Context) (map[string]int, error) {
	out := map[string]int{}
	for _, st := range AssetStatuses {
		out[st] = 0
	}
	rows, err := s.db.QueryContext(ctx, `SELECT status, COUNT(*) FROM assets GROUP BY status`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var st string
		var n int
		if err := rows.Scan(&st, &n); err != nil {
			return nil, err
		}
		out[st] = n
	}
	return out, rows.Err()
}

// ---------- 附件（R-365） ----------

// AssetAttachment 是素材的一个附件：访客上传、你收下为附件的文件（动画文件、参考视频等），不算版本。
type AssetAttachment struct {
	ID        string         `json:"id"`
	Version   int            `json:"version" doc:"收下时素材的最新版本"`
	File      FileRef        `json:"file"`
	Note      string         `json:"note,omitempty" doc:"访客上传时写的说明"`
	Submitter AssetSubmitter `json:"submitter" doc:"上传的人（访客）"`
	CreatedAt time.Time      `json:"created_at"`
}

// AddAssetAttachment 给素材记一个附件。
func (s *Store) AddAssetAttachment(ctx context.Context, assetID, fileID, note string, sub AssetSubmitter) (AssetAttachment, error) {
	d, err := s.GetAsset(ctx, assetID)
	if err != nil {
		return AssetAttachment{}, err
	}
	f, err := s.GetFile(ctx, fileID)
	if err != nil {
		return AssetAttachment{}, err
	}
	a := AssetAttachment{ID: protocol.NewID(), Version: d.Version, File: f, Note: note, Submitter: sub}
	at := now()
	if _, err := s.db.ExecContext(ctx, `INSERT INTO asset_attachments(id, asset_id, version, file_id, note, submitter_kind, submitter_ref, submitter_name, created_at) VALUES(?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		a.ID, d.ID, a.Version, fileID, note, sub.Kind, sub.Ref, sub.Name, at); err != nil {
		return a, err
	}
	_, _ = s.db.ExecContext(ctx, `UPDATE assets SET updated_at = ? WHERE id = ?`, at, d.ID)
	a.CreatedAt = parseTS(at)
	return a, nil
}

// ListAssetAttachments 列出素材的附件，新的在前。
func (s *Store) ListAssetAttachments(ctx context.Context, assetID string) ([]AssetAttachment, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT x.id, x.version, x.file_id, COALESCE(f.name, ''), COALESCE(f.mime, ''), COALESCE(f.size, 0), x.note, x.submitter_kind, x.submitter_ref, x.submitter_name, x.created_at
		FROM asset_attachments x LEFT JOIN files f ON f.id = x.file_id WHERE x.asset_id = ? ORDER BY x.created_at DESC, x.rowid DESC`, assetID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []AssetAttachment{}
	for rows.Next() {
		var a AssetAttachment
		var created string
		if err := rows.Scan(&a.ID, &a.Version, &a.File.ID, &a.File.Name, &a.File.Mime, &a.File.Size, &a.Note, &a.Submitter.Kind, &a.Submitter.Ref, &a.Submitter.Name, &created); err != nil {
			return nil, err
		}
		a.CreatedAt = parseTS(created)
		out = append(out, a)
	}
	return out, rows.Err()
}

// AssetFileUse 是引用了某个附件的素材：哪一版的主文件 / 预览图，或素材的附件（R-365 访客页按它放行文件）。
type AssetFileUse struct {
	AssetID    string
	Version    int  // 版本号；附件是收下时的最新版本
	Main       bool // 这一版的主文件（GLB 或独立的图）：原文件，受下载开关管
	Attachment bool // 素材的附件
}

// AssetFileUses 找出引用了附件 id 的素材版本（主文件、预览图、自动生成的缩略图）和素材附件。
func (s *Store) AssetFileUses(ctx context.Context, id string) ([]AssetFileUse, error) {
	if id == "" || strings.ContainsAny(id, `%_"\`) {
		return nil, nil
	}
	var out []AssetFileUse
	rows, err := s.db.QueryContext(ctx, `SELECT asset_id, version, file_id = ? FROM asset_versions WHERE file_id = ? OR previews LIKE ? OR auto_thumb = ?`, id, id, "%"+id+"%", id)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var u AssetFileUse
		if err := rows.Scan(&u.AssetID, &u.Version, &u.Main); err != nil {
			rows.Close()
			return nil, err
		}
		out = append(out, u)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}
	rows, err = s.db.QueryContext(ctx, `SELECT asset_id, version FROM asset_attachments WHERE file_id = ?`, id)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		u := AssetFileUse{Attachment: true}
		if err := rows.Scan(&u.AssetID, &u.Version); err != nil {
			return nil, err
		}
		out = append(out, u)
	}
	return out, rows.Err()
}
