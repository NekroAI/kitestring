import { useEffect, useState } from 'react'
import { useSearchParams } from 'react-router'
import { ChevronDown, LockOpen, Palette, Plus, Send, X, Zap } from 'lucide-react'
import { toast } from 'sonner'
import type { ReqFields, Requirement } from '../api/client'
import { Md } from '../ui/markdown'
import { useProjects, useSessions } from '../api/queries'
import { defaultTarget, targetGroup, targetItems, targetLabel, useAssign } from './assign'
import { t } from '../i18n'
import { cx, loadPref, savePref, useDesktop, usePref } from '../lib/util'
import { Badge, Button, Switch, Textarea, Tip } from '../ui/base'
import { FullSheet, Menu, Panel, useAskReason, type MenuItem } from '../ui/overlay'
import { done, schedule, usePendingMap, useReqVerdict } from './verdict'
import { displayState, presence, PriorityDot, RoleTag, StatusDot } from '../ui/marks'
import { useActions, useBusy } from './actions'
import { AttachZone, type FileRef } from '../ui/attach'
import { useOpenReq } from './nav'
import { ProjGlyph, ReqGlyph, statusCls } from '../ui/refmark'
import { DraftLine, dropDraft, useDraft } from './draft'
import { hasContent } from '../lib/drafts'

export const boardStatuses = ['draft', 'ready', 'in_progress', 'blocked', 'review'] as const
export const closedStatuses = ['done', 'shelved'] as const
export const allStatuses = [...boardStatuses, ...closedStatuses]
export const priorities = ['P0', 'P1', 'P2', 'P3']

// 状态配色与纸片印记统一放在 ui/refmark（R-96）。
export { statusCls } from '../ui/refmark'

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

// useSetStatus：修改状态（右键菜单、状态菜单、拖拽、逐条确认草稿共用）。R-161：一律先进撤销期，不再弹确认框；
// R-201：需求立刻显示在新列（to），块上盖撤销条遮罩，走完即生效；块不在屏幕上时撤回出现在顶部浮动条——
// 待验收 → 已完成就是通过；待验收 → 其它状态就是退回，先要一句意见（必填），送达后再落到目标状态（证据照旧作废，D19）。
// 返回是否排上了（取消意见框 / 搁置框、已在撤销期里时是 false），逐条确认草稿时据此决定要不要前进（R-133）。
// R-177：验收只在抽屉里做——在列表、盘面上把待验收改走（拖拽、状态菜单）时打开这条需求的抽屉，由底部的判断完成；已经在它的抽屉里时照旧。
export function useSetStatus() {
  const { updateReq } = useActions()
  const verdict = useReqVerdict()
  const ask = useAskReason()
  const openReq = useOpenReq()
  const [params] = useSearchParams()
  return async (r: Requirement, status: string): Promise<boolean> => {
    if (status === r.status) return false
    if (r.status === 'review' && params.get('r') !== r.code && params.get('a') !== `r:${r.code}`) {
      openReq(r.code)
      return false
    }
    if (r.status === 'review' && status === 'done') return verdict.pass(r.code)
    const to = (patch: Partial<ReqFields> = {}) => toFields(r, { status: status as ReqFields['status'], ...patch })
    if (r.status === 'review') {
      const key = `reason:${r.code}:back`
      const a = await ask({ title: t.verdict.askTitle(r.code), description: t.verdict.askDesc, confirmText: t.verdict.back('accept'), placeholder: t.verdict.askPlaceholder, kinds: [], required: true, multiline: true, draft: key })
      if (!a) return false
      // 退回后需求回到进行中；拖到的是别的列（阻塞、待开始……）时再改过去。送达后才清意见草稿（撤销期里撤回时还在）
      return verdict.back(r.code, {
        comment: a.note,
        to: status,
        after: () => {
          dropDraft(key)
          if (status !== 'in_progress') void updateReq(r.code, to())
        },
      })
    }
    // 搁置（R-157）：问一句原因，类别和一句话都可以不填；取消就不搁置
    let why: Partial<ReqFields> = {}
    const shelveKey = `reason:${r.code}:shelve`
    if (status === 'shelved') {
      const a = await ask({
        draft: shelveKey,
        title: t.reqs.shelve.title,
        description: t.reqs.shelve.desc,
        confirmText: t.reqs.shelve.confirm,
        placeholder: t.reqs.shelve.placeholder,
        kinds: Object.entries(t.reqs.shelve.kinds).map(([value, label]) => ({ value, label })),
      })
      if (!a) return false
      why = { shelve_kind: (a.kind || undefined) as ReqFields['shelve_kind'], shelve_reason: a.note || undefined }
    }
    const body = to(why)
    return schedule(r.code, {
      act: 'move',
      label: t.verdict.move(t.reqStatus[status]),
      subject: r.code,
      to: status,
      beacon: { method: 'PUT', url: `/api/requirements/${encodeURIComponent(r.code)}`, body },
      commit: () => done(updateReq(r.code, body), status === 'shelved' ? () => dropDraft(shelveKey) : undefined),
    })
  }
}

// useWithdrawRelease：撤回放行（R-166）同样有 3 秒撤销期（R-201 / D-413），按钮换成撤销条；键 release:R-12。
export const releaseKey = (code: string) => `release:${code}`
export function useWithdrawRelease() {
  const { withdrawRelease } = useActions()
  const pend = usePendingMap()
  return {
    pending: (code: string) => pend.has(releaseKey(code)),
    withdraw: (code: string, reason = '', after?: () => void) =>
      schedule(releaseKey(code), {
        act: 'move',
        label: t.verdict.withdrawRelease,
        subject: code,
        beacon: { method: 'POST', url: `/api/requirements/${encodeURIComponent(code)}/release/withdraw`, body: { reason } },
        commit: () => done(withdrawRelease(code, reason), after),
      }),
  }
}

export function statusItems(r: Requirement, setStatus: (r: Requirement, s: string) => void): MenuItem[] {
  return allStatuses.map((s) => ({
    label: t.reqStatus[s],
    hint: t.reqStatusHint[s],
    checked: s === r.status,
    icon: <ReqGlyph status={s} />,
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

// ReleaseBadge：PM / Adv 放行的需求（R-166），待开始 / 进行中 / 阻塞时在卡片上标出放行人，悬停看理由
export function ReleaseBadge({ r, className }: { r: Requirement; className?: string }) {
  if (!r.release || (r.status !== 'ready' && r.status !== 'in_progress' && r.status !== 'blocked')) return null
  const who = t.role[r.release.role] || r.release.role || 'Agent'
  return (
    <Tip content={t.timeline.releaseTip(who, r.release.reason)}>
      <span className={cx('inline-flex min-w-0 shrink-0', className)}>
        <Badge tone="accent" icon={<LockOpen className="size-3" />}>
          {t.timeline.releaseBadge(who)}
        </Badge>
      </span>
    </Tip>
  )
}

// useEditDraft：编辑已有内容的草稿（R-202）：draftKey 为空时不缓存。和原文一样就不算草稿；
// 失焦保存后清掉；「清空」只清文字（撤销期里失焦不保存空内容），「恢复原文」回到编辑前。
function useEditDraft(draftKey: string | undefined, value: string) {
  const dr = useDraft<{ text: string }>(draftKey, { text: value }, (v) => v.text !== value)
  return dr
}

// EditableText：点击即编辑，失焦保存。
// wrap：单行内容但允许折行显示（如标题），回车保存而不是换行。draftKey：编辑中的内容存草稿（edit:R-12:title）。
export function EditableText({ value, onSave, placeholder, multiline, wrap, className, draftKey }: { value: string; onSave: (v: string) => void; placeholder: string; multiline?: boolean; wrap?: boolean; className?: string; draftKey?: string }) {
  const dr = useEditDraft(draftKey, value)
  const v = dr.value.text
  const setV = (text: string) => dr.set({ text })
  const commit = () => {
    if (dr.clearing && !v.trim()) return // 刚点了清空：等撤销期走完，不把空内容存进去
    if (v.trim() !== value.trim()) onSave(v.trim())
    dr.drop()
  }
  // 平时看起来就是普通文字，悬停出现边框，聚焦时变成输入框。
  const cls = cx(
    'block w-full min-w-0 rounded-lg border border-transparent bg-transparent px-3 text-[15px] text-fg transition-[border,background,box-shadow] placeholder:text-fg-3 hover:border-line focus:border-accent focus:bg-surface focus:ring-3 focus:ring-[var(--ring)] focus:outline-none sm:text-sm',
    className,
  )
  const line = draftKey && <DraftLine h={dr} empty={{ text: '' }} revert className="px-3" />
  if (wrap)
    return (
      <>
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
        {line}
      </>
    )
  return (
    <>
      {multiline ? (
        <Textarea value={v} minRows={2} placeholder={placeholder} onChange={(e) => setV(e.target.value)} onBlur={commit} unstyled className={cx(cls, 'resize-none py-2 leading-relaxed')} />
      ) : (
        <input value={v} placeholder={placeholder} onChange={(e) => setV(e.target.value)} onBlur={commit} onKeyDown={(e) => e.key === 'Enter' && (e.target as HTMLInputElement).blur()} className={cx(cls, 'h-9')} />
      )}
      {line}
    </>
  )
}


// EditableMd：展示态按 Markdown 渲染（R-149），点击进入编辑变成原文输入框，失焦保存后回到渲染态。
// 点链接、编号印记不进入编辑。有没保存的草稿时（关抽屉、刷新前正在改）直接停在编辑态，内容是上次写到的。
export function EditableMd({ value, onSave, placeholder, className, draftKey }: { value: string; onSave: (v: string) => void; placeholder: string; className?: string; draftKey?: string }) {
  const [editing, setEditing] = useState(false)
  const dr = useEditDraft(draftKey, value)
  if (editing || dr.dirty || dr.clearing)
    return (
      <div>
        <EditableMdInput
          v={dr.value.text}
          onChange={(text) => dr.set({ text })}
          focus={editing}
          placeholder={placeholder}
          className={className}
          onDone={(save) => {
            const v = dr.value.text
            if (save && dr.clearing && !v.trim()) return setEditing(false)
            setEditing(false)
            if (save && v.trim() !== value.trim()) onSave(v.trim())
            dr.drop()
          }}
        />
        {draftKey && <DraftLine h={dr} empty={{ text: '' }} revert className="px-3" />}
      </div>
    )
  return (
    <div
      role="button"
      tabIndex={0}
      onClick={(e) => {
        if ((e.target as HTMLElement).closest('a,button')) return
        if (window.getSelection()?.toString()) return
        setEditing(true)
      }}
      onKeyDown={(e) => e.key === 'Enter' && e.target === e.currentTarget && setEditing(true)}
      className={cx('block w-full min-w-0 cursor-text rounded-lg border border-transparent px-3 py-2 text-[15px] text-fg transition-[border] hover:border-line sm:text-sm', className)}
    >
      {value.trim() ? <Md text={value} /> : <span className="text-fg-3">{placeholder}</span>}
    </div>
  )
}

// onDone(true) 失焦保存，onDone(false) Esc 放弃
function EditableMdInput({ v, onChange, onDone, focus, placeholder, className }: { v: string; onChange: (v: string) => void; onDone: (save: boolean) => void; focus: boolean; placeholder: string; className?: string }) {
  return (
    <Textarea
      autoFocus={focus}
      value={v}
      minRows={3}
      placeholder={placeholder}
      onChange={(e) => onChange(e.target.value)}
      onBlur={() => onDone(true)}
      onKeyDown={(e) => e.key === 'Escape' && onDone(false)}
      unstyled
      className={cx('block w-full min-w-0 resize-none rounded-lg border border-accent bg-surface px-3 py-2 text-[15px] leading-relaxed text-fg ring-3 ring-[var(--ring)] outline-none placeholder:text-fg-3 sm:text-sm', className)}
    />
  )
}

// NewReqModal：新建需求抽屉（桌面右侧、手机底部）。上面写清楚要做什么，下面决定交给谁：
// “仅新建”只建需求并关联选中的会话（随时可以开工）；“新建并交出”立刻把它交给会话开工。
type NewReq = Omit<ReqFields, 'files'> & { refs: FileRef[] }

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
  const all0 = () => sessions.data?.sessions ?? []
  const lastUsed = loadPref<string>('ks.newreq.project', '')
  // 草稿（R-202）：newreq，关掉（含手机下拉、点外面）、刷新后再打开还在，建成功才清。
  // 只有标题、描述、验收标准、贴图算内容；状态、优先级、项目跟着存，没内容时按当次打开的默认
  const s0 = defaultSession ? all0().find((x) => x.key === defaultSession) : undefined
  const [sticky, setSticky] = useState<Partial<ReqFields>>({}) // 连续新建：上一条的状态 / 优先级 / 项目 / 类型
  const init: NewReq = { ...blank(), ...(s0?.project_id ? { project_id: s0.project_id } : {}), ...sticky, refs: [] }
  const dr = useDraft<NewReq>('newreq', init, (v) => hasContent([v.title, v.detail, v.acceptance, v.refs]), open)
  const { refs: files, ...f } = dr.value
  const setF = (v: Omit<ReqFields, 'files'>) => dr.set(v)
  const setFiles = (v: FileRef[]) => dr.set({ refs: v })
  const [more, setMore] = usePref('ks.newreq.more', false) // 连续新建：建完清空表单，抽屉不关
  // 交给哪个会话：默认同项目里待命或刚活跃过的会话；手动选过就以你的为准
  const [target, setTarget] = useState<string | null | undefined>(undefined)
  const all = sessions.data?.sessions ?? []
  const to = target === undefined ? defaultTarget(all, f.project_id) : all.find((s) => s.key === target)
  // 从会话详情里新建：默认交给这个会话、用它所在的项目（R-50）
  useEffect(() => {
    if (open) {
      setTarget(defaultSession)
      setSticky({})
    }
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
      dr.drop()
      if (more) setSticky({ project_id: f.project_id, status: f.status, priority: f.priority, kind: f.kind })
      else {
        onClose()
        if (!defaultSession) openReq(r.code) // 从会话详情新建时留在会话详情，新需求出现在它的需求里
      }
    })
  const desktop = useDesktop()
  const proj = projects.data?.projects.find((p) => p.id === f.project_id)
  const chip = 'inline-flex max-w-[220px] items-center gap-1.5 rounded-lg border border-line bg-surface px-2.5 py-1.5 text-sm text-fg-2 transition hover:border-line-strong hover:text-fg active:scale-[0.97]'
  const designToggle = (
              <button aria-pressed={f.kind === 'design'} onClick={() => setF({ ...f, kind: f.kind === 'design' ? 'feature' : 'design' })} className={cx(chip, f.kind === 'design' && 'border-accent/50 bg-accent-soft text-accent hover:text-accent')}>
                <Palette className="size-3.5" />
                {t.design.toggle}
              </button>
  )
  const titleInput = (
    <div>
      <input
        autoFocus
        value={f.title}
        placeholder={t.reqs.titlePlaceholder}
        onChange={(e) => setF({ ...f, title: e.target.value })}
        className="w-full bg-transparent font-display text-[22px] text-fg outline-none placeholder:text-fg-3"
      />
      <DraftLine h={dr} />
    </div>
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
                  <ReqGlyph status={f.status ?? 'ready'} />
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
                  <ProjGlyph />
                  <span className="truncate">{proj?.name ?? t.reqs.noProject}</span>
                  <ChevronDown className="size-3 opacity-60" />
                </button>
              }
              items={[{ label: t.reqs.noProject, checked: !f.project_id, onSelect: () => setF({ ...f, project_id: undefined }) }, ...(projects.data?.projects ?? []).filter((p) => !p.hidden || p.id === f.project_id).sort((a, b) => Number(b.id === lastUsed) - Number(a.id === lastUsed)).map((p) => ({ label: p.name, hint: p.id === lastUsed ? [t.reqs.lastUsed, p.remote].filter(Boolean).join(t.common.sep) : p.remote, checked: p.id === f.project_id, onSelect: () => setF({ ...f, project_id: p.id }) }))]}
            />
            {designToggle}
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
                  {to?.role && <RoleTag role={to.role} presence={presence(to) || undefined} />}
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
