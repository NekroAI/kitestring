package hub

// 组合预览（R-407）：素材详情里按版本 meta 的 display.attach / display.refs 找到同项目里对应素材的 GLB，
// 网页查看器据此挂上挂件、在旁边放比例参照。找不到时照样列出并标 missing，查看器给提示、不报错。
// 分享页：被挂的素材不一定在分享范围里，访客看得到的素材版本里引用到的 GLB 也放行（只能看，不算原文件下载）。

import (
	"context"
	"strings"

	"github.com/NekroAI/kitestring/internal/protocol"
	"github.com/NekroAI/kitestring/internal/store"
)

// composeResolver 在一个项目里按素材 id 找 GLB，同一次请求里缓存。
type composeResolver struct {
	h       *Hub
	project string
	cache   map[string]store.AssetCompose
}

func (r *composeResolver) find(ctx context.Context, target string) store.AssetCompose {
	if c, ok := r.cache[target]; ok {
		return c
	}
	var c store.AssetCompose
	d, err := r.h.store.FindAsset(ctx, r.project, target)
	switch {
	case err != nil || d.ProjectID != r.project:
		c.Missing = "asset"
	case d.Latest == nil || d.Kind != protocol.AssetPart || d.Latest.File == nil:
		c.Code, c.Missing = d.Code, "glb"
	default:
		f := *d.Latest.File
		c.Code, c.Title, c.File = d.Code, d.Latest.Name, &f
	}
	r.cache[target] = c
	return c
}

// resolve 把一版的配置解析成网页要的列表。
func (r *composeResolver) resolve(ctx context.Context, meta []byte) []store.AssetCompose {
	cfg := protocol.MetaCompose(meta)
	if cfg.Empty() {
		return nil
	}
	out := make([]store.AssetCompose, 0, len(cfg.Attach)+len(cfg.Refs))
	for _, a := range cfg.Attach {
		c := r.find(ctx, protocol.ComposeTarget(a.Asset, a.Variant))
		c.Kind, c.Name, c.Socket, c.Asset, c.Variant, c.Clips = "attach", a.Key(), a.Socket, a.Asset, a.Variant, a.Clips
		out = append(out, c)
	}
	for _, x := range cfg.Refs {
		c := r.find(ctx, protocol.ComposeTarget(x.Asset, x.Variant))
		c.Kind, c.Asset, c.Variant, c.Offset = "ref", x.Asset, x.Variant, x.Offset
		out = append(out, c)
	}
	return out
}

// fillCompose 给素材详情的每一版填上组合预览。
func (h *Hub) fillCompose(ctx context.Context, d *store.AssetDetail) {
	r := &composeResolver{h: h, project: d.ProjectID, cache: map[string]store.AssetCompose{}}
	for i := range d.Versions {
		d.Versions[i].Compose = r.resolve(ctx, d.Versions[i].Meta)
	}
	if len(d.Versions) > 0 {
		d.Latest = &d.Versions[0]
	}
}

// guestComposeFile：文件是不是访客看得到的某一版里挂件 / 参照的 GLB（按现在的解析结果核对，只认被挂素材的最新一版）。
func (h *Hub) guestComposeFile(ctx context.Context, g *guestCtx, fileID string) (bool, error) {
	uses, err := h.store.AssetFileUses(ctx, fileID)
	if err != nil {
		return false, err
	}
	for _, u := range uses {
		if !u.Main {
			continue
		}
		t, err := h.store.GetAsset(ctx, u.AssetID)
		if err != nil || t.UID == "" || t.Latest == nil || t.Latest.File == nil || t.Latest.File.ID != fileID {
			continue
		}
		base, _, _ := strings.Cut(t.UID, protocol.VariantSep)
		ms, err := h.store.AssetVersionsMentioning(ctx, t.ProjectID, base)
		if err != nil {
			return false, err
		}
		for _, m := range ms {
			if !g.scope.AssetVisible(m.AssetID, m.Version) {
				continue
			}
			cfg := protocol.MetaCompose(m.Meta)
			for _, a := range cfg.Attach {
				if protocol.ComposeTarget(a.Asset, a.Variant) == t.UID {
					return true, nil
				}
			}
			for _, x := range cfg.Refs {
				if protocol.ComposeTarget(x.Asset, x.Variant) == t.UID {
					return true, nil
				}
			}
		}
	}
	return false, nil
}
