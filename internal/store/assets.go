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
// R-376 起按 asset.json 推送的素材以带命名空间的稳定 id（uid，如 core:weapon.carbine）为主键：batch 为空、key = uid，
// 批次降为多值元数据（batches，推送时并入）。旧的按批次推送的条目不动，两种共存（取代 / 迁移见 R-380）。

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
	Batch         string         `json:"batch" doc:"旧的按批次推送的批次；按素材 id 推送的为空，批次看 batches"`
	Key           string         `json:"key" doc:"manifest 的 id；独立的图是文件名去扩展名；按素材 id 推送的等于 uid"`
	UID           string         `json:"uid,omitempty" doc:"R-376：素材 id（带命名空间，如 core:weapon.carbine）；旧的按批次推送的条目为空"`
	Batches       []string       `json:"batches" doc:"素材带过的全部批次（多值）；旧条目是 [batch]"`
	Kind          string         `json:"kind" enum:"part,image,audio,font" doc:"part GLB 零件 / image 独立的图（含 SVG） / audio 音效（R-381） / font 字体（R-387）"`
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
	Baseline      string         `json:"baseline,omitempty" doc:"R-393：申请标为已在用、等用户批准的决策 D-x；没有挂着的申请时为空"`
	// R-380：变体与取代（见 assetlinks.go）
	Variant      string     `json:"variant,omitempty" doc:"具名变体的名字：变体条目写自己的名字，素材本身写默认变体名；没有变体时为空"`
	Parent       *AssetRef  `json:"parent,omitempty" doc:"这是哪件素材的变体（变体条目才有）"`
	SupersededBy *AssetRef  `json:"superseded_by,omitempty" doc:"已被哪件素材取代（ks asset supersede 标记）"`
	SupersededAt *time.Time `json:"superseded_at,omitempty" doc:"标记取代的时间"`
}

// AssetVersion 是素材的一个版本。
type AssetVersion struct {
	ID        string                   `json:"id"`
	Version   int                      `json:"version"`
	Batch     string                   `json:"batch"`
	Batches   []string                 `json:"batches" doc:"这一版推送时带的批次（R-376；旧条目是 [batch]）"`
	Branch    string                   `json:"branch"`
	Commit    string                   `json:"commit"`
	File      *FileRef                 `json:"file,omitempty" doc:"主文件：零件的 GLB，独立的图本身"`
	Previews  []AssetPreview           `json:"previews" doc:"零件的预览图"`
	Triangles int                      `json:"triangles,omitempty"`
	Budget    int                      `json:"budget,omitempty" doc:"面数预算上限"`
	Size      []float64                `json:"size,omitempty" doc:"尺寸（米，x / y / z）"`
	Pixels    []int                    `json:"pixels,omitempty" doc:"像素尺寸（宽 / 高）"`
	Audio     *protocol.AssetAudioInfo `json:"audio,omitempty" doc:"音效的时长、声道、采样率、响度、峰值、波形（R-381）"`
	Meta      json.RawMessage          `json:"meta,omitempty" doc:"旧 manifest：这一项的原文；asset.json：项目特有字段（原样）"`
	Subtype   string                   `json:"subtype,omitempty" doc:"细类（R-394）：asset.json 的 subtype 字段（在项目字段里），资源库按类别 → 细类 → 素材 → 变体分层浏览"`
	Source    string                   `json:"source,omitempty" doc:"来源（R-376）"`
	License   string                   `json:"license,omitempty" doc:"许可（R-376）"`
	Variants  json.RawMessage          `json:"variants,omitempty" doc:"asset.json 里具名变体的原文（R-376 先存下，R-380 拆成各自的版本与审核）"`
	AutoThumb *FileRef                 `json:"auto_thumb,omitempty" doc:"自动生成的缩略图（R-383）：只有 GLB、没有预览图的零件，网页把模型渲染成图后回传，图片墙用它"`
	Submitter AssetSubmitter           `json:"submitter" doc:"这一版的提交者"`
	CreatedAt time.Time                `json:"created_at"`
}

// AssetPreview 是零件的一张预览图；View 是机位（R-376：文件名「<名字>__<机位>.png」里的机位，网页按它分组；旧数据为空）。
type AssetPreview struct {
	FileRef
	View string `json:"view,omitempty" doc:"机位"`
}

// AssetDetail 是素材与它的全部版本（新的在前）。
type AssetDetail struct {
	Asset
	Versions    []AssetVersion    `json:"versions"`
	Reviews     []AssetReview     `json:"reviews" doc:"审核记录（R-351 第 4 步），新的在前"`
	Attachments []AssetAttachment `json:"attachments" doc:"附件（R-365：访客上传、你收下为附件的文件），新的在前"`
	Variants    []AssetRef        `json:"variants" doc:"R-380：这件素材的具名变体（各自有版本和审核），按名字排序"`
	Supersedes  []AssetRef        `json:"supersedes" doc:"R-380：被这件素材取代的旧条目"`
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
	ByID          bool   // R-376：按素材 id 推送（key 是 uid，Batch 为空，批次在每条素材的 Batches 里）
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
	res := protocol.AssetPushResult{PushID: n.PushID, Project: n.ProjectName, Batch: n.Batch, ByID: n.ByID, Items: []protocol.AssetPushItemResult{}, Reused: n.Reused}
	if n.ByID {
		n.Batch = ""
	}
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
			if err := linkVariant(ctx, t, n, it); err != nil {
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
	previews := []AssetPreview{}
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
		previews = append(previews, AssetPreview{FileRef: f})
	}
	for i, p := range it.Previews {
		previews[i].View = p.View
		if p.View != "" {
			pshas[i] += "@" + p.View // 机位改名也算内容有变
		}
	}
	slices.Sort(pshas)
	for _, p := range pshas {
		parts = append(parts, "p:"+p)
	}
	if n.ByID { // asset.json：来源许可、变体原文变了也记新版本；批次只是元数据，不算
		parts = append(parts, "s:"+it.Source, "l:"+it.License, "v:"+string(it.Variants))
	}
	if d := declaredAudio(it.Audio); d != "" { // 音效（R-381）：从文件读出的随文件内容走；asset.json 声明的值变了也记新版本
		parts = append(parts, "a:"+d)
	}
	batches := cleanBatches(it.Batches)
	if n.Batch != "" {
		batches = []string{n.Batch}
	}
	sum := sha256.Sum256([]byte(strings.Join(parts, "\n")))
	digest := hex.EncodeToString(sum[:])

	var id, latest, had string
	var seq, version int
	err := t.QueryRowContext(ctx, `SELECT id, seq, version, latest_id, batches FROM assets WHERE project_id = ? AND batch = ? AND key = ?`, n.ProjectID, n.Batch, it.Key).Scan(&id, &seq, &version, &latest, &had)
	switch {
	case errors.Is(err, sql.ErrNoRows):
		id = protocol.NewID()
		if err := t.QueryRowContext(ctx, `SELECT COALESCE(MAX(seq), 0) + 1 FROM assets`).Scan(&seq); err != nil {
			return r, err
		}
		version = 0
		uid := ""
		if n.Batch == "" {
			uid = it.Key
		}
		if _, err := t.ExecContext(ctx, `INSERT INTO assets(id, seq, project_id, batch, key, kind, category, grp, status, version, submitter_kind, submitter_ref, submitter_name, requirement_id, status_at, created_at, updated_at, uid, batches)
			VALUES(?, ?, ?, ?, ?, ?, ?, ?, ?, 0, ?, ?, ?, ?, ?, ?, ?, ?, ?)`, id, seq, n.ProjectID, n.Batch, it.Key, it.Kind, it.Category, it.Group, status, n.Submitter.Kind, n.Submitter.Ref, n.Submitter.Name, n.RequirementID, at, at, at, uid, batchesJSON(uidBatches(uid, nil, batches))); err != nil {
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
		merged := had
		if n.Batch == "" { // 按 id 推送的素材：这次带的批次并入（只增不删）
			merged = batchesJSON(cleanBatches(append(parseBatches(had), batches...)))
		}
		if old == digest {
			// 内容没变不新增版本，但批次是元数据，照样并入（同一素材进了新批次）
			if merged != had {
				if _, err := t.ExecContext(ctx, `UPDATE assets SET batches = ?, updated_at = ? WHERE id = ?`, merged, at, id); err != nil {
					return r, err
				}
			}
			r.Code, r.Version, r.Result = AssetCode(seq), version, "unchanged"
			return r, nil
		}
		if merged != had {
			if _, err := t.ExecContext(ctx, `UPDATE assets SET batches = ? WHERE id = ?`, merged, id); err != nil {
				return r, err
			}
		}
		r.Result = "updated"
	}
	version++
	vid := protocol.NewID()
	vbatches := "[]"
	if n.Batch == "" {
		vbatches = batchesJSON(batches)
	}
	if _, err := t.ExecContext(ctx, `INSERT INTO asset_versions(id, asset_id, version, push_id, batch, branch, commit_sha, file_id, previews, triangles, budget, size, pixels, meta, digest, submitter_kind, submitter_ref, submitter_name, created_at, batches, source, license, variants, audio)
		VALUES(?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		vid, id, version, n.PushID, n.Batch, n.Branch, n.Commit, fileID, previewsJSON(previews), it.Triangles, it.Budget,
		jsonOrEmpty(it.Size), jsonOrEmpty(it.Pixels), string(it.Meta), digest, n.Submitter.Kind, n.Submitter.Ref, n.Submitter.Name, at,
		vbatches, it.Source, it.License, string(it.Variants), audioJSON(it.Audio)); err != nil {
		return r, err
	}
	reqSet := ""
	args := []any{version, vid, it.Kind, it.Category, it.Group, status, n.Submitter.Kind, n.Submitter.Ref, n.Submitter.Name}
	if n.RequirementID != "" {
		reqSet = ", requirement_id = ?"
		args = append(args, n.RequirementID)
	}
	args = append(args, at, at, id)
	if _, err := t.ExecContext(ctx, `UPDATE assets SET version = ?, latest_id = ?, kind = ?, category = ?, grp = ?, status = ?, submitter_kind = ?, submitter_ref = ?, submitter_name = ?, baseline_id = ''`+reqSet+`, status_at = ?, updated_at = ? WHERE id = ?`, args...); err != nil {
		return r, err
	}
	r.Code, r.Version = AssetCode(seq), version
	return r, nil
}

// cleanBatches 去掉空白和重复，保持顺序。
func cleanBatches(xs []string) []string {
	out := []string{}
	for _, x := range xs {
		if x = strings.TrimSpace(x); x != "" && !slices.Contains(out, x) {
			out = append(out, x)
		}
	}
	return out
}

// uidBatches：新建素材时存的 batches 列——按 id 推送的存这次带的批次；旧条目留空（批次看 batch 列）。
func uidBatches(uid string, had, add []string) []string {
	if uid == "" {
		return nil
	}
	return cleanBatches(append(had, add...))
}

func batchesJSON(xs []string) string {
	if len(xs) == 0 {
		return "[]"
	}
	b, _ := json.Marshal(xs)
	return string(b)
}

func parseBatches(s string) []string {
	var out []string
	_ = json.Unmarshal([]byte(s), &out)
	return cleanBatches(out)
}

// assetBatches：素材（或版本）的全部批次——旧条目只有 batch 列，按 id 推送的看 batches 列。
func assetBatches(batch, raw string) []string {
	return cleanBatches(append([]string{batch}, parseBatches(raw)...))
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
	ID        string // R-376：素材 id（uid）或编号 A-x；末尾带 * 时按 id 前缀查（如 core:weapon.*）
	Limit     int    // R-391：最多返回几条，0 不限
	Offset    int    // R-391：跳过前几条（分页）
}

const assetCols = `a.id, a.seq, a.project_id, a.batch, a.key, a.kind, a.category, a.grp, a.status, a.version, a.submitter_kind, a.submitter_ref, a.submitter_name, a.requirement_id, a.status_at, a.created_at, a.updated_at, a.uid, a.batches,
	COALESCE((SELECT 'D-' || d.seq FROM asset_baselines b JOIN decisions d ON d.id = b.decision_id WHERE b.id = a.baseline_id AND b.status = 'pending'), '')`

const versionCols = `v.id, v.version, v.batch, v.branch, v.commit_sha, v.file_id, v.previews, v.triangles, v.budget, v.size, v.pixels, v.meta, v.submitter_kind, v.submitter_ref, v.submitter_name, v.created_at, v.batches, v.source, v.license, v.variants, v.auto_thumb, v.audio`

func scanAsset(row interface{ Scan(...any) error }, extra ...any) (Asset, error) {
	var a Asset
	var seq int
	var statusAt, created, updated, batches string
	dst := append([]any{&a.ID, &seq, &a.ProjectID, &a.Batch, &a.Key, &a.Kind, &a.Category, &a.Group, &a.Status, &a.Version, &a.Submitter.Kind, &a.Submitter.Ref, &a.Submitter.Name, &a.RequirementID, &statusAt, &created, &updated, &a.UID, &batches, &a.Baseline}, extra...)
	if err := row.Scan(dst...); err != nil {
		return a, err
	}
	a.Code = AssetCode(seq)
	a.Batches = assetBatches(a.Batch, batches)
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
	batches, variants        string
	autoThumb, audio         string
}

func (r *versionRow) dst() []any {
	return []any{&r.v.ID, &r.v.Version, &r.v.Batch, &r.v.Branch, &r.v.Commit, &r.fileID, &r.previews, &r.v.Triangles, &r.v.Budget, &r.size, &r.pixels, &r.meta, &r.v.Submitter.Kind, &r.v.Submitter.Ref, &r.v.Submitter.Name, &r.crtd,
		&r.batches, &r.v.Source, &r.v.License, &r.variants, &r.autoThumb, &r.audio}
}

func (s *Store) finishVersion(ctx context.Context, r versionRow) AssetVersion {
	v := r.v
	v.Previews = []AssetPreview{}
	_ = json.Unmarshal([]byte(r.previews), &v.Previews)
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
		v.Subtype = metaSubtype(v.Meta)
	}
	if r.variants != "" && json.Valid([]byte(r.variants)) {
		v.Variants = json.RawMessage(r.variants)
	}
	if r.autoThumb != "" {
		if f, err := s.GetFile(ctx, r.autoThumb); err == nil {
			v.AutoThumb = &f
		}
	}
	if r.audio != "" {
		var a protocol.AssetAudioInfo
		if json.Unmarshal([]byte(r.audio), &a) == nil {
			v.Audio = &a
		}
	}
	v.Batches = assetBatches(v.Batch, r.batches)
	v.CreatedAt = parseTS(r.crtd)
	return v
}

// audioJSON：音效的音频信息存成 JSON；不是音效的存空。
func audioJSON(a *protocol.AssetAudioInfo) string {
	if a == nil {
		return ""
	}
	b, _ := json.Marshal(a)
	return string(b)
}

// declaredAudio：音频信息里取自 asset.json 声明的那几项（算进内容指纹）；没有声明时为空。
func declaredAudio(a *protocol.AssetAudioInfo) string {
	if a == nil || len(a.Declared) == 0 {
		return ""
	}
	var parts []string
	for _, k := range a.Declared {
		switch k {
		case "duration_s":
			parts = append(parts, fmt.Sprintf("%s=%g", k, a.Duration))
		case "loudness_lufs":
			if a.Loudness != nil {
				parts = append(parts, fmt.Sprintf("%s=%g", k, *a.Loudness))
			}
		case "peak_dbfs":
			if a.Peak != nil {
				parts = append(parts, fmt.Sprintf("%s=%g", k, *a.Peak))
			}
		}
	}
	return strings.Join(parts, ",")
}

// metaSubtype 取项目字段里的细类（asset.json 的 subtype，R-394）；不是字符串或没写时为空。
func metaSubtype(meta json.RawMessage) string {
	var m struct {
		Subtype any `json:"subtype"`
	}
	if json.Unmarshal(meta, &m) != nil {
		return ""
	}
	s, _ := m.Subtype.(string)
	return strings.TrimSpace(s)
}

// ErrAutoThumbNotNeeded：这一版不需要自动缩略图（不是零件、没有 GLB，或已经有预览图）。
var ErrAutoThumbNotNeeded = errors.New("这一版不需要自动生成缩略图：只给有 GLB、没有预览图的零件")

// SetAssetAutoThumb 记下某一版自动生成的缩略图（R-383）。只给有 GLB、没有预览图的零件；
// 已经有了就不覆盖（几个页面同时渲染时先到的为准），返回 false。
func (s *Store) SetAssetAutoThumb(ctx context.Context, ref string, version int, fileID string) (bool, error) {
	d, err := s.GetAsset(ctx, ref)
	if err != nil {
		return false, err
	}
	var v *AssetVersion
	for i := range d.Versions {
		if d.Versions[i].Version == version {
			v = &d.Versions[i]
		}
	}
	if v == nil {
		return false, fmt.Errorf("%w：%s 没有第 %d 版", ErrNotFound, d.Code, version)
	}
	if d.Kind != "part" || v.File == nil || len(v.Previews) > 0 {
		return false, ErrAutoThumbNotNeeded
	}
	res, err := s.db.ExecContext(ctx, `UPDATE asset_versions SET auto_thumb = ? WHERE id = ? AND auto_thumb = ''`, fileID, v.ID)
	if err != nil {
		return false, err
	}
	n, _ := res.RowsAffected()
	return n > 0, nil
}

// assetWhere 把筛选条件拼成 WHERE 子句（ListAssets 与 CountAssets 共用）。
func assetWhere(f AssetFilter) (string, []any) {
	var where []string
	var args []any
	for _, c := range []struct{ col, v string }{{"a.project_id", f.ProjectID}, {"a.category", f.Category}, {"a.status", f.Status}, {"a.kind", f.Kind}} {
		if c.v != "" {
			where = append(where, c.col+" = ?")
			args = append(args, c.v)
		}
	}
	if b := strings.TrimSpace(f.Batch); b != "" {
		where = append(where, assetInBatch)
		args = append(args, b, b)
	}
	if id := strings.TrimSpace(f.ID); id != "" {
		pre, prefix := strings.CutSuffix(id, "*")
		switch {
		case prefix:
			where = append(where, "a.uid != '' AND substr(a.uid, 1, ?) = ?")
			args = append(args, len(pre), pre)
		case assetSeq(id) > 0:
			where = append(where, "a.seq = ?")
			args = append(args, assetSeq(id))
		default:
			where = append(where, "(a.uid = ? OR a.id = ?)")
			args = append(args, id, id)
		}
	}
	if len(where) == 0 {
		return "", args
	}
	return ` WHERE ` + strings.Join(where, " AND "), args
}

// CountAssets 返回符合筛选的素材总数（不受 Limit / Offset 影响，R-391）。
func (s *Store) CountAssets(ctx context.Context, f AssetFilter) (int, error) {
	w, args := assetWhere(f)
	var n int
	err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM assets a`+w, args...).Scan(&n)
	return n, err
}

// ListAssets 列出素材（带最新版本），按批次、编号排序。
func (s *Store) ListAssets(ctx context.Context, f AssetFilter) ([]Asset, error) {
	w, args := assetWhere(f)
	q := `SELECT ` + assetCols + `, ` + versionCols + ` FROM assets a JOIN asset_versions v ON v.id = a.latest_id` + w + ` ORDER BY a.batch, a.seq`
	if f.Limit > 0 {
		q += ` LIMIT ? OFFSET ?`
		args = append(args, f.Limit, max(f.Offset, 0))
	}
	rows, err := s.db.QueryContext(ctx, q, args...)
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
	return out, s.fillAssetLinks(ctx, out)
}

// assetInBatch：素材带过这个批次（旧条目看 batch 列，按 id 推送的看 batches）。参数：批次 ×2。
const assetInBatch = `(a.batch = ? OR EXISTS (SELECT 1 FROM json_each(a.batches) WHERE value = ?))`

// assetSeq 解析编号 A-x，不是编号返回 0。
func assetSeq(ref string) int {
	n, ok := strings.CutPrefix(strings.ToUpper(strings.TrimSpace(ref)), "A-")
	if !ok {
		return 0
	}
	seq, err := strconv.Atoi(n)
	if err != nil || seq <= 0 {
		return 0
	}
	return seq
}

// ErrAssetAmbiguous 是素材 id 在多个项目里都有、又没指定项目。
var ErrAssetAmbiguous = errors.New("多个项目里都有这个素材 id，请指定项目")

// GetAsset 读一条素材和它的全部版本；ref 是 ID、编号 A-x 或素材 id（uid；多个项目都有时报 ErrAssetAmbiguous）。
func (s *Store) GetAsset(ctx context.Context, ref string) (AssetDetail, error) {
	return s.FindAsset(ctx, "", ref)
}

// FindAsset 同 GetAsset；projectID 不为空时素材 id 只在这个项目里找（R-376）。
func (s *Store) FindAsset(ctx context.Context, projectID, ref string) (AssetDetail, error) {
	var d AssetDetail
	ref = strings.TrimSpace(ref)
	a, err := s.findAssetRow(ctx, projectID, ref)
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
	if err := rows.Err(); err != nil {
		return d, err
	}
	if d.Reviews, err = s.ListAssetReviews(ctx, a.ID); err != nil {
		return d, err
	}
	if d.Attachments, err = s.ListAssetAttachments(ctx, a.ID); err != nil {
		return d, err
	}
	return d, s.fillAssetDetail(ctx, &d)
}

func (s *Store) findAssetRow(ctx context.Context, projectID, ref string) (Asset, error) {
	if seq := assetSeq(ref); seq > 0 {
		return scanAsset(s.db.QueryRowContext(ctx, `SELECT `+assetCols+` FROM assets a WHERE a.seq = ?`, seq))
	}
	if strings.HasPrefix(strings.ToUpper(ref), "A-") || ref == "" {
		return Asset{}, sql.ErrNoRows
	}
	a, err := scanAsset(s.db.QueryRowContext(ctx, `SELECT `+assetCols+` FROM assets a WHERE a.id = ?`, ref))
	if !errors.Is(err, sql.ErrNoRows) {
		return a, err
	}
	q, args := `SELECT `+assetCols+` FROM assets a WHERE a.uid = ?`, []any{ref}
	if projectID != "" {
		q += ` AND a.project_id = ?`
		args = append(args, projectID)
	}
	rows, err := s.db.QueryContext(ctx, q+` LIMIT 2`, args...)
	if err != nil {
		return a, err
	}
	defer rows.Close()
	var got []Asset
	for rows.Next() {
		x, err := scanAsset(rows)
		if err != nil {
			return a, err
		}
		got = append(got, x)
	}
	switch len(got) {
	case 0:
		return a, sql.ErrNoRows
	case 1:
		return got[0], rows.Err()
	}
	return a, ErrAssetAmbiguous
}

// AssetFacets 是一个项目里素材的批次和类别（筛选项用），按名称排序。
type AssetFacets struct {
	Batches        []string       `json:"batches"`
	Categories     []string       `json:"categories"`
	Counts         map[string]int `json:"counts" doc:"各状态的素材数"`
	CategoryCounts map[string]int `json:"category_counts,omitempty" doc:"各类别的素材数（R-391）"`
}

// ListAssetFacets 统计项目里素材的批次、类别和各状态数量；projectID 为空时统计全部。
func (s *Store) ListAssetFacets(ctx context.Context, projectID string) (AssetFacets, error) {
	f := AssetFacets{Batches: []string{}, Categories: []string{}, Counts: map[string]int{}, CategoryCounts: map[string]int{}}
	for _, st := range AssetStatuses {
		f.Counts[st] = 0
	}
	q, args := `SELECT batch, batches, category, status FROM assets`, []any{}
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
		var b, bs, c, st string
		if err := rows.Scan(&b, &bs, &c, &st); err != nil {
			return f, err
		}
		for _, x := range assetBatches(b, bs) {
			if !slices.Contains(f.Batches, x) {
				f.Batches = append(f.Batches, x)
			}
		}
		if c != "" && !slices.Contains(f.Categories, c) {
			f.Categories = append(f.Categories, c)
		}
		if c != "" {
			f.CategoryCounts[c]++
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

func previewsJSON(ps []AssetPreview) string {
	b, _ := json.Marshal(ps)
	return string(b)
}
