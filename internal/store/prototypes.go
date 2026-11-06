package store

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"errors"
	"net/url"
	"strings"
	"time"

	"github.com/NekroAI/kitestring/internal/blocks"
	"github.com/NekroAI/kitestring/internal/protocol"
)

// 可交互原型（R-197）：zip 原样存成附件，这里记版本、入口、封面和访问令牌。
// 令牌每个版本一个、随机长串，是打开原型的唯一凭据（隔离路径 /p/<令牌>/… 不认登录 cookie）。

// Prototype 是原型的快照（证据、决策里存的就是它）。
type Prototype = blocks.Prototype

// PrototypeRow 是原型表的一行。
type PrototypeRow struct {
	ID            string
	Lineage       string
	Version       int
	RequirementID string
	Title         string
	Entry         string
	FileID        string
	CoverID       string
	Token         string
	Files         int
	Size          int64
	SessionKey    string
	CreatedAt     time.Time
}

// NewPrototype 是登记一个原型版本的参数。
type NewPrototype struct {
	RequirementID string
	Title         string
	Entry         string
	FileID        string
	CoverID       string
	Files         int
	Size          int64
	SessionKey    string
	VersionOf     string // 指定是哪个原型的新版本（原型 ID）；不写时同一需求下同标题即为新版本
}

// ProtoURL 是原型某个路径的隔离地址：/p/<令牌>/<路径>（逐段转义）。
func ProtoURL(token, path string) string {
	segs := strings.Split(path, "/")
	for i, s := range segs {
		segs[i] = url.PathEscape(s)
	}
	return "/p/" + token + "/" + strings.Join(segs, "/")
}

// ProtoDownloadURL 是下载整包的地址。
func ProtoDownloadURL(token string) string { return "/p/" + token + "/?download" }

// Snapshot 转成证据 / 决策里存的快照（不含历史版本）。
func (s *Store) protoSnapshot(ctx context.Context, r PrototypeRow) Prototype {
	p := Prototype{ID: r.ID, Lineage: r.Lineage, Version: r.Version, Title: r.Title, Entry: r.Entry, Files: r.Files, Size: r.Size,
		URL: ProtoURL(r.Token, r.Entry), DownloadURL: ProtoDownloadURL(r.Token), CreatedAt: r.CreatedAt}
	if r.CoverID != "" {
		if f, err := s.GetFile(ctx, r.CoverID); err == nil {
			bf := blocks.File{ID: f.ID, Name: f.Name, Mime: f.Mime, Size: f.Size}
			p.Cover = &bf
		}
	}
	return p
}

const protoCols = `id, lineage, version, requirement_id, title, entry, file_id, cover_id, token, files, size, session_key, created_at`

func scanProto(row interface{ Scan(...any) error }) (PrototypeRow, error) {
	var r PrototypeRow
	var created string
	err := row.Scan(&r.ID, &r.Lineage, &r.Version, &r.RequirementID, &r.Title, &r.Entry, &r.FileID, &r.CoverID, &r.Token, &r.Files, &r.Size, &r.SessionKey, &created)
	r.CreatedAt = parseTS(created)
	return r, err
}

// CreatePrototype 登记一个原型版本，返回快照。
func (s *Store) CreatePrototype(ctx context.Context, n NewPrototype) (Prototype, error) {
	b := make([]byte, 24)
	if _, err := rand.Read(b); err != nil {
		return Prototype{}, err
	}
	r := PrototypeRow{ID: protocol.NewID(), RequirementID: n.RequirementID, Title: n.Title, Entry: n.Entry, FileID: n.FileID, CoverID: n.CoverID,
		Token: hex.EncodeToString(b), Files: n.Files, Size: n.Size, SessionKey: n.SessionKey, CreatedAt: time.Now()}
	err := s.tx(ctx, func(t *sql.Tx) error {
		lineage := ""
		if n.VersionOf != "" {
			if err := t.QueryRowContext(ctx, `SELECT lineage FROM prototypes WHERE id = ? OR lineage = ? LIMIT 1`, n.VersionOf, n.VersionOf).Scan(&lineage); err != nil {
				if errors.Is(err, sql.ErrNoRows) {
					return errors.New("version_of 指定的原型不存在：" + n.VersionOf)
				}
				return err
			}
		} else if n.RequirementID != "" {
			rows, err := t.QueryContext(ctx, `SELECT lineage, title FROM prototypes WHERE requirement_id = ? ORDER BY created_at DESC`, n.RequirementID)
			if err != nil {
				return err
			}
			want := NormTitle(n.Title)
			for rows.Next() {
				var l, title string
				if err := rows.Scan(&l, &title); err != nil {
					rows.Close()
					return err
				}
				if NormTitle(title) == want {
					lineage = l
					break
				}
			}
			rows.Close()
		}
		if lineage == "" {
			lineage = r.ID
		}
		r.Lineage = lineage
		if err := t.QueryRowContext(ctx, `SELECT COALESCE(MAX(version), 0) + 1 FROM prototypes WHERE lineage = ?`, lineage).Scan(&r.Version); err != nil {
			return err
		}
		_, err := t.ExecContext(ctx, `INSERT INTO prototypes(`+protoCols+`) VALUES(?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			r.ID, r.Lineage, r.Version, r.RequirementID, r.Title, r.Entry, r.FileID, r.CoverID, r.Token, r.Files, r.Size, r.SessionKey, ts(r.CreatedAt))
		return err
	})
	if err != nil {
		return Prototype{}, err
	}
	return s.protoSnapshot(ctx, r), nil
}

// GetPrototype 按 ID 取原型。
func (s *Store) GetPrototype(ctx context.Context, id string) (PrototypeRow, error) {
	r, err := scanProto(s.db.QueryRowContext(ctx, `SELECT `+protoCols+` FROM prototypes WHERE id = ?`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return r, ErrNotFound
	}
	return r, err
}

// PrototypeByToken 按访问令牌取原型。
func (s *Store) PrototypeByToken(ctx context.Context, token string) (PrototypeRow, error) {
	if token == "" {
		return PrototypeRow{}, ErrNotFound
	}
	r, err := scanProto(s.db.QueryRowContext(ctx, `SELECT `+protoCols+` FROM prototypes WHERE token = ?`, token))
	if errors.Is(err, sql.ErrNoRows) {
		return r, ErrNotFound
	}
	return r, err
}

// PrototypeSnapshot 按 ID 取快照（交证据、提决策时用）。
func (s *Store) PrototypeSnapshot(ctx context.Context, id string) (Prototype, error) {
	r, err := s.GetPrototype(ctx, id)
	if err != nil {
		return Prototype{}, err
	}
	return s.protoSnapshot(ctx, r), nil
}

// ResolvePrototypes 把原型 ID 列表解析成快照，忽略不存在的和重复的。
func (s *Store) ResolvePrototypes(ctx context.Context, ids []string) ([]Prototype, error) {
	out := []Prototype{}
	seen := map[string]bool{}
	for _, id := range ids {
		id = strings.TrimSpace(id)
		if id == "" || seen[id] {
			continue
		}
		seen[id] = true
		p, err := s.PrototypeSnapshot(ctx, id)
		if errors.Is(err, ErrNotFound) {
			continue
		}
		if err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, nil
}

// latestPrototypes 把证据里的原型换成同一原型的最新版，并补上全部版本（新的在前）：需求抽屉显示最新版、可切历史版本。
func (s *Store) latestPrototypes(ctx context.Context, ps []Prototype) []Prototype {
	out := make([]Prototype, 0, len(ps))
	seen := map[string]bool{}
	for _, p := range ps {
		lineage := p.Lineage
		if lineage == "" {
			lineage = p.ID
		}
		if seen[lineage] {
			continue
		}
		seen[lineage] = true
		rows, err := s.db.QueryContext(ctx, `SELECT `+protoCols+` FROM prototypes WHERE lineage = ? ORDER BY version DESC`, lineage)
		if err != nil {
			out = append(out, p)
			continue
		}
		var vs []PrototypeRow
		for rows.Next() {
			if r, err := scanProto(rows); err == nil {
				vs = append(vs, r)
			}
		}
		rows.Close()
		if len(vs) == 0 {
			out = append(out, p)
			continue
		}
		latest := s.protoSnapshot(ctx, vs[0])
		for _, v := range vs {
			latest.Versions = append(latest.Versions, blocks.PrototypeVersion{ID: v.ID, Version: v.Version, Files: v.Files, Size: v.Size,
				URL: ProtoURL(v.Token, v.Entry), DownloadURL: ProtoDownloadURL(v.Token), CreatedAt: v.CreatedAt})
		}
		out = append(out, latest)
	}
	return out
}

// fillEvidencePrototypes 给一组证据补最新版原型（在读完证据行之后调用，连接只有一个）。
func (s *Store) fillEvidencePrototypes(ctx context.Context, es []Evidence) {
	for i := range es {
		if len(es[i].Prototypes) > 0 {
			es[i].Prototypes = s.latestPrototypes(ctx, es[i].Prototypes)
		}
	}
}

func protosJSON(ps []Prototype) string {
	if len(ps) == 0 {
		return ""
	}
	return blocks.Encode(ps)
}

func parseProtos(s string) []Prototype {
	if p := blocks.Decode[[]Prototype](s); p != nil {
		return *p
	}
	return nil
}

// AdoptPrototypes：原型交上来时还不知道挂在哪条需求上（例如决策自动挂需求），挂上后补记，访客页按需求判断能不能看。
func (s *Store) AdoptPrototypes(ctx context.Context, ids []string, reqID string) error {
	if reqID == "" {
		return nil
	}
	for _, id := range ids {
		if id == "" {
			continue
		}
		if _, err := s.db.ExecContext(ctx, `UPDATE prototypes SET requirement_id = ? WHERE id = ? AND requirement_id = ''`, reqID, id); err != nil {
			return err
		}
	}
	return nil
}
