package store

import (
	"database/sql"
	"encoding/json"
	"strings"
)

// 状态和标记只来自明确的记录（R-239，设计原则第 11 条）：会话在等什么、用户批准还是拒绝、推荐的是哪个选项，
// 都由写入方在发生时明确记成字段，界面和放行检查只读字段，不再从文字里猜。
// 老数据在补列时按原来的文字规则回填一次（columnFills），之后不再从文字推。

// 会话的等待类型（sessions.wait_kind）：由 hook 事件直接定，不看状态说明的文字。
const (
	WaitApproval = "approval" // 等你授权（PermissionRequest / permission_prompt）
	WaitQuestion = "question" // 用提问工具在问你（AskUserQuestion、ExitPlanMode、request_user_input）
)

// 批准类决策的结论（decisions.verdict）：作答时由网页 / 代答方明确给出；限时请求超时视为拒绝也记 rejected（timed_out 另标）；改状态申请作废不写。
const (
	VerdictApproved = "approved"
	VerdictRejected = "rejected"
)

// legacyVerdict：旧客户端没带结论时，按所选选项文字定一次（批准类决策的选项固定为「批准 / 拒绝」，答复以所选选项开头）。
// 只在写入时用一次并存成字段，读的地方一律读字段。
func legacyVerdict(answer string) string {
	a := strings.TrimSpace(answer)
	switch {
	case strings.HasPrefix(a, "批准"):
		return VerdictApproved
	case strings.HasPrefix(a, "拒绝"):
		return VerdictRejected
	}
	return ""
}

// RecOption：决策创建时定推荐的是第几个选项（从 0 开始，-1 = 没有）。explicit 是提问方明确给的序号；
// 没给时只认推荐文字与某个选项完全相同，不做模糊匹配。
func RecOption(options []string, rec string, explicit *int) int {
	if explicit != nil {
		if *explicit >= 0 && *explicit < len(options) {
			return *explicit
		}
		return -1
	}
	r := strings.TrimSpace(rec)
	if r == "" {
		return -1
	}
	for i, o := range options {
		if strings.TrimSpace(o) == r {
			return i
		}
	}
	return -1
}

// legacyRecOption 是 R-239 之前网页的匹配规则（DecisionCard recIndex），只用于老决策补列时回填一次，让老数据照常显示。
func legacyRecOption(options []string, rec string) int {
	r := strings.TrimSpace(rec)
	if r == "" {
		return -1
	}
	if i := RecOption(options, r, nil); i >= 0 {
		return i
	}
	for i, o := range options {
		x := strings.TrimSpace(o)
		if len([]rune(x)) >= 2 && (strings.HasPrefix(r, x) || strings.Contains(r, "「"+x+"」") || strings.Contains(r, "“"+x+"”") || strings.Contains(r, "**"+x+"**")) {
			return i
		}
	}
	return -1
}

func fillWaitKind(db *sql.DB) error {
	_, err := db.Exec(`UPDATE sessions SET wait_kind = CASE
		WHEN state = ? THEN ?
		WHEN state = ? AND (state_detail LIKE 'AskUserQuestion%' OR state_detail LIKE 'ExitPlanMode%' OR state_detail LIKE 'request_user_input%') THEN ?
		ELSE '' END`, StateWaitApproval, WaitApproval, StateWaitReply, WaitQuestion)
	return err
}

func fillVerdict(db *sql.DB) error {
	_, err := db.Exec(`UPDATE decisions SET verdict = CASE
		WHEN timed_out = 1 THEN 'rejected'
		WHEN substr(trim(answer), 1, 2) = '批准' THEN ?
		WHEN substr(trim(answer), 1, 2) = '拒绝' THEN ?
		ELSE '' END
		WHERE approval = 1 AND answer <> '' AND request_voided = 0`, VerdictApproved, VerdictRejected)
	return err
}

func fillRecOption(db *sql.DB) error {
	rows, err := db.Query(`SELECT id, options, recommendation FROM decisions WHERE recommendation <> ''`)
	if err != nil {
		return err
	}
	type item struct {
		id  string
		opt int
	}
	var items []item
	for rows.Next() {
		var id, opts, rec string
		if err := rows.Scan(&id, &opts, &rec); err != nil {
			rows.Close()
			return err
		}
		var options []string
		_ = json.Unmarshal([]byte(opts), &options)
		if i := legacyRecOption(options, rec); i >= 0 {
			items = append(items, item{id, i})
		}
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return err
	}
	for _, it := range items {
		if _, err := db.Exec(`UPDATE decisions SET rec_option = ? WHERE id = ?`, it.opt, it.id); err != nil {
			return err
		}
	}
	return nil
}

// fillTellFYI：老的「只告知」团队消息只靠标题 QuietTellQuestion 区分（R-198、R-227），补上 fyi 列时按标题回填一次（R-249）。
func fillTellFYI(db *sql.DB) error {
	_, err := db.Exec(`UPDATE decisions SET fyi = 1 WHERE kind = ? AND question = ?`, KindTell, QuietTellQuestion)
	return err
}
