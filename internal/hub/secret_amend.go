package hub

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/danielgtaylor/huma/v2"

	"github.com/NekroAI/kitestring/internal/store"
)

// 修改授权申请（R-320）：会话对已有凭据（生效中、已失效、已撤回）申请改授权范围——允许的命令增删改、有效期、项目、写入磁盘，
// 写明理由。走和凭据申请同一条请求类审批（request_kind = secret）、同一个抽屉：抽屉里显示改前 / 改后，你可以再调整后「保存并授权」
// 或「拒绝…」，结果送回提申请的会话。
//   - 申请里只有授权范围，没有值：不能借它改值，也拿不到值（答复里只有授权范围）。
//   - 批准前凭据的授权原样不动（改后的范围只记在 secrets.request 里），旧授权照常生效。
//   - 只有凭据授权给了会话所在项目时才能申请（不能借申请去动别的项目的凭据）。
//   - 时间线：会话申请时记一条 amend（理由），批准时记 edit / regrant，带申请的会话和改了什么。
//   - 替换值（R-325）：申请里可以写要替换哪几个值（环境变量名和新值的获取方式），也可以只替换值、不改授权范围。申请里仍然没有值：
//     抽屉里这几个值显示填写框，用户填好新值才能保存并授权。批准后新值生效、旧值作废（不能再取用），凭据名、取用记录、时间线不变；
//     时间线记「替换了哪些值」（只有显示名）。旧值留在脱敏器里（secrets.retired），已存记录里的旧值继续隐去。

// secretKeepExpiry：保存授权时的有效期取这个值 = 到期时间不变（只在生效中的凭据上可用）。
const secretKeepExpiry = -1

type secretAmendIn struct {
	SessionKey    string            `json:"session_key" minLength:"1"`
	Name          string            `json:"name" minLength:"1" doc:"要改授权的凭据名"`
	Reason        string            `json:"reason,omitempty" doc:"理由（必填）：为什么要改（例如哪条命令被拒、为什么需要），用户在申请里看到"`
	Requirement   string            `json:"requirement,omitempty" doc:"关联需求 R-x"`
	Allowed       []string          `json:"allowed,omitempty" doc:"改后允许的命令（整份替换）；不写 = 在原来的基础上用 add_allowed / remove_allowed 增删"`
	AnyCommand    bool              `json:"any_command,omitempty" doc:"改成不限命令"`
	AddAllowed    []string          `json:"add_allowed,omitempty" doc:"加上的命令"`
	RemoveAllowed []string          `json:"remove_allowed,omitempty" doc:"去掉的命令（要和原来的写法一致，空白不计）"`
	Days          *int              `json:"days,omitempty" doc:"改后的有效期（天），从批准时算起；0 = 长期；不写 = 生效中的到期时间不变，已失效 / 已撤回的按 7 天"`
	Projects      []string          `json:"projects,omitempty" doc:"改后的授权项目（项目名或 ID，整份替换）；不写 = 不变"`
	Disk          *bool             `json:"disk,omitempty" doc:"改后是否允许写入磁盘；不写 = 不变"`
	Replace       []secretReplaceIn `json:"replace,omitempty" doc:"要替换的值（R-325）：环境变量名和新值的获取方式；用户在抽屉里填新值，申请里没有值。可以只替换值、不改授权范围"`
}

type secretReplaceIn struct {
	Env string `json:"env" doc:"要替换的值的环境变量名（凭据里已有的）"`
	How string `json:"how,omitempty" doc:"新值的获取方式：按服务写清步骤；不写 = 沿用原来的获取方式"`
}

// amendReplace 核对要替换的值：环境变量名是凭据里已有的、不重复；获取方式里的已知值隐去。
func (h *Hub) amendReplace(x store.Secret, in []secretReplaceIn, known map[string]string) ([]store.SecretReplace, error) {
	var out []store.SecretReplace
	for _, r := range in {
		env := strings.TrimSpace(r.Env)
		if env == "" {
			continue
		}
		if !slices.ContainsFunc(x.Fields, func(f store.SecretField) bool { return f.Env == env }) {
			return nil, fmt.Errorf("凭据「%s」没有 %s 这个值；现有的：%s。要加新的值用 ks secret request", x.Name, env, secretEnvs(x))
		}
		if slices.ContainsFunc(out, func(o store.SecretReplace) bool { return o.Env == env }) {
			return nil, fmt.Errorf("要替换的值 %s 重复", env)
		}
		out = append(out, store.SecretReplace{Env: env, How: h.redactText(strings.TrimSpace(r.How), known)})
	}
	return out, nil
}

// replaceLabels 是要替换的值的显示名，按凭据字段的顺序。
func replaceLabels(x store.Secret, rs []store.SecretReplace) []string {
	var out []string
	for _, f := range x.Fields {
		if slices.ContainsFunc(rs, func(r store.SecretReplace) bool { return r.Env == f.Env }) {
			out = append(out, f.Label)
		}
	}
	return out
}

// amendTitle 是修改授权申请的标题（「待处理」里的一行）：只替换值、只改授权，或两者都有。
func amendTitle(name string, replace bool, scope bool) string {
	switch {
	case replace && scope:
		return name + "：替换值并修改授权"
	case replace:
		return name + "：替换值"
	}
	return name + "：修改授权"
}

// amendScope 是一份授权范围（改前或改后），用来比较、写改动说明。
type amendScope struct {
	Projects []string // 项目 ID
	Names    []string // 与 Projects 对应的项目名
	Days     int      // 有效期（天），secretKeepExpiry = 到期时间不变
	Expires  *time.Time
	Allowed  []string
	Disk     bool
}

func scopeOf(x store.Secret) amendScope {
	return amendScope{Projects: slices.Clone(x.Projects), Names: slices.Clone(x.ProjectNames), Days: secretKeepExpiry, Expires: x.ExpiresAt, Allowed: slices.Clone(x.Allowed), Disk: x.Disk}
}

func quoteAll(xs []string) string {
	q := make([]string, len(xs))
	for i, x := range xs {
		q[i] = "「" + x + "」"
	}
	return strings.Join(q, "、")
}

func expiryText(days int, at *time.Time) string {
	switch {
	case days == secretKeepExpiry && at == nil:
		return "长期"
	case days == secretKeepExpiry:
		return "至 " + at.Local().Format("01-02 15:04")
	case days == 0:
		return "长期"
	}
	return fmt.Sprintf("%d 天", days)
}

// amendChanges 写改前 → 改后的每一项改动（没改的不写）：项目、有效期、允许的命令、写入磁盘。
func amendChanges(before, after amendScope) []string {
	var out []string
	if !sameSet(before.Projects, after.Projects) {
		out = append(out, "项目："+strings.Join(before.Names, "、")+" → "+strings.Join(after.Names, "、"))
	}
	if after.Days != secretKeepExpiry {
		out = append(out, "有效期："+expiryText(secretKeepExpiry, before.Expires)+" → "+expiryText(after.Days, nil))
	}
	if !slices.Equal(before.Allowed, after.Allowed) {
		switch {
		case len(after.Allowed) == 0:
			out = append(out, "允许的命令：改为不限命令")
		case len(before.Allowed) == 0:
			out = append(out, "允许的命令：不限命令 → 只允许"+quoteAll(after.Allowed))
		default:
			var add, del []string
			for _, c := range after.Allowed {
				if !slices.Contains(before.Allowed, c) {
					add = append(add, c)
				}
			}
			for _, c := range before.Allowed {
				if !slices.Contains(after.Allowed, c) {
					del = append(del, c)
				}
			}
			var parts []string
			if len(add) > 0 {
				parts = append(parts, "加上"+quoteAll(add))
			}
			if len(del) > 0 {
				parts = append(parts, "去掉"+quoteAll(del))
			}
			if len(parts) == 0 {
				parts = append(parts, "调整了顺序")
			}
			out = append(out, "允许的命令："+strings.Join(parts, "，"))
		}
	}
	if before.Disk != after.Disk {
		on := map[bool]string{true: "允许", false: "不允许"}
		out = append(out, "写入磁盘："+on[before.Disk]+" → "+on[after.Disk])
	}
	return out
}

func sameSet(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for _, x := range a {
		if !slices.Contains(b, x) {
			return false
		}
	}
	return true
}

// resolveProjects 把项目名或 ID 换成 ID 和名字；找不到的报错。
func (h *Hub) resolveProjects(ctx context.Context, refs []string) ([]string, []string, error) {
	all, err := h.store.ListProjects(ctx)
	if err != nil {
		return nil, nil, err
	}
	var ids, names []string
	for _, r := range refs {
		r = strings.TrimSpace(r)
		if r == "" {
			continue
		}
		i := slices.IndexFunc(all, func(p store.Project) bool { return p.ID == r })
		if i < 0 {
			i = slices.IndexFunc(all, func(p store.Project) bool { return strings.EqualFold(p.Name, r) })
		}
		if i < 0 {
			return nil, nil, fmt.Errorf("项目「%s」不存在", r)
		}
		if !slices.Contains(ids, all[i].ID) {
			ids, names = append(ids, all[i].ID), append(names, all[i].Name)
		}
	}
	if len(ids) == 0 {
		return nil, nil, errors.New("至少保留一个项目")
	}
	return ids, names, nil
}

// amendAllowed 算改后的允许的命令：整份替换（allowed）或不限命令，再增删。
func amendAllowed(cur []string, b secretAmendIn) ([]string, error) {
	out := slices.Clone(cur)
	switch {
	case b.AnyCommand:
		if len(b.Allowed) > 0 || len(b.AddAllowed) > 0 {
			return nil, errors.New("改成不限命令时不用再写允许的命令")
		}
		return []string{}, nil
	case b.Allowed != nil:
		out = normAllowed(b.Allowed)
	}
	for _, c := range normAllowed(b.RemoveAllowed) {
		i := slices.Index(out, c)
		if i < 0 {
			return nil, fmt.Errorf("允许的命令里没有「%s」；现在允许的：%s", c, allowedText(cur))
		}
		out = slices.Delete(out, i, i+1)
	}
	for _, c := range normAllowed(b.AddAllowed) {
		if !slices.Contains(out, c) {
			out = append(out, c)
		}
	}
	if len(cur) > 0 && len(out) == 0 && !b.AnyCommand {
		return nil, errors.New("去掉后没有允许的命令了，等于不限命令；确实要不限命令时写明（any_command / --any-command）")
	}
	return out, nil
}

func allowedText(xs []string) string {
	if len(xs) == 0 {
		return "不限命令"
	}
	return quoteAll(xs)
}

// secretAmend 处理会话的修改授权申请。
func (h *Hub) secretAmend(ctx context.Context, b secretAmendIn) (*secretRequestOut, error) {
	me, err := h.agentSessionOf(ctx, b.SessionKey)
	if err != nil {
		return nil, err
	}
	reason := strings.TrimSpace(b.Reason)
	if reason == "" {
		return nil, huma.Error400BadRequest("申请要写理由（reason / --reason）：为什么要改，例如哪条命令被拒、要运行什么")
	}
	x, err := h.store.GetSecret(ctx, b.Name)
	if errors.Is(err, store.ErrNotFound) {
		return nil, huma.Error404NotFound(fmt.Sprintf("没有名为「%s」的凭据。ks secret list 看本项目能用的；新的凭据用 ks secret request 申请", strings.TrimSpace(b.Name)))
	} else if err != nil {
		return nil, err
	}
	// 只能改授权给本项目的凭据：不能借申请去动别的项目的凭据
	if !secretInProject(x, me.ProjectID) {
		return nil, huma.Error403Forbidden(fmt.Sprintf("凭据「%s」没有授权给本项目（%s），不能申请修改它的授权", x.Name, me.ProjectName))
	}
	out := &secretRequestOut{}
	if x.RequestPending {
		d, _ := h.store.GetDecision(ctx, x.RequestID)
		out.Body.Decision, out.Body.Existing = &d, true
		out.Body.Message = fmt.Sprintf("凭据「%s」已有申请 %s 等待用户处理，没有重复提。结果会送到提申请的会话。", x.Name, x.RequestCode)
		return out, nil
	}
	if x.Status == store.SecretPending {
		return nil, huma.Error400BadRequest(fmt.Sprintf("凭据「%s」还没填写值，不能修改授权；用 ks secret request 申请", x.Name))
	}
	reqID := ""
	if code := strings.TrimSpace(b.Requirement); code != "" {
		r, err := h.store.GetRequirement(ctx, code)
		if err != nil {
			return nil, huma.Error400BadRequest("需求 " + code + " 不存在")
		}
		if r.ProjectID != "" && r.ProjectID != me.ProjectID {
			return nil, huma.Error400BadRequest(r.Code + " 不属于你所在的项目")
		}
		reqID = r.ID
	}
	before := scopeOf(x)
	after := scopeOf(x)
	if after.Allowed, err = amendAllowed(x.Allowed, b); err != nil {
		return nil, huma.Error400BadRequest(err.Error())
	}
	if b.Projects != nil {
		if after.Projects, after.Names, err = h.resolveProjects(ctx, b.Projects); err != nil {
			return nil, huma.Error400BadRequest(err.Error())
		}
	}
	switch {
	case b.Days != nil:
		if err := checkSecretDays(*b.Days); err != nil {
			return nil, huma.Error400BadRequest(err.Error())
		}
		after.Days = *b.Days
	case x.Status != store.SecretActive:
		after.Days = defaultSecretDays // 已失效 / 已撤回：批准即重新授权，要有新的有效期
	}
	if b.Disk != nil {
		after.Disk = *b.Disk
	}
	changes := amendChanges(before, after)
	vals, _ := h.secretValues(x)
	known := sensitiveValues(x, vals)
	replace, err := h.amendReplace(x, b.Replace, known)
	if err != nil {
		return nil, huma.Error400BadRequest(err.Error())
	}
	if len(changes) == 0 && len(replace) == 0 {
		return nil, huma.Error400BadRequest(fmt.Sprintf("申请里没有要改的：凭据「%s」现在的授权是 %s。要替换值时写明替换哪几个（replace / --replace）", x.Name, secretScope(x)))
	}
	all := changes
	if len(replace) > 0 {
		all = append([]string{"替换值：" + strings.Join(replaceLabels(x, replace), "、")}, changes...)
	}
	reason = h.redactText(reason, known)
	d, err := h.store.CreateDecision(ctx, store.Decision{
		SessionKey: me.Key, ProjectID: me.ProjectID, RequirementID: reqID, Kind: store.KindDecision,
		Question: amendTitle(x.Name, len(replace) > 0, len(changes) > 0), Context: reason,
		Approval: true, Options: []string{"批准", "拒绝"}, Risk: store.RiskHigh, Hard: "permission",
		RequestKind: store.RequestSecret, FromRole: me.Role,
	})
	if err != nil {
		return nil, apiErr(err)
	}
	x.RequestID, x.RequestedBy = d.ID, me.Key
	x.Ask = &store.SecretAsk{Mode: store.AskAmend, ProjectID: me.ProjectID, Reason: reason, Days: after.Days, Allowed: after.Allowed, Projects: after.Projects, Disk: after.Disk, Replace: replace}
	if x, err = h.store.SaveSecret(ctx, x); err != nil {
		return nil, apiErr(err)
	}
	if _, err := h.store.AddSecretLog(ctx, store.SecretLog{SecretID: x.ID, Kind: store.SecretLogAmend, SessionKey: me.Key, RequirementID: reqID, Reason: reason, Days: after.Days}); err != nil {
		return nil, err
	}
	h.broker.publish("decisions", "sessions", "requirements", "secrets")
	out.Body.Decision = &d
	extra := ""
	if len(replace) > 0 {
		extra = "新值由用户在抽屉里填写，你拿不到值；批准前旧值照常可用，批准后新值生效、旧值作废，凭据名不变，照常用 ks secret run 取用。"
	}
	out.Body.Message = fmt.Sprintf("已申请修改授权（%s）：凭据「%s」，%s。在用户的「待处理」里等待处理；批准前原来的授权照常生效。%s用户保存并授权或拒绝后，结果会送到你这里。不要让用户去凭据页手动改，也不要新建凭据。",
		d.Code, x.Name, h.redactText(strings.Join(all, "；"), known), extra)
	return out, nil
}

// secretAmendAnswer 是批准修改授权申请后送给申请方的答复（以「批准」开头）。changes 是这次实际改了的授权（你在抽屉里调整过的以此为准），
// replaced 是替换了的值的显示名（R-325，不含值）。
func secretAmendAnswer(x store.Secret, replaced, changes []string) string {
	what := "授权没有改动"
	if len(changes) > 0 {
		what = strings.Join(changes, "；")
	}
	if len(replaced) > 0 {
		if len(changes) > 0 {
			what = "授权改动：" + what
		}
		return fmt.Sprintf("批准（用户已替换凭据「%s」的值：%s，新值已生效，旧值已作废；%s。现在的授权：%s）。照常用 ks secret run %q -- <命令> 运行，值只作为这条命令的环境变量出现；不要打印、回显或写进文件。",
			x.Name, strings.Join(replaced, "、"), what, secretScope(x), x.Name)
	}
	return fmt.Sprintf("批准（用户已修改凭据「%s」的授权：%s。现在的授权：%s）。用 ks secret run %q -- <命令> 运行，值只作为这条命令的环境变量出现；不要打印、回显或写进文件。",
		x.Name, what, secretScope(x), x.Name)
}
