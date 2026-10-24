import { useMemo } from 'react'
import type { Decision, Plan, Requirement, Session } from '../api/client'
import { useMorning, useRequirement } from '../api/queries'
import { t } from '../i18n'
import { cx, relTime } from '../lib/util'
import { displayState, RoleBadge, RoleTag } from '../ui/marks'
import { plainText } from '../ui/markdown'
import type { useWorkbench } from './inbox'
import { gateStage, needsApproval } from './plans'
import { awaitingReply } from './replyBox'
import { sessionTitle, splitTool } from './SessionCard'

// 「要你处理」的条目（R-177）：所有要你下判断的事只有一个界面——右侧的审批抽屉（app/approval.tsx）。
// 工作台左栏、会话面板、项目页、⌘K 只放固定大小的条目（类型 + 标题 + 一行状态 + 参谋建议 + 等待时长），
// 不铺开证据、不就地展开、不就地作答；点开进抽屉，抽屉里可以上一条 / 下一条连续处理。
// 条目的键也是抽屉的地址（?a=键）：r:R-1 需求验收、d:D-2 决策、p:P-3 计划批准、g:P-3 阶段验收、o:D-4 转交、
// s:会话键 会话在等授权 / 在问你、m:会话键 在等你回复（不计件数）、x:D-5 推翻参谋的代答（只从离开期间小结打开，不在队列里）。

export type AskKind = 'review' | 'decide' | 'approve' | 'request' | 'plan' | 'stage' | 'orphan' | 'permit' | 'question' | 'reply' | 'proxy'

export type Ask = {
  key: string
  kind: AskKind
  title: string
  at: string // 从什么时候开始等你
  counted: boolean // 计入「要你处理」件数（在等你回复的会话不计）
  code?: string
  s?: Session
  projectId: string
  projectName: string
  req?: Requirement
  d?: Decision
  plan?: Plan
  stage?: number // 阶段验收：等你验收的阶段
}

type WB = ReturnType<typeof useWorkbench>

// 越小越靠前：拍板 / 批准 → 会话在等你 → 验收 → 计划 / 阶段 → 转交 → 在等你回复；同类里等得越久越靠前。
const rank: Record<AskKind, number> = { approve: 0, request: 0, decide: 0, permit: 1, question: 1, review: 2, stage: 3, plan: 3, orphan: 4, reply: 5, proxy: 9 }

// buildAsks：从工作台数据算出全部条目（工作台、侧栏角标、抽屉队列同一口径）。
export function buildAsks(wb: WB): Ask[] {
  const out: Ask[] = []
  const reviewing = new Set<string>()
  for (const th of wb.threads) for (const r of th.reviews) reviewing.add(r.code)
  const seen = new Set<string>()
  const push = (a: Ask) => {
    if (seen.has(a.key)) return
    seen.add(a.key)
    out.push(a)
  }
  for (const th of wb.threads) {
    const s = th.s
    const pid = (x?: { project_id?: string }) => x?.project_id || s?.project_id || ''
    const pname = (x?: { project_name?: string }) => x?.project_name || s?.project_name || ''
    for (const r of th.reviews) push({ key: `r:${r.code}`, kind: 'review', title: r.title, at: r.updated_at, counted: true, code: r.code, s, req: r, projectId: pid(r), projectName: pname(r) })
    for (const d of th.decisions) {
      // 挂在待验收需求上的决策在验收视图里一起处理，不另列一条
      if (d.requirement_code && reviewing.has(d.requirement_code)) continue
      push({ key: `d:${d.code}`, kind: d.approval ? 'approve' : 'decide', title: d.short_title || d.question, at: d.created_at, counted: true, code: d.code, s, d, projectId: pid(d), projectName: pname(d) })
    }
    for (const d of th.orphans) push({ key: `o:${d.code}`, kind: 'orphan', title: d.short_title || d.question, at: d.answered_at ?? d.created_at, counted: true, code: d.code, s, d, projectId: pid(d), projectName: pname(d) })
    if (s && s.state !== 'ended') {
      const ds = displayState(s)
      if (ds === 'waiting_approval' || ds === 'waiting_answer')
        push({ key: `s:${s.key}`, kind: ds === 'waiting_approval' ? 'permit' : 'question', title: sessionTitle(s), at: s.state_since || s.last_active_at, counted: true, s, projectId: s.project_id || '', projectName: s.project_name || '' })
      else if (awaitingReply(s)) push({ key: `m:${s.key}`, kind: 'reply', title: sessionTitle(s), at: s.state_since || s.last_active_at, counted: false, s, projectId: s.project_id || '', projectName: s.project_name || '' })
    }
  }
  for (const g of wb.projects)
    for (const s of g.active)
      if (awaitingReply(s)) push({ key: `m:${s.key}`, kind: 'reply', title: sessionTitle(s), at: s.state_since || s.last_active_at, counted: false, s, projectId: s.project_id || '', projectName: s.project_name || '' })
  for (const x of wb.plans) {
    const st = gateStage(x)
    if (st) push({ key: `g:${x.code}`, kind: 'stage', title: x.title, at: x.updated_at, counted: true, code: x.code, plan: x, stage: st.seq, projectId: x.project_id || '', projectName: x.project_name || '' })
    else if (needsApproval(x)) push({ key: `p:${x.code}`, kind: 'plan', title: x.title, at: x.updated_at, counted: true, code: x.code, plan: x, projectId: x.project_id || '', projectName: x.project_name || '' })
  }
  return out.sort((a, b) => rank[a.kind] - rank[b.kind] || a.at.localeCompare(b.at))
}

export function useAsks(wb: WB) {
  return useMemo(() => buildAsks(wb), [wb])
}

export { useOpenAsk } from './nav'

// decisionAsk：单条待决策的条目（会话面板等只列决策的地方用）。
export function decisionAsk(d: Decision, s?: Session): Ask {
  return { key: `d:${d.code}`, kind: d.approval ? 'approve' : 'decide', title: d.short_title || d.question, at: d.created_at, counted: true, code: d.code, s, d, projectId: d.project_id || '', projectName: d.project_name || '' }
}

// ---------- 条目 ----------

const kindTone: Record<AskKind, string> = {
  review: 'text-seal',
  decide: 'text-wait',
  approve: 'text-wait',
  request: 'text-wait',
  plan: 'text-accent',
  stage: 'text-seal',
  orphan: 'text-fg-2',
  permit: 'text-wait',
  question: 'text-reply',
  reply: 'text-reply',
  proxy: 'text-accent',
}

const firstLine = (s?: string) => plainText(s ?? '').split('\n').map((x) => x.trim()).find(Boolean) ?? ''

// ReviewLine：验收条目的一行状态——达标数 + 最近一份证据的结论。
function ReviewLine({ code }: { code: string }) {
  const q = useRequirement(code)
  const ev = (q.data?.evidence ?? []).filter((e) => !e.voided).at(-1)
  if (!q.data) return <span className="text-fg-3">…</span>
  if (!ev) return <span className="text-fg-3">{t.desk.noEvidence}</span>
  const c = ev.payload?.blocks?.find((b) => b.type === 'checklist')
  const items = c?.items ?? []
  const pass = items.filter((x) => x.result === 'pass').length
  const text = ev.payload?.verdict || firstLine(ev.summary)
  return (
    <>
      {items.length > 0 && <b className={cx('mr-1.5 font-semibold tabular', pass === items.length ? 'text-run' : 'text-wait')}>{t.ask.score(pass, items.length)}</b>}
      <span>{text}</span>
    </>
  )
}

function statusLine(a: Ask): React.ReactNode {
  const d = a.d
  switch (a.kind) {
    case 'review':
      return <ReviewLine code={a.code!} />
    case 'decide': {
      if (!d) return ''
      if (d.points?.length) return t.desk.points(d.points.length)
      const rec = d.recommendation?.trim()
      if (rec) return t.ask.rec(rec)
      if (d.options?.length) return t.ask.options(d.options.length)
      return firstLine(d.context) || t.ask.free
    }
    case 'approve':
    case 'request':
      return firstLine(d?.context) || t.desk.approval
    case 'plan': {
      const n = a.plan!.stages.reduce((k, st) => k + st.requirements.length, 0)
      return t.ask.plan(a.plan!.stages.length, n)
    }
    case 'stage': {
      const st = a.plan!.stages.find((x) => x.seq === a.stage)
      const reqs = st?.requirements ?? []
      const total = reqs.reduce((k, r) => k + r.check_total, 0)
      const pass = reqs.reduce((k, r) => k + r.check_pass, 0)
      return [t.ask.stage(a.stage ?? 0, st?.title ?? ''), total ? t.ask.score(pass, total) : ''].filter(Boolean).join(t.common.sep)
    }
    case 'orphan':
      return t.desk.orphanSub
    case 'permit':
      return t.desk.lane.approve(a.s?.state_detail ?? '')
    case 'question':
      return splitTool(a.s?.state_detail ?? '')[1] || t.session.asking
    case 'reply':
      return a.s?.reply_ask?.split('\n').map((x) => x.trim()).filter(Boolean).at(-1) ?? ''
    case 'proxy':
      return d?.proxy_answer ?? ''
  }
}

// Advice：参谋建议标注（R-165）：验收上的「Adv 建议通过 / 退回」，决策上的「Adv 未代答」。
function Advice({ a }: { a: Ask }) {
  const morning = useMorning()
  if (a.kind === 'review') {
    const x = (morning.data?.advice ?? []).find((v) => v.requirement_code === a.code)
    if (!x) return null
    return (
      <span title={x.reason} className="inline-flex shrink-0 items-center gap-1">
        <RoleTag role="advisor" />
        <span className={cx('text-[11.5px] font-medium', x.verdict === 'pass' ? 'text-run' : 'text-danger')}>{t.morning.verdict[x.verdict]}</span>
      </span>
    )
  }
  if (a.d && a.d.status === 'pending') {
    const h = (morning.data?.holds ?? []).find((v) => v.decision_code === a.code)
    if (!h) return null
    return (
      <span title={h.reason} className="inline-flex shrink-0 items-center gap-1">
        <RoleTag role="advisor" />
        <span className="text-[11.5px] text-fg-3">{t.ask.held}</span>
      </span>
    )
  }
  return null
}

// AskItem：固定高度的条目，点开审批抽屉。高度不随内容变（标题、状态都只占一行）。
export function AskItem({
  a,
  now,
  onOpen,
  active,
  lit,
  fresh,
  showProject,
  onHover,
  className,
}: {
  a: Ask
  now: number
  onOpen: (key: string) => void
  active?: boolean
  lit?: boolean
  fresh?: boolean
  showProject?: boolean
  onHover?: (k: string | null) => void
  className?: string
}) {
  const s = a.s
  const who = s ? sessionTitle(s) : a.projectName || t.thread.noSession
  return (
    <button
      type="button"
      data-nav-item=""
      data-ask={a.key}
      onClick={() => onOpen(a.key)}
      onMouseEnter={onHover && s ? () => onHover(s.key) : undefined}
      onMouseLeave={onHover ? () => onHover(null) : undefined}
      className={cx(
        'group grid h-[86px] w-full shrink-0 grid-rows-3 content-center gap-y-0.5 overflow-hidden rounded-[14px] border bg-surface px-3.5 text-left shadow-card transition-[border,box-shadow,background] duration-150 hover:border-line-strong hover:shadow-pop',
        a.counted ? 'border-reply/25' : 'border-line',
        active && 'border-accent/60 ring-2 ring-accent/25',
        fresh && 'desk-drop',
        lit && 'desk-lit',
        className,
      )}
    >
      <span className="flex min-w-0 items-center gap-2 text-[11.5px] leading-5 text-fg-3">
        <b className={cx('shrink-0 font-semibold tracking-wider', kindTone[a.kind])}>{t.ask.kind[a.kind]}</b>
        {s && <RoleBadge s={s} />}
        <span className="min-w-0 truncate">{showProject && a.projectName && s ? `${a.projectName}${t.common.sep}${who}` : who}</span>
        <span className="ml-auto shrink-0 tabular">{relTime(a.at, now)}</span>
      </span>
      <span className="flex min-w-0 items-center gap-1.5 leading-5">
        {a.code && <span className="shrink-0 font-mono text-[11.5px] text-fg-3">{a.code}</span>}
        <span className="min-w-0 truncate text-[14px] font-semibold text-fg group-hover:text-accent">{a.title}</span>
      </span>
      <span className="flex min-w-0 items-center gap-2 text-[12.5px] leading-5 text-fg-2">
        <span className="min-w-0 flex-1 truncate">{statusLine(a)}</span>
        <Advice a={a} />
      </span>
    </button>
  )
}
