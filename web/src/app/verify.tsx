import { ScanSearch } from 'lucide-react'
import type { Requirement, ReviewState } from '../api/client'
import { useSessions } from '../api/queries'
import { t } from '../i18n'
import { reviewPhase } from '../lib/review'
import { cx } from '../lib/util'
import { Badge, Tip } from '../ui/base'
import { RoleTag } from '../ui/marks'
import { sessionTitle } from './SessionCard'

// 评审验证（R-232、R-237，D-714）：交给评审的待验收需求，评审交意见前不进「要你处理」，需求上写「评审中 · 谁在审」；
// 交了意见进验收卡，卡上带评审结论；30 分钟没动静（或评审会话结束）照常弹给用户，标「评审未完成」。
// 状态由 Hub 推出来（Requirement.review）；到点时 Hub 会推一次刷新，这里再按 due_at 兜底判断一次。

export { reviewHeld, reviewPhase } from '../lib/review'

// useReviewerName：在审的评审叫什么（会话标题，取不到时用评审交意见时的快照，再取不到用会话键）。
function useReviewerName(v?: ReviewState | null) {
  const sessions = useSessions()
  if (!v) return ''
  const s = (sessions.data?.sessions ?? []).find((x) => x.key === v.session_key)
  return (s && sessionTitle(s)) || v.session_title || v.session_key
}

// reviewResult：评审结论的短句——通过 x/y、不通过 n 条、已交意见。
function reviewResult(v: ReviewState) {
  if (v.fail > 0) return { text: t.verify.fail(v.fail), tone: 'danger' as const }
  if (v.total > 0) return { text: t.verify.pass(v.pass, v.total), tone: 'run' as const }
  return { text: t.verify.done, tone: 'neutral' as const }
}

// ReviewBadge：需求抽屉、需求列表里的评审标——「评审中 · 名字」「评审未完成」「评审 通过 3/3」「评审 不通过 1 条」。
export function ReviewBadge({ r, className }: { r: Requirement; className?: string }) {
  const phase = reviewPhase(r)
  const v = r.review
  const who = useReviewerName(v)
  if (!phase || !v) return null
  const icon = <ScanSearch className="size-3" />
  let body
  if (phase === 'reviewing')
    body = (
      <Badge tone="wait" icon={icon}>
        {t.verify.reviewing(who)}
      </Badge>
    )
  else if (phase === 'overdue')
    body = (
      <Badge tone="reply" icon={icon}>
        {t.verify.overdue}
      </Badge>
    )
  else {
    const x = reviewResult(v)
    body = (
      <Badge tone={x.tone} icon={icon}>
        {t.verify.mark(x.text)}
      </Badge>
    )
  }
  return (
    <Tip content={phase === 'reviewing' ? t.verify.reviewingTip(who) : phase === 'overdue' ? t.verify.overdueTip(who) : v.verdict || who}>
      <span className={cx('inline-flex min-w-0 shrink-0', className)}>{body}</span>
    </Tip>
  )
}

// ReviewMark：「要你处理」验收条目上的评审结论（与主助理的建议并排）：评审 · 通过 3/3 / 不通过 1 条 / 未完成。
export function ReviewMark({ r }: { r?: Requirement }) {
  const phase = reviewPhase(r)
  const v = r?.review
  if (!phase || !v || phase === 'reviewing') return null
  const x = phase === 'overdue' ? { text: t.verify.unfinished, tone: 'reply' as const } : reviewResult(v)
  return (
    <span title={v.verdict || undefined} className="inline-flex shrink-0 items-center gap-1">
      <RoleTag role="reviewer" />
      <span className={cx('text-[11.5px] font-medium', x.tone === 'danger' ? 'text-danger' : x.tone === 'run' ? 'text-run' : x.tone === 'reply' ? 'text-reply' : 'text-fg-3')}>{x.text}</span>
    </span>
  )
}

// ReviewChip：看板小块上的「评审中」「评审未完成」（和「设计」小标同样大小）；交了意见的不标，点开看结论。
export function ReviewChip({ r }: { r: Requirement }) {
  const phase = reviewPhase(r)
  if (phase !== 'reviewing' && phase !== 'overdue') return null
  return (
    <span className={cx('shrink-0 rounded px-1 text-[11px] leading-4 font-medium', phase === 'reviewing' ? 'bg-wait-soft text-wait' : 'bg-reply-soft text-reply')}>
      {phase === 'reviewing' ? t.verify.inProgress : t.verify.overdue}
    </span>
  )
}
