import { useEffect, useMemo, useState } from 'react'
import { ArrowDown, ArrowUp, Check, ChevronDown, ChevronRight, FolderGit2, Lock, Milestone, MoreHorizontal, Pencil, Plus, Send, Trash2, Undo2, X, Zap } from 'lucide-react'
import { toast } from 'sonner'
import type { Plan, PlanReq, Requirement, Session, Stage } from '../api/client'
import { usePlans, useProjects, useRequirements, useSessions } from '../api/queries'
import { t } from '../i18n'
import { cx, loadPref, relTime, savePref, useMedia, useNow } from '../lib/util'
import { AttachZone, type FileRef } from '../ui/attach'
import { Button, Card, Empty, Textarea, Tip } from '../ui/base'
import { Menu, Panel, useConfirm, type MenuItem } from '../ui/overlay'
import { Collapse, Item, List } from '../ui/motion'
import { displayState, StatusDot } from '../ui/marks'
import { useActions, useBusy, type PlanBody } from './actions'
import { targetGroup, targetItems, targetLabel } from './assign'
import { useOpenPlan } from './nav'
import { ReqView } from './reqDrawer'
import { statusCls } from './requirements'

// 计划（R-43）：需求之上的一层，按顺序分阶段推进、逐阶段验收。
// 阶段状态与推进规则在后端；这里负责展示进度、阶段闸门（通过 / 退回）和编辑。

const finishedReq = (r: PlanReq) => r.status === 'review' || r.status === 'done'
export const stageFinished = (st: Stage) => st.requirements.length > 0 && st.requirements.every(finishedReq)
const planReqCount = (x: Plan) => x.stages.reduce((n, st) => n + st.requirements.length, 0)
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
        const n = st.requirements.length
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
export function PlanCard({ x }: { x: Plan }) {
  const open = useOpenPlan()
  const sessionOf = useSessionOf()
  const owner = sessionOf(x.owner_session)
  const state = planState(x)
  return (
    <Card
      role="button"
      tabIndex={0}
      onClick={() => open(x.code)}
      onKeyDown={(e) => e.key === 'Enter' && open(x.code)}
      className={cx('cursor-pointer p-4 transition-all duration-200 hover:-translate-y-px hover:shadow-pop', state.needsYou && 'border-reply/35 ring-1 ring-reply/25', x.status === 'draft' && x.created_by === 'agent' && 'border-dashed')}
    >
      <div className="flex items-center gap-2.5">
        <span className="font-mono text-[11px] font-semibold text-fg-3">{x.code}</span>
        <span className="min-w-0 flex-1 truncate text-[15px] font-semibold text-fg">{x.title}</span>
        <PlanChip x={x} />
      </div>
      <StageRail x={x} className="mt-3 mb-2.5" />
      <div className="flex flex-wrap items-center gap-x-2 gap-y-1 text-xs text-fg-3">
        <span>{progressText(x)}</span>
        {x.project_name && (
          <span className="inline-flex items-center gap-1">
            <FolderGit2 className="size-3.5" />
            {x.project_name}
          </span>
        )}
        {owner && <span className="truncate">{t.plan.by(targetLabel(owner))}</span>}
      </div>
    </Card>
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
  const open = useOpenPlan()
  if (!r.plan_code || !r.stage_seq) return null
  const locked = r.stage_status === 'locked'
  return (
    <Tip content={locked ? t.plan.lockedHint(r.stage_seq - 1) : t.plan.badgeTip(r.plan_code, t.plan.stageName(r.stage_seq, r.stage_title ?? ''))}>
      <button
        onClick={(e) => {
          e.stopPropagation()
          open(r.plan_code!)
        }}
        className={cx('inline-flex shrink-0 items-center gap-1 rounded-md bg-sunken px-1.5 py-0.5 text-xs font-medium transition hover:text-accent', locked ? 'text-fg-3' : 'text-fg-2', className)}
      >
        {locked ? <Lock className="size-3" /> : <Milestone className="size-3.5" />}
        {t.plan.badge(r.plan_code, r.stage_seq)}
      </button>
    </Tip>
  )
}

// ---------- 计划详情 ----------

// PlanReqRow：阶段里的一条需求。宽屏时点开在左侧并排的抽屉里看（onPick），窄屏就地展开。
function PlanReqRow({ r, locked, picked, onPick }: { r: PlanReq; locked: boolean; picked?: boolean; onPick?: () => void }) {
  const [open, setOpen] = useState(false)
  const inline = open && !onPick
  return (
    <div className={cx('rounded-xl border bg-surface transition-colors', picked ? 'border-accent/60 bg-accent-soft/50 ring-1 ring-accent/30' : inline ? 'border-line-strong shadow-card' : 'border-line hover:border-line-strong')}>
      <button onClick={() => (onPick ? onPick() : setOpen(!open))} className="flex w-full items-center gap-2.5 px-3 py-2 text-left">
        <ChevronRight className={cx('size-3.5 shrink-0 text-fg-3 transition-transform', (inline || picked) && 'rotate-90', picked && 'text-accent')} />
        <span className="w-10 shrink-0 font-mono text-[11px] font-semibold text-fg-3">{r.code}</span>
        <span className="min-w-0 flex-1 truncate text-sm text-fg">{r.title}</span>
        {locked && r.status !== 'done' ? (
          <span className="shrink-0 rounded-md bg-sunken px-1.5 py-0.5 text-xs text-fg-3">{t.plan.stageState.locked}</span>
        ) : (
          <span className={cx('inline-flex shrink-0 items-center gap-1 rounded-md px-1.5 py-0.5 text-xs font-medium', statusCls[r.status]?.pill)}>
            {r.status === 'done' && <Check className="size-3" />}
            {t.reqStatus[r.status]}
          </span>
        )}
      </button>
      <Collapse show={inline}>
        <div className="flex max-h-[min(560px,70dvh)] flex-col border-t border-line">{inline && <ReqView refId={r.code} />}</div>
      </Collapse>
    </div>
  )
}

// Gate：当前阶段的闸门——阶段小结、附言、通过 / 退回。
function Gate({ x, st }: { x: Plan; st: Stage }) {
  const { passStage, reviseStage } = useActions()
  const [busy, wrap] = useBusy()
  const [comment, setComment] = useState('')
  const [files, setFiles] = useState<FileRef[]>([])
  const ids = () => files.map((f) => f.id)
  const next = x.stages.find((s) => s.seq === st.seq + 1)?.seq ?? null
  const together = st.requirements.filter((r) => r.status === 'review').map((r) => r.code)
  const ready = stageFinished(st)
  if (!ready && st.status !== 'review') return null
  const pass = () =>
    wrap(async () => {
      if (await passStage(x.code, st.seq, comment.trim(), ids())) {
        toast.success(t.plan.passed(x.code, st.seq))
        setComment('')
        setFiles([])
      }
    })
  const revise = () =>
    wrap(async () => {
      if (!comment.trim() && !files.length) return void toast(t.plan.reviseNeedsComment)
      if (await reviseStage(x.code, st.seq, comment.trim(), ids())) {
        toast(t.plan.revised(x.code, st.seq))
        setComment('')
        setFiles([])
      }
    })
  return (
    <div className="mt-3 rounded-2xl border border-reply/30 bg-gradient-to-b from-reply-soft to-reply-soft/40 p-3.5">
      {st.summary ? (
        <>
          <div className="text-xs font-semibold text-reply">{t.plan.summary}</div>
          <p className="mt-1 text-[13px] leading-relaxed whitespace-pre-wrap text-fg-2">{st.summary}</p>
        </>
      ) : (
        <p className="text-[13px] text-fg-3">{t.plan.noSummary}</p>
      )}
      <AttachZone value={files} onChange={setFiles} className="mt-3">
        <Textarea minRows={1} value={comment} onChange={(e) => setComment(e.target.value)} placeholder={t.plan.comment} className="bg-surface" />
      </AttachZone>
      {x.paused && <p className="mt-2 text-xs text-danger">{t.plan.pausedHint}</p>}
      <div className="mt-3 flex flex-wrap items-center gap-2">
        <Button variant="primary" loading={busy} disabled={x.paused} onClick={() => void pass()} icon={<Check className="size-4" />}>
          <span className="max-sm:hidden">{t.plan.pass(next)}</span>
          <span className="sm:hidden">{t.plan.passShort(next)}</span>
        </Button>
        <Button disabled={busy} onClick={() => void revise()} icon={<Undo2 className="size-4" />}>
          {t.plan.revise}
        </Button>
      </div>
      {together.length > 0 && <p className="mt-2 text-xs text-fg-3">{t.plan.passTogether(together.join('、'))}</p>}
    </div>
  )
}

function StageBlock({ x, st, last, sel, onPick }: { x: Plan; st: Stage; last: boolean; sel?: string | null; onPick?: (code: string) => void }) {
  const now = useNow()
  const cur = x.status === 'active' && st.seq === x.current
  const locked = st.status === 'locked'
  const hint =
    st.status === 'passed'
      ? t.plan.passedAt(relTime(st.passed_at, now))
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
      {cur && <Gate x={x} st={st} />}
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
      items={[{ label: t.plan.noOwner, checked: !owner, onSelect: () => onPick('') }, { type: 'separator' }, ...targetItems(sessions, projectId, (s) => onPick(s.key), { checked: owner?.key })]}
    />
  )
}

const bodyOf = (x: Plan, patch: Partial<PlanBody> = {}): PlanBody => ({ title: x.title, goal: x.goal, project_id: x.project_id || undefined, owner_session: x.owner_session, ...patch })

export function PlanPanel({ code, onClose }: { code: string | null; onClose: () => void }) {
  const plans = usePlans()
  const sessions = useSessions()
  const { updatePlan, deletePlan, startPlan, rejectPlan } = useActions()
  const confirm = useConfirm()
  const [busy, wrap] = useBusy()
  const [editing, setEditing] = useState(false)
  const [comment, setComment] = useState('')
  const [files, setFiles] = useState<FileRef[]>([])
  const [sel, setSel] = useState<string | null>(null) // 宽屏：左侧并排打开的需求
  const roomy = useMedia('(min-width: 1140px)')
  const x = (plans.data ?? []).find((p) => p.code === code)
  const all = sessions.data?.sessions ?? []
  const owner = all.find((s) => s.key === x?.owner_session)
  useEffect(() => {
    setComment('')
    setFiles([])
    setSel(null)
  }, [code])
  const shown = roomy && sel && x?.stages.some((st) => st.requirements.some((r) => r.code === sel)) ? sel : null

  const menu: MenuItem[] = x
    ? [
        { label: t.plan.edit, icon: <Pencil />, onSelect: () => setEditing(true) },
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
  const approval = x?.status === 'draft' && x.created_by === 'agent'
  const done = () => {
    setComment('')
    setFiles([])
  }
  const ids = files.map((f) => f.id)
  const start = () => wrap(async () => x && (await startPlan(x.code, comment.trim(), ids)) && (toast.success(t.plan.started(x.code)), done()))
  const reject = () => wrap(async () => x && (await rejectPlan(x.code, comment.trim(), ids)) && (toast(t.plan.rejected(x.code)), done()))
  const aside = shown ? (
    <ReqView
      key={shown}
      refId={shown}
      top={
        <div className="flex shrink-0 items-center gap-2 px-4 pt-3 sm:px-5">
          <span className="font-mono text-[11px] font-semibold text-fg-3">{shown}</span>
          <span className="text-xs text-fg-3">{t.plan.inPlan(x!.code)}</span>
          <span className="flex-1" />
          <Button size="icon-sm" variant="ghost" aria-label={t.common.close} onClick={() => setSel(null)}>
            <X className="size-4" />
          </Button>
        </div>
      }
    />
  ) : undefined

  return (
    <>
      <Panel open={!!code && !editing} onClose={onClose} wide bare title={x?.title ?? t.plan.title} aside={aside}>
        {x ? (
          <div className="flex min-h-0 flex-1 flex-col">
            <div className="flex shrink-0 items-center gap-2 px-5 pt-4">
              <span className="font-mono text-[11px] font-semibold text-fg-3">{x.code}</span>
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
            <div className="min-h-0 flex-1 overflow-y-auto overscroll-contain px-5 pt-2 pb-6">
              <h2 className="font-display text-[22px] leading-snug text-fg">{x.title}</h2>
              {x.goal && <p className="mt-1.5 text-sm leading-relaxed text-fg-3">{x.goal}</p>}
              <div className="mt-3 flex flex-wrap items-center gap-x-2 gap-y-1">
                <OwnerMenu owner={owner} projectId={x.project_id} sessions={all} onPick={(key) => void updatePlan(x.code, bodyOf(x, { owner_session: key }))} className="-ml-2 max-w-[320px] bg-accent-soft/70 text-accent hover:bg-accent-soft" />
                <span className="text-xs text-fg-3">{owner ? t.plan.autoNext : t.plan.ownerHint}</span>
              </div>
              <StageRail x={x} className="mt-4 mb-6" />
              {x.stages.map((st, i) => (
                <StageBlock key={st.id} x={x} st={st} last={i === x.stages.length - 1} sel={shown} onPick={roomy ? (c) => setSel(sel === c ? null : c) : undefined} />
              ))}
            </div>
            {(x.status === 'draft' || x.status === 'shelved') && (
              <div className="shrink-0 space-y-2.5 border-t border-line bg-surface px-5 pt-3 pb-[max(0.875rem,env(safe-area-inset-bottom))]">
                <p className="text-xs text-fg-3">{approval ? t.plan.approveHint : t.plan.startHint}</p>
                {approval && (
                  <AttachZone value={files} onChange={setFiles}>
                    <Textarea minRows={1} value={comment} onChange={(e) => setComment(e.target.value)} placeholder={t.plan.approveNote} />
                  </AttachZone>
                )}
                <div className="flex items-center justify-end gap-2">
                  {approval && (
                    <Button disabled={busy} onClick={() => void reject()}>
                      {t.plan.reject}
                    </Button>
                  )}
                  <Button variant="primary" loading={busy} disabled={!planReqCount(x)} onClick={() => void start()} icon={<Send className="size-4" />}>
                    {approval ? t.plan.approve : t.plan.start}
                  </Button>
                </div>
              </div>
            )}
          </div>
        ) : (
          <div className="p-6 text-sm text-fg-3">{plans.isPending ? '' : t.plan.empty}</div>
        )}
      </Panel>
      <PlanEditor open={editing} onClose={() => setEditing(false)} plan={x} />
    </>
  )
}

// ---------- 新建 / 编辑 ----------

type DraftReq = { key: string; ref?: string; title: string; status?: string }
type DraftStage = { key: string; id?: string; title: string; status?: string; reqs: DraftReq[] }

let seqKey = 0
const nk = () => `k${++seqKey}`

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
            <span className="w-10 shrink-0 font-mono text-[11px] font-semibold text-fg-3">{r.ref ?? '新'}</span>
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
            items={candidates.length ? candidates.slice(0, 40).map((r) => ({ label: `${r.code}  ${r.title}`, hint: [t.reqStatus[r.status], r.project_name].filter(Boolean).join(t.common.sep), onSelect: () => onChange({ ...st, reqs: [...st.reqs, { key: nk(), ref: r.code, title: r.title, status: r.status }] }) })) : [{ label: t.plan.noCandidates, disabled: true, onSelect: () => {} }]}
          />
        </div>
      </div>
    </div>
  )
}

export function PlanEditor({ open, onClose, plan, defaultProject }: { open: boolean; onClose: () => void; plan?: Plan; defaultProject?: string }) {
  const projects = useProjects()
  const sessions = useSessions()
  const reqs = useRequirements()
  const { createPlan, updatePlan } = useActions()
  const openPlan = useOpenPlan()
  const [busy, wrap] = useBusy()
  const [title, setTitle] = useState('')
  const [goal, setGoal] = useState('')
  const [project, setProject] = useState<string | undefined>()
  const [owner, setOwner] = useState('')
  const [stages, setStages] = useState<DraftStage[]>(fromPlan())
  useEffect(() => {
    if (!open) return
    setTitle(plan?.title ?? '')
    setGoal(plan?.goal ?? '')
    setProject(plan ? plan.project_id || undefined : defaultProject || loadPref<string>('ks.newreq.project', '') || undefined)
    setOwner(plan?.owner_session ?? '')
    setStages(fromPlan(plan))
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [open, plan?.id])

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
          onClose()
        }
        return
      }
      const x = await createPlan({ ...body, start })
      if (!x) return
      if (project) savePref('ks.newreq.project', project)
      toast.success(start ? t.plan.started(x.code) : t.plan.created(x.code))
      onClose()
      openPlan(x.code)
    })

  return (
    <Panel open={open} onClose={onClose} wide bare title={plan ? t.plan.edit : t.plan.new}>
      <div className="flex min-h-0 flex-1 flex-col">
        <div className="flex shrink-0 items-center gap-2 px-5 pt-4 pb-2">
          <h2 className="font-display text-lg text-fg">{plan ? `${t.plan.edit} · ${plan.code}` : t.plan.new}</h2>
          <span className="flex-1" />
          <Button size="icon-sm" variant="ghost" aria-label={t.common.close} onClick={onClose}>
            <X className="size-4" />
          </Button>
        </div>
        <div className="min-h-0 flex-1 space-y-4 overflow-y-auto overscroll-contain px-5 pt-2 pb-5">
          <input autoFocus={!plan} value={title} placeholder={t.plan.titlePlaceholder} onChange={(e) => setTitle(e.target.value)} className="w-full bg-transparent font-display text-[22px] text-fg outline-none placeholder:text-fg-3" />
          <Textarea minRows={2} value={goal} placeholder={t.plan.goalPlaceholder} onChange={(e) => setGoal(e.target.value)} />
          <div className="flex flex-wrap items-center gap-2">
            <Menu
              align="start"
              trigger={
                <button className={chip}>
                  <FolderGit2 className="size-3.5 opacity-70" />
                  <span className="truncate">{proj?.name ?? t.reqs.noProject}</span>
                  <ChevronDown className="size-3 opacity-60" />
                </button>
              }
              items={[{ label: t.reqs.noProject, checked: !project, onSelect: () => setProject(undefined) }, ...(projects.data?.projects ?? []).map((p) => ({ label: p.name, hint: p.remote, checked: p.id === project, onSelect: () => setProject(p.id) }))]}
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
          <span className="min-w-0 flex-1 truncate text-xs text-fg-3 max-sm:hidden">{t.plan.progressDraft(stages.length, total)}</span>
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

