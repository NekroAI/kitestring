import { createContext, useContext, useEffect, useLayoutEffect, useMemo, useState, type ReactNode } from 'react'
import { HoverCard } from 'radix-ui'
import { Check, ChevronDown, ChevronRight, ListChecks } from 'lucide-react'
import type { Requirement, Schemas } from '../api/client'
import { useDecisions, useRequirement } from '../api/queries'
import { t } from '../i18n'
import { cx, useMedia, useNow } from '../lib/util'
import { Button, Tip } from '../ui/base'
import { Ctx, type CtxItem } from '../ui/contextMenu'
import { RoleTag } from '../ui/marks'
import { RefChip, ReqGlyph, ReqStatus, statusCls } from '../ui/refmark'
import type { useTouchDrag } from '../ui/touchDrag'
import { useReqMenu } from './menus'
import { useOpenReq } from './nav'
import { allStatuses, priorities, useSetStatus } from './requirements'

// 需求池盘面（R-133）：分布条、池子指标、按状态流动排列的需求块。
// 布局按内容区的实际宽度排（不看视口）：侧栏展开或折叠时，盘面自动在宽屏横排与纵向之间切换。

export type Pool = Omit<Schemas['Pool'], 'items' | 'done_daily'> & { items: Schemas['PoolItem'][]; done_daily: Schemas['PoolDay'][] }
export type ColorBy = 'priority' | 'project' | 'plan'

export const flowStatuses = ['ready', 'in_progress', 'blocked', 'review'] as const
export const openStatuses = ['draft', ...flowStatuses] as const

const prio: Record<string, number> = { P0: 0, P1: 1, P2: 2, P3: 3 }
const seq = (r: Requirement) => Number(r.code.slice(2)) || 0
export const sortReqs = (a: Requirement, b: Requirement) => (prio[a.priority] ?? 9) - (prio[b.priority] ?? 9) || b.updated_at.localeCompare(a.updated_at)

// ---------- 盘面上下文 ----------

type TD = ReturnType<typeof useTouchDrag> | null
type PoolCtx = {
  colorOf: (r: Requirement) => string
  since: Map<string, Schemas['PoolItem']>
  td: TD // 平板等触屏：长按浮起后拖动
  phone: boolean // 手机：长按出状态菜单代替拖拽
  drop: (code: string, status: string) => void
}
const Ctx_ = createContext<PoolCtx | null>(null)
export const PoolProvider = Ctx_.Provider
const usePoolCtx = () => useContext(Ctx_)!

// 草稿按优先级、再按先记下的在前（放得久的先处理）；逐条确认的队列用同一顺序
export const sortDrafts = (a: Requirement, b: Requirement) => (prio[a.priority] ?? 9) - (prio[b.priority] ?? 9) || a.created_at.localeCompare(b.created_at) || seq(a) - seq(b)
// 已完成 / 搁置：最近进入的在前
export function sortRecent(since: Map<string, Schemas['PoolItem']>) {
  return (a: Requirement, b: Requirement) => (since.get(b.code)?.since ?? b.updated_at).localeCompare(since.get(a.code)?.since ?? a.updated_at)
}

// ---------- 着色 ----------

const prioColor: Record<string, string> = {
  P0: 'var(--danger)',
  P1: 'var(--wait)',
  P2: 'color-mix(in oklab, var(--accent) 45%, var(--line-strong))',
  P3: 'var(--line-strong)',
}
// 类别色：按序号用黄金角分开色相，项目 / 计划少时彼此分得清；明暗两套主题都看得见
export const catColor = (i: number) => `oklch(0.68 0.12 ${Math.round((i * 137.5 + 230) % 360)})`

export function useColorOf(by: ColorBy, projectIds: string[], planCodes: string[]) {
  return useMemo(() => {
    const pi = new Map(projectIds.map((id, i) => [id, i]))
    const li = new Map(planCodes.map((c, i) => [c, i]))
    return (r: Requirement) => {
      if (by === 'project') return r.project_id && pi.has(r.project_id) ? catColor(pi.get(r.project_id)!) : 'var(--line-strong)'
      if (by === 'plan') return r.plan_code && li.has(r.plan_code) ? catColor(li.get(r.plan_code)!) : 'var(--line-strong)'
      return prioColor[r.priority] ?? prioColor.P2
    }
  }, [by, projectIds, planCodes])
}

export function Legend({ items }: { items: { key: string; label: string; color: string }[] }) {
  if (!items.length) return null
  return (
    <div className="flex flex-wrap items-center gap-x-3.5 gap-y-1 text-xs text-fg-3">
      {items.map((x) => (
        <span key={x.key} className="inline-flex min-w-0 items-center gap-1.5">
          <span className="h-3 w-[3px] shrink-0 rounded-full" style={{ background: x.color }} />
          <span className="max-w-[160px] truncate">{x.label}</span>
        </span>
      ))}
    </div>
  )
}

// ---------- 宽度 ----------

export function useWidth() {
  const [el, setEl] = useState<HTMLElement | null>(null)
  const [w, setW] = useState(0)
  useEffect(() => {
    if (!el) return
    const ro = new ResizeObserver(([e]) => setW(e.contentRect.width))
    ro.observe(el)
    return () => ro.disconnect()
  }, [el])
  return [setEl, w] as const
}

// 盘面占满视口剩余高度（顶栏、页面标题以下）：整页不滚动，内容多时在各区域内部滚动（R-133）。
// 高度 = 视口高 - 元素在文档里的顶部位置 - main 的底部留白；窗口太矮（< 420）时退回整页滚动。
export function useFillHeight(el: HTMLElement | null, on: boolean) {
  const [h, setH] = useState<number>()
  useLayoutEffect(() => {
    if (!el || !on) {
      setH(undefined)
      return
    }
    const main = el.closest('main')
    const calc = () => {
      const top = el.getBoundingClientRect().top + window.scrollY
      const pb = main ? parseFloat(getComputedStyle(main).paddingBottom) || 0 : 0
      setH(Math.max(420, Math.floor(window.innerHeight - top - pb)))
    }
    calc()
    window.addEventListener('resize', calc)
    // 授权横幅出现 / 消失会改变页面顶部位置
    const ro = new ResizeObserver(calc)
    if (el.parentElement) ro.observe(el.parentElement)
    return () => {
      window.removeEventListener('resize', calc)
      ro.disconnect()
    }
  }, [el, on])
  return h
}

// ---------- 分布条 ----------

// 按数量占宽，每段至少 min 宽保证可点；段内放「名称 数量」，放不下只放数量。
export function DistBar({ counts, statuses, active, onPick, thin }: { counts: Record<string, number>; statuses: readonly string[]; active: string | null; onPick: (s: string | null) => void; thin?: boolean }) {
  const [ref, w] = useWidth()
  const gap = thin ? 2 : 3
  const min = thin ? 10 : 34
  const sum = statuses.reduce((n, s) => n + (counts[s] ?? 0), 0)
  const widths = useMemo(() => {
    const avail = Math.max(0, w - gap * (statuses.length - 1))
    if (!avail) return null
    // 先按比例分；不到最小宽的固定为最小宽，剩下的再按比例分给其余段
    let fixed = new Set<string>()
    for (let pass = 0; pass < 3; pass++) {
      const rest = avail - fixed.size * min
      const restSum = statuses.filter((s) => !fixed.has(s)).reduce((n, s) => n + (counts[s] ?? 0), 0)
      const next = new Set(fixed)
      for (const s of statuses) if (!fixed.has(s) && (!restSum || ((counts[s] ?? 0) / restSum) * rest < min)) next.add(s)
      if (next.size === fixed.size) break
      fixed = next
    }
    const rest = avail - fixed.size * min
    const restSum = statuses.filter((s) => !fixed.has(s)).reduce((n, s) => n + (counts[s] ?? 0), 0)
    return Object.fromEntries(statuses.map((s) => [s, fixed.has(s) || !restSum ? min : ((counts[s] ?? 0) / restSum) * rest]))
  }, [w, statuses, counts, gap, min])
  return (
    <div ref={ref} className={cx('flex w-full', thin ? 'h-2 gap-0.5' : 'h-8 gap-[3px]')} role="group" aria-label={t.pool.title}>
      {statuses.map((s, i) => {
        const n = counts[s] ?? 0
        const name = t.reqStatus[s]
        const width = widths?.[s]
        // 估算文字宽度：中文约 13px / 字，数字约 8px
        const full = width != null && width >= name.length * 13 + String(n).length * 8 + 22
        const num = width != null && width >= String(n).length * 8 + 14
        const on = active === s
        return (
          <Tip key={s} content={t.pool.segTip(name, n, on)}>
            <button
              onClick={() => onPick(on ? null : s)}
              aria-pressed={on}
              aria-label={`${name} ${n}`}
              style={width != null ? { width, flex: 'none' } : { flex: `${Math.max(n, sum / 30)} 1 0` }}
              className={cx(
                'flex min-w-0 items-center overflow-hidden whitespace-nowrap transition-[opacity,box-shadow] duration-150',
                thin ? 'rounded-full' : 'rounded-md px-2 text-[13px] font-semibold',
                i === 0 && !thin && 'rounded-l-lg',
                i === statuses.length - 1 && !thin && 'rounded-r-lg',
                thin ? statusCls[s]?.dot : statusCls[s]?.pill,
                thin && (s === 'draft' || s === 'shelved') && 'bg-fg-3/35',
                active && !on && 'opacity-40 hover:opacity-70',
                on && 'ring-2 ring-accent ring-offset-1 ring-offset-bg',
              )}
            >
              {!thin && (full ? `${name} ${n}` : num ? n : '')}
            </button>
          </Tip>
        )
      })}
    </div>
  )
}

// ---------- 指标 ----------

function Metric({ n, label, tip, sub }: { n: ReactNode; label: ReactNode; tip: string; sub?: ReactNode }) {
  return (
    <Tip content={tip}>
      <span className="inline-flex items-baseline gap-1.5 whitespace-nowrap">
        <b className="font-display text-[19px] font-semibold text-fg tabular">{n}</b>
        <span className="text-[13px] text-fg-3">
          {label}
          {sub}
        </span>
      </span>
    </Tip>
  )
}

export function Metrics({ p }: { p: Pool }) {
  const m = t.pool.m
  return (
    <div className="flex flex-wrap items-baseline gap-x-6 gap-y-2">
      <Metric n={p.total} label={m.total} tip={m.tip.total} />
      <Metric n={`+${p.new_yesterday}`} label={m.newY} tip={m.tip.newY} />
      <Metric n={p.done_yesterday} label={m.doneY} tip={m.tip.doneY} />
      <Metric n={p.drafts} label={m.drafts} sub={` · ${m.oldest(p.draft_oldest_days)}`} tip={m.tip.drafts} />
      <Metric n={p.flowing} label={m.flowing} tip={m.tip.flowing} />
      <Metric n={p.open_by_user} label={m.byUser} sub={` · ${m.byAgent(p.open_by_agent)}`} tip={m.tip.by} />
    </div>
  )
}

// 手机：2×2 卡片
export function MetricCards({ p }: { p: Pool }) {
  const m = t.pool.m
  const card = (n: ReactNode, label: ReactNode, tip: string) => (
    <Tip content={tip}>
      <div className="min-w-0 rounded-2xl border border-line bg-surface px-4 py-3 shadow-xs">
        <div className="font-display text-[22px] leading-tight font-semibold text-fg tabular">{n}</div>
        <div className="mt-0.5 truncate text-xs text-fg-3">{label}</div>
      </div>
    </Tip>
  )
  return (
    <div className="grid grid-cols-2 gap-2.5">
      {card(`+${p.new_yesterday} / ${p.done_yesterday}`, m.newDoneShort, `${m.tip.newY}；${m.tip.doneY}`)}
      {card(p.drafts, `${m.drafts} · ${m.oldest(p.draft_oldest_days)}`, m.tip.drafts)}
      {card(p.flowing, m.flowingShort, m.tip.flowing)}
      {card(`${p.open_by_user} / ${p.open_by_agent}`, m.byShort, m.tip.by)}
    </div>
  )
}

// ---------- 需求块 ----------

// 一句话说明：描述首句（去掉 Markdown 记号），不新增字段。
export function firstSentence(md: string, max = 90) {
  const lines = md
    .replace(/```[\s\S]*?```/g, '')
    .split('\n')
    .map((l) =>
      l
        .replace(/^\s*(#{1,6}\s+.*|[-*_]{3,}\s*)$/, '')
        .replace(/^\s*(?:[-*+>]|\d+[.)])\s+/, '')
        .replace(/!\[[^\]]*\]\([^)]*\)/g, '')
        .replace(/\[([^\]]*)\]\([^)]*\)/g, '$1')
        .replace(/[*_`~]+/g, '')
        .trim(),
    )
    .filter(Boolean)
  const s = lines[0] ?? ''
  const m = s.match(/^.*?[。！？!?](?=\s|$|[^。！？!?])/)
  const one = (m ? m[0] : s).trim()
  return one.length > max ? one.slice(0, max) + '…' : one
}

const mmdd = (iso: string) => {
  const d = new Date(iso)
  return `${String(d.getMonth() + 1).padStart(2, '0')}-${String(d.getDate()).padStart(2, '0')}`
}

// 悬停浮层的内容：标题、一句话说明、状态 · 谁记下 · 日期、决策进度。
function Peek({ r }: { r: Requirement }) {
  const full = useRequirement(r.code) // 打开浮层才取：用新建那次状态变更里的身份写「Adv 记下」
  const decs = useDecisions()
  const created = full.data?.status_changes?.find((c) => !c.from)
  const role = r.created_by === 'agent' ? created?.role : ''
  const mine = (decs.data ?? []).filter((d) => d.requirement_id === r.id && (d.kind || 'decision') === 'decision' && d.status !== 'closed')
  const pending = mine.filter((d) => d.status === 'pending').map((d) => d.code)
  const decided = mine.filter((d) => d.status !== 'pending').map((d) => d.code)
  const line = firstSentence(r.detail)
  return (
    <div className="space-y-2">
      <div className="flex flex-wrap items-center gap-1.5 text-xs text-fg-3">
        <RefChip code={r.code} title="" link={false} status={r.status} />
        <span className="font-mono text-[11px] font-semibold text-fg-2">{r.priority}</span>
        <ReqStatus status={r.status} />
        <span>·</span>
        {role && t.role[role] ? (
          <span className="inline-flex items-center gap-1">
            <RoleTag role={role} />
            {t.pool.hover.byRole}
          </span>
        ) : (
          <span>{t.pool.hover.by[r.created_by === 'agent' ? 'agent' : 'user']}</span>
        )}
        <span>·</span>
        <span className="tabular">{mmdd(r.created_at)}</span>
      </div>
      <div className="text-sm leading-snug font-semibold text-fg">{r.title}</div>
      <p className={cx('text-[13px] leading-relaxed', line ? 'text-fg-2' : 'text-fg-3')}>{line || t.pool.hover.noDetail}</p>
      {decided.length > 0 && (
        <div className="flex items-center gap-1 text-xs text-run">
          <Check className="size-3.5" />
          {t.pool.hover.decided(decided.join('、'))}
        </div>
      )}
      {pending.length > 0 && <div className="text-xs text-reply">{t.pool.hover.pending(pending.join('、'))}</div>}
      <div className="border-t border-line pt-2 text-[11px] text-fg-3">{t.pool.hover.hint}</div>
    </div>
  )
}

function HoverPeek({ r, children, off }: { r: Requirement; children: ReactNode; off?: boolean }) {
  if (off) return <>{children}</>
  return (
    <HoverCard.Root openDelay={400} closeDelay={60}>
      <HoverCard.Trigger asChild>{children}</HoverCard.Trigger>
      <HoverCard.Portal>
        <HoverCard.Content side="bottom" align="start" sideOffset={6} collisionPadding={12} className="menu-pop z-50 w-[min(360px,calc(100vw-24px))] rounded-xl border border-line bg-surface p-3.5 shadow-pop">
          <Peek r={r} />
        </HoverCard.Content>
      </HoverCard.Portal>
    </HoverCard.Root>
  )
}

function Ghost({ r }: { r: Requirement }) {
  return (
    <div className="rounded-xl border border-accent/40 bg-surface p-3 text-sm">
      <RefChip code={r.code} title="" status={r.status} link={false} />
      <div className="mt-0.5 line-clamp-2 font-medium text-fg">{r.title}</div>
    </div>
  )
}

// 手机长按：直接给状态菜单（代替拖拽），下面再放打开等常用项
function useStatusMenu() {
  const setStatus = useSetStatus()
  const openReq = useOpenReq()
  return (r: Requirement): CtxItem[] => [
    { type: 'label', label: `${r.code} ${r.title}` },
    ...allStatuses.map((s) => ({ label: t.reqStatus[s], hint: t.reqStatusHint[s], checked: s === r.status, icon: <ReqGlyph status={s} />, onSelect: () => void setStatus(r, s) })),
    { type: 'separator' as const },
    { label: t.menu.openReq, icon: <ChevronRight />, onSelect: () => openReq(r.code) },
  ]
}

function ageText(iso: string, now: number) {
  const m = Math.max(0, Math.floor((now - new Date(iso).getTime()) / 60000))
  if (m < 1) return t.pool.ageNow
  if (m < 60) return t.pool.ageM(m)
  if (m < 1440) return t.pool.ageH(Math.floor(m / 60))
  return t.pool.ageD(Math.floor(m / 1440))
}
const createdAt = (iso: string) => {
  const d = new Date(iso)
  const p = (n: number) => String(n).padStart(2, '0')
  return `${d.getFullYear()}年${d.getMonth() + 1}月${d.getDate()}日 ${p(d.getHours())}:${p(d.getMinutes())}`
}

// Tile：需求块。编号、标记（设计 / 计划）、两行标题；左侧 3px 色条按着色取值；右上角常驻写从创建算起放了多久（3d 前 / 5h 前 / 20m 前 / 刚刚），悬停看创建时间。
export function Tile({ r, faded }: { r: Requirement; faded?: boolean }) {
  const c = usePoolCtx()
  const open = useOpenReq()
  const menu = useReqMenu()
  const statusMenu = useStatusMenu()
  const coarse = useMedia('(pointer: coarse)')
  const now = useNow()
  const drag = !c.phone && c.td ? c.td.source(r.code, <Ghost r={r} />) : null
  const body = (
    <div
      role="button"
      tabIndex={0}
      data-nav-item=""
      onClick={() => open(r.code)}
      onKeyDown={(e) => e.key === 'Enter' && open(r.code)}
      draggable={!coarse}
      onDragStart={(e) => e.dataTransfer.setData('text/ks-req', r.code)}
      {...drag}
      className={cx(
        'group relative flex cursor-pointer flex-col overflow-hidden rounded-xl border border-line bg-surface py-2 pr-2.5 pl-3.5 shadow-xs transition-[transform,box-shadow,opacity] duration-150 outline-none hover:-translate-y-px hover:shadow-card focus-visible:ring-2 focus-visible:ring-accent/50 active:cursor-grabbing',
        c.phone ? 'min-h-[76px]' : 'h-[76px]',
        r.status === 'review' && 'border-reply/40',
        faded && 'opacity-70',
        c.td?.dragging === r.code && 'opacity-40',
      )}
    >
      <span aria-hidden className="absolute inset-y-0 left-0 w-[3px]" style={{ background: c.colorOf(r) }} />
      <div className="flex min-w-0 items-center gap-1.5">
        <RefChip code={r.code} title="" status={r.status} link={false} />
        {r.kind === 'design' && <span className="shrink-0 rounded bg-accent-soft px-1 text-[11px] leading-4 font-medium text-accent">{t.design.label}</span>}
        {r.plan_code && <span className="shrink-0 rounded bg-sunken px-1 font-mono text-[11px] leading-4 font-semibold text-fg-2">{r.plan_code}</span>}
        <span className="ml-auto shrink-0 text-[11px] whitespace-nowrap text-fg-3 tabular" title={t.pool.ageTip(createdAt(r.created_at))}>
          {ageText(r.created_at, now)}
        </span>
      </div>
      <div className={cx('mt-1 text-[13px] leading-[1.35] font-medium text-fg', c.phone ? 'line-clamp-3' : 'line-clamp-2')}>{r.title}</div>
    </div>
  )
  return (
    <Ctx items={c.phone ? statusMenu(r) : menu(r)} className="rounded-xl">
      <HoverPeek r={r} off={coarse}>
        {body}
      </HoverPeek>
    </Ctx>
  )
}

// ---------- 放置区 ----------

function useDrop(status: string) {
  const c = usePoolCtx()
  const [over, setOver] = useState(false)
  return {
    over: over || c.td?.over === status,
    props: {
      'data-drop': status,
      onDragOver: (e: React.DragEvent) => {
        e.preventDefault()
        setOver(true)
      },
      onDragLeave: (e: React.DragEvent) => {
        if (!(e.currentTarget as HTMLElement).contains(e.relatedTarget as Node)) setOver(false)
      },
      onDrop: (e: React.DragEvent) => {
        e.preventDefault()
        setOver(false)
        const code = e.dataTransfer.getData('text/ks-req')
        if (code) c.drop(code, status)
      },
    },
  }
}

// 区域内滚动的内容区：两侧留一点边距，需求块悬停上浮和阴影不被裁掉
const scrollBody = '-mx-1 overflow-y-auto overscroll-contain px-1 py-0.5'

const zone = (over: boolean) => cx('rounded-2xl transition-colors', over ? 'bg-accent-soft ring-2 ring-accent/40' : 'bg-sunken/70')

function ColHead({ status, n, extra, right }: { status: string; n: number; extra?: ReactNode; right?: ReactNode }) {
  return (
    <div className="flex min-h-7 shrink-0 items-center gap-2 px-1 pb-2">
      <ReqGlyph status={status} />
      <span className="text-[13px] font-semibold whitespace-nowrap text-fg">{t.reqStatus[status]}</span>
      <span className="text-xs text-fg-3 tabular">{n}</span>
      {extra && <span className="min-w-0 truncate text-xs text-fg-3">{extra}</span>}
      {right && <span className="ml-auto shrink-0">{right}</span>}
    </div>
  )
}

function EmptyHint({ status, over }: { status: string; over: boolean }) {
  return <div className="rounded-xl border border-dashed border-line-strong/70 px-2 py-4 text-center text-xs text-fg-3">{over ? t.reqs.dropHere : t.pool.emptyCol(t.reqStatus[status])}</div>
}

// NarrowCol：待开始 / 进行中 / 阻塞 / 待验收这样的窄列；空列只占一个提示块高度。
// 内容多时列头（标题和计数）固定在顶部，列内滚动，全部显示、不再截断。
export function NarrowCol({ status, items, className, width }: { status: string; items: Requirement[]; className?: string; width?: number }) {
  const { over, props } = useDrop(status)
  return (
    <section {...props} style={width ? { width } : undefined} className={cx('flex max-h-full min-h-0 min-w-0 flex-col self-start p-2', width && 'shrink-0', zone(over), className)}>
      <ColHead status={status} n={items.length} />
      {items.length ? (
        <div className={cx('flex min-h-0 flex-col gap-2', scrollBody)}>
          {items.map((r) => (
            <Tile key={r.id} r={r} faded={status === 'shelved'} />
          ))}
        </div>
      ) : (
        <EmptyHint status={status} over={over} />
      )}
    </section>
  )
}

// DraftBlock：草稿整块，按 P0～P3 分层，层内多列网格；列头写「等你确认才会排期」和「逐条确认」入口。
export function DraftBlock({ items, cols, onConfirmEach, className }: { items: Requirement[]; cols: number; onConfirmEach: () => void; className?: string; compact?: boolean }) {
  const { over, props } = useDrop('draft')
  return (
    <section {...props} className={cx('flex max-h-full min-h-0 min-w-0 flex-col p-2.5', zone(over), className)}>
      <ColHead
        status="draft"
        n={items.length}
        extra={t.pool.draftHintShort}
        right={
          items.length > 0 && (
            <Button size="sm" icon={<ListChecks className="size-3.5" />} onClick={onConfirmEach}>
              {t.pool.confirmEach}
            </Button>
          )
        }
      />
      {items.length ? (
        <div className={cx('min-h-0 space-y-2.5', scrollBody)}>
          {priorities.map((p) => {
            const layer = items.filter((r) => r.priority === p)
            if (!layer.length) return null
            return (
              <div key={p}>
                <div className="px-1 pb-1.5 font-mono text-[11px] font-semibold text-fg-3">{t.pool.layer(p, layer.length)}</div>
                <div className="grid gap-2" style={{ gridTemplateColumns: `repeat(${cols}, minmax(0, 1fr))` }}>
                  {layer.map((r) => (
                    <Tile key={r.id} r={r} />
                  ))}
                </div>
              </div>
            )
          })}
        </div>
      ) : (
        <EmptyHint status="draft" over={over} />
      )}
    </section>
  )
}

// 近 7 天每日完成柱
function DoneBars({ days }: { days: Schemas['PoolDay'][] }) {
  const max = Math.max(1, ...days.map((d) => d.count))
  return (
    <div className="px-1">
      <div className="flex h-12 items-end gap-1.5 border-b border-line">
        {days.map((d) => (
          <Tip key={d.date} content={t.pool.dayTip(d.date, d.count)}>
            <div className="flex h-full flex-1 items-end">
              <div className="w-full rounded-t-[3px] bg-run/70" style={{ height: d.count ? `${Math.max(8, (d.count / max) * 100)}%` : 2, opacity: d.count ? 1 : 0.35 }} />
            </div>
          </Tip>
        ))}
      </div>
      <div className="mt-1 flex gap-1.5">
        {days.map((d) => (
          <span key={d.date} className="flex-1 text-center text-[10px] text-fg-3 tabular">
            {Number(d.date.slice(8))}
          </span>
        ))}
      </div>
    </div>
  )
}

// 已完成：近 7 天每日完成柱 + 最近 7 条（淡色）+「全部 N 条」。
export function DoneBody({ items, days, onAll, className }: { items: Requirement[]; days: Schemas['PoolDay'][]; onAll: () => void; className?: string }) {
  const open = useOpenReq()
  return (
    <div className={cx('space-y-2.5', className)}>
      <DoneBars days={days} />
      <div className="space-y-0.5">
        {items.slice(0, 7).map((r) => (
          <button key={r.id} onClick={() => open(r.code)} className="flex w-full min-w-0 items-center gap-1.5 rounded-md px-1 py-1 text-left opacity-75 transition hover:bg-hover hover:opacity-100">
            <RefChip code={r.code} title="" status={r.status} link={false} />
            <span className="min-w-0 flex-1 truncate text-xs text-fg-2">{r.title}</span>
          </button>
        ))}
      </div>
      {items.length > 0 && (
        <button onClick={onAll} className="flex items-center gap-0.5 px-1 text-xs text-fg-3 hover:text-fg">
          {t.pool.doneAll(items.length)}
          <ChevronRight className="size-3.5" />
        </button>
      )}
    </div>
  )
}

export function DoneCol({ items, days, onAll, className, width }: { items: Requirement[]; days: Schemas['PoolDay'][]; onAll: () => void; className?: string; width?: number }) {
  const { over, props } = useDrop('done')
  const week = days.reduce((n, d) => n + d.count, 0)
  return (
    <section {...props} style={width ? { width } : undefined} className={cx('flex max-h-full min-h-0 min-w-0 flex-col self-start p-2', width && 'shrink-0', zone(over), className)}>
      <ColHead status="done" n={items.length} extra={t.pool.doneHead(week)} />
      <DoneBody items={items} days={days} onAll={onAll} className={cx('min-h-0', scrollBody)} />
    </section>
  )
}

// 平板：已完成 / 搁置折叠成一行摘要，可展开。
export function FoldRow({ status, n, summary, children }: { status: string; n: number; summary: string; children: ReactNode }) {
  const [open, setOpen] = useState(false)
  const { over, props } = useDrop(status)
  return (
    <section {...props} className={cx('flex max-h-[50%] shrink-0 flex-col p-2', zone(over))}>
      <button onClick={() => setOpen(!open)} aria-expanded={open} className="flex w-full min-w-0 shrink-0 items-center gap-2 px-1 py-1 text-left">
        <ReqGlyph status={status} />
        <span className="text-[13px] font-semibold text-fg">{t.reqStatus[status]}</span>
        <span className="text-xs text-fg-3 tabular">{n}</span>
        <span className="min-w-0 flex-1 truncate pl-2 text-xs text-fg-3">{summary}</span>
        <span className="flex shrink-0 items-center gap-0.5 text-xs text-fg-3">
          {open ? t.pool.collapse : t.pool.expand}
          <ChevronDown className={cx('size-3.5 transition-transform', open && 'rotate-180')} />
        </span>
      </button>
      {open && <div className={cx('min-h-0 pt-2 pb-1', scrollBody)}>{children}</div>}
    </section>
  )
}

// 只看一段时：该状态的需求铺成网格（草稿仍按优先级分层）。
export function StatusGrid({ status, items, width, onConfirmEach, bare }: { status: string; items: Requirement[]; width: number; onConfirmEach: () => void; bare?: boolean }) {
  const cols = Math.max(1, Math.min(6, Math.floor((width + 8) / 228)))
  const { over, props } = useDrop(status)
  if (status === 'draft') return <DraftBlock items={items} cols={cols} onConfirmEach={onConfirmEach} />
  return (
    <section {...(bare ? {} : props)} className={cx('flex max-h-full min-h-0 flex-col', !bare && 'p-2.5', !bare && zone(over))}>
      {!bare && <ColHead status={status} n={items.length} />}
      {items.length ? (
        <div className={cx('grid min-h-0 gap-2', scrollBody)} style={{ gridTemplateColumns: `repeat(${cols}, minmax(0, 1fr))` }}>
          {items.map((r) => (
            <Tile key={r.id} r={r} faded={status === 'done' || status === 'shelved'} />
          ))}
        </div>
      ) : (
        <EmptyHint status={status} over={over} />
      )}
    </section>
  )
}

// 手机：一列需求块，草稿按优先级分层
export function PhoneList({ status, items }: { status: string; items: Requirement[] }) {
  if (!items.length) return <p className="py-12 text-center text-sm text-fg-3">{t.pool.emptyCol(t.reqStatus[status])}</p>
  if (status !== 'draft')
    return (
      <div className="flex flex-col gap-2.5">
        {items.map((r) => (
          <Tile key={r.id} r={r} faded={status === 'done' || status === 'shelved'} />
        ))}
      </div>
    )
  return (
    <div className="space-y-3">
      {priorities.map((p) => {
        const layer = items.filter((r) => r.priority === p)
        if (!layer.length) return null
        return (
          <div key={p}>
            <div className="px-0.5 pb-1.5 font-mono text-xs font-semibold text-fg-3">{t.pool.layer(p, layer.length)}</div>
            <div className="flex flex-col gap-2.5">
              {layer.map((r) => (
                <Tile key={r.id} r={r} />
              ))}
            </div>
          </div>
        )
      })}
    </div>
  )
}
