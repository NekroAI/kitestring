package hub

import (
	"context"
	"fmt"
	"net/http"
	"path"
	"strings"
	"unicode/utf8"

	"github.com/danielgtaylor/huma/v2"

	"github.com/NekroAI/kitestring/internal/roles"
	"github.com/NekroAI/kitestring/internal/store"
)

// 会话自报名字和说明（R-416）：任一身份接入时可加 --name / --note（ks role <身份> --name … --note …），
// 任务换了用 ks session name 改；和模型生成的标题（此刻在做什么，每轮会变）分开存、不被覆盖。
// 没自报时，会话第一次归到项目就定下默认名「目录名-工具」，之后不再变。
// 名字在本项目没结束的会话里不重名：撞了自动加后缀 -2、-3。团队消息的 --to 除身份、会话键外也认名字。

const (
	// NameMax：名字的上限（字）。
	NameMax = 20
	// NoteMax：说明的上限（字）。
	NoteMax = 40
	// defaultBaseMax：默认名里目录名部分最多取几个字（默认名不受 NameMax 限制，目录名常见长度都放得下）。
	defaultBaseMax = 24
)

// cleanName 校验并整理自报的名字：去掉首尾空白，不换行、不含冒号（冒号是会话键的写法）、不和身份同名，20 字以内。
func cleanName(name string) (string, error) {
	name = strings.Join(strings.Fields(name), " ")
	switch {
	case name == "":
		return "", huma.Error400BadRequest("名字不能为空")
	case utf8.RuneCountInString(name) > NameMax:
		return "", huma.Error400BadRequest(fmt.Sprintf("名字最多 %d 字，收到 %d 字", NameMax, utf8.RuneCountInString(name)))
	case strings.ContainsAny(name, ":："):
		return "", huma.Error400BadRequest("名字不能含冒号：冒号是会话键（tool:id）的写法")
	}
	if _, ok := roles.Resolve(name); ok {
		return "", huma.Error400BadRequest("名字「" + name + "」和身份重名，--to 会按身份理解；换一个名字")
	}
	return name, nil
}

// cleanNote 校验并整理说明：一行，40 字以内；可以为空（清除说明）。
func cleanNote(note string) (string, error) {
	note = strings.Join(strings.Fields(note), " ")
	if n := utf8.RuneCountInString(note); n > NoteMax {
		return "", huma.Error400BadRequest(fmt.Sprintf("说明最多 %d 字，收到 %d 字", NoteMax, n))
	}
	return note, nil
}

// defaultName 是没自报名字时的默认名：工作目录名-工具（如 kitestring-claude）；目录名取不到时用项目名。
func defaultName(x store.Session) string {
	cwd := strings.TrimRight(strings.ReplaceAll(x.Cwd, `\`, "/"), "/")
	base := path.Base(cwd)
	if cwd == "" || base == "." || base == "/" {
		base = x.ProjectName
	}
	base = strings.Join(strings.Fields(strings.NewReplacer(":", "-", "：", "-").Replace(base)), "-")
	if r := []rune(base); len(r) > defaultBaseMax {
		base = string(r[:defaultBaseMax])
	}
	if base == "" {
		return x.Tool
	}
	return base + "-" + x.Tool
}

// uniqueName 在项目 projectID 没结束的会话里给 key 找一个不重名的名字：base 被别的会话用了就加 -2、-3……
func (h *Hub) uniqueName(ctx context.Context, projectID, key, base string) string {
	if projectID == "" {
		return base
	}
	list, err := h.store.LiveSessions(ctx, projectID)
	if err != nil {
		return base
	}
	taken := map[string]bool{}
	for _, x := range list {
		if x.Key != key && x.Name != "" {
			taken[strings.ToLower(x.Name)] = true
		}
	}
	name := base
	for i := 2; taken[strings.ToLower(name)]; i++ {
		name = fmt.Sprintf("%s-%d", base, i)
	}
	return name
}

// ensureName：会话归到项目、还没有名字时定下默认名（只定一次，之后不再变）。
func (h *Hub) ensureName(ctx context.Context, x store.Session) {
	if x.Name != "" || x.ProjectID == "" || x.Background {
		return
	}
	name := h.uniqueName(ctx, x.ProjectID, x.Key, defaultName(x))
	if err := h.store.SetSessionName(ctx, x.Key, &name, nil); err != nil {
		h.log.Warn("记录默认会话名失败", "err", err)
	}
}

// backfillNames：Hub 启动时给没结束、还没名字的会话补上默认名（R-416 上线前就在的会话不必等下一条事件），
// 先来的（最近活跃的）先拿不带后缀的名字。
func (h *Hub) backfillNames(ctx context.Context) {
	ps, err := h.store.ListProjects(ctx)
	if err != nil {
		return
	}
	for _, p := range ps {
		list, err := h.store.LiveSessions(ctx, p.ID)
		if err != nil {
			continue
		}
		for _, x := range list {
			if x.Name == "" {
				h.ensureName(ctx, x)
			}
		}
	}
}

// setName 记下会话自报的名字和说明；nil 表示不改。返回最后记下的名字（重名时带了后缀）和是否加了后缀。
func (h *Hub) setName(ctx context.Context, key string, name, note *string) (string, bool, error) {
	x, err := h.store.GetSession(ctx, key)
	if err != nil {
		return "", false, err
	}
	suffixed := false
	if name != nil {
		n, err := cleanName(*name)
		if err != nil {
			return "", false, err
		}
		final := h.uniqueName(ctx, x.ProjectID, key, n)
		suffixed = final != n
		name = &final
	}
	if note != nil {
		n, err := cleanNote(*note)
		if err != nil {
			return "", false, err
		}
		note = &n
	}
	if name == nil && note == nil {
		return x.Name, false, nil
	}
	if err := h.store.SetSessionName(ctx, key, name, note); err != nil {
		return "", false, err
	}
	h.sessionChanged(key)
	if name != nil {
		return *name, suffixed, nil
	}
	return x.Name, false, nil
}

// byName 在项目 projectID 没结束的会话里按名字找（不分大小写），不含 except。
func (h *Hub) byName(ctx context.Context, projectID, name, except string) []store.Session {
	list, err := h.store.LiveSessions(ctx, projectID)
	if err != nil {
		return nil
	}
	var out []store.Session
	for _, x := range list {
		if x.Key != except && x.Name != "" && strings.EqualFold(x.Name, name) {
			out = append(out, x)
		}
	}
	return out
}

// shownTitle 是列表、送达状态里称呼会话的那几个字：名字优先，没有时用模型生成的标题，再没有用最近一条消息。
func shownTitle(x store.Session) string {
	if x.Name != "" {
		return x.Name
	}
	if x.Title != "" {
		return x.Title
	}
	return x.LastPrompt
}

// whoIs 是团队消息里称呼会话的方式（R-416）：「名字」（身份 · 说明），没有身份、说明的部分不写。
func whoIs(x store.Session) string {
	var tags []string
	if n := roles.Name(x.Role); n != "" {
		tags = append(tags, n)
	}
	if x.Note != "" {
		tags = append(tags, x.Note)
	}
	s := "「" + brief(x.DisplayName(), 30) + "」"
	if len(tags) > 0 {
		s += "（" + strings.Join(tags, " · ") + "）"
	}
	return s
}

// nameNote 是 ks role / ks session name 输出里那一句：本会话现在叫什么、别的会话怎么找到它。
func nameNote(x store.Session, suffixed bool) string {
	if x.Name == "" {
		return ""
	}
	s := "[Kitestring] 本会话名「" + x.Name + "」"
	if x.Note != "" {
		s += "，说明：" + x.Note
	}
	if suffixed {
		s += "（本项目已有同名会话，已自动加后缀）"
	}
	return s + "；别的会话用 ks team tell --to " + x.Name + " 给你发消息。\n"
}

type nameOut struct {
	Body struct {
		SessionKey string `json:"session_key"`
		Name       string `json:"name" doc:"最后记下的名字：本项目已有同名会话时自动加了后缀 -2、-3"`
		Note       string `json:"note"`
		Suffixed   bool   `json:"suffixed" doc:"名字和本项目没结束的会话重名，已自动加后缀"`
		Message    string `json:"message"`
	}
}

func (h *Hub) registerAgentName(api huma.API) {
	huma.Register(api, huma.Operation{OperationID: "agent-get-name", Method: http.MethodGet, Path: "/api/agent/sessions/name", Tags: []string{"agent"},
		Summary: "会话现在的名字和说明（R-416）"},
		func(ctx context.Context, in *struct {
			SessionKey string `query:"session_key" minLength:"1"`
		}) (*nameOut, error) {
			x, err := h.store.GetSession(ctx, in.SessionKey)
			if err != nil {
				return nil, apiErr(err)
			}
			out := &nameOut{}
			out.Body.SessionKey, out.Body.Name, out.Body.Note = x.Key, x.Name, x.Note
			out.Body.Message = strings.TrimSpace(nameNote(x, false))
			return out, nil
		})
	huma.Register(api, huma.Operation{OperationID: "agent-set-name", Method: http.MethodPost, Path: "/api/agent/sessions/name", Tags: []string{"agent"},
		Summary: "会话自报名字和说明（R-416）：和模型生成的标题分开存，不被覆盖",
		Description: fmt.Sprintf("name 名字（%d 字以内，不含冒号，不和身份同名；和本项目没结束的会话重名时自动加后缀 -2），note 一句说明（%d 字以内，空字符串清除）；不填的字段不改。"+
			"别的会话用 team_tell 的 to 填名字就能发给你；ks team sessions、团队消息的发送方、工作台泳道都按名字显示。", NameMax, NoteMax)},
		func(ctx context.Context, in *struct {
			Body struct {
				SessionKey string  `json:"session_key" minLength:"1"`
				Name       *string `json:"name,omitempty" doc:"名字；不填不改"`
				Note       *string `json:"note,omitempty" doc:"一句说明；不填不改，空字符串清除"`
			}
		}) (*nameOut, error) {
			if in.Body.Name == nil && in.Body.Note == nil {
				return nil, huma.Error400BadRequest("请写名字（name）或说明（note）")
			}
			_, suffixed, err := h.setName(ctx, in.Body.SessionKey, in.Body.Name, in.Body.Note)
			if err != nil {
				return nil, apiErr(err)
			}
			x, err := h.store.GetSession(ctx, in.Body.SessionKey)
			if err != nil {
				return nil, apiErr(err)
			}
			out := &nameOut{}
			out.Body.SessionKey, out.Body.Name, out.Body.Note, out.Body.Suffixed = x.Key, x.Name, x.Note, suffixed
			out.Body.Message = strings.TrimSpace(nameNote(x, suffixed))
			return out, nil
		})
}
