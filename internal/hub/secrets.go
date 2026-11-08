package hub

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"time"

	"github.com/danielgtaylor/huma/v2"

	"github.com/NekroAI/kitestring/internal/store"
)

// 纸鸢凭据库（R-295，D-1434 / D-1438）。
//
//   - 申请：会话用 ks secret request（或 MCP secret_request）写明要什么、用途、关联需求、建议有效期、允许的命令和每个值的获取方式。
//     申请是一条请求类审批（request_kind = secret，批准类、标为权限类硬规则，永不代答），出现在「待处理」；网页抽屉里填值、
//     改项目 / 有效期 / 允许的命令 / 写入磁盘后「保存并授权」，或「拒绝…」。结果作为这条决策的答复送回申请方（现有送达机制）。
//     申请里的项目、有效期、命令只是建议（secrets.request），保存授权后才生效：申请不能自己扩大已有的授权。
//   - 取用：ks secret run <凭据> -- <命令>。授权有效、会话所在项目在授权范围内、命令在允许范围内，纸鸢才把值交给 ks 进程，
//     由它作为这条命令的环境变量运行；否则拒绝并说明原因。会话拿不到明文：没有打印明文的命令。值进了子进程的环境，
//     会话执行 env 之类的命令照样能读到——这一层只防误用，防不住故意读取（界面写明）。
//   - 写入磁盘（ks secret write）单独授权，记下路径。
//   - 记录：每次取用（含被拒）记时间、会话、需求、命令、结果；命令里的已知值存之前就换成「[已隐去]」。授权变更记在同一条时间线。
//     越界被拒只记录，不进「待处理」、不推送（D-1438）。
//   - 凭据接口只挂在网页登录（requireWeb）和设备 token（requireDevice）两组路由上：访客路由（/api/share/）没有凭据接口，
//     沙箱里的原型发出的请求带 Origin: null，requireWeb 一律当未登录。

const (
	defaultSecretDays = 7
	maxSecretDays     = 3650
)

var secretEnvRe = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)

// secretFieldIn 是申请 / 新建时的一个值。
type secretFieldIn struct {
	Env   string `json:"env" doc:"环境变量名，例如 CLOUDFLARE_API_TOKEN"`
	Label string `json:"label,omitempty" doc:"显示名，例如 API Token；不写时用环境变量名"`
	How   string `json:"how,omitempty" doc:"获取方式：按服务写清步骤，指导用户快速取到（例如「Cloudflare 控制台 → 我的个人资料 → API 令牌，模板选编辑 Cloudflare Workers」）"`
	Value string `json:"value,omitempty" doc:"值（只在网页上新建时填）"`
	Plain bool   `json:"plain,omitempty" doc:"不敏感（如用户名、Account ID）：明文显示、不在纸鸢里隐去；不写 = 敏感。注入命令的规则不变"`
}

// normSecretFields 检查并整理字段：环境变量名合法、不重复，至少一个。
func normSecretFields(in []secretFieldIn) ([]store.SecretField, error) {
	if len(in) == 0 {
		return nil, errors.New("至少要有一个值（环境变量名）")
	}
	seen := map[string]bool{}
	out := make([]store.SecretField, 0, len(in))
	for _, f := range in {
		env := strings.TrimSpace(f.Env)
		if !secretEnvRe.MatchString(env) {
			return nil, fmt.Errorf("环境变量名 %q 不合法：只能用字母、数字和下划线，不能以数字开头", env)
		}
		if seen[env] {
			return nil, fmt.Errorf("环境变量名 %s 重复", env)
		}
		seen[env] = true
		label := strings.TrimSpace(f.Label)
		if label == "" {
			label = env
		}
		out = append(out, store.SecretField{Env: env, Label: label, How: strings.TrimSpace(f.How), Plain: f.Plain})
	}
	return out, nil
}

// normAllowed 整理允许的命令：去空白、去重。空表 = 不限命令。
func normAllowed(in []string) []string {
	out := []string{}
	for _, a := range in {
		a = strings.Join(strings.Fields(a), " ")
		if a != "" && !slices.Contains(out, a) {
			out = append(out, a)
		}
	}
	return out
}

func checkSecretDays(days int) error {
	if days < 0 || days > maxSecretDays {
		return fmt.Errorf("有效期要在 0～%d 天之间（0 = 长期）", maxSecretDays)
	}
	return nil
}

// secretExpiry：有效期（天）对应的到期时间；0 = 长期。
func secretExpiry(days int) *time.Time {
	if days <= 0 {
		return nil
	}
	t := time.Now().Add(time.Duration(days) * 24 * time.Hour)
	return &t
}

// ---------- 命令范围 ----------

// program 是要运行的程序：ks 用 exec.LookPath 解析出的绝对路径，以及它是不是按 PATH 找到的（命令里写的是裸程序名）。
type program struct {
	path    string
	viaPath bool
}

// commandAllowed：argv 是否在允许的命令里。allowed 为空 = 不限命令。
// 每条允许的命令按空白拆成词，逐词对 argv：「*」单独一词放在最后表示后面可以跟任意参数（也可以没有），放在中间匹配一个参数；
// 词里的「*」匹配任意字符（如 --project-name=*）。
// 第一个词是程序：写裸程序名（wrangler）时，只认按 PATH 找到的那一个，./wrangler、/tmp/x/wrangler 这类写了路径的都不算；
// 写绝对路径时和解析出的路径精确比对。会话改 PATH、NODE_OPTIONS 这类环境变量仍能换掉实际运行的东西——取用记录写实际路径，
// 这一层只防误用（D-1434）。
func commandAllowed(allowed, argv []string, prog program) bool {
	if len(allowed) == 0 {
		return len(argv) > 0
	}
	for _, a := range allowed {
		if matchCommand(strings.Fields(a), argv, prog) {
			return true
		}
	}
	return false
}

func matchCommand(pat, argv []string, prog program) bool {
	if len(pat) == 0 || len(argv) == 0 {
		return false
	}
	if strings.Contains(pat[0], "/") {
		if !wildMatch(pat[0], prog.path) {
			return false
		}
	} else if !prog.viaPath || !wildMatch(pat[0], filepath.Base(prog.path)) {
		return false
	}
	pat, argv = pat[1:], argv[1:]
	for i, p := range pat {
		if p == "*" && i == len(pat)-1 {
			return true
		}
		if i >= len(argv) || !wildMatch(p, argv[i]) {
			return false
		}
	}
	return len(argv) == len(pat)
}

// wildMatch：「*」匹配任意字符（含 /），其余逐字相等。
func wildMatch(pat, s string) bool {
	parts := strings.Split(pat, "*")
	if len(parts) == 1 {
		return pat == s
	}
	if !strings.HasPrefix(s, parts[0]) {
		return false
	}
	s = s[len(parts[0]):]
	for _, p := range parts[1 : len(parts)-1] {
		i := strings.Index(s, p)
		if i < 0 {
			return false
		}
		s = s[i+len(p):]
	}
	return strings.HasSuffix(s, parts[len(parts)-1])
}

// commandLine 把 argv 拼成一行显示：含空白或 shell 特殊字符的参数加双引号。
func commandLine(argv []string) string {
	out := make([]string, len(argv))
	for i, a := range argv {
		if a == "" || strings.ContainsAny(a, " \t\n\"'\\$`|&;<>(){}*?!#~") {
			a = `"` + strings.NewReplacer(`\`, `\\`, `"`, `\"`).Replace(a) + `"`
		}
		out[i] = a
	}
	return strings.Join(out, " ")
}

// ---------- 读取与视图 ----------

// secretView 补上最近一次取用和不敏感的值的明文（R-318；敏感的值永不返回）。
func (h *Hub) secretView(ctx context.Context, x store.Secret) store.Secret {
	if l, err := h.store.LastSecretUse(ctx, x.ID); err == nil {
		x.LastUse = l
	}
	x.Fields = slices.Clone(x.Fields)
	var vals map[string]string
	for i, f := range x.Fields {
		x.Fields[i].Value = ""
		if !f.Plain || !f.Filled || x.Cipher == "" {
			continue
		}
		if vals == nil {
			v, err := h.secretValues(x)
			if err != nil {
				break
			}
			vals = v
		}
		x.Fields[i].Value = vals[f.Env]
	}
	return x
}

// sensitiveValues 是参与脱敏的值：只有标为敏感的字段（R-318）；没有对应字段说明的按敏感算。
func sensitiveValues(x store.Secret, vals map[string]string) map[string]string {
	out := make(map[string]string, len(vals))
	for k, v := range vals {
		i := slices.IndexFunc(x.Fields, func(f store.SecretField) bool { return f.Env == k })
		if i >= 0 && x.Fields[i].Plain {
			continue
		}
		out[k] = v
	}
	return out
}

// applyPlain 按 env → 不敏感改字段的敏感标记，返回要记的时间线（每个改了的值一条）。没写的字段保持原样。
func applyPlain(x *store.Secret, plain map[string]bool) ([]store.SecretLog, error) {
	var logs []store.SecretLog
	for env, p := range plain {
		i := slices.IndexFunc(x.Fields, func(f store.SecretField) bool { return f.Env == env })
		if i < 0 {
			return nil, fmt.Errorf("凭据「%s」没有 %s 这个值", x.Name, env)
		}
		if x.Fields[i].Plain == p {
			continue
		}
		x.Fields[i].Plain = p
		kind := store.SecretLogSensitive
		if p {
			kind = store.SecretLogPlain
		}
		logs = append(logs, store.SecretLog{SecretID: x.ID, Kind: kind, Reason: x.Fields[i].Label, Days: -1})
	}
	slices.SortFunc(logs, func(a, b store.SecretLog) int { return strings.Compare(a.Reason, b.Reason) })
	return logs, nil
}

// getSecretRef 按 ID、名字或申请的决策编号（D-x）找凭据。
func (h *Hub) getSecretRef(ctx context.Context, ref string) (store.Secret, error) {
	if _, ok := strings.CutPrefix(strings.ToUpper(strings.TrimSpace(ref)), "D-"); ok {
		if x, err := h.store.SecretByRequest(ctx, ref); err == nil {
			return x, nil
		}
	}
	return h.store.GetSecret(ctx, ref)
}

// secretTimeline 是详情抽屉的时间线：取用记录与授权变更，新的在前；授权已到期的，在到期时刻补一条「已到期」。
func (h *Hub) secretTimeline(ctx context.Context, x store.Secret) ([]store.SecretLog, error) {
	logs, err := h.store.ListSecretLogs(ctx, x.ID)
	if err != nil {
		return nil, err
	}
	if x.ExpiresAt != nil && x.RevokedAt == nil && x.Cipher != "" && time.Now().After(*x.ExpiresAt) {
		exp := store.SecretLog{ID: "expire-" + x.ID, SecretID: x.ID, Kind: secretLogExpire, At: *x.ExpiresAt, Days: -1}
		i := 0
		for i < len(logs) && logs[i].At.After(exp.At) {
			i++
		}
		logs = slices.Insert(logs, i, exp)
	}
	return logs, nil
}

// secretLogExpire 只出现在时间线上（不存库）：授权在这一刻到期。
const secretLogExpire = "expire"

// ---------- 申请的答复（送回申请方） ----------

// secretScope 是授权范围的一句话：项目、有效期、允许的命令、写入磁盘。
func secretScope(x store.Secret) string {
	exp := "长期有效"
	if x.ExpiresAt != nil {
		exp = "有效期至 " + x.ExpiresAt.Local().Format("01-02 15:04")
	}
	cmds := "不限命令"
	if len(x.Allowed) > 0 {
		cmds = "允许的命令：" + strings.Join(x.Allowed, "、")
	}
	disk := "不允许写入磁盘"
	if x.Disk {
		disk = "允许写入磁盘（ks secret write）"
	}
	return fmt.Sprintf("项目：%s；%s；%s；%s", strings.Join(x.ProjectNames, "、"), exp, cmds, disk)
}

func secretEnvs(x store.Secret) string {
	envs := make([]string, len(x.Fields))
	for i, f := range x.Fields {
		envs[i] = f.Env
	}
	return strings.Join(envs, "、")
}

// secretGrantAnswer 是保存授权后送给申请方的答复（存进决策的 answer，以「批准」开头）。
func secretGrantAnswer(x store.Secret) string {
	return fmt.Sprintf("批准（用户已授权凭据「%s」，含 %s；%s）。凭据编号 %[4]s，命令里写编号，改名不影响。用 ks secret run %[4]s -- <命令> 运行，值只作为这条命令的环境变量出现；不要打印、回显或写进文件，也不要再让用户在终端里输入。",
		x.Name, secretEnvs(x), secretScope(x), x.Code)
}

// secretRequestText：凭据申请送给申请方的答复。批准时 answer 已是完整的一段（secretGrantAnswer）。
func secretRequestText(d store.Decision) string {
	if d.Verdict == store.VerdictRejected {
		return rejectedText("凭据申请「"+d.Question+"」", answerNote(d.Answer, "拒绝")) + "。不要改用别的方式（例如在终端里输入）向用户索要这个凭据。"
	}
	return d.Answer
}

// ---------- 写入 ----------

// secretValues 解密已有的值；解不开时返回错误（调用方决定是否要求整条重填）。
func (h *Hub) secretValues(x store.Secret) (map[string]string, error) {
	return h.openSecret(x.ID, x.Cipher)
}

// mergeSecretValues 把新填的值并进已有值：没填的保留原值。requireAll（保存授权时）要求每个字段最后都有值；
// 替换值时没填过的字段（例如等待中的申请新加的）可以继续空着。返回是否有改动。
func mergeSecretValues(x *store.Secret, old, in map[string]string, requireAll bool) (map[string]string, bool, error) {
	vals := map[string]string{}
	changed := false
	for i, f := range x.Fields {
		v := old[f.Env]
		if nv := in[f.Env]; strings.TrimSpace(nv) != "" {
			changed = changed || nv != v
			v = nv
		}
		if v == "" {
			if requireAll {
				return nil, false, fmt.Errorf("「%s」还没填写", f.Label)
			}
			x.Fields[i].Filled = false
			continue
		}
		vals[f.Env] = v
		x.Fields[i].Filled = true
	}
	for k := range in {
		if !slices.ContainsFunc(x.Fields, func(f store.SecretField) bool { return f.Env == k }) {
			return nil, false, fmt.Errorf("凭据「%s」没有 %s 这个值", x.Name, k)
		}
	}
	return vals, changed, nil
}

// checkProjects 核对项目 ID 都存在、至少一个，去重。
func (h *Hub) checkProjects(ctx context.Context, ids []string) ([]string, error) {
	out := []string{}
	for _, id := range ids {
		id = strings.TrimSpace(id)
		if id == "" || slices.Contains(out, id) {
			continue
		}
		if _, err := h.store.GetProject(ctx, id); err != nil {
			return nil, fmt.Errorf("项目 %s 不存在", id)
		}
		out = append(out, id)
	}
	if len(out) == 0 {
		return nil, errors.New("至少选择一个项目")
	}
	return out, nil
}

// secretsChanged：凭据有变化——重建脱敏器、通知网页。
func (h *Hub) secretsChanged(ctx context.Context) {
	h.refreshRedactor(ctx)
	h.broker.publish("secrets")
}

type secretAuthIn struct {
	Values   map[string]string `json:"values,omitempty" doc:"环境变量名 → 值；没写或为空的保留原值。待填写的每个值都要填"`
	Projects []string          `json:"projects" doc:"授权的项目 ID，至少一个"`
	Days     int               `json:"days" doc:"有效期（天），从现在算起；0 = 长期"`
	Allowed  []string          `json:"allowed,omitempty" doc:"允许的命令；空 = 不限命令"`
	Disk     bool              `json:"disk,omitempty" doc:"允许写入磁盘"`
	Plain    map[string]bool   `json:"plain,omitempty" doc:"环境变量名 → 是否不敏感（R-318）；没写的保持原样"`
	Note     string            `json:"note,omitempty" doc:"附言（R-321）：有等待中的申请时随结果送给申请方；都记在授权变更时间线上。已知值隐去"`
	Name     string            `json:"name,omitempty" doc:"新名称（R-326）：只在修改授权申请提出改名时用，空 = 按申请里的新名称"`
}

// authorize 保存授权：填值（可选）、设项目 / 有效期 / 允许的命令 / 写入磁盘。有等待中的申请时同时把它答成批准，送回申请方。
func (h *Hub) authorize(ctx context.Context, x store.Secret, in secretAuthIn) (store.Secret, error) {
	before := x.Status
	pendingReq := x.RequestPending
	amend := pendingReq && x.Ask.IsAmend() // 修改授权申请（R-320）
	orig := scopeOf(x)
	origFields := slices.Clone(x.Fields)
	old, err := h.secretValues(x)
	if err != nil {
		old = map[string]string{} // 主密钥换了：要求把每个值重新填一遍
	}
	if amend { // 替换值（R-325）：申请里要替换的值都要填新值；新的获取方式随之写进字段说明
		for _, r := range x.Ask.Replace {
			i := slices.IndexFunc(x.Fields, func(f store.SecretField) bool { return f.Env == r.Env })
			if i < 0 {
				continue
			}
			nv := in.Values[r.Env]
			if strings.TrimSpace(nv) == "" {
				return x, huma.Error400BadRequest(fmt.Sprintf("「%s」要填写新的值", x.Fields[i].Label))
			}
			if nv == old[r.Env] {
				return x, huma.Error400BadRequest(fmt.Sprintf("「%s」的新值和原来的相同", x.Fields[i].Label))
			}
			if r.How != "" {
				x.Fields[i].How = r.How
			}
		}
	}
	// 改名（R-326）：修改授权申请里提出了新名称时，按抽屉里的（可再调整）改
	oldName, renamed := x.Name, false
	if amend && x.Ask.Name != "" {
		nn := strings.TrimSpace(in.Name)
		if nn == "" {
			nn = x.Ask.Name
		}
		x.Name, renamed = nn, nn != oldName
	}
	vals, changed, err := mergeSecretValues(&x, old, in.Values, true)
	if err != nil {
		return x, huma.Error400BadRequest(err.Error())
	}
	marks, err := applyPlain(&x, in.Plain)
	if err != nil {
		return x, huma.Error400BadRequest(err.Error())
	}
	projects, err := h.checkProjects(ctx, in.Projects)
	if err != nil {
		return x, huma.Error400BadRequest(err.Error())
	}
	if in.Days == secretKeepExpiry {
		if before != store.SecretActive {
			return x, huma.Error400BadRequest("凭据「" + x.Name + "」不在生效中，选一个有效期")
		}
	} else if err := checkSecretDays(in.Days); err != nil {
		return x, huma.Error400BadRequest(err.Error())
	}
	if x.Cipher, err = h.sealSecret(x.ID, vals); err != nil {
		return x, err
	}
	replaced, err := h.retireValues(&x, origFields, old, vals) // 换掉的旧值作废，仍参与脱敏（R-325）
	if err != nil {
		return x, err
	}
	n := time.Now()
	x.Projects, x.Allowed, x.Disk = projects, normAllowed(in.Allowed), in.Disk
	if in.Days != secretKeepExpiry {
		x.ExpiresAt = secretExpiry(in.Days)
	}
	x.GrantedAt, x.RevokedAt = &n, nil
	saved, err := h.store.SaveSecret(ctx, x)
	if err != nil {
		return x, apiErr(err)
	}
	kind := store.SecretLogEdit
	switch {
	case amend && (before == store.SecretExpired || before == store.SecretRevoked):
		kind = store.SecretLogRegrant
	case amend:
		kind = store.SecretLogEdit
	case pendingReq || before == store.SecretPending:
		kind = store.SecretLogGrant
	case before == store.SecretExpired || before == store.SecretRevoked:
		kind = store.SecretLogRegrant
	}
	// 附言（R-321）：按普通文字处理，已知值隐去（含这次新填的值）
	note := h.redactText(strings.TrimSpace(in.Note), sensitiveValues(saved, vals))
	l := store.SecretLog{SecretID: x.ID, Kind: kind, Days: in.Days, Note: note}
	if pendingReq {
		l.SessionKey = x.RequestedBy // 「申请来自「x」」
	}
	var changes []string
	if amend {
		next := scopeOf(saved)
		next.Days = in.Days
		changes = amendChanges(orig, next)
		// 改了什么记在时间线上（命令里的已知值隐去）；改动说明里只有授权范围，没有值
		l.Reason = h.redactText(strings.Join(changes, "；"), sensitiveValues(saved, vals))
	}
	// 替换值记一条「替换了哪些值」（只有显示名），改名记一条「旧名 → 新名」（R-326）。修改授权申请只替换值 / 改名、授权没改时，
	// 附言和申请的会话记在这几条上（附言记在最后一条），不再另记「修改了授权」
	onlyReplace := amend && (len(replaced) > 0 || renamed) && len(changes) == 0 && kind == store.SecretLogEdit
	if changed && (kind != store.SecretLogGrant || amend) {
		rl := store.SecretLog{SecretID: x.ID, Kind: store.SecretLogReplace, Reason: strings.Join(replaced, "、"), Days: -1}
		if amend {
			rl.SessionKey = x.RequestedBy
		}
		if onlyReplace && !renamed {
			rl.Note = note
		}
		if _, err := h.store.AddSecretLog(ctx, rl); err != nil {
			return saved, err
		}
	}
	if renamed {
		rn := store.SecretLog{SecretID: x.ID, Kind: store.SecretLogRename, Reason: renameText(oldName, saved.Name), SessionKey: x.RequestedBy, Days: -1}
		if onlyReplace {
			rn.Note = note
		}
		if _, err := h.store.AddSecretLog(ctx, rn); err != nil {
			return saved, err
		}
	}
	if !onlyReplace {
		if _, err := h.store.AddSecretLog(ctx, l); err != nil {
			return saved, err
		}
	}
	for _, m := range marks {
		if _, err := h.store.AddSecretLog(ctx, m); err != nil {
			return saved, err
		}
	}
	if pendingReq {
		answer := secretGrantAnswer(saved)
		if amend {
			all := changes
			if renamed {
				all = append([]string{"名称：" + renameText(oldName, saved.Name)}, changes...)
			}
			answer = h.redactText(secretAmendAnswer(saved, replaced, all), sensitiveValues(saved, vals))
		}
		if note != "" {
			answer += "\n附言：" + note
		}
		d, err := h.store.AnswerDecisionPoints(ctx, x.RequestID, answer, nil, nil, "", store.VerdictApproved)
		if err != nil {
			return saved, apiErr(err)
		}
		h.awayUser(ctx, "answer", d.ProjectID, d.Code, d.Question, "保存并授权")
		h.watchDecisionAnswered(ctx, d, "")
		h.decisionsChanged(ctx, d)
		saved, _ = h.store.GetSecret(ctx, x.ID)
	}
	h.secretsChanged(ctx)
	return h.secretView(ctx, saved), nil
}

// rejectSecretRequest 拒绝等待中的申请：答复送回申请方；从没填过值的凭据随之删除（它只为这条申请存在）。
func (h *Hub) rejectSecretRequest(ctx context.Context, x store.Secret, note string) (store.Decision, error) {
	if !x.RequestPending {
		return store.Decision{}, huma.Error400BadRequest("凭据「" + x.Name + "」没有等待处理的申请")
	}
	answer := "拒绝"
	// 附言（R-321）：已知值隐去后送回申请方、记进时间线
	vals, _ := h.secretValues(x)
	if note = h.redactText(strings.TrimSpace(note), sensitiveValues(x, vals)); note != "" {
		answer += "；" + note
	}
	d, err := h.store.AnswerDecisionPoints(ctx, x.RequestID, answer, nil, nil, "", store.VerdictRejected)
	if err != nil {
		return d, apiErr(err)
	}
	h.awayUser(ctx, "answer", d.ProjectID, d.Code, d.Question, "拒绝")
	h.watchDecisionAnswered(ctx, d, "")
	h.decisionsChanged(ctx, d)
	h.afterSecretRequest(ctx, d, note)
	return d, nil
}

// afterSecretRequest：申请被拒绝或撤回后，从没填过值的凭据删除；有值的留着，去掉这次申请新加、还没填的值（不留下
// 卡住替换值 / 重新授权的空字段），被拒绝的记一条「拒绝了申请」。
func (h *Hub) afterSecretRequest(ctx context.Context, d store.Decision, note string) {
	if d.RequestKind != store.RequestSecret {
		return
	}
	x, err := h.store.SecretByRequest(ctx, d.ID)
	if err != nil {
		return
	}
	if x.Cipher == "" {
		_ = h.store.DeleteSecret(ctx, x.ID)
		h.broker.publish("secrets")
		return
	}
	if x.Ask != nil && len(x.Ask.Added) > 0 {
		x.Fields = slices.DeleteFunc(x.Fields, func(f store.SecretField) bool { return !f.Filled && slices.Contains(x.Ask.Added, f.Env) })
		x.Ask.Added = nil
		if _, err := h.store.SaveSecret(ctx, x); err != nil {
			h.log.Warn("回滚凭据申请新加的值失败", "secret", x.Name, "err", err)
		}
	}
	if d.Verdict == store.VerdictRejected {
		if note != "" { // 通用作答接口拒绝时附言没经过脱敏：记之前隐去已知值
			vals, _ := h.secretValues(x)
			note = h.redactText(note, sensitiveValues(x, vals))
		}
		_, _ = h.store.AddSecretLog(ctx, store.SecretLog{SecretID: x.ID, Kind: store.SecretLogReject, SessionKey: x.RequestedBy, Note: note, Days: -1})
	}
	h.broker.publish("secrets")
}

// checkSecretAnswer：凭据申请不能在通用的作答接口里批准（要先填值），只能拒绝。
func checkSecretAnswer(d store.Decision, answer, verdict string, reject bool) error {
	if d.RequestKind != store.RequestSecret {
		return nil
	}
	if d.Status != store.DecPending {
		return huma.Error400BadRequest(d.Code + " 这条凭据申请已经处理过，不能再答")
	}
	if reject || verdict == store.VerdictRejected || (verdict == "" && strings.HasPrefix(strings.TrimSpace(answer), "拒绝")) {
		return nil
	}
	return huma.Error400BadRequest("凭据申请要在抽屉里填写值后「保存并授权」，不能直接批准")
}

// ---------- 网页接口 ----------

type secretsOut struct {
	Body struct {
		Secrets []store.Secret `json:"secrets"`
	}
}

type secretOut struct {
	Body struct {
		Secret store.Secret      `json:"secret"`
		Logs   []store.SecretLog `json:"logs"`
	}
}

func (h *Hub) secretDetail(ctx context.Context, x store.Secret) (*secretOut, error) {
	out := &secretOut{}
	out.Body.Secret = h.secretView(ctx, x)
	logs, err := h.secretTimeline(ctx, x)
	if err != nil {
		return nil, err
	}
	out.Body.Logs = logs
	return out, nil
}

func (h *Hub) registerSecretsWeb(api huma.API) {
	tag := []string{"secrets"}
	huma.Register(api, huma.Operation{OperationID: "list-secrets", Method: http.MethodGet, Path: "/api/secrets", Tags: tag, Summary: "凭据列表（R-295）：值永不返回"},
		func(ctx context.Context, _ *struct{}) (*secretsOut, error) {
			list, err := h.store.ListSecrets(ctx)
			if err != nil {
				return nil, err
			}
			out := &secretsOut{}
			out.Body.Secrets = make([]store.Secret, len(list))
			for i, x := range list {
				out.Body.Secrets[i] = h.secretView(ctx, x)
			}
			return out, nil
		})
	huma.Register(api, huma.Operation{OperationID: "get-secret", Method: http.MethodGet, Path: "/api/secrets/{ref}", Tags: tag, Summary: "凭据详情与时间线；ref 可以是 ID、名字或申请的决策编号 D-x"},
		func(ctx context.Context, in *struct {
			Ref string `path:"ref"`
		}) (*secretOut, error) {
			x, err := h.getSecretRef(ctx, in.Ref)
			if err != nil {
				return nil, apiErr(err)
			}
			return h.secretDetail(ctx, x)
		})
	huma.Register(api, huma.Operation{OperationID: "create-secret", Method: http.MethodPost, Path: "/api/secrets", Tags: tag, Summary: "新建凭据并授权"},
		func(ctx context.Context, in *struct {
			Body struct {
				Name     string          `json:"name" minLength:"1"`
				Purpose  string          `json:"purpose,omitempty"`
				Fields   []secretFieldIn `json:"fields" doc:"每个值：环境变量名、显示名、值"`
				Projects []string        `json:"projects" doc:"授权的项目 ID，至少一个"`
				Days     int             `json:"days" doc:"有效期（天）；0 = 长期"`
				Allowed  []string        `json:"allowed,omitempty" doc:"允许的命令；空 = 不限命令"`
				Disk     bool            `json:"disk,omitempty" doc:"允许写入磁盘"`
			}
		}) (*secretOut, error) {
			fields, err := normSecretFields(in.Body.Fields)
			if err != nil {
				return nil, huma.Error400BadRequest(err.Error())
			}
			vals := map[string]string{}
			for _, f := range in.Body.Fields {
				vals[strings.TrimSpace(f.Env)] = f.Value
			}
			x, err := h.store.CreateSecret(ctx, store.Secret{Name: in.Body.Name, Purpose: strings.TrimSpace(in.Body.Purpose), Fields: fields})
			if err != nil {
				return nil, apiErr(err)
			}
			auth := secretAuthIn{Values: vals, Projects: in.Body.Projects, Days: in.Body.Days, Allowed: in.Body.Allowed, Disk: in.Body.Disk}
			if _, err := h.authorize(ctx, x, auth); err != nil {
				_ = h.store.DeleteSecret(ctx, x.ID) // 值或授权不对：不留半截的凭据
				return nil, err
			}
			x, err = h.store.GetSecret(ctx, x.ID)
			if err != nil {
				return nil, err
			}
			// authorize 记的是「保存并授权」；网页上新建的改记「新建」
			_ = h.relabelFirstGrant(ctx, x.ID)
			return h.secretDetail(ctx, x)
		})
	huma.Register(api, huma.Operation{OperationID: "authorize-secret", Method: http.MethodPost, Path: "/api/secrets/{ref}/authorize", Tags: tag,
		Summary: "保存并授权：填写 / 替换值、修改授权、重新授权；有等待中的申请时一并答成批准、送回申请方"},
		func(ctx context.Context, in *struct {
			Ref  string `path:"ref"`
			Body secretAuthIn
		}) (*secretOut, error) {
			x, err := h.getSecretRef(ctx, in.Ref)
			if err != nil {
				return nil, apiErr(err)
			}
			if _, err := h.authorize(ctx, x, in.Body); err != nil {
				return nil, err
			}
			x, err = h.store.GetSecret(ctx, x.ID)
			if err != nil {
				return nil, err
			}
			return h.secretDetail(ctx, x)
		})
	huma.Register(api, huma.Operation{OperationID: "replace-secret-values", Method: http.MethodPost, Path: "/api/secrets/{ref}/values", Tags: tag, Summary: "替换值：授权不变"},
		func(ctx context.Context, in *struct {
			Ref  string `path:"ref"`
			Body struct {
				Values map[string]string `json:"values" doc:"环境变量名 → 新值；没写的保留原值"`
				Plain  map[string]bool   `json:"plain,omitempty" doc:"环境变量名 → 是否不敏感（R-318）；没写的保持原样"`
			}
		}) (*secretOut, error) {
			x, err := h.getSecretRef(ctx, in.Ref)
			if err != nil {
				return nil, apiErr(err)
			}
			if x.Status == store.SecretPending {
				return nil, huma.Error400BadRequest("凭据「" + x.Name + "」还没填写，用「填写…」保存并授权")
			}
			old, err := h.secretValues(x)
			if err != nil {
				old = map[string]string{}
			}
			orig := x
			orig.Fields = slices.Clone(x.Fields)
			vals, changed, err := mergeSecretValues(&x, old, in.Body.Values, false)
			if err != nil {
				return nil, huma.Error400BadRequest(err.Error())
			}
			if !changed {
				x = orig // 只改了敏感标记：值和密文原样不动
			}
			marks, err := applyPlain(&x, in.Body.Plain)
			if err != nil {
				return nil, huma.Error400BadRequest(err.Error())
			}
			if !changed && len(marks) == 0 {
				return nil, huma.Error400BadRequest("没有填写新的值")
			}
			var replaced []string
			if changed {
				if x.Cipher, err = h.sealSecret(x.ID, vals); err != nil {
					return nil, err
				}
				if replaced, err = h.retireValues(&x, orig.Fields, old, vals); err != nil { // 旧值仍参与脱敏（R-325）
					return nil, err
				}
			}
			if x, err = h.store.SaveSecret(ctx, x); err != nil {
				return nil, apiErr(err)
			}
			if changed {
				if _, err := h.store.AddSecretLog(ctx, store.SecretLog{SecretID: x.ID, Kind: store.SecretLogReplace, Reason: strings.Join(replaced, "、"), Days: -1}); err != nil {
					return nil, err
				}
			}
			for _, m := range marks {
				if _, err := h.store.AddSecretLog(ctx, m); err != nil {
					return nil, err
				}
			}
			h.secretsChanged(ctx)
			return h.secretDetail(ctx, x)
		})
	huma.Register(api, huma.Operation{OperationID: "rename-secret", Method: http.MethodPost, Path: "/api/secrets/{ref}/rename", Tags: tag,
		Summary: "改名（R-326）：名称只给人看，编号 S-x 不变，按编号取用不受影响"},
		func(ctx context.Context, in *struct {
			Ref  string `path:"ref"`
			Body struct {
				Name string `json:"name" minLength:"1" doc:"新名称"`
			}
		}) (*secretOut, error) {
			x, err := h.getSecretRef(ctx, in.Ref)
			if err != nil {
				return nil, apiErr(err)
			}
			old := x.Name
			x.Name = strings.TrimSpace(in.Body.Name)
			if x.Name == old {
				return nil, huma.Error400BadRequest("名称没有改动")
			}
			if x, err = h.store.SaveSecret(ctx, x); err != nil {
				return nil, apiErr(err)
			}
			if _, err := h.store.AddSecretLog(ctx, store.SecretLog{SecretID: x.ID, Kind: store.SecretLogRename, Reason: renameText(old, x.Name), Days: -1}); err != nil {
				return nil, err
			}
			h.secretsChanged(ctx)
			return h.secretDetail(ctx, x)
		})
	huma.Register(api, huma.Operation{OperationID: "mark-secret-fields", Method: http.MethodPost, Path: "/api/secrets/{ref}/plain", Tags: tag,
		Summary: "改值的敏感标记（R-318）：不敏感的值明文显示、不在纸鸢里隐去；值和授权不变"},
		func(ctx context.Context, in *struct {
			Ref  string `path:"ref"`
			Body struct {
				Plain map[string]bool `json:"plain" doc:"环境变量名 → 是否不敏感；没写的保持原样"`
			}
		}) (*secretOut, error) {
			x, err := h.getSecretRef(ctx, in.Ref)
			if err != nil {
				return nil, apiErr(err)
			}
			marks, err := applyPlain(&x, in.Body.Plain)
			if err != nil {
				return nil, huma.Error400BadRequest(err.Error())
			}
			if len(marks) > 0 {
				if x, err = h.store.SaveSecret(ctx, x); err != nil {
					return nil, apiErr(err)
				}
				for _, m := range marks {
					if _, err := h.store.AddSecretLog(ctx, m); err != nil {
						return nil, err
					}
				}
				h.secretsChanged(ctx)
			}
			return h.secretDetail(ctx, x)
		})
	huma.Register(api, huma.Operation{OperationID: "revoke-secret", Method: http.MethodPost, Path: "/api/secrets/{ref}/revoke", Tags: tag, Summary: "撤销授权：之后的取用一律拒绝；值保留，可重新授权"},
		func(ctx context.Context, in *struct {
			Ref string `path:"ref"`
		}) (*secretOut, error) {
			x, err := h.getSecretRef(ctx, in.Ref)
			if err != nil {
				return nil, apiErr(err)
			}
			if x.Status != store.SecretActive {
				return nil, huma.Error400BadRequest("凭据「" + x.Name + "」不在生效中，不用撤销")
			}
			n := time.Now()
			x.RevokedAt = &n
			if x, err = h.store.SaveSecret(ctx, x); err != nil {
				return nil, apiErr(err)
			}
			if _, err := h.store.AddSecretLog(ctx, store.SecretLog{SecretID: x.ID, Kind: store.SecretLogRevoke, Days: -1}); err != nil {
				return nil, err
			}
			h.broker.publish("secrets")
			return h.secretDetail(ctx, x)
		})
	huma.Register(api, huma.Operation{OperationID: "reject-secret-request", Method: http.MethodPost, Path: "/api/secrets/{ref}/reject", Tags: tag, Summary: "拒绝凭据申请（可附言），答复送回申请方"},
		func(ctx context.Context, in *struct {
			Ref  string `path:"ref"`
			Body struct {
				Note string `json:"note,omitempty" doc:"附言"`
			}
		}) (*decisionOut, error) {
			x, err := h.getSecretRef(ctx, in.Ref)
			if err != nil {
				return nil, apiErr(err)
			}
			d, err := h.rejectSecretRequest(ctx, x, in.Body.Note)
			if err != nil {
				return nil, err
			}
			return &decisionOut{Body: d}, nil
		})
}

// relabelFirstGrant 把网页上新建凭据时记的那条「保存并授权」改记为「新建」。
func (h *Hub) relabelFirstGrant(ctx context.Context, id string) error {
	_, err := h.store.DB().ExecContext(ctx, `UPDATE secret_logs SET kind = ? WHERE secret_id = ? AND kind = ?`, store.SecretLogCreate, id, store.SecretLogGrant)
	return err
}

// ---------- 设备接口（ks secret / MCP） ----------

// agentSecret 是给会话看的凭据：只有名字、状态、字段名、授权范围，没有值。
type agentSecret struct {
	Code      string              `json:"code" doc:"稳定编号 S-x：命令里写它（ks secret run S-x -- <命令>），改名不影响"`
	Run       string              `json:"run" doc:"推荐的取用写法"`
	Name      string              `json:"name" doc:"名称：只给人看，可能被改"`
	Purpose   string              `json:"purpose"`
	Status    string              `json:"status" enum:"active,pending,expired,revoked"`
	Fields    []store.SecretField `json:"fields"`
	Projects  []string            `json:"projects" doc:"授权的项目名"`
	Allowed   []string            `json:"allowed" doc:"允许的命令；空 = 不限命令"`
	Disk      bool                `json:"disk"`
	ExpiresAt *time.Time          `json:"expires_at,omitempty"`
	Request   string              `json:"request,omitempty" doc:"等待处理的申请 D-x"`
}

func toAgentSecret(x store.Secret) agentSecret {
	a := agentSecret{Code: x.Code, Run: "ks secret run " + x.Code + " -- <命令>", Name: x.Name, Purpose: x.Purpose, Status: x.Status, Fields: x.Fields, Projects: x.ProjectNames, Allowed: x.Allowed, Disk: x.Disk, ExpiresAt: x.ExpiresAt}
	if x.RequestPending {
		a.Request = x.RequestCode
	}
	return a
}

// agentSessionOf 认出发起调用的会话：必须存在、在调用的设备上、属于某个项目。
func (h *Hub) agentSessionOf(ctx context.Context, key string) (store.Session, error) {
	x, err := h.store.GetSession(ctx, strings.TrimSpace(key))
	if err != nil {
		return x, huma.Error400BadRequest("认不出当前会话：在 Claude Code / Codex 会话里运行，或用 --session 指定")
	}
	if dev, ok := deviceFrom(ctx); ok && dev.ID != x.DeviceID {
		return x, huma.Error403Forbidden("会话不在这台设备上，不能代它取用凭据")
	}
	if x.ProjectID == "" {
		return x, huma.Error400BadRequest("当前会话不属于任何项目，凭据按项目授权，在项目目录里运行")
	}
	return x, nil
}

func secretInProject(x store.Secret, projectID string) bool {
	return slices.Contains(x.Projects, projectID)
}

type secretRequestIn struct {
	SessionKey  string          `json:"session_key" minLength:"1"`
	Name        string          `json:"name" minLength:"1" doc:"凭据名，例如「Cloudflare 部署」；ks secret run 按它取用"`
	Purpose     string          `json:"purpose" doc:"用途：拿它做什么（用户在申请里看到）"`
	Requirement string          `json:"requirement,omitempty" doc:"关联需求 R-x"`
	Days        *int            `json:"days,omitempty" doc:"建议的有效期（天），默认 7；0 = 长期"`
	Allowed     []string        `json:"allowed,omitempty" doc:"建议允许的命令，如 \"wrangler pages deploy *\"（* 单独放最后 = 后面可跟任意参数）；不写 = 不限命令"`
	Fields      []secretFieldIn `json:"fields" doc:"要的每个值：环境变量名、显示名、获取方式（按服务写清步骤，指导用户快速取到）"`
}

type secretRequestOut struct {
	Body struct {
		Decision *store.Decision `json:"decision,omitempty"`
		Existing bool            `json:"existing" doc:"已有等待中的申请或凭据已可用，没有新建"`
		Message  string          `json:"message"`
	}
}

type secretUseOut struct {
	Body struct {
		LogID string            `json:"log_id" doc:"这次取用的记录 ID，命令结束后用它上报退出码"`
		Name  string            `json:"name"`
		Env   map[string]string `json:"env" doc:"环境变量名 → 值：只交给 ks 进程放进子进程环境，不要打印"`
	}
}

func (h *Hub) registerSecretsAgent(api huma.API) {
	tag := []string{"agent"}
	huma.Register(api, huma.Operation{OperationID: "agent-secret-request", Method: http.MethodPost, Path: "/api/agent/secrets/request", Tags: tag,
		Summary: "会话申请凭据（R-295）：进用户的「待处理」，用户填值并授权或拒绝后结果送回"},
		func(ctx context.Context, in *struct{ Body secretRequestIn }) (*secretRequestOut, error) {
			return h.secretRequest(ctx, in.Body)
		})
	huma.Register(api, huma.Operation{OperationID: "agent-secret-amend", Method: http.MethodPost, Path: "/api/agent/secrets/amend", Tags: tag,
		Summary: "会话申请修改已有凭据的授权（R-320）：允许的命令、有效期、项目、写入磁盘；进用户的「待处理」，批准前原授权不变"},
		func(ctx context.Context, in *struct{ Body secretAmendIn }) (*secretRequestOut, error) {
			return h.secretAmend(ctx, in.Body)
		})
	huma.Register(api, huma.Operation{OperationID: "agent-list-secrets", Method: http.MethodGet, Path: "/api/agent/secrets", Tags: tag, Summary: "当前项目能用的凭据（没有值）"},
		func(ctx context.Context, in *struct {
			SessionKey string `query:"session_key"`
		}) (*struct {
			Body struct {
				Secrets []agentSecret `json:"secrets"`
			}
		}, error) {
			me, err := h.agentSessionOf(ctx, in.SessionKey)
			if err != nil {
				return nil, err
			}
			list, err := h.store.ListSecrets(ctx)
			if err != nil {
				return nil, err
			}
			out := &struct {
				Body struct {
					Secrets []agentSecret `json:"secrets"`
				}
			}{}
			out.Body.Secrets = []agentSecret{}
			for _, x := range list {
				if secretInProject(x, me.ProjectID) || (x.Ask != nil && x.Ask.ProjectID == me.ProjectID) {
					out.Body.Secrets = append(out.Body.Secrets, toAgentSecret(x))
				}
			}
			return out, nil
		})
	huma.Register(api, huma.Operation{OperationID: "agent-secret-use", Method: http.MethodPost, Path: secretUsePath, Tags: tag,
		Summary: "ks secret run / write 取值：授权有效、项目匹配、命令在允许范围内才返回值；否则拒绝并记录"},
		func(ctx context.Context, in *struct {
			Body struct {
				SessionKey  string   `json:"session_key" minLength:"1"`
				Name        string   `json:"name" minLength:"1"`
				Argv        []string `json:"argv,omitempty" doc:"要运行的命令（ks secret run）"`
				Program     string   `json:"program,omitempty" doc:"ks 解析出的程序绝对路径（exec.LookPath）；按它判定允许的命令、记进取用记录"`
				ViaPath     bool     `json:"via_path,omitempty" doc:"程序是按 PATH 找到的（命令里写的是裸程序名）"`
				Path        string   `json:"path,omitempty" doc:"要写入的文件绝对路径（ks secret write）"`
				Requirement string   `json:"requirement,omitempty" doc:"这次取用为的需求 R-x；不写时取会话正在处理的需求"`
			}
		}) (*secretUseOut, error) {
			b := in.Body
			return h.secretUse(ctx, b.SessionKey, b.Name, b.Argv, program{path: b.Program, viaPath: b.ViaPath}, b.Path, b.Requirement)
		})
	huma.Register(api, huma.Operation{OperationID: "agent-secret-finish", Method: http.MethodPost, Path: "/api/agent/secrets/uses/{id}/finish", Tags: tag, Summary: "上报取用命令的退出码"},
		func(ctx context.Context, in *struct {
			ID   string `path:"id"`
			Body struct {
				SessionKey string `json:"session_key" minLength:"1" doc:"发起这次取用的会话"`
				ExitCode   int    `json:"exit_code"`
			}
		}) (*okBody, error) {
			// 只有发起这次取用的会话（在它所在的设备上）能上报，且只能上报一次（FinishSecretLog 只改还没上报的）
			me, err := h.agentSessionOf(ctx, in.Body.SessionKey)
			if err != nil {
				return nil, err
			}
			l, err := h.store.GetSecretLog(ctx, in.ID)
			if err != nil {
				return nil, apiErr(err)
			}
			if l.SessionKey != me.Key || (l.Kind != store.SecretLogUse && l.Kind != store.SecretLogWrite) {
				return nil, huma.Error403Forbidden("这条取用记录不是当前会话发起的，不能上报结果")
			}
			if _, err := h.store.FinishSecretLog(ctx, in.ID, in.Body.ExitCode); err != nil {
				return nil, apiErr(err)
			}
			h.broker.publish("secrets")
			return ok(), nil
		})
}

// secretRequest 处理会话的凭据申请。
func (h *Hub) secretRequest(ctx context.Context, b secretRequestIn) (*secretRequestOut, error) {
	me, err := h.agentSessionOf(ctx, b.SessionKey)
	if err != nil {
		return nil, err
	}
	name := strings.TrimSpace(b.Name)
	purpose := strings.TrimSpace(b.Purpose)
	if purpose == "" {
		return nil, huma.Error400BadRequest("申请要写用途（purpose / --purpose）：拿这个凭据做什么，用户在申请里看到")
	}
	fields, err := normSecretFields(b.Fields)
	if err != nil {
		return nil, huma.Error400BadRequest(err.Error() + "（--field 环境变量名|显示名|获取方式，可重复）")
	}
	days := defaultSecretDays
	if b.Days != nil {
		days = *b.Days
	}
	if err := checkSecretDays(days); err != nil {
		return nil, huma.Error400BadRequest(err.Error())
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
	ask := &store.SecretAsk{ProjectID: me.ProjectID, Days: days, Allowed: normAllowed(b.Allowed)}
	out := &secretRequestOut{}
	x, err := h.store.GetSecret(ctx, name)
	switch {
	case errors.Is(err, store.ErrNotFound):
		x, err = h.store.CreateSecret(ctx, store.Secret{Name: name, Purpose: purpose, Fields: fields, Projects: []string{me.ProjectID}, Allowed: ask.Allowed,
			RequirementID: reqID, RequestedBy: me.Key, Ask: ask})
		if err != nil {
			return nil, apiErr(err)
		}
	case err != nil:
		return nil, apiErr(err)
	default:
		if x.RequestPending {
			d, _ := h.store.GetDecision(ctx, x.RequestID)
			out.Body.Decision, out.Body.Existing = &d, true
			out.Body.Message = fmt.Sprintf("凭据「%s」已有申请 %s 等待用户处理，没有重复提。结果会送到提申请的会话。", x.Name, x.RequestCode)
			return out, nil
		}
		// 同名凭据没授权给本项目：不能借申请往别人的凭据里加值、改申请内容
		if !secretInProject(x, me.ProjectID) && (x.Ask == nil || x.Ask.ProjectID != me.ProjectID) {
			return nil, huma.Error403Forbidden(fmt.Sprintf("已有名为「%s」的凭据，没有授权给本项目（%s）；换一个名字申请。确实要共用这个凭据时，请用户在凭据页把本项目加进授权", x.Name, me.ProjectName))
		}
		missing := false
		for _, f := range fields {
			i := slices.IndexFunc(x.Fields, func(g store.SecretField) bool { return g.Env == f.Env })
			if i < 0 {
				x.Fields = append(x.Fields, f) // 新要的值：授权时一起填；申请被拒绝或撤回时去掉
				ask.Added = append(ask.Added, f.Env)
				missing = true
			} else if !x.Fields[i].Filled {
				missing = true
			}
		}
		if !missing && x.Status == store.SecretActive && secretInProject(x, me.ProjectID) {
			out.Body.Existing = true
			out.Body.Message = fmt.Sprintf("凭据「%s」已授权给本项目（%s），不用申请。用 ks secret run %s -- <命令> 运行；命令不在允许范围内时会被拒绝。", x.Name, secretScope(x), x.Code)
			out.Body.Message += "要改允许的命令、有效期、项目或写入磁盘，用 ks secret amend（MCP secret_amend）申请修改授权。"
			return out, nil
		}
		if x.Purpose == "" {
			x.Purpose = purpose
		}
		x.RequestedBy, x.Ask = me.Key, ask
		if reqID != "" {
			x.RequirementID = reqID
		}
	}
	labels := make([]string, len(fields))
	for i, f := range fields {
		labels[i] = f.Label
	}
	d, err := h.store.CreateDecision(ctx, store.Decision{
		SessionKey: me.Key, ProjectID: me.ProjectID, RequirementID: reqID, Kind: store.KindDecision,
		Question: name + "：" + joinAnd(labels), Context: purpose,
		Approval: true, Options: []string{"批准", "拒绝"}, Risk: store.RiskHigh, Hard: "permission",
		RequestKind: store.RequestSecret, FromRole: me.Role,
	})
	if err != nil {
		return nil, apiErr(err)
	}
	x.RequestID = d.ID
	if x, err = h.store.SaveSecret(ctx, x); err != nil {
		return nil, apiErr(err)
	}
	if _, err := h.store.AddSecretLog(ctx, store.SecretLog{SecretID: x.ID, Kind: store.SecretLogRequest, SessionKey: me.Key, RequirementID: reqID, Days: days}); err != nil {
		return nil, err
	}
	h.broker.publish("decisions", "sessions", "requirements", "secrets")
	out.Body.Decision = &d
	out.Body.Message = fmt.Sprintf("已申请（%s）：凭据「%s」，在用户的「待处理」里等待填写。用户保存并授权或拒绝后，结果会送到你这里；授权后用 ks secret run %s -- <命令> 运行。不要再让用户在终端里输入这些值。", d.Code, x.Name, x.Code)
	return out, nil
}

// joinAnd：「a 和 b」「a、b 和 c」。
func joinAnd(xs []string) string {
	switch len(xs) {
	case 0:
		return ""
	case 1:
		return xs[0]
	}
	return strings.Join(xs[:len(xs)-1], "、") + " 和 " + xs[len(xs)-1]
}

// secretUseReq：取用记在哪条需求上——明确给的；否则会话关联、进行中的那一条（只有一条时）；否则凭据申请时关联的。
func (h *Hub) secretUseReq(ctx context.Context, me store.Session, x store.Secret, code string) string {
	if code = strings.TrimSpace(code); code != "" {
		if r, err := h.store.GetRequirement(ctx, code); err == nil {
			return r.ID
		}
	}
	var doing []string
	for _, r := range me.Requirements {
		if r.Status == store.ReqInProgress {
			doing = append(doing, r.ID)
		}
	}
	if len(doing) == 1 {
		return doing[0]
	}
	return x.RequirementID
}

// secretUse 判断能否取用；能就记一条进行中的取用并返回值，不能就记一条被拒并说明原因。
func (h *Hub) secretUse(ctx context.Context, key, name string, argv []string, prog program, path, reqCode string) (*secretUseOut, error) {
	me, err := h.agentSessionOf(ctx, key)
	if err != nil {
		return nil, err
	}
	x, err := h.store.GetSecret(ctx, name)
	if errors.Is(err, store.ErrNotFound) {
		return nil, huma.Error404NotFound(fmt.Sprintf("没有编号或名称为「%s」的凭据。ks secret list 看本项目能用的（命令里写编号 S-x）；没有就用 ks secret request 申请，不要让用户在终端里输入", strings.TrimSpace(name)))
	} else if err != nil {
		return nil, apiErr(err)
	}
	write := path != ""
	if write && !filepath.IsAbs(path) {
		return nil, huma.Error400BadRequest("写入路径要用绝对路径")
	}
	if !write && len(argv) == 0 {
		return nil, huma.Error400BadRequest("用法：ks secret run <凭据> -- <命令>")
	}
	if !write {
		if prog.path == "" { // 旧版 ks 没上报解析结果：按命令里写的判
			prog = program{path: argv[0], viaPath: !strings.Contains(argv[0], "/")}
		} else if !filepath.IsAbs(prog.path) {
			return nil, huma.Error400BadRequest("程序路径要用绝对路径")
		}
		argv = append([]string{prog.path}, argv[1:]...) // 记录写实际运行的程序
	}
	vals, openErr := h.secretValues(x)
	l := store.SecretLog{SecretID: x.ID, Kind: store.SecretLogUse, SessionKey: me.Key, RequirementID: h.secretUseReq(ctx, me, x, reqCode), Days: -1}
	if write {
		l.Kind, l.Path = store.SecretLogWrite, path
	} else {
		l.Command = h.redactText(commandLine(argv), sensitiveValues(x, vals))
	}
	deny, hint := "", ""
	switch {
	case x.Status == store.SecretPending:
		deny, hint = secretDenyPending, "用户还没填写这个凭据的值"
		if x.RequestPending {
			hint += "（申请 " + x.RequestCode + " 等待处理）"
		}
	case x.Status == store.SecretRevoked:
		deny, hint = secretDenyRevoked, fmt.Sprintf("用户已撤销授权；确实需要时用 ks secret amend %s --reason <理由> 申请重新授权，写明为什么", x.Code)
	case x.Status == store.SecretExpired:
		deny, hint = secretDenyExpired, "授权已于 "+x.ExpiresAt.Local().Format("01-02 15:04")+fmt.Sprintf(" 到期；确实需要时用 ks secret amend %s --days <天数> --reason <理由> 申请重新授权", x.Code)
	case !secretInProject(x, me.ProjectID):
		deny, hint = secretDenyProject, fmt.Sprintf("这个凭据只授权给 %s，当前会话在「%s」；需要时用 ks secret request 申请", strings.Join(x.ProjectNames, "、"), me.ProjectName)
	case write && !x.Disk:
		deny, hint = secretDenyDisk, fmt.Sprintf("这个凭据没有授权写入磁盘，改用 ks secret run 把值作为命令的环境变量；确实要写文件时用 ks secret amend %s --disk --reason <理由> 申请修改授权，不要让用户去凭据页手动改", x.Code)
	case !write && !commandAllowed(x.Allowed, argv, prog):
		deny, hint = secretDenyCommand, "允许的命令："+strings.Join(x.Allowed, "、")+fmt.Sprintf("。不要换个写法绕过；确实需要时用 ks secret amend %s --add-allow <命令> --reason <理由> 申请修改授权（写错的用 --remove-allow 去掉；一个参数里含空格时用 * 代替其中的空格，不要拆成多段），不要让用户去凭据页手动改", x.Code)
	case openErr != nil:
		deny, hint = secretDenyUnreadable, openErr.Error()
	}
	if deny != "" {
		l.Result, l.Reason = store.SecretDenied, deny
		if _, err := h.store.AddSecretLog(ctx, l); err != nil {
			return nil, err
		}
		h.broker.publish("secrets")
		return nil, huma.Error403Forbidden(fmt.Sprintf("凭据「%s」拒绝取用：%s。%s", x.Name, deny, hint))
	}
	l.Result = store.SecretRunning
	saved, err := h.store.AddSecretLog(ctx, l)
	if err != nil {
		return nil, err
	}
	h.broker.publish("secrets")
	out := &secretUseOut{}
	out.Body.LogID, out.Body.Name, out.Body.Env = saved.ID, x.Name, vals
	return out, nil
}

// renameText 是改名在时间线和答复里的写法：「旧名」→「新名」。
func renameText(old, name string) string { return "「" + old + "」→「" + name + "」" }
