package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/NekroAI/kitestring/internal/protocol"
)

// 提交、回滚与 CI（R-156）：纸鸢只关联和展示，绝不执行改仓库的操作（不 revert / reset / push，也不触发 CI）。
//   - 来源：daemon 读本地 git log，标题写了需求编号（仓库惯例在标题末尾写（R-xx））的提交自动关联；
//     Agent 也可以手动关联（commit_link），交证据时附的 commit 类证据同样算
//   - 回滚：提交说明里 "This reverts commit <哈希>" 认出 revert，被回滚的提交标上「已被 x 回滚」，并给一句该怎么做的提示
//   - CI：只读，Agent 上报，或 Hub 配了 GitHub token 时用只读接口查；没有就不显示

// CIState 是一个提交的 CI 结果（只读）。
type CIState struct {
	State  string    `json:"state" enum:"pending,success,failure" doc:"pending 进行中 / success 通过 / failure 未通过"`
	URL    string    `json:"url,omitempty"`
	Source string    `json:"source" enum:"agent,github" doc:"agent = Agent 上报；github = GitHub 只读接口"`
	At     time.Time `json:"at"`
}

// ReqCommits 是一条需求关联的提交，以及给 Agent / 用户的提示（例如被回滚了该怎么做）。
type ReqCommits struct {
	Code    string        `json:"code"`
	Commits []ChainCommit `json:"commits"`
	Hints   []string      `json:"hints" doc:"一句话提示：提交被回滚、CI 未通过时告诉你该怎么做（纸鸢不会替你改仓库）"`
}

var (
	reqCodeRe = regexp.MustCompile(`(?i)(?:^|[^A-Za-z0-9-])R-([0-9]+)(?:$|[^0-9A-Za-z])`)
	shaRe     = regexp.MustCompile(`^[0-9a-f]{7,40}$`)
)

// ReqCodesIn 取出一段文字里写的需求编号（R-x，按词边界），去重、规整成大写。
func ReqCodesIn(s string) []string {
	var out []string
	seen := map[string]bool{}
	// 相邻编号共用分隔符（「R-1、R-2」），逐个往后找
	for i := 0; i < len(s); {
		loc := reqCodeRe.FindStringSubmatchIndex(s[i:])
		if loc == nil {
			break
		}
		c := "R-" + strings.TrimLeft(s[i+loc[2]:i+loc[3]], "0")
		if c != "R-" && !seen[c] {
			seen[c] = true
			out = append(out, c)
		}
		i += loc[3]
	}
	return out
}

// NormSHA 规整提交哈希（小写）；不像哈希（7～40 位十六进制）时返回空。
func NormSHA(s string) string {
	s = strings.ToLower(strings.TrimSpace(s))
	if !shaRe.MatchString(s) {
		return ""
	}
	return s
}

func shaMatch(a, b string) bool {
	if a == "" || b == "" {
		return false
	}
	return strings.HasPrefix(a, b) || strings.HasPrefix(b, a)
}

func addCode(reqs, code string) string {
	if reqs == "" {
		reqs = ","
	}
	if strings.Contains(reqs, ","+code+",") {
		return reqs
	}
	return reqs + code + ","
}

func codesField(codes []string) string {
	if len(codes) == 0 {
		return ""
	}
	return "," + strings.Join(codes, ",") + ","
}

type commitRow struct {
	sha, subject, reverts, reqs, source, session string
	at                                           time.Time
	ci                                           *CIState
}

const commitCols = `sha, subject, committed_at, reverts, reqs, source, session_key, ci_state, ci_url, ci_source, ci_at`

func scanCommit(sc interface{ Scan(...any) error }) (commitRow, error) {
	var c commitRow
	var at, st, url, src, ciAt string
	if err := sc.Scan(&c.sha, &c.subject, &at, &c.reverts, &c.reqs, &c.source, &c.session, &st, &url, &src, &ciAt); err != nil {
		return c, err
	}
	c.at = parseTS(at)
	if st != "" {
		c.ci = &CIState{State: st, URL: url, Source: src, At: parseTS(ciAt)}
	}
	return c, nil
}

func (s *Store) queryCommits(ctx context.Context, q string, args ...any) ([]commitRow, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT `+commitCols+` FROM commits `+q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []commitRow
	for rows.Next() {
		c, err := scanCommit(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

// findCommit 找项目里哈希匹配的提交（完整哈希或任一方是另一方的前缀）。
func (s *Store) findCommit(ctx context.Context, projectID, sha string) (commitRow, bool, error) {
	cs, err := s.queryCommits(ctx, `WHERE project_id = ? AND (sha = ? OR sha LIKE ? OR ? LIKE sha || '%') ORDER BY length(sha) DESC LIMIT 1`, projectID, sha, sha+"%", sha)
	if err != nil || len(cs) == 0 {
		return commitRow{}, false, err
	}
	return cs[0], true, nil
}

// RecordCommits 存下 daemon 采集到的提交（按项目 + 哈希去重），返回新增条数。
// 之前 Agent 用短哈希手动关联过的同一个提交，并进这条完整记录。
func (s *Store) RecordCommits(ctx context.Context, projectID, sessionKey string, cs []protocol.Commit) (int, error) {
	if projectID == "" {
		return 0, nil
	}
	added := 0
	for _, c := range cs {
		sha := NormSHA(c.SHA)
		if sha == "" {
			continue
		}
		reqs := codesField(ReqCodesIn(c.Subject))
		err := s.tx(ctx, func(tx *sql.Tx) error {
			var n int
			if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM commits WHERE project_id = ? AND sha = ?`, projectID, sha).Scan(&n); err != nil || n > 0 {
				return err
			}
			ci := [4]string{}
			rows, err := tx.QueryContext(ctx, `SELECT sha, reqs, ci_state, ci_url, ci_source, ci_at FROM commits WHERE project_id = ? AND sha <> ? AND ? LIKE sha || '%'`, projectID, sha, sha)
			if err != nil {
				return err
			}
			var short []string
			for rows.Next() {
				var x, r string
				var c4 [4]string
				if err := rows.Scan(&x, &r, &c4[0], &c4[1], &c4[2], &c4[3]); err != nil {
					rows.Close()
					return err
				}
				short = append(short, x)
				for _, code := range strings.Split(strings.Trim(r, ","), ",") {
					if code != "" {
						reqs = addCode(reqs, code)
					}
				}
				if c4[0] != "" {
					ci = c4
				}
			}
			rows.Close()
			for _, x := range short {
				if _, err := tx.ExecContext(ctx, `DELETE FROM commits WHERE project_id = ? AND sha = ?`, projectID, x); err != nil {
					return err
				}
			}
			at := ""
			if !c.At.IsZero() {
				at = ts(c.At)
			}
			if _, err := tx.ExecContext(ctx, `INSERT INTO commits(project_id, sha, subject, committed_at, reverts, reqs, source, session_key, ci_state, ci_url, ci_source, ci_at, created_at) VALUES(?, ?, ?, ?, ?, ?, 'git', ?, ?, ?, ?, ?, ?)`,
				projectID, sha, c.Subject, at, NormSHA(c.Reverts), reqs, sessionKey, ci[0], ci[1], ci[2], ci[3], now()); err != nil {
				return err
			}
			added++
			return nil
		})
		if err != nil {
			return added, err
		}
	}
	return added, nil
}

// LinkCommit 由 Agent 手动把一个提交关联到需求（R-156）。已采集到的同一提交（哈希前缀匹配）直接加上编号。
func (s *Store) LinkCommit(ctx context.Context, projectID, sessionKey, sha, subject, code string) error {
	sha = NormSHA(sha)
	if sha == "" {
		return fmt.Errorf("提交哈希要写 7～40 位十六进制")
	}
	c, ok, err := s.findCommit(ctx, projectID, sha)
	if err != nil {
		return err
	}
	if ok {
		sub := c.subject
		if sub == "" {
			sub = subject
		}
		_, err = s.db.ExecContext(ctx, `UPDATE commits SET reqs = ?, subject = ? WHERE project_id = ? AND sha = ?`, addCode(c.reqs, code), sub, projectID, c.sha)
		return err
	}
	_, err = s.db.ExecContext(ctx, `INSERT INTO commits(project_id, sha, subject, committed_at, reqs, source, session_key, created_at) VALUES(?, ?, ?, ?, ?, 'agent', ?, ?)`,
		projectID, sha, firstLine(subject), now(), addCode("", code), sessionKey, now())
	return err
}

// SetCommitCI 记下一个提交的 CI 结果（Agent 上报或 GitHub 只读查询）。还没记过这个提交时新建一条（不关联需求）。
func (s *Store) SetCommitCI(ctx context.Context, projectID, sha string, ci CIState) error {
	sha = NormSHA(sha)
	if sha == "" {
		return fmt.Errorf("提交哈希要写 7～40 位十六进制")
	}
	if ci.At.IsZero() {
		ci.At = time.Now()
	}
	c, ok, err := s.findCommit(ctx, projectID, sha)
	if err != nil {
		return err
	}
	if ok {
		_, err = s.db.ExecContext(ctx, `UPDATE commits SET ci_state = ?, ci_url = ?, ci_source = ?, ci_at = ? WHERE project_id = ? AND sha = ?`, ci.State, ci.URL, ci.Source, ts(ci.At), projectID, c.sha)
		return err
	}
	_, err = s.db.ExecContext(ctx, `INSERT INTO commits(project_id, sha, committed_at, source, ci_state, ci_url, ci_source, ci_at, created_at) VALUES(?, ?, '', 'agent', ?, ?, ?, ?, ?)`,
		projectID, sha, ci.State, ci.URL, ci.Source, ts(ci.At), now())
	return err
}

// TouchCommitCI 只更新查询时间（GitHub 上查不到、没有 CI 时用，避免反复查）。
func (s *Store) TouchCommitCI(ctx context.Context, projectID, sha string) error {
	_, err := s.db.ExecContext(ctx, `UPDATE commits SET ci_at = ? WHERE project_id = ? AND sha = ?`, now(), projectID, sha)
	return err
}

// CommitCIAt 返回提交上次查询 / 上报 CI 的时间与来源（GitHub 查询节流用）。
func (s *Store) CommitCIAt(ctx context.Context, projectID, sha string) (at time.Time, state, source string, err error) {
	var a string
	err = s.db.QueryRowContext(ctx, `SELECT ci_at, ci_state, ci_source FROM commits WHERE project_id = ? AND sha = ?`, projectID, sha).Scan(&a, &state, &source)
	if errors.Is(err, sql.ErrNoRows) {
		return time.Time{}, "", "", ErrNotFound
	}
	if a != "" {
		at = parseTS(a)
	}
	return at, state, source, err
}

func (c commitRow) chain() ChainCommit {
	return ChainCommit{Link: c.sha, Summary: firstLine(c.subject), At: c.at, Source: c.source, Reverts: c.reverts, CI: c.ci}
}

// GetReqCommits 读一条需求关联的提交：标题写了编号的、Agent 手动关联的、commit 类证据，以及回滚它们的提交；按时间排。
func (s *Store) GetReqCommits(ctx context.Context, ref string) (ReqCommits, error) {
	r, err := s.GetRequirement(ctx, ref)
	if err != nil {
		return ReqCommits{}, err
	}
	cs, err := s.reqCommits(ctx, r)
	if err != nil {
		return ReqCommits{}, err
	}
	return ReqCommits{Code: r.Code, Commits: cs, Hints: CommitHints(r.Code, cs)}, nil
}

func (s *Store) reqCommits(ctx context.Context, r Requirement) ([]ChainCommit, error) {
	scope, args := `project_id = ?`, []any{r.ProjectID}
	if r.ProjectID == "" {
		scope, args = `1 = 1`, nil
	}
	rows, err := s.queryCommits(ctx, `WHERE `+scope+` AND reqs LIKE ? ORDER BY committed_at`, append(args, "%,"+r.Code+",%")...)
	if err != nil {
		return nil, err
	}
	out := []ChainCommit{}
	for _, c := range rows {
		out = append(out, c.chain())
	}
	// commit 类证据：哈希对得上已有记录的并进去，其余单独列出
	for _, e := range r.Evidence {
		if e.Kind != "commit" || e.Voided {
			continue
		}
		sha := NormSHA(e.Link)
		dup := false
		for _, c := range out {
			if sha != "" && shaMatch(c.Link, sha) {
				dup = true
				break
			}
		}
		if dup {
			continue
		}
		cc := ChainCommit{Link: e.Link, Summary: firstLine(e.Summary), At: e.CreatedAt, Source: "evidence"}
		if sha != "" && r.ProjectID != "" {
			if c, ok, err := s.findCommit(ctx, r.ProjectID, sha); err == nil && ok {
				cc.CI = c.ci
				if c.reverts != "" {
					cc.Reverts = c.reverts
				}
			}
		}
		out = append(out, cc)
	}
	// 回滚：项目里回滚了这些提交的 revert（标题里不一定写编号），连着回滚的回滚一起找
	if r.ProjectID != "" {
		reverts, err := s.queryCommits(ctx, `WHERE project_id = ? AND reverts <> '' ORDER BY committed_at`, r.ProjectID)
		if err != nil {
			return nil, err
		}
		for changed := true; changed; {
			changed = false
			for _, rv := range reverts {
				in := false
				for _, c := range out {
					if shaMatch(c.Link, rv.sha) {
						in = true
						break
					}
				}
				if in {
					continue
				}
				for _, c := range out {
					if shaMatch(c.Link, rv.reverts) {
						out = append(out, rv.chain())
						changed = true
						break
					}
				}
			}
		}
	}
	for i := range out {
		if out[i].Reverts == "" {
			continue
		}
		for j := range out {
			if i != j && shaMatch(out[j].Link, out[i].Reverts) {
				out[j].RevertedBy = out[i].Link
			}
		}
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].At.Before(out[j].At) })
	return out, nil
}

func short(sha string) string {
	if len(sha) > 7 && NormSHA(sha) != "" {
		return sha[:7]
	}
	return sha
}

// CommitHints 给出提交被回滚、CI 未通过时的一句话提示。纸鸢不改仓库，只告诉你该怎么做。
func CommitHints(code string, cs []ChainCommit) []string {
	hints := []string{}
	reverted := map[string]bool{}
	for _, c := range cs {
		if c.RevertedBy != "" {
			reverted[c.Link] = true
		}
	}
	for _, c := range cs {
		if c.RevertedBy == "" || reverted[c.RevertedBy] {
			continue // 没被回滚，或回滚本身又被回滚了（改动已恢复）
		}
		if c.Reverts != "" {
			continue
		}
		hints = append(hints, fmt.Sprintf("提交 %s 已被 %s 回滚。纸鸢不会改仓库：要恢复这项改动，请在仓库里自己执行 git revert %s（或重新提交），标题末尾写（%s）会自动关联。", short(c.Link), short(c.RevertedBy), short(c.RevertedBy), code))
	}
	for _, c := range cs {
		if c.CI != nil && c.CI.State == "failure" {
			h := fmt.Sprintf("提交 %s 的 CI 未通过", short(c.Link))
			if c.CI.URL != "" {
				h += "（" + c.CI.URL + "）"
			}
			hints = append(hints, h+"。纸鸢只读 CI 结果、不会重跑：请在仓库里修复后重新提交。")
		}
	}
	return hints
}

// 合入判定（R-245）：只认明确记录的提交（commits 表里关联到需求的、Agent 手动关联的、commit 类证据），不按提交说明猜。
// 一条需求可能交过几轮验收，只看「最近一轮」：最近一次交验收之前、最近一次被退回（从待验收 / 已完成退回去返工）之后的提交，
// 也包括交验收之后才有的（合入 main 时 cherry-pick 出来的那份）。

// RoundStart 是需求最近一轮从哪一刻算起：最近一次交验收之前的最近一次退回；没退回过时为零值（全部提交都算）。changes 按时间正序。
func RoundStart(changes []StatusChange) time.Time {
	end := len(changes)
	for i, c := range changes {
		if c.To == ReqReview {
			end = i
		}
	}
	var since time.Time
	for _, c := range changes[:end] {
		if (c.From == ReqReview || c.From == ReqDone) && c.To != ReqReview && c.To != ReqDone {
			since = c.At
		}
	}
	return since
}

// RoundCommits 是需求最近一轮关联的提交（哈希像样的、没被回滚的、不是回滚本身的），按时间正序。没有记录时为空。
func (s *Store) RoundCommits(ctx context.Context, r Requirement) ([]ChainCommit, error) {
	cs, err := s.reqCommits(ctx, r)
	if err != nil {
		return nil, err
	}
	since := RoundStart(r.StatusChanges)
	out := []ChainCommit{}
	for _, c := range cs {
		if NormSHA(c.Link) == "" || c.Reverts != "" || c.RevertedBy != "" || !c.At.After(since) {
			continue
		}
		out = append(out, c)
	}
	return out, nil
}
