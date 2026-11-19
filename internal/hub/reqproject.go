package hub

import (
	"context"
	"strings"

	"github.com/danielgtaylor/huma/v2"

	"github.com/NekroAI/kitestring/internal/store"
)

// targetProject 按项目名、git remote 或项目 ID 找到要记需求的项目（R-358）。
// 找不到、或对应多个项目时报错并列出可选项目，不退回会话所在项目。
// 范围沿用设备令牌现有的权限：Hub 上任一项目都能记（D-1744）。
func (h *Hub) targetProject(ctx context.Context, q string) (store.Project, error) {
	return h.findProject(ctx, q, "未记下需求")
}

// findProject 同 targetProject，tail 是找不到时报错的结尾（「未记下需求」「消息没有送出」：R-379 团队消息跨项目也用它）。
func (h *Hub) findProject(ctx context.Context, q, tail string) (store.Project, error) {
	q = strings.TrimSpace(q)
	all, err := h.store.ListProjects(ctx)
	if err != nil {
		return store.Project{}, err
	}
	for _, p := range all {
		if p.ID == q {
			return p, nil
		}
	}
	remote := store.NormalizeRemote(q)
	var hits []store.Project
	for _, p := range all {
		if strings.EqualFold(p.Name, q) || (p.Remote != "" && remote != "" && (p.Remote == remote || strings.HasSuffix(p.Remote, "/"+remote))) {
			hits = append(hits, p)
		}
	}
	if len(hits) > 1 { // 重名时先排除隐藏的（误识别的目录），剩一个就用它
		var shown []store.Project
		for _, p := range hits {
			if !p.Hidden {
				shown = append(shown, p)
			}
		}
		if len(shown) > 0 {
			hits = shown
		}
	}
	switch len(hits) {
	case 1:
		return hits[0], nil
	case 0:
		var shown []store.Project
		for _, p := range all {
			if !p.Hidden {
				shown = append(shown, p)
			}
		}
		if len(shown) == 0 {
			return store.Project{}, huma.Error400BadRequest("找不到项目「" + q + "」，Hub 上还没有项目。" + tail)
		}
		return store.Project{}, huma.Error400BadRequest("找不到项目「" + q + "」。可选项目：" + projectChoices(shown, false) + "。按项目名或 git remote 指定。" + tail)
	default:
		return store.Project{}, huma.Error400BadRequest("「" + q + "」对应多个项目：" + projectChoices(hits, true) + "。改用 git remote 或项目 ID 指定。" + tail)
	}
}

// projectChoices 把项目写成「名称」（remote）的列表；withID 时附上项目 ID，重名又没有 remote 时也能区分。
func projectChoices(ps []store.Project, withID bool) string {
	parts := make([]string, 0, len(ps))
	for _, p := range ps {
		s := "「" + p.Name + "」"
		var meta []string
		if p.Remote != "" {
			meta = append(meta, p.Remote)
		}
		if withID {
			meta = append(meta, "ID "+p.ID)
		}
		if len(meta) > 0 {
			s += "（" + strings.Join(meta, "，") + "）"
		}
		parts = append(parts, s)
	}
	return strings.Join(parts, "、")
}
