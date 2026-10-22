import { useEffect, useState } from 'react'
import { Bot, ChevronDown, MessageCircleQuestion, Monitor, MoreHorizontal, Send, User } from 'lucide-react'
import { toast } from 'sonner'
import type { ReqFields, Requirement } from '../api/client'
import { useProjects, useSessions } from '../api/queries'
import { defaultTarget, targetGroup, targetItems, targetLabel, useAssign } from './assign'
import { t } from '../i18n'
import { cx } from '../lib/util'
import { Badge, Button, Card, Input, Textarea, Tip } from '../ui/base'
import { Menu, Modal, useConfirm, type MenuItem } from '../ui/overlay'
import { displayState, PriorityDot, StatusDot } from '../ui/marks'
import { useActions, useBusy } from './actions'
import { AttachZone, type FileRef } from '../ui/attach'
import { useOpenReq } from './nav'
import { Ctx } from '../ui/contextMenu'
import { useReqMenu } from './menus'

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
  const menu = useReqMenu()
  const actor = t.nextActor[r.next_actor]
  return (
    <Ctx items={menu(r)} className="h-full rounded-xl">
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
              <button className="rounded-md p-0.5 text-fg-3 opacity-0 transition group-hover:opacity-100 hover:bg-hover hover:text-fg focus:opacity-100 max-lg:opacity-100 pointer-coarse:opacity-100" aria-label={t.common.more}>
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
          <Tip content={t.reqs.decisionsTip(r.pending_decisions)}>
            <span>
              <Badge tone="wait" icon={<MessageCircleQuestion className="size-3" />}>
                {r.pending_decisions}
              </Badge>
            </span>
          </Tip>
        )}
        {actor && r.status !== 'done' && r.status !== 'shelved' && (
          <Badge tone={r.next_actor === 'user' ? 'reply' : 'outline'} icon={r.next_actor === 'user' ? <User className="size-3" /> : <Bot className="size-3" />}>
            {actor}
          </Badge>
        )}
        {r.sessions.length > 0 && (
          <Tip content={t.reqs.sessionsTip(r.sessions.length)}>
            <span className="inline-flex items-center gap-1 text-xs text-fg-3 tabular">
              <Monitor className="size-3.5" />
              {r.sessions.length}
            </span>
          </Tip>
        )}
      </div>
    </Card></Ctx>
  )
}

// EditableText：点击即编辑，失焦保存。
export function EditableText({ value, onSave, placeholder, multiline, className }: { value: string; onSave: (v: string) => void; placeholder: string; multiline?: boolean; className?: string }) {
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


export function NewReqModal({ open, onClose, defaultProject }: { open: boolean; onClose: () => void; defaultProject?: string }) {
  const projects = useProjects()
  const sessions = useSessions()
  const { createReq } = useActions()
  const assign = useAssign()
  const openReq = useOpenReq()
  const [busy, wrap] = useBusy()
  const [f, setF] = useState<ReqFields>({ title: '', status: 'ready', priority: 'P2', project_id: defaultProject })
  const [files, setFiles] = useState<FileRef[]>([])
  // 交给哪个会话：默认同项目里待命或刚活跃过的会话；手动选过就以你的为准
  const [target, setTarget] = useState<string | null | undefined>(undefined)
  const all = sessions.data?.sessions ?? []
  const to = target === undefined ? defaultTarget(all, f.project_id) : all.find((s) => s.key === target)
  useEffect(() => {
    if (open) {
      setF({ title: '', status: 'ready', priority: 'P2', project_id: defaultProject })
      setFiles([])
      setTarget(undefined)
    }
  }, [open, defaultProject])
  const submit = () =>
    wrap(async () => {
      const r = await createReq({ ...f, title: f.title.trim(), files: files.map((x) => x.id) })
      if (r) {
        if (to) await assign(r.code, to)
        else toast.success(t.reqs.created(r.code))
        onClose()
        openReq(r.code)
      }
    })
  const proj = projects.data?.projects.find((p) => p.id === f.project_id)
  return (
    <Modal
      open={open}
      onClose={onClose}
      title={t.reqs.newTitle}
      footer={
        <>
          <Button onClick={onClose}>{t.common.cancel}</Button>
          <Button variant="primary" loading={busy} disabled={!f.title.trim()} onClick={submit} icon={to ? <Send className="size-4" /> : undefined}>
            {to ? t.assign.createAndSend : t.common.create}
          </Button>
        </>
      }
    >
      <div className="space-y-4">
        <Input autoFocus value={f.title} placeholder={t.reqs.titlePlaceholder} onChange={(e) => setF({ ...f, title: e.target.value })} onKeyDown={(e) => e.key === 'Enter' && f.title.trim() && submit()} className="h-11 text-base" />
        <AttachZone value={files} onChange={setFiles}>
          <Textarea value={f.detail ?? ''} placeholder={t.reqs.detailPlaceholder} onChange={(e) => setF({ ...f, detail: e.target.value })} />
        </AttachZone>
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
        <div className="flex items-center gap-2 rounded-xl bg-surface-2 px-3 py-2">
          <Send className="size-4 shrink-0 text-fg-3" />
          <span className="shrink-0 text-[13px] text-fg-3">{t.assign.field}</span>
          <Menu
            align="start"
            width="w-80"
            trigger={
              <button className="flex min-w-0 flex-1 items-center gap-2 rounded-lg px-1.5 py-1 text-left text-sm text-fg hover:bg-hover">
                {to && <StatusDot state={displayState(to)} />}
                <span className="min-w-0 flex-1 truncate">{to ? targetLabel(to) : t.assign.nobody}</span>
                {to && <span className={cx('shrink-0 text-xs', targetGroup(to) === 'now' ? 'text-accent' : 'text-fg-3')}>{t.assign.when[targetGroup(to)]}</span>}
                <ChevronDown className="size-3 shrink-0 opacity-60" />
              </button>
            }
            items={[{ label: t.assign.nobody, checked: !to, onSelect: () => setTarget(null) }, { type: 'separator' }, ...targetItems(all, f.project_id, (s) => setTarget(s.key), { checked: to?.key })]}
          />
        </div>
      </div>
    </Modal>
  )
}
