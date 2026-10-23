package store

import (
	"context"
	"regexp"
	"strings"
	"time"
)

// 决策后果链（R-154）：一条决策产生了哪些需求、这些需求交了哪些提交，以及决策之间的取代 / 推翻关系。
// 不另存关系表，全部从已有记录读出：
//   - 产生的需求：需求的 from_decision（Agent 新建时声明），或来源（source）里写了这个编号（旧数据多是这样写的，如「D-161 第 2 项」）；
//     另外决策本身关联的需求算「针对的需求」
//   - 提交：上述需求的 commit 类证据（没作废的）
//   - 取代：撤回时写的 replaced_by（R-136），两个方向都列
//   - 推翻 / 接着：新决策声明的 from_decision + overturns；你推翻 Adv 代答（R-163）记在决策自己的 overridden_at 上，网页直接读

// ChainReq 是后果链里的一条需求。
type ChainReq struct {
	Code     string        `json:"code"`
	Title    string        `json:"title"`
	Status   string        `json:"status"`
	Relation string        `json:"relation" enum:"from,source,about" doc:"from = 新建时声明由这条决策产生；source = 来源里写了这个编号；about = 决策本身关联的需求"`
	Commits  []ChainCommit `json:"commits"`
}

// ChainCommit 是需求上的一条提交证据。
type ChainCommit struct {
	Link    string    `json:"link"`
	Summary string    `json:"summary"`
	At      time.Time `json:"at"`
}

// ChainDec 是后果链里相关的一条决策。
type ChainDec struct {
	Code      string `json:"code"`
	Title     string `json:"title"`
	Status    string `json:"status"`
	Overturns bool   `json:"overturns,omitempty"`
	Reason    string `json:"reason,omitempty" doc:"用户作答时附的一句话理由（R-157）；没写或旧决策为空"`
}

// DecisionChain 是一条决策的后果链。
type DecisionChain struct {
	Code         string     `json:"code"`
	From         *ChainDec  `json:"from,omitempty" doc:"它接着 / 推翻的那条决策（overturns = 推翻）"`
	Followups    []ChainDec `json:"followups" doc:"之后声明来自它的决策；overturns = 推翻了它"`
	ReplacedBy   *ChainDec  `json:"replaced_by,omitempty" doc:"撤回时指向的新决策"`
	Replaces     []ChainDec `json:"replaces" doc:"撤回时指向它的旧决策"`
	Requirements []ChainReq `json:"requirements"`
}

func chainDec(d Decision) ChainDec {
	t := d.ShortTitle
	if t == "" {
		t = d.Question
	}
	return ChainDec{Code: d.Code, Title: t, Status: d.Status, Overturns: d.Overturns, Reason: d.AnswerReason}
}

// NormDecCode 把「d-12」「D-12」「12」统一成 D-12；不像决策编号时返回空。
func NormDecCode(ref string) string {
	if seq, ok := parseRef(ref, "D"); ok && seq > 0 {
		return decCode(seq)
	}
	return ""
}

// GetDecisionChain 读一条决策的后果链。
func (s *Store) GetDecisionChain(ctx context.Context, ref string) (DecisionChain, error) {
	d, err := s.GetDecision(ctx, ref)
	if err != nil {
		return DecisionChain{}, err
	}
	c := DecisionChain{Code: d.Code, Followups: []ChainDec{}, Replaces: []ChainDec{}, Requirements: []ChainReq{}}
	if d.FromDecision != "" {
		if p, err := s.GetDecision(ctx, d.FromDecision); err == nil {
			x := chainDec(p)
			x.Overturns = d.Overturns
			c.From = &x
		}
	}
	if d.ReplacedBy != "" {
		if p, err := s.GetDecision(ctx, d.ReplacedBy); err == nil {
			x := chainDec(p)
			x.Overturns = false
			c.ReplacedBy = &x
		}
	}
	fol, err := s.queryDecs(ctx, decSelect+` WHERE x.from_decision = ? ORDER BY x.seq`, d.Code)
	if err != nil {
		return c, err
	}
	for _, f := range fol {
		c.Followups = append(c.Followups, chainDec(f))
	}
	rep, err := s.queryDecs(ctx, decSelect+` WHERE x.replaced_by = ? ORDER BY x.seq`, d.Code)
	if err != nil {
		return c, err
	}
	for _, f := range rep {
		x := chainDec(f)
		x.Overturns = false
		c.Replaces = append(c.Replaces, x)
	}

	// 需求：声明的、来源里写了编号的（LIKE 粗筛后按词边界精确匹配，D-16 不误中 D-161）、决策关联的
	mention := regexp.MustCompile(`(?i)(^|[^A-Za-z0-9-])` + regexp.QuoteMeta(d.Code) + `($|[^0-9])`)
	rows, err := s.db.QueryContext(ctx, `SELECT id, from_decision, source FROM requirements WHERE from_decision = ? OR source LIKE ? OR id = ? ORDER BY seq`, d.Code, "%"+d.Code+"%", d.RequirementID)
	if err != nil {
		return c, err
	}
	type hit struct{ id, rel string }
	var hits []hit
	for rows.Next() {
		var id, from, src string
		if err := rows.Scan(&id, &from, &src); err != nil {
			rows.Close()
			return c, err
		}
		switch {
		case from == d.Code:
			hits = append(hits, hit{id, "from"})
		case mention.MatchString(src):
			hits = append(hits, hit{id, "source"})
		case id == d.RequirementID:
			hits = append(hits, hit{id, "about"})
		}
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return c, err
	}
	for _, h := range hits {
		r, err := s.GetRequirement(ctx, h.id)
		if err != nil {
			continue
		}
		cr := ChainReq{Code: r.Code, Title: r.Title, Status: r.Status, Relation: h.rel, Commits: []ChainCommit{}}
		for _, e := range r.Evidence {
			if e.Kind == "commit" && !e.Voided {
				cr.Commits = append(cr.Commits, ChainCommit{Link: e.Link, Summary: firstLine(e.Summary), At: e.CreatedAt})
			}
		}
		c.Requirements = append(c.Requirements, cr)
	}
	return c, nil
}

func firstLine(s string) string {
	s = strings.TrimSpace(s)
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		s = s[:i]
	}
	if r := []rune(s); len(r) > 80 {
		s = string(r[:80]) + "…"
	}
	return s
}
