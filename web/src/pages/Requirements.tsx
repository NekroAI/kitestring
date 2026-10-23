import { useEffect, useMemo, useState } from 'react'
import { ChevronDown, ChevronRight, Columns3, FolderGit2, List, ListPlus, MessageCircleQuestion, Milestone, Plus, Search, User } from 'lucide-react'
import type { Requirement } from '../api/client'
import { useProjects, useRequirements } from '../api/queries'
import { t } from '../i18n'
import { ReqView } from '../app/reqDrawer'
import { DesignBadge } from '../app/design'
import { PlanBadge, PlanEditor, PlansView } from '../app/plans'
import { OwnBadge, useOwnership } from '../app/ownership'
import { Ctx } from '../ui/contextMenu'
import { useReqMenu } from '../app/menus'
import { Collapse, Item, List as MotionList } from '../ui/motion'
import { cx, relTime, useMedia, useNow, usePref } from '../lib/util'
import { useTouchDrag } from '../ui/touchDrag'
import { Button, Card, CardSkeleton, Empty, ErrorState, Input, PageHeader, Segmented, Switch, Tip } from '../ui/base'
import { PriorityDot } from '../ui/marks'
import { Menu } from '../ui/overlay'
import { boardStatuses, closedStatuses, NewReqModal, ReqCard, StatusMenu, statusCls, useSetStatus } from '../app/requirements'

const prio: Record<string, number> = { P0: 0, P1: 1, P2: 2, P3: 3 }
const sortReqs = (a: Requirement, b: Requirement) => (prio[a.priority] ?? 9) - (prio[b.priority] ?? 9) || b.updated_at.localeCompare(a.updated_at)

// useDrop：把元素变成需求卡片的放置区。
function useDrop(status: string, onDrop: (code: string, status: string) => void) {
  const [over, setOver] = useState(false)
  return {
    over,
    props: {
      onDragOver: (e: React.DragEvent) => {
        e.preventDefault()
        setOver(true)
      },
      onDragLeave: () => setOver(false),
      onDrop: (e: React.DragEvent) => {
        e.preventDefault()
        setOver(false)
        const code = e.dataTransfer.getData('text/ks-req')
        if (code) onDrop(code, status)
      },
    },
  }
}

const dragProps = (r: Requirement) => ({ draggable: true, onDragStart: (e: React.DragEvent) => e.dataTransfer.setData('text/ks-req', r.code) })

type TD = ReturnType<typeof useTouchDrag> | null

// 触屏拖拽时跟着手指走的影子
function Ghost({ r }: { r: Requirement }) {
  return (
    <div className="rounded-xl border border-accent/40 bg-surface p-3 text-sm">
      <div className="font-mono text-[11px] font-semibold text-fg-3">{r.code}</div>
      <div className="mt-0.5 line-clamp-2 font-medium text-fg">{r.title}</div>
    </div>
  )
}

// DragCard：桌面用原生拖放；触屏长按浮起后拖动（R-55）
function DragCard({ r, td }: { r: Requirement; td: TD }) {
  if (!td) return <ReqCard r={r} {...dragProps(r)} />
  return (
    <div {...td.source(r.code, <Ghost r={r} />)} className={cx('h-full transition-opacity', td.dragging === r.code && 'opacity-40')}>
      <ReqCard r={r} />
    </div>
  )
}

// 看板列：宽度足够时使用。
function Column({ status, items, onDrop, td }: { status: string; items: Requirement[]; onDrop: (code: string, status: string) => void; td: TD }) {
  const { over: dragOver, props } = useDrop(status, onDrop)
  const over = dragOver || td?.over === status
  return (
    <div
      {...props}
      data-drop={status}
      className={cx('flex min-h-[240px] min-w-0 flex-1 flex-col rounded-2xl p-2 transition-colors', over ? 'bg-accent-soft ring-2 ring-accent/40' : 'bg-sunken/70')}
    >
      <div className="flex items-center gap-2 px-2 pt-1 pb-2.5">
        <span className={cx('size-2 rounded-full', statusCls[status].dot)} />
        <span className="text-[13px] font-semibold text-fg">{t.reqStatus[status]}</span>
        <span className="text-xs text-fg-3 tabular">{items.length}</span>
      </div>
      <MotionList className={cx('flex flex-col', items.length > 0 && 'flex-1')} space={8}>
        {items.map((r) => (
          <Item key={r.id} space={8} layout>
            <DragCard r={r} td={td} />
          </Item>
        ))}
      </MotionList>
      <div className={cx('flex flex-col', !items.length && 'flex-1')}>
        {!items.length && <div className="flex flex-1 items-center justify-center rounded-xl border border-dashed border-line-strong/70 py-6 text-xs text-fg-3">{over ? t.reqs.dropHere : t.reqs.empty}</div>}
      </div>
    </div>
  )
}

// 纵向分组：宽度放不下所有列时使用（竖屏、窄窗口），按状态分段，段内多列网格。
function StatusSection({ status, items, onDrop, td }: { status: string; items: Requirement[]; onDrop: (code: string, status: string) => void; td: TD }) {
  const { over: dragOver, props } = useDrop(status, onDrop)
  const over = dragOver || td?.over === status
  return (
    <section {...props} data-drop={status} className={cx('@container rounded-2xl p-2 transition-colors', over ? 'bg-accent-soft ring-2 ring-accent/40' : '')}>
      <div className="flex items-center gap-2 px-1 pb-2.5">
        <span className={cx('size-2 rounded-full', statusCls[status].dot)} />
        <span className="text-[13px] font-semibold text-fg">{t.reqStatus[status]}</span>
        <span className="text-xs text-fg-3 tabular">{items.length}</span>
      </div>
      {items.length ? (
        <MotionList className="grid grid-cols-1 gap-x-2.5 @md:grid-cols-2 @2xl:grid-cols-3 @5xl:grid-cols-4" space={10}>
          {items.map((r) => (
            <Item key={r.id} space={10} layout className="h-full">
              <DragCard r={r} td={td} />
            </Item>
          ))}
        </MotionList>
      ) : (
        <div className="rounded-xl border border-dashed border-line-strong/70 py-3 text-center text-xs text-fg-3">{over ? t.reqs.dropHere : t.reqs.empty}</div>
      )}
    </section>
  )
}

// 列表视图：按项目分组，组内按状态；点开就地展开详情。
function ReqListRow({ r, expanded, onToggle }: { r: Requirement; expanded: boolean; onToggle: () => void }) {
  const now = useNow()
  const menu = useReqMenu()
  const own = useOwnership()(r)
  return (
    <div className={cx('rounded-xl transition-colors', expanded ? 'bg-surface shadow-card ring-1 ring-line' : 'hover:bg-hover/60')}>
      <Ctx items={menu(r)} className="rounded-xl">
      <div role="button" tabIndex={0} onClick={onToggle} onKeyDown={(e) => e.key === 'Enter' && onToggle()} className="flex cursor-pointer items-center gap-3 px-3 py-2.5">
        <ChevronRight className={cx('size-4 shrink-0 text-fg-3 transition-transform', expanded && 'rotate-90')} />
        <span onClick={(e) => e.stopPropagation()}>
          <StatusMenu r={r} />
        </span>
        <PriorityDot p={r.priority} />
        <span className="w-10 shrink-0 font-mono text-[11px] font-semibold text-fg-3">{r.code}</span>
        <span className="min-w-0 flex-1 truncate text-sm text-fg">{r.title}</span>
        <PlanBadge r={r} className="max-sm:hidden" />
        <DesignBadge r={r} className="max-sm:hidden" />
        {r.pending_decisions > 0 && (
          <Tip content={t.reqs.decisionsTip(r.pending_decisions)}>
            <span className="inline-flex items-center gap-1 text-xs text-wait tabular">
              <MessageCircleQuestion className="size-3.5" />
              {r.pending_decisions}
            </span>
          </Tip>
        )}
        {own && own.tone !== 'locked' && <OwnBadge own={own} className="max-sm:hidden" />}
        {!own && r.next_actor === 'user' && r.status !== 'done' && (
          <Tip content={t.nextActor.user}>
            <span className="inline-flex text-reply">
              <User className="size-3.5" />
            </span>
          </Tip>
        )}
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

function ListView({ items, statuses, projects }: { items: Requirement[]; statuses: string[]; projects: { id: string; name: string }[] }) {
  const [expanded, setExpanded] = useState<string | null>(null)
  const order = new Map(statuses.map((s, i) => [s, i]))
  const groups = new Map<string, Requirement[]>()
  for (const r of items) groups.set(r.project_id || '', [...(groups.get(r.project_id || '') ?? []), r])
  const name = (id: string) => projects.find((p) => p.id === id)?.name || t.reqs.noProject
  return (
    <div className="space-y-7">
      {[...groups.entries()]
        .sort((a, b) => name(a[0]).localeCompare(name(b[0])))
        .map(([pid, rs]) => (
          <section key={pid || 'none'} className="rise">
            <div className="mb-2 flex items-center gap-2.5 px-1">
              <FolderGit2 className="size-4 text-fg-3" />
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

// useWidth：观察元素宽度。
function useWidth() {
  const [el, setEl] = useState<HTMLDivElement | null>(null)
  const [w, setW] = useState(0)
  useEffect(() => {
    if (!el) return
    const ro = new ResizeObserver(([e]) => setW(e.contentRect.width))
    ro.observe(el)
    return () => ro.disconnect()
  }, [el])
  return [setEl, w] as const
}

const COL_MIN = 212 // 每列最小宽度（含间距）；放不下所有列时改为纵向分组

export default function RequirementsPage() {
  const q = useRequirements()
  const projects = useProjects()
  const [measure, width] = useWidth()
  const setStatus = useSetStatus()
  const [project, setProject] = usePref<string>('ks.reqs.project', '')
  const [showClosed, setShowClosed] = usePref('ks.reqs.closed', false)
  const [mobileStatus, setMobileStatus] = usePref<string>('ks.reqs.mstatus', 'in_progress')
  const [view, setView] = usePref<'list' | 'board' | 'plans'>('ks.reqs.view', 'list')
  const [search, setSearch] = useState('')
  const [creating, setCreating] = useState(false)
  const [creatingPlan, setCreatingPlan] = useState(false)
  const newItem = () => (view === 'plans' ? setCreatingPlan(true) : setCreating(true))

  const all = q.data ?? []
  const filtered = useMemo(() => {
    const k = search.trim().toLowerCase()
    return all.filter((r) => (!project || r.project_id === project) && (!k || r.title.toLowerCase().includes(k) || r.code.toLowerCase().includes(k)))
  }, [all, project, search])
  const statuses = showClosed ? [...boardStatuses, ...closedStatuses] : [...boardStatuses]
  const byStatus = (s: string) => filtered.filter((r) => r.status === s).sort(sortReqs)
  const open = all.filter((r) => r.status !== 'done' && r.status !== 'shelved').length
  const projName = projects.data?.projects.find((p) => p.id === project)?.name

  const drop = (code: string, status: string) => {
    const r = all.find((x) => x.code === code)
    if (r && r.status !== status) void setStatus(r, status)
  }
  const coarse = useMedia('(pointer: coarse)')
  const touch = useTouchDrag(drop)
  const td = coarse ? touch : null

  return (
    <div ref={measure}>
      <PageHeader
        title={t.reqs.title}
        subtitle={q.isPending ? ' ' : t.reqs.summary(open)}
        actions={
          <Button variant="primary" icon={<Plus className="size-4" />} onClick={newItem}>
            {view === 'plans' ? t.plan.new : t.reqs.new}
          </Button>
        }
      />

      <div className="mb-5 flex flex-wrap items-center gap-2">
        <Menu
          align="start"
          trigger={
            <Button>
              <span className="max-w-[180px] truncate">{projName ?? t.reqs.allProjects}</span>
              <ChevronDown className="size-3.5 opacity-60" />
            </Button>
          }
          items={[{ label: t.reqs.allProjects, checked: !project, onSelect: () => setProject('') }, ...(projects.data?.projects ?? []).map((p) => ({ label: p.name, hint: p.remote, checked: p.id === project, onSelect: () => setProject(p.id) }))]}
        />
        <div className="relative w-full sm:w-56">
          <Search className="pointer-events-none absolute top-1/2 left-3 size-4 -translate-y-1/2 text-fg-3" />
          <Input value={search} onChange={(e) => setSearch(e.target.value)} placeholder={t.reqs.search} className="pl-9" />
        </div>
        <Segmented
          size="sm"
          value={view}
          onChange={setView}
          options={[
            { value: 'list', label: <List className="size-4" />, tip: t.reqs.viewList },
            { value: 'board', label: <Columns3 className="size-4" />, tip: t.reqs.viewBoard },
            { value: 'plans', label: <Milestone className="size-4" />, tip: t.plan.view },
          ]}
        />
        <label className="ml-auto flex items-center gap-2 text-sm text-fg-2">
          <Switch checked={showClosed} onChange={setShowClosed} />
          {t.reqs.showClosed}
        </label>
      </div>

      {q.isError ? (
        <ErrorState error={q.error} onRetry={() => q.refetch()} />
      ) : q.isPending ? (
        <CardSkeleton rows={2} />
      ) : view === 'plans' ? (
        <PlansView project={project || undefined} showClosed={showClosed} onNew={() => setCreatingPlan(true)} />
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
      ) : view === 'list' ? (
        <ListView items={filtered.filter((r) => (statuses as string[]).includes(r.status))} statuses={statuses} projects={projects.data?.projects ?? []} />
      ) : width >= statuses.length * COL_MIN ? (
        <div className="flex gap-3 pb-4">
          {statuses.map((s) => (
            <Column key={s} status={s} items={byStatus(s)} onDrop={drop} td={td} />
          ))}
        </div>
      ) : width >= 600 ? (
        <div className="space-y-4">
          {statuses.map((s) => (
            <StatusSection key={s} status={s} items={byStatus(s)} onDrop={drop} td={td} />
          ))}
        </div>
      ) : (
        <>
          <div className="-mx-4 mb-4 overflow-x-auto px-4">
            <Segmented id="req-mobile" size="sm" value={mobileStatus} onChange={setMobileStatus} dropTargets={coarse} over={td?.over} options={statuses.map((s) => ({ value: s, label: t.reqStatus[s], count: byStatus(s).length }))} />
          </div>
          <MotionList space={10}>
            {byStatus(mobileStatus).map((r) => (
              <Item key={r.id} space={10}>
                <DragCard r={r} td={td} />
              </Item>
            ))}
          </MotionList>
          <div>
            {!byStatus(mobileStatus).length && <p className="py-10 text-center text-sm text-fg-3">{t.reqs.empty}</p>}
          </div>
        </>
      )}

      {td?.layer}
      <NewReqModal open={creating} onClose={() => setCreating(false)} defaultProject={project || undefined} />
      <PlanEditor open={creatingPlan} onClose={() => setCreatingPlan(false)} defaultProject={project || undefined} />
    </div>
  )
}
