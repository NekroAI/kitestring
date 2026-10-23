import { useEffect, useState } from 'react'
import { Bot, ChevronDown, FolderGit2, MessageCircleQuestion, Monitor, MoreHorizontal, Palette, Plus, Send, User, X, Zap } from 'lucide-react'
import { toast } from 'sonner'
import type { ReqFields, Requirement } from '../api/client'
import { useProjects, useSessions } from '../api/queries'
import { defaultTarget, targetGroup, targetItems, targetLabel, useAssign } from './assign'
import { t } from '../i18n'
import { cx, loadPref, savePref, useDesktop, usePref } from '../lib/util'
import { Badge, Button, Card, Switch, Textarea, Tip } from '../ui/base'
import { FullSheet, Menu, Panel, useConfirm, type MenuItem } from '../ui/overlay'
import { displayState, PriorityDot, StatusDot } from '../ui/marks'
import { useActions, useBusy } from './actions'
import { AttachZone, type FileRef } from '../ui/attach'
import { useOpenReq } from './nav'
import { sessionTitle } from './SessionCard'
import { DesignBadge } from './design'
import { PlanBadge } from './plans'
import { OwnBadge, useOwnership } from './ownership'
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
  const own = useOwnership()(r)
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
        <PlanBadge r={r} />
        <DesignBadge r={r} />
        {r.pending_decisions > 0 && (
          <Tip content={t.reqs.decisionsTip(r.pending_decisions)}>
            <span>
              <Badge tone="wait" icon={<MessageCircleQuestion className="size-3" />}>
                {r.pending_decisions}
              </Badge>
            </span>
          </Tip>
        )}
        {own ? (
          own.tone !== 'locked' && <OwnBadge own={own} />
        ) : actor && r.status !== 'done' && r.status !== 'shelved' && (
          <Badge tone={r.next_actor === 'user' ? 'reply' : 'outline'} icon={r.next_actor === 'user' ? <User className="size-3" /> : <Bot className="size-3" />}>
            {actor}
          </Badge>
        )}
        {r.sessions.length > 0 && (
          <Tip content={own?.session ? own.text : t.reqs.sessionsTip(r.sessions.length)}>
            <span className="inline-flex min-w-0 items-center gap-1 text-xs text-fg-3 tabular">
              <Monitor className="size-3.5 shrink-0" />
              {own?.session ? <span className="max-w-[12em] truncate">{sessionTitle(own.session)}</span> : r.sessions.length}
            </span>
          </Tip>
        )}
      </div>
    </Card></Ctx>
  )
}

// EditableText：点击即编辑，失焦保存。
// wrap：单行内容但允许折行显示（如标题），回车保存而不是换行。
export function EditableText({ value, onSave, placeholder, multiline, wrap, className }: { value: string; onSave: (v: string) => void; placeholder: string; multiline?: boolean; wrap?: boolean; className?: string }) {
  const [v, setV] = useState(value)
  useEffect(() => setV(value), [value])
  const commit = () => v.trim() !== value.trim() && onSave(v.trim())
  // 平时看起来就是普通文字，悬停出现边框，聚焦时变成输入框。
  const cls = cx(
    'block w-full min-w-0 rounded-lg border border-transparent bg-transparent px-3 text-[15px] text-fg transition-[border,background,box-shadow] placeholder:text-fg-3 hover:border-line focus:border-accent focus:bg-surface focus:ring-3 focus:ring-[var(--ring)] focus:outline-none sm:text-sm',
    className,
  )
  if (wrap)
    return (
      <Textarea
        value={v}
        minRows={1}
        placeholder={placeholder}
        onChange={(e) => setV(e.target.value.replace(/\n/g, ' '))}
        onBlur={commit}
        onKeyDown={(e) => {
          if (e.key === 'Enter' && !e.nativeEvent.isComposing) {
            e.preventDefault()
            ;(e.target as HTMLTextAreaElement).blur()
          }
        }}
        unstyled
        className={cx(cls, 'resize-none')}
      />
    )
  return multiline ? (
    <Textarea value={v} minRows={2} placeholder={placeholder} onChange={(e) => setV(e.target.value)} onBlur={commit} unstyled className={cx(cls, 'resize-none py-2 leading-relaxed')} />
  ) : (
    <input value={v} placeholder={placeholder} onChange={(e) => setV(e.target.value)} onBlur={commit} onKeyDown={(e) => e.key === 'Enter' && (e.target as HTMLInputElement).blur()} className={cx(cls, 'h-9')} />
  )
}


// NewReqModal：新建需求抽屉（桌面右侧、手机底部）。上面写清楚要做什么，下面决定交给谁：
// “仅新建”只建需求并关联选中的会话（随时可以开工）；“新建并交出”立刻把它交给会话开工。
export function NewReqModal({ open, onClose, defaultProject, defaultSession }: { open: boolean; onClose: () => void; defaultProject?: string; defaultSession?: string }) {
  const projects = useProjects()
  const sessions = useSessions()
  const { createReq, linkReq } = useActions()
  const assign = useAssign()
  const openReq = useOpenReq()
  const [busy, wrap] = useBusy()
  // 项目默认用上次新建时选的（页面已按项目筛选时以筛选为准），连续新建不用每次切
  const lastProject = () => defaultProject || loadPref<string>('ks.newreq.project', '') || undefined
  // 默认草稿（R-19）：“仅新建”不会被自动排进会话的待办；“新建并交出”时会改为待开始
  const blank = (): ReqFields => ({ title: '', status: 'draft', priority: 'P2', project_id: lastProject() })
  const lastUsed = loadPref<string>('ks.newreq.project', '')
  const [f, setF] = useState<ReqFields>(blank)
  const [files, setFiles] = useState<FileRef[]>([])
  const [more, setMore] = usePref('ks.newreq.more', false) // 连续新建：建完清空表单，抽屉不关
  // 交给哪个会话：默认同项目里待命或刚活跃过的会话；手动选过就以你的为准
  const [target, setTarget] = useState<string | null | undefined>(undefined)
  const all = sessions.data?.sessions ?? []
  const to = target === undefined ? defaultTarget(all, f.project_id) : all.find((s) => s.key === target)
  useEffect(() => {
    if (open) {
      // 从会话详情里新建：默认交给这个会话、用它所在的项目（R-50）
      const s = defaultSession ? all.find((x) => x.key === defaultSession) : undefined
      setF(s?.project_id ? { ...blank(), project_id: s.project_id } : blank())
      setTarget(defaultSession)
      setFiles([])
    }
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [open, defaultProject, defaultSession])
  const submit = (send: boolean) =>
    wrap(async () => {
      if (!f.title.trim()) return
      const r = await createReq({ ...f, title: f.title.trim(), files: files.map((x) => x.id) })
      if (!r) return
      savePref('ks.newreq.project', f.project_id ?? '')
      if (to && send) await assign(r.code, to)
      else {
        if (to) await linkReq(r.code, to.key)
        // 建完直接打开这条需求时不再弹提示：它就在眼前，提示条反而盖住抽屉（R-72）
        if (more || defaultSession) toast.success(to ? t.reqs.createdLinked(r.code, targetLabel(to)) : t.reqs.created(r.code))
      }
      if (more) {
        setF({ ...blank(), project_id: f.project_id, status: f.status, priority: f.priority, kind: f.kind })
        setFiles([])
      } else {
        onClose()
        if (!defaultSession) openReq(r.code) // 从会话详情新建时留在会话详情，新需求出现在它的需求里
      }
    })
  const desktop = useDesktop()
  const proj = projects.data?.projects.find((p) => p.id === f.project_id)
  const chip = 'inline-flex max-w-[220px] items-center gap-1.5 rounded-lg border border-line bg-surface px-2.5 py-1.5 text-sm text-fg-2 transition hover:border-line-strong hover:text-fg active:scale-[0.97]'
  // 手机上不挂轻点提示：一点就弹出的提示会盖住旁边的状态、优先级
  const designToggle = (
              <button aria-pressed={f.kind === 'design'} onClick={() => setF({ ...f, kind: f.kind === 'design' ? 'feature' : 'design' })} className={cx(chip, f.kind === 'design' && 'border-accent/50 bg-accent-soft text-accent hover:text-accent')}>
                <Palette className="size-3.5" />
                {t.design.toggle}
              </button>
  )
  const titleInput = (
    <input
      autoFocus
      value={f.title}
      placeholder={t.reqs.titlePlaceholder}
      onChange={(e) => setF({ ...f, title: e.target.value })}
      className="w-full bg-transparent font-display text-[22px] text-fg outline-none placeholder:text-fg-3"
    />
  )
  const fields = (
    <>
          <section>
            <h3 className="mb-1.5 text-xs font-medium text-fg-3">{t.reqs.detail}</h3>
            <AttachZone value={files} onChange={setFiles} compact>
              <Textarea minRows={7} value={f.detail ?? ''} placeholder={t.reqs.detailPlaceholderLong} onChange={(e) => setF({ ...f, detail: e.target.value })} className="pr-10" />
            </AttachZone>
          </section>
          <div className="flex flex-wrap items-center gap-2">
            <Menu
              align="start"
              trigger={
                <button className={chip}>
                  <span className={cx('size-2 rounded-full', statusCls[f.status ?? 'ready'].dot)} />
                  {t.reqStatus[f.status ?? 'ready']}
                  <ChevronDown className="size-3 opacity-60" />
                </button>
              }
              items={allStatuses.map((s) => ({ label: t.reqStatus[s], hint: t.reqStatusHint[s], checked: s === f.status, onSelect: () => setF({ ...f, status: s as ReqFields['status'] }) }))}
            />
            <Menu
              align="start"
              trigger={
                <button className={chip}>
                  <PriorityDot p={f.priority ?? 'P2'} />
                  {t.priority[f.priority ?? 'P2']}
                  <ChevronDown className="size-3 opacity-60" />
                </button>
              }
              items={priorities.map((p) => ({ label: t.priority[p], checked: p === f.priority, icon: <PriorityDot p={p} />, onSelect: () => setF({ ...f, priority: p as ReqFields['priority'] }) }))}
            />
            <Menu
              align="start"
              trigger={
                <button className={chip}>
                  <FolderGit2 className="size-3.5 opacity-70" />
                  <span className="truncate">{proj?.name ?? t.reqs.noProject}</span>
                  <ChevronDown className="size-3 opacity-60" />
                </button>
              }
              items={[{ label: t.reqs.noProject, checked: !f.project_id, onSelect: () => setF({ ...f, project_id: undefined }) }, ...(projects.data?.projects ?? []).filter((p) => !p.hidden || p.id === f.project_id).sort((a, b) => Number(b.id === lastUsed) - Number(a.id === lastUsed)).map((p) => ({ label: p.name, hint: p.id === lastUsed ? [t.reqs.lastUsed, p.remote].filter(Boolean).join(t.common.sep) : p.remote, checked: p.id === f.project_id, onSelect: () => setF({ ...f, project_id: p.id }) }))]}
            />
            {desktop ? <Tip content={t.design.makeHint}>{designToggle}</Tip> : designToggle}
          </div>

          <section>
            <h3 className="mb-1.5 text-xs font-medium text-fg-3">{t.reqs.acceptance}</h3>
            <Textarea minRows={3} value={f.acceptance ?? ''} placeholder={t.reqs.acceptancePlaceholder} onChange={(e) => setF({ ...f, acceptance: e.target.value })} />
          </section>
    </>
  )
  const targetRow = (
          <div className="flex items-center gap-2 rounded-xl bg-surface-2 px-3 py-2">
            <Send className="size-4 shrink-0 text-fg-3" />
            <span className="shrink-0 text-[13px] text-fg-3">{t.assign.field}</span>
            <Menu
              align="start"
              width="w-80"
              trigger={
                <button className="flex min-w-0 flex-1 items-center gap-2 rounded-lg px-1.5 py-1 text-left text-sm text-fg hover:bg-hover">
                  {to && (targetGroup(to) === 'now' ? <Zap className="size-4 shrink-0 text-accent" /> : <StatusDot state={displayState(to)} />)}
                  <span className="min-w-0 flex-1 truncate">{to ? targetLabel(to) : t.assign.nobody}</span>
                  {to && <span className={cx('shrink-0 text-xs', targetGroup(to) === 'now' ? 'text-accent' : 'text-fg-3')}>{t.assign.when[targetGroup(to)]}</span>}
                  <ChevronDown className="size-3 shrink-0 opacity-60" />
                </button>
              }
              items={[{ label: t.assign.nobody, checked: !to, onSelect: () => setTarget(null) }, { type: 'separator' }, ...targetItems(all, f.project_id, (s) => setTarget(s.key), { checked: to?.key })]}
            />
          </div>
  )
  const moreToggle = (
    <label className="ml-auto flex cursor-pointer items-center gap-2 text-xs text-fg-3">
            <Switch checked={more} onChange={setMore} />
            {t.reqs.createMore}
          </label>
  )
  const onKeyDown = (e: React.KeyboardEvent) => {
    if ((e.metaKey || e.ctrlKey) && e.key === 'Enter') {
      e.preventDefault()
      void submit(true)
    }
  }
  // 手机：全屏编辑页，提交按钮在顶栏（键盘挡不住），交给谁、连续新建放在内容末尾（R-72）
  if (!desktop)
    return (
      <FullSheet
        open={open}
        onClose={onClose}
        title={t.reqs.newTitle}
        header={
          <>
            <Button variant="ghost" onClick={onClose}>
              {t.common.cancel}
            </Button>
            <span className="min-w-0 flex-1 truncate text-center text-[15px] font-semibold text-fg">{t.reqs.newTitle}</span>
            <Button variant="primary" loading={busy} disabled={!f.title.trim()} onClick={() => void submit(true)} icon={to ? <Send className="size-4" /> : <Plus className="size-4" />}>
              {to ? t.assign.sendShort : t.common.create}
            </Button>
          </>
        }
      >
        <div className="space-y-4 px-4 pt-4 pb-[max(2rem,env(safe-area-inset-bottom))]" onKeyDown={onKeyDown}>
          {titleInput}
          {fields}
          {targetRow}
          <div className="flex items-center gap-3">
            {moreToggle}
            {to && (
              <Button size="sm" variant="ghost" className="-mr-2" disabled={!f.title.trim() || busy} onClick={() => void submit(false)}>
                {t.reqs.createOnly}
              </Button>
            )}
          </div>
        </div>
      </FullSheet>
    )
  return (
    <Panel open={open} onClose={onClose} wide bare title={t.reqs.newTitle}>
      <div className="flex min-h-0 flex-1 flex-col" onKeyDown={onKeyDown}>
        <div className="flex shrink-0 items-center gap-2 px-5 pt-4 pb-2">
          <h2 className="font-display text-lg text-fg">{t.reqs.newTitle}</h2>
          {moreToggle}
          <Button size="icon-sm" variant="ghost" aria-label={t.common.close} onClick={onClose}>
            <X className="size-4" />
          </Button>
        </div>
        <div className="min-h-0 flex-1 space-y-4 overflow-y-auto overscroll-contain px-5 pt-2 pb-5">
          {titleInput}
          {fields}
        </div>
        <div className="shrink-0 space-y-3 border-t border-line bg-surface px-5 pt-3 pb-[max(0.875rem,env(safe-area-inset-bottom))]">
          {targetRow}
          <div className="flex items-center gap-2">
            <span className="min-w-0 flex-1 truncate text-xs text-fg-3 max-sm:hidden">{to ? t.reqs.createHintTo : t.reqs.createHint}</span>
            {to ? (
              <>
                <Button className="ml-auto" disabled={!f.title.trim() || busy} onClick={() => void submit(false)}>
                  {t.reqs.createOnly}
                </Button>
                <Button variant="primary" loading={busy} disabled={!f.title.trim()} onClick={() => void submit(true)} icon={<Send className="size-4" />}>
                  {t.assign.createAndSend}
                </Button>
              </>
            ) : (
              <Button className="ml-auto" variant="primary" loading={busy} disabled={!f.title.trim()} onClick={() => void submit(false)} icon={<Plus className="size-4" />}>
                {t.common.create}
              </Button>
            )}
          </div>
        </div>
      </div>
    </Panel>
  )
}
