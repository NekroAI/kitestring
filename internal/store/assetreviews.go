package store

import (
	"context"
	"database/sql"
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

// AssetReview 是一次审核。
type AssetReview struct {
	ID       string      `json:"id"`
	AssetID  string      `json:"asset_id"`
	Version  int         `json:"version" doc:"审核时的最新版本"`
	Verdict  string      `json:"verdict" enum:"approved,rework" doc:"approved 通过 / rework 返工"`
	Comment  string      `json:"comment"`
	Files    []FileRef   `json:"files"`
	Author   AssetAuthor `json:"author" doc:"写意见的人：user 你 / session 会话 / guest 分享页访客（R-365 预留）"`
	SentTo   string      `json:"sent_to,omitempty" doc:"意见送给的会话键；空 = 没有可送达的会话"`
	SentAs   string      `json:"sent_as,omitempty" enum:"submitter,pm," doc:"submitter 提交素材的会话 / pm 提交的会话已结束，转给同项目主调度"`
	SentName string      `json:"sent_name,omitempty" doc:"收件会话当时的标题"`
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
}

// ErrAssetStale 是审核时素材已有新版本或状态已变。
var ErrAssetStale = errors.New("素材已变化")

// ReviewAsset 记一次审核并改素材状态：返工必须写意见或附图；状态变化要在 AssetCanMove 里。
func (s *Store) ReviewAsset(ctx context.Context, n NewAssetReview) (AssetReview, Asset, error) {
	var r AssetReview
	if n.Verdict != AssetApproved && n.Verdict != AssetRework {
		return r, Asset{}, fmt.Errorf("审核结论只能是 approved（通过）或 rework（返工）：%s", n.Verdict)
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
		if !AssetCanMove(cur, n.Verdict) {
			return fmt.Errorf("%w：%s → %s", ErrAssetMove, cur, n.Verdict)
		}
		if _, err := t.ExecContext(ctx, `UPDATE assets SET status = ?, status_at = ?, updated_at = ? WHERE id = ?`, n.Verdict, at, at, d.ID); err != nil {
			return err
		}
		r = AssetReview{ID: protocol.NewID(), AssetID: d.ID, Version: version, Verdict: n.Verdict, Comment: n.Comment, Files: n.Files, Author: n.Author}
		if r.Files == nil {
			r.Files = []FileRef{}
		}
		_, err := t.ExecContext(ctx, `INSERT INTO asset_reviews(id, asset_id, version, verdict, comment, files, author_kind, author_ref, author_name, created_at) VALUES(?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			r.ID, d.ID, version, n.Verdict, n.Comment, filesJSON(r.Files), n.Author.Kind, n.Author.Ref, n.Author.Name, at)
		return err
	})
	if err != nil {
		return r, Asset{}, err
	}
	r.CreatedAt = parseTS(at)
	d.Status = n.Verdict
	return r, d.Asset, nil
}

// LinkAssetReview 记下审核意见送给了谁、用的哪条送达记录。
func (s *Store) LinkAssetReview(ctx context.Context, id, decisionID, sentTo, sentAs, sentName string) error {
	_, err := s.db.ExecContext(ctx, `UPDATE asset_reviews SET decision_id = ?, sent_to = ?, sent_as = ?, sent_name = ? WHERE id = ?`, decisionID, sentTo, sentAs, sentName, id)
	return err
}

const reviewCols = `r.id, r.asset_id, r.version, r.verdict, r.comment, r.files, r.author_kind, r.author_ref, r.author_name, r.sent_to, r.sent_as, r.sent_name,
	COALESCE(x.seq, 0), COALESCE(x.status, ''), x.delivered_at, r.created_at`

func scanReview(row interface{ Scan(...any) error }) (AssetReview, string, error) {
	var r AssetReview
	var files, created string
	var seq int
	var delivered sql.NullString
	err := row.Scan(&r.ID, &r.AssetID, &r.Version, &r.Verdict, &r.Comment, &files, &r.Author.Kind, &r.Author.Ref, &r.Author.Name, &r.SentTo, &r.SentAs, &r.SentName,
		&seq, &r.Delivery, &delivered, &created)
	if err != nil {
		return r, "", err
	}
	r.Files = parseFiles(files)
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

// AssetReviewByDecision 找送达记录对应的审核（恢复 / 压缩后补发前判断还有没有用）。
func (s *Store) AssetReviewByDecision(ctx context.Context, decisionID string) (AssetReview, error) {
	r, _, err := scanReview(s.db.QueryRowContext(ctx, `SELECT `+reviewCols+` FROM asset_reviews r LEFT JOIN decisions x ON x.id = r.decision_id WHERE r.decision_id = ?`, decisionID))
	if errors.Is(err, sql.ErrNoRows) {
		return r, ErrNotFound
	}
	return r, err
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
