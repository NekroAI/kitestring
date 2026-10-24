import { useEffect, useMemo, useRef, useState } from 'react'
import { ChevronDown, ChevronRight, LayoutGrid, List, ListPlus, MessageCircleQuestion, MoreHorizontal, Plus, Search, X } from 'lucide-react'
import type { Requirement } from '../api/client'
import { usePlans, usePool, useProjects, useRequirements } from '../api/queries'
import { t } from '../i18n'
import { ReqView } from '../app/reqDrawer'
import { DesignBadge } from '../app/design'
import { PlanBadge, PlanEditor, PlansView, needsApproval } from '../app/plans'
import { OwnBadge, useOwnership } from '../app/ownership'
import { Ctx } from '../ui/contextMenu'
import { useReqMenu } from '../app/menus'
import { useOpenDraftQueue } from '../app/nav'
import { Collapse, Item, List as MotionList } from '../ui/motion'
import { cx, relTime, useMedia, useNow, usePref } from '../lib/util'
import { useTouchDrag } from '../ui/touchDrag'
import { Button, Card, CardSkeleton, Empty, ErrorState, Input, PageHeader, Segmented, Switch, Tip } from '../ui/base'
import { PriorityTag } from '../ui/marks'
import { Menu, type MenuItem } from '../ui/overlay'
import { allStatuses, NewReqModal, ReleaseBadge, StatusMenu, useSetStatus } from '../app/requirements'
import { PlanGlyph, ProjGlyph, RefChip, ReqGlyph, TurnSeal } from '../ui/refmark'
import { nextView, useKsEvent } from '../app/command'
import {
  catColor,
  DistBar,
  DoneBody,
  DoneCol,
  DraftBlock,
  FoldRow,
  flowStatuses,
  Legend,
  MetricCards,
  Metrics,
  NarrowCol,
  openStatuses,
  PhoneList,
  PoolProvider,
  sortDrafts,
  sortRecent,
  sortReqs,
  StatusGrid,
  useColorOf,
  useFillHeight,
  useWidth,
  type ColorBy,
  type Pool,
} from '../app/pool'

// 需求页 = 需求池（R-133）：以需求为单位、跨项目看全量，回答「池子里有什么、堆在哪、在不在流动」。
// 默认视图是盘面；列表、计划保留为次级视图。

type View = 'pool' | 'list' | 'plans'

function ViewLabel({ icon, text }: { icon: React.ReactNode; text: string }) {
  return (
    <span className="inline-flex items-center gap-1.5">
      {icon}
      <span className="text-[13px]">{text}</span>
    </span>
  )
}

// ---------- 列表视图 ----------

// 按项目分组，组内按状态；点开就地展开详情。
function ReqListRow({ r, expanded, onToggle }: { r: Requirement; expanded: boolean; onToggle: () => void }) {
  const now = useNow()
  const menu = useReqMenu()
  const own = useOwnership()(r)
  return (
    <div className={cx('rounded-xl transition-colors', expanded ? 'bg-surface shadow-card ring-1 ring-line' : 'hover:bg-hover/60')}>
      <Ctx items={menu(r)} className="rounded-xl">
        <div role="button" tabIndex={0} data-nav-item="" onClick={onToggle} onKeyDown={(e) => e.key === 'Enter' && onToggle()} className="flex cursor-pointer items-center gap-3 px-3 py-2.5">
          <ChevronRight className={cx('size-4 shrink-0 text-fg-3 transition-transform', expanded && 'rotate-90')} />
          <span onClick={(e) => e.stopPropagation()}>
            <StatusMenu r={r} />
          </span>
          <PriorityTag p={r.priority} />
          <span className="w-[68px] shrink-0">
            <RefChip code={r.code} status={r.status} link={false} />
          </span>
          <span className="min-w-0 flex-1 truncate text-sm text-fg">{r.title}</span>
          <PlanBadge r={r} className="max-sm:hidden" />
          <DesignBadge r={r} className="max-sm:hidden" />
          <ReleaseBadge r={r} className="max-sm:hidden" />
          {r.pending_decisions > 0 && (
            <Tip content={t.reqs.decisionsTip(r.pending_decisions)}>
              <span className="inline-flex items-center gap-1 text-xs text-wait tabular">
                <MessageCircleQuestion className="size-3.5" />
                {r.pending_decisions}
              </span>
            </Tip>
          )}
          {/* 草稿左边的状态已经写着「草稿」，右边不再重复「待你确认」（R-129） */}
          {own && own.tone !== 'locked' && own.tone !== 'draft' && <OwnBadge own={own} className="max-sm:hidden" />}
          {!own && r.next_actor === 'user' && r.status !== 'done' && <TurnSeal />}
          <span className="hidden w-20 shrink-0 text-right text-xs text-fg-3 sm:block">{relTime(r.updated_at, now)}</span>
        </div>
      </Ctx>
      <Collapse show={expanded}>
        <div className="flex max-h-[min(680px,78dvh)] flex-col border-t border-line">
          <ReqView refId={r.code} />
        </div>
      </Collapse>
    </div>
  )
}

function ListView({ items, projects }: { items: Requirement[]; projects: { id: string; name: string }[] }) {
  const [expanded, setExpanded] = useState<string | null>(null)
  const order = new Map(allStatuses.map((s, i) => [s as string, i]))
  const groups = new Map<string, Requirement[]>()
  for (const r of items) groups.set(r.project_id || '', [...(groups.get(r.project_id || '') ?? []), r])
  const name = (id: string) => projects.find((p) => p.id === id)?.name || t.reqs.noProject
  if (!items.length) return <p className="py-12 text-center text-sm text-fg-3">{t.pool.noMatch}</p>
  return (
    <div className="space-y-7">
      {[...groups.entries()]
        .sort((a, b) => name(a[0]).localeCompare(name(b[0])))
        .map(([pid, rs]) => (
          <section key={pid || 'none'} className="rise">
            <div className="mb-2 flex items-center gap-2.5 px-1">
              <ProjGlyph className="size-4" />
              <h2 className="font-display text-[17px] font-semibold text-fg">{name(pid)}</h2>
              <span className="text-xs text-fg-3 tabular">{rs.length}</span>
            </div>
            <Card className="p-1.5">
              <MotionList space={2}>
                {rs
                  .sort((a, b) => (order.get(a.status) ?? 9) - (order.get(b.status) ?? 9) || sortReqs(a, b))
                  .map((r) => (
                    <Item key={r.id} space={2} layout>
                      <ReqListRow r={r} expanded={expanded === r.code} onToggle={() => setExpanded(expanded === r.code ? null : r.code)} />
                    </Item>
                  ))}
              </MotionList>
            </Card>
          </section>
        ))}
    </div>
  )
}

// ---------- 盘面 ----------

type BoardProps = {
  width: number
  pool: Pool
  items: Requirement[] // 已按项目、搜索筛过
  shelved: boolean
  active: string | null
  onAll: (status: string) => void
    onConfirmEach: () => void
}

const WIDE = 1240 // 内容区至少这么宽才横排：草稿列占剩余宽度，其余状态是窄列
const PHONE = 700 // 内容区窄于此用手机布局

function Board({ width, pool, items, shelved, active, onAll, onConfirmEach }: BoardProps) {
  const since = useMemo(() => new Map(pool.items.map((x) => [x.code, x])), [pool.items])
  const by = (s: string) => {
    const list = items.filter((r) => r.status === s)
    return list.sort(s === 'draft' ? sortDrafts : s === 'done' || s === 'shelved' ? sortRecent(since) : sortReqs)
  }
  const done = by('done')
  const week = pool.done_daily.reduce((n, d) => n + d.count, 0)

  if (active) return <div className="flex h-full"><StatusGrid status={active} items={by(active)} width={width} onConfirmEach={onConfirmEach} /></div>

  if (width >= WIDE) {
    // 越宽越能用满：窄列随宽度放宽、能显示的条数增加，多出来的宽度全给草稿列（列数随之增加，需求块保持 ~230px 宽）
    const narrow = width >= 2600 ? 248 : width >= 2000 ? 216 : width >= 1560 ? 176 : 156
    const doneW = width >= 2600 ? 320 : width >= 2000 ? 280 : width >= 1560 ? 244 : 212
    const cols = flowStatuses.length + (shelved ? 1 : 0)
    const draftW = width - narrow * cols - doneW - 12 * (cols + 1)
    return (
      <div className="flex h-full items-start gap-3">
        <DraftBlock items={by('draft')} cols={Math.max(2, Math.min(12, Math.floor((draftW - 12) / 230)))} onConfirmEach={onConfirmEach} className="flex-1" compact={draftW < 560} />
        {flowStatuses.map((s) => (
          <NarrowCol key={s} status={s} items={by(s)} width={narrow} />
        ))}
        <DoneCol items={done} days={pool.done_daily} onAll={() => onAll('done')} width={doneW} />
        {shelved && <NarrowCol status="shelved" items={by('shelved')} width={narrow} />}
      </div>
    )
  }
  // 纵向：先「在流转」一行四列，再草稿整块，最后已完成（与搁置）折叠成一行摘要
  const flowing = flowStatuses.reduce((n, s) => n + by(s).length, 0)
  const sh = by('shelved')
  return (
    <div className="flex h-full flex-col gap-4">
      <section className="flex max-h-[40%] shrink-0 flex-col">
        <div className="mb-2 flex shrink-0 items-center gap-2 px-1">
          <span className="text-[13px] font-semibold text-fg">{t.pool.flowing}</span>
          <span className="text-xs text-fg-3 tabular">{flowing}</span>
        </div>
        <div className="grid min-h-0 grid-cols-4 grid-rows-[minmax(0,1fr)] gap-3">
          {flowStatuses.map((s) => (
            <NarrowCol key={s} status={s} items={by(s)} fill />
          ))}
        </div>
      </section>
      <DraftBlock items={by('draft')} cols={width >= 1000 ? 4 : width >= 760 ? 3 : 2} onConfirmEach={onConfirmEach} compact={width < 900} className="shrink" />
      <FoldRow status="done" n={done.length} summary={t.pool.doneSummary(week, pool.done_yesterday)}>
        <DoneBody items={done} days={pool.done_daily} onAll={() => onAll('done')} />
      </FoldRow>
      {shelved && (
        <FoldRow status="shelved" n={sh.length} summary={t.pool.shelvedSummary(sh.length)}>
          <StatusGrid status="shelved" items={sh} width={width - 24} onConfirmEach={onConfirmEach} bare />
        </FoldRow>
      )}
    </div>
  )
}

// 手机：状态胶囊横滑 + 左右滑切换，需求块单列全宽
function PhoneBoard({ items, statuses, status, setStatus, pool }: { items: Requirement[]; statuses: string[]; status: string; setStatus: (s: string) => void; pool: Pool }) {
  const since = useMemo(() => new Map(pool.items.map((x) => [x.code, x])), [pool.items])
  const count = (s: string) => items.filter((r) => r.status === s).length
  const list = items.filter((r) => r.status === status).sort(status === 'draft' ? sortDrafts : status === 'done' || status === 'shelved' ? sortRecent(since) : sortReqs)
  const strip = useRef<HTMLDivElement>(null)
  const touch = useRef<{ x: number; y: number } | null>(null)
  useEffect(() => {
    const el = strip.current?.querySelector<HTMLElement>(`[data-status="${status}"]`)
    const box = strip.current
    if (el && box) box.scrollTo({ left: el.offsetLeft - box.clientWidth / 2 + el.clientWidth / 2, behavior: 'smooth' })
  }, [status])
  const step = (d: number) => {
    const i = statuses.indexOf(status) + d
    if (i >= 0 && i < statuses.length) setStatus(statuses[i])
  }
  return (
    <div>
      <div ref={strip} className="no-scrollbar relative -mx-4 flex gap-2 overflow-x-auto px-4 pb-1">
        {statuses.map((s) => {
          const on = s === status
          return (
            <button
              key={s}
              data-status={s}
              onClick={() => setStatus(s)}
              aria-pressed={on}
              className={cx('flex h-9 shrink-0 items-center gap-1.5 rounded-full border px-3.5 text-sm font-medium transition-colors', on ? 'border-transparent bg-fg text-bg' : 'border-line-strong text-fg-2')}
            >
              {t.reqStatus[s]}
              <span className={cx('tabular', on ? 'opacity-80' : 'text-fg-3')}>{count(s)}</span>
            </button>
          )
        })}
      </div>
      <p className="mt-2 mb-3 text-xs text-fg-3">{t.pool.mobileHint}</p>
      <div
        className="min-h-[40vh] pb-[calc(5rem+env(safe-area-inset-bottom))]"
        onTouchStart={(e) => (touch.current = { x: e.touches[0].clientX, y: e.touches[0].clientY })}
        onTouchEnd={(e) => {
          const s = touch.current
          touch.current = null
          if (!s) return
          const dx = e.changedTouches[0].clientX - s.x
          const dy = e.changedTouches[0].clientY - s.y
          if (Math.abs(dx) > 64 && Math.abs(dx) > Math.abs(dy) * 1.6) step(dx < 0 ? 1 : -1)
        }}
      >
        <PhoneList status={status} items={list} />
      </div>
    </div>
  )
}

// ---------- 页面 ----------

export default function RequirementsPage() {
  const q = useRequirements()
  const projects = useProjects()
  const plans = usePlans()
  const [measure, width] = useWidth()
  const [rootEl, setRootEl] = useState<HTMLElement | null>(null)
  const setStatus = useSetStatus()
  const openQueue = useOpenDraftQueue()
  const [project, setProject] = usePref<string>('ks.reqs.project', '')
  const [shelved, setShelved] = usePref('ks.reqs.shelved', false)
  const [closedPlans, setClosedPlans] = usePref('ks.reqs.closedPlans', false)
  // 新键：盘面上线后默认视图改为盘面；旧的「看板」偏好不再存在，一律当盘面
  const [rawView, setView] = usePref<View | 'board'>('ks.reqs.view2', 'pool')
  const view: View = rawView === 'board' ? 'pool' : rawView
  const [colorBy, setColorBy] = usePref<ColorBy>('ks.reqs.color', 'priority')
  const [active, setActive] = useState<string | null>(null) // 分布条上点中的一段
  const [search, setSearch] = useState('')
  const [phoneSearch, setPhoneSearch] = useState(false)
  const [creating, setCreating] = useState(false)
  const [creatingPlan, setCreatingPlan] = useState(false)
  const searchRef = useRef<HTMLInputElement>(null)
  const pool = usePool(project || undefined)
  const phone = width > 0 && width < PHONE
  // 桌面 / 平板的盘面占满视口剩余高度，各区域内滚动；手机维持整页纵向滚动
  const fillH = useFillHeight(rootEl, view === 'pool' && !phone && width > 0)
  const newItem = () => (view === 'plans' ? setCreatingPlan(true) : setCreating(true))

  // V / 命令面板切换视图（R-127）
  useKsEvent('ks:reqs-view', (v) => setView((v === 'next' ? nextView(view) : v) as View))

  // / 聚焦搜索
  useEffect(() => {
    const onKey = (e: KeyboardEvent) => {
      if (e.key !== '/' || e.metaKey || e.ctrlKey || e.altKey) return
      const el = e.target as HTMLElement
      if (el.closest('input, textarea, [contenteditable=true], [role=menu], [role=dialog]')) return
      e.preventDefault()
      if (phone) setPhoneSearch(true)
      window.setTimeout(() => searchRef.current?.focus(), 0)
    }
    window.addEventListener('keydown', onKey)
    return () => window.removeEventListener('keydown', onKey)
  }, [phone])

  const all = useMemo(() => q.data ?? [], [q.data])
  const projList = useMemo(() => projects.data?.projects ?? [], [projects.data])
  const filtered = useMemo(() => {
    const k = search.trim().toLowerCase()
    return all.filter((r) => (!project || r.project_id === project) && (!k || r.title.toLowerCase().includes(k) || r.code.toLowerCase().includes(k)))
  }, [all, project, search])
  const projName = projList.find((p) => p.id === project)?.name

  // 着色：项目按名称排序、计划按编号排序后分色
  const projIds = useMemo(() => [...projList].sort((a, b) => a.name.localeCompare(b.name)).map((p) => p.id), [projList])
  const planCodes = useMemo(() => [...new Set(all.map((r) => r.plan_code).filter(Boolean) as string[])].sort((a, b) => Number(a.slice(2)) - Number(b.slice(2))), [all])
  const colorOf = useColorOf(colorBy, projIds, planCodes)
  const legend = useMemo(() => {
    if (colorBy === 'priority') return []
    const shown = filtered.filter((r) => r.status !== 'done' && r.status !== 'shelved')
    if (colorBy === 'project') {
      const ids = new Set(shown.map((r) => r.project_id).filter(Boolean))
      return projIds.filter((id) => ids.has(id)).map((id) => ({ key: id, label: projList.find((p) => p.id === id)?.name ?? id, color: catColor(projIds.indexOf(id)) }))
    }
    const codes = new Set(shown.map((r) => r.plan_code).filter(Boolean))
    const title = (c: string) => (plans.data ?? []).find((x) => x.code === c)?.title
    return [...planCodes.filter((c) => codes.has(c)).map((c) => ({ key: c, label: [c, title(c)].filter(Boolean).join(' '), color: catColor(planCodes.indexOf(c)) })), { key: '-', label: t.pool.noPlan, color: 'var(--line-strong)' }]
  }, [colorBy, filtered, projIds, projList, planCodes, plans.data])

  const drop = (code: string, status: string) => {
    const r = all.find((x) => x.code === code)
    if (r && r.status !== status) void setStatus(r, status)
  }
  const coarse = useMedia('(pointer: coarse)')
  const touch = useTouchDrag(drop)
  const td = coarse && !phone ? touch : null

  // 手机上的状态：默认停在数量最多的未完成状态
  const phoneStatuses = useMemo(() => [...openStatuses, 'done', ...(shelved ? ['shelved'] : [])], [shelved])
  const [mStatus, setMStatus] = useState<string | null>(null)
  const autoStatus = useMemo(() => [...openStatuses].sort((a, b) => filtered.filter((r) => r.status === b).length - filtered.filter((r) => r.status === a).length)[0], [filtered])
  const phoneStatus = mStatus && phoneStatuses.includes(mStatus) ? mStatus : autoStatus

  // 计划视图的副标题说计划（R-131）：几个在推进、几个等你批准
  const planSummary = useMemo(() => {
    const list = (plans.data ?? []).filter((x) => !project || x.project_id === project)
    return t.plan.countLine(list.filter((x) => x.status === 'active').length, list.filter(needsApproval).length)
  }, [plans.data, project])

  const since = useMemo(() => new Map((pool.data?.items ?? []).map((x) => [x.code, x])), [pool.data])
  const showAll = (status: string) => {
    setActive(status)
    setView('list')
  }
  const confirmEach = () => {
    const first = filtered.filter((r) => r.status === 'draft').sort(sortDrafts)[0]
    if (first) openQueue(first.code, project)
  }
  const pickSeg = (s: string | null) => {
    if (phone) {
      if (s) setMStatus(s)
      return
    }
    setActive(s)
  }
  // 列表视图：只看点中的一段；没点时看未关闭的（包含搁置时再加搁置）
  const listItems = filtered.filter((r) => (active ? r.status === active : r.status !== 'done' && (shelved || r.status !== 'shelved')))

  const projectMenu = (trigger: React.ReactNode) => (
    <Menu
      align="start"
      trigger={trigger}
      items={[{ label: t.reqs.allProjects, checked: !project, onSelect: () => setProject('') }, ...projList.filter((p) => !p.hidden || p.id === project).map((p) => ({ label: p.name, hint: p.remote, checked: p.id === project, onSelect: () => setProject(p.id) }))]}
    />
  )
  const colors: ColorBy[] = ['priority', 'project', 'plan']
  const colorItems: MenuItem[] = colors.map((c) => ({ label: t.pool.color[c], checked: colorBy === c, onSelect: () => setColorBy(c) }))
  const viewOptions: { value: View; label: React.ReactNode; text: string }[] = [
    { value: 'pool', text: t.pool.view, label: <ViewLabel icon={<LayoutGrid className="size-4" />} text={t.pool.view} /> },
    { value: 'list', text: t.reqs.viewList, label: <ViewLabel icon={<List className="size-4" />} text={t.reqs.viewList} /> },
    // 图标旁直接写字（R-131）
    { value: 'plans', text: t.plan.view, label: <ViewLabel icon={<PlanGlyph className="size-4 text-current" />} text={t.plan.view} /> },
  ]
  const searchBox = (
    <div className="relative w-full sm:w-60">
      <Search className="pointer-events-none absolute top-1/2 left-3 size-4 -translate-y-1/2 text-fg-3" />
      <Input ref={searchRef} value={search} onChange={(e) => setSearch(e.target.value)} placeholder={t.reqs.search} aria-label={t.reqs.search} className="pr-8 pl-9" onKeyDown={(e) => e.key === 'Escape' && (e.target as HTMLInputElement).blur()} />
      {!search && !coarse && (
        <kbd className="absolute top-1/2 right-2.5 -translate-y-1/2 rounded border border-line px-1.5 font-mono text-[11px] text-fg-3">/</kbd>
      )}
      {search && (
        <button aria-label={t.common.close} onClick={() => setSearch('')} className="absolute top-1/2 right-2 -translate-y-1/2 rounded p-0.5 text-fg-3 hover:text-fg">
          <X className="size-3.5" />
        </button>
      )}
    </div>
  )
  const subtitle = q.isPending ? ' ' : view === 'plans' ? planSummary : projName ? t.pool.subtitleProject(projName) : t.pool.subtitle
  const p = pool.data

  return (
    <div
      ref={(el) => {
        measure(el)
        setRootEl(el)
      }}
      className={fillH ? 'flex flex-col' : undefined}
      style={fillH ? { height: fillH } : undefined}
    >
      {phone ? (
        <div className="mb-4">
          <div className="flex items-center gap-1">
            <h1 className="flex-1 font-display text-[26px] font-semibold tracking-wide text-fg">{view === 'plans' ? t.plan.view : t.pool.title}</h1>
            <Button variant="ghost" size="icon" aria-label={t.reqs.search} onClick={() => setPhoneSearch(!phoneSearch)}>
              <Search className="size-[18px]" />
            </Button>
            <Menu
              trigger={
                <Button variant="ghost" size="icon" aria-label={t.common.more}>
                  <MoreHorizontal className="size-[18px]" />
                </Button>
              }
              items={[
                ...viewOptions.map((o) => ({ label: o.text, checked: view === o.value, onSelect: () => setView(o.value) })),
                { type: 'separator' as const },
                view === 'plans'
                  ? { label: t.pool.closedPlans, checked: closedPlans, onSelect: () => setClosedPlans(!closedPlans) }
                  : { label: t.pool.shelved, checked: shelved, onSelect: () => setShelved(!shelved) },
              ]}
            />
          </div>
          <div className="mt-0.5 flex min-w-0 items-center gap-1.5 text-sm text-fg-2">
            {projectMenu(
              <button className="inline-flex min-w-0 items-center gap-0.5">
                <span className="max-w-[150px] truncate">{projName ?? t.reqs.allProjects}</span>
                <ChevronDown className="size-3.5 opacity-60" />
              </button>,
            )}
            {view === 'pool' && (
              <>
                <span className="text-fg-3">·</span>
                <Menu
                  align="start"
                  trigger={
                    <button className="inline-flex items-center gap-0.5">
                      {t.pool.colorShort(t.pool.color[colorBy])}
                      <ChevronDown className="size-3.5 opacity-60" />
                    </button>
                  }
                  items={colorItems}
                />
              </>
            )}
          </div>
          {phoneSearch && <div className="mt-3">{searchBox}</div>}
        </div>
      ) : (
        <>
          <PageHeader
            title={view === 'plans' ? t.plan.view : t.pool.title}
            subtitle={subtitle}
            actions={
              <Button variant="primary" icon={<Plus className="size-4" />} onClick={newItem}>
                {view === 'plans' ? t.plan.new : t.reqs.new}
              </Button>
            }
          />
          <div className="mb-4 flex flex-wrap items-center gap-2">
            {projectMenu(
              <Button>
                <span className="max-w-[180px] truncate">{projName ?? t.reqs.allProjects}</span>
                <ChevronDown className="size-3.5 opacity-60" />
              </Button>,
            )}
            {searchBox}
            <Segmented size="sm" value={view} onChange={setView} options={viewOptions.map(({ value, label }) => ({ value, label }))} />
            {view === 'pool' && (
              <span className="flex items-center gap-1.5">
                <span className="pl-1 text-[13px] text-fg-3">{t.pool.colorBy}</span>
                <Segmented size="sm" value={colorBy} onChange={setColorBy} options={colors.map((c) => ({ value: c, label: <span className="text-[13px]">{t.pool.color[c]}</span> }))} />
              </span>
            )}
            <label className="ml-auto flex items-center gap-2 text-sm whitespace-nowrap text-fg-2">
              {view === 'plans' ? (
                <>
                  <Switch checked={closedPlans} onChange={setClosedPlans} />
                  {t.pool.closedPlans}
                </>
              ) : (
                <>
                  <Switch checked={shelved} onChange={setShelved} />
                  {t.pool.shelved}
                </>
              )}
            </label>
          </div>
        </>
      )}

      {q.isError ? (
        <ErrorState error={q.error} onRetry={() => q.refetch()} />
      ) : q.isPending || !width ? (
        <CardSkeleton rows={2} />
      ) : view === 'plans' ? (
        <PlansView project={project || undefined} showClosed={closedPlans} onNew={() => setCreatingPlan(true)} />
      ) : !all.length ? (
        <Card>
          <Empty
            icon={<ListPlus />}
            title={t.reqs.emptyAll}
            action={
              <Button variant="primary" icon={<Plus className="size-4" />} onClick={() => setCreating(true)}>
                {t.reqs.new}
              </Button>
            }
          >
            {t.reqs.emptyAllSub}
          </Empty>
        </Card>
      ) : (
        <>
          {p && (
            <div className={cx('space-y-3', phone ? 'mb-4' : 'mb-5')}>
              <DistBar counts={p.counts} statuses={allStatuses} active={phone ? null : active} onPick={pickSeg} thin={phone} />
              {phone ? (
                <>
                  <p className="text-[13px] text-fg-3 tabular">{t.pool.summaryLine(p.drafts, p.flowing, p.counts.done ?? 0, p.counts.shelved ?? 0)}</p>
                  {view === 'pool' && <MetricCards p={p} />}
                </>
              ) : (
                <Metrics p={p} />
              )}
              {!phone && active && (
                <div className="flex items-center gap-2 text-[13px] text-fg-2">
                  <ReqGlyph status={active} />
                  {t.pool.filtered(t.reqStatus[active])}
                  <button onClick={() => setActive(null)} className="text-accent hover:underline">
                    {t.pool.clearFilter}
                  </button>
                </div>
              )}
              {view === 'pool' && <Legend items={legend} />}
            </div>
          )}
          {view === 'list' ? (
            <ListView items={listItems} projects={projList} />
          ) : (
            p && (
              <PoolProvider value={{ colorOf, since, td, phone, drop }}>
                {phone ? (
                  <PhoneBoard items={filtered} statuses={phoneStatuses} status={phoneStatus} setStatus={setMStatus} pool={p} />
                ) : (
                  <div className={fillH ? 'min-h-[260px] flex-1' : undefined}>
                  <Board width={width} pool={p} items={filtered} shelved={shelved} active={active} onAll={showAll} onConfirmEach={confirmEach} />
                  </div>
                )}
              </PoolProvider>
            )
          )}
        </>
      )}

      {phone && view !== 'plans' && (
        <button
          aria-label={t.reqs.new}
          onClick={newItem}
          className="fixed right-4 bottom-[calc(4.75rem+env(safe-area-inset-bottom))] z-20 flex size-14 items-center justify-center rounded-full bg-seal text-seal-fg shadow-pop transition active:scale-95"
        >
          <Plus className="size-6" />
        </button>
      )}
      {td?.layer}
      <NewReqModal open={creating} onClose={() => setCreating(false)} defaultProject={project || undefined} />
      <PlanEditor open={creatingPlan} onClose={() => setCreatingPlan(false)} defaultProject={project || undefined} />
    </div>
  )
}
