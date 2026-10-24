// 状态和标记只来自明确的记录（R-239，设计原则第 11 条）。这里集中界面读这些字段的口径，不从文字里猜：
// - 会话在等你：只认等授权（state = waiting_approval）和提问工具在问（wait_kind = question）；对话里写的问句不算。
// - 批准 / 拒绝：读决策的 verdict；推荐选项：读 rec_option。老数据由 Hub 补列时回填过一次，这里不再兜底推。

type SessionLike = { state: string; wait_kind?: string }
type DecisionLike = { approval?: boolean; verdict?: string; rec_option?: number; options?: string[] | null }

// sessionAsk：会话此刻是否在正式地等你——permit 等你授权、question 用提问工具在问你；都不是为空。
export function sessionAsk(s: SessionLike): 'permit' | 'question' | '' {
  if (s.state === 'waiting_approval') return 'permit'
  if (s.state === 'waiting_reply' && s.wait_kind === 'question') return 'question'
  return ''
}

// approvedOf：批准类决策的结论；非批准类或还没结论时为 undefined。
export function approvedOf(d: DecisionLike): boolean | undefined {
  if (!d.approval) return undefined
  if (d.verdict === 'approved') return true
  if (d.verdict === 'rejected') return false
  return undefined
}

// recOf：推荐的是第几个选项，-1 = 没有。
export function recOf(d: DecisionLike): number {
  const i = d.rec_option ?? -1
  return i >= 0 && i < (d.options?.length ?? 0) ? i : -1
}

// verdictOf：作答时随答复带上的结论（批准类按用户点的按钮，R-239）。
export function verdictOf(scene: string, act: string): 'approved' | 'rejected' | undefined {
  if (scene !== 'approval' && scene !== 'request') return undefined
  if (act === 'pass') return 'approved'
  if (act === 'back') return 'rejected'
  return undefined
}
