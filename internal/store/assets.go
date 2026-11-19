package store

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/NekroAI/kitestring/internal/protocol"
)

// 资源库（R-351）：素材与版本。一条素材 = 项目 + 批次 + key；内容有变的重推记为新版本，旧版本与原文件全部保留。

// 素材状态。
const (
	AssetWIP      = "wip"      // 制作中
	AssetPending  = "pending"  // 待审
	AssetApproved = "approved" // 通过
	AssetRework   = "rework"   // 返工
)

// AssetStatuses 是素材状态的全集，按展示顺序。
var AssetStatuses = []string{AssetWIP, AssetPending, AssetApproved, AssetRework}

// assetMoves 是审核（第 4 步）能做的状态变化；推送不走这里：有新版本时一律回到待审（或推送时指定的制作中）。
var assetMoves = map[string][]string{
	AssetWIP:      {AssetPending},
	AssetPending:  {AssetApproved, AssetRework, AssetWIP},
	AssetApproved: {AssetPending, AssetRework}, // 撤回、改判
	AssetRework:   {AssetPending, AssetApproved},
}

// AssetCanMove 判断审核能否把素材从 from 改到 to。
func AssetCanMove(from, to string) bool { return slices.Contains(assetMoves[from], to) }

// AssetCode 是素材编号 A-<seq>。
func AssetCode(seq int) string { return "A-" + strconv.Itoa(seq) }

// Asset 是一条素材（带最新版本）。
type Asset struct {
	ID            string         `json:"id"`
	Code          string         `json:"code" doc:"素材编号 A-x"`
	ProjectID     string         `json:"project_id"`
	Batch         string         `json:"batch"`
	Key           string         `json:"key" doc:"manifest 的 id；独立的图是文件名去扩展名"`
	Kind          string         `json:"kind" enum:"part,image" doc:"part GLB 零件 / image 独立的图"`
	Category      string         `json:"category"`
	Group         string         `json:"group,omitempty" doc:"同一用途的候选分组（概念图 A / B 版），并排比较用"`
	Status        string         `json:"status" enum:"wip,pending,approved,rework"`
	Version       int            `json:"version" doc:"最新版本号"`
	Submitter     AssetSubmitter `json:"submitter" doc:"最近一次提交素材的人（返工意见送它）"`
	RequirementID string         `json:"requirement_id,omitempty"`
	StatusAt      *time.Time     `json:"status_at,omitempty" doc:"状态最近一次变化的时间"`
	CreatedAt     time.Time      `json:"created_at"`
	UpdatedAt     time.Time      `json:"updated_at"`
	Latest        *AssetVersion  `json:"latest,omitempty" doc:"最新版本"`
}

// AssetVersion 是素材的一个版本。
type AssetVersion struct {
	ID        string          `json:"id"`
	Version   int             `json:"version"`
	Batch     string          `json:"batch"`
	Branch    string          `json:"branch"`
	Commit    string          `json:"commit"`
	File      *FileRef        `json:"file,omitempty" doc:"主文件：零件的 GLB，独立的图本身"`
	Previews  []FileRef       `json:"previews" doc:"零件的预览图"`
	Triangles int             `json:"triangles,omitempty"`
	Budget    int             `json:"budget,omitempty" doc:"面数预算上限"`
	Size      []float64       `json:"size,omitempty" doc:"尺寸（米，x / y / z）"`
	Pixels    []int           `json:"pixels,omitempty" doc:"像素尺寸（宽 / 高）"`
	Meta      json.RawMessage `json:"meta,omitempty" doc:"manifest 里这一项的原文"`
	Submitter AssetSubmitter  `json:"submitter" doc:"这一版的提交者"`
	CreatedAt time.Time       `json:"created_at"`
}

// AssetDetail 是素材与它的全部版本（新的在前）。
type AssetDetail struct {
	Asset
	Versions []AssetVersion `json:"versions"`
}

// 提交者的三种（R-351；guest 给 R-365 资源库分享预留，访客在分享页上传素材）。
const (
	SubmitterSession = "session" // 会话推送：Ref 是会话键
	SubmitterUser    = "user"    // 你自己在网页上传
	SubmitterGuest   = "guest"   // 分享页上的访客：Ref 是访客链接 ID，Name 是访客名字
)

// AssetSubmitter 是素材某一版的提交者。
type AssetSubmitter struct {
	Kind string `json:"kind" enum:"session,user,guest," doc:"session 会话 / user 你 / guest 分享页访客；旧数据为空"`
	Ref  string `json:"ref,omitempty" doc:"session：会话键；guest：访客链接 ID；user：空"`
	Name string `json:"name,omitempty" doc:"显示名：会话标题、访客名字"`
}

// NewAssetPush 是登记一次推送的参数（文件已在 files 表里）。
type NewAssetPush struct {
	PushID        string
	ProjectID     string
	ProjectName   string
	Submitter     AssetSubmitter
	RequirementID string
	Batch         string
	Branch        string
	Commit        string
	Status        string // wip / pending（默认）
	Items         []protocol.AssetItem
	Reused        int
}

// FileBySHA 找内容相同的已有附件（按 sha256 去重）；mime 不为空时还要类型相同。
func (s *Store) FileBySHA(ctx context.Context, sha, mime string) (FileRef, error) {
	var f FileRef
	q := `SELECT id, name, mime, size FROM files WHERE sha256 = ?`
	args := []any{sha}
	if mime != "" {
		q += ` AND mime = ?`
		args = append(args, mime)
	}
	err := s.db.QueryRowContext(ctx, q+` ORDER BY created_at LIMIT 1`, args...).Scan(&f.ID, &f.Name, &f.Mime, &f.Size)
	if errors.Is(err, sql.ErrNoRows) {
		return f, ErrNotFound
	}
	return f, err
}

// fileSHA 读附件的 sha256。
func fileSHA(ctx context.Context, t *sql.Tx, id string) (string, error) {
	var sha string
	err := t.QueryRowContext(ctx, `SELECT sha256 FROM files WHERE id = ?`, id).Scan(&sha)
	if errors.Is(err, sql.ErrNoRows) {
		return "", fmt.Errorf("附件不存在：%s", id)
	}
	return sha, err
}

// PushAssets 登记一批素材：新素材建档；已有素材内容有变记为新版本、状态回到待审（或制作中）；内容相同的不动。
// 同一 PushID 重放时原样返回第一次的结果（daemon 补传可能重复提交）。
func (s *Store) PushAssets(ctx context.Context, n NewAssetPush) (protocol.AssetPushResult, error) {
	res := protocol.AssetPushResult{PushID: n.PushID, Project: n.ProjectName, Batch: n.Batch, Items: []protocol.AssetPushItemResult{}, Reused: n.Reused}
	status := n.Status
	if status == "" {
		status = AssetPending
	}
	if status != AssetPending && status != AssetWIP {
		return res, fmt.Errorf("推送时的状态只能是 pending（待审）或 wip（制作中）：%s", status)
	}
	err := s.tx(ctx, func(t *sql.Tx) error {
		if n.PushID != "" {
			var raw string
			err := t.QueryRowContext(ctx, `SELECT result FROM asset_pushes WHERE id = ?`, n.PushID).Scan(&raw)
			if err == nil {
				return json.Unmarshal([]byte(raw), &res)
			}
			if !errors.Is(err, sql.ErrNoRows) {
				return err
			}
		}
		at := now()
		for _, it := range n.Items {
			r, err := pushAsset(ctx, t, n, it, status, at)
			if err != nil {
				return fmt.Errorf("%s：%w", it.Key, err)
			}
			res.Items = append(res.Items, r)
		}
		if n.PushID != "" {
			b, _ := json.Marshal(res)
			if _, err := t.ExecContext(ctx, `INSERT INTO asset_pushes(id, result, created_at) VALUES(?, ?, ?)`, n.PushID, string(b), at); err != nil {
				return err
			}
		}
		return nil
	})
	return res, err
}

func pushAsset(ctx context.Context, t *sql.Tx, n NewAssetPush, it protocol.AssetItem, status, at string) (protocol.AssetPushItemResult, error) {
	r := protocol.AssetPushItemResult{Key: it.Key}
	// 指纹：类型 + 主文件 + 预览图（排序后）的 sha256
	fileID := ""
	parts := []string{it.Kind}
	if it.File != nil {
		fileID = it.File.ID
		sha, err := fileSHA(ctx, t, fileID)
		if err != nil {
			return r, err
		}
		parts = append(parts, "f:"+sha)
	}
	previews := []FileRef{}
	var pshas []string
	for _, p := range it.Previews {
		sha, err := fileSHA(ctx, t, p.ID)
		if err != nil {
			return r, err
		}
		pshas = append(pshas, sha)
		var f FileRef
		if err := t.QueryRowContext(ctx, `SELECT id, name, mime, size FROM files WHERE id = ?`, p.ID).Scan(&f.ID, &f.Name, &f.Mime, &f.Size); err != nil {
			return r, err
		}
		previews = append(previews, f)
	}
	slices.Sort(pshas)
	for _, p := range pshas {
		parts = append(parts, "p:"+p)
	}
	sum := sha256.Sum256([]byte(strings.Join(parts, "\n")))
	digest := hex.EncodeToString(sum[:])

	var id, latest string
	var seq, version int
	err := t.QueryRowContext(ctx, `SELECT id, seq, version, latest_id FROM assets WHERE project_id = ? AND batch = ? AND key = ?`, n.ProjectID, n.Batch, it.Key).Scan(&id, &seq, &version, &latest)
	switch {
	case errors.Is(err, sql.ErrNoRows):
		id = protocol.NewID()
		if err := t.QueryRowContext(ctx, `SELECT COALESCE(MAX(seq), 0) + 1 FROM assets`).Scan(&seq); err != nil {
			return r, err
		}
		version = 0
		if _, err := t.ExecContext(ctx, `INSERT INTO assets(id, seq, project_id, batch, key, kind, category, grp, status, version, submitter_kind, submitter_ref, submitter_name, requirement_id, status_at, created_at, updated_at)
			VALUES(?, ?, ?, ?, ?, ?, ?, ?, ?, 0, ?, ?, ?, ?, ?, ?, ?)`, id, seq, n.ProjectID, n.Batch, it.Key, it.Kind, it.Category, it.Group, status, n.Submitter.Kind, n.Submitter.Ref, n.Submitter.Name, n.RequirementID, at, at, at); err != nil {
			return r, err
		}
		r.Result = "created"
	case err != nil:
		return r, err
	default:
		var old string
		if err := t.QueryRowContext(ctx, `SELECT digest FROM asset_versions WHERE id = ?`, latest).Scan(&old); err != nil && !errors.Is(err, sql.ErrNoRows) {
			return r, err
		}
		if old == digest {
			r.Code, r.Version, r.Result = AssetCode(seq), version, "unchanged"
			return r, nil
		}
		r.Result = "updated"
	}
	version++
	vid := protocol.NewID()
	if _, err := t.ExecContext(ctx, `INSERT INTO asset_versions(id, asset_id, version, push_id, batch, branch, commit_sha, file_id, previews, triangles, budget, size, pixels, meta, digest, submitter_kind, submitter_ref, submitter_name, created_at)
		VALUES(?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		vid, id, version, n.PushID, n.Batch, n.Branch, n.Commit, fileID, filesJSON(previews), it.Triangles, it.Budget,
		jsonOrEmpty(it.Size), jsonOrEmpty(it.Pixels), string(it.Meta), digest, n.Submitter.Kind, n.Submitter.Ref, n.Submitter.Name, at); err != nil {
		return r, err
	}
	reqSet := ""
	args := []any{version, vid, it.Kind, it.Category, it.Group, status, n.Submitter.Kind, n.Submitter.Ref, n.Submitter.Name}
	if n.RequirementID != "" {
		reqSet = ", requirement_id = ?"
		args = append(args, n.RequirementID)
	}
	args = append(args, at, at, id)
	if _, err := t.ExecContext(ctx, `UPDATE assets SET version = ?, latest_id = ?, kind = ?, category = ?, grp = ?, status = ?, submitter_kind = ?, submitter_ref = ?, submitter_name = ?`+reqSet+`, status_at = ?, updated_at = ? WHERE id = ?`, args...); err != nil {
		return r, err
	}
	r.Code, r.Version = AssetCode(seq), version
	return r, nil
}

func jsonOrEmpty[T any](v []T) string {
	if len(v) == 0 {
		return ""
	}
	b, _ := json.Marshal(v)
	return string(b)
}

// AssetFilter 是素材列表的筛选条件，空字段不限。
type AssetFilter struct {
	ProjectID string
	Batch     string
	Category  string
	Status    string
	Kind      string
}

const assetCols = `a.id, a.seq, a.project_id, a.batch, a.key, a.kind, a.category, a.grp, a.status, a.version, a.submitter_kind, a.submitter_ref, a.submitter_name, a.requirement_id, a.status_at, a.created_at, a.updated_at`

const versionCols = `v.id, v.version, v.batch, v.branch, v.commit_sha, v.file_id, v.previews, v.triangles, v.budget, v.size, v.pixels, v.meta, v.submitter_kind, v.submitter_ref, v.submitter_name, v.created_at`

func scanAsset(row interface{ Scan(...any) error }, extra ...any) (Asset, error) {
	var a Asset
	var seq int
	var statusAt, created, updated string
	dst := append([]any{&a.ID, &seq, &a.ProjectID, &a.Batch, &a.Key, &a.Kind, &a.Category, &a.Group, &a.Status, &a.Version, &a.Submitter.Kind, &a.Submitter.Ref, &a.Submitter.Name, &a.RequirementID, &statusAt, &created, &updated}, extra...)
	if err := row.Scan(dst...); err != nil {
		return a, err
	}
	a.Code = AssetCode(seq)
	if statusAt != "" {
		t := parseTS(statusAt)
		a.StatusAt = &t
	}
	a.CreatedAt, a.UpdatedAt = parseTS(created), parseTS(updated)
	return a, nil
}

type versionRow struct {
	v                        AssetVersion
	fileID, previews         string
	size, pixels, meta, crtd string
}

func (r *versionRow) dst() []any {
	return []any{&r.v.ID, &r.v.Version, &r.v.Batch, &r.v.Branch, &r.v.Commit, &r.fileID, &r.previews, &r.v.Triangles, &r.v.Budget, &r.size, &r.pixels, &r.meta, &r.v.Submitter.Kind, &r.v.Submitter.Ref, &r.v.Submitter.Name, &r.crtd}
}

func (s *Store) finishVersion(ctx context.Context, r versionRow) AssetVersion {
	v := r.v
	v.Previews = parseFiles(r.previews)
	if r.fileID != "" {
		if f, err := s.GetFile(ctx, r.fileID); err == nil {
			v.File = &f
		}
	}
	if r.size != "" {
		_ = json.Unmarshal([]byte(r.size), &v.Size)
	}
	if r.pixels != "" {
		_ = json.Unmarshal([]byte(r.pixels), &v.Pixels)
	}
	if r.meta != "" && json.Valid([]byte(r.meta)) {
		v.Meta = json.RawMessage(r.meta)
	}
	v.CreatedAt = parseTS(r.crtd)
	return v
}

// ListAssets 列出素材（带最新版本），按批次、编号排序。
func (s *Store) ListAssets(ctx context.Context, f AssetFilter) ([]Asset, error) {
	var where []string
	var args []any
	for _, c := range []struct{ col, v string }{{"a.project_id", f.ProjectID}, {"a.batch", f.Batch}, {"a.category", f.Category}, {"a.status", f.Status}, {"a.kind", f.Kind}} {
		if c.v != "" {
			where = append(where, c.col+" = ?")
			args = append(args, c.v)
		}
	}
	q := `SELECT ` + assetCols + `, ` + versionCols + ` FROM assets a JOIN asset_versions v ON v.id = a.latest_id`
	if len(where) > 0 {
		q += ` WHERE ` + strings.Join(where, " AND ")
	}
	rows, err := s.db.QueryContext(ctx, q+` ORDER BY a.batch, a.seq`, args...)
	if err != nil {
		return nil, err
	}
	type pair struct {
		a Asset
		r versionRow
	}
	var got []pair
	for rows.Next() {
		var p pair
		a, err := scanAsset(rows, p.r.dst()...)
		if err != nil {
			rows.Close()
			return nil, err
		}
		p.a = a
		got = append(got, p)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}
	out := make([]Asset, 0, len(got))
	for _, p := range got {
		v := s.finishVersion(ctx, p.r)
		p.a.Latest = &v
		out = append(out, p.a)
	}
	return out, nil
}

// GetAsset 读一条素材和它的全部版本；ref 是 ID 或编号 A-x。
func (s *Store) GetAsset(ctx context.Context, ref string) (AssetDetail, error) {
	var d AssetDetail
	ref = strings.TrimSpace(ref)
	q, arg := `SELECT `+assetCols+` FROM assets a WHERE a.id = ?`, any(ref)
	if n, ok := strings.CutPrefix(strings.ToUpper(ref), "A-"); ok {
		seq, err := strconv.Atoi(n)
		if err != nil {
			return d, ErrNotFound
		}
		q, arg = `SELECT `+assetCols+` FROM assets a WHERE a.seq = ?`, seq
	}
	a, err := scanAsset(s.db.QueryRowContext(ctx, q, arg))
	if errors.Is(err, sql.ErrNoRows) {
		return d, ErrNotFound
	}
	if err != nil {
		return d, err
	}
	d.Asset = a
	rows, err := s.db.QueryContext(ctx, `SELECT `+versionCols+` FROM asset_versions v WHERE v.asset_id = ? ORDER BY v.version DESC`, a.ID)
	if err != nil {
		return d, err
	}
	var rs []versionRow
	for rows.Next() {
		var r versionRow
		if err := rows.Scan(r.dst()...); err != nil {
			rows.Close()
			return d, err
		}
		rs = append(rs, r)
	}
	rows.Close()
	d.Versions = make([]AssetVersion, 0, len(rs))
	for _, r := range rs {
		d.Versions = append(d.Versions, s.finishVersion(ctx, r))
	}
	if len(d.Versions) > 0 {
		d.Latest = &d.Versions[0]
	}
	return d, rows.Err()
}

// AssetFacets 是一个项目里素材的批次和类别（筛选项用），按名称排序。
type AssetFacets struct {
	Batches    []string       `json:"batches"`
	Categories []string       `json:"categories"`
	Counts     map[string]int `json:"counts" doc:"各状态的素材数"`
}

// ListAssetFacets 统计项目里素材的批次、类别和各状态数量；projectID 为空时统计全部。
func (s *Store) ListAssetFacets(ctx context.Context, projectID string) (AssetFacets, error) {
	f := AssetFacets{Batches: []string{}, Categories: []string{}, Counts: map[string]int{}}
	for _, st := range AssetStatuses {
		f.Counts[st] = 0
	}
	q, args := `SELECT batch, category, status FROM assets`, []any{}
	if projectID != "" {
		q += ` WHERE project_id = ?`
		args = append(args, projectID)
	}
	rows, err := s.db.QueryContext(ctx, q, args...)
	if err != nil {
		return f, err
	}
	defer rows.Close()
	for rows.Next() {
		var b, c, st string
		if err := rows.Scan(&b, &c, &st); err != nil {
			return f, err
		}
		if !slices.Contains(f.Batches, b) {
			f.Batches = append(f.Batches, b)
		}
		if c != "" && !slices.Contains(f.Categories, c) {
			f.Categories = append(f.Categories, c)
		}
		f.Counts[st]++
	}
	slices.Sort(f.Batches)
	slices.Sort(f.Categories)
	return f, rows.Err()
}

// ErrAssetMove 是审核不能做的状态变化。
var ErrAssetMove = errors.New("素材状态不能这样改")

// SetAssetStatus 按审核改素材状态（第 4 步用）：from 不为空时要求当前状态是它（防止并发改判），变化要在 AssetCanMove 里。
func (s *Store) SetAssetStatus(ctx context.Context, id, from, to string) error {
	return s.tx(ctx, func(t *sql.Tx) error {
		var cur string
		if err := t.QueryRowContext(ctx, `SELECT status FROM assets WHERE id = ?`, id).Scan(&cur); err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				return ErrNotFound
			}
			return err
		}
		if from != "" && cur != from {
			return fmt.Errorf("%w：状态已变化，刷新后查看", ErrAssetMove)
		}
		if !AssetCanMove(cur, to) {
			return fmt.Errorf("%w：%s → %s", ErrAssetMove, cur, to)
		}
		at := now()
		_, err := t.ExecContext(ctx, `UPDATE assets SET status = ?, status_at = ?, updated_at = ? WHERE id = ?`, to, at, at, id)
		return err
	})
}
