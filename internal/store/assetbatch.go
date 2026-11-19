package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"

	"github.com/NekroAI/kitestring/internal/protocol"
)

// 资源库批量审核（R-393）：
//   - 申请标为已在用（--baseline）：历史素材推送后先挂在一条申请上，同一次推送提一条批准类决策给用户（D-2074 主助理裁定：
//     不按身份或 --yes 放行，AI 会自己加 --yes）。用户批准时整批记为通过，审核记录写明是用户批准的历史导入，不给会话发意见；
//     拒绝时转为普通待审。之后有新版本照常回到待审（推送新版本时清掉挂着的申请）。
//   - 批量通过 / 返工：按素材列表或筛选条件一次审一批，一个事务写完；每条审核记录记下属于哪次批量操作和审核前的状态，可撤销最近一次。
//   - 可以批量审素材的会话：用户在网页上授权，默认没有。

// AssetBrief 是批量操作用的素材摘要（不读版本和文件）。
type AssetBrief struct {
	ID        string         `json:"id"`
	Code      string         `json:"code"`
	ProjectID string         `json:"project_id"`
	Batch     string         `json:"batch"`
	Key       string         `json:"key"`
	UID       string         `json:"uid,omitempty"`
	Kind      string         `json:"kind"`
	Category  string         `json:"category"`
	Status    string         `json:"status"`
	Version   int            `json:"version"`
	Submitter AssetSubmitter `json:"submitter"`
}

// Name 是素材在文字里的称呼：素材 id，旧条目是「批次/key」。
func (b AssetBrief) Name() string {
	if b.UID != "" {
		return b.UID
	}
	return b.Batch + "/" + b.Key
}

const briefCols = `a.id, a.seq, a.project_id, a.batch, a.key, a.uid, a.kind, a.category, a.status, a.version, a.submitter_kind, a.submitter_ref, a.submitter_name`

func scanBrief(row interface{ Scan(...any) error }) (AssetBrief, error) {
	var b AssetBrief
	var seq int
	err := row.Scan(&b.ID, &seq, &b.ProjectID, &b.Batch, &b.Key, &b.UID, &b.Kind, &b.Category, &b.Status, &b.Version, &b.Submitter.Kind, &b.Submitter.Ref, &b.Submitter.Name)
	b.Code = AssetCode(seq)
	return b, err
}

// MaxAssetBatch 是一次批量审核最多的件数。
const MaxAssetBatch = 1000

// AssetTargets 找出批量操作的素材：refs（编号 A-x、ID 或素材 id）和筛选条件二选一（都给时取交集）。按编号排序，去重。
func (s *Store) AssetTargets(ctx context.Context, projectID string, refs []string, f AssetFilter) ([]AssetBrief, error) {
	var out []AssetBrief
	if len(refs) > 0 {
		seen := map[string]bool{}
		for _, ref := range refs {
			if ref = strings.TrimSpace(ref); ref == "" {
				continue
			}
			a, err := s.findAssetRow(ctx, projectID, ref)
			if errors.Is(err, sql.ErrNoRows) {
				return nil, fmt.Errorf("%w：素材 %s", ErrNotFound, ref)
			}
			if err != nil {
				return nil, fmt.Errorf("%s：%w", ref, err)
			}
			if projectID != "" && a.ProjectID != projectID {
				return nil, fmt.Errorf("素材 %s 不在这个项目里", a.Code)
			}
			if seen[a.ID] || !briefMatch(a, f) {
				continue
			}
			seen[a.ID] = true
			out = append(out, AssetBrief{ID: a.ID, Code: a.Code, ProjectID: a.ProjectID, Batch: a.Batch, Key: a.Key, UID: a.UID, Kind: a.Kind, Category: a.Category,
				Status: a.Status, Version: a.Version, Submitter: a.Submitter})
		}
		slices.SortFunc(out, func(x, y AssetBrief) int { return assetSeq(x.Code) - assetSeq(y.Code) })
		return out, nil
	}
	if projectID != "" {
		f.ProjectID = projectID
	}
	where, args := assetWhere(f)
	if where == "" {
		return nil, errors.New("要指定素材或筛选条件")
	}
	rows, err := s.db.QueryContext(ctx, `SELECT `+briefCols+` FROM assets a`+where+` ORDER BY a.seq LIMIT ?`, append(args, MaxAssetBatch+1)...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		b, err := scanBrief(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, b)
	}
	return out, rows.Err()
}

// briefMatch：按编号点名的素材再按筛选条件过一遍（筛选条件为空时都算）。批次、id 前缀这类只在 SQL 里查，这里只看简单字段。
func briefMatch(a Asset, f AssetFilter) bool {
	return (f.Status == "" || a.Status == f.Status) && (f.Category == "" || a.Category == f.Category) && (f.Kind == "" || a.Kind == f.Kind) &&
		(f.Batch == "" || slices.Contains(a.Batches, f.Batch))
}

// AssetSkip 是批量操作里没有动的一件和原因。
type AssetSkip struct {
	Code   string `json:"code"`
	Name   string `json:"name"`
	Status string `json:"status"`
	Why    string `json:"why"`
}

var assetStatusZh = map[string]string{AssetWIP: "制作中", AssetPending: "待审", AssetApproved: "通过", AssetRework: "返工"}

// PlanAssetBatch 预检批量审核：哪些能改成 verdict、哪些跳过（已经是这个状态、或状态不能这样改）。
func PlanAssetBatch(xs []AssetBrief, verdict string) (ok []AssetBrief, skip []AssetSkip) {
	for _, x := range xs {
		switch {
		case x.Status == verdict:
			skip = append(skip, AssetSkip{Code: x.Code, Name: x.Name(), Status: x.Status, Why: "已经是" + assetStatusZh[verdict]})
		case !AssetCanMove(x.Status, verdict):
			skip = append(skip, AssetSkip{Code: x.Code, Name: x.Name(), Status: x.Status, Why: assetStatusZh[x.Status] + "的素材不能直接改为" + assetStatusZh[verdict]})
		default:
			ok = append(ok, x)
		}
	}
	return ok, skip
}

// NewAssetBatch 是一次批量审核的参数。
type NewAssetBatch struct {
	Assets  []AssetBrief // 预检时看到的素材（版本、状态）；提交时仍要一致，变了的跳过
	Verdict string       // approved / rework
	Comment string
	Files   []FileRef
	Author  AssetAuthor
}

// AssetBatchDone 是批量审核的结果：这次改了哪些（审核记录与素材一一对应）、哪些跳过。
type AssetBatchDone struct {
	BatchID string        `json:"batch_id"`
	Reviews []AssetReview `json:"-"`
	Assets  []AssetBrief  `json:"-"`
	Skipped []AssetSkip   `json:"skipped"`
}

// ReviewAssetBatch 在一个事务里批量通过 / 返工：返工必须写意见或附图；预检后状态或版本变了的跳过。
func (s *Store) ReviewAssetBatch(ctx context.Context, n NewAssetBatch) (AssetBatchDone, error) {
	var out AssetBatchDone
	if n.Verdict != AssetApproved && n.Verdict != AssetRework {
		return out, fmt.Errorf("审核结论只能是 approved（通过）或 rework（返工）：%s", n.Verdict)
	}
	n.Comment = strings.TrimSpace(n.Comment)
	if n.Verdict == AssetRework && n.Comment == "" && len(n.Files) == 0 {
		return out, errors.New("返工需写明修改意见")
	}
	if len(n.Assets) > MaxAssetBatch {
		return out, fmt.Errorf("一次最多审 %d 件，这次 %d 件：缩小筛选范围后分批审", MaxAssetBatch, len(n.Assets))
	}
	files := n.Files
	if files == nil {
		files = []FileRef{}
	}
	at := now()
	out.BatchID = protocol.NewID()
	err := s.tx(ctx, func(t *sql.Tx) error {
		for _, x := range n.Assets {
			var cur string
			var version int
			if err := t.QueryRowContext(ctx, `SELECT status, version FROM assets WHERE id = ?`, x.ID).Scan(&cur, &version); err != nil {
				if errors.Is(err, sql.ErrNoRows) {
					out.Skipped = append(out.Skipped, AssetSkip{Code: x.Code, Name: x.Name(), Why: "素材已不存在"})
					continue
				}
				return err
			}
			if cur != x.Status || version != x.Version {
				out.Skipped = append(out.Skipped, AssetSkip{Code: x.Code, Name: x.Name(), Status: cur, Why: "预检后状态或版本变了"})
				continue
			}
			if cur == n.Verdict || !AssetCanMove(cur, n.Verdict) {
				out.Skipped = append(out.Skipped, AssetSkip{Code: x.Code, Name: x.Name(), Status: cur, Why: assetStatusZh[cur] + "的素材不能直接改为" + assetStatusZh[n.Verdict]})
				continue
			}
			if _, err := t.ExecContext(ctx, `UPDATE assets SET status = ?, status_at = ?, updated_at = ? WHERE id = ?`, n.Verdict, at, at, x.ID); err != nil {
				return err
			}
			r := AssetReview{ID: protocol.NewID(), AssetID: x.ID, Version: version, Verdict: n.Verdict, Comment: n.Comment, Files: files, Author: n.Author, CreatedAt: parseTS(at)}
			if _, err := t.ExecContext(ctx, `INSERT INTO asset_reviews(id, asset_id, version, verdict, comment, files, author_kind, author_ref, author_name, batch_id, prev_status, created_at) VALUES(?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
				r.ID, x.ID, version, n.Verdict, n.Comment, filesJSON(files), n.Author.Kind, n.Author.Ref, n.Author.Name, out.BatchID, cur, at); err != nil {
				return err
			}
			x.Status = n.Verdict
			out.Reviews = append(out.Reviews, r)
			out.Assets = append(out.Assets, x)
		}
		if len(out.Reviews) == 0 {
			return nil
		}
		_, err := t.ExecContext(ctx, `INSERT INTO asset_review_batches(id, verdict, comment, count, author_kind, author_ref, author_name, created_at) VALUES(?, ?, ?, ?, ?, ?, ?, ?)`,
			out.BatchID, n.Verdict, n.Comment, len(out.Reviews), n.Author.Kind, n.Author.Ref, n.Author.Name, at)
		return err
	})
	if err != nil {
		return AssetBatchDone{}, err
	}
	if len(out.Reviews) == 0 {
		out.BatchID = ""
	}
	return out, nil
}

// LinkAssetReviews 记下一批审核意见合并送给了谁（同一条送达记录）。
func (s *Store) LinkAssetReviews(ctx context.Context, ids []string, decisionID, sentTo, sentAs, sentName string) error {
	return s.tx(ctx, func(t *sql.Tx) error {
		for _, id := range ids {
			if _, err := t.ExecContext(ctx, `UPDATE asset_reviews SET decision_id = ?, sent_to = ?, sent_as = ?, sent_name = ? WHERE id = ?`, decisionID, sentTo, sentAs, sentName, id); err != nil {
				return err
			}
		}
		return nil
	})
}

// AssetBatch 是一次批量审核。
type AssetBatch struct {
	ID        string      `json:"id"`
	Verdict   string      `json:"verdict" enum:"approved,rework"`
	Comment   string      `json:"comment"`
	Count     int         `json:"count" doc:"这次改了几件"`
	Author    AssetAuthor `json:"author"`
	CreatedAt string      `json:"created_at"`
	Undone    bool        `json:"undone"`
}

// LatestAssetBatch 是 author 最近一次没撤销的批量审核；没有时 ErrNotFound。
func (s *Store) LatestAssetBatch(ctx context.Context, author AssetAuthor) (AssetBatch, error) {
	var b AssetBatch
	var undone string
	err := s.db.QueryRowContext(ctx, `SELECT id, verdict, comment, count, author_kind, author_ref, author_name, created_at, undone_at FROM asset_review_batches
		WHERE author_kind = ? AND author_ref = ? AND undone_at = '' ORDER BY created_at DESC, rowid DESC LIMIT 1`, author.Kind, author.Ref).
		Scan(&b.ID, &b.Verdict, &b.Comment, &b.Count, &b.Author.Kind, &b.Author.Ref, &b.Author.Name, &b.CreatedAt, &undone)
	if errors.Is(err, sql.ErrNoRows) {
		return b, ErrNotFound
	}
	b.Undone = undone != ""
	return b, err
}

// AssetBatchUndone 是撤销的结果：还原了哪些（素材和它们原来的意见送达记录）、哪些没还原（之后又被改过）。
type AssetBatchUndone struct {
	Batch     AssetBatch   `json:"batch"`
	Restored  []AssetBrief `json:"restored" doc:"还原了的素材（status 是还原后的状态）"`
	Kept      []AssetSkip  `json:"kept" doc:"之后又有新版本或被改过、没有还原的素材"`
	Decisions []string     `json:"-"` // 被撤销的审核意见用过的送达记录
}

// UndoAssetBatch 撤销一次批量审核：素材仍是这次改成的状态、没有新版本、之后也没有别的审核时还原为审核前的状态，删掉这次的审核记录；
// 其余的保留并列出。批量操作本身记为已撤销。
func (s *Store) UndoAssetBatch(ctx context.Context, id string) (AssetBatchUndone, error) {
	var out AssetBatchUndone
	var undone string
	b := &out.Batch
	err := s.db.QueryRowContext(ctx, `SELECT id, verdict, comment, count, author_kind, author_ref, author_name, created_at, undone_at FROM asset_review_batches WHERE id = ?`, id).
		Scan(&b.ID, &b.Verdict, &b.Comment, &b.Count, &b.Author.Kind, &b.Author.Ref, &b.Author.Name, &b.CreatedAt, &undone)
	if errors.Is(err, sql.ErrNoRows) {
		return out, ErrNotFound
	}
	if err != nil {
		return out, err
	}
	if undone != "" {
		return out, errors.New("这次批量审核已经撤销过了")
	}
	at := now()
	decs := map[string]bool{}
	err = s.tx(ctx, func(t *sql.Tx) error {
		rows, err := t.QueryContext(ctx, `SELECT r.id, r.version, r.prev_status, r.decision_id, r.created_at, `+briefCols+` FROM asset_reviews r JOIN assets a ON a.id = r.asset_id WHERE r.batch_id = ? ORDER BY a.seq`, id)
		if err != nil {
			return err
		}
		type row struct {
			rid, prev, dec, created string
			version                 int
			a                       AssetBrief
		}
		var got []row
		for rows.Next() {
			var x row
			var seq int
			a := &x.a
			if err := rows.Scan(&x.rid, &x.version, &x.prev, &x.dec, &x.created, &a.ID, &seq, &a.ProjectID, &a.Batch, &a.Key, &a.UID, &a.Kind, &a.Category, &a.Status, &a.Version,
				&a.Submitter.Kind, &a.Submitter.Ref, &a.Submitter.Name); err != nil {
				rows.Close()
				return err
			}
			a.Code = AssetCode(seq)
			got = append(got, x)
		}
		rows.Close()
		if err := rows.Err(); err != nil {
			return err
		}
		for _, x := range got {
			var later int
			if err := t.QueryRowContext(ctx, `SELECT COUNT(*) FROM asset_reviews WHERE asset_id = ? AND id <> ? AND created_at >= ? AND batch_id <> ?`, x.a.ID, x.rid, x.created, id).Scan(&later); err != nil {
				return err
			}
			switch {
			case x.a.Version != x.version:
				out.Kept = append(out.Kept, AssetSkip{Code: x.a.Code, Name: x.a.Name(), Status: x.a.Status, Why: "之后有了新版本"})
				continue
			case x.a.Status != b.Verdict || later > 0:
				out.Kept = append(out.Kept, AssetSkip{Code: x.a.Code, Name: x.a.Name(), Status: x.a.Status, Why: "之后又被审核过"})
				continue
			}
			prev := x.prev
			if prev == "" {
				prev = AssetPending
			}
			if _, err := t.ExecContext(ctx, `UPDATE assets SET status = ?, status_at = ?, updated_at = ? WHERE id = ?`, prev, at, at, x.a.ID); err != nil {
				return err
			}
			if _, err := t.ExecContext(ctx, `DELETE FROM asset_reviews WHERE id = ?`, x.rid); err != nil {
				return err
			}
			if x.dec != "" && !decs[x.dec] {
				decs[x.dec] = true
				out.Decisions = append(out.Decisions, x.dec)
			}
			x.a.Status = prev
			out.Restored = append(out.Restored, x.a)
		}
		_, err = t.ExecContext(ctx, `UPDATE asset_review_batches SET undone_at = ? WHERE id = ?`, at, id)
		return err
	})
	if err != nil {
		return AssetBatchUndone{}, err
	}
	b.Undone = true
	return out, nil
}

// AssetReviewsByDecision 找一条送达记录合并送出的全部审核（批量审核一个会话只收一条，R-393）。
func (s *Store) AssetReviewsByDecision(ctx context.Context, decisionID string) ([]AssetReview, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT `+reviewCols+` FROM asset_reviews r LEFT JOIN decisions x ON x.id = r.decision_id WHERE r.decision_id = ?`, decisionID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []AssetReview
	for rows.Next() {
		r, _, err := scanReview(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// ---------- 申请标为已在用（--baseline） ----------

// AssetBaseline 是一次「申请标为已在用」。
type AssetBaseline struct {
	ID          string `json:"id"`
	PushID      string `json:"push_id"`
	ProjectID   string `json:"project_id"`
	SessionKey  string `json:"session_key"`
	SessionName string `json:"session_name"`
	DecisionID  string `json:"decision_id"`
	Status      string `json:"status" enum:"pending,approved,rejected"`
}

// BaselineItem 是申请里的一件素材（提决策时拼件数、类别分布和代表缩略图用）。
type BaselineItem struct {
	AssetBrief
	Thumb string `json:"thumb,omitempty" doc:"代表图的附件 ID：零件取第一张预览图，独立的图取它本身"`
}

// 申请的状态。
const (
	BaselinePending  = "pending"
	BaselineApproved = "approved"
	BaselineRejected = "rejected"
)

// StartAssetBaseline 为一次 --baseline 推送登记申请：codes 里仍在待审 / 制作中的素材挂上申请（已通过、返工中的不动）。
// 同一 pushID 重放时返回已有的申请（replay = true），不重复登记。没有可申请的素材时 b.ID 为空。
func (s *Store) StartAssetBaseline(ctx context.Context, pushID, projectID string, sub AssetSubmitter, codes []string) (b AssetBaseline, items []BaselineItem, replay bool, err error) {
	if pushID == "" {
		pushID = protocol.NewID()
	}
	err = s.tx(ctx, func(t *sql.Tx) error {
		e := t.QueryRowContext(ctx, `SELECT id, push_id, project_id, session_key, session_name, decision_id, status FROM asset_baselines WHERE push_id = ?`, pushID).
			Scan(&b.ID, &b.PushID, &b.ProjectID, &b.SessionKey, &b.SessionName, &b.DecisionID, &b.Status)
		if e == nil {
			replay = true
			return nil
		}
		if !errors.Is(e, sql.ErrNoRows) {
			return e
		}
		for _, code := range codes {
			seq := assetSeq(code)
			if seq == 0 {
				continue
			}
			var it BaselineItem
			var fileID, previews string
			var n int
			row := t.QueryRowContext(ctx, `SELECT `+briefCols+`, v.file_id, v.previews FROM assets a JOIN asset_versions v ON v.id = a.latest_id WHERE a.seq = ?`, seq)
			x := &it.AssetBrief
			if err := row.Scan(&x.ID, &n, &x.ProjectID, &x.Batch, &x.Key, &x.UID, &x.Kind, &x.Category, &x.Status, &x.Version, &x.Submitter.Kind, &x.Submitter.Ref, &x.Submitter.Name, &fileID, &previews); err != nil {
				if errors.Is(err, sql.ErrNoRows) {
					continue
				}
				return err
			}
			x.Code = AssetCode(n)
			if x.Status != AssetPending && x.Status != AssetWIP {
				continue
			}
			var ps []AssetPreview
			_ = json.Unmarshal([]byte(previews), &ps)
			switch {
			case x.Kind == protocol.AssetImage:
				it.Thumb = fileID
			case len(ps) > 0:
				it.Thumb = ps[0].ID
			}
			items = append(items, it)
		}
		if len(items) == 0 {
			return nil
		}
		b = AssetBaseline{ID: protocol.NewID(), PushID: pushID, ProjectID: projectID, SessionKey: sub.Ref, SessionName: sub.Name, Status: BaselinePending}
		if _, err := t.ExecContext(ctx, `INSERT INTO asset_baselines(id, push_id, project_id, session_key, session_name, created_at) VALUES(?, ?, ?, ?, ?, ?)`,
			b.ID, pushID, projectID, sub.Ref, sub.Name, now()); err != nil {
			return err
		}
		for _, it := range items {
			if _, err := t.ExecContext(ctx, `INSERT INTO asset_baseline_items(baseline_id, asset_id, version) VALUES(?, ?, ?)`, b.ID, it.ID, it.Version); err != nil {
				return err
			}
			if _, err := t.ExecContext(ctx, `UPDATE assets SET baseline_id = ? WHERE id = ?`, b.ID, it.ID); err != nil {
				return err
			}
		}
		return nil
	})
	return b, items, replay, err
}

// LinkAssetBaseline 记下申请对应的批准类决策。
func (s *Store) LinkAssetBaseline(ctx context.Context, id, decisionID string) error {
	_, err := s.db.ExecContext(ctx, `UPDATE asset_baselines SET decision_id = ? WHERE id = ?`, decisionID, id)
	return err
}

// AssetBaselineByDecision 找决策对应的申请；不是申请的决策返回 ErrNotFound。
func (s *Store) AssetBaselineByDecision(ctx context.Context, decisionID string) (AssetBaseline, error) {
	var b AssetBaseline
	err := s.db.QueryRowContext(ctx, `SELECT id, push_id, project_id, session_key, session_name, decision_id, status FROM asset_baselines WHERE decision_id = ? AND decision_id <> ''`, decisionID).
		Scan(&b.ID, &b.PushID, &b.ProjectID, &b.SessionKey, &b.SessionName, &b.DecisionID, &b.Status)
	if errors.Is(err, sql.ErrNoRows) {
		return b, ErrNotFound
	}
	return b, err
}

// BaselineNote 是用户批准的历史导入写进审核记录的意见。
func BaselineNote(decisionCode, sessionName string) string {
	t := "导入时标为已在用：用户批准的历史导入（" + decisionCode + "）"
	if sessionName != "" {
		t += "，推送会话「" + sessionName + "」"
	}
	return t
}

// DecideAssetBaseline 按用户的答复处理申请：批准时仍挂着这条申请、仍是申请时那一版、仍在待审 / 制作中的素材记为通过，
// 审核记录作者是用户、意见写明是批准的历史导入，不送意见；拒绝时只摘掉申请，素材留在待审（制作中的不动）。
// 已处理过的申请不再处理（返回 0）。返回通过的件数。
func (s *Store) DecideAssetBaseline(ctx context.Context, id string, approve bool, decisionCode string) (int, error) {
	n := 0
	err := s.tx(ctx, func(t *sql.Tx) error {
		var status, name string
		if err := t.QueryRowContext(ctx, `SELECT status, session_name FROM asset_baselines WHERE id = ?`, id).Scan(&status, &name); err != nil {
			return err
		}
		if status != BaselinePending {
			return nil
		}
		at := now()
		result := BaselineRejected
		if approve {
			result = BaselineApproved
			rows, err := t.QueryContext(ctx, `SELECT a.id, a.version, a.status FROM asset_baseline_items i JOIN assets a ON a.id = i.asset_id
				WHERE i.baseline_id = ? AND a.baseline_id = ? AND a.version = i.version AND a.status IN (?, ?)`, id, id, AssetPending, AssetWIP)
			if err != nil {
				return err
			}
			type row struct {
				id, status string
				version    int
			}
			var got []row
			for rows.Next() {
				var x row
				if err := rows.Scan(&x.id, &x.version, &x.status); err != nil {
					rows.Close()
					return err
				}
				got = append(got, x)
			}
			rows.Close()
			if err := rows.Err(); err != nil {
				return err
			}
			note := BaselineNote(decisionCode, name)
			for _, x := range got {
				if _, err := t.ExecContext(ctx, `UPDATE assets SET status = ?, status_at = ?, updated_at = ? WHERE id = ?`, AssetApproved, at, at, x.id); err != nil {
					return err
				}
				if _, err := t.ExecContext(ctx, `INSERT INTO asset_reviews(id, asset_id, version, verdict, comment, files, author_kind, prev_status, created_at) VALUES(?, ?, ?, ?, ?, '[]', ?, ?, ?)`,
					protocol.NewID(), x.id, x.version, AssetApproved, note, SubmitterUser, x.status, at); err != nil {
					return err
				}
				n++
			}
		}
		if _, err := t.ExecContext(ctx, `UPDATE assets SET baseline_id = '' WHERE baseline_id = ?`, id); err != nil {
			return err
		}
		_, err := t.ExecContext(ctx, `UPDATE asset_baselines SET status = ?, decided_at = ? WHERE id = ?`, result, at, id)
		return err
	})
	return n, err
}

// ---------- 可以批量审素材的会话 ----------

// SetAssetReviewer 授权 / 收回会话批量审素材（用户在网页上操作）。
func (s *Store) SetAssetReviewer(ctx context.Context, key string, allow bool) error {
	if !allow {
		_, err := s.db.ExecContext(ctx, `DELETE FROM asset_reviewers WHERE session_key = ?`, key)
		return err
	}
	if _, err := s.GetSession(ctx, key); err != nil {
		return err
	}
	_, err := s.db.ExecContext(ctx, `INSERT INTO asset_reviewers(session_key, granted_at) VALUES(?, ?) ON CONFLICT(session_key) DO NOTHING`, key, now())
	return err
}

// AssetReviewerAllowed：会话是否被授权批量审素材。
func (s *Store) AssetReviewerAllowed(ctx context.Context, key string) (bool, error) {
	var n int
	err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM asset_reviewers WHERE session_key = ?`, key).Scan(&n)
	return n > 0, err
}

// ListAssetReviewers 列出授权了的会话键。
func (s *Store) ListAssetReviewers(ctx context.Context) ([]string, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT session_key FROM asset_reviewers ORDER BY granted_at`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []string{}
	for rows.Next() {
		var k string
		if err := rows.Scan(&k); err != nil {
			return nil, err
		}
		out = append(out, k)
	}
	return out, rows.Err()
}
