package hub

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/NekroAI/kitestring/internal/store"
	"github.com/NekroAI/kitestring/internal/summary"
)

// AIFeatures 是 Hub 侧模型的各项功能开关，默认全部关闭。
type AIFeatures struct {
	Summary       bool `json:"summary,omitempty"`        // 会话一句话进展
	Title         bool `json:"title,omitempty"`          // 会话标题 + 阶段
	Digest        bool `json:"digest,omitempty"`         // 一轮结束时提炼“这一轮做了什么”（不再概括在等你什么，R-239）
	DecisionTitle bool `json:"decision_title,omitempty"` // 决策短标题
	Recap         bool `json:"recap,omitempty"`          // 时段回顾
}

func (f AIFeatures) any() bool {
	return f.Summary || f.Title || f.Digest || f.DecisionTitle || f.Recap
}

// needsReply 表示是否需要设备上送 agent 每轮的最后回复。
func (f AIFeatures) needsReply() bool { return f.Summary || f.Title || f.Digest }

// titleNoise 去掉标题里的引号与问号（模型常常不遵守“不要标点”）。
var titleNoise = strings.NewReplacer("“", "", "”", "", "\"", "", "「", "", "」", "", "？", "", "?", "", "《", "", "》", "")

// aiGuard 防止同一会话同类任务并发重复调用模型。
type aiGuard struct {
	mu   sync.Mutex
	busy map[string]bool
}

func (g *aiGuard) try(k string) bool {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.busy == nil {
		g.busy = map[string]bool{}
	}
	if g.busy[k] {
		return false
	}
	g.busy[k] = true
	return true
}

func (g *aiGuard) done(k string) {
	g.mu.Lock()
	delete(g.busy, k)
	g.mu.Unlock()
}

func (h *Hub) aiSettings() (Settings, bool) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	s, err := h.Settings(ctx)
	if err != nil || !s.Summary.Enabled() {
		return s, false
	}
	return s, true
}

const titlePrompt = `根据用户给 AI 编程助手的第一条指令，起一个不超过 12 个汉字的中文标题，概括这次要做的事。像任务名一样简洁，不要标点。只输出标题。`

// aiOnPrompt 在会话还没有标题时，根据用户消息生成标题。
func (h *Hub) aiOnPrompt(key, prompt string) {
	s, ok := h.aiSettings()
	if !ok || !s.AI.Title || strings.TrimSpace(prompt) == "" || !h.ai.try("title:"+key) {
		return
	}
	defer h.ai.done("title:" + key)
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	if x, err := h.store.GetSession(ctx, key); err != nil || x.Title != "" {
		return
	}
	text, err := summary.Chat(h.aiCtx(ctx, aiTitle), s.Summary, titlePrompt, prompt, 40)
	if err != nil {
		h.log.Warn("生成会话标题失败", "session", key, "err", err)
		return
	}
	if t := summary.Clean(titleNoise.Replace(text), 20); t != "" {
		_ = h.store.SetSessionAI(ctx, key, store.SessionAI{Title: t})
		h.broker.publish("sessions")
	}
}

// aiOnStop 在一轮结束时，按开启的功能一次调用生成摘要、标题、这一轮做了什么。
// 不再让模型猜“阶段”：它只反映最后一轮说了什么、待命后不会刷新，与状态胶囊和需求印记重复且常常过时（R-125）。
func (h *Hub) aiOnStop(key, prompt, reply string) {
	s, ok := h.aiSettings()
	if !ok || !s.AI.needsReply() || !h.ai.try("stop:"+key) {
		return
	}
	defer h.ai.done("stop:" + key)
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	x, err := h.store.GetSession(ctx, key)
	if err != nil {
		return
	}

	var fields []string
	if s.AI.Summary {
		fields = append(fields, `"summary": 一句话说明这个会话目前进行到哪了，不超过 40 字`)
	}
	if s.AI.Title {
		// 标题随会话手上的事更新：一个会话常常连着做好几件事，第一条指令起的标题很快就过时了（R-71）
		if x.Title == "" {
			fields = append(fields, `"title": 不超过 12 个汉字的任务标题，概括这个会话现在在做的事，不要标点`)
		} else {
			fields = append(fields, fmt.Sprintf(`"title": 会话原标题是「%s」；如果还贴合这一轮在做的事就原样输出，否则改成不超过 12 个汉字、概括现在在做的事的新标题，不要标点`, x.Title))
		}
	}
	if s.AI.Digest {
		// 只记做了什么；会话在不在等用户只认决策、验收、授权、提问工具这些明确记录，不让模型从回复里概括（R-239）
		fields = append(fields, `"done": 这一轮助手做成了什么，一句话，不超过 40 字`)
	}
	system := "你是开发进度速记员。根据用户给 AI 编程助手的最后一条指令和助手这一轮的最后回复，输出一个 JSON 对象，只包含以下字段：\n" + strings.Join(fields, "\n") + "\n用中文，简洁具体，不要客套。只输出 JSON。"
	user := fmt.Sprintf("最后一条指令：\n%s\n\n助手最后回复：\n%s", prompt, reply)
	var out struct{ Summary, Title, Done string }
	if err := summary.ChatJSON(h.aiCtx(ctx, aiTurn), s.Summary, system, user, 600, &out); err != nil {
		h.log.Warn("生成会话摘要失败", "session", key, "err", err)
		return
	}
	a := store.SessionAI{Summary: summary.Clean(out.Summary, 80), Title: summary.Clean(titleNoise.Replace(out.Title), 20), DigestDone: summary.Clean(out.Done, 80)}
	if !s.AI.Summary {
		a.Summary = ""
	}
	if !s.AI.Digest {
		a.DigestDone = ""
	}
	if err := h.store.SetSessionAI(ctx, key, a); err != nil {
		return
	}
	h.broker.publish("sessions")
}

// 短标题只做缩写，不改原问题的意思（R-238 / R-241）：只给模型原问题、不给说明，免得它把背景里的说法换进来；
// 生成后再核对一遍用字，和原问题对不上的不用，界面照旧显示原问题。
const decisionTitlePrompt = `把下面这个需要用户决策的问题缩写成一个不超过 14 个汉字的中文短标题。只能删减，不能改写：保留原问题里的关键词和它在问的那件事，不要换成别的说法，不要加入原问题里没有的内容，不要改变问的方向（问「要不要做 A」就仍是 A）。不要问号和引号。只输出标题。`

// aiOnDecision 为新决策生成短标题。
func (h *Hub) aiOnDecision(d store.Decision) {
	s, ok := h.aiSettings()
	if !ok || !s.AI.DecisionTitle {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	if len([]rune(strings.TrimSpace(d.Question))) <= 16 {
		return // 原问题已经够短，直接显示原问题
	}
	text, err := summary.Chat(h.aiCtx(ctx, aiDecisionTitle), s.Summary, decisionTitlePrompt, d.Question, 40)
	if err != nil {
		h.log.Warn("生成决策短标题失败", "decision", d.Code, "err", err)
		return
	}
	if t := summary.Clean(titleNoise.Replace(text), 24); t != "" && store.TitleKeepsMeaning(t, d.Question) {
		_ = h.store.SetDecisionTitle(ctx, d.ID, t)
		h.broker.publish("decisions")
	}
}

const recapPrompt = `你在帮一位同时指挥多个 AI 编程助手的开发者回顾一段时间内的进展。根据给出的会话、决策和需求记录，用中文写一份简洁的回顾，使用以下 Markdown 结构：
## 概览
一到两句话总结这段时间整体推进了什么。
## 各项目进展
每个项目一个三级标题（### 项目名），下面用 2 到 4 条要点列出做成了什么、还在进行什么。
## 需要你跟进
列出仍在等用户处理的事（待答复决策、待验收、等回复的会话）；没有就写“暂无”。
只根据给出的记录写，不要编造；要点具体，避免空话。`

// Recap 生成 [from, to] 的时段回顾。
func (h *Hub) Recap(ctx context.Context, from, to time.Time) (string, error) {
	s, ok := h.aiSettings()
	if !ok {
		return "", fmt.Errorf("还没有配置模型")
	}
	if !s.AI.Recap {
		return "", fmt.Errorf("时段回顾未开启")
	}
	sessions, err := h.store.SessionsBetween(ctx, from, to)
	if err != nil {
		return "", err
	}
	var b strings.Builder
	fmt.Fprintf(&b, "时间范围：%s 至 %s\n\n# 会话\n", from.Local().Format("01-02 15:04"), to.Local().Format("01-02 15:04"))
	for _, x := range sessions {
		name := x.ProjectName
		if name == "" {
			name = x.Cwd
		}
		fmt.Fprintf(&b, "- 项目：%s｜%s @ %s｜当前状态：%s", name, x.Tool, x.DeviceName, x.State)
		if x.Title != "" {
			fmt.Fprintf(&b, "｜标题：%s", x.Title)
		}
		if x.Summary != "" {
			fmt.Fprintf(&b, "｜进展：%s", x.Summary)
		}
		b.WriteString("\n")
		prompts, _ := h.store.PromptsBetween(ctx, x.Key, from, to, 8)
		for _, p := range prompts {
			fmt.Fprintf(&b, "  - 用户指令：%s\n", p)
		}
		if b.Len() > 14000 {
			break
		}
	}
	decs, _ := h.store.DecisionsBetween(ctx, from, to)
	if len(decs) > 0 {
		b.WriteString("\n# 决策\n")
		for _, d := range decs {
			fmt.Fprintf(&b, "- [%s] %s", d.Status, summary.Truncate(d.Question, 120))
			if d.Answer != "" {
				fmt.Fprintf(&b, " → 用户答复：%s", summary.Truncate(d.Answer, 120))
			}
			b.WriteString("\n")
		}
	}
	reqs, _ := h.store.RequirementsUpdatedBetween(ctx, from, to)
	if len(reqs) > 0 {
		b.WriteString("\n# 需求变化\n")
		for _, r := range reqs {
			fmt.Fprintf(&b, "- %s %s：%s（项目：%s）\n", r.Code, r.Title, r.Status, r.ProjectName)
		}
	}
	if len(sessions) == 0 && len(decs) == 0 && len(reqs) == 0 {
		return "", fmt.Errorf("这段时间没有活动记录")
	}
	return summary.Chat(h.aiCtx(ctx, aiRecap), s.Summary, recapPrompt, summary.Truncate(b.String(), 16000), 1500)
}
