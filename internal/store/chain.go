package store

import (
	"context"
	"regexp"
	"strings"
	"time"
	"unicode"

	"github.com/NekroAI/kitestring/internal/blocks"
)

// 决策后果链（R-154）：一条决策产生了哪些需求、这些需求交了哪些提交，以及决策之间的取代 / 推翻关系。
// 不另存关系表，全部从已有记录读出：
//   - 产生的需求：需求的 from_decision（Agent 新建时声明），或来源（source）里写了这个编号（旧数据多是这样写的，如「D-161 第 2 项」）；
//     另外决策本身关联的需求算「针对的需求」
//   - 提交：上述需求的 commit 类证据（没作废的）
//   - 取代：撤回时写的 replaced_by（R-136），两个方向都列
//   - 推翻 / 接着：新决策声明的 from_decision + overturns；你推翻 Adv 代答（R-163）记在决策自己的 overridden_at 上，网页直接读
//   - 因此改了需求（R-241）：修改记录（req_edit）声明的 from_decision / approval，或修改理由里明写了这个编号；
//     放行记录的理由里明写了这个编号。只认明写的编号，不做语义推测（设计原则第 11 条）

// ChainReq 是后果链里的一条需求。
type ChainReq struct {
	Code     string        `json:"code"`
	Title    string        `json:"title"`
	Status   string        `json:"status"`
	Relation string        `json:"relation" enum:"from,source,about" doc:"from = 新建时声明由这条决策产生；source = 来源里写了这个编号；about = 决策本身关联的需求"`
	Commits  []ChainCommit `json:"commits"`
}

// ChainCommit 是需求上的一条提交：commit 类证据，或本地 git log 采集 / Agent 手动关联的提交（R-156）。
type ChainCommit struct {
	Link    string    `json:"link" doc:"提交哈希（证据里写的链接原样保留）"`
	Summary string    `json:"summary"`
	At      time.Time `json:"at"`
	Source  string    `json:"source,omitempty" enum:"git,agent,evidence" doc:"git = 标题写了需求编号、本地采集；agent = Agent 手动关联；evidence = 交证据时附的提交"`
	// Reverts：这是一个 revert，回滚了哪个提交；RevertedBy：这个提交被哪个提交回滚了（R-156）
	Reverts    string   `json:"reverts,omitempty"`
	RevertedBy string   `json:"reverted_by,omitempty"`
	CI         *CIState `json:"ci,omitempty" doc:"CI 结果（只读）；没有上报、也没配 GitHub token 时不给"`
}

// ChainDec 是后果链里相关的一条决策。
type ChainDec struct {
	Code      string `json:"code"`
	Title     string `json:"title"`
	Status    string `json:"status"`
	Overturns bool   `json:"overturns,omitempty"`
	Reason    string `json:"reason,omitempty" doc:"用户作答时附的一句话理由（R-157）；没写或旧决策为空"`
}

// ChainEdit 是因这条决策对需求做的一次修改或放行（R-241）。
type ChainEdit struct {
	Code     string    `json:"code" doc:"修改 / 放行记录自己的编号（D-x）"`
	Kind     string    `json:"kind" enum:"edit,release" doc:"edit = 改了需求内容；release = 放行（草稿改为待开始）"`
	ReqCode  string    `json:"req_code"`
	ReqTitle string    `json:"req_title"`
	Fields   []string  `json:"fields" doc:"改了哪些字段（中文名：标题、描述、验收标准……）；放行时为空"`
	Note     string    `json:"note" doc:"修改说明 / 放行理由"`
	At       time.Time `json:"at"`
	Declared bool      `json:"declared,omitempty" doc:"true = 修改时声明了依据这条决策（from_decision / approval）；false = 理由里写了这个编号"`
}

// DecisionChain 是一条决策的后果链。
type DecisionChain struct {
	Code         string      `json:"code"`
	From         *ChainDec   `json:"from,omitempty" doc:"它接着 / 推翻的那条决策（overturns = 推翻）"`
	Followups    []ChainDec  `json:"followups" doc:"之后声明来自它的决策；overturns = 推翻了它"`
	ReplacedBy   *ChainDec   `json:"replaced_by,omitempty" doc:"撤回时指向的新决策"`
	Replaces     []ChainDec  `json:"replaces" doc:"撤回时指向它的旧决策"`
	Requirements []ChainReq  `json:"requirements"`
	Edits        []ChainEdit `json:"edits" doc:"因它改了哪些需求的内容、放行了哪些需求（R-241），按时间排"`
}

func chainDec(d Decision) ChainDec {
	return ChainDec{Code: d.Code, Title: DecisionTitle(d), Status: d.Status, Overturns: d.Overturns, Reason: d.AnswerReason}
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
	c := DecisionChain{Code: d.Code, Followups: []ChainDec{}, Replaces: []ChainDec{}, Requirements: []ChainReq{}, Edits: []ChainEdit{}}
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
	fol, err := s.queryDecs(ctx, decSelect+` WHERE x.from_decision = ? AND x.kind = ? ORDER BY x.seq`, d.Code, KindDecision)
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
		cr := ChainReq{Code: r.Code, Title: r.Title, Status: r.Status, Relation: h.rel}
		if cr.Commits, err = s.reqCommits(ctx, r); err != nil {
			return c, err
		}
		c.Requirements = append(c.Requirements, cr)
	}

	// 因此改了需求 / 放行：声明了依据它的修改记录，或修改 / 放行理由里明写了它的编号
	eds, err := s.queryDecs(ctx, decSelect+` WHERE x.kind IN (?, ?) AND (x.from_decision = ? OR x.answer LIKE ?) ORDER BY x.seq`, KindEdit, KindRelease, d.Code, "%"+d.Code+"%")
	if err != nil {
		return c, err
	}
	for _, e := range eds {
		declared := e.FromDecision == d.Code
		if !declared && (e.FromDecision != "" || !mention.MatchString(e.Answer)) {
			continue // 声明了别的决策时以声明为准；没声明时理由里要明写这个编号
		}
		x := ChainEdit{Code: e.Code, Kind: e.Kind, ReqCode: e.RequirementCode, ReqTitle: e.RequirementTitle, Fields: []string{}, Note: e.Answer, Declared: declared}
		if e.AnsweredAt != nil {
			x.At = *e.AnsweredAt
		} else {
			x.At = e.CreatedAt
		}
		for _, b := range e.Blocks {
			if b.Type != blocks.Table {
				continue
			}
			for _, row := range b.Rows {
				if len(row) > 1 {
					x.Fields = append(x.Fields, row[1])
				}
			}
		}
		c.Edits = append(c.Edits, x)
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

// TitleKeepsMeaning：短标题的字大多出自原问题才算缩写（汉字、字母、数字按字算，至少七成在原问题里出现过）。
func TitleKeepsMeaning(title, question string) bool {
	seen := map[rune]bool{}
	for _, r := range question {
		seen[unicode.ToLower(r)] = true
	}
	n, hit := 0, 0
	for _, r := range title {
		if !unicode.IsLetter(r) && !unicode.IsDigit(r) {
			continue
		}
		n++
		if seen[unicode.ToLower(r)] {
			hit++
		}
	}
	return n > 0 && hit*10 >= n*7
}

// DecisionTitle：决策的标题——短标题只做缩写，改了意思的（老数据）用原问题（R-238 / R-241）。网页 lib/waiting.ts 的 titleOf 同一口径。
func DecisionTitle(d Decision) string {
	if d.ShortTitle != "" && TitleKeepsMeaning(d.ShortTitle, d.Question) {
		return d.ShortTitle
	}
	return d.Question
}
