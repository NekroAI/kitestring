import { useEffect, useState, type ReactNode } from 'react'
import { Check, ChevronDown, ExternalLink, FileCheck2, Link2, MessageCircleQuestion, MoreHorizontal, Trash2, Undo2, User, Bot } from 'lucide-react'
import { toast } from 'sonner'
import type { ReqFields, Requirement } from '../api/client'
import { useDecisions, useProjects, useRequirement, useSessions } from '../api/queries'
import { t } from '../i18n'
import { absTime, cx, relTime, useNow } from '../lib/util'
import { Badge, Button, Card, ErrorState, Field, Input, Skeleton, Textarea } from '../ui/base'
import { Menu, Modal, Panel, useConfirm, type MenuItem } from '../ui/overlay'
import { PriorityDot, StatusDot } from '../ui/marks'
import { useActions, useBusy } from './actions'
import { DecisionComposer, DecisionRow } from './DecisionCard'
import { useOpenReq, useOpenSession } from './nav'

export const boardStatuses = ['draft', 'ready', 'in_progress', 'blocked', 'review'] as const
export const closedStatuses = ['done', 'shelved'] as const
export const allStatuses = [...boardStatuses, ...closedStatuses]
export const priorities = ['P0', 'P1', 'P2', 'P3']

export const statusTone: Record<string, 'neutral' | 'accent' | 'run' | 'wait' | 'reply' | 'danger' | 'idle'> = {
  draft: 'idle',
  ready: 'accent',
  in_progress: 'run',
  blocked: 'danger',
  review: 'reply',
  done: 'neutral',
  shelved: 'neutral',
}

// 状态的完整类名（Tailwind 需要静态可见的类名，不能拼接）。
export const statusCls: Record<string, { pill: string; dot: string }> = {
  draft: { pill: 'bg-idle-soft text-idle', dot: 'bg-idle' },
  ready: { pill: 'bg-accent-soft text-accent', dot: 'bg-accent' },
  in_progress: { pill: 'bg-run-soft text-run', dot: 'bg-run' },
  blocked: { pill: 'bg-danger-soft text-danger', dot: 'bg-danger' },
  review: { pill: 'bg-reply-soft text-reply', dot: 'bg-reply' },
  done: { pill: 'bg-sunken text-fg-2', dot: 'bg-fg-3' },
  shelved: { pill: 'bg-sunken text-fg-3', dot: 'bg-line-strong' },
}

export function toFields(r: Requirement, patch: Partial<ReqFields> = {}): ReqFields {
  return {
    title: r.title,
    detail: r.detail,
    status: r.status as ReqFields['status'],
    priority: r.priority as ReqFields['priority'],
    project_id: r.project_id || undefined,
    source: r.source,
    acceptance: r.acceptance,
    doc_link: r.doc_link,
    ...patch,
  }
}

// useSetStatus：修改状态；从“待验收”退回时先确认（会作废证据，D19）。
export function useSetStatus() {
  const { updateReq } = useActions()
  const confirm = useConfirm()
  return async (r: Requirement, status: string) => {
    if (status === r.status) return
    if (r.status === 'review' && status !== 'done') {
      const ok = await confirm({ title: t.reqs.reject, description: t.reqs.rejectConfirm, confirmText: t.reqs.reject, danger: true })
      if (!ok) return
    }
    const res = await updateReq(r.code, toFields(r, { status: status as ReqFields['status'] }))
    if (res) {
      if (status === 'done' && r.status === 'review') toast.success(t.reqs.accepted(r.code))
      else if (r.status === 'review') toast(t.reqs.rejected(r.code))
      else toast(t.reqs.moved(r.code, t.reqStatus[status]))
    }
  }
}

export function statusItems(r: Requirement, setStatus: (r: Requirement, s: string) => void): MenuItem[] {
  return allStatuses.map((s) => ({
    label: t.reqStatus[s],
    hint: t.reqStatusHint[s],
    checked: s === r.status,
    icon: <span className={cx('size-2 rounded-full', statusCls[s].dot)} />,
    onSelect: () => void setStatus(r, s),
  }))
}

export function StatusMenu({ r, size = 'sm' }: { r: Requirement; size?: 'sm' | 'md' }) {
  const setStatus = useSetStatus()
  return (
    <Menu
      width="min-w-[240px]"
      align="start"
      trigger={
        <button onClick={(e) => e.stopPropagation()} className={cx('inline-flex items-center gap-1 rounded-md font-medium transition hover:brightness-95', size === 'sm' ? 'px-1.5 py-0.5 text-xs' : 'px-2.5 py-1 text-sm', statusCls[r.status]?.pill)}>
          {t.reqStatus[r.status]}
          <ChevronDown className="size-3 opacity-70" />
        </button>
      }
      items={statusItems(r, setStatus)}
    />
  )
}

// ReqCard：看板卡片。
export function ReqCard({ r, draggable, onDragStart }: { r: Requirement; draggable?: boolean; onDragStart?: (e: React.DragEvent) => void }) {
  const open = useOpenReq()
  const setStatus = useSetStatus()
  const actor = t.nextActor[r.next_actor]
  return (
    <Card
      role="button"
      tabIndex={0}
      draggable={draggable}
      onDragStart={onDragStart}
      onClick={() => open(r.code)}
      onKeyDown={(e) => e.key === 'Enter' && open(r.code)}
      className={cx('group cursor-pointer p-3.5 transition-all duration-200 hover:-translate-y-px hover:shadow-pop', r.status === 'review' && 'border-reply/40 ring-1 ring-reply/30', draggable && 'active:cursor-grabbing')}
    >
      <div className="flex items-center gap-2 text-xs text-fg-3">
        <PriorityDot p={r.priority} />
        <span className="font-mono text-[11px] font-semibold">{r.code}</span>
        {r.project_name && <span className="truncate">{r.project_name}</span>}
        <span className="ml-auto" onClick={(e) => e.stopPropagation()}>
          <Menu
            trigger={
              <button className="rounded-md p-0.5 text-fg-3 opacity-0 transition group-hover:opacity-100 hover:bg-hover hover:text-fg focus:opacity-100 max-lg:opacity-100" aria-label={t.common.more}>
                <MoreHorizontal className="size-4" />
              </button>
            }
            items={statusItems(r, setStatus)}
          />
        </span>
      </div>
      <div className="mt-1.5 line-clamp-3 text-sm leading-snug font-medium text-fg">{r.title}</div>
      <div className="mt-2.5 flex flex-wrap items-center gap-1.5">
        {r.pending_decisions > 0 && (
          <Badge tone="wait" icon={<MessageCircleQuestion className="size-3" />}>
            {r.pending_decisions}
          </Badge>
        )}
        {actor && r.status !== 'done' && r.status !== 'shelved' && (
          <Badge tone={r.next_actor === 'user' ? 'reply' : 'outline'} icon={r.next_actor === 'user' ? <User className="size-3" /> : <Bot className="size-3" />}>
            {actor}
          </Badge>
        )}
        {r.sessions.length > 0 && <span className="text-xs text-fg-3">{t.reqs.sessionsCount(r.sessions.length)}</span>}
      </div>
    </Card>
  )
}

// ReviewActions：待验收的“验收通过 / 退回”。
export function ReviewActions({ r, size = 'sm' }: { r: Requirement; size?: 'sm' | 'md' }) {
  const setStatus = useSetStatus()
  const [busy, wrap] = useBusy()
  return (
    <div className="flex gap-2">
      <Button size={size} variant="ghost" icon={<Undo2 className="size-3.5" />} disabled={busy} onClick={() => wrap(() => setStatus(r, 'in_progress'))}>
        {t.reqs.reject}
      </Button>
      <Button size={size} variant="primary" icon={<Check className="size-3.5" />} loading={busy} onClick={() => wrap(() => setStatus(r, 'done'))}>
        {t.reqs.accept}
      </Button>
    </div>
  )
}

// EditableText：点击即编辑，失焦保存。
function EditableText({ value, onSave, placeholder, multiline, className }: { value: string; onSave: (v: string) => void; placeholder: string; multiline?: boolean; className?: string }) {
  const [v, setV] = useState(value)
  useEffect(() => setV(value), [value])
  const commit = () => v.trim() !== value.trim() && onSave(v.trim())
  // 平时看起来就是普通文字，悬停出现边框，聚焦时变成输入框。
  const cls = cx(
    'block w-full min-w-0 rounded-lg border border-transparent bg-transparent px-3 text-[15px] text-fg transition-[border,background,box-shadow] placeholder:text-fg-3 hover:border-line focus:border-accent focus:bg-surface focus:ring-3 focus:ring-[var(--ring)] focus:outline-none sm:text-sm',
    className,
  )
  return multiline ? (
    <Textarea value={v} minRows={2} placeholder={placeholder} onChange={(e) => setV(e.target.value)} onBlur={commit} unstyled className={cx(cls, 'resize-none py-2 leading-relaxed')} />
  ) : (
    <input value={v} placeholder={placeholder} onChange={(e) => setV(e.target.value)} onBlur={commit} onKeyDown={(e) => e.key === 'Enter' && (e.target as HTMLInputElement).blur()} className={cx(cls, 'h-9')} />
  )
}

function Block({ title, children, right }: { title: string; children: ReactNode; right?: ReactNode }) {
  return (
    <section className="mb-5">
      <div className="mb-1.5 flex items-center justify-between">
        <h3 className="text-[13px] font-semibold text-fg-2">{title}</h3>
        {right}
      </div>
      {children}
    </section>
  )
}

export function ReqPanel({ refId, onClose }: { refId: string | null; onClose: () => void }) {
  const now = useNow()
  const q = useRequirement(refId)
  const projects = useProjects()
  const sessions = useSessions()
  const decisions = useDecisions()
  const { updateReq, deleteReq, linkReq } = useActions()
  const confirm = useConfirm()
  const openSession = useOpenSession()
  const r = q.data
  const save = (patch: Partial<ReqFields>) => r && void updateReq(r.code, toFields(r, patch))
  const sessByKey = new Map((sessions.data?.sessions ?? []).map((s) => [s.key, s]))
  const related = (decisions.data ?? []).filter((d) => r && d.requirement_id === r.id)
  const linkable = (sessions.data?.sessions ?? []).filter((s) => r && !r.sessions.includes(s.key) && s.state !== 'ended')

  return (
    <Panel
      open={!!refId}
      onClose={onClose}
      wide
      title={r ? <span className="font-mono text-sm text-fg-3">{r.code}</span> : <Skeleton className="h-5 w-24" />}
      subtitle={r && t.reqs.created(relTime(r.created_at, now), r.created_by)}
      actions={
        r && (
          <Menu
            trigger={
              <Button size="icon-sm" variant="ghost" aria-label={t.common.more}>
                <MoreHorizontal className="size-4" />
              </Button>
            }
            items={[
              {
                label: t.common.delete,
                icon: <Trash2 />,
                danger: true,
                onSelect: async () => {
                  if (await confirm({ title: t.common.delete, description: t.reqs.deleteConfirm, confirmText: t.common.delete, danger: true })) {
                    if (await deleteReq(r.code)) {
                      toast(t.reqs.deleted)
                      onClose()
                    }
                  }
                },
              },
            ]}
          />
        )
      }
    >
      {q.isError ? (
        <ErrorState error={q.error} onRetry={() => q.refetch()} />
      ) : !r ? (
        <div className="space-y-3">
          <Skeleton className="h-8" />
          <Skeleton className="h-24" />
        </div>
      ) : (
        <>
          <EditableText value={r.title} placeholder={t.reqs.titlePlaceholder} onSave={(v) => v && save({ title: v })} className="-mx-3 h-auto py-1.5 text-lg font-semibold" />

          <div className="mt-3 mb-5 flex flex-wrap items-center gap-2">
            <StatusMenu r={r} size="md" />
            <Menu
              align="start"
              trigger={
                <button className="inline-flex items-center gap-1.5 rounded-md bg-sunken px-2.5 py-1 text-sm text-fg-2 hover:text-fg">
                  <PriorityDot p={r.priority} />
                  {t.priority[r.priority]}
                  <ChevronDown className="size-3 opacity-70" />
                </button>
              }
              items={priorities.map((p) => ({ label: t.priority[p], checked: p === r.priority, icon: <PriorityDot p={p} />, onSelect: () => save({ priority: p as ReqFields['priority'] }) }))}
            />
            <Menu
              align="start"
              trigger={
                <button className="inline-flex max-w-[220px] items-center gap-1.5 rounded-md bg-sunken px-2.5 py-1 text-sm text-fg-2 hover:text-fg">
                  <span className="truncate">{r.project_name || t.reqs.noProject}</span>
                  <ChevronDown className="size-3 shrink-0 opacity-70" />
                </button>
              }
              items={[
                { label: t.reqs.noProject, checked: !r.project_id, onSelect: () => save({ project_id: undefined }) },
                ...(projects.data?.projects ?? []).map((p) => ({ label: p.name, hint: p.remote, checked: p.id === r.project_id, onSelect: () => save({ project_id: p.id }) })),
              ]}
            />
          </div>

          {r.status === 'review' && (
            <div className="mb-5 flex flex-col gap-3 rounded-2xl bg-reply-soft p-3.5 sm:flex-row sm:items-center">
              <div className="flex min-w-0 flex-1 items-start gap-2.5">
                <FileCheck2 className="mt-0.5 size-5 shrink-0 text-reply" />
                <p className="text-sm text-fg">{t.reqs.reviewBanner}</p>
              </div>
              <div className="flex justify-end">
                <ReviewActions r={r} />
              </div>
            </div>
          )}

          {related.filter((d) => d.status === 'pending').length > 0 && (
            <Block title={t.inbox.sections.decisions}>
              <div className="space-y-3">
                {related
                  .filter((d) => d.status === 'pending')
                  .map((d) => (
                    <DecisionComposer key={d.id} d={d} />
                  ))}
              </div>
            </Block>
          )}

          <Block title={t.reqs.detail}>
            <EditableText multiline value={r.detail} placeholder={t.reqs.detailPlaceholder} onSave={(v) => save({ detail: v })} className="-mx-3" />
          </Block>
          <Block title={t.reqs.acceptance}>
            <EditableText multiline value={r.acceptance} placeholder={t.reqs.acceptancePlaceholder} onSave={(v) => save({ acceptance: v })} className="-mx-3" />
          </Block>

          <Block title={t.reqs.evidence}>
            {r.evidence?.length ? (
              <ul className="space-y-1.5">
                {r.evidence.map((e) => (
                  <li key={e.id} className={cx('flex items-start gap-2.5 rounded-xl border border-line px-3 py-2', e.voided && 'opacity-60')}>
                    <Badge tone={e.voided ? 'neutral' : 'run'}>{e.kind}</Badge>
                    <div className="min-w-0 flex-1">
                      <div className={cx('text-sm break-words text-fg', e.voided && 'line-through')}>{e.summary}</div>
                      {e.link && <div className="mt-0.5 truncate font-mono text-xs text-fg-3">{e.link}</div>}
                    </div>
                    {e.voided ? <Badge tone="danger">{t.reqs.voided}</Badge> : <span className="shrink-0 text-xs text-fg-3">{relTime(e.created_at, now)}</span>}
                  </li>
                ))}
              </ul>
            ) : (
              <p className="text-sm text-fg-3">{t.reqs.noEvidence}</p>
            )}
          </Block>

          <Block
            title={t.reqs.sessions}
            right={
              <Menu
                width="min-w-[280px]"
                trigger={
                  <Button size="sm" variant="ghost" icon={<Link2 className="size-3.5" />} disabled={!linkable.length}>
                    {t.session.link}
                  </Button>
                }
                items={linkable.map((s) => ({
                  label: [s.project_name || s.cwd, s.device_name].filter(Boolean).join(t.common.sep),
                  hint: s.summary || s.last_prompt,
                  icon: <StatusDot state={s.state} />,
                  onSelect: () => void linkReq(r.code, s.key),
                }))}
              />
            }
          >
            {r.sessions.length ? (
              <div className="divide-y divide-line rounded-2xl border border-line">
                {r.sessions.map((k) => {
                  const s = sessByKey.get(k)
                  return (
                    <div key={k} className="flex items-center gap-2.5 px-3 py-2">
                      {s ? <StatusDot state={s.state} /> : <span className="size-2 rounded-full bg-line-strong" />}
                      <button onClick={() => s && openSession(k)} className="min-w-0 flex-1 text-left">
                        <div className="truncate text-sm text-fg">{s ? [s.project_name, s.device_name].filter(Boolean).join(t.common.sep) : k}</div>
                        <div className="truncate text-xs text-fg-3">{s ? s.summary || s.last_prompt : t.reqs.sessionGone}</div>
                      </button>
                      <Button size="icon-sm" variant="ghost" aria-label={t.session.unlink} onClick={() => void linkReq(r.code, k, true)}>
                        <span className="text-fg-3">×</span>
                      </Button>
                    </div>
                  )
                })}
              </div>
            ) : (
              <p className="text-sm text-fg-3">{t.reqs.noSessions}</p>
            )}
          </Block>

          {related.some((d) => d.status !== 'pending') && (
            <Block title={t.reqs.decisions}>
              <div className="overflow-hidden rounded-2xl border border-line">
                {related
                  .filter((d) => d.status !== 'pending')
                  .map((d) => (
                    <DecisionRow key={d.id} d={d} />
                  ))}
              </div>
            </Block>
          )}

          <div className="grid gap-4 sm:grid-cols-2">
            <Field label={t.reqs.source}>
              <EditableText value={r.source} placeholder={t.reqs.sourcePlaceholder} onSave={(v) => save({ source: v })} className="-mx-3" />
            </Field>
            <Field
              label={
                <span className="flex items-center gap-1">
                  {t.reqs.docLink}
                  {/^https?:\/\//.test(r.doc_link) && (
                    <a href={r.doc_link} target="_blank" rel="noreferrer" className="text-accent">
                      <ExternalLink className="size-3" />
                    </a>
                  )}
                </span>
              }
            >
              <EditableText value={r.doc_link} placeholder={t.reqs.docLinkPlaceholder} onSave={(v) => save({ doc_link: v })} className="-mx-3 font-mono text-[13px]" />
            </Field>
          </div>
          <p className="mt-4 text-xs text-fg-3">{absTime(r.updated_at)}</p>
        </>
      )}
    </Panel>
  )
}

export function NewReqModal({ open, onClose, defaultProject }: { open: boolean; onClose: () => void; defaultProject?: string }) {
  const projects = useProjects()
  const { createReq } = useActions()
  const openReq = useOpenReq()
  const [busy, wrap] = useBusy()
  const [f, setF] = useState<ReqFields>({ title: '', status: 'ready', priority: 'P2', project_id: defaultProject })
  useEffect(() => {
    if (open) setF({ title: '', status: 'ready', priority: 'P2', project_id: defaultProject })
  }, [open, defaultProject])
  const submit = () =>
    wrap(async () => {
      const r = await createReq({ ...f, title: f.title.trim() })
      if (r) {
        toast.success(t.reqs.createdToast(r.code))
        onClose()
        openReq(r.code)
      }
    })
  const proj = projects.data?.projects.find((p) => p.id === f.project_id)
  return (
    <Modal
      open={open}
      onClose={onClose}
      title={t.reqs.new}
      footer={
        <>
          <Button onClick={onClose}>{t.common.cancel}</Button>
          <Button variant="primary" loading={busy} disabled={!f.title.trim()} onClick={submit}>
            {t.common.create}
          </Button>
        </>
      }
    >
      <div className="space-y-4">
        <Input autoFocus value={f.title} placeholder={t.reqs.titlePlaceholder} onChange={(e) => setF({ ...f, title: e.target.value })} onKeyDown={(e) => e.key === 'Enter' && f.title.trim() && submit()} className="h-11 text-base" />
        <Textarea value={f.detail ?? ''} placeholder={t.reqs.detailPlaceholder} onChange={(e) => setF({ ...f, detail: e.target.value })} />
        <div className="flex flex-wrap gap-2">
          <Menu
            align="start"
            trigger={
              <Button size="sm">
                {t.reqStatus[f.status ?? 'ready']}
                <ChevronDown className="size-3 opacity-60" />
              </Button>
            }
            items={allStatuses.map((s) => ({ label: t.reqStatus[s], hint: t.reqStatusHint[s], checked: s === f.status, onSelect: () => setF({ ...f, status: s as ReqFields['status'] }) }))}
          />
          <Menu
            align="start"
            trigger={
              <Button size="sm" icon={<PriorityDot p={f.priority ?? 'P2'} />}>
                {t.priority[f.priority ?? 'P2']}
              </Button>
            }
            items={priorities.map((p) => ({ label: t.priority[p], checked: p === f.priority, icon: <PriorityDot p={p} />, onSelect: () => setF({ ...f, priority: p as ReqFields['priority'] }) }))}
          />
          <Menu
            align="start"
            trigger={
              <Button size="sm">
                <span className="max-w-[160px] truncate">{proj?.name ?? t.reqs.noProject}</span>
                <ChevronDown className="size-3 opacity-60" />
              </Button>
            }
            items={[{ label: t.reqs.noProject, checked: !f.project_id, onSelect: () => setF({ ...f, project_id: undefined }) }, ...(projects.data?.projects ?? []).map((p) => ({ label: p.name, hint: p.remote, checked: p.id === f.project_id, onSelect: () => setF({ ...f, project_id: p.id }) }))]}
          />
        </div>
      </div>
    </Modal>
  )
}
