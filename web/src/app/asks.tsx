import { useMemo } from 'react'
import type { Decision, Plan, Requirement, Session } from '../api/client'
import { useMorning, useRequirement } from '../api/queries'
import { t } from '../i18n'
import { cx, relTime } from '../lib/util'
import { sessionAsk, titleOf } from '../lib/waiting'
import { displayState, RoleBadge, RoleTag } from '../ui/marks'
import { plainText } from '../ui/markdown'
import type { useWorkbench } from './inbox'
import { gateStage, needsApproval } from './plans'
import { sessionTitle, splitTool } from './SessionCard'
import { isStatusReq, StatusReqLines } from './statusReq'
import { DraftMark, dropDraft } from './draft'
import { Check, Hourglass, Snowflake, Undo2 } from 'lucide-react'
import { SwipeRow, type RowAction } from '../ui/gesture'
import { useAskReason } from '../ui/overlay'
import { composeAnswer, decKey, useOutbox } from './outbox'
import { useWarmFlag } from './laneFeed'
import { PendingCover, usePending, usePendingMap, useReqVerdict } from './verdict'
import { ReviewMark } from './verify'
import type { BlockedWhy } from '../lib/waiton'
import type { ShareComment } from '../api/queries'

// 「要你处理」的条目（R-177）：所有要你下判断的事只有一个界面——右侧的审批抽屉（app/approval.tsx）。
// 工作台左栏、会话面板、项目页、⌘K 只放固定大小的条目（类型 + 标题 + 一行状态 + 参谋建议 + 等待时长），
// 不铺开证据、不就地展开、不就地作答；点开进抽屉，抽屉里可以上一条 / 下一条连续处理。
// 唯一的例外（D-491，R-73）：触屏上验收、批准类决策可以左滑露出「通过 / 退回」（useAskActions）。
// 条目的键也是抽屉的地址（?a=键）：r:R-1 需求验收、d:D-2 决策、p:P-3 计划批准、g:P-3 阶段验收、o:D-4 转交、
// s:会话键 会话在等授权 / 用提问工具在问你、x:D-5 推翻参谋的代答（只从离开期间小结打开，不在队列里）、
// v:留言 ID 待收的访客留言（R-281：放入需求或忽略）。
// 只认明确记录（R-239）：会话只在对话里写了问句、没提决策的，不进这里。

export type AskKind = 'review' | 'decide' | 'approve' | 'request' | 'plan' | 'stage' | 'orphan' | 'permit' | 'question' | 'proxy' | 'blocked' | 'waited' | 'stalled' | 'unstarted' | 'guest'

export type Ask = {
  key: string
  kind: AskKind
  title: string
  at: string // 从什么时候开始等你
  counted: boolean // 计入「要你处理」件数（代答记录不计）
  code?: string
  s?: Session
  projectId: string
  projectName: string
  req?: Requirement
  d?: Decision
  plan?: Plan
  stage?: number // 阶段验收：等你验收的阶段
  waitedBy?: string[] // 在等这一条的受阻需求（R-243）：「R-210 在等这条」
  blockedWhy?: BlockedWhy // 受阻需求本身进来的原因（R-243）
  gc?: ShareComment // 访客留言（R-281）
}

type WB = ReturnType<typeof useWorkbench>

// 越小越靠前：决策 / 批准 → 会话在等你 → 验收 → 计划 / 阶段 → 转交；同类里等得越久越靠前。
// 受阻 / 被等着定稿的草稿和计划同档（解开它工作才能往下走）；停滞和转交同档。
// 访客留言不挡任何进行中的工作，排在最后（R-281）。
const rank: Record<AskKind, number> = { approve: 0, request: 0, decide: 0, permit: 1, question: 1, review: 2, stage: 3, plan: 3, waited: 3, blocked: 3, orphan: 4, stalled: 4, unstarted: 3, guest: 5, proxy: 9 }

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
    for (const d of th.orphans) push({ key: `o:${d.code}`, kind: 'orphan', title: titleOf(d), at: d.answered_at ?? d.created_at, counted: true, code: d.code, s, d, projectId: pid(d), projectName: pname(d) })
    if (s && s.state !== 'ended') {
      const ds = displayState(s)
      const k = ds === 'done' ? '' : sessionAsk(s) // 等授权、提问工具在问才算（R-239）
      if (k) push({ key: `s:${s.key}`, kind: k, title: sessionTitle(s), at: s.state_since || s.last_active_at, counted: true, s, projectId: s.project_id || '', projectName: s.project_name || '' })
    }
  }
  // 等你的事（R-243，lib/waiton.ts）：归到最近活跃的关联会话，没有就归项目
  const att = wb.attention
  const byKey = new Map(wb.sessions.map((x) => [x.key, x]))
  const ownerOf = (r: Requirement) => r.sessions.map((k) => byKey.get(k)).filter((x): x is Session => !!x).sort((a, b) => b.last_active_at.localeCompare(a.last_active_at))[0]
  const reqAsk = (r: Requirement, kind: AskKind, at: string, extra: Partial<Ask> = {}): Ask => {
    const s = ownerOf(r)
    return { key: `${kind[0]}:${r.code}`, kind, title: r.title, at, counted: true, code: r.code, s, req: r, projectId: r.project_id || s?.project_id || '', projectName: r.project_name || s?.project_name || '', ...extra }
  }
  const full = (code: string) => wb.reqs.find((r) => r.code === code)
  for (const x of att.drafts) {
    const r = full(x.code)
    if (r) push({ ...reqAsk(r, 'waited', r.updated_at), key: `w:${r.code}`, s: undefined })
  }
  for (const x of att.blocked) {
    const r = full(x.r.code)
    if (r) push(reqAsk(r, 'blocked', r.updated_at, { blockedWhy: x.why }))
  }
  for (const x of att.stalled) {
    const r = full(x.r.code)
    if (r) push({ ...reqAsk(r, 'stalled', x.since), key: `t:${r.code}` })
  }
  // 指派后未开始处理（R-278）：归到指派的接收方
  for (const x of att.unstarted) {
    const r = full(x.r.code)
    const s = r?.offered_to ? byKey.get(r.offered_to) : undefined
    if (r) push({ ...reqAsk(r, 'unstarted', x.since), key: `u:${r.code}`, ...(s ? { s } : {}) })
  }
  for (const x of wb.plans) {
    const st = gateStage(x)
    if (st) push({ key: `g:${x.code}`, kind: 'stage', title: x.title, at: x.updated_at, counted: true, code: x.code, plan: x, stage: st.seq, projectId: x.project_id || '', projectName: x.project_name || '' })
    else if (needsApproval(x)) push({ key: `p:${x.code}`, kind: 'plan', title: x.title, at: x.updated_at, counted: true, code: x.code, plan: x, projectId: x.project_id || '', projectName: x.project_name || '' })
  }
  for (const c of wb.guests) push(guestAsk(c))
  for (const a of out) {
    const by = a.code ? att.waitedBy.get(a.code) : undefined
    if (by?.length) a.waitedBy = by
  }
  return out.sort((a, b) => rank[a.kind] - rank[b.kind] || a.at.localeCompare(b.at))
}

// guestAsk：一条待收的访客留言（R-281）。标题是所属需求（挂在阶段上的写计划和阶段）。
export function guestAsk(c: ShareComment): Ask {
  const stage = c.target_kind === 'stage'
  return {
    key: `v:${c.id}`,
    kind: 'guest',
    title: stage ? c.target_title || c.plan_title || '' : c.requirement_title || '',
    at: c.created_at,
    counted: true,
    code: stage ? c.target_code : c.requirement_code,
    gc: c,
    projectId: c.project_id || '',
    projectName: '',
  }
}

// guestTarget：访客留言针对的对象写成一个短标签（挂在需求本身时为空）。
export function guestTarget(c: { target_kind: string; target_code?: string; target_seq?: number; target_title?: string }) {
  if (c.target_kind === 'dec') return c.target_code ?? ''
  if (c.target_kind === 'evidence') return t.share.inbox.onEvidence(c.target_title ?? '')
  if (c.target_kind === 'stage') return t.share.inbox.onStage(c.target_code ?? '', c.target_seq ?? 0)
  return ''
}

export function useAsks(wb: WB) {
  return useMemo(() => buildAsks(wb), [wb])
}

// pendingKeyOf：这件事在撤销期 / 盖印中用的键（与 Verdict 的 pendingKey、发件箱的 decKey 一致）。
export function pendingKeyOf(a: Ask | undefined) {
  if (!a) return undefined
  if (a.kind === 'review') return a.code
  if (a.kind === 'plan') return `plan:${a.code}`
  if (a.kind === 'stage') return `stage:${a.code}:${a.stage}`
  if (a.d && (a.kind === 'decide' || a.kind === 'approve' || a.kind === 'request')) return decKey(a.d.code)
  if (a.kind === 'guest' && a.gc) return `guest:${a.gc.id}`
  return undefined
}

// useLeft：撤销期一结束（进入盖印 / 已提交）这件事就在本地离开「要你处理」，不等服务端刷新（R-230，原则第 8 条）；
// 撤销期里（wait）还在列表里，元素上盖着撤销条；提交失败时调度器放掉这件事，它自己回到列表（带进场动画）。
// 只管列表和件数；审批抽屉的队列仍按完整的 asks 走（已下判断的事在抽屉里照常显示印章）。
export function useLeft() {
  const pend = usePendingMap()
  return (a: Ask) => {
    const k = pendingKeyOf(a)
    const e = k ? pend.get(k) : undefined
    return !!e && e.act !== 'move' && e.phase !== 'wait'
  }
}

// useStaying：去掉已经离开的，结果按内容稳定（调度器的状态变了才换数组，别处的 useMemo 不会白算）。
export function useStaying(all: Ask[]) {
  const left = useLeft()
  const gone = all.filter(left).map((a) => a.key).join('|')
  return useMemo(() => (gone ? all.filter((a) => !gone.split('|').includes(a.key)) : all), [all, gone])
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
  return titleOf(d)
}

// offeredName：指派后未开始处理的条目上写的接收方（R-278）：会话名，找不到会话时为空。
export function offeredName(a: Ask) {
  const s = a.s && a.s.key === a.req?.offered_to ? a.s : undefined
  return s ? sessionTitle(s) : ''
}

// roughDuration：停滞多久，取整到小时（不到 1 小时写分钟），如「5 小时」（R-243）。
export function roughDuration(iso: string, now: number) {
  const m = Math.max(0, Math.floor((now - Date.parse(iso)) / 60000))
  return m < 60 ? `${m} 分钟` : `${Math.floor(m / 60)} 小时`
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
  blocked: 'text-danger',
  waited: 'text-wait',
  stalled: 'text-reply',
  unstarted: 'text-wait',
  guest: 'text-accent',
  permit: 'text-wait',
  question: 'text-reply',
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
    case 'proxy':
      return d?.proxy_answer ?? ''
    case 'waited':
      return t.ask.waitedDraft(a.waitedBy ?? [])
    case 'blocked': {
      const r = a.req
      if (a.blockedWhy === 'gone') return t.ask.blockedGone(r?.blocked_on ?? '')
      if (a.blockedWhy === 'missing') return t.ask.blockedMissing(r?.blocked_on ?? '')
      return r?.blocked_reason ? t.ask.blockedWhy(r.blocked_reason) : t.ask.blockedBare
    }
    case 'stalled':
      return t.ask.stalledLine(roughDuration(a.at, now))
    case 'unstarted':
      return t.ask.unstartedLine(roughDuration(a.at, now))
    case 'guest': {
      const on = a.gc && a.gc.target_kind !== 'stage' ? guestTarget(a.gc) : ''
      return (
        <>
          {on && <span className="mr-1.5 font-mono text-[11.5px] text-fg-3">{on}</span>}
          {firstLine(a.gc?.text)}
        </>
      )
    }
  }
}

// askLine：泳道上「等你」那一行写的明确记录本身（R-239）：哪条决策、哪条验收、请求执行什么、提问工具问什么。
export function askLine(a: Ask): string {
  switch (a.kind) {
    case 'permit':
      return t.desk.lane.approve(a.s?.state_detail ?? '')
    case 'question':
      return splitTool(a.s?.state_detail ?? '')[1] || t.session.asking
    default:
      // 带编号的写「类型 + 编号」，编号在泳道上画成带标题的印记，不再重复标题
      return [t.ask.kind[a.kind], a.code || a.title].filter(Boolean).join(' ')
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

// AskWarm：对应会话（停下的）缓存临近过期 / 已冷却时，状态行末尾的小图标，详情在悬停提示里（R-207）。
function AskWarm({ a, now }: { a: Ask; now: number }) {
  const s = a.s
  const w = useWarmFlag(s && s.state !== 'ended' && displayState(s) !== 'running' ? s.key : undefined, now)
  if (!w) return null
  return (
    <span data-ask-warm={w.cold ? 'cold' : 'soon'} title={`${w.label}${t.common.sep}${w.title}`} className={cx('shrink-0', w.cold ? 'text-fg-3' : 'text-reply')}>
      {w.cold ? <Snowflake className="size-3.5" /> : <Hourglass className="size-3.5" />}
    </span>
  )
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
  // 访客留言（R-281）不能左滑，但放入 / 忽略的撤销期里条目上同样盖遮罩
  const gk = props.a.kind === 'guest' ? pendingKeyOf(props.a) : undefined
  if (gk)
    return (
      <div className="relative shrink-0 rounded-[14px]">
        <AskCard {...rest} spotKey={props.a.s?.key ?? ''} />
        <PendingCover k={gk} />
      </div>
    )
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
  showShare = true,
}: {
  a: Ask
  now: number
  onOpen: (key: string) => void
  active?: boolean
  showProject?: boolean
  className?: string
  spotKey?: string
  showShare?: boolean // 访客留言写经哪个分享；分享详情页里不写
}) {
  const s = a.s
  const g = a.gc
  // 访客留言：访客名字，不在分享页里时再写经哪个分享
  const who = g ? [g.guest_name, showShare ? t.share.inbox.via(g.share_name) : ''].filter(Boolean).join(t.common.sep) : s ? sessionTitle(s) : a.projectName || t.thread.noSession
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
          <DraftMark owners={[a.code, s && (a.kind === 'question' || a.kind === 'permit') ? `s:${s.key}` : undefined]} />
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
          {a.kind !== 'waited' && !!a.waitedBy?.length && (
            <span data-waited-by={a.waitedBy.join(',')} className="shrink-0 rounded bg-wait-soft px-1 text-[11px] leading-4 font-medium text-wait">
              {t.ask.waitedBy(a.waitedBy)}
            </span>
          )}
          <span className="min-w-0 flex-1 truncate">{statusLine(a, now)}</span>
          <AskWarm a={a} now={now} />
          {a.kind === 'review' && <ReviewMark r={a.req} />}
          <Advice a={a} />
        </span>
      )}
    </button>
  )
}
