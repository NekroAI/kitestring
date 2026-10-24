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
import { isStatusReq, StatusReqLines } from './statusReq'
import { DraftMark, dropDraft } from './draft'
import { Check, Undo2 } from 'lucide-react'
import { SwipeRow, type RowAction } from '../ui/gesture'
import { useAskReason } from '../ui/overlay'
import { composeAnswer, decKey, useOutbox } from './outbox'
import { PendingCover, usePending, useReqVerdict } from './verdict'

// 「要你处理」的条目（R-177）：所有要你下判断的事只有一个界面——右侧的审批抽屉（app/approval.tsx）。
// 工作台左栏、会话面板、项目页、⌘K 只放固定大小的条目（类型 + 标题 + 一行状态 + 参谋建议 + 等待时长），
// 不铺开证据、不就地展开、不就地作答；点开进抽屉，抽屉里可以上一条 / 下一条连续处理。
// 唯一的例外（D-491，R-73）：触屏上验收、批准类决策可以左滑露出「通过 / 退回」（useAskActions）。
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
      // 挂在待验收需求上的决策在需求抽屉里一起处理，不另列一条
      if (d.requirement_code && reviewing.has(d.requirement_code)) continue
      push({ key: `d:${d.code}`, kind: decisionKind(d), title: askTitle(d), at: d.created_at, counted: true, code: d.code, s, d, projectId: pid(d), projectName: pname(d) })
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

// decisionKind：决策归哪一类——Agent 的限时请求（R-169 推荐开 Dev 会话）、批准类、选项类。
export function decisionKind(d: Decision): AskKind {
  if (d.request_kind) return 'request'
  return d.approval ? 'approve' : 'decide'
}

// askKindLabel：条目的类型词；改状态申请（R-188）叫「申请」。
export function askKindLabel(a: Ask) {
  return a.kind === 'request' && isStatusReq(a.d) ? t.ask.status.kind : t.ask.kind[a.kind]
}

// askTitle：决策条目的标题。改状态申请（R-188）用需求标题——你要一眼看到是哪条需求。
export function askTitle(d: Decision) {
  if (isStatusReq(d)) return d.requirement_title || d.question
  return d.short_title || d.question
}

// requestLeft：限时请求还剩多久到点（到点按规则视为拒绝）。
export function requestLeft(d: Decision, now: number) {
  if (!d.expires_at) return ''
  const ms = new Date(d.expires_at).getTime() - now
  return ms > 0 ? t.ask.request.left(Math.ceil(ms / 60000)) : t.ask.request.expiring
}

// decisionAsk：单条待决策的条目（会话面板等只列决策的地方用）。
export function decisionAsk(d: Decision, s?: Session): Ask {
  return { key: `d:${d.code}`, kind: decisionKind(d), title: askTitle(d), at: d.created_at, counted: true, code: d.code, s, d, projectId: d.project_id || '', projectName: d.project_name || '' }
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

function statusLine(a: Ask, now: number): React.ReactNode {
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
      return firstLine(d?.context) || t.desk.approval
    case 'request':
      if (isStatusReq(d)) return d?.context ?? ''
      return d ? [t.ask.request.what(d.request_count ?? 1), requestLeft(d, now)].filter(Boolean).join(t.common.sep) : t.desk.approval
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
        <RoleTag role="aide" />
        <span className={cx('text-[11.5px] font-medium', x.verdict === 'pass' ? 'text-run' : 'text-danger')}>{t.morning.verdict[x.verdict]}</span>
      </span>
    )
  }
  if (a.d && a.d.status === 'pending') {
    const h = (morning.data?.holds ?? []).find((v) => v.decision_code === a.code)
    if (!h) return null
    return (
      <span title={h.reason} className="inline-flex shrink-0 items-center gap-1">
        <RoleTag role="aide" />
        <span className="text-[11.5px] text-fg-3">{t.ask.held}</span>
      </span>
    )
  }
  return null
}

// swipeKey：这件事在撤销期里用的键（与审批抽屉、Verdict 一致）；没有左滑快捷操作的返回 undefined。
function swipeKey(a: Ask) {
  if (a.kind === 'review' && a.code) return a.code
  const d = a.d
  // 只给批准类单点决策（批准 / 拒绝）；选项类要先选、多点决策要逐点选，进抽屉做
  if ((a.kind === 'approve' || a.kind === 'request') && d && d.approval && !d.points?.length && d.status === 'pending' && !d.voided) return decKey(d.code)
  return undefined
}

// useAskActions：列表左滑露出的「通过 / 退回」（D-491，是 R-177「审批只在右侧抽屉里作答」的例外，见 docs/principles.md 第 2 条）。
// 点了才执行，走全站同一个撤销期（verdict.tsx 的 schedule）；退回 / 拒绝先写意见（原因框，R-202 的草稿：关掉再打开还在，提交成功才清）。
function useAskActions(a: Ask): RowAction[] {
  const verdict = useReqVerdict()
  const { send } = useOutbox()
  const ask = useAskReason()
  const key = swipeKey(a)
  if (!key) return []
  if (a.kind === 'review') {
    const code = a.code!
    const draft = `reason:${code}:back`
    return [
      {
        label: t.verdict.back('accept'),
        icon: <Undo2 />,
        tone: 'plain',
        run: async () => {
          const r = await ask({ title: t.verdict.askTitle(code), description: t.verdict.askDesc, confirmText: t.verdict.back('accept'), placeholder: t.verdict.askPlaceholder, kinds: [], required: true, multiline: true, draft })
          if (r) verdict.back(code, { comment: r.note, after: () => dropDraft(draft) })
        },
      },
      { label: t.verdict.pass('accept'), icon: <Check />, tone: 'primary', run: () => void verdict.pass(code) },
    ]
  }
  const d = a.d!
  const scene = d.request_kind ? 'request' : 'approval'
  const options = d.options ?? []
  const draft = `reason:${d.code}:back`
  return [
    {
      label: t.verdict.back(scene),
      icon: <Undo2 />,
      tone: 'plain',
      run: async () => {
        const r = await ask({ title: t.verdict.rejectTitle(scene, d.code), description: t.verdict.rejectDesc, confirmText: t.verdict.back(scene), placeholder: t.verdict.rejectPlaceholder(scene), kinds: [], required: true, multiline: true, draft })
        if (r) send([{ code: d.code, answer: composeAnswer({ choice: 1, note: r.note }, options), act: 'back', scene, after: () => dropDraft(draft) }])
      },
    },
    { label: t.verdict.pass(scene), icon: <Check />, tone: 'primary', run: () => send([{ code: d.code, answer: composeAnswer({ choice: 0, note: '' }, options), act: 'pass', scene }]) },
  ]
}

// AskItem：固定高度的条目，点开审批抽屉。高度不随内容变（标题、状态都只占一行）。
// 触屏上验收、批准类决策可以左滑露出「通过 / 退回」（swipe = false 时不给，例如会话面板里）。
export function AskItem(props: Parameters<typeof AskCard>[0] & { swipe?: boolean }) {
  const { swipe = true, ...rest } = props
  const actions = useAskActions(props.a)
  const key = swipeKey(props.a)
  const pend = usePending(key)
  if (!swipe || !actions.length) return <AskCard {...rest} spotKey={props.a.s?.key ?? ''} />
  return (
    <SwipeRow actions={actions} disabled={!!pend} className="shrink-0" spotKey={props.a.s?.key ?? ''}>
      <AskCard {...rest} />
      {key && <PendingCover k={key} />}
    </SwipeRow>
  )
}

function AskCard({
  a,
  now,
  onOpen,
  active,
  showProject,
  className,
  spotKey,
}: {
  a: Ask
  now: number
  onOpen: (key: string) => void
  active?: boolean
  showProject?: boolean
  className?: string
  spotKey?: string
}) {
  const s = a.s
  const who = s ? sessionTitle(s) : a.projectName || t.thread.noSession
  // 改状态申请（R-188）：多一行——需求编号 + 标题、一句话说明、当前 → 目标状态 + 理由；卡片上不作答，点开进抽屉
  const sr = a.kind === 'request' && isStatusReq(a.d) ? a.d : undefined
  return (
    <button
      type="button"
      data-nav-item=""
      data-ask={a.key}
      onClick={() => onOpen(a.key)}
      data-spot-key={spotKey}
      className={cx(
        'group grid w-full shrink-0 content-center gap-y-0.5 overflow-hidden rounded-[14px] border bg-surface px-3.5 text-left shadow-card transition-[border,box-shadow,background,opacity,filter] duration-200 hover:border-line-strong hover:shadow-pop',
        sr ? 'h-[106px] grid-rows-4' : 'h-[86px] grid-rows-3',
        a.counted ? 'border-reply/25' : 'border-line',
        active && 'border-accent/60 ring-2 ring-accent/25',
        className,
      )}
    >
      <span className="flex min-w-0 items-center gap-2 text-[11.5px] leading-5 text-fg-3">
        <b className={cx('shrink-0 font-semibold tracking-wider', kindTone[a.kind])}>{sr ? t.ask.status.kind : t.ask.kind[a.kind]}</b>
        {s && <RoleBadge s={s} />}
        <span className="min-w-0 truncate">{showProject && a.projectName && s ? `${a.projectName}${t.common.sep}${who}` : who}</span>
        {/* 有没发出的草稿（R-202） */}
        <span className="ml-auto flex shrink-0 items-center gap-1.5">
          <DraftMark owners={[a.code, s && (a.kind === 'reply' || a.kind === 'question' || a.kind === 'permit') ? `s:${s.key}` : undefined]} />
          <span className="tabular">{relTime(a.at, now)}</span>
        </span>
      </span>
      <span className="flex min-w-0 items-center gap-1.5 leading-5">
        {(sr ? sr.requirement_code : a.code) && <span className="shrink-0 font-mono text-[11.5px] text-fg-3">{sr ? sr.requirement_code : a.code}</span>}
        <span className="min-w-0 truncate text-[14px] font-semibold text-fg group-hover:text-accent">{a.title}</span>
      </span>
      {sr ? (
        <StatusReqLines d={sr} />
      ) : (
        <span className="flex min-w-0 items-center gap-2 text-[12.5px] leading-5 text-fg-2">
          <span className="min-w-0 flex-1 truncate">{statusLine(a, now)}</span>
          <Advice a={a} />
        </span>
      )}
    </button>
  )
}
