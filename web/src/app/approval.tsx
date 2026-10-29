import { useEffect, useMemo, useRef, useState, type ReactNode } from 'react'
import { useSearchParams } from 'react-router'
import { CheckCheck, ChevronLeft, ChevronRight, CircleCheck, PanelRight, X } from 'lucide-react'
import type { Decision, Session } from '../api/client'
import { useAway, useDecisions, useMorning, usePlans, usePrefetchReqs, useSessions } from '../api/queries'
import { t } from '../i18n'
import { cx } from '../lib/util'
import { Button, Skeleton, Tip } from '../ui/base'
import { Panel } from '../ui/overlay'
import { Pager } from '../ui/gesture'
import { RoleBadge } from '../ui/marks'
import { Md } from '../ui/markdown'
import { RefChip } from '../ui/refmark'
import { askKindLabel, askTitle, buildAsks, decisionKind, pendingKeyOf, type Ask } from './asks'
import { useKsEvent } from './command'
import { DecisionComposer, OrphanCard } from './DecisionCard'
import { ProxyNote, OverrideBox } from './grant'
import { useWorkbench } from './inbox'
import { useGoBack, useOpenSession } from './nav'
import { PeekScope, RefPeek, usePeek } from './peek'
import { AdviceNote } from './morning'
import { PlanBody } from './plans'
import { BackButton, DrawerSkeleton, ReqView } from './reqDrawer'
import { Callout, sessionTitle } from './SessionCard'
import { isStatusReq, StatusReqNote } from './statusReq'
import { useAdvance, usePendingMap } from './verdict'

// 审批抽屉（R-177）：所有要你下判断的事只在这里处理——需求验收、阶段验收、计划批准、批准类 / 选项类决策、
// Agent 的限时请求（R-169 推荐开 Dev 会话）、转交、会话在等你、推翻代答。同一个外壳（需求抽屉的版式：顶栏 → 中部滚动 → 底部固定的判断），
// 按类型换中部内容；底部统一是 R-161 的 Verdict，用词按 R-176 的场景。
// 从「要你处理」打开时按它的顺序排队：顶部 ‹ › 切换，← → / J K 同样切换；处理完显示「都处理完了」。
// 撤销期不挡用户的下一步（R-201），但系统替用户做的下一步要等撤销期结束（R-226，docs/principles.md 第 8 条）：点了通过 / 批准 / 回答，停在这一件盖着撤销遮罩，3 秒走完盖印后才滑到下一件；
// 撤回则不翻；撤销期内自己翻走会取消这次自动跳转，撤回挪到顶部浮动条（verdict.tsx 的 useAdvance）。

type Dir = 'next' | 'prev' | 'pass'


// useDecided：这件事已经下了判断（撤销期、盖印中或已提交）。
function useDecided(a: Ask | undefined) {
  const pend = usePendingMap()
  const k = pendingKeyOf(a)
  const e = k ? pend.get(k) : undefined
  return !!e && e.act !== 'move'
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
    if (p === 'x' && d) return { key, kind: 'proxy', title: askTitle(d), at: d.proxy_at ?? d.answered_at ?? d.created_at, counted: false, code: d.code, d, projectId: d.project_id ?? '', projectName: d.project_name ?? '' }
    const old = seen.current.get(key)
    if (old) {
      // 用最新的数据刷新（决策、计划会变）
      if (old.d && d) return { ...old, d }
      const x = old.plan && (plans.data ?? []).find((v) => v.code === old.code)
      if (x) return { ...old, plan: x }
      return old
    }
    if ((p === 'd' || p === 'o') && d) return { key, kind: p === 'o' ? 'orphan' : decisionKind(d), title: askTitle(d), at: d.created_at, counted: true, code: d.code, d, projectId: d.project_id ?? '', projectName: d.project_name ?? '' }
    const x = (plans.data ?? []).find((v) => v.code === id)
    if ((p === 'p' || p === 'g') && x) return { key, kind: p === 'g' ? 'stage' : 'plan', title: x.title, at: x.updated_at, counted: true, code: x.code, plan: x, stage: x.current, projectId: x.project_id ?? '', projectName: x.project_name ?? '' }
    const s = (sessions.data?.sessions ?? []).find((v) => v.key === id)
    if (p === 's' && s) return { key, kind: s.state === 'waiting_approval' ? 'permit' : 'question', title: sessionTitle(s), at: s.state_since, counted: true, s, projectId: s.project_id ?? '', projectName: s.project_name ?? '' }
    if (p === 'r') return { key, kind: 'review', title: id, at: '', counted: true, code: id, projectId: '', projectName: '' }
    return undefined
  }
  return { asks, find, loading: wb.loading }
}

export function ApprovalPanel({ askKey, onClose }: { askKey: string | null; onClose: () => void }) {
  const [, setParams] = useSearchParams()
  const { asks, find, loading } = useAskSource()
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
  // 引用在左边再展开一张纸（R-196）
  const peek = usePeek(cur?.code, askKey)
  const goBack = useGoBack()
  const inQueue = !!askKey && order.includes(askKey) && cur?.kind !== 'proxy'
  const idx = askKey ? order.indexOf(askKey) : -1
  const prev = inQueue ? order.slice(0, idx).reverse().find((k) => live.has(k)) : undefined
  const next = inQueue ? order.slice(idx + 1).find((k) => live.has(k)) : undefined
  const liveOrder = order.filter((k) => live.has(k))
  // 再往外一件的需求详情先取好（邻件本身已画在带子上）
  const reviewCode = (k?: string) => {
    const a = k ? find(k) : undefined
    return a?.kind === 'review' ? a.code : undefined
  }
  const liveBefore = (k: string) => order.slice(0, order.indexOf(k)).reverse().find((x) => live.has(x))
  const liveAfter = (k: string) => order.slice(order.indexOf(k) + 1).find((x) => live.has(x))
  usePrefetchReqs([reviewCode(prev && liveBefore(prev)), reviewCode(next && liveAfter(next))])

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
  // 已经下了判断、还在撤销期里的事仍在「要你处理」里，不算下一件
  const pendMap = usePendingMap()
  const open = (k: string) => {
    if (!live.has(k) || k === askKey) return false
    const pk = pendingKeyOf(find(k))
    return !pk || !pendMap.has(pk)
  }
  const advance = () => {
    const after = order.slice(idx + 1).find(open) ?? order.slice(0, Math.max(0, idx)).reverse().find(open)
    if (after) go(after, 'pass')
    else {
      setDir('pass')
      setFinished(true)
    }
  }
  const sealed = useDecided(cur)
  const awayOn = !!useAway().data?.away // 离开中：这里的处理照常生效，记进时段报告（R-210）
  // 下了判断停在这一件，撤销期走完盖印后翻到下一件；撤销期内撤回或自己翻走则不翻
  const here = askKey
  useAdvance(pendingKeyOf(cur), inQueue && !finished, advance, () => here && go(here, 'prev'))
  const wasLive = useRef(false)
  const isLive = !!askKey && live.has(askKey)
  // 改状态申请在看的时候作废了（R-188）：停在这里显示「已作废」，不自动滑走
  const voided = isStatusReq(cur?.d) && !!cur?.d?.voided
  useEffect(() => {
    if (wasLive.current && !isLive && inQueue && !sealed && !finished && !voided) advance()
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
    return a ? `${askKindLabel(a)} · ${a.title}` : undefined
  }
  // 顶栏按「哪一件」画：邻件在带子上露出来时，顶栏就已经是它自己的（第几件、类型、编号），切过来时不变
  const navOf = (key: string) => {
    const inQ = order.includes(key) && find(key)?.kind !== 'proxy'
    const i = order.indexOf(key)
    return {
      inQ,
      prev: inQ ? order.slice(0, i).reverse().find((k) => live.has(k)) : undefined,
      next: inQ ? order.slice(i + 1).find((k) => live.has(k)) : undefined,
      pos: live.has(key) ? liveOrder.indexOf(key) + 1 : 0,
    }
  }
  const topFor = (key: string) => {
    const a = find(key)
    const n = navOf(key)
    return (
      <div className="flex shrink-0 items-center gap-1 px-3 pt-3 pb-1 sm:px-4">
        {goBack ? <BackButton back={goBack} /> : loading && !n.inQ ? (
          // 加载中先占住前后切换的位置（R-211）
          <span aria-hidden className="flex items-center gap-1">
            <span className={navBtn}>
              <ChevronLeft className="size-[18px] opacity-30" />
            </span>
            <span className={navBtn}>
              <ChevronRight className="size-[18px] opacity-30" />
            </span>
            <Skeleton className="ml-1 h-3 w-24" />
          </span>
        ) : n.inQ && (
          <>
            <Tip content={label(n.prev)}>
              <button className={navBtn} disabled={!n.prev} aria-label={t.ask.prev} onClick={() => n.prev && go(n.prev, 'prev')}>
                <ChevronLeft className="size-[18px]" />
              </button>
            </Tip>
            <Tip content={label(n.next)}>
              <button className={navBtn} disabled={!n.next} aria-label={t.ask.next} onClick={() => n.next && go(n.next, 'next')}>
                <ChevronRight className="size-[18px]" />
              </button>
            </Tip>
            <span className="ml-1 text-xs whitespace-nowrap text-fg-3 tabular">{n.pos ? `${t.ask.queue} · ${t.ask.position(n.pos, liveOrder.length)}` : t.ask.queue}</span>
          </>
        )}
        <span className="ml-auto flex min-w-0 items-center gap-1.5 truncate px-1 text-xs text-fg-3">
          {awayOn && <span className="truncate text-accent">{t.away.drawerNote}</span>}
          {a && (
            <>
              <b className="shrink-0 font-semibold tracking-wider text-fg-2">{askKindLabel(a)}</b>
              {a.code && <RefChip code={a.code} link={false} />}
            </>
          )}
        </span>
        <button className={navBtn} aria-label={t.common.close} onClick={onClose}>
          <X className="size-[18px]" />
        </button>
      </div>
    )
  }
  const top = topFor(askKey ?? '')
  // 邻件是否已下了判断（撤销期 / 盖印中），和 useDecided 同一口径
  const decidedOf = (a: Ask | undefined) => {
    const pk = pendingKeyOf(a)
    const e = pk ? pendMap.get(pk) : undefined
    return !!e && e.act !== 'move'
  }
  // 带子上的任意一页：这一件、上一件、下一件同一个画法（邻件提前画好，拖出来就是真实内容）
  const page = (key: string) => {
    if (key === 'finished') return <AllDone top={topFor('')} onClose={onClose} />
    const a = find(key)
    const tp = key === askKey ? top : topFor(key)
    // 数据没到齐时先画骨架，不先闪一下「已经处理了」（R-211）
    if (a) return <AskBody a={a} top={tp} sealed={key === askKey ? sealed : decidedOf(a)} />
    if (loading)
      return (
        <div className="flex min-h-0 flex-1 flex-col">
          {tp}
          <DrawerSkeleton />
        </div>
      )
    return <Gone top={tp} />
  }

  return (
    <PeekScope peek={peek}>
    <Panel
      open={!!askKey}
      onClose={onClose}
      wide="approval"
      bare
      aside={peek.stack.length && askKey ? <RefPeek peek={peek} from={`a:${askKey}`} mainLabel={cur?.code} /> : undefined}
      onAsideClose={peek.close}
      title={cur ? `${askKindLabel(cur)} ${cur.title}` : t.ask.queue}
    >
      {/* 左右拖 / 妙控板双指横滑切换上一件 / 下一件（R-73），和顶栏 ‹ ›、← → 同一条带子 */}
      <Pager
        k={finished ? 'finished' : (askKey ?? '')}
        dir={dir === 'prev' ? -1 : 1}
        prev={finished ? undefined : prev}
        next={finished ? undefined : next}
        onNav={(d) => {
          const k = d > 0 ? next : prev
          if (!k) return false
          go(k, d > 0 ? 'next' : 'prev')
        }}
        render={page}
      />
    </Panel>
    </PeekScope>
  )
}

// AskBody：按类型换中部内容；每种内容自己把判断放在底部。
function AskBody({ a, top, sealed }: { a: Ask; top: ReactNode; sealed: boolean }) {
  const decisions = useDecisions()
  const plans = usePlans()
  const morning = useMorning()
  // 验收建议挂在抽屉顶上，到齐前先画骨架，免得建议后到把整页往下推（R-211）
  if (a.kind === 'review' && morning.isPending)
    return (
      <div className="flex min-h-0 flex-1 flex-col">
        {top}
        <DrawerSkeleton />
      </div>
    )
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
    if (d && isStatusReq(d) && d.voided) return <VoidedBody d={d} top={top} />
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
  if (a.s) return <SessionBody s={a.s} top={top} />
  return <Gone top={top} />
}

function Scroll({ children, className }: { children: ReactNode; className?: string }) {
  return <div className={cx('min-h-0 flex-1 space-y-4 overflow-y-auto overscroll-contain bg-bg/40 px-4 py-5 sm:px-5', className)}>{children}</div>
}

function Foot({ children }: { children: ReactNode }) {
  return <footer className="shrink-0 border-t border-line bg-surface px-4 pt-3 pb-[max(0.875rem,env(safe-area-inset-bottom))] sm:px-5">{children}</footer>
}

// VoidedBody：改状态申请已作废（R-188）——批准前需求状态被改动过，申请没有执行；只展示，不再作答。
function VoidedBody({ d, top }: { d: Decision; top: ReactNode }) {
  return (
    <div className="flex min-h-0 flex-1 flex-col">
      {top}
      <Scroll>
        <h3 className="font-display text-[17px] leading-snug font-semibold text-fg">{d.question}</h3>
        <StatusReqNote d={d} />
      </Scroll>
    </div>
  )
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

// SessionBody：会话在等你——等授权 / 用提问工具在问你，要回终端处理。
function SessionBody({ s, top }: { s: Session; top: ReactNode }) {
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
        <p className="text-[13px] leading-relaxed text-fg-3">{t.ask.terminal}</p>
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
