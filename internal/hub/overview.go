package hub

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/NekroAI/kitestring/internal/protocol"
	"github.com/NekroAI/kitestring/internal/roles"
	"github.com/NekroAI/kitestring/internal/store"
)

// 接手概览（R-245）：ks role 接任主调度 / 主助理时，纸鸢按身份从记录生成要接手的事，附在身份提示词后；
// 压缩上下文后补注入的关键状态（stateNote）也带上。交接因此只需写纸鸢里没有的几句（做到一半的事、口头约定、注意事项）。
//   - 主调度：进行中 / 受阻的需求；已放行待排（按优先级）；在跑的子任务；已验收但没合入 main / 没部署的需求；main 上没推送的提交数
//   - 主助理：待验收的需求和写过的验收建议；上一任（和自己）提的、还没答的决策；别的会话提的、还没答的决策（R-272）；在等评审的需求
//   - 所有身份：项目上生效中的当前安排
// 合入 / 部署只认明确记录的提交（原则第 11 条）：取需求最近一轮关联的提交，由会话所在设备的 daemon 在本机仓库查。

const (
	overviewHead  = "接手概览"
	gitWait       = 6 * time.Second    // 接任时等 daemon 查仓库的时限
	gitWaitQuick  = 2 * time.Second    // 压缩后补注入时等的时限（hook 不能久等）
	shippedWindow = 7 * 24 * time.Hour // 「已验收但没合入 / 没部署」只看这么久以内验收的
)

// SetDeployCommit 改「正在运行的 ks」的构建提交（测试用；默认是 version.Commit）。
func (h *Hub) SetDeployCommit(c string) { h.deploy = c }

func (h *Hub) gitDone(r protocol.GitResult) {
	h.mu.Lock()
	ch := h.gits[r.ReqID]
	delete(h.gits, r.ReqID)
	h.mu.Unlock()
	if ch != nil {
		ch <- r
	}
}

// deployCommit：运行中 ks 的构建提交；开发构建（unknown / dev）时为空。
func (h *Hub) deployCommit() string { return store.NormSHA(h.deploy) }

// gitCheck 请会话 x 所在设备的 daemon 在 x 的工作目录里查提交。查不了时返回 nil 和原因。
func (h *Hub) gitCheck(ctx context.Context, x store.Session, shas []string, wait time.Duration) (*protocol.GitResult, string) {
	if x.Cwd == "" {
		return nil, "不知道仓库在哪（会话没有工作目录）"
	}
	h.mu.Lock()
	c := h.conns[x.DeviceID]
	h.mu.Unlock()
	if c == nil {
		return nil, "会话所在设备的 daemon 没连上"
	}
	reqID := protocol.NewID()
	ch := make(chan protocol.GitResult, 1)
	h.mu.Lock()
	h.gits[reqID] = ch
	h.mu.Unlock()
	drop := func() {
		h.mu.Lock()
		delete(h.gits, reqID)
		h.mu.Unlock()
	}
	c.send(protocol.Envelope{Type: protocol.MsgGitCheck, GitCheck: &protocol.GitCheck{ReqID: reqID, Dir: x.Cwd, SHAs: shas, Deploy: h.deployCommit()}})
	select {
	case r := <-ch:
		if r.Err != "" {
			return nil, r.Err
		}
		return &r, ""
	case <-time.After(wait):
		drop()
		return nil, "daemon 没有及时回音"
	case <-ctx.Done():
		drop()
		return nil, "查询被取消"
	}
}

type ovSection struct {
	title   string
	lines   []string
	ordered bool // 行里自带编号，不加「- 」
}

// overview 生成会话 x 以 role 接手时的概览（head 是标题）；没有可写的内容时返回空。
func (h *Hub) overview(ctx context.Context, x store.Session, role, head string, wait time.Duration) string {
	if x.ProjectID == "" {
		return ""
	}
	var secs []ovSection
	if a := h.arrangeNote(ctx, x.ProjectID); len(a) > 0 || role == roles.PM || role == roles.Aide {
		secs = append(secs, ovSection{title: "当前安排（用户交代的，照办；不再适用时 ks arrange revoke N-x）", lines: a})
	}
	switch role {
	case roles.PM:
		secs = append(secs, h.pmOverview(ctx, x, wait)...)
	case roles.Aide:
		secs = append(secs, h.aideOverview(ctx, x)...)
	}
	if len(secs) == 0 {
		return ""
	}
	var b strings.Builder
	fmt.Fprintf(&b, "\n## %s（纸鸢按记录生成，%s）\n\n", head, time.Now().Local().Format("01-02 15:04"))
	for _, s := range secs {
		fmt.Fprintf(&b, "### %s\n", s.title)
		if len(s.lines) == 0 {
			b.WriteString("（没有）\n\n")
			continue
		}
		for _, l := range s.lines {
			if s.ordered || strings.HasPrefix(l, "（") { // 编号行、括号里的补充说明不加「- 」
				b.WriteString(l + "\n")
				continue
			}
			b.WriteString("- " + l + "\n")
		}
		b.WriteString("\n")
	}
	return b.String()
}

func reqName(r store.Requirement) string {
	return fmt.Sprintf("%s「%s」", r.Code, oneLineClip(r.Title, 40))
}

func (h *Hub) pmOverview(ctx context.Context, x store.Session, wait time.Duration) []ovSection {
	var out []ovSection
	// 进行中 / 受阻
	if list, err := h.store.ListRequirements(ctx, x.ProjectID, []string{store.ReqInProgress, store.ReqBlocked}); err == nil {
		sortQueue(list)
		var lines []string
		for _, r := range list {
			l := reqName(r) + reqStatusName[r.Status]
			if r.Status == store.ReqBlocked {
				switch {
				case r.BlockedOn != "" && r.BlockedReason != "":
					l += "，在等 " + r.BlockedOn + "：" + oneLineClip(r.BlockedReason, 40)
				case r.BlockedOn != "":
					l += "，在等 " + r.BlockedOn
				case r.BlockedReason != "":
					l += "：" + oneLineClip(r.BlockedReason, 40)
				}
			}
			lines = append(lines, l)
		}
		out = append(out, ovSection{title: "进行中 / 受阻的需求", lines: lines})
	}
	// 已放行待排
	if list, err := h.store.ListRequirements(ctx, x.ProjectID, []string{store.ReqReady}); err == nil {
		sortQueue(list)
		var lines []string
		for i, r := range list {
			l := fmt.Sprintf("%d. %s%s", i+1, reqName(r), r.Priority)
			switch {
			case r.OfferedTo != "":
				l += "（已送给 " + h.sessionName(ctx, r.OfferedTo) + "，还没开始处理）"
			case r.StageStatus == store.StageLocked:
				l += "（计划里前一阶段还没通过）"
			}
			lines = append(lines, l)
		}
		out = append(out, ovSection{title: "已放行待排（按优先级，排在前的先做）", lines: lines, ordered: true})
	}
	// 在跑的子任务
	if list, err := h.store.ListSessions(ctx, recentWindow, false); err == nil {
		var lines []string
		now := time.Now()
		for _, s := range list {
			if s.ProjectID != x.ProjectID || s.State == store.StateEnded || s.State == store.StateLost {
				continue
			}
			var names []string
			for _, t := range store.LiveTasks(s.Tasks, now) {
				if t.Kind != "agent" {
					continue
				}
				label := t.Label
				if label == "" {
					label = t.AgentType
				}
				names = append(names, fmt.Sprintf("「%s」（已跑 %d 分钟）", oneLineClip(label, 40), int(now.Sub(t.StartedAt).Minutes())))
			}
			if len(names) > 0 {
				lines = append(lines, h.sessionName(ctx, s.Key)+"："+strings.Join(names, "、"))
			}
		}
		out = append(out, ovSection{title: "在跑的子任务", lines: lines})
	}
	out = append(out, h.shippedSections(ctx, x, wait)...)
	return out
}

// sortQueue：按优先级、编号排（和指派顺序一致）。
func sortQueue(list []store.Requirement) {
	sort.SliceStable(list, func(i, j int) bool {
		a, b := prioRank[list[i].Priority], prioRank[list[j].Priority]
		if a != b {
			return a < b
		}
		return codeNum(list[i].Code) < codeNum(list[j].Code)
	})
}

// sessionName：会话的称呼（标题，没有时用会话键）。
func (h *Hub) sessionName(ctx context.Context, key string) string {
	if x, err := h.store.GetSession(ctx, key); err == nil && strings.TrimSpace(x.Title) != "" {
		return "「" + oneLineClip(x.Title, 30) + "」"
	}
	return key
}

type shipped struct {
	r        store.Requirement
	at       time.Time
	commits  []store.ChainCommit
	main     string // ok / no / unknown / none（没记录）
	deploy   string // ok / no / unknown
	notMain  []string
	notDep   []string
	missing  []string
	patchHit bool
}

// shippedSections：主调度概览里的「已验收但没合入 main / 没部署」和「推送」两栏。
func (h *Hub) shippedSections(ctx context.Context, x store.Session, wait time.Duration) []ovSection {
	list, err := h.store.ListRequirements(ctx, x.ProjectID, []string{store.ReqDone})
	if err != nil {
		return nil
	}
	cutoff := time.Now().Add(-shippedWindow)
	var items []*shipped
	var shas []string
	seen := map[string]bool{}
	for _, r := range list {
		if r.UpdatedAt.Before(cutoff) {
			continue
		}
		full, err := h.store.GetRequirement(ctx, r.ID)
		if err != nil {
			continue
		}
		var at time.Time
		for _, c := range full.StatusChanges {
			if c.To == store.ReqDone {
				at = c.At
			}
		}
		if at.IsZero() || at.Before(cutoff) {
			continue
		}
		cs, err := h.store.RoundCommits(ctx, full)
		if err != nil {
			continue
		}
		it := &shipped{r: full, at: at, commits: cs}
		items = append(items, it)
		for _, c := range cs {
			if !seen[c.Link] {
				seen[c.Link] = true
				shas = append(shas, c.Link)
			}
		}
	}
	sort.SliceStable(items, func(i, j int) bool { return items[i].at.After(items[j].at) })
	res, why := h.gitCheck(ctx, x, shas, wait)
	var secs []ovSection
	title := "已验收但没合入 main / 没部署（近 7 天验收的，只认关联到需求的提交）"
	if res == nil {
		if len(items) > 0 {
			secs = append(secs, ovSection{title: title, lines: []string{fmt.Sprintf("（查不了本机仓库：%s。近 7 天验收了 %d 条，合入 / 部署情况没法判断）", why, len(items))}})
		}
		return append(secs, ovSection{title: "推送", lines: []string{"查不了本机仓库：" + why}})
	}
	place := map[string]protocol.CommitPlace{}
	for _, p := range res.Commits {
		place[p.SHA] = p
	}
	deployKnown := h.deployCommit() != ""
	deployHere := false
	for _, p := range res.Commits {
		deployHere = deployHere || p.Deploy != protocol.PlaceUnknown
	}
	var lines []string
	okN := 0
	for _, it := range items {
		if len(it.commits) == 0 {
			lines = append(lines, reqName(it.r)+"：最近一轮没有关联的提交记录，判断不了合没合入（不按提交说明猜；提交后用 ks commit link R-x <哈希> 关联）")
			continue
		}
		notMain, missing, notDep := roundPlaces(it.commits, place)
		switch {
		case len(notMain) > 0:
			lines = append(lines, fmt.Sprintf("%s：没合入 main（最近一轮的 %s 不在 main 上）", reqName(it.r), strings.Join(notMain, "、")))
		case len(missing) > 0:
			lines = append(lines, fmt.Sprintf("%s：本机仓库里找不到提交 %s，判断不了", reqName(it.r), strings.Join(missing, "、")))
		case deployHere && len(notDep) > 0:
			lines = append(lines, fmt.Sprintf("%s：已合入 main，还没部署（%s 不在运行中的 ks 里）", reqName(it.r), strings.Join(notDep, "、")))
		default:
			okN++
		}
	}
	if okN > 0 {
		done := "已合入 main"
		if deployHere {
			done += " 并已部署"
		}
		lines = append(lines, fmt.Sprintf("（其余 %d 条%s）", okN, done))
	}
	if len(items) > 0 && !deployHere {
		if deployKnown {
			lines = append(lines, "（运行中的 ks 不是从这个仓库构建的，不判断部署）")
		} else {
			lines = append(lines, "（不知道运行中的 ks 是哪个提交构建的，部署情况判断不了）")
		}
	}
	secs = append(secs, ovSection{title: title, lines: lines})
	var push string
	switch {
	case res.Main == "":
		push = "找不到 main / master 分支"
	case res.NoRemote:
		push = res.Main + " 没有远端（没有上游分支），推送情况不适用"
	case res.Ahead > 0:
		push = fmt.Sprintf("%s 比 %s 多 %d 个提交没推送", res.Main, res.Upstream, res.Ahead)
	default:
		push = fmt.Sprintf("%s 和 %s 一致，没有待推送的提交", res.Main, res.Upstream)
	}
	return append(secs, ovSection{title: "推送", lines: []string{push}})
}

// roundPlaces 判断一轮提交里哪些没合入 main、本机找不到、没部署（R-245，D-1043）。
// daemon 已认出三种：是祖先、补丁相同、main 上有 cherry-pick -x 摘它的提交（来源记录，解决过冲突也在）。
// 都认不到时兜底：main 上有关联到同一需求、标题相同、更晚的提交（没加 -x、合入时解决过冲突的 cherry-pick，或 rebase 后的同一提交），
// 按那一份算——两者都是明确关联到这条需求的提交记录，不是从提交说明里猜编号。
func roundPlaces(cs []store.ChainCommit, place map[string]protocol.CommitPlace) (notMain, missing, notDep []string) {
	onMain := func(p protocol.CommitPlace) bool {
		return p.Main != protocol.PlaceNo && p.Main != protocol.PlaceUnknown
	}
	deployed := func(p protocol.CommitPlace) bool {
		return p.Deploy != protocol.PlaceNo && p.Deploy != protocol.PlaceUnknown
	}
	for _, c := range cs {
		p, ok := place[c.Link]
		if !ok || p.Main == protocol.PlaceUnknown {
			missing = append(missing, short7(c.Link))
			continue
		}
		if p.Main == protocol.PlaceNo && p.Subject != "" {
			for _, d := range cs {
				q, ok := place[d.Link]
				if ok && d.Link != c.Link && q.Subject == p.Subject && onMain(q) && d.At.After(c.At) {
					p.Main = q.Main
					if p.Deploy == protocol.PlaceNo && deployed(q) {
						p.Deploy = q.Deploy
					}
					break
				}
			}
		}
		if p.Main == protocol.PlaceNo {
			notMain = append(notMain, short7(c.Link))
		}
		if p.Deploy == protocol.PlaceNo {
			notDep = append(notDep, short7(c.Link))
		}
	}
	return
}

func short7(sha string) string {
	if len(sha) > 7 {
		return sha[:7]
	}
	return sha
}

func (h *Hub) aideOverview(ctx context.Context, x store.Session) []ovSection {
	var out []ovSection
	reviews, err := h.store.ListRequirements(ctx, x.ProjectID, []string{store.ReqReview})
	if err != nil {
		return nil
	}
	sortQueue(reviews)
	advice := map[string]store.Advice{}
	if list, err := h.store.ReviewAdvice(ctx, x.ProjectID); err == nil {
		for _, a := range list {
			advice[a.RequirementID] = a
		}
	}
	var lines, waiting []string
	for _, r := range reviews {
		l := reqName(r)
		if a, ok := advice[r.ID]; ok {
			v := "建议通过"
			if a.Verdict == "return" {
				v = "建议退回"
			}
			l += "：已写" + v + "——" + oneLineClip(a.Reason, 50)
		} else {
			l += "：还没写验收建议"
		}
		lines = append(lines, l)
		if full, err := h.store.GetRequirement(ctx, r.ID); err == nil && full.Review != nil {
			switch full.Review.State {
			case store.ReviewReviewing:
				waiting = append(waiting, reqName(r)+"：评审"+reviewerName(full.Review)+"在审")
			case store.ReviewOverdue:
				waiting = append(waiting, reqName(r)+"：评审"+reviewerName(full.Review)+"没按时交意见（已照常进用户的「待处理」）")
			}
		}
	}
	out = append(out, ovSection{title: "待验收（写验收建议用 ks away advise R-x pass|return 理由）", lines: lines})
	// 主助理提的、还没答的决策
	once, _ := h.store.SessionsOnceRole(ctx, x.ProjectID, roles.Aide)
	if once == nil {
		once = map[string]bool{}
	}
	once[x.Key] = true
	var decs, others []string
	if list, err := h.store.ListDecisions(ctx, []string{store.DecPending}, -1); err == nil {
		for _, d := range list {
			if d.ProjectID != x.ProjectID || (d.Kind != store.KindDecision && d.Kind != "") {
				continue
			}
			l := fmt.Sprintf("%s「%s」", d.Code, oneLineClip(store.DecisionTitle(d), 50))
			if d.FromRole == roles.Aide || (d.FromRole == "" && once[d.SessionKey]) {
				if d.SessionKey != x.Key {
					l += "（" + h.sessionName(ctx, d.SessionKey) + "提的，答复会送到那个会话）"
				}
				decs = append(decs, l)
				continue
			}
			// 别的会话提的（R-272）：主助理订阅 decision 是为了查冲突，上一任下线期间新提的不会再唤醒谁，接任时一并列出
			by := roles.Label(d.FromRole)
			if by != "" {
				by += " "
			}
			others = append(others, l+"（"+by+h.sessionName(ctx, d.SessionKey)+"提的）")
		}
	}
	out = append(out, ovSection{title: "主助理提的、还没答的决策", lines: decs})
	out = append(out, ovSection{title: "别的会话提的、还没答的决策（查一遍有没有和用户定过的规矩、别的决策冲突）", lines: others})
	out = append(out, ovSection{title: "在等评审的需求", lines: waiting})
	return out
}
