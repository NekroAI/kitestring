package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/NekroAI/kitestring/internal/blocks"
	"github.com/NekroAI/kitestring/internal/protocol"
)

// 凭据库（R-295，D-1434 / D-1438）：第三方 API 凭据。会话拿不到明文，只能用 ks secret run 让值作为一条命令的环境变量出现。
// 这一层只管存取与记录：加密、解密、脱敏、授权判断都在 hub（主密钥不进数据库，store 只见到密文）。

// 凭据状态，按 cipher / revoked_at / expires_at 推算（D-1438：生效中 / 待填写 / 已失效 / 已撤回）。
const (
	SecretActive  = "active"  // 生效中
	SecretPending = "pending" // 待填写：会话申请了、还没填值
	SecretExpired = "expired" // 已失效：过了有效期
	SecretRevoked = "revoked" // 已撤回：你撤销了授权
)

// 取用记录与授权变更的类别（secret_logs.kind）。
const (
	SecretLogUse     = "use"     // ks secret run 取用
	SecretLogWrite   = "write"   // ks secret write 写入磁盘
	SecretLogRequest = "request" // 会话申请
	SecretLogAmend   = "amend"   // 会话申请修改授权（R-320），Reason 记会话写的理由
	SecretLogGrant   = "grant"   // 你保存并授权了申请
	SecretLogReject  = "reject"  // 你拒绝了申请
	SecretLogCreate  = "create"  // 你新建了凭据
	SecretLogReplace = "replace" // 你替换了值
	SecretLogEdit    = "edit"    // 你修改了授权
	SecretLogRegrant = "regrant" // 你重新授权（已失效 / 已撤回之后）
	SecretLogRevoke  = "revoke"  // 你撤销了授权
	// R-318：改了某个值的敏感标记，Reason 记是哪个值（显示名）
	SecretLogPlain     = "plain"     // 你把某个值标为不敏感
	SecretLogSensitive = "sensitive" // 你把某个值标为敏感
	// R-326：改名，Reason 记「旧名 → 新名」；按修改授权申请改的带 SessionKey
	SecretLogRename = "rename"
)

// 取用结果（secret_logs.result）。
const (
	SecretRunning = "running" // 已放行，命令还没上报结果
	SecretOK      = "ok"      // 已完成
	SecretFailed  = "failed"  // 失败（退出码非 0）
	SecretDenied  = "denied"  // 已拒绝（reason 写原因）
)

// SecretField 是凭据里的一个值的说明（不含值）。
type SecretField struct {
	Env    string `json:"env" doc:"环境变量名，例如 CLOUDFLARE_API_TOKEN"`
	Label  string `json:"label" doc:"显示名，例如 API Token"`
	How    string `json:"how,omitempty" doc:"获取方式：申请的会话按服务写清步骤，指导用户快速取到"`
	Filled bool   `json:"filled" doc:"已填写值"`
	// 敏感标记（R-318）：默认敏感（老数据没有这个字段也按敏感）。不敏感的值（用户名、Account ID 之类）在凭据页明文显示、
	// 不进全局脱敏；注入命令的规则和加密存储不变。
	Plain bool `json:"plain,omitempty" doc:"不敏感：明文显示、不在纸鸢里隐去；默认 false = 敏感"`
	// Value 只在网页接口里给不敏感的值填上（hub 解密后补），永不存库：存库前清空。
	Value string `json:"value,omitempty" doc:"不敏感的值的明文（只在网页的凭据接口里给出）；敏感的值永不返回"`
}

// storedFields 是存库用的字段说明：去掉明文（Value 只在接口里临时补上，不能落进 fields 列）。
func storedFields(fs []SecretField) []SecretField {
	out := make([]SecretField, len(fs))
	for i, f := range fs {
		f.Value = ""
		out[i] = f
	}
	return out
}

// Secret 是一条凭据。值只以密文存在 Cipher 里，任何接口都不返回。
type Secret struct {
	ID              string        `json:"id"`
	Code            string        `json:"code" doc:"稳定编号 S-x（R-326）：ks secret run / amend / write 认它，改名不影响"`
	Name            string        `json:"name" doc:"名称：只给人看，可以改"`
	Purpose         string        `json:"purpose"`
	Fields          []SecretField `json:"fields"`
	Projects        []string      `json:"projects" doc:"授权的项目 ID"`
	ProjectNames    []string      `json:"project_names" doc:"授权的项目名，与 projects 一一对应"`
	Allowed         []string      `json:"allowed" doc:"允许的命令；空 = 不限命令"`
	Disk            bool          `json:"disk" doc:"允许写入磁盘（ks secret write）"`
	ExpiresAt       *time.Time    `json:"expires_at,omitempty" doc:"授权到期时间；省略表示长期"`
	GrantedAt       *time.Time    `json:"granted_at,omitempty"`
	RevokedAt       *time.Time    `json:"revoked_at,omitempty"`
	RequirementID   string        `json:"requirement_id,omitempty"`
	RequirementCode string        `json:"requirement_code,omitempty"`
	RequestID       string        `json:"request_id,omitempty" doc:"最近一次申请的决策 ID"`
	RequestCode     string        `json:"request_code,omitempty" doc:"最近一次申请的决策编号 D-x"`
	RequestPending  bool          `json:"request_pending" doc:"有一条申请等待你填写 / 授权"`
	RequestedBy     string        `json:"requested_by,omitempty" doc:"最近一次申请的会话键"`
	Ask             *SecretAsk    `json:"ask,omitempty" doc:"最近一次申请的内容：申请方的项目、建议有效期、要的命令"`
	LastUsedAt      *time.Time    `json:"last_used_at,omitempty"`
	LastUse         *SecretLog    `json:"last_use,omitempty" doc:"最近一次取用（含被拒）"`
	Status          string        `json:"status" enum:"active,pending,expired,revoked" doc:"active 生效中 / pending 待填写 / expired 已失效 / revoked 已撤回"`
	CreatedAt       time.Time     `json:"created_at"`
	UpdatedAt       time.Time     `json:"updated_at"`

	Cipher string `json:"-"`
	// Retired 是替换掉的旧值（R-325）的密文：旧值作废、不能取用，只用来继续隐去已存记录里出现的旧值。任何接口都不返回。
	Retired string `json:"-"`
}

// StatusAt 推算凭据在某时刻的状态。
func (x Secret) StatusAt(t time.Time) string {
	switch {
	case x.Cipher == "":
		return SecretPending
	case x.RevokedAt != nil:
		return SecretRevoked
	case x.ExpiresAt != nil && !t.Before(*x.ExpiresAt):
		return SecretExpired
	}
	return SecretActive
}

// SecretAsk 是会话申请时提的授权范围。只是建议：你在抽屉里改过、保存授权后才生效。
type SecretAsk struct {
	ProjectID   string   `json:"project_id,omitempty" doc:"申请方所在的项目"`
	ProjectName string   `json:"project_name,omitempty"`
	Days        int      `json:"days" doc:"建议的有效期（天），0 = 长期；修改授权申请里 -1 = 到期时间不变"`
	Allowed     []string `json:"allowed" doc:"要的命令；空 = 没限定。修改授权申请里是改后的完整列表（空 = 不限命令）"`
	Added       []string `json:"added,omitempty" doc:"这次申请给已有凭据新加的值（环境变量名）；申请被拒绝或撤回时没填的随之去掉"`
	// 修改授权申请（R-320）：会话对已有凭据提的改后授权范围（完整的一份，改前就是凭据当前的授权）。批准前凭据的授权不变。
	Mode     string   `json:"mode,omitempty" enum:",amend" doc:"amend = 修改授权申请；空 = 申请凭据（填值 / 续期 / 补值）"`
	Reason   string   `json:"reason,omitempty" doc:"修改授权申请的理由"`
	Projects []string `json:"projects,omitempty" doc:"修改授权申请：改后的授权项目 ID"`
	Disk     bool     `json:"disk,omitempty" doc:"修改授权申请：改后是否允许写入磁盘"`
	// 替换值（R-325）：修改授权申请里要替换的值。用户在抽屉里填新值后才能保存并授权；申请里没有值。
	Replace []SecretReplace `json:"replace,omitempty" doc:"修改授权申请：要替换的值（环境变量名和获取方式）；用户填好新值才能保存并授权"`
	// 改名（R-326）：修改授权申请里提出的新名称；空 = 不改名。批准后生效。
	Name string `json:"name,omitempty" doc:"修改授权申请：提出的新名称；空 = 不改名"`
}

// SecretReplace 是修改授权申请里要替换的一个值（R-325）：不含值，只有环境变量名和获取新值的方式。
type SecretReplace struct {
	Env string `json:"env" doc:"要替换的值的环境变量名"`
	How string `json:"how,omitempty" doc:"新值的获取方式；空 = 沿用这个值原来的获取方式"`
}

// AskAmend 是修改授权申请（R-320）的模式名。
const AskAmend = "amend"

// IsAmend：这条申请是修改授权申请。
func (a *SecretAsk) IsAmend() bool { return a != nil && a.Mode == AskAmend }

// SecretLog 是一条取用记录或授权变更。
type SecretLog struct {
	ID              string     `json:"id"`
	SecretID        string     `json:"secret_id"`
	Kind            string     `json:"kind" enum:"use,write,request,amend,grant,reject,create,replace,edit,regrant,revoke,plain,sensitive,rename"`
	At              time.Time  `json:"at"`
	SessionKey      string     `json:"session_key,omitempty"`
	SessionTitle    string     `json:"session_title,omitempty" doc:"会话当时的标题"`
	SessionRole     string     `json:"session_role,omitempty" doc:"会话当时的身份"`
	RequirementID   string     `json:"requirement_id,omitempty"`
	RequirementCode string     `json:"requirement_code,omitempty"`
	Command         string     `json:"command,omitempty" doc:"取用的命令；已知值已换成「[已隐去]」"`
	Path            string     `json:"path,omitempty" doc:"写入磁盘的路径"`
	Result          string     `json:"result,omitempty" enum:",running,ok,failed,denied"`
	ExitCode        int        `json:"exit_code"`
	Reason          string     `json:"reason,omitempty" doc:"被拒的原因；改敏感标记时是哪个值（显示名）；修改授权申请的理由、批准时改了什么；老数据里拒绝申请的附言也在这里"`
	Note            string     `json:"note,omitempty" doc:"你保存授权、拒绝申请时写的附言（R-321，已知值已隐去）；有申请时随结果送给申请方"`
	Days            int        `json:"days" doc:"授权时选的有效期（天）：0 = 长期，-1 = 不适用"`
	FinishedAt      *time.Time `json:"finished_at,omitempty"`
}

// SecretCode 是凭据的稳定编号 S-<seq>（R-326）。
func SecretCode(seq int) string { return "S-" + strconv.Itoa(seq) }

// secretCodeRe 认编号 S-x（不分大小写）。
var secretCodeRe = regexp.MustCompile(`^[Ss]-(\d+)$`)

// IsSecretCode：这段文字是凭据编号的写法（S-x）。名称不能写成这样，免得和编号混淆。
func IsSecretCode(s string) bool { return secretCodeRe.MatchString(strings.TrimSpace(s)) }

// AmbiguousSecretError：按名称找凭据时对上了多条（R-326）。不静默取第一条，列出各自的编号。
type AmbiguousSecretError struct {
	Name  string
	Codes []string
}

func (e *AmbiguousSecretError) Error() string {
	return fmt.Sprintf("名称「%s」对应多条凭据：%s。用编号指定，例如 ks secret run %s -- <命令>", e.Name, strings.Join(e.Codes, "、"), e.Codes[0])
}

const secretSelect = `SELECT x.id, x.seq, x.name, x.purpose, x.fields, x.cipher, x.projects, x.allowed, x.disk, x.expires_at, x.granted_at, x.revoked_at,
	x.requirement_id, COALESCE(r.seq, 0), x.request_decision, COALESCE(d.seq, 0), COALESCE(d.status, ''), x.requested_by, x.request, x.last_used_at, x.created_at, x.updated_at, x.retired
	FROM secrets x
	LEFT JOIN requirements r ON r.id = x.requirement_id
	LEFT JOIN decisions d ON d.id = x.request_decision`

func scanSecret(row interface{ Scan(...any) error }) (Secret, error) {
	var x Secret
	var fields, projects, allowed, exp, granted, revoked, used, created, updated, decStatus, ask string
	var rseq, dseq int
	var seq int
	if err := row.Scan(&x.ID, &seq, &x.Name, &x.Purpose, &fields, &x.Cipher, &projects, &allowed, &x.Disk, &exp, &granted, &revoked,
		&x.RequirementID, &rseq, &x.RequestID, &dseq, &decStatus, &x.RequestedBy, &ask, &used, &created, &updated, &x.Retired); err != nil {
		return x, err
	}
	x.Code = SecretCode(seq)
	x.Fields, x.Projects, x.Allowed = []SecretField{}, []string{}, []string{}
	if v := blocks.Decode[[]SecretField](fields); v != nil {
		x.Fields = *v
	}
	if v := blocks.Decode[[]string](projects); v != nil {
		x.Projects = *v
	}
	if v := blocks.Decode[[]string](allowed); v != nil {
		x.Allowed = *v
	}
	x.ExpiresAt = parseNullTS(sql.NullString{String: exp, Valid: exp != ""})
	x.GrantedAt = parseNullTS(sql.NullString{String: granted, Valid: granted != ""})
	x.RevokedAt = parseNullTS(sql.NullString{String: revoked, Valid: revoked != ""})
	x.LastUsedAt = parseNullTS(sql.NullString{String: used, Valid: used != ""})
	x.CreatedAt, x.UpdatedAt = parseTS(created), parseTS(updated)
	if rseq > 0 {
		x.RequirementCode = reqCode(rseq)
	}
	if dseq > 0 {
		x.RequestCode = decCode(dseq)
		x.RequestPending = decStatus == DecPending
	}
	x.Ask = blocks.Decode[SecretAsk](ask)
	x.Status = x.StatusAt(time.Now())
	return x, nil
}

// fillSecretProjects 补上授权项目的名字；项目已删除的写 ID。
func (s *Store) fillSecretProjects(ctx context.Context, x *Secret) {
	x.ProjectNames = make([]string, len(x.Projects))
	for i, id := range x.Projects {
		x.ProjectNames[i] = s.projectName(ctx, id)
	}
	if x.Ask != nil && x.Ask.ProjectID != "" {
		x.Ask.ProjectName = s.projectName(ctx, x.Ask.ProjectID)
	}
	if x.Ask != nil && x.Ask.Allowed == nil {
		x.Ask.Allowed = []string{}
	}
}

// projectName 是项目的名字；项目已删除时返回 ID。
func (s *Store) projectName(ctx context.Context, id string) string {
	var name string
	if err := s.db.QueryRowContext(ctx, `SELECT name FROM projects WHERE id = ?`, id).Scan(&name); err == nil {
		return name
	}
	return id
}

// secretNameTaken：同名（不分大小写）的凭据已存在。exceptID 是自己。
// SecretNameTaken：别的凭据（exceptID 之外）已经用了这个名称（不分大小写）。
func (s *Store) SecretNameTaken(ctx context.Context, name, exceptID string) (bool, error) {
	return s.secretNameTaken(ctx, name, exceptID)
}

func (s *Store) secretNameTaken(ctx context.Context, name, exceptID string) (bool, error) {
	var n int
	err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM secrets WHERE lower(name) = lower(?) AND id <> ?`, strings.TrimSpace(name), exceptID).Scan(&n)
	return n > 0, err
}

// checkSecretName：名称不能为空，也不能写成编号的样子（S-x）。
func checkSecretName(name string) error {
	if name == "" {
		return errors.New("凭据名不能为空")
	}
	if IsSecretCode(name) {
		return fmt.Errorf("名称不能写成编号的样子（%s）", name)
	}
	return nil
}

// CreateSecret 新建凭据，分配下一个编号 S-x。名字不能和已有的重复（按名称取用仍兼容）。
func (s *Store) CreateSecret(ctx context.Context, x Secret) (Secret, error) {
	x.Name = strings.TrimSpace(x.Name)
	if err := checkSecretName(x.Name); err != nil {
		return x, err
	}
	if taken, err := s.secretNameTaken(ctx, x.Name, ""); err != nil {
		return x, err
	} else if taken {
		return x, fmt.Errorf("已有名为「%s」的凭据", x.Name)
	}
	x.ID = protocol.NewID()
	n := now()
	_, err := s.db.ExecContext(ctx, `INSERT INTO secrets(id, seq, name, purpose, fields, cipher, projects, allowed, disk, expires_at, granted_at, revoked_at, requirement_id, request_decision, requested_by, request, created_at, updated_at)
		VALUES(?, (SELECT COALESCE(MAX(seq), 0) + 1 FROM secrets), ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		x.ID, x.Name, x.Purpose, blocks.Encode(storedFields(x.Fields)), x.Cipher, blocks.Encode(nonNil(x.Projects)), blocks.Encode(nonNil(x.Allowed)), x.Disk,
		tsOrEmpty(x.ExpiresAt), tsOrEmpty(x.GrantedAt), tsOrEmpty(x.RevokedAt), x.RequirementID, x.RequestID, x.RequestedBy, blocks.Encode(x.Ask), n, n)
	if err != nil {
		return x, err
	}
	return s.GetSecret(ctx, x.ID)
}

func nonNil[T any](v []T) []T {
	if v == nil {
		return []T{}
	}
	return v
}

// SaveSecret 写回凭据的可变字段（名字、用途、字段说明、密文、授权、申请）。
func (s *Store) SaveSecret(ctx context.Context, x Secret) (Secret, error) {
	x.Name = strings.TrimSpace(x.Name)
	if err := checkSecretName(x.Name); err != nil {
		return x, err
	}
	if taken, err := s.secretNameTaken(ctx, x.Name, x.ID); err != nil {
		return x, err
	} else if taken {
		return x, fmt.Errorf("已有名为「%s」的凭据", x.Name)
	}
	res, err := s.db.ExecContext(ctx, `UPDATE secrets SET name = ?, purpose = ?, fields = ?, cipher = ?, projects = ?, allowed = ?, disk = ?, expires_at = ?, granted_at = ?, revoked_at = ?,
		requirement_id = ?, request_decision = ?, requested_by = ?, request = ?, retired = ?, updated_at = ? WHERE id = ?`,
		x.Name, x.Purpose, blocks.Encode(storedFields(x.Fields)), x.Cipher, blocks.Encode(nonNil(x.Projects)), blocks.Encode(nonNil(x.Allowed)), x.Disk,
		tsOrEmpty(x.ExpiresAt), tsOrEmpty(x.GrantedAt), tsOrEmpty(x.RevokedAt), x.RequirementID, x.RequestID, x.RequestedBy, blocks.Encode(x.Ask), x.Retired, now(), x.ID)
	if err != nil {
		return x, err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return x, ErrNotFound
	}
	return s.GetSecret(ctx, x.ID)
}

// DeleteSecret 删除凭据和它的记录（只用于被拒绝、从没填过值的申请）。
func (s *Store) DeleteSecret(ctx context.Context, id string) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM secrets WHERE id = ?`, id)
	return err
}

// GetSecret 按编号 S-x、内部 ID 或名称（不分大小写，兼容老写法）读取凭据（R-326）。名称对上多条时返回 *AmbiguousSecretError。
func (s *Store) GetSecret(ctx context.Context, ref string) (Secret, error) {
	ref = strings.TrimSpace(ref)
	var x Secret
	var err error
	if m := secretCodeRe.FindStringSubmatch(ref); m != nil {
		x, err = scanSecret(s.db.QueryRowContext(ctx, secretSelect+` WHERE x.seq = ?`, m[1]))
	} else {
		x, err = scanSecret(s.db.QueryRowContext(ctx, secretSelect+` WHERE x.id = ?`, ref))
		if errors.Is(err, sql.ErrNoRows) {
			x, err = s.secretByName(ctx, ref)
		}
	}
	if errors.Is(err, sql.ErrNoRows) {
		return x, ErrNotFound
	}
	if err != nil {
		return x, err
	}
	s.fillSecretProjects(ctx, &x)
	return x, nil
}

// secretByName 按名称找：对上一条时返回它，多条时报出各自的编号。
func (s *Store) secretByName(ctx context.Context, name string) (Secret, error) {
	rows, err := s.db.QueryContext(ctx, secretSelect+` WHERE lower(x.name) = lower(?) ORDER BY x.seq`, name)
	if err != nil {
		return Secret{}, err
	}
	defer rows.Close()
	var hits []Secret
	for rows.Next() {
		x, err := scanSecret(rows)
		if err != nil {
			return Secret{}, err
		}
		hits = append(hits, x)
	}
	if err := rows.Err(); err != nil {
		return Secret{}, err
	}
	switch len(hits) {
	case 0:
		return Secret{}, sql.ErrNoRows
	case 1:
		return hits[0], nil
	}
	e := &AmbiguousSecretError{Name: name}
	for _, x := range hits {
		e.Codes = append(e.Codes, x.Code)
	}
	return Secret{}, e
}

// fillSecretSeq 给老凭据补编号（R-326）：按创建先后从 S-1 起。
func fillSecretSeq(db *sql.DB) error {
	_, err := db.Exec(`UPDATE secrets SET seq = (SELECT COUNT(*) FROM secrets s2 WHERE s2.created_at < secrets.created_at OR (s2.created_at = secrets.created_at AND s2.rowid <= secrets.rowid))`)
	return err
}

// SecretByRequest 按申请的决策（ID 或 D-x）找凭据。
func (s *Store) SecretByRequest(ctx context.Context, ref string) (Secret, error) {
	d, err := s.GetDecision(ctx, ref)
	if err != nil {
		return Secret{}, err
	}
	var id string
	if err := s.db.QueryRowContext(ctx, `SELECT id FROM secrets WHERE request_decision = ?`, d.ID).Scan(&id); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return Secret{}, ErrNotFound
		}
		return Secret{}, err
	}
	return s.GetSecret(ctx, id)
}

// ListSecrets 列出全部凭据，按名字排序。
func (s *Store) ListSecrets(ctx context.Context) ([]Secret, error) {
	rows, err := s.db.QueryContext(ctx, secretSelect+` ORDER BY lower(x.name)`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Secret{}
	for rows.Next() {
		x, err := scanSecret(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, x)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	for i := range out {
		s.fillSecretProjects(ctx, &out[i])
	}
	return out, nil
}

const secretLogSelect = `SELECT l.id, l.secret_id, l.kind, l.at, l.session_key, l.session_title, l.session_role, l.requirement_id, COALESCE(r.seq, 0),
	l.command, l.path, l.result, l.exit_code, l.reason, l.note, l.days, l.finished_at
	FROM secret_logs l LEFT JOIN requirements r ON r.id = l.requirement_id`

func scanSecretLog(row interface{ Scan(...any) error }) (SecretLog, error) {
	var l SecretLog
	var at, fin string
	var rseq int
	err := row.Scan(&l.ID, &l.SecretID, &l.Kind, &at, &l.SessionKey, &l.SessionTitle, &l.SessionRole, &l.RequirementID, &rseq,
		&l.Command, &l.Path, &l.Result, &l.ExitCode, &l.Reason, &l.Note, &l.Days, &fin)
	l.At = parseTS(at)
	l.FinishedAt = parseNullTS(sql.NullString{String: fin, Valid: fin != ""})
	if rseq > 0 {
		l.RequirementCode = reqCode(rseq)
	}
	return l, err
}

// AddSecretLog 记一条取用记录或授权变更；会话的标题与身份存成快照。取用（含被拒）同时更新凭据的最近取用时间。
func (s *Store) AddSecretLog(ctx context.Context, l SecretLog) (SecretLog, error) {
	l.ID = protocol.NewID()
	if l.At.IsZero() {
		l.At = time.Now()
	}
	if l.SessionKey != "" && l.SessionTitle == "" {
		l.SessionTitle, l.SessionRole = sessionSnap(ctx, s.db, l.SessionKey)
	}
	err := s.tx(ctx, func(t *sql.Tx) error {
		if _, err := t.ExecContext(ctx, `INSERT INTO secret_logs(id, secret_id, kind, at, session_key, session_title, session_role, requirement_id, command, path, result, exit_code, reason, note, days, finished_at)
			VALUES(?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			l.ID, l.SecretID, l.Kind, ts(l.At), l.SessionKey, l.SessionTitle, l.SessionRole, l.RequirementID, l.Command, l.Path, l.Result, l.ExitCode, l.Reason, l.Note, l.Days, tsOrEmpty(l.FinishedAt)); err != nil {
			return err
		}
		if l.Kind == SecretLogUse || l.Kind == SecretLogWrite {
			_, err := t.ExecContext(ctx, `UPDATE secrets SET last_used_at = ? WHERE id = ?`, ts(l.At), l.SecretID)
			return err
		}
		return nil
	})
	if err != nil {
		return l, err
	}
	return s.GetSecretLog(ctx, l.ID)
}

// GetSecretLog 读一条记录。
func (s *Store) GetSecretLog(ctx context.Context, id string) (SecretLog, error) {
	l, err := scanSecretLog(s.db.QueryRowContext(ctx, secretLogSelect+` WHERE l.id = ?`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return l, ErrNotFound
	}
	return l, err
}

// FinishSecretLog 记下取用命令的结果：退出码 0 为已完成，否则为失败。只改还没上报结果的记录。
func (s *Store) FinishSecretLog(ctx context.Context, id string, exitCode int) (SecretLog, error) {
	result := SecretOK
	if exitCode != 0 {
		result = SecretFailed
	}
	res, err := s.db.ExecContext(ctx, `UPDATE secret_logs SET result = ?, exit_code = ?, finished_at = ? WHERE id = ? AND result = ?`, result, exitCode, now(), id, SecretRunning)
	if err != nil {
		return SecretLog{}, err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return SecretLog{}, errors.New("这条取用记录不存在或已上报过结果")
	}
	return s.GetSecretLog(ctx, id)
}

// ListSecretLogs 列出凭据的全部记录，新的在前；同一毫秒里的按写入顺序（rowid），不靠 ID 里的随机部分。
func (s *Store) ListSecretLogs(ctx context.Context, secretID string) ([]SecretLog, error) {
	return s.querySecretLogs(ctx, secretLogSelect+` WHERE l.secret_id = ? ORDER BY l.at DESC, l.rowid DESC`, secretID)
}

// LastSecretUse 是凭据最近一次取用（含被拒、写入磁盘）。
func (s *Store) LastSecretUse(ctx context.Context, secretID string) (*SecretLog, error) {
	list, err := s.querySecretLogs(ctx, secretLogSelect+` WHERE l.secret_id = ? AND l.kind IN (?, ?) ORDER BY l.at DESC, l.rowid DESC LIMIT 1`, secretID, SecretLogUse, SecretLogWrite)
	if err != nil || len(list) == 0 {
		return nil, err
	}
	return &list[0], nil
}

func (s *Store) querySecretLogs(ctx context.Context, q string, args ...any) ([]SecretLog, error) {
	rows, err := s.db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []SecretLog{}
	for rows.Next() {
		l, err := scanSecretLog(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, l)
	}
	return out, rows.Err()
}
