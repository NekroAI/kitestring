// 评审验证（R-232、R-237）：一条需求这一轮的评审状态。Hub 推出 review.state；到点（due_at）前后 Hub 会推一次刷新，
// 这里再按 due_at 兜底：标着评审中、但已经过了时限的，按「评审未完成」算，照常进「要你处理」。

export type ReviewPhase = 'reviewing' | 'overdue' | 'done'

type ReqLike = { status: string; review?: { state: string; due_at?: string | null } | null }

export function reviewPhase(r: ReqLike | undefined, now = Date.now()): ReviewPhase | undefined {
  const v = r?.status === 'review' ? r.review : undefined
  if (!v) return undefined
  if (v.state === 'reviewing' && v.due_at && Date.parse(v.due_at) <= now) return 'overdue'
  return v.state as ReviewPhase
}

// reviewHeld：评审交意见前，这条需求不进「要你处理」（不计件数、不出卡片）。需求页上照样能直接验收。
export const reviewHeld = (r: ReqLike, now = Date.now()) => reviewPhase(r, now) === 'reviewing'
