package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/NekroAI/kitestring/internal/protocol"
)

// 资源库的变体、取代与删除（R-380）。
//
// 变体：asset.json 里声明的具名变体各是一条素材（key = uid = 「素材 id@变体名」），有自己的版本和审核；
// parent_id 指向素材本身，variant 是变体名。素材本身就是默认变体（variant 写默认变体名）。asset.json 里去掉的变体不自动删。
// 取代：ks asset supersede 把旧条目标为被某件素材取代（superseded_by），旧条目照常保留、能看能审，网页和 list / get 显示取代关系；
// 分享挂在批次 / 类别上时，范围里被取代的素材，取代它的那件（连同它的变体）也算在分享里。
// 删除：只给推错的条目，网页上确认后真删（版本、审核记录、附件一起删，变体一起删）；文件留在附件目录，由孤儿文件清理处理。

// AssetRef 是对另一件素材的引用（变体的上级、取代关系）。
type AssetRef struct {
	ID      string `json:"id"`
	Code    string `json:"code" doc:"素材编号 A-x"`
	UID     string `json:"uid,omitempty" doc:"素材 id；旧的按批次推送的条目为空"`
	Key     string `json:"key"`
	Batch   string `json:"batch,omitempty" doc:"旧条目的批次"`
	Variant string `json:"variant,omitempty"`
	Status  string `json:"status" enum:"wip,pending,approved,rework"`
}

const refCols = `id, seq, uid, key, batch, variant, status`

func scanRef(row interface{ Scan(...any) error }) (AssetRef, error) {
	var r AssetRef
	var seq int
	err := row.Scan(&r.ID, &seq, &r.UID, &r.Key, &r.Batch, &r.Variant, &r.Status)
	r.Code = AssetCode(seq)
	return r, err
}

// inIDs 把 ids 分批拼成 `... IN (?, ...)` 查询，每批交给 fn。
func (s *Store) inIDs(ctx context.Context, q string, ids []string, fn func(*sql.Rows) error) error {
	for len(ids) > 0 {
		n := min(len(ids), 500)
		part := ids[:n]
		ids = ids[n:]
		args := make([]any, len(part))
		for i, id := range part {
			args[i] = id
		}
		rows, err := s.db.QueryContext(ctx, q+`(?`+strings.Repeat(", ?", len(part)-1)+`)`, args...)
		if err != nil {
			return err
		}
		for rows.Next() {
			if err := fn(rows); err != nil {
				rows.Close()
				return err
			}
		}
		rows.Close()
		if err := rows.Err(); err != nil {
			return err
		}
	}
	return nil
}

func (s *Store) assetRefs(ctx context.Context, ids []string) (map[string]AssetRef, error) {
	out := map[string]AssetRef{}
	err := s.inIDs(ctx, `SELECT `+refCols+` FROM assets WHERE id IN `, ids, func(r *sql.Rows) error {
		x, err := scanRef(r)
		out[x.ID] = x
		return err
	})
	return out, err
}

// fillAssetLinks 补上素材的变体名、上级和取代关系。
func (s *Store) fillAssetLinks(ctx context.Context, as []Asset) error {
	if len(as) == 0 {
		return nil
	}
	type link struct{ variant, parent, by, at string }
	links := map[string]link{}
	ids := make([]string, len(as))
	for i, a := range as {
		ids[i] = a.ID
	}
	err := s.inIDs(ctx, `SELECT id, variant, parent_id, superseded_by, superseded_at FROM assets WHERE id IN `, ids, func(r *sql.Rows) error {
		var id string
		var l link
		if err := r.Scan(&id, &l.variant, &l.parent, &l.by, &l.at); err != nil {
			return err
		}
		links[id] = l
		return nil
	})
	if err != nil {
		return err
	}
	var want []string
	for _, l := range links {
		for _, x := range []string{l.parent, l.by} {
			if x != "" && !slices.Contains(want, x) {
				want = append(want, x)
			}
		}
	}
	refs, err := s.assetRefs(ctx, want)
	if err != nil {
		return err
	}
	for i := range as {
		l := links[as[i].ID]
		as[i].Variant = l.variant
		if r, ok := refs[l.parent]; ok {
			as[i].Parent = &r
		}
		if r, ok := refs[l.by]; ok {
			as[i].SupersededBy = &r
			as[i].SupersededAt = supersededAt(l.at)
		}
	}
	return nil
}

// fillAssetDetail 补上详情里的变体和取代关系。
func (s *Store) fillAssetDetail(ctx context.Context, d *AssetDetail) error {
	one := []Asset{d.Asset}
	if err := s.fillAssetLinks(ctx, one); err != nil {
		return err
	}
	d.Asset = one[0]
	var err error
	if d.Variants, err = s.refsWhere(ctx, `parent_id = ? ORDER BY variant, seq`, d.ID); err != nil {
		return err
	}
	d.Supersedes, err = s.refsWhere(ctx, `superseded_by = ? ORDER BY seq`, d.ID)
	return err
}

func (s *Store) refsWhere(ctx context.Context, where string, args ...any) ([]AssetRef, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT `+refCols+` FROM assets WHERE `+where, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []AssetRef{}
	for rows.Next() {
		r, err := scanRef(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// linkVariant：按 asset.json 推送的一条推完后记下变体关系。变体条目（Parent 不为空）指向素材本身——素材要在这次推送里或已经在库里；
// 素材本身写上默认变体名。旧的按批次推送、没有变体的不动。
func linkVariant(ctx context.Context, t *sql.Tx, n NewAssetPush, it protocol.AssetItem) error {
	if !n.ByID || (it.Parent == "" && it.Variant == "") {
		return nil
	}
	if it.Parent == "" {
		_, err := t.ExecContext(ctx, `UPDATE assets SET variant = ? WHERE project_id = ? AND batch = '' AND key = ? AND parent_id = ''`, it.Variant, n.ProjectID, it.Key)
		return err
	}
	var pid, pparent string
	err := t.QueryRowContext(ctx, `SELECT id, parent_id FROM assets WHERE project_id = ? AND batch = '' AND key = ?`, n.ProjectID, it.Parent).Scan(&pid, &pparent)
	if errors.Is(err, sql.ErrNoRows) {
		return fmt.Errorf("变体所属的素材 %s 不在资源库里，要和变体一起推送", it.Parent)
	}
	if err != nil {
		return err
	}
	if pparent != "" {
		return fmt.Errorf("%s 本身是变体，变体下面不能再有变体", it.Parent)
	}
	_, err = t.ExecContext(ctx, `UPDATE assets SET variant = ?, parent_id = ? WHERE project_id = ? AND batch = '' AND key = ?`, it.Variant, pid, n.ProjectID, it.Key)
	return err
}

// ---------- 取代 ----------

// SupersedePair 是一条取代关系：Old 被 New 取代。New 为空表示取消 Old 的取代标记。
// 两边都可以写编号 A-x、素材 ID 或素材 id；指定了项目时旧条目还可以写「批次/文件名」。
type SupersedePair struct {
	Old string `json:"old" doc:"旧条目：编号 A-x、素材 ID 或素材 id"`
	New string `json:"new,omitempty" doc:"取代它的素材：编号 A-x、素材 ID 或素材 id；取消取代时不写"`
}

// SupersedeResult 是一条取代关系的处理结果。
type SupersedeResult struct {
	Old     string    `json:"old"`
	New     string    `json:"new,omitempty"`
	OldRef  *AssetRef `json:"old_ref,omitempty"`
	NewRef  *AssetRef `json:"new_ref,omitempty"`
	Result  string    `json:"result" enum:"set,unchanged,cleared,error" doc:"set 标记了取代 / unchanged 已经是这样 / cleared 取消了取代 / error 有错（见 error）"`
	Error   string    `json:"error,omitempty"`
	Replace *AssetRef `json:"replaced,omitempty" doc:"原来标记的取代者（这次改成了别的）"`
}

// SupersedeAssets 批量标记（或取消）取代关系。projectID 不为空时素材 id 只在这个项目里找，编号也要属于这个项目。
// 先逐条校验：找不到、跨项目、自己取代自己、形成循环都算错；有一条错时一条都不改（返回 applied = false），dryRun 时只校验。
func (s *Store) SupersedeAssets(ctx context.Context, projectID string, pairs []SupersedePair, dryRun bool) (res []SupersedeResult, applied bool, err error) {
	res = make([]SupersedeResult, len(pairs))
	cur := map[string]string{} // 现在的取代关系：旧 → 新（只装涉及到的）
	load := func(id string) (string, error) {
		if by, ok := cur[id]; ok {
			return by, nil
		}
		var by string
		err := s.db.QueryRowContext(ctx, `SELECT superseded_by FROM assets WHERE id = ?`, id).Scan(&by)
		if errors.Is(err, sql.ErrNoRows) {
			err = nil
		}
		cur[id] = by
		return by, err
	}
	find := func(ref string) (*AssetRef, string, error) {
		a, err := s.findAssetRow(ctx, projectID, strings.TrimSpace(ref))
		if errors.Is(err, sql.ErrNoRows) && projectID != "" {
			// 旧的按批次推送的条目写成「批次/文件名」（ks asset list 里的写法）
			if b, k, ok := strings.Cut(strings.TrimSpace(ref), "/"); ok && !strings.Contains(ref, ":") {
				a, err = scanAsset(s.db.QueryRowContext(ctx, `SELECT `+assetCols+` FROM assets a WHERE a.project_id = ? AND a.batch = ? AND a.key = ?`, projectID, b, k))
			}
		}
		switch {
		case errors.Is(err, sql.ErrNoRows):
			return nil, "找不到素材 " + ref, nil
		case errors.Is(err, ErrAssetAmbiguous):
			return nil, ref + "：" + err.Error(), nil
		case err != nil:
			return nil, "", err
		case projectID != "" && a.ProjectID != projectID:
			return nil, ref + " 不在这个项目里", nil
		}
		r, err := scanRef(s.db.QueryRowContext(ctx, `SELECT `+refCols+` FROM assets WHERE id = ?`, a.ID))
		return &r, "", err
	}
	bad := false
	type todo struct{ old, by string }
	var plan []todo
	seen := map[string]int{}
	for i, p := range pairs {
		r := &res[i]
		r.Old, r.New = strings.TrimSpace(p.Old), strings.TrimSpace(p.New)
		fail := func(msg string) {
			r.Result, r.Error = "error", msg
			bad = true
		}
		if r.Old == "" {
			fail("没写旧条目")
			continue
		}
		old, msg, err := find(r.Old)
		if err != nil {
			return nil, false, err
		}
		if msg != "" {
			fail(msg)
			continue
		}
		r.OldRef = old
		if j, dup := seen[old.ID]; dup {
			fail(fmt.Sprintf("%s 在第 %d 条里已经写过", old.Code, j+1))
			continue
		}
		seen[old.ID] = i
		had, err := load(old.ID)
		if err != nil {
			return nil, false, err
		}
		if r.New == "" {
			r.Result = "cleared"
			if had == "" {
				r.Result = "unchanged"
			}
			cur[old.ID] = ""
			plan = append(plan, todo{old.ID, ""})
			continue
		}
		nw, msg, err := find(r.New)
		if err != nil {
			return nil, false, err
		}
		if msg != "" {
			fail(msg)
			continue
		}
		r.NewRef = nw
		var oldProj, newProj string
		_ = s.db.QueryRowContext(ctx, `SELECT project_id FROM assets WHERE id = ?`, old.ID).Scan(&oldProj)
		_ = s.db.QueryRowContext(ctx, `SELECT project_id FROM assets WHERE id = ?`, nw.ID).Scan(&newProj)
		switch {
		case nw.ID == old.ID:
			fail("不能自己取代自己")
			continue
		case oldProj != newProj:
			fail(old.Code + " 和 " + nw.Code + " 不在同一个项目里")
			continue
		}
		if had == nw.ID {
			r.Result = "unchanged"
		} else {
			r.Result = "set"
			if had != "" {
				if m, err := s.assetRefs(ctx, []string{had}); err == nil {
					if x, ok := m[had]; ok {
						r.Replace = &x
					}
				}
			}
		}
		cur[old.ID] = nw.ID
		plan = append(plan, todo{old.ID, nw.ID})
	}
	// 循环：顺着取代关系（库里的 + 这次的）从每条的新素材往下走，回到旧条目就是循环
	for i := range res {
		r := &res[i]
		if r.Result == "error" || r.NewRef == nil {
			continue
		}
		at := r.NewRef.ID
		for step := 0; step < 64 && at != ""; step++ {
			if at == r.OldRef.ID {
				r.Result, r.Error = "error", "会形成循环（"+r.NewRef.Code+" 顺着取代关系又回到 "+r.OldRef.Code+"）"
				bad = true
				break
			}
			next, err := load(at)
			if err != nil {
				return nil, false, err
			}
			at = next
		}
	}
	if bad || dryRun {
		return res, false, nil
	}
	err = s.tx(ctx, func(t *sql.Tx) error {
		at := now()
		for _, p := range plan {
			stamp := at
			if p.by == "" {
				stamp = ""
			}
			if _, err := t.ExecContext(ctx, `UPDATE assets SET superseded_by = ?, superseded_at = CASE WHEN superseded_by = ? THEN superseded_at ELSE ? END WHERE id = ?`, p.by, p.by, stamp, p.old); err != nil {
				return err
			}
		}
		return nil
	})
	return res, err == nil, err
}

// replacementsOf：分享范围里被取代的素材，取代它的那件（顺着链往下）和它的变体（R-380）。返回要补进范围的素材 ID（不含已在 have 里的）。
func (s *Store) replacementsOf(ctx context.Context, have map[string]bool) ([]string, error) {
	by := map[string]string{}
	rows, err := s.db.QueryContext(ctx, `SELECT id, superseded_by FROM assets WHERE superseded_by != ''`)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var a, b string
		if err := rows.Scan(&a, &b); err != nil {
			rows.Close()
			return nil, err
		}
		by[a] = b
	}
	rows.Close()
	if len(by) == 0 {
		return nil, rows.Err()
	}
	var add []string
	seen := map[string]bool{}
	for id := range have {
		at := by[id]
		for step := 0; step < 64 && at != "" && !seen[at]; step++ {
			seen[at] = true
			if !have[at] {
				add = append(add, at)
			}
			at = by[at]
		}
	}
	if len(add) == 0 {
		return nil, nil
	}
	var kids []string
	err = s.inIDs(ctx, `SELECT id FROM assets WHERE parent_id IN `, add, func(r *sql.Rows) error {
		var id string
		if err := r.Scan(&id); err != nil {
			return err
		}
		if !have[id] && !seen[id] {
			seen[id] = true
			kids = append(kids, id)
		}
		return nil
	})
	slices.Sort(add)
	return append(add, kids...), err
}

// ---------- 删除 ----------

// AssetShareUse 是引用着某件素材的分享。
type AssetShareUse struct {
	ID   string `json:"id"`
	Name string `json:"name"`
	Via  string `json:"via" enum:"asset,asset_batch,asset_cat" doc:"asset 单独挂着这件素材 / asset_batch 挂着它所在的批次 / asset_cat 挂着它的类别"`
	Ref  string `json:"ref" doc:"批次名或类别名；单独挂着时是素材编号"`
}

// AssetDeleteInfo 是删除前的检查：删掉会连带什么，网页上确认时据此额外提示。
type AssetDeleteInfo struct {
	Asset      AssetRef        `json:"asset"`
	Versions   int             `json:"versions" doc:"版本数（含变体的）"`
	Reviews    int             `json:"reviews" doc:"审核记录条数（含变体的）"`
	Variants   []AssetRef      `json:"variants" doc:"一起删掉的变体"`
	Supersedes []AssetRef      `json:"supersedes" doc:"被它取代的旧条目：删后取消它们的取代标记"`
	Shares     []AssetShareUse `json:"shares" doc:"引用着它的分享（没撤回的）：删后访客看不到它"`
}

// AssetDeleteCheck 列出删除一件素材会连带什么。
func (s *Store) AssetDeleteCheck(ctx context.Context, ref string) (AssetDeleteInfo, error) {
	var info AssetDeleteInfo
	d, err := s.GetAsset(ctx, ref)
	if err != nil {
		return info, err
	}
	r, err := scanRef(s.db.QueryRowContext(ctx, `SELECT `+refCols+` FROM assets WHERE id = ?`, d.ID))
	if err != nil {
		return info, err
	}
	info.Asset, info.Variants, info.Supersedes = r, d.Variants, d.Supersedes
	ids := []string{d.ID}
	for _, v := range d.Variants {
		ids = append(ids, v.ID)
	}
	if err := s.inIDs(ctx, `SELECT COUNT(*) FROM asset_versions WHERE asset_id IN `, ids, func(r *sql.Rows) error {
		var n int
		err := r.Scan(&n)
		info.Versions += n
		return err
	}); err != nil {
		return info, err
	}
	if err := s.inIDs(ctx, `SELECT COUNT(*) FROM asset_reviews WHERE asset_id IN `, ids, func(r *sql.Rows) error {
		var n int
		err := r.Scan(&n)
		info.Reviews += n
		return err
	}); err != nil {
		return info, err
	}
	// 引用着它的分享：单独挂着（素材或它的变体）、挂着它的批次或类别；被单独取消的不算，撤回的分享不算
	refs := []string{}
	q := `SELECT sh.id, sh.name, i.kind, i.ref FROM share_items i JOIN shares sh ON sh.id = i.share_id WHERE sh.revoked_at = '' AND i.excluded = 0 AND (`
	var conds []string
	var args []any
	conds = append(conds, `(i.kind = ? AND i.ref IN (?`+strings.Repeat(", ?", len(ids)-1)+`))`)
	args = append(args, ShareKindAsset)
	for _, id := range ids {
		args = append(args, id)
	}
	for _, b := range d.Batches {
		refs = append(refs, AssetScopeRef(d.ProjectID, b))
	}
	if len(refs) > 0 {
		conds = append(conds, `(i.kind = ? AND i.ref IN (?`+strings.Repeat(", ?", len(refs)-1)+`))`)
		args = append(args, ShareKindAssetBatch)
		for _, x := range refs {
			args = append(args, x)
		}
	}
	if d.Category != "" {
		conds = append(conds, `(i.kind = ? AND i.ref = ?)`)
		args = append(args, ShareKindAssetCat, AssetScopeRef(d.ProjectID, d.Category))
	}
	rows, err := s.db.QueryContext(ctx, q+strings.Join(conds, " OR ")+`) ORDER BY sh.created_at`, args...)
	if err != nil {
		return info, err
	}
	defer rows.Close()
	info.Shares = []AssetShareUse{}
	for rows.Next() {
		var u AssetShareUse
		if err := rows.Scan(&u.ID, &u.Name, &u.Via, &u.Ref); err != nil {
			return info, err
		}
		if u.Via == ShareKindAsset {
			u.Ref = d.Code
			if slices.ContainsFunc(info.Shares, func(x AssetShareUse) bool { return x.ID == u.ID && x.Via == u.Via }) {
				continue // 素材和它的变体都单独挂在同一个分享里，只算一次
			}
		} else if _, name, ok := SplitAssetScopeRef(u.Ref); ok {
			u.Ref = name
		}
		info.Shares = append(info.Shares, u)
	}
	return info, rows.Err()
}

// DeleteAsset 真删除一件素材（推错的条目）：连同变体、全部版本、审核记录、附件和单独挂着它的分享项；
// 被它取代的旧条目取消取代标记。文件本身留在附件目录。返回删掉的素材（含变体）。
func (s *Store) DeleteAsset(ctx context.Context, ref string) ([]AssetRef, error) {
	d, err := s.GetAsset(ctx, ref)
	if err != nil {
		return nil, err
	}
	gone := []AssetRef{{ID: d.ID, Code: d.Code, UID: d.UID, Key: d.Key, Batch: d.Batch, Variant: d.Variant, Status: d.Status}}
	gone = append(gone, d.Variants...)
	err = s.tx(ctx, func(t *sql.Tx) error {
		for _, g := range gone {
			for _, q := range []string{
				`DELETE FROM share_items WHERE kind = 'asset' AND ref = ?`,
				`UPDATE assets SET superseded_by = '', superseded_at = '' WHERE superseded_by = ?`,
				`UPDATE assets SET parent_id = '' WHERE parent_id = ?`,
				`DELETE FROM assets WHERE id = ?`,
			} {
				if _, err := t.ExecContext(ctx, q, g.ID); err != nil {
					return err
				}
			}
		}
		return nil
	})
	return gone, err
}

// supersededAt：取代标记的时间，没有时为 nil。
func supersededAt(s string) *time.Time {
	if s == "" {
		return nil
	}
	t := parseTS(s)
	return &t
}
