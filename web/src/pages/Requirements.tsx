import { useMemo, useState } from 'react'
import { ChevronDown, ListPlus, Plus, Search } from 'lucide-react'
import type { Requirement } from '../api/client'
import { useProjects, useRequirements } from '../api/queries'
import { t } from '../i18n'
import { cx, useDesktop, usePref } from '../lib/util'
import { Button, Card, CardSkeleton, Empty, ErrorState, Input, PageHeader, Segmented, Switch } from '../ui/base'
import { Menu } from '../ui/overlay'
import { boardStatuses, closedStatuses, NewReqModal, ReqCard, statusCls, useSetStatus } from '../app/requirements'

const prio: Record<string, number> = { P0: 0, P1: 1, P2: 2, P3: 3 }
const sortReqs = (a: Requirement, b: Requirement) => (prio[a.priority] ?? 9) - (prio[b.priority] ?? 9) || b.updated_at.localeCompare(a.updated_at)

function Column({ status, items, onDrop }: { status: string; items: Requirement[]; onDrop: (code: string, status: string) => void }) {
  const [over, setOver] = useState(false)
  return (
    <div
      onDragOver={(e) => {
        e.preventDefault()
        setOver(true)
      }}
      onDragLeave={() => setOver(false)}
      onDrop={(e) => {
        e.preventDefault()
        setOver(false)
        const code = e.dataTransfer.getData('text/ks-req')
        if (code) onDrop(code, status)
      }}
      className={cx('flex min-h-[200px] w-[260px] shrink-0 flex-col rounded-2xl p-2 transition-colors xl:w-auto xl:min-w-[200px] xl:flex-1 xl:shrink', over ? 'bg-accent-soft ring-2 ring-accent/40' : 'bg-sunken/70')}
    >
      <div className="flex items-center gap-2 px-2 pt-1 pb-2.5">
        <span className={cx('size-2 rounded-full', statusCls[status].dot)} />
        <span className="text-[13px] font-semibold text-fg">{t.reqStatus[status]}</span>
        <span className="text-xs text-fg-3 tabular">{items.length}</span>
      </div>
      <div className="flex flex-1 flex-col gap-2">
        {items.map((r) => (
          <div key={r.id} className="rise">
            <ReqCard r={r} draggable onDragStart={(e) => e.dataTransfer.setData('text/ks-req', r.code)} />
          </div>
        ))}
        {!items.length && <div className="flex flex-1 items-center justify-center rounded-xl border border-dashed border-line-strong/70 py-6 text-xs text-fg-3">{over ? t.reqs.dropHere : t.reqs.empty}</div>}
      </div>
    </div>
  )
}

export default function RequirementsPage() {
  const q = useRequirements()
  const projects = useProjects()
  const desktop = useDesktop()
  const setStatus = useSetStatus()
  const [project, setProject] = usePref<string>('ks.reqs.project', '')
  const [showClosed, setShowClosed] = usePref('ks.reqs.closed', false)
  const [mobileStatus, setMobileStatus] = usePref<string>('ks.reqs.mstatus', 'in_progress')
  const [search, setSearch] = useState('')
  const [creating, setCreating] = useState(false)

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
    if (r) void setStatus(r, status)
  }

  return (
    <>
      <PageHeader
        title={t.reqs.title}
        subtitle={q.isPending ? ' ' : t.reqs.summary(open)}
        actions={
          <Button variant="primary" icon={<Plus className="size-4" />} onClick={() => setCreating(true)}>
            {t.reqs.new}
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
        <label className="ml-auto flex items-center gap-2 text-sm text-fg-2">
          <Switch checked={showClosed} onChange={setShowClosed} />
          {t.reqs.showClosed}
        </label>
      </div>

      {q.isError ? (
        <ErrorState error={q.error} onRetry={() => q.refetch()} />
      ) : q.isPending ? (
        <CardSkeleton rows={2} />
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
      ) : desktop ? (
        <div className="-mx-10 flex gap-3 overflow-x-auto px-10 pb-4">
          {statuses.map((s) => (
            <Column key={s} status={s} items={byStatus(s)} onDrop={drop} />
          ))}
        </div>
      ) : (
        <>
          <div className="-mx-4 mb-4 overflow-x-auto px-4">
            <Segmented id="req-mobile" size="sm" value={mobileStatus} onChange={setMobileStatus} options={statuses.map((s) => ({ value: s, label: t.reqStatus[s], count: byStatus(s).length }))} />
          </div>
          <div className="space-y-2.5">
            {byStatus(mobileStatus).map((r) => (
              <div key={r.id} className="rise">
                <ReqCard r={r} />
              </div>
            ))}
            {!byStatus(mobileStatus).length && <p className="py-10 text-center text-sm text-fg-3">{t.reqs.empty}</p>}
          </div>
        </>
      )}

      <NewReqModal open={creating} onClose={() => setCreating(false)} defaultProject={project || undefined} />
    </>
  )
}
