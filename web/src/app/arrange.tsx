import { useState } from 'react'
import { Pin, Plus } from 'lucide-react'
import { toast } from 'sonner'
import type { Arrangement, Session } from '../api/client'
import { useArrangements, useProjects } from '../api/queries'
import { t } from '../i18n'
import { roleName } from '../i18n/roles'
import { cx } from '../lib/util'
import { Button, Segmented, Textarea, Tip } from '../ui/base'
import { Modal } from '../ui/overlay'
import { Linkify } from '../ui/refmark'
import { useActions } from './actions'
import { DraftLine, useDraft } from './draft'
import { PendingCover, schedule, usePendingMap } from './verdict'

// 当前安排条（R-245）：工作台简报条下方的一条小条，列各项目生效中的安排（用户口头交代的临时安排，如「额度紧，做完这几条就停」「这次可以 push」）。
//   - 每条一行、固定高度：内容 · 谁记的 · 到期时间 ·「撤销」；撤销走全站统一的 3 秒撤销期（docs/principles.md 第 8 条）
//   - 右边「添加」：你在网页上也能记一条，署名是你；接任主调度 / 主助理的会话从纸鸢读到，不靠交接转述
//   - 留痕不删：撤销 / 到期的不再显示，记录还在

const arrangeKey = (a: Arrangement) => `arrange:${a.code}`

function fmtAt(s: string) {
  const d = new Date(s)
  const pad = (n: number) => String(n).padStart(2, '0')
  return `${pad(d.getMonth() + 1)}-${pad(d.getDate())} ${pad(d.getHours())}:${pad(d.getMinutes())}`
}

function who(a: Arrangement) {
  if (!a.by_session) return t.arrange.byUser
  const name = a.by_title || a.by_session
  return a.by_role ? `${roleName(a.by_role) ?? ''}「${name}」` : `「${name}」`
}

function AddArrange({ open, onClose, sessions }: { open: boolean; onClose: () => void; sessions: Session[] }) {
  const { addArrangement } = useActions()
  // 可选的项目：工作台上有会话的（没隐藏的）；默认记到有主调度 / 主助理在线的那个项目
  const all = (useProjects().data?.projects ?? []).filter((p) => !p.hidden)
  const used = new Set(sessions.map((s) => s.project_id).filter(Boolean))
  const projects = all.some((p) => used.has(p.id)) ? all.filter((p) => used.has(p.id)) : all
  const live = sessions.find((s) => (s.role === 'pm' || s.role === 'aide') && s.state !== 'ended' && s.project_id)?.project_id
  const dr = useDraft<{ text: string }>('arrange:new', { text: '' }, undefined, open)
  const [pid, setPid] = useState('')
  const [until, setUntil] = useState('')
  const [busy, setBusy] = useState(false)
  const project = pid || live || projects[0]?.id || ''
  const go = async () => {
    const text = dr.value.text.trim()
    if (!text || !project) return
    setBusy(true)
    const r = await addArrangement({ project_id: project, text, until: until || undefined })
    setBusy(false)
    if (r) {
      dr.drop()
      toast.success(t.arrange.saved)
      onClose()
    }
  }
  return (
    <Modal
      open={open}
      onClose={onClose}
      title={t.arrange.addTitle}
      description={t.arrange.addIntro}
      footer={
        <>
          <Button onClick={onClose}>{t.common.cancel}</Button>
          <Button variant="primary" disabled={busy || !dr.value.text.trim() || !project} onClick={go}>
            {t.arrange.save}
          </Button>
        </>
      }
    >
      <div className="space-y-4 pb-1">
        {projects.length > 1 && (
          <section className="space-y-1.5">
            <h4 className="text-[13px] font-medium text-fg-2">{t.arrange.project}</h4>
            <select
              value={project}
              onChange={(e) => setPid(e.target.value)}
              className="block h-9 w-full min-w-0 rounded-lg border border-line bg-surface px-3 text-sm text-fg shadow-xs hover:border-line-strong focus:border-accent focus:outline-none"
            >
              {projects.map((p) => (
                <option key={p.id} value={p.id}>
                  {p.name}
                </option>
              ))}
            </select>
          </section>
        )}
        <section className="space-y-1.5">
          <h4 className="text-[13px] font-medium text-fg-2">{t.arrange.text}</h4>
          <Textarea value={dr.value.text} onChange={(e) => dr.set({ text: e.target.value })} placeholder={t.arrange.placeholder} maxLength={200} minRows={2} autoFocus />
          <DraftLine h={dr} />
        </section>
        <section className="space-y-1.5">
          <h4 className="text-[13px] font-medium text-fg-2">{t.arrange.until}</h4>
          <Segmented value={until} onChange={setUntil} options={t.arrange.untils} />
        </section>
      </div>
    </Modal>
  )
}

export function ArrangeStrip({ sessions, className }: { sessions: Session[]; className?: string }) {
  const q = useArrangements()
  const { revokeArrangement } = useActions()
  const pend = usePendingMap()
  const [adding, setAdding] = useState(false)
  // 撤销期走完（已提交或在提交）就在本地离开小条，不等重新取数；提交失败时放回
  const list = (q.data ?? []).filter((a) => {
    const e = pend.get(arrangeKey(a))
    return !e || e.phase === 'wait'
  })
  const multi = new Set(list.map((a) => a.project_id)).size > 1
  const revoke = (a: Arrangement) =>
    schedule(arrangeKey(a), {
      act: 'move',
      label: t.arrange.revokeAct,
      subject: a.code,
      beacon: { method: 'POST', url: `/api/arrangements/${a.code}/revoke`, body: {} },
      commit: async () => !!(await revokeArrangement(a.code)),
    })
  if (q.isPending) return null
  const add = (
    <Button size="sm" variant="ghost" className="shrink-0" onClick={() => setAdding(true)}>
      <Plus className="size-3.5" />
      {t.arrange.add}
    </Button>
  )
  return (
    <section aria-label={t.arrange.title} className={cx('min-w-0 overflow-hidden rounded-xl bg-surface shadow-card', className)}>
      {list.length === 0 ? (
        <div className="flex h-9 min-w-0 items-center gap-2 pr-1.5 pl-3.5 text-[12.5px]">
          <Pin className="size-3.5 shrink-0 text-fg-3" />
          <span className="shrink-0 font-medium text-fg-2">{t.arrange.title}</span>
          <span className="min-w-0 flex-1 truncate text-fg-3">{t.arrange.empty}</span>
          {add}
        </div>
      ) : (
        <ul className="divide-y divide-line">
          {list.map((a, i) => (
            <li key={a.code} className="relative flex h-9 min-w-0 items-center gap-2 pr-1.5 pl-3.5 text-[13px]">
              <Pin className="size-3.5 shrink-0 text-accent" />
              {i === 0 && <span className="shrink-0 font-medium text-fg-2 max-sm:hidden">{t.arrange.title}</span>}
              {multi && <span className="shrink-0 text-xs text-fg-3 max-sm:hidden">{a.project_name}</span>}
              <Tip content={a.text} truncated>
                <span className="min-w-0 flex-1 truncate text-fg">
                  <Linkify text={a.text} />
                </span>
              </Tip>
              <span className="shrink-0 text-xs text-fg-3 tabular max-md:hidden">
                {who(a)}
                {t.common.sep}
                {a.expires_at ? t.arrange.until_(fmtAt(a.expires_at)) : t.arrange.forever}
              </span>
              <Button size="sm" variant="ghost" className="shrink-0" onClick={() => revoke(a)}>
                {t.arrange.revoke}
              </Button>
              {i === 0 && add}
              <PendingCover k={arrangeKey(a)} />
            </li>
          ))}
        </ul>
      )}
      <AddArrange open={adding} onClose={() => setAdding(false)} sessions={sessions} />
    </section>
  )
}
