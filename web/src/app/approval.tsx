import { useEffect, useMemo, useRef, useState, type ReactNode } from 'react'
import { useSearchParams } from 'react-router'
import { AnimatePresence, motion } from 'motion/react'
import { CheckCheck, ChevronLeft, ChevronRight, CircleCheck, PanelRight, X } from 'lucide-react'
import type { Decision, Session } from '../api/client'
import { useDecisions, usePlans, useSessions } from '../api/queries'
import { t } from '../i18n'
import { cx } from '../lib/util'
import { Button, Tip } from '../ui/base'
import { Panel } from '../ui/overlay'
import { RoleBadge } from '../ui/marks'
import { Md } from '../ui/markdown'
import { RefChip } from '../ui/refmark'
import { buildAsks, type Ask } from './asks'
import { useKsEvent } from './command'
import { DecisionComposer, OrphanCard } from './DecisionCard'
import { ProxyNote, OverrideBox } from './grant'
import { useWorkbench } from './inbox'
import { useOpenSession } from './nav'
import { AdviceNote } from './morning'
import { useOutbox } from './outbox'
import { PlanBody } from './plans'
import { ReplyBox } from './replyBox'
import { ReqView } from './reqDrawer'
import { Callout, sessionTitle } from './SessionCard'
import { reducedMotion, SEAL_MS, usePendingMap } from './verdict'

// 审批抽屉（R-177）：所有要你下判断的事只在这里处理——需求验收、阶段验收、计划批准、批准类 / 选项类决策、
// （R-169 的请求预留）、转交、会话在等你、推翻代答。同一个外壳（需求抽屉的版式：顶栏 → 中部滚动 → 底部固定的判断），
// 按类型换中部内容；底部统一是 R-161 的 Verdict，用词按 R-176 的场景。
// 从「要你处理」打开时按它的顺序排队：顶部 ‹ › 切换，← → / J K 同样切换；盖印后自动滑到下一件，处理完显示「都处理完了」。

type Dir = 'next' | 'prev' | 'pass'

const slide = {
  enter: (d: Dir) => ({ x: d === 'prev' ? -56 : 56, opacity: 0 }),
  center: { x: 0, opacity: 1, rotate: 0 },
  exit: (d: Dir) => ({ x: d === 'prev' ? 96 : -56, opacity: 0, rotate: d === 'pass' ? 1.2 : 0 }),
}

// pendingKeyOf：这件事在撤销期 / 盖印中用的键（与 Verdict 的 pendingKey 一致）。
function pendingKeyOf(a: Ask) {
  if (a.kind === 'review') return a.code
  if (a.kind === 'plan') return `plan:${a.code}`
  if (a.kind === 'stage') return `stage:${a.code}:${a.stage}`
  return undefined
}

// useSealed：这件事已经盖印（判断生效、不能撤销了），抽屉据此滑到下一件。
function useSealed(a: Ask | undefined) {
  const pend = usePendingMap()
  const { sealed } = useOutbox()
  if (!a) return false
  const k = pendingKeyOf(a)
  if (k) {
    const e = pend.get(k)
    return !!e && e.phase !== 'wait' && e.act !== 'move'
  }
  if (a.d && (a.kind === 'decide' || a.kind === 'approve' || a.kind === 'request')) return !!sealed[a.d.code]
  return false
}

// useAskSource：按键找到这件事（已不在队列里的也能找到：刚处理完、从链接打开、推翻代答）。
function useAskSource() {
  const wb = useWorkbench()
  const decisions = useDecisions()
  const plans = usePlans()
  const sessions = useSessions()
  const asks = useMemo(() => buildAsks(wb), [wb])
  const seen = useRef(new Map<string, Ask>())
  for (const a of asks) seen.current.set(a.key, a)
  const find = (key: string): Ask | undefined => {
    const live = asks.find((a) => a.key === key)
    if (live) return live
    const [p, ...rest] = key.split(':')
    const id = rest.join(':')
    const d = (decisions.data ?? []).find((x) => x.code === id)
    if (p === 'x' && d) return { key, kind: 'proxy', title: d.short_title || d.question, at: d.proxy_at ?? d.answered_at ?? d.created_at, counted: false, code: d.code, d, projectId: d.project_id ?? '', projectName: d.project_name ?? '' }
    const old = seen.current.get(key)
    if (old) {
      // 用最新的数据刷新（决策、计划会变）
      if (old.d && d) return { ...old, d }
      const x = old.plan && (plans.data ?? []).find((v) => v.code === old.code)
      if (x) return { ...old, plan: x }
      return old
    }
    if ((p === 'd' || p === 'o') && d) return { key, kind: p === 'o' ? 'orphan' : d.approval ? 'approve' : 'decide', title: d.short_title || d.question, at: d.created_at, counted: true, code: d.code, d, projectId: d.project_id ?? '', projectName: d.project_name ?? '' }
    const x = (plans.data ?? []).find((v) => v.code === id)
    if ((p === 'p' || p === 'g') && x) return { key, kind: p === 'g' ? 'stage' : 'plan', title: x.title, at: x.updated_at, counted: true, code: x.code, plan: x, stage: x.current, projectId: x.project_id ?? '', projectName: x.project_name ?? '' }
    const s = (sessions.data?.sessions ?? []).find((v) => v.key === id)
    if ((p === 's' || p === 'm') && s) return { key, kind: p === 'm' ? 'reply' : 'question', title: sessionTitle(s), at: s.state_since, counted: p === 's', s, projectId: s.project_id ?? '', projectName: s.project_name ?? '' }
    if (p === 'r') return { key, kind: 'review', title: id, at: '', counted: true, code: id, projectId: '', projectName: '' }
    return undefined
  }
  return { asks, find, loading: wb.loading }
}

export function ApprovalPanel({ askKey, onClose }: { askKey: string | null; onClose: () => void }) {
  const [, setParams] = useSearchParams()
  const { asks, find } = useAskSource()
  const live = useMemo(() => new Set(asks.map((a) => a.key)), [asks])
  const [order, setOrder] = useState<string[]>([])
  const [dir, setDir] = useState<Dir>('next')
  const [finished, setFinished] = useState(false)
  const internal = useRef(false)

  // 从外部打开时，按「要你处理」此刻的顺序排队；之后新进来的事接在后面，处理过的留在原位（前后顺序不乱）
  useEffect(() => {
    if (!askKey) {
      setOrder([])
      setFinished(false)
      return
    }
    if (internal.current) {
      internal.current = false
      return
    }
    setFinished(false)
    setOrder(asks.map((a) => a.key))
  }, [askKey]) // eslint-disable-line react-hooks/exhaustive-deps
  useEffect(() => {
    setOrder((o) => {
      const add = asks.map((a) => a.key).filter((k) => !o.includes(k))
      return add.length ? [...o, ...add] : o
    })
  }, [asks])

  const cur = askKey ? find(askKey) : undefined
  const inQueue = !!askKey && order.includes(askKey) && cur?.kind !== 'proxy'
  const idx = askKey ? order.indexOf(askKey) : -1
  const prev = inQueue ? order.slice(0, idx).reverse().find((k) => live.has(k)) : undefined
  const next = inQueue ? order.slice(idx + 1).find((k) => live.has(k)) : undefined
  const liveOrder = order.filter((k) => live.has(k))
  const pos = askKey && live.has(askKey) ? liveOrder.indexOf(askKey) + 1 : 0

  const go = (key: string, d: Dir) => {
    internal.current = true
    setDir(d)
    setFinished(false)
    setParams(
      (p) => {
        const n = new URLSearchParams(p)
        n.delete('plan')
        n.set('a', key)
        return n
      },
      { replace: true },
    )
  }
  // 这一件处理完（盖印，或者在别处处理掉了）后滑到下一件；后面没有了就往前找，都没有就显示「都处理完了」
  const advance = () => {
    const after = order.slice(idx + 1).find((k) => live.has(k) && k !== askKey) ?? order.slice(0, Math.max(0, idx)).reverse().find((k) => live.has(k) && k !== askKey)
    if (after) go(after, 'pass')
    else {
      setDir('pass')
      setFinished(true)
    }
  }
  const sealed = useSealed(cur)
  useEffect(() => {
    if (!sealed || !inQueue) return
    const id = window.setTimeout(advance, reducedMotion() ? 0 : SEAL_MS)
    return () => window.clearTimeout(id)
  }, [sealed, askKey]) // eslint-disable-line react-hooks/exhaustive-deps
  const wasLive = useRef(false)
  const isLive = !!askKey && live.has(askKey)
  useEffect(() => {
    if (wasLive.current && !isLive && inQueue && !sealed && !finished) advance()
    wasLive.current = isLive
  }, [isLive, askKey]) // eslint-disable-line react-hooks/exhaustive-deps

  // ← → / J K 切换（输入时不拦截）
  useEffect(() => {
    if (!askKey) return
    const onKey = (e: KeyboardEvent) => {
      const el = e.target as HTMLElement
      if (e.metaKey || e.ctrlKey || e.altKey || el.closest('input, textarea, [contenteditable=true], [role=menu]')) return
      if (e.key === 'ArrowLeft' && prev) go(prev, 'prev')
      if (e.key === 'ArrowRight' && next) go(next, 'next')
    }
    window.addEventListener('keydown', onKey)
    return () => window.removeEventListener('keydown', onKey)
  })
  useKsEvent(
    'ks:req-step',
    (d) => {
      if (d === 1 && next) go(next, 'next')
      if (d === -1 && prev) go(prev, 'prev')
    },
    !!askKey,
  )

  const navBtn = 'flex size-8 items-center justify-center rounded-lg text-fg-2 transition hover:bg-hover hover:text-fg active:scale-95 disabled:pointer-events-none disabled:opacity-30 pointer-coarse:size-10'
  const label = (k?: string) => {
    const a = k ? find(k) : undefined
    return a ? `${t.ask.kind[a.kind]} · ${a.title}` : undefined
  }
  const top = (
    <div className="flex shrink-0 items-center gap-1 px-3 pt-3 pb-1 sm:px-4">
      {inQueue && (
        <>
          <Tip content={label(prev)}>
            <button className={navBtn} disabled={!prev} aria-label={t.ask.prev} onClick={() => prev && go(prev, 'prev')}>
              <ChevronLeft className="size-[18px]" />
            </button>
          </Tip>
          <Tip content={label(next)}>
            <button className={navBtn} disabled={!next} aria-label={t.ask.next} onClick={() => next && go(next, 'next')}>
              <ChevronRight className="size-[18px]" />
            </button>
          </Tip>
          <span className="ml-1 text-xs whitespace-nowrap text-fg-3 tabular">{pos ? `${t.ask.queue} · ${t.ask.position(pos, liveOrder.length)}` : t.ask.queue}</span>
        </>
      )}
      <span className="ml-auto flex min-w-0 items-center gap-1.5 truncate px-1 text-xs text-fg-3">
        {cur && !finished && (
          <>
            <b className="shrink-0 font-semibold tracking-wider text-fg-2">{t.ask.kind[cur.kind]}</b>
            {cur.code && <RefChip code={cur.code} link={false} />}
          </>
        )}
      </span>
      <button className={navBtn} aria-label={t.common.close} onClick={onClose}>
        <X className="size-[18px]" />
      </button>
    </div>
  )

  const wide = cur?.kind === 'review' && !finished ? 'xl' : true
  return (
    <Panel
      open={!!askKey}
      onClose={onClose}
      wide={wide}
      bare
      title={cur ? `${t.ask.kind[cur.kind]} ${cur.title}` : t.ask.queue}
      onSwipe={(d) => {
        if (d === 'left' && next) go(next, 'next')
        else if (d === 'right' && prev) go(prev, 'prev')
        else return false
        return true
      }}
    >
      <div className="relative flex min-h-0 flex-1 flex-col overflow-hidden">
        <AnimatePresence initial={false} custom={dir} mode="popLayout">
          <motion.div
            key={finished ? 'finished' : (askKey ?? '')}
            custom={dir}
            variants={slide}
            initial="enter"
            animate="center"
            exit="exit"
            transition={{ duration: 0.32, ease: [0.2, 0.8, 0.2, 1] }}
            className="flex min-h-0 flex-1 flex-col bg-surface"
          >
            {finished ? <AllDone top={top} onClose={onClose} /> : cur ? <AskBody a={cur} top={top} sealed={sealed} /> : <Gone top={top} />}
          </motion.div>
        </AnimatePresence>
      </div>
    </Panel>
  )
}

// AskBody：按类型换中部内容；每种内容自己把判断放在底部。
function AskBody({ a, top, sealed }: { a: Ask; top: ReactNode; sealed: boolean }) {
  const decisions = useDecisions()
  const plans = usePlans()
  if (a.kind === 'review')
    return (
      <ReqView
        refId={a.code!}
        top={
          <>
            {top}
            {/* 参谋的验收建议（R-165）挂在抽屉顶上 */}
            <div className="shrink-0 px-4 empty:hidden sm:px-5 [&>p]:mt-0 [&>p]:mb-1">
              <AdviceNote code={a.code!} />
            </div>
          </>
        }
      />
    )
  if (a.kind === 'plan' || a.kind === 'stage') {
    const x = (plans.data ?? []).find((v) => v.code === a.code) ?? a.plan
    return x ? <PlanBody key={x.code} x={x} top={top} /> : <Gone top={top} />
  }
  const d: Decision | undefined = a.d ? ((decisions.data ?? []).find((v) => v.code === a.d!.code) ?? a.d) : undefined
  if (a.kind === 'decide' || a.kind === 'approve' || a.kind === 'request') {
    if (!d || (d.status !== 'pending' && !sealed)) return <Gone top={top} />
    return (
      <div className="flex min-h-0 flex-1 flex-col">
        {top}
        <DecisionComposer key={d.code} d={d} drawer />
      </div>
    )
  }
  if (a.kind === 'orphan' && d) return <OrphanBody d={d} top={top} />
  if (a.kind === 'proxy' && d) return <ProxyBody d={d} top={top} />
  if (a.s) return <SessionBody s={a.s} reply={a.kind === 'reply'} top={top} />
  return <Gone top={top} />
}

function Scroll({ children, className }: { children: ReactNode; className?: string }) {
  return <div className={cx('min-h-0 flex-1 space-y-4 overflow-y-auto overscroll-contain bg-bg/40 px-4 py-5 sm:px-5', className)}>{children}</div>
}

function Foot({ children }: { children: ReactNode }) {
  return <footer className="shrink-0 border-t border-line bg-surface px-4 pt-3 pb-[max(0.875rem,env(safe-area-inset-bottom))] sm:px-5">{children}</footer>
}

function OrphanBody({ d, top }: { d: Decision; top: ReactNode }) {
  const sessions = useSessions()
  return (
    <div className="flex min-h-0 flex-1 flex-col">
      {top}
      <Scroll>
        <OrphanCard d={d} sessions={sessions.data?.sessions ?? []} />
      </Scroll>
    </div>
  )
}

// ProxyBody：推翻参谋的代答（从离开期间小结打开）：问题、背景、代答留痕；底部写你的答复。
function ProxyBody({ d, top }: { d: Decision; top: ReactNode }) {
  const over = !!d.overridden_at
  return (
    <div className="flex min-h-0 flex-1 flex-col">
      {top}
      <Scroll>
        <h3 className="font-display text-[17px] leading-snug font-semibold text-fg">{d.short_title || d.question}</h3>
        {d.short_title && <p className="text-sm leading-relaxed text-fg-2">{d.question}</p>}
        {d.context && <Md text={d.context} className="text-sm leading-relaxed text-fg-2" />}
        <ProxyNote d={d} withOverride={false} />
      </Scroll>
      {!over && d.status !== 'closed' && (
        <Foot>
          <OverrideBox d={d} startOpen />
        </Foot>
      )}
    </div>
  )
}

// SessionBody：会话在等你——等授权 / 在问你时要回终端处理；在等你回复时就在这里回复。
function SessionBody({ s, reply, top }: { s: Session; reply: boolean; top: ReactNode }) {
  const open = useOpenSession()
  return (
    <div className="flex min-h-0 flex-1 flex-col">
      {top}
      <Scroll>
        <div className="flex min-w-0 items-center gap-2">
          <h3 className="min-w-0 truncate font-display text-[17px] font-semibold text-fg">{sessionTitle(s)}</h3>
          <RoleBadge s={s} />
          <Button size="sm" variant="ghost" icon={<PanelRight className="size-3.5" />} onClick={() => open(s.key)} className="ml-auto shrink-0">
            {t.ask.openSession}
          </Button>
        </div>
        <Callout s={s} />
        {reply ? <ReplyBox s={s} /> : <p className="text-[13px] leading-relaxed text-fg-3">{t.ask.terminal}</p>}
      </Scroll>
    </div>
  )
}

function Gone({ top }: { top: ReactNode }) {
  return (
    <div className="flex min-h-0 flex-1 flex-col">
      {top}
      <div className="flex flex-1 flex-col items-center justify-center gap-3 px-6 text-center text-fg-3">
        <CircleCheck className="size-8 text-run" />
        <div className="font-display text-base text-fg-2">{t.ask.gone}</div>
      </div>
    </div>
  )
}

function AllDone({ top, onClose }: { top: ReactNode; onClose: () => void }) {
  return (
    <div className="flex min-h-0 flex-1 flex-col">
      {top}
      <div className="flex flex-1 flex-col items-center justify-center gap-3 px-6 text-center">
        <span className="flex size-14 items-center justify-center rounded-2xl bg-run-soft text-run">
          <CheckCheck className="size-7" />
        </span>
        <div className="font-display text-lg text-fg">{t.ask.allDone}</div>
        <p className="text-sm text-fg-3">{t.ask.allDoneSub}</p>
        <Button className="mt-2" onClick={onClose}>
          {t.common.close}
        </Button>
      </div>
    </div>
  )
}
