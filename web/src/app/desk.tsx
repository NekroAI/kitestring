import { useEffect, useMemo, useState, useSyncExternalStore } from 'react'
import { ChevronDown, Undo2 } from 'lucide-react'
import { toast } from 'sonner'
import type { Decision, Plan, Requirement, Session } from '../api/client'
import { usePlans, useProjects, useRequirement, useRequirements } from '../api/queries'
import { t } from '../i18n'
import { cx, relTime } from '../lib/util'
import { Button, Card } from '../ui/base'
import { Score } from '../ui/blocks'
import { displayState, presence, SessAvatar } from '../ui/marks'
import { Collapse } from '../ui/motion'
import { Linkify, RefChip } from '../ui/refmark'
import { useActions } from './actions'
import { DecisionComposer, OrphanCard } from './DecisionCard'
import type { useWorkbench } from './inbox'
import { useOpenPlan, useOpenReq, useOpenSession } from './nav'
import { useOwnership } from './ownership'
import { planState } from './plans'
import { sessionTitle } from './SessionCard'

// 工作台三层（R-113，原型 ②）：PM 简报 → 待你处理（按需求聚合，能就地操作）→ 进行中（计划的分段进度、独立需求）。
// 会话视图整体放在这三层下面的折叠区里（D-142 选 B）。

type WB = ReturnType<typeof useWorkbench>

const briefTTL = 12 * 3600e3 // 超过 12 小时没更新的简报不再显示

// ---------- PM 简报 ----------

export function Briefs({ sessions, now }: { sessions: Session[]; now: number }) {
  const projects = useProjects()
  const list = (projects.data?.projects ?? []).filter((p) => p.brief && p.brief_at && now - Date.parse(p.brief_at) < briefTTL && !p.hidden)
  if (!list.length) return null
  return (
    <div className="mb-7 space-y-3">
      {list.map((p) => {
        const pm = sessions.find((x) => x.key === p.brief_by)
        return (
          <section key={p.id} className="grid gap-1.5 rounded-r-xl border-l-[3px] border-seal bg-surface px-4 py-3 shadow-xs sm:px-5">
            <div className="flex min-w-0 items-center gap-2 text-xs text-fg-3">
              <SessAvatar role="pm" presence={pm ? presence(pm) || undefined : undefined} />
              <span className="truncate">{t.desk.briefBy(list.length > 1 ? p.name : '', relTime(p.brief_at, now))}</span>
            </div>
            <p className="text-[15px] leading-relaxed whitespace-pre-wrap text-fg">
              <Linkify text={p.brief} />
            </p>
          </section>
        )
      })}
    </div>
  )
}

// ---------- 就地通过（R-114）：点了先进入 5 秒撤销期，到时才真正通过；离开工作台也照常生效 ----------

const passDelay = 5000
const passing = new Map<string, { until: number; timer: number }>()
const passSubs = new Set<() => void>()
let passVer = 0
const emitPass = () => {
  passVer++
  passSubs.forEach((f) => f())
}
function usePassing() {
  useSyncExternalStore(
    (f) => {
      passSubs.add(f)
      return () => passSubs.delete(f)
    },
    () => passVer,
  )
  return passing
}
function schedulePass(code: string, commit: () => Promise<unknown>) {
  if (passing.has(code)) return
  const timer = window.setTimeout(() => {
    passing.delete(code)
    emitPass()
    void commit()
  }, passDelay)
  passing.set(code, { until: Date.now() + passDelay, timer })
  emitPass()
}
function undoPass(code: string) {
  const p = passing.get(code)
  if (!p) return
  window.clearTimeout(p.timer)
  passing.delete(code)
  emitPass()
}

function Countdown({ until }: { until: number }) {
  const [, tick] = useState(0)
  useEffect(() => {
    const id = window.setInterval(() => tick((n) => n + 1), 250)
    return () => window.clearInterval(id)
  }, [])
  return <>{t.desk.passing(Math.max(1, Math.ceil((until - Date.now()) / 1000)))}</>
}

// ---------- 待你处理 ----------

type Todo =
  | { kind: 'req'; key: string; code: string; review?: Requirement; decisions: Decision[]; at: string }
  | { kind: 'dec'; key: string; d: Decision; at: string }
  | { kind: 'plan'; key: string; x: Plan; at: string }
  | { kind: 'orphan'; key: string; d: Decision; at: string }
  | { kind: 'session'; key: string; s: Session; at: string }

const sessionKind = (s: Session) => {
  const ds = displayState(s)
  return ds === 'waiting_approval' ? 'approve' : ds === 'waiting_answer' ? 'answer' : 'reply'
}

// useTodo：把工作台的线程摊平成“一件事一行”：同一条需求的验收和决策合成一行。
export function useTodo(wb: WB): Todo[] {
  const reqs = useRequirements()
  return useMemo(() => {
    const out: Todo[] = []
    const byReq = new Map<string, Extract<Todo, { kind: 'req' }>>()
    const rowOf = (code: string, at: string) => {
      let row = byReq.get(code)
      if (!row) {
        row = { kind: 'req', key: `r:${code}`, code, decisions: [], at }
        byReq.set(code, row)
        out.push(row)
      }
      if (at < row.at) row.at = at
      return row
    }
    for (const th of wb.threads) {
      for (const r of th.reviews) rowOf(r.code, r.updated_at).review = r
      for (const d of th.decisions) {
        if (d.requirement_code) rowOf(d.requirement_code, d.created_at).decisions.push(d)
        else out.push({ kind: 'dec', key: `d:${d.code}`, d, at: d.created_at })
      }
      for (const d of th.orphans) out.push({ kind: 'orphan', key: `o:${d.code}`, d, at: d.answered_at ?? d.created_at })
      const s = th.s
      if (s && s.state !== 'ended' && ['waiting_approval', 'waiting_answer', 'waiting_reply'].includes(displayState(s))) out.push({ kind: 'session', key: `s:${s.key}`, s, at: s.state_since || s.last_active_at })
    }
    for (const x of wb.plans) out.push({ kind: 'plan', key: `p:${x.code}`, x, at: x.updated_at })
    // 拍板、授权、提问最急，其次验收与计划，最后是等你回复
    const rank = (x: Todo) => (x.kind === 'session' ? { approve: 0, answer: 1, reply: 4 }[sessionKind(x.s)] : x.kind === 'dec' || (x.kind === 'req' && x.decisions.length) ? 1 : x.kind === 'orphan' ? 3 : 2)
    return out.sort((a, b) => rank(a) - rank(b) || a.at.localeCompare(b.at))
    // reqs.data：需求标题 / 状态更新时重算
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [wb.threads, wb.plans, reqs.data])
}

function Kind({ children }: { children: React.ReactNode }) {
  return <span className="w-12 shrink-0 pt-0.5 text-xs font-semibold text-seal max-sm:w-auto">{children}</span>
}

// ReviewGist：最近一份证据的达标小方块与图片数；没有结构化清单时只说有证据。
function ReviewGist({ r }: { r: Requirement }) {
  // 列表接口不带证据，按编号取详情（与验收卡相同）
  const detail = useRequirement(r.code)
  if (detail.isPending) return null
  const e = (detail.data?.evidence ?? []).filter((x) => !x.voided).at(-1)
  if (!e) return <span>{t.desk.noEvidence}</span>
  const c = e.payload?.blocks?.find((b) => b.type === 'checklist')
  const pics = (e.files ?? []).filter((f) => f.mime?.startsWith('image/')).length
  return (
    <span className="inline-flex flex-wrap items-center gap-x-2 gap-y-0.5">
      {c?.items?.length ? <Score items={c.items} /> : <span>{t.desk.hasEvidence}</span>}
      {pics > 0 && <span>{t.desk.pics(pics)}</span>}
    </span>
  )
}

function decisionSub(ds: Decision[]) {
  const pts = ds.reduce((n, d) => n + (d.points?.length ?? 0), 0)
  if (pts) return t.desk.points(pts)
  if (ds.length === 1 && ds[0].approval) return t.desk.approval
  return t.desk.decisions(ds.length)
}

function Row({ kind, title, sub, actions, children, open }: { kind: React.ReactNode; title: React.ReactNode; sub?: React.ReactNode; actions?: React.ReactNode; children?: React.ReactNode; open?: boolean }) {
  return (
    <div className="border-t border-line first:border-t-0">
      <div className="flex items-start gap-3 px-4 py-3 max-sm:flex-col max-sm:gap-1.5">
        <Kind>{kind}</Kind>
        <div className="grid min-w-0 flex-1 gap-1">
          <div className="flex min-w-0 items-center gap-2">{title}</div>
          {sub && <div className="text-[13px] text-fg-3">{sub}</div>}
        </div>
        {actions && <div className="flex shrink-0 items-center gap-2 max-sm:self-end">{actions}</div>}
      </div>
      {children && (
        <Collapse show={!!open}>
          <div className="@container space-y-3 border-t border-dashed border-line-strong bg-surface-2/60 p-3 sm:p-4">{children}</div>
        </Collapse>
      )}
    </div>
  )
}

function ReqTodo({ x }: { x: Extract<Todo, { kind: 'req' }> }) {
  const openReq = useOpenReq()
  const { review } = useActions()
  const pass = usePassing().get(x.code)
  const [open, setOpen] = useState(false)
  const r = x.review
  const kinds = [r && t.desk.kind.review, x.decisions.length && t.desk.kind.decide].filter(Boolean).join(' · ')
  const title = r?.title ?? x.decisions[0]?.requirement_title ?? ''
  const commit = (code: string) => async () => {
    if (await review([{ ref: code, verdict: 'accept' }])) toast.success(t.review.passed(code))
  }
  if (pass)
    return (
      <div className="flex items-center gap-3 border-t border-line bg-run-soft/50 px-4 py-3 first:border-t-0">
        <RefChip code={x.code} status="done" link={false} />
        <span className="min-w-0 flex-1 truncate text-sm text-run">
          <Countdown until={pass.until} />
        </span>
        <Button size="sm" variant="ghost" icon={<Undo2 className="size-3.5" />} onClick={() => undoPass(x.code)}>
          {t.desk.undo}
        </Button>
      </div>
    )
  return (
    <Row
      kind={kinds}
      title={
        <>
          <RefChip code={x.code} />
          <span className="min-w-0 truncate text-sm font-medium text-fg">{title}</span>
        </>
      }
      sub={
        <span className="inline-flex flex-wrap items-center gap-x-3 gap-y-0.5">
          {r && <ReviewGist r={r} />}
          {x.decisions.length > 0 && <span>{decisionSub(x.decisions)}</span>}
        </span>
      }
      actions={
        <>
          {x.decisions.length > 0 && (
            <Button size="sm" variant={r ? 'ghost' : 'secondary'} onClick={() => setOpen(!open)} icon={<ChevronDown className={cx('size-3.5 transition-transform', open && 'rotate-180')} />}>
              {t.desk.decide}
            </Button>
          )}
          {r && (
            <Button size="sm" onClick={() => openReq(x.code)}>
              {t.desk.review}
            </Button>
          )}
          {r && !x.decisions.length && (
            <Button size="sm" variant="primary" onClick={() => schedulePass(x.code, commit(x.code))}>
              {t.desk.pass}
            </Button>
          )}
        </>
      }
      open={open}
    >
      {x.decisions.map((d) => (
        <DecisionComposer key={d.id} d={d} />
      ))}
    </Row>
  )
}

function DecTodo({ d }: { d: Decision }) {
  const [open, setOpen] = useState(false)
  return (
    <Row
      kind={t.desk.kind.decide}
      title={
        <>
          <RefChip code={d.code} status={d.status} />
          <span className="min-w-0 truncate text-sm font-medium text-fg">{d.short_title || d.question}</span>
        </>
      }
      sub={[decisionSub([d]), d.project_name].filter(Boolean).join(t.common.sep)}
      actions={
        <Button size="sm" onClick={() => setOpen(!open)} icon={<ChevronDown className={cx('size-3.5 transition-transform', open && 'rotate-180')} />}>
          {t.desk.decide}
        </Button>
      }
      open={open}
    >
      <DecisionComposer d={d} />
    </Row>
  )
}

function OrphanTodo({ d, sessions }: { d: Decision; sessions: Session[] }) {
  const [open, setOpen] = useState(false)
  return (
    <Row
      kind={t.desk.kind.orphan}
      title={
        <>
          <RefChip code={d.code} status={d.status} />
          <span className="min-w-0 truncate text-sm font-medium text-fg">{d.short_title || d.question}</span>
        </>
      }
      sub={t.desk.orphanSub}
      actions={
        <Button size="sm" onClick={() => setOpen(!open)} icon={<ChevronDown className={cx('size-3.5 transition-transform', open && 'rotate-180')} />}>
          {t.desk.handle}
        </Button>
      }
      open={open}
    >
      <OrphanCard d={d} sessions={sessions} />
    </Row>
  )
}

function PlanTodo({ x }: { x: Plan }) {
  const openPlan = useOpenPlan()
  const st = planState(x)
  return (
    <Row
      kind={t.desk.kind.plan}
      title={
        <>
          <RefChip code={x.code} status={x.status} />
          <span className="min-w-0 truncate text-sm font-medium text-fg">{x.title}</span>
        </>
      }
      sub={st.label}
      actions={
        <Button size="sm" onClick={() => openPlan(x.code)}>
          {t.desk.view}
        </Button>
      }
    />
  )
}

function SessionTodo({ s, now }: { s: Session; now: number }) {
  const openSession = useOpenSession()
  const k = sessionKind(s)
  const text = s.digest_ask || (k === 'reply' ? s.digest_done || s.summary : s.state_detail) || s.summary || s.last_prompt
  return (
    <Row
      kind={t.desk.kind[k]}
      title={
        <>
          <SessAvatar role={s.role} />
          <span className="min-w-0 truncate text-sm font-medium text-fg">{sessionTitle(s)}</span>
          {s.project_name && <span className="shrink-0 text-xs text-fg-3 max-sm:hidden">{s.project_name}</span>}
        </>
      }
      sub={
        <span className="line-clamp-1">
          {t.desk.waiting(relTime(s.state_since || s.last_active_at, now))}
          {text && (
            <>
              {t.common.sep}
              <Linkify text={text} />
            </>
          )}
        </span>
      }
      actions={
        <Button size="sm" onClick={() => openSession(s.key)}>
          {k === 'reply' ? t.desk.reply : t.desk.handle}
        </Button>
      }
    />
  )
}

export function TodoList({ items, sessions, now }: { items: Todo[]; sessions: Session[]; now: number }) {
  return (
    <section className="mb-7">
      <SectionTitle title={t.desk.todo(items.length)} />
      {items.length ? (
        <Card className="overflow-hidden p-0">
          {items.map((x) =>
            x.kind === 'req' ? (
              <ReqTodo key={x.key} x={x} />
            ) : x.kind === 'dec' ? (
              <DecTodo key={x.key} d={x.d} />
            ) : x.kind === 'orphan' ? (
              <OrphanTodo key={x.key} d={x.d} sessions={sessions} />
            ) : x.kind === 'plan' ? (
              <PlanTodo key={x.key} x={x.x} />
            ) : (
              <SessionTodo key={x.key} s={x.s} now={now} />
            ),
          )}
        </Card>
      ) : (
        <p className="rounded-xl border border-dashed border-line-strong px-4 py-4 text-sm text-fg-3">{t.desk.clear}</p>
      )}
    </section>
  )
}

export function SectionTitle({ title, right }: { title: React.ReactNode; right?: React.ReactNode }) {
  return (
    <div className="mb-3 flex items-center gap-2.5">
      <h2 className="shrink-0 font-display text-[17px] font-semibold text-fg">{title}</h2>
      <span aria-hidden className="h-0 min-w-6 flex-1 border-t border-dashed border-line-strong/80" />
      {right}
    </div>
  )
}

// ---------- 进行中 ----------

const segCls: Record<string, string> = { done: 'bg-run', review: 'bg-seal', in_progress: 'bg-accent', blocked: 'bg-wait' }

function PlanProgress({ x }: { x: Plan }) {
  const openPlan = useOpenPlan()
  const all = x.stages.flatMap((st) => st.requirements)
  const count = (s: string) => all.filter((r) => r.status === s).length
  const segs = ['done', 'review', 'in_progress', 'blocked'].map((s) => [s, count(s)] as const).filter(([, n]) => n)
  const rest = all.length - segs.reduce((n, [, c]) => n + c, 0)
  const st = planState(x)
  return (
    <div role="button" tabIndex={0} onClick={() => openPlan(x.code)} onKeyDown={(e) => e.key === 'Enter' && openPlan(x.code)} className="grid cursor-pointer gap-2 border-t border-line px-4 py-3 transition-colors first:border-t-0 hover:bg-hover/50">
      <div className="flex min-w-0 flex-wrap items-center gap-x-2 gap-y-1">
        <RefChip code={x.code} status={x.status} link={false} />
        <span className="min-w-0 truncate text-sm font-semibold text-fg">{x.title}</span>
        <span className="text-xs text-fg-3">
          {t.desk.planAt(x.current, x.stages.length)}
          {st.tone !== 'active' && `${t.common.sep}${st.label}`}
        </span>
      </div>
      {all.length > 0 && (
        <div className="flex h-1.5 gap-0.5 overflow-hidden rounded-full bg-sunken" aria-label={t.desk.planBar(all.length, count('done'), count('review'))}>
          {segs.map(([s, n]) => (
            <i key={s} className={cx('block h-full', segCls[s])} style={{ flex: n }} />
          ))}
          {rest > 0 && <i className="block h-full" style={{ flex: rest }} />}
        </div>
      )}
      <div className="flex flex-wrap items-center gap-x-1 gap-y-1.5 text-fg-3">
        {x.stages.map((st, i) => (
          // 手机上只列当前阶段的需求，其余阶段看进度条
          <span key={st.id ?? i} className={cx('inline-flex flex-wrap items-center gap-1', st.seq !== x.current && 'max-sm:hidden')}>
            {i > 0 && <span aria-hidden className="mx-1 h-3.5 border-l border-line-strong max-sm:hidden" />}
            {st.requirements.map((r) => (
              <RefChip key={r.code} code={r.code} status={r.status} />
            ))}
          </span>
        ))}
      </div>
    </div>
  )
}

function ReqProgress({ r }: { r: Requirement }) {
  const own = useOwnership()(r)
  const s = own?.session
  return (
    <div className="flex min-w-0 items-center gap-2.5 border-t border-line px-4 py-2.5 first:border-t-0">
      <RefChip code={r.code} status={r.status} size="row" className="min-w-0 flex-1" />
      {s ? (
        <span className="inline-flex max-w-[40%] shrink-0 items-center gap-1.5 text-xs text-fg-3">
          <SessAvatar role={s.role} />
          <span className="truncate">{sessionTitle(s)}</span>
        </span>
      ) : (
        own && <span className="shrink-0 text-xs text-fg-3">{own.short}</span>
      )}
    </div>
  )
}

const maxSolo = 8

export function InProgress() {
  const plans = usePlans()
  const reqs = useRequirements()
  const active = (plans.data ?? []).filter((x) => x.status === 'active')
  const solo = (reqs.data ?? []).filter((r) => !r.plan_code && (r.status === 'in_progress' || r.status === 'blocked')).sort((a, b) => b.updated_at.localeCompare(a.updated_at))
  if (!active.length && !solo.length) return null
  return (
    <section className="mb-7">
      <SectionTitle title={t.desk.doing} />
      <Card className="overflow-hidden p-0">
        {active.map((x) => (
          <PlanProgress key={x.id} x={x} />
        ))}
        {solo.slice(0, maxSolo).map((r) => (
          <ReqProgress key={r.id} r={r} />
        ))}
        {solo.length > maxSolo && (
          <a href="/requirements" className="block border-t border-line px-4 py-2.5 text-xs text-accent hover:underline">
            {t.desk.moreReqs(solo.length - maxSolo)}
          </a>
        )}
      </Card>
    </section>
  )
}
