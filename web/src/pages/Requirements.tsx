import { useMemo, useState, type FormEvent } from 'react'
import { useSearchParams } from 'react-router'
import { useQueryClient } from '@tanstack/react-query'
import { api, errMsg, unwrap, type Project, type ReqFields, type Requirement } from '../api/client'
import { useProjects, useRequirements } from '../api/queries'
import { Empty, ErrorBox, Field, Loading, Modal, Spinner, Toggle, cx, reqStatusClass, useToast } from '../components/ui'
import { RequirementDrawer } from '../components/RequirementDrawer'
import { t } from '../i18n'

export const REQ_STATUSES = ['draft', 'ready', 'in_progress', 'blocked', 'review', 'done', 'shelved'] as const
export const PRIORITIES = ['P0', 'P1', 'P2', 'P3'] as const
type ReqStatus = (typeof REQ_STATUSES)[number]
type Priority = (typeof PRIORITIES)[number]

// toFields 把需求转换为 PUT 所需的完整字段。
export function toFields(r: Requirement, patch: Partial<ReqFields> = {}): ReqFields {
  return {
    title: r.title,
    project_id: r.project_id || undefined,
    detail: r.detail,
    status: r.status as ReqStatus,
    priority: (r.priority || 'P2') as Priority,
    source: r.source,
    acceptance: r.acceptance,
    doc_link: r.doc_link,
    ...patch,
  }
}

// useReqMutations 提供改状态、验收、退回等常用操作。
export function useReqActions() {
  const qc = useQueryClient()
  const toast = useToast()
  const update = async (r: Requirement, patch: Partial<ReqFields>, okText = t.common.saved) => {
    try {
      const out = await unwrap(api.PUT('/api/requirements/{ref}', { params: { path: { ref: r.code } }, body: toFields(r, patch) }))
      toast(okText)
      void qc.invalidateQueries({ queryKey: ['requirements'] })
      void qc.invalidateQueries({ queryKey: ['sessions'] })
      return out
    } catch (e) {
      toast(errMsg(e), 'err')
      throw e
    }
  }
  return { update }
}

const PRIORITY_STYLE: Record<string, string> = {
  P0: 'bg-red-600 text-white',
  P1: 'bg-orange-100 text-orange-800 dark:bg-orange-950 dark:text-orange-300',
  P2: 'chip-gray',
  P3: 'bg-transparent text-slate-500 ring-1 ring-slate-300 ring-inset dark:ring-slate-700',
}

export function PriorityChip({ p }: { p: string }) {
  return <span className={cx('chip font-mono', PRIORITY_STYLE[p] ?? PRIORITY_STYLE.P2)}>{p || 'P2'}</span>
}

export function ActorChip({ actor }: { actor: string }) {
  if (actor !== 'user' && actor !== 'agent') return null
  return (
    <span className={cx('chip', actor === 'user' ? 'bg-amber-500 text-white dark:text-black' : 'bg-sky-100 text-sky-800 dark:bg-sky-950 dark:text-sky-300')}>
      {t.req.actor[actor]}
    </span>
  )
}

function loadShowDone() {
  try {
    return localStorage.getItem('ks.req.showDone') === '1'
  } catch {
    return false
  }
}

type Group = { key: string; title: string; projects: { key: string; title: string; reqs: Requirement[] }[] }

export default function RequirementsPage() {
  const reqs = useRequirements()
  const projects = useProjects()
  const [params, setParams] = useSearchParams()
  const openRef = params.get('r')
  const [showDone, setShowDoneState] = useState(loadShowDone)
  const [creating, setCreating] = useState(false)

  const setShowDone = (v: boolean) => {
    setShowDoneState(v)
    try {
      localStorage.setItem('ks.req.showDone', v ? '1' : '0')
    } catch {
      // 忽略存储不可用
    }
  }

  const open = (ref: string | null) => {
    const p = new URLSearchParams(params)
    if (ref) p.set('r', ref)
    else p.delete('r')
    setParams(p, { replace: !ref })
  }

  const all = reqs.data ?? []
  const hiddenN = all.filter((r) => r.status === 'done' || r.status === 'shelved').length

  const groups = useMemo<Group[]>(() => {
    const list = (reqs.data ?? []).filter((r) => showDone || (r.status !== 'done' && r.status !== 'shelved'))
    const ps = projects.data?.projects ?? []
    const ws = projects.data?.workspaces ?? []
    const pMap = new Map(ps.map((p) => [p.id, p]))
    const byProject = new Map<string, Requirement[]>()
    for (const r of list) {
      const k = r.project_id || ''
      byProject.set(k, [...(byProject.get(k) ?? []), r])
    }
    const statusRank = (s: string) => ({ review: 0, blocked: 1, in_progress: 2, ready: 3, draft: 4, done: 5, shelved: 6 })[s] ?? 9
    const sortReqs = (rs: Requirement[]) =>
      rs.sort((a, b) => statusRank(a.status) - statusRank(b.status) || (a.priority || 'P2').localeCompare(b.priority || 'P2') || b.updated_at.localeCompare(a.updated_at))
    const projEntry = (pid: string, p?: Project) => ({ key: pid || 'none', title: p?.name || (pid ? t.common.unknown : t.req.noProject), reqs: sortReqs(byProject.get(pid) ?? []) })

    const out: Group[] = []
    for (const w of ws) {
      const projs = [...byProject.keys()].filter((pid) => pid && pMap.get(pid)?.workspace_id === w.id)
      if (projs.length) out.push({ key: w.id, title: w.name, projects: projs.map((pid) => projEntry(pid, pMap.get(pid))).sort((a, b) => a.title.localeCompare(b.title)) })
    }
    const wsIds = new Set(ws.map((w) => w.id))
    const loose = [...byProject.keys()].filter((pid) => !pid || !wsIds.has(pMap.get(pid)?.workspace_id ?? ''))
    if (loose.length) {
      out.push({
        key: 'none',
        title: t.req.noWorkspace,
        projects: loose.map((pid) => projEntry(pid, pMap.get(pid))).sort((a, b) => (a.key === 'none' ? 1 : b.key === 'none' ? -1 : a.title.localeCompare(b.title))),
      })
    }
    return out
  }, [reqs.data, projects.data, showDone])

  if (reqs.isPending) return <Loading />
  if (reqs.isError) return <ErrorBox error={reqs.error} onRetry={() => reqs.refetch()} />

  return (
    <div className="space-y-4">
      <div className="flex flex-wrap items-center gap-2">
        <h1 className="mr-auto text-lg font-semibold">{t.req.title}</h1>
        <Toggle checked={showDone} onChange={setShowDone}>
          {t.req.showDone}
        </Toggle>
        <button className="btn btn-primary" onClick={() => setCreating(true)}>
          + {t.req.create}
        </button>
      </div>
      {!showDone && hiddenN > 0 && <p className="muted -mt-2 text-xs">{t.req.hiddenCount(hiddenN)}</p>}

      {groups.length === 0 ? (
        <Empty>{t.req.empty}</Empty>
      ) : (
        groups.map((g) => (
          <section key={g.key} className="space-y-3">
            <h2 className="section-title">{g.title}</h2>
            {g.projects.map((p) => (
              <div key={p.key} className="card overflow-hidden">
                <div className="flex items-center gap-2 border-b border-slate-200 bg-slate-50 px-3 py-2 dark:border-slate-800 dark:bg-slate-900/60">
                  <h3 className="min-w-0 truncate text-sm font-semibold">{p.title}</h3>
                  <span className="muted text-xs tabular-nums">{p.reqs.length}</span>
                </div>
                <ul className="divide-y divide-slate-100 dark:divide-slate-800">
                  {p.reqs.map((r) => (
                    <ReqRow key={r.id} r={r} onOpen={() => open(r.code)} />
                  ))}
                </ul>
              </div>
            ))}
          </section>
        ))
      )}

      {creating && <CreateReqModal projects={projects.data?.projects ?? []} onClose={() => setCreating(false)} onCreated={(r) => open(r.code)} />}
      {openRef && <RequirementDrawer reqRef={openRef} onClose={() => open(null)} />}
    </div>
  )
}

// StatusSelect 允许直接修改需求状态；离开“待验收”时提示作废证据。
export function StatusSelect({ r, className }: { r: Requirement; className?: string }) {
  const { update } = useReqActions()
  const [busy, setBusy] = useState(false)
  return (
    <select
      aria-label={t.req.fields.status}
      className={cx('h-8 rounded-md border-0 py-0 pr-7 pl-2 text-xs font-medium focus:ring-2 focus:ring-sky-500/40', reqStatusClass(r.status), className)}
      value={r.status}
      disabled={busy}
      onClick={(e) => e.stopPropagation()}
      onChange={async (e) => {
        const status = e.target.value as ReqStatus
        if (r.status === 'review' && status !== 'done' && !window.confirm(t.req.leaveReviewConfirm)) {
          e.target.value = r.status
          return
        }
        setBusy(true)
        try {
          await update(r, { status })
        } catch {
          e.target.value = r.status
        } finally {
          setBusy(false)
        }
      }}
    >
      {REQ_STATUSES.map((s) => (
        <option key={s} value={s}>
          {t.req.status[s]}
        </option>
      ))}
    </select>
  )
}

export function ReviewActions({ r, size = 'sm' }: { r: Requirement; size?: 'sm' | 'md' }) {
  const { update } = useReqActions()
  const [busy, setBusy] = useState(false)
  if (r.status !== 'review') return null
  const run = async (patch: Partial<ReqFields>, okText: string) => {
    setBusy(true)
    try {
      await update(r, patch, okText)
    } catch {
      // 已提示
    } finally {
      setBusy(false)
    }
  }
  const sz = size === 'sm' ? 'btn-sm' : ''
  return (
    <div className="flex gap-2" onClick={(e) => e.stopPropagation()}>
      <button className={cx('btn border-emerald-600 bg-emerald-600 text-white hover:bg-emerald-700 dark:border-emerald-500 dark:bg-emerald-600 dark:text-white dark:hover:bg-emerald-500', sz)} disabled={busy} onClick={() => run({ status: 'done' }, t.req.accepted)}>
        {t.req.accept}
      </button>
      <button
        className={cx('btn btn-danger', sz)}
        disabled={busy}
        onClick={() => {
          if (window.confirm(t.req.rejectConfirm)) void run({ status: 'in_progress' }, t.req.rejected)
        }}
      >
        {t.req.reject}
      </button>
    </div>
  )
}

function ReqRow({ r, onOpen }: { r: Requirement; onOpen: () => void }) {
  const review = r.status === 'review'
  const sessionsN = r.sessions?.length ?? 0
  return (
    <li
      className={cx('cursor-pointer px-3 py-2.5 hover:bg-slate-50 dark:hover:bg-slate-800/40', review && 'bg-amber-50/70 dark:bg-amber-950/20')}
      onClick={onOpen}
      role="button"
      tabIndex={0}
      onKeyDown={(e) => e.key === 'Enter' && onOpen()}
    >
      <div className="flex flex-col gap-2 md:flex-row md:items-center md:gap-3">
        <div className="flex min-w-0 flex-1 items-baseline gap-2">
          <span className="muted shrink-0 font-mono text-xs">{r.code}</span>
          <span className={cx('min-w-0 text-sm break-words', r.status === 'shelved' && 'muted line-through')}>{r.title}</span>
        </div>
        <div className="flex flex-wrap items-center gap-1.5">
          <StatusSelect r={r} />
          <PriorityChip p={r.priority} />
          <ActorChip actor={r.next_actor} />
          {r.pending_decisions > 0 && <span className="chip bg-amber-100 text-amber-900 dark:bg-amber-950 dark:text-amber-300">{t.req.decisions(r.pending_decisions)}</span>}
          {sessionsN > 0 && <span className="chip chip-gray">{t.req.sessions(sessionsN)}</span>}
          {review && (
            <span className="ml-auto md:ml-1">
              <ReviewActions r={r} />
            </span>
          )}
        </div>
      </div>
    </li>
  )
}

function CreateReqModal({ projects, onClose, onCreated }: { projects: Project[]; onClose: () => void; onCreated: (r: Requirement) => void }) {
  const qc = useQueryClient()
  const toast = useToast()
  const [title, setTitle] = useState('')
  const [projectId, setProjectId] = useState('')
  const [priority, setPriority] = useState<Priority>('P2')
  const [status, setStatus] = useState<ReqStatus>('draft')
  const [detail, setDetail] = useState('')
  const [err, setErr] = useState('')
  const [busy, setBusy] = useState(false)

  const submit = async (e?: FormEvent) => {
    e?.preventDefault()
    if (!title.trim()) return setErr(t.req.titleRequired)
    setBusy(true)
    try {
      const r = await unwrap(
        api.POST('/api/requirements', { body: { title: title.trim(), project_id: projectId || undefined, priority, status, detail: detail.trim() || undefined } }),
      )
      toast(t.req.created)
      void qc.invalidateQueries({ queryKey: ['requirements'] })
      onClose()
      onCreated(r)
    } catch (e) {
      setErr(errMsg(e))
    } finally {
      setBusy(false)
    }
  }

  return (
    <Modal
      title={t.req.create}
      onClose={onClose}
      footer={
        <>
          <button className="btn" onClick={onClose}>
            {t.common.cancel}
          </button>
          <button className="btn btn-primary" onClick={() => submit()} disabled={busy}>
            {busy && <Spinner />}
            {t.common.create}
          </button>
        </>
      }
    >
      <form className="space-y-3" onSubmit={submit}>
        <Field label={t.req.fields.title}>
          <input className="input" autoFocus value={title} onChange={(e) => setTitle(e.target.value)} />
        </Field>
        <Field label={t.req.fields.project}>
          <select className="input" value={projectId} onChange={(e) => setProjectId(e.target.value)}>
            <option value="">{t.req.noProject}</option>
            {projects.map((p) => (
              <option key={p.id} value={p.id}>
                {p.name}
              </option>
            ))}
          </select>
        </Field>
        <div className="grid grid-cols-2 gap-3">
          <Field label={t.req.fields.status}>
            <select className="input" value={status} onChange={(e) => setStatus(e.target.value as ReqStatus)}>
              {REQ_STATUSES.map((s) => (
                <option key={s} value={s}>
                  {t.req.status[s]}
                </option>
              ))}
            </select>
          </Field>
          <Field label={t.req.fields.priority}>
            <select className="input" value={priority} onChange={(e) => setPriority(e.target.value as Priority)}>
              {PRIORITIES.map((p) => (
                <option key={p} value={p}>
                  {p}
                </option>
              ))}
            </select>
          </Field>
        </div>
        <Field label={t.common.withOptional(t.req.fields.detail)}>
          <textarea className="input" rows={3} value={detail} onChange={(e) => setDetail(e.target.value)} />
        </Field>
        {err && <p className="text-sm text-red-600 dark:text-red-400">{err}</p>}
        <button type="submit" className="hidden" />
      </form>
    </Modal>
  )
}
