import { useEffect, useState } from 'react'
import { Link } from 'react-router'
import { useQueryClient } from '@tanstack/react-query'
import { api, errMsg, unwrap, type ReqFields, type Requirement } from '../api/client'
import { useProjects, useRequirement, useSessions } from '../api/queries'
import { ActorChip, PRIORITIES, REQ_STATUSES, ReviewActions, toFields, useReqActions } from '../pages/Requirements'
import { ToolChip } from '../pages/Wall'
import { t } from '../i18n'
import { absTime } from '../lib/time'
import { ConfirmButton, Drawer, ErrorBox, Field, Loading, ReqStatusChip, Spinner, StateBadge, cx, useToast } from './ui'

export function RequirementDrawer({ reqRef, onClose }: { reqRef: string; onClose: () => void }) {
  const q = useRequirement(reqRef)
  return (
    <Drawer title={q.data ? `${q.data.code} · ${t.req.detailTitle}` : t.req.detailTitle} onClose={onClose}>
      {q.isPending ? <Loading /> : q.isError ? <ErrorBox error={q.error} onRetry={() => q.refetch()} /> : <ReqDetail r={q.data} onDeleted={onClose} />}
    </Drawer>
  )
}

function ReqDetail({ r, onDeleted }: { r: Requirement; onDeleted: () => void }) {
  const qc = useQueryClient()
  const toast = useToast()
  const projects = useProjects()
  const sessions = useSessions()
  const { update } = useReqActions()
  const [form, setForm] = useState<ReqFields>(() => toFields(r))
  const [busy, setBusy] = useState(false)
  const [linkKey, setLinkKey] = useState('')

  // 服务端数据变化（SSE 推送）时重置表单
  useEffect(() => setForm(toFields(r)), [r])

  const dirty = JSON.stringify(form) !== JSON.stringify(toFields(r))
  const set = <K extends keyof ReqFields>(k: K, v: ReqFields[K]) => setForm((f) => ({ ...f, [k]: v }))

  const save = async () => {
    if (!form.title.trim()) return toast(t.req.titleRequired, 'err')
    if (r.status === 'review' && form.status !== 'review' && form.status !== 'done' && !window.confirm(t.req.leaveReviewConfirm)) return
    setBusy(true)
    try {
      await update(r, { ...form, title: form.title.trim(), project_id: form.project_id || undefined })
    } catch {
      // 已提示
    } finally {
      setBusy(false)
    }
  }

  const link = async (key: string, unlink = false) => {
    try {
      await unwrap(api.POST('/api/requirements/{ref}/links', { params: { path: { ref: r.code } }, body: { session_key: key, unlink } }))
      void qc.invalidateQueries({ queryKey: ['requirements'] })
      void qc.invalidateQueries({ queryKey: ['sessions'] })
      setLinkKey('')
    } catch (e) {
      toast(errMsg(e), 'err')
    }
  }

  const allSessions = sessions.data?.sessions ?? []
  const sMap = new Map(allSessions.map((s) => [s.key, s]))
  const linked = r.sessions ?? []
  const linkable = allSessions.filter((s) => !linked.includes(s.key))
  const evidence = r.evidence ?? []

  return (
    <div className="space-y-5">
      <div className="flex flex-wrap items-center gap-2">
        <ReqStatusChip status={r.status} />
        <ActorChip actor={r.next_actor} />
        {r.pending_decisions > 0 && (
          <Link to="/decisions" className="chip bg-amber-100 text-amber-900 dark:bg-amber-950 dark:text-amber-300">
            {t.req.decisions(r.pending_decisions)}
          </Link>
        )}
        <span className="muted ml-auto text-xs">
          {t.req.fields.createdBy} {t.req.createdBy[r.created_by] ?? r.created_by}
          {t.common.sep}
          {t.req.fields.updatedAt} {absTime(r.updated_at)}
        </span>
      </div>

      {r.status === 'review' && (
        <div className="flex flex-wrap items-center gap-3 rounded-xl border border-amber-400 bg-amber-50 p-3 dark:border-amber-700 dark:bg-amber-950/40">
          <span className="flex-1 text-sm font-medium text-amber-900 dark:text-amber-200">{t.req.status.review}</span>
          <ReviewActions r={r} size="md" />
        </div>
      )}

      <section className="card space-y-3 p-3">
        <Field label={t.req.fields.title}>
          <input className="input" value={form.title} onChange={(e) => set('title', e.target.value)} />
        </Field>
        <div className="grid grid-cols-2 gap-3 sm:grid-cols-3">
          <Field label={t.req.fields.status}>
            <select className="input" value={form.status} onChange={(e) => set('status', e.target.value as ReqFields['status'])}>
              {REQ_STATUSES.map((s) => (
                <option key={s} value={s}>
                  {t.req.status[s]}
                </option>
              ))}
            </select>
          </Field>
          <Field label={t.req.fields.priority}>
            <select className="input" value={form.priority} onChange={(e) => set('priority', e.target.value as ReqFields['priority'])}>
              {PRIORITIES.map((p) => (
                <option key={p} value={p}>
                  {p}
                </option>
              ))}
            </select>
          </Field>
          <div className="col-span-2 sm:col-span-1">
            <Field label={t.req.fields.project}>
              <select className="input" value={form.project_id ?? ''} onChange={(e) => set('project_id', e.target.value || undefined)}>
                <option value="">{t.req.noProject}</option>
                {(projects.data?.projects ?? []).map((p) => (
                  <option key={p.id} value={p.id}>
                    {p.name}
                  </option>
                ))}
              </select>
            </Field>
          </div>
        </div>
        <Field label={t.req.fields.detail}>
          <textarea className="input" rows={4} value={form.detail ?? ''} onChange={(e) => set('detail', e.target.value)} />
        </Field>
        <Field label={t.req.fields.acceptance}>
          <textarea className="input" rows={3} value={form.acceptance ?? ''} onChange={(e) => set('acceptance', e.target.value)} />
        </Field>
        <div className="grid gap-3 sm:grid-cols-2">
          <Field label={t.req.fields.source}>
            <input className="input" value={form.source ?? ''} onChange={(e) => set('source', e.target.value)} />
          </Field>
          <Field label={t.req.fields.docLink}>
            <input className="input" value={form.doc_link ?? ''} onChange={(e) => set('doc_link', e.target.value)} />
          </Field>
        </div>
        {r.doc_link && /^https?:\/\//.test(r.doc_link) && (
          <a href={r.doc_link} target="_blank" rel="noreferrer" className="block truncate text-xs text-sky-700 underline dark:text-sky-400">
            {r.doc_link}
          </a>
        )}
        <div className="flex justify-end gap-2">
          {dirty && (
            <button className="btn" onClick={() => setForm(toFields(r))} disabled={busy}>
              {t.common.cancel}
            </button>
          )}
          <button className="btn btn-primary" onClick={save} disabled={!dirty || busy}>
            {busy && <Spinner />}
            {t.common.save}
          </button>
        </div>
      </section>

      <section>
        <h3 className="section-title mb-2">{t.req.evidence}</h3>
        {evidence.length === 0 ? (
          <p className="muted text-sm">{t.req.noEvidence}</p>
        ) : (
          <ul className="card divide-y divide-slate-100 dark:divide-slate-800">
            {evidence.map((e) => (
              <li key={e.id} className={cx('px-3 py-2 text-sm', e.voided && 'opacity-60')}>
                <div className="flex items-center gap-2">
                  {e.kind && <span className="chip chip-gray font-mono">{e.kind}</span>}
                  {e.voided && <span className="chip bg-red-100 text-red-700 dark:bg-red-950 dark:text-red-300">{t.req.voided}</span>}
                  <span className="muted ml-auto shrink-0 text-xs">{absTime(e.created_at)}</span>
                </div>
                <p className={cx('mt-1 break-words', e.voided && 'line-through')}>{e.summary}</p>
                {e.link && (
                  <a href={e.link} target="_blank" rel="noreferrer" className={cx('block truncate text-xs text-sky-700 underline dark:text-sky-400', e.voided && 'line-through')}>
                    {e.link}
                  </a>
                )}
              </li>
            ))}
          </ul>
        )}
      </section>

      <section>
        <h3 className="section-title mb-2">{t.req.linkedSessions}</h3>
        {linked.length === 0 ? (
          <p className="muted mb-2 text-sm">{t.req.noLinkedSessions}</p>
        ) : (
          <ul className="card mb-2 divide-y divide-slate-100 dark:divide-slate-800">
            {linked.map((key) => {
              const s = sMap.get(key)
              return (
                <li key={key} className="flex items-center gap-2 px-3 py-2 text-sm">
                  {s ? (
                    <Link to={`/?session=${encodeURIComponent(key)}`} className="flex min-w-0 flex-1 items-center gap-2">
                      <ToolChip tool={s.tool} />
                      <span className="min-w-0 truncate">{[s.project_name, s.device_name].filter(Boolean).join(t.common.sep)}</span>
                      <StateBadge state={s.state} />
                    </Link>
                  ) : (
                    <span className="min-w-0 flex-1 truncate">
                      <span className="font-mono text-xs">{key}</span> <span className="muted text-xs">{t.req.sessionUnknown}</span>
                    </span>
                  )}
                  <button className="btn btn-sm btn-ghost text-red-600 dark:text-red-400" onClick={() => link(key, true)}>
                    {t.req.unlink}
                  </button>
                </li>
              )
            })}
          </ul>
        )}
        <div className="flex gap-2">
          <select className="input flex-1" value={linkKey} onChange={(e) => setLinkKey(e.target.value)}>
            <option value="">{t.req.linkPick}</option>
            {linkable.map((s) => (
              <option key={s.key} value={s.key}>
                {[t.tool[s.tool] ?? s.tool, s.project_name || t.wall.noProject, s.device_name, t.state[s.state] ?? s.state].join(t.common.sep)}
              </option>
            ))}
          </select>
          <button className="btn" disabled={!linkKey} onClick={() => link(linkKey)}>
            {t.req.linkSession}
          </button>
        </div>
      </section>

      <div className="border-t border-slate-200 pt-4 dark:border-slate-800">
        <ConfirmButton
          message={t.req.deleteConfirm}
          confirmLabel={t.common.delete}
          onConfirm={async () => {
            try {
              await unwrap(api.DELETE('/api/requirements/{ref}', { params: { path: { ref: r.code } } }))
              toast(t.req.deleted)
              void qc.invalidateQueries({ queryKey: ['requirements'] })
              onDeleted()
            } catch (e) {
              toast(errMsg(e), 'err')
            }
          }}
        >
          {t.common.delete}
        </ConfirmButton>
      </div>
    </div>
  )
}
