import { useContext, useMemo, useState, type ReactNode } from 'react'
import { ArrowDown, ArrowUp, Check, ChevronDown, ChevronRight, Lock, Milestone, MoreHorizontal, PanelRight, Pencil, Plus, Send, Share2, Trash2, X, Zap } from 'lucide-react'
import { toast } from 'sonner'
import { useNavigate } from 'react-router'
import { Ctx } from '../ui/contextMenu'
import type { Plan, PlanReq, Requirement, Session, Stage } from '../api/client'
import { usePlans, useProjects, useRequirements, useSessions } from '../api/queries'
import { t } from '../i18n'
import { cx, loadPref, relTime, savePref, useNow } from '../lib/util'
import { type FileRef } from '../ui/attach'
import { Button, Card, Empty, Textarea, Tip } from '../ui/base'
import { Menu, Panel, useConfirm, type MenuItem } from '../ui/overlay'
import { Collapse, Item, List } from '../ui/motion'
import { displayState, StatusDot } from '../ui/marks'
import { useActions, useBusy, type PlanBody } from './actions'
import { targetGroup, targetItems, targetLabel } from './assign'
import { sessionTitle } from './SessionCard'
import { PeekContext, useGoBack, useOpenPlan, useOpenReq } from './nav'
import { PeekScope, RefPeek, usePeek } from './peek'
import { BackButton, ReqView } from './reqDrawer'
import { Md } from '../ui/markdown'
import { ProjGlyph, RefChip, ReqGlyph, ReqStatus } from '../ui/refmark'
import { BlockList, ScoreCount } from '../ui/blocks'
import { done, PendingBar, schedule, Seal, usePending, Verdict } from './verdict'
import { DraftField, DraftHint, useDraft } from './draft'

// 计划（R-43）：需求之上的一层，按顺序分阶段推进、逐阶段验收。
// 阶段状态与推进规则在后端；这里负责展示进度、阶段闸门（批准 / 退回）和编辑。

const finishedReq = (r: PlanReq) => r.status === 'review' || r.status === 'done'
const shelvedReq = (r: PlanReq) => r.status === 'shelved'
// 已搁置的需求不计入阶段完成与计划进度（R-175）
export const stageFinished = (st: Stage) => st.requirements.length > 0 && st.requirements.every((r) => finishedReq(r) || shelvedReq(r))
const planReqCount = (x: Plan) => x.stages.reduce((n, st) => n + st.requirements.filter((r) => !shelvedReq(r)).length, 0)
// 批准前要提醒的：每个阶段里已搁置的条数
const shelvedByStage = (x: Plan) => x.stages.map((st) => ({ seq: st.seq, n: st.requirements.filter(shelvedReq).length })).filter((s) => s.n > 0)
const planDoneCount = (x: Plan) => x.stages.reduce((n, st) => n + st.requirements.filter((r) => r.status === 'done').length, 0)
const currentStage = (x: Plan) => x.stages.find((st) => st.seq === x.current)

type Tone = 'approve' | 'draft' | 'review' | 'active' | 'paused' | 'done' | 'shelved'
const toneCls: Record<Tone, string> = {
  approve: 'bg-wait-soft text-wait',
  draft: 'bg-idle-soft text-idle',
  review: 'bg-reply-soft text-reply',
  active: 'bg-run-soft text-run',
  paused: 'bg-danger-soft text-danger',
  done: 'bg-sunken text-fg-2',
  shelved: 'bg-sunken text-fg-3',
}

// planState：计划现在处在哪一步、是不是在等你。
export function planState(x: Plan): { label: string; tone: Tone; needsYou: boolean } {
  if (x.status === 'draft' && x.returned) return { label: t.plan.state.returned, tone: 'draft', needsYou: false }
  if (x.status === 'draft') return x.created_by === 'agent' ? { label: t.plan.state.approve, tone: 'approve', needsYou: true } : { label: t.plan.state.draft, tone: 'draft', needsYou: false }
  if (x.status === 'done') return { label: t.plan.state.done, tone: 'done', needsYou: false }
  if (x.status === 'shelved') return { label: t.plan.state.shelved, tone: 'shelved', needsYou: false }
  if (x.paused) return { label: t.plan.state.paused, tone: 'paused', needsYou: false }
  const st = currentStage(x)
  if (st?.status === 'review') return { label: t.plan.state.review(st.seq), tone: 'review', needsYou: true }
  if (st && stageFinished(st)) return { label: t.plan.state.finished(st.seq), tone: 'review', needsYou: true }
  return { label: t.plan.state.active(x.current), tone: 'active', needsYou: false }
}

export function PlanChip({ x, className }: { x: Plan; className?: string }) {
  const s = planState(x)
  return <span className={cx('inline-flex shrink-0 items-center rounded-full px-2 py-0.5 text-xs font-medium whitespace-nowrap', toneCls[s.tone], className)}>{s.label}</span>
}

// StageRail：一个阶段一段。绿 = 已通过；红 = 等你验收；进行中的按完成比例填充。
export function StageRail({ x, className }: { x: Plan; className?: string }) {
  return (
    <div className={cx('flex gap-1', className)}>
      {x.stages.map((st) => {
        const cur = x.status === 'active' && st.seq === x.current
        const n = st.requirements.filter((r) => !shelvedReq(r)).length
        const pct = n ? (st.requirements.filter(finishedReq).length / n) * 100 : 0
        return (
          <i
            key={st.id}
            className={cx(
              'relative h-[5px] flex-1 overflow-hidden rounded-full transition-colors',
              st.status === 'passed' ? 'bg-run' : cur && x.paused ? 'bg-danger/70' : cur && (st.status === 'review' || stageFinished(st)) ? 'bg-reply' : 'bg-line',
            )}
          >
            {cur && !x.paused && st.status === 'active' && !stageFinished(st) && <span className="absolute inset-y-0 left-0 rounded-full bg-run transition-[width] duration-500" style={{ width: `${Math.max(pct, 6)}%` }} />}
          </i>
        )
      })}
    </div>
  )
}

function useSessionOf() {
  const sessions = useSessions()
  const all = sessions.data?.sessions ?? []
  return (key?: string) => (key ? all.find((s) => s.key === key) : undefined)
}

function progressText(x: Plan) {
  const n = planReqCount(x)
  if (x.status === 'done') return t.plan.progressDone(x.stages.length, n)
  if (x.status !== 'active') return t.plan.progressDraft(x.stages.length, n)
  return t.plan.progress(x.current, x.stages.length, n, planDoneCount(x))
}

// PlanCard：计划视图里的一张卡片。
// ownerLabel：计划负责会话的称呼——有身份写身份（PM / Dev），没有身份才用会话标题；后面跟设备。
const ownerLabel = (s: Session) => [s.role && t.role[s.role] ? t.role[s.role] : sessionTitle(s), s.device_name].filter(Boolean).join(t.common.sep)

// act：工作台「要你处理」里的计划卡，待批准 / 待阶段验收时卡片底部直接给 [附言][退回][批准]（R-161）。
export function PlanCard({ x, act }: { x: Plan; act?: boolean }) {
  const open = useOpenPlan()
  const navigate = useNavigate()
  const gate = act ? gateStage(x) : undefined
  const verdict = act && (needsApproval(x) || !!gate)
  const sessionOf = useSessionOf()
  const owner = sessionOf(x.owner_session)
  const state = planState(x)
  // 右键 / 长按菜单：打开计划、分享（R-281）
  const menu = [
    { label: t.menu.openPlan, icon: <PanelRight />, onSelect: () => open(x.code) },
    { label: t.share.menu, icon: <Share2 />, onSelect: () => navigate(`/shares/new?with=${encodeURIComponent(x.code)}`) },
  ]
  return (
    <Ctx items={menu} className="rounded-2xl">
    <Card
      role="button"
      tabIndex={0}
      data-nav-item=""
      onClick={() => open(x.code)}
      onKeyDown={(e) => e.key === 'Enter' && open(x.code)}
      className={cx('relative cursor-pointer overflow-hidden p-4 transition-all duration-200 hover:-translate-y-px hover:shadow-pop', state.needsYou && 'border-reply/35 ring-1 ring-reply/25', needsApproval(x) && 'border-dashed')}
    >
      <div className="flex items-center gap-2.5">
        <RefChip code={x.code} status={x.status} link={false} />
        <span className="min-w-0 flex-1 truncate text-[15px] font-semibold text-fg">{x.title}</span>
        <PlanChip x={x} />
      </div>
      <StageRail x={x} className="mt-3 mb-2.5" />
      <div className="flex flex-wrap items-center gap-x-2 gap-y-1 text-xs text-fg-3">
        <span>{progressText(x)}</span>
        {x.project_name && (
          <span className="inline-flex items-center gap-1">
            <ProjGlyph />
            {x.project_name}
          </span>
        )}
        {owner && (
          // 「交给」写身份 + 设备，会话的 AI 标题会变、读不通，放到悬停里（R-131）
          <Tip content={sessionTitle(owner)}>
            <span className="truncate">{t.plan.by(ownerLabel(owner))}</span>
          </Tip>
        )}
      </div>
      {verdict && (
        <div className="mt-3" onClick={(e) => e.stopPropagation()} onKeyDown={(e) => e.stopPropagation()}>
          <PlanVerdict x={x} st={gate} />
        </div>
      )}
      {verdict && <Seal k={gate ? stageKey(x, gate) : planKey(x)} />}
    </Card>
    </Ctx>
  )
}

const rank = (x: Plan) => {
  const s = planState(x)
  if (s.needsYou) return 0
  if (x.status === 'active') return 1
  if (x.status === 'draft') return 2
  return 3
}

// PlansView：需求页的“计划”视图。
export function PlansView({ project, showClosed, onNew }: { project?: string; showClosed: boolean; onNew: () => void }) {
  const q = usePlans()
  const list = useMemo(
    () =>
      (q.data ?? [])
        .filter((x) => (!project || x.project_id === project) && (showClosed || (x.status !== 'done' && x.status !== 'shelved')))
        .sort((a, b) => rank(a) - rank(b) || b.updated_at.localeCompare(a.updated_at)),
    [q.data, project, showClosed],
  )
  if (q.isPending) return null
  if (!list.length)
    return (
      <Card>
        <Empty
          icon={<Milestone />}
          title={t.plan.empty}
          action={
            <Button variant="primary" icon={<Plus className="size-4" />} onClick={onNew}>
              {t.plan.new}
            </Button>
          }
        >
          {t.plan.emptySub}
        </Empty>
      </Card>
    )
  return (
    <div className="@container">
      <List className="grid grid-cols-1 gap-x-3 @3xl:grid-cols-2" space={12}>
        {list.map((x) => (
          <Item key={x.id} space={12} layout className="h-full">
            <PlanCard x={x} />
          </Item>
        ))}
      </List>
    </div>
  )
}

// PlanBadge：需求上的“P-1 · 阶段 2”，点开计划。
export function PlanBadge({ r, className }: { r: Requirement; className?: string }) {
  if (!r.plan_code || !r.stage_seq) return null
  const locked = r.stage_status === 'locked'
  return (
    <RefChip
      code={r.plan_code}
      className={className}
      hint={locked ? t.plan.lockedHint(r.stage_seq - 1) : t.plan.stageName(r.stage_seq, r.stage_title ?? '')}
      suffix={
        <>
          {locked && <Lock className="size-3" />}
          {t.ref.stage(r.stage_seq)}
        </>
      }
    />
  )
}

// ---------- 计划详情 ----------

// PlanReqRow：阶段里的一条需求。在抽屉里点开在左边那张纸里看（onPick，R-196），否则就地展开。
function PlanReqRow({ r, locked, picked, onPick }: { r: PlanReq; locked: boolean; picked?: boolean; onPick?: () => void }) {
  const [open, setOpen] = useState(false)
  const inline = open && !onPick
  return (
    <div className={cx('rounded-xl border bg-surface transition-colors', picked ? 'border-accent/60 bg-accent-soft/50 ring-1 ring-accent/30' : inline ? 'border-line-strong shadow-card' : 'border-line hover:border-line-strong')}>
      <button onClick={() => (onPick ? onPick() : setOpen(!open))} className="flex w-full items-center gap-2.5 px-3 py-2 text-left">
        <ChevronRight className={cx('size-3.5 shrink-0 text-fg-3 transition-transform', (inline || picked) && 'rotate-90', picked && 'text-accent')} />
        <span className="w-[68px] shrink-0">
          <RefChip code={r.code} status={r.status} link={false} />
        </span>
        <span className="min-w-0 flex-1 truncate text-sm text-fg">{r.title}</span>
        {locked && r.status !== 'done' ? (
          <span className="shrink-0 rounded-md bg-sunken px-1.5 py-0.5 text-xs text-fg-3">{t.plan.stageState.locked}</span>
        ) : (
          <ReqStatus status={r.status} />
        )}
      </button>
      <Collapse show={inline}>
        <div className="flex max-h-[min(560px,70dvh)] flex-col border-t border-line">{inline && <ReqView refId={r.code} />}</div>
      </Collapse>
    </div>
  )
}

// 计划批准与阶段验收的判断（R-161）：和需求验收同一个 Verdict、同一套撤销期；key 是 plan:P-3 / stage:P-3:2。
const planKey = (x: Plan) => `plan:${x.code}`
const stageKey = (x: Plan, st: Stage) => `stage:${x.code}:${st.seq}`
const nextStageOf = (x: Plan, st: Stage) => x.stages.find((s) => s.seq > st.seq && !(s.requirements.length > 0 && s.requirements.every((r) => r.status === 'done')))?.seq ?? null
// gateStage：等你验收的那一阶段（没有就是 undefined）
export const gateStage = (x: Plan) => {
  const st = x.status === 'active' ? currentStage(x) : undefined
  return st && (stageFinished(st) || st.status === 'review') ? st : undefined
}
export const needsApproval = (x: Plan) => x.status === 'draft' && x.created_by === 'agent' && !x.returned

function usePlanVerdict() {
  const { passStage, reviseStage, startPlan, rejectPlan } = useActions()
  const post = (url: string, comment: string, files: string[]) => ({ method: 'POST' as const, url, body: { comment, files } })
  return {
    approve: (x: Plan, comment: string, files: string[], after?: () => void) =>
      schedule(planKey(x), { act: 'pass', scene: 'plan', subject: x.code, beacon: post(`/api/plans/${x.code}/start`, comment, files), commit: () => done(startPlan(x.code, comment, files), after) }),
    reject: (x: Plan, comment: string, files: string[], after?: () => void) =>
      schedule(planKey(x), { act: 'back', scene: 'plan', subject: x.code, beacon: post(`/api/plans/${x.code}/reject`, comment, files), commit: () => done(rejectPlan(x.code, comment, files), after) }),
    pass: (x: Plan, st: Stage, comment: string, files: string[], after?: () => void) =>
      schedule(stageKey(x, st), { act: 'pass', scene: 'accept', subject: x.code, beacon: post(`/api/plans/${x.code}/stages/${st.seq}/pass`, comment, files), commit: () => done(passStage(x.code, st.seq, comment, files), after) }),
    back: (x: Plan, st: Stage, comment: string, files: string[], after?: () => void) =>
      schedule(stageKey(x, st), { act: 'back', scene: 'accept', subject: x.code, beacon: post(`/api/plans/${x.code}/stages/${st.seq}/revise`, comment, files), commit: () => done(reviseStage(x.code, st.seq, comment, files), after) }),
  }
}

// PlanVerdict：计划卡 / 计划详情里的判断行——待批准的计划，或待验收的当前阶段。
function PlanVerdict({ x, st, stack, extra, className }: { x: Plan; st?: Stage; stack?: boolean; extra?: ReactNode; className?: string }) {
  const v = usePlanVerdict()
  // 草稿（R-202）：plan:P-3 / stage:P-3:2，提交成功才清
  const dr = useDraft<{ text: string; files: FileRef[] }>(st ? stageKey(x, st) : planKey(x), { text: '', files: [] })
  const { text: comment, files } = dr.value
  const ids = files.map((f) => f.id)
  const clear = dr.drop
  const c = comment.trim()
  return (
    <Verdict
      scene={st ? 'accept' : 'plan'}
      stack={stack}
      extra={stack ? <DraftHint h={dr}>{extra}</DraftHint> : extra}
      className={className}
      note={comment}
      onNote={(v) => dr.set({ text: v })}
      files={files}
      onFiles={(f) => dr.set({ files: f })}
      draft={dr}
      pendingKey={st ? stageKey(x, st) : planKey(x)}
      canPass={st ? !x.paused : planReqCount(x) > 0}
      onPass={() => (st ? v.pass(x, st, c, ids, clear) : v.approve(x, c, ids, clear))}
      onBack={() => (st ? v.back(x, st, c, ids, clear) : v.reject(x, c, ids, clear))}
    />
  )
}

// Gate：当前阶段的闸门——阶段小结，然后统一的 [附言][退回][通过]。
// pin：判断按钮不放在这里，固定在抽屉底部（GateVerdict，R-177）。
function Gate({ x, st, pin }: { x: Plan; st: Stage; pin?: boolean }) {
  // 后面需求都已完成的阶段会一并自动通过（D-60），提示里写真正会开始的那一阶段（见 GateVerdict）
  const ready = stageFinished(st)
  if (!ready && st.status !== 'review') return null
  return (
    <div className="relative mt-3 overflow-hidden rounded-2xl border border-reply/30 bg-gradient-to-b from-reply-soft to-reply-soft/40 p-3.5">
      {!pin && <Seal k={stageKey(x, st)} />}
      <StageScores st={st} />
      {st.summary ? (
        <>
          <div className="text-xs font-semibold text-reply">{t.plan.summary}</div>
          <Md text={st.summary} className="mt-1 text-[13px] leading-relaxed text-fg-2" />
          <BlockList blocks={st.summary_blocks} className="mt-3" />
        </>
      ) : (
        <p className="text-[13px] text-fg-3">{t.plan.noSummary}</p>
      )}
      {x.paused && <p className="mt-2 text-xs text-danger">{t.plan.pausedHint}</p>}
      {!pin && <GateVerdict x={x} st={st} className="mt-3" />}
    </div>
  )
}

// GateVerdict：阶段验收的判断行，提示通过后开始哪一阶段、哪些需求一并通过。
function GateVerdict({ x, st, className }: { x: Plan; st: Stage; className?: string }) {
  const next = nextStageOf(x, st)
  const together = st.requirements.filter((r) => r.status === 'review').map((r) => r.code)
  return (
    <PlanVerdict
      key={stageKey(x, st)}
      x={x}
      st={st}
      stack
      className={className}
      extra={
        <p className="text-xs text-fg-3">
          {t.verdict.stageNext(next)}
          {together.length > 0 && <span className="max-sm:block"> · {t.plan.passTogether(together.join('、'))}</span>}
        </p>
      }
    />
  )
}

// StageScores：阶段验收时自动汇总各条需求最近一份证据的清单达标数（R-111），Agent 的小结只写跨需求的部分。
function StageScores({ st }: { st: Stage }) {
  const openReq = useOpenReq()
  if (!st.requirements.length) return null
  return (
    <div className="mb-3 space-y-0.5">
      <div className="text-xs font-semibold text-reply">{t.plan.scores}</div>
      {st.requirements.map((r) => (
        <button key={r.code} type="button" onClick={() => openReq(r.code)} className="flex w-full min-w-0 items-center gap-2 rounded-lg px-1.5 py-1 text-left transition hover:bg-surface/70">
          <RefChip code={r.code} status={r.status} link={false} />
          <span className="min-w-0 flex-1 truncate text-[13px] text-fg">{r.title}</span>
          {r.check_total > 0 ? (
            <span className="flex shrink-0 items-center gap-1.5 text-xs text-fg-2 tabular-nums">
              <ScoreCount pass={r.check_pass} total={r.check_total} />
              {r.check_pass}/{r.check_total}
            </span>
          ) : (
            <span className="shrink-0 text-xs text-fg-3">{t.plan.noChecklist}</span>
          )}
        </button>
      ))}
    </div>
  )
}

// passedWith：阶段是不是随前一个阶段一起自动通过的（你通过阶段 N 时，后面需求早已逐条验收完的阶段一并通过，D-60）。
// 是就返回你验收的那个阶段号，否则 0。同一次操作写下的通过时间相同，按 2 秒内连号判断（R-211：写清是谁做的）。
export function passedWith(x: Plan, st: Stage): number {
  if (!st.passed_at) return 0
  let by = 0
  let prev = 0
  for (const v of [...x.stages].sort((a, b) => a.seq - b.seq)) {
    const at = v.passed_at ? Date.parse(v.passed_at) : 0
    by = at && prev && Math.abs(at - prev) < 2000 ? by : v.seq
    prev = at
    if (v.seq === st.seq) return by === st.seq ? 0 : by
  }
  return 0
}

function StageBlock({ x, st, last, sel, onPick, pin }: { x: Plan; st: Stage; last: boolean; sel?: string | null; onPick?: (code: string) => void; pin?: boolean }) {
  const now = useNow()
  const cur = x.status === 'active' && st.seq === x.current
  const locked = st.status === 'locked'
  const hint =
    st.status === 'passed'
      ? passedWith(x, st)
        ? t.plan.passedWith(passedWith(x, st), relTime(st.passed_at, now))
        : t.plan.passedAt(relTime(st.passed_at, now))
      : locked
        ? x.status === 'active' ? t.plan.lockedHint(st.seq - 1) : ''
        : st.status === 'review'
          ? t.plan.reviewHint
          : cur && stageFinished(st)
            ? t.plan.finishedHint
            : t.plan.stageState[st.status]
  return (
    <div className={cx('relative pl-9', !last && 'pb-5')}>
      {!last && <span aria-hidden className={cx('absolute top-7 bottom-0 left-[11px] w-0.5 rounded-full', st.status === 'passed' ? 'bg-run' : 'bg-line')} />}
      <span
        className={cx(
          'absolute top-0 left-0 flex size-6 items-center justify-center rounded-full border-2 text-xs font-semibold',
          st.status === 'passed' ? 'border-run bg-run text-white' : cur ? (x.paused ? 'border-danger bg-surface text-danger' : 'border-reply bg-surface text-reply shadow-[0_0_0_4px_var(--reply-soft)]') : 'border-line-strong bg-surface text-fg-3',
        )}
      >
        {st.status === 'passed' ? <Check className="size-3.5" /> : st.seq}
      </span>
      <div className={cx(locked && x.status === 'active' && 'opacity-60')}>
        <div className="flex min-h-6 flex-wrap items-baseline gap-x-2">
          <span className="text-[15px] font-semibold text-fg">{t.plan.stageName(st.seq, st.title)}</span>
          {hint && (
            <span className="inline-flex items-center gap-1 text-xs text-fg-3">
              {locked && x.status === 'active' && <Lock className="size-3" />}
              {hint}
            </span>
          )}
        </div>
        <div className="mt-2 space-y-1.5">
          {st.requirements.map((r) => (
            <PlanReqRow key={r.id} r={r} locked={locked && x.status === 'active'} picked={sel === r.code} onPick={onPick && (() => onPick(r.code))} />
          ))}
          {!st.requirements.length && <p className="rounded-xl border border-dashed border-line-strong/70 px-3 py-2 text-xs text-fg-3">{t.plan.emptyStage}</p>}
        </div>
      </div>
      {cur && <Gate x={x} st={st} pin={pin} />}
    </div>
  )
}

// OwnerMenu：负责会话选择（详情与编辑共用）。
function OwnerMenu({ owner, projectId, onPick, sessions, className }: { owner?: Session; projectId?: string; onPick: (key: string) => void; sessions: Session[]; className?: string }) {
  return (
    <Menu
      align="start"
      width="w-80"
      trigger={
        <button className={cx('flex min-w-0 items-center gap-2 rounded-lg px-2 py-1 text-left text-sm text-fg transition hover:bg-hover', className)}>
          {owner ? targetGroup(owner) === 'now' ? <Zap className="size-4 shrink-0 text-accent" /> : <StatusDot state={displayState(owner)} /> : <Send className="size-4 shrink-0 text-fg-3" />}
          <span className="min-w-0 flex-1 truncate">{owner ? targetLabel(owner) : t.plan.noOwner}</span>
          <ChevronDown className="size-3 shrink-0 opacity-60" />
        </button>
      }
      items={[{ label: t.plan.noOwner, checked: !owner, onSelect: () => onPick('') }, { type: 'separator' }, ...targetItems(sessions.filter((s) => s.role !== 'advisor' && s.role !== 'reviewer' && s.role !== 'aide'), projectId, (s) => onPick(s.key), { checked: owner?.key })]}
    />
  )
}

const bodyOf = (x: Plan, patch: Partial<PlanBody> = {}): PlanBody => ({ title: x.title, goal: x.goal, project_id: x.project_id || undefined, owner_session: x.owner_session, ...patch })

// PlanBody：计划正文——标题、目标、负责会话、各阶段；底部固定判断：待批准 → 批准 / 退回，待阶段验收 → 通过 / 退回，
// 自己建的草稿 → 开始。计划抽屉与审批抽屉（R-177）共用；top 是抽屉顶栏。
export function PlanBody({ x, top, sel, onPick: pick }: { x: Plan; top: ReactNode; sel?: string | null; onPick?: (code: string) => void }) {
  // 在抽屉里（审批抽屉、左边那张纸）点需求同样在左边那张纸里看（R-196）；都没有时就地展开
  const peekRef = useContext(PeekContext)
  const onPick = pick ?? peekRef ?? undefined
  const sessions = useSessions()
  const { updatePlan, startPlan } = useActions()
  const all = sessions.data?.sessions ?? []
  const owner = all.find((s) => s.key === x.owner_session)
  const approval = needsApproval(x)
  const gate = gateStage(x)
  // 自己建的计划「开始」不是判断，但同样有 3 秒撤销期（R-201 / D-413），底部换成撤销条；Agent 拆的计划走 PlanVerdict（R-161）
  const start = () =>
    schedule(planKey(x), {
      act: 'move',
      label: t.verdict.startPlan,
      subject: x.code,
      beacon: { method: 'POST', url: `/api/plans/${x.code}/start`, body: { comment: '', files: [] } },
      commit: () => done(startPlan(x.code), () => toast.success(t.plan.started(x.code))),
    })
  const starting = !!usePending(planKey(x))
  const foot = 'shrink-0 border-t border-line bg-surface px-5 pt-3 pb-[max(0.875rem,env(safe-area-inset-bottom))]'
  return (
    <div className="relative flex min-h-0 flex-1 flex-col">
      {approval && <Seal k={planKey(x)} className="bg-transparent" />}
      {gate && <Seal k={stageKey(x, gate)} className="bg-transparent" />}
      {top}
      <div className="min-h-0 flex-1 overflow-y-auto overscroll-contain px-5 pt-2 pb-6">
        <h2 className="font-display text-[22px] leading-snug text-fg">{x.title}</h2>
        {x.goal && <Md text={x.goal} className="mt-1.5 text-sm leading-relaxed text-fg-3" />}
        <div className="mt-3 flex flex-wrap items-center gap-x-2 gap-y-1">
          <OwnerMenu owner={owner} projectId={x.project_id} sessions={all} onPick={(key) => void updatePlan(x.code, bodyOf(x, { owner_session: key }))} className="-ml-2 max-w-[320px] bg-accent-soft/70 text-accent hover:bg-accent-soft" />
          <span className="text-xs text-fg-3">{owner ? t.plan.autoNext : t.plan.ownerHint}</span>
        </div>
        <StageRail x={x} className="mt-4 mb-6" />
        {x.stages.map((st, i) => (
          <StageBlock key={st.id} x={x} st={st} last={i === x.stages.length - 1} sel={sel} onPick={onPick} pin={!!gate} />
        ))}
      </div>
      {approval ? (
        <div className={foot}>
          <PlanVerdict
            key={x.code}
            x={x}
            stack
            extra={
              <div className="space-y-0.5 text-xs text-fg-3">
                {shelvedByStage(x).map((s) => (
                  <p key={s.seq} className="text-wait">
                    {t.plan.shelvedNote(s.seq, s.n)}
                  </p>
                ))}
                <p>{t.verdict.planNext}</p>
              </div>
            }
          />
        </div>
      ) : gate ? (
        <div className={foot}>
          <GateVerdict x={x} st={gate} />
        </div>
      ) : (
        (x.status === 'draft' || x.status === 'shelved') && (
          <div className={cx(foot, 'space-y-2.5')}>
            <p className="text-xs text-fg-3">{t.plan.startHint}</p>
            {starting ? (
              <PendingBar k={planKey(x)} />
            ) : (
              <div className="flex items-center justify-end gap-2">
                <Button variant="primary" disabled={!planReqCount(x)} onClick={start} icon={<Send className="size-4" />}>
                  {t.plan.start}
                </Button>
              </div>
            )}
          </div>
        )
      )}
    </div>
  )
}

export function PlanPanel({ code, onClose }: { code: string | null; onClose: () => void }) {
  const plans = usePlans()
  const navigate = useNavigate()
  const { deletePlan } = useActions()
  const confirm = useConfirm()
  const [editing, setEditing] = useState(false)
  const x = (plans.data ?? []).find((p) => p.code === code)
  // 选中的需求 / 点开的引用在左边再展开一张纸（R-196，和需求、审批、会话抽屉同一张纸）
  const peek = usePeek(code, code)
  const goBack = useGoBack()
  const shown = peek.stack.length === 1 ? peek.stack[0] : null

  const menu: MenuItem[] = x
    ? [
        { label: t.plan.edit, icon: <Pencil />, onSelect: () => setEditing(true) },
        { label: t.share.menu, icon: <Share2 />, onSelect: () => navigate(`/shares/new?with=${encodeURIComponent(x.code)}`) },
        { type: 'separator' },
        {
          label: t.common.delete,
          icon: <Trash2 />,
          danger: true,
          onSelect: async () => {
            if (!(await confirm({ title: t.common.delete, description: t.plan.deleteConfirm(x.code), confirmText: t.common.delete, danger: true }))) return
            if (await deletePlan(x.code)) {
              toast(t.plan.deleted(x.code))
              onClose()
            }
          },
        },
      ]
    : []
  const aside = peek.stack.length && code ? <RefPeek peek={peek} from={`plan:${code}`} mainLabel={code} /> : undefined

  return (
    <>
      <PeekScope peek={peek}>
      <Panel open={!!code && !editing} onClose={onClose} wide bare title={x?.title ?? t.plan.title} aside={aside} onAsideClose={peek.close}>
        {x ? (
          <PlanBody
            x={x}
            sel={shown}
            onPick={peek.open}
            top={
              <div className="flex shrink-0 items-center gap-2 px-5 pt-4">
                {goBack && <BackButton back={goBack} />}
                <RefChip code={x.code} status={x.status} link={false} />
                <PlanChip x={x} />
                <span className="flex-1" />
                <Menu
                  trigger={
                    <Button size="icon-sm" variant="ghost" aria-label={t.common.more}>
                      <MoreHorizontal className="size-4" />
                    </Button>
                  }
                  items={menu}
                />
                <Button size="icon-sm" variant="ghost" aria-label={t.common.close} onClick={onClose}>
                  <X className="size-4" />
                </Button>
              </div>
            }
          />
        ) : (
          <div className="p-6 text-sm text-fg-3">{plans.isPending ? '' : t.plan.empty}</div>
        )}
      </Panel>
      </PeekScope>
      <PlanEditor open={editing} onClose={() => setEditing(false)} plan={x} />
    </>
  )
}

// ---------- 新建 / 编辑 ----------

type DraftReq = { key: string; ref?: string; title: string; status?: string }
type DraftStage = { key: string; id?: string; title: string; status?: string; reqs: DraftReq[] }

// 草稿里存着阶段的键，刷新后新加的不能和它重复
let seqKey = 0
const nk = () => `k${Date.now().toString(36)}${++seqKey}`

function fromPlan(x?: Plan): DraftStage[] {
  if (!x) return [{ key: nk(), title: '', reqs: [] }]
  return x.stages.map((st) => ({ key: nk(), id: st.id, title: st.title, status: st.status, reqs: st.requirements.map((r) => ({ key: nk(), ref: r.code, title: r.title, status: r.status })) }))
}

function StageEditor({
  st,
  i,
  stages,
  candidates,
  onChange,
  onMove,
  onRemove,
  onMoveReq,
}: {
  st: DraftStage
  i: number
  stages: DraftStage[]
  candidates: Requirement[]
  onChange: (st: DraftStage) => void
  onMove: (dir: -1 | 1) => void
  onRemove: () => void
  onMoveReq: (req: DraftReq, to: number) => void
}) {
  const [title, setTitle] = useState('')
  const fixed = st.status === 'passed'
  const add = () => {
    const v = title.trim()
    if (!v) return
    onChange({ ...st, reqs: [...st.reqs, { key: nk(), title: v }] })
    setTitle('')
  }
  return (
    <div className="rounded-2xl border border-line bg-surface-2/60 p-3">
      <div className="flex items-center gap-2">
        <span className="flex size-6 shrink-0 items-center justify-center rounded-full border-2 border-line-strong bg-surface text-xs font-semibold text-fg-3">{fixed ? <Check className="size-3.5 text-run" /> : i + 1}</span>
        <input
          value={st.title}
          placeholder={t.plan.stagePlaceholder}
          onChange={(e) => onChange({ ...st, title: e.target.value })}
          className="min-w-0 flex-1 rounded-lg border border-transparent bg-transparent px-2 py-1 text-[15px] font-semibold text-fg outline-none placeholder:font-normal placeholder:text-fg-3 hover:border-line focus:border-accent focus:bg-surface"
        />
        <Tip content={t.plan.up}>
          <Button size="icon-sm" variant="ghost" disabled={i === 0} onClick={() => onMove(-1)} aria-label={t.plan.up}>
            <ArrowUp className="size-4" />
          </Button>
        </Tip>
        <Tip content={t.plan.down}>
          <Button size="icon-sm" variant="ghost" disabled={i === stages.length - 1} onClick={() => onMove(1)} aria-label={t.plan.down}>
            <ArrowDown className="size-4" />
          </Button>
        </Tip>
        <Tip content={t.plan.removeStage}>
          <Button size="icon-sm" variant="ghost" disabled={stages.length === 1 || fixed} onClick={onRemove} aria-label={t.plan.removeStage}>
            <Trash2 className="size-4" />
          </Button>
        </Tip>
      </div>
      <div className="mt-2 space-y-1.5 pl-8">
        {st.reqs.map((r) => (
          <div key={r.key} className="flex items-center gap-2 rounded-xl border border-line bg-surface px-3 py-1.5">
            <span className="w-[68px] shrink-0">{r.ref ? <RefChip code={r.ref} status={r.status} link={false} /> : <span className="text-xs text-fg-3">{t.plan.newReq}</span>}</span>
            <span className="min-w-0 flex-1 truncate text-sm text-fg">{r.title}</span>
            {stages.length > 1 && (
              <Menu
                trigger={
                  <button className="shrink-0 rounded-md px-1.5 py-0.5 text-xs text-fg-3 transition hover:bg-hover hover:text-fg">
                    {t.plan.moveTo}
                    <ChevronDown className="ml-0.5 inline size-3" />
                  </button>
                }
                items={stages.map((o, j) => ({ label: t.plan.stageName(j + 1, o.title || t.plan.stage(j + 1)), checked: j === i, disabled: j === i, onSelect: () => onMoveReq(r, j) }))}
              />
            )}
            <button aria-label={t.plan.remove} onClick={() => onChange({ ...st, reqs: st.reqs.filter((x) => x.key !== r.key) })} className="shrink-0 rounded-md p-1 text-fg-3 transition hover:bg-hover hover:text-danger">
              <X className="size-3.5" />
            </button>
          </div>
        ))}
        <div className="flex items-center gap-2">
          <input
            value={title}
            placeholder={t.plan.newReqPlaceholder}
            onChange={(e) => setTitle(e.target.value)}
            onKeyDown={(e) => {
              if (e.key === 'Enter' && !e.nativeEvent.isComposing) {
                e.preventDefault()
                add()
              }
            }}
            className="h-9 min-w-0 flex-1 rounded-xl border border-dashed border-line-strong bg-transparent px-3 text-sm text-fg outline-none placeholder:text-fg-3 focus:border-accent focus:bg-surface"
          />
          <Menu
            width="w-80"
            trigger={
              <Button size="sm" icon={<Plus className="size-3.5" />}>
                {t.plan.pickReq}
              </Button>
            }
            items={candidates.length ? candidates.slice(0, 40).map((r) => ({ label: `${r.code}  ${r.title}`, icon: <ReqGlyph status={r.status} />, hint: [t.reqStatus[r.status], r.project_name].filter(Boolean).join(t.common.sep), onSelect: () => onChange({ ...st, reqs: [...st.reqs, { key: nk(), ref: r.code, title: r.title, status: r.status }] }) })) : [{ label: t.plan.noCandidates, disabled: true, onSelect: () => {} }]}
          />
        </div>
      </div>
    </div>
  )
}

type PlanForm = { title: string; goal: string; project: string; owner: string; stages: DraftStage[] }
// planSig：比较时不看阶段 / 需求的临时键
const planSig = (v: PlanForm) => JSON.stringify([v.title.trim(), v.goal.trim(), v.project, v.owner, v.stages.map((st) => [st.title.trim(), st.reqs.map((r) => r.ref ?? r.title)])])

export function PlanEditor({ open, onClose, plan, defaultProject }: { open: boolean; onClose: () => void; plan?: Plan; defaultProject?: string }) {
  const projects = useProjects()
  const sessions = useSessions()
  const reqs = useRequirements()
  const { createPlan, updatePlan } = useActions()
  const openPlan = useOpenPlan()
  const [busy, wrap] = useBusy()
  // 草稿（R-202）：新建是 planedit:new，编辑是 planedit:P-3；关掉、刷新后再打开还在，保存成功才清。
  // 编辑时和计划本身一样就不算草稿；底下「恢复原文」回到计划现在的样子
  const base = useMemo<PlanForm>(
    () => ({
      title: plan?.title ?? '',
      goal: plan?.goal ?? '',
      project: plan ? plan.project_id || '' : defaultProject || loadPref<string>('ks.newreq.project', ''),
      owner: plan?.owner_session ?? '',
      stages: fromPlan(plan),
    }),
    // eslint-disable-next-line react-hooks/exhaustive-deps
    [open, plan?.id],
  )
  const dr = useDraft<PlanForm>(`planedit:${plan?.code ?? 'new'}`, base, (v) => planSig(v) !== planSig(base), open)
  const { title, goal, owner, stages } = dr.value
  const project = dr.value.project || undefined
  const setTitle = (v: string) => dr.set({ title: v })
  const setGoal = (v: string) => dr.set({ goal: v })
  const setProject = (v: string | undefined) => dr.set({ project: v ?? '' })
  const setOwner = (v: string) => dr.set({ owner: v })
  const setStages = (v: DraftStage[]) => dr.set({ stages: v })

  const all = sessions.data?.sessions ?? []
  const used = new Set(stages.flatMap((st) => st.reqs.map((r) => r.ref).filter(Boolean)))
  const candidates = (reqs.data ?? []).filter((r) => !used.has(r.code) && (!r.plan_id || r.plan_id === plan?.id) && r.status !== 'done' && r.status !== 'shelved' && (!project || r.project_id === project))
  const proj = projects.data?.projects.find((p) => p.id === project)
  const ownerS = all.find((s) => s.key === owner)
  const chip = 'inline-flex max-w-[240px] items-center gap-1.5 rounded-lg border border-line bg-surface px-2.5 py-1.5 text-sm text-fg-2 transition hover:border-line-strong hover:text-fg'
  const total = stages.reduce((n, st) => n + st.reqs.length, 0)

  const setAt = (i: number, st: DraftStage) => setStages(stages.map((x, j) => (j === i ? st : x)))
  const move = (i: number, dir: -1 | 1) => {
    const n = [...stages]
    ;[n[i], n[i + dir]] = [n[i + dir], n[i]]
    setStages(n)
  }
  const moveReq = (from: number, r: DraftReq, to: number) => setStages(stages.map((st, j) => (j === from ? { ...st, reqs: st.reqs.filter((x) => x.key !== r.key) } : j === to ? { ...st, reqs: [...st.reqs, r] } : st)))

  const save = (start: boolean) =>
    wrap(async () => {
      if (!title.trim()) return
      if (!total) return void toast(t.plan.needStages)
      const body: PlanBody = {
        title: title.trim(),
        goal: goal.trim(),
        project_id: project,
        owner_session: owner,
        stages: stages.map((st, i) => ({ id: st.id, title: st.title.trim() || t.plan.stage(i + 1), requirements: st.reqs.map((r) => (r.ref ? { ref: r.ref } : { title: r.title })) })),
      }
      if (plan) {
        if (await updatePlan(plan.code, body)) {
          toast.success(t.plan.saved(plan.code))
          dr.drop()
          onClose()
        }
        return
      }
      const x = await createPlan({ ...body, start })
      if (!x) return
      if (project) savePref('ks.newreq.project', project)
      toast.success(start ? t.plan.started(x.code) : t.plan.created(x.code))
      dr.drop()
      onClose()
      openPlan(x.code)
    })

  return (
    <Panel open={open} onClose={onClose} wide bare title={plan ? t.plan.edit : t.plan.new}>
      <div className="flex min-h-0 flex-1 flex-col">
        <div className="flex shrink-0 items-center gap-2 px-5 pt-4 pb-2">
          <h2 className="flex items-center gap-2 font-display text-lg text-fg">
            {plan ? t.plan.edit : t.plan.new}
            {plan && <RefChip code={plan.code} status={plan.status} link={false} />}
          </h2>
          <span className="flex-1" />
          <Button size="icon-sm" variant="ghost" aria-label={t.common.close} onClick={onClose}>
            <X className="size-4" />
          </Button>
        </div>
        <div className="min-h-0 flex-1 space-y-4 overflow-y-auto overscroll-contain px-5 pt-2 pb-5">
          <input autoFocus={!plan} value={title} placeholder={t.plan.titlePlaceholder} onChange={(e) => setTitle(e.target.value)} className="w-full bg-transparent font-display text-[22px] text-fg outline-none placeholder:text-fg-3" />
          {/* 编辑已有计划时「清空」没有意义，只给「恢复原文」 */}
          <DraftField h={dr} revert={!!plan} noClear={!!plan}>
            {(pad) => <Textarea minRows={2} value={goal} placeholder={t.plan.goalPlaceholder} onChange={(e) => setGoal(e.target.value)} className={pad} />}
          </DraftField>
          <div className="flex flex-wrap items-center gap-2">
            <Menu
              align="start"
              trigger={
                <button className={chip}>
                  <ProjGlyph />
                  <span className="truncate">{proj?.name ?? t.reqs.noProject}</span>
                  <ChevronDown className="size-3 opacity-60" />
                </button>
              }
              items={[{ label: t.reqs.noProject, checked: !project, onSelect: () => setProject(undefined) }, ...(projects.data?.projects ?? []).filter((p) => !p.hidden || p.id === project).map((p) => ({ label: p.name, hint: p.remote, checked: p.id === project, onSelect: () => setProject(p.id) }))]}
            />
            <div className={cx(chip, 'gap-1 py-0.5 pr-1 pl-2.5')}>
              <span className="shrink-0 text-[13px] text-fg-3">{t.plan.owner}</span>
              <OwnerMenu owner={ownerS} projectId={project} sessions={all} onPick={setOwner} className="max-w-[200px]" />
            </div>
          </div>
          <div className="space-y-2.5">
            {stages.map((st, i) => (
              <StageEditor
                key={st.key}
                st={st}
                i={i}
                stages={stages}
                candidates={candidates}
                onChange={(n) => setAt(i, n)}
                onMove={(dir) => move(i, dir)}
                onRemove={() => setStages(stages.filter((_, j) => j !== i))}
                onMoveReq={(r, to) => moveReq(i, r, to)}
              />
            ))}
            <Button variant="ghost" icon={<Plus className="size-4" />} onClick={() => setStages([...stages, { key: nk(), title: '', reqs: [] }])}>
              {t.plan.addStage}
            </Button>
          </div>
        </div>
        <div className="flex shrink-0 items-center gap-2 border-t border-line bg-surface px-5 pt-3 pb-[max(0.875rem,env(safe-area-inset-bottom))]">
          <span className="min-w-0 flex-1 truncate text-xs text-fg-3 max-sm:hidden">
            <DraftHint h={dr}>{t.plan.progressDraft(stages.length, total)}</DraftHint>
          </span>
          {plan ? (
            <Button className="ml-auto" variant="primary" loading={busy} disabled={!title.trim()} onClick={() => void save(false)}>
              {t.plan.save}
            </Button>
          ) : (
            <>
              <Button className="ml-auto" disabled={!title.trim() || busy} onClick={() => void save(false)}>
                {t.plan.save}
              </Button>
              <Button variant="primary" loading={busy} disabled={!title.trim() || !total} onClick={() => void save(true)} icon={<Send className="size-4" />}>
                {t.plan.saveAndStart}
              </Button>
            </>
          )}
        </div>
      </div>
    </Panel>
  )
}

// usePlanTodo：在等你的计划（待批准、待阶段验收），工作台与角标用。
export function usePlanTodo() {
  const plans = usePlans()
  return useMemo(() => (plans.data ?? []).filter((x) => planState(x).needsYou), [plans.data])
}

export function PlanAttention({ plans }: { plans: Plan[] }) {
  if (!plans.length) return null
  return (
    <section className="mb-9">
      <div className="mb-3 flex items-center gap-2.5">
        <span className="flex size-7 items-center justify-center rounded-lg bg-surface text-fg-2 shadow-xs [&_svg]:size-4">
          <Milestone />
        </span>
        <h2 className="font-display text-[17px] font-semibold text-fg">{t.plan.attention}</h2>
        <span className="text-xs text-fg-3 tabular">{plans.length}</span>
        <span aria-hidden className="h-0 min-w-6 flex-1 border-t border-dashed border-line-strong/80" />
      </div>
      <div className="@container">
        <List className="grid grid-cols-1 gap-x-3 @3xl:grid-cols-2" space={12}>
          {plans.map((x) => (
            <Item key={x.id} space={12} layout className="h-full">
              <PlanCard x={x} />
            </Item>
          ))}
        </List>
      </div>
    </section>
  )
}

