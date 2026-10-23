package store

import (
	"context"
	"database/sql"
	"time"
)

// 需求池盘面（R-133）的指标：全部由现有字段与状态变更历史算出，不调 AI。
//
// 计算口径：
//   - 总数：范围内（可按项目筛）全部需求，含已完成与搁置。
//   - 「进入当前状态的时间」since：状态变更历史里最近一次变为当前状态的时间；没有记录的旧需求，
//     草稿取创建时间，其余取最后更新时间。
//   - 昨天新增：创建时间落在昨天（按调用方时区的自然日）的需求数。
//   - 昨天完成：现在是已完成、且进入已完成的时间落在昨天的需求数（按进入 done 的时间，不按 updated_at）。
//   - 近 7 天每日完成：同上口径，今天在内往前 7 个自然日。
//   - 草稿待确认：草稿数；最久已放天数 = 草稿里 since 距今最长的整天数（不足 1 天算 0）。
//   - 在流转：待开始 + 进行中 + 阻塞 + 待验收。
//   - 你提出 / Agent 记下：未关闭（草稿 ~ 待验收）的需求按 created_by 分 user / agent。

// PoolDay 是某一天的完成数。
type PoolDay struct {
	Date  string `json:"date" doc:"本地日期 YYYY-MM-DD"`
	Count int    `json:"count"`
}

// PoolItem 是一条需求进入当前状态的时间。
type PoolItem struct {
	Code   string    `json:"code"`
	Status string    `json:"status"`
	Since  time.Time `json:"since" doc:"进入当前状态的时间；没有状态变更记录的旧需求，草稿取创建时间、其余取最后更新时间"`
	Days   int       `json:"days" doc:"在当前状态已放的整天数"`
}

// Pool 是需求池盘面的指标。
type Pool struct {
	Total           int            `json:"total"`
	Counts          map[string]int `json:"counts" doc:"各状态的条数（全部七种状态都有键）"`
	NewYesterday    int            `json:"new_yesterday"`
	DoneYesterday   int            `json:"done_yesterday" doc:"昨天进入已完成、现在仍是已完成的条数"`
	Drafts          int            `json:"drafts"`
	DraftOldestDays int            `json:"draft_oldest_days"`
	Flowing         int            `json:"flowing" doc:"待开始 + 进行中 + 阻塞 + 待验收"`
	OpenByUser      int            `json:"open_by_user" doc:"未关闭需求里由你提出的条数"`
	OpenByAgent     int            `json:"open_by_agent" doc:"未关闭需求里由 Agent 记下的条数"`
	DoneDaily       []PoolDay      `json:"done_daily" doc:"近 7 天（含今天）每日完成数，日期正序"`
	Items           []PoolItem     `json:"items"`
}

// open 判断需求是否未关闭（草稿 ~ 待验收）。
func openStatus(s string) bool { return s != ReqDone && s != ReqShelved }

// statusSince 取每条需求最近一次进入当前状态的时间（R-153 起才有记录）。
func (s *Store) statusSince(ctx context.Context, projectID string) (map[string]time.Time, error) {
	q := `SELECT r.id, (SELECT MAX(c.at) FROM status_changes c WHERE c.requirement_id = r.id AND c.to_status = r.status) FROM requirements r`
	var args []any
	if projectID != "" {
		q += ` WHERE r.project_id = ?`
		args = append(args, projectID)
	}
	rows, err := s.db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]time.Time{}
	for rows.Next() {
		var id string
		var at sql.NullString
		if err := rows.Scan(&id, &at); err != nil {
			return nil, err
		}
		if at.Valid && at.String != "" {
			out[id] = parseTS(at.String)
		}
	}
	return out, rows.Err()
}

// RequirementPool 算出需求池盘面指标；loc 决定「昨天」「近 7 天」按哪个时区的自然日算。
func (s *Store) RequirementPool(ctx context.Context, projectID string, now time.Time, loc *time.Location) (Pool, error) {
	reqs, err := s.ListRequirements(ctx, projectID, nil)
	if err != nil {
		return Pool{}, err
	}
	since, err := s.statusSince(ctx, projectID)
	if err != nil {
		return Pool{}, err
	}
	return ComputePool(reqs, since, now, loc), nil
}

// ComputePool 是盘面指标的纯计算部分（便于测试）。since 按需求 ID 给出进入当前状态的时间，缺的按口径回退。
func ComputePool(reqs []Requirement, since map[string]time.Time, now time.Time, loc *time.Location) Pool {
	if loc == nil {
		loc = time.Local
	}
	day := func(t time.Time) time.Time {
		y, m, d := t.In(loc).Date()
		return time.Date(y, m, d, 0, 0, 0, 0, loc)
	}
	today := day(now)
	yesterday := today.AddDate(0, 0, -1)
	p := Pool{Counts: map[string]int{}, Items: []PoolItem{}}
	for _, st := range ReqStatuses {
		p.Counts[st] = 0
	}
	daily := make([]PoolDay, 7)
	idx := map[string]int{}
	for i := range daily {
		d := today.AddDate(0, 0, i-6)
		daily[i].Date = d.Format("2006-01-02")
		idx[daily[i].Date] = i
	}
	for _, r := range reqs {
		p.Total++
		p.Counts[r.Status]++
		at, ok := since[r.ID]
		if !ok {
			if r.Status == ReqDraft {
				at = r.CreatedAt
			} else {
				at = r.UpdatedAt
			}
		}
		days := 0
		if d := now.Sub(at); d > 0 {
			days = int(d / (24 * time.Hour))
		}
		p.Items = append(p.Items, PoolItem{Code: r.Code, Status: r.Status, Since: at, Days: days})
		if day(r.CreatedAt).Equal(yesterday) {
			p.NewYesterday++
		}
		switch r.Status {
		case ReqDraft:
			p.Drafts++
			if days > p.DraftOldestDays {
				p.DraftOldestDays = days
			}
		case ReqReady, ReqInProgress, ReqBlocked, ReqReview:
			p.Flowing++
		case ReqDone:
			d := day(at)
			if d.Equal(yesterday) {
				p.DoneYesterday++
			}
			if i, ok := idx[d.Format("2006-01-02")]; ok {
				daily[i].Count++
			}
		}
		if openStatus(r.Status) {
			if r.CreatedBy == "agent" {
				p.OpenByAgent++
			} else {
				p.OpenByUser++
			}
		}
	}
	p.DoneDaily = daily
	return p
}
