import { useEffect, useLayoutEffect, useMemo, useRef, useState, type ReactNode } from 'react'
import { useSearchParams } from 'react-router'
import { AnimatePresence, motion } from 'motion/react'
import { Check, CheckCheck, ChevronDown, CornerDownLeft, ChevronLeft, ChevronRight, ExternalLink, Link2, MoreHorizontal, Trash2, Undo2, X } from 'lucide-react'
import { toast } from 'sonner'
import type { ReqFields, Requirement } from '../api/client'
import { useDecisions, useProjects, useRequirement, useRequirements, useSessions } from '../api/queries'
import { t } from '../i18n'
import { cx, relTime, useNow, usePref } from '../lib/util'
import { Button, ErrorState, Skeleton, Textarea, Tip } from '../ui/base'
import { AttachZone, type FileRef } from '../ui/attach'
import { AnnotateTarget } from '../ui/annotate'
import { Collapse } from '../ui/motion'
import { Menu, Panel, useConfirm } from '../ui/overlay'
import { PriorityDot, StatusDot } from '../ui/marks'
import { useActions, useBusy } from './actions'
import { useOpenSession } from './nav'
import { EditableText, priorities, StatusMenu, statusCls, toFields } from './requirements'
import { ReqTimeline } from './reqTimeline'
import { AssignButton } from './assign'
import { DesignBadge } from './design'
import { PlanBadge } from './plans'
import { OwnLine } from './ownership'

// 需求抽屉：从上到下是“需求是什么”（固定）→ 来回的记录（滚动，最新在下）→ 轮到你做的事（固定在底部）。
// 验收时可以在同一个抽屉里连续处理同一项目的待验收：通过后自动滑到下一条，顶部左右按钮切换。

const prio: Record<string, number> = { P0: 0, P1: 1, P2: 2, P3: 3 }
const sortReqs = (a: Requirement, b: Requirement) => (prio[a.priority] ?? 9) - (prio[b.priority] ?? 9) || b.updated_at.localeCompare(a.updated_at)
const closed = (r: Requirement) => r.status === 'done' || r.status === 'shelved'

type Dir = 'next' | 'prev' | 'pass'

// ---------- 主体 ----------

export function ReqView({ refId, top, onPassed }: { refId: string; top?: ReactNode; onPassed?: (code: string) => void }) {
  const q = useRequirement(refId)
  const decisions = useDecisions()
  const r = q.data
  const related = useMemo(() => (decisions.data ?? []).filter((d) => r && d.requirement_id === r.id), [decisions.data, r])
 const scroller = useRef<HTMLDivElement>(null)
  // 看图时“标注并附上”：标注后的图片送进底部输入区
  const [incoming, setIncoming] = useState<File[]>([])
  const count = (r?.evidence?.length ?? 0) + (r?.feedback?.length ?? 0) + related.length

  // 打开时停在最新的记录；有新记录时，如果本来就在底部附近就跟上去。
  const stick = useRef(true)
  useLayoutEffect(() => {
    const el = scroller.current
    if (el && r) el.scrollTop = el.scrollHeight
  }, [r?.id]) // eslint-disable-line react-hooks/exhaustive-deps
  useEffect(() => {
    const el = scroller.current
    if (el && stick.current) window.setTimeout(() => el.scrollTo({ top: el.scrollHeight, behavior: 'smooth' }), 320)
  }, [count])

  return (
    <AnnotateTarget.Provider value={(f) => setIncoming((x) => [...x, f])}>
    <div className="flex min-h-0 flex-1 flex-col">
      {top}
      {q.isError ? (
        <div className="p-5">
          <ErrorState error={q.error} onRetry={() => q.refetch()} />
        </div>
      ) : !r ? (
        <div className="space-y-3 p-5">
          <Skeleton className="h-8" />
          <Skeleton className="h-24" />
        </div>
      ) : (
        <>
          <Brief r={r} />
          <div
            ref={scroller}
            onScroll={(e) => {
              const el = e.currentTarget
              stick.current = el.scrollHeight - el.scrollTop - el.clientHeight < 80
            }}
            className="min-h-0 flex-1 overflow-y-auto overscroll-contain bg-bg/40 px-4 py-5 sm:px-5"
          >
            <ReqTimeline r={r} decisions={related} />
          </div>
          <footer className="shrink-0 border-t border-line bg-surface px-4 pt-3 pb-[max(0.875rem,env(safe-area-inset-bottom))] sm:px-5">
            <Composer
              key={r.code}
              r={r}
              onPassed={onPassed}
              pending={related.filter((d) => d.status === 'pending').length}
              onJump={() => scroller.current?.scrollTo({ top: scroller.current.scrollHeight, behavior: 'smooth' })}
              incoming={incoming}
              onIncoming={() => setIncoming([])}
            />
          </footer>
        </>
      )}
    </div>
    </AnnotateTarget.Provider>
  )
}

// Brief：需求本身——标题、状态、可折叠的说明（描述、验收标准、关联会话、来源）。
function Brief({ r }: { r: Requirement }) {
  const projects = useProjects()
  const sessions = useSessions()
  const { updateReq, linkReq } = useActions()
  const openSession = useOpenSession()
  const [open, setOpen] = usePref('ks.req.brief', false)
  const save = (patch: Partial<ReqFields>) => void updateReq(r.code, toFields(r, patch))
  const sessByKey = new Map((sessions.data?.sessions ?? []).map((s) => [s.key, s]))
  const linkable = (sessions.data?.sessions ?? []).filter((s) => !r.sessions.includes(s.key) && s.state !== 'ended')
  const chip = 'inline-flex max-w-[220px] items-center gap-1.5 rounded-md bg-sunken px-2.5 py-1 text-sm text-fg-2 transition hover:text-fg active:scale-[0.97]'

  return (
    <header className="shrink-0 border-b border-line px-4 pt-1 pb-3 sm:px-5">
      <EditableText value={r.title} placeholder={t.reqs.titlePlaceholder} onSave={(v) => v && save({ title: v })} className="-mx-3 h-auto py-1 font-display text-lg leading-snug" />
      <div className="mt-2 flex flex-wrap items-center gap-2">
        <StatusMenu r={r} size="md" />
        <PlanBadge r={r} className="px-2.5 py-1 text-sm" />
        <DesignBadge r={r} className="px-2.5 py-1 text-sm" />
        <Menu
          align="start"
          trigger={
            <button className={chip}>
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
            <button className={chip}>
              <span className="truncate">{r.project_name || t.reqs.noProject}</span>
              <ChevronDown className="size-3 shrink-0 opacity-70" />
            </button>
          }
          items={[
            { label: t.reqs.noProject, checked: !r.project_id, onSelect: () => save({ project_id: undefined }) },
            ...(projects.data?.projects ?? []).map((p) => ({ label: p.name, hint: p.remote, checked: p.id === r.project_id, onSelect: () => save({ project_id: p.id }) })),
          ]}
        />
        <button onClick={() => setOpen(!open)} className="ml-auto inline-flex items-center gap-1 rounded-md px-2 py-1 text-[13px] text-fg-3 transition hover:bg-hover hover:text-fg">
          {t.reqs.brief}
          <ChevronDown className={cx('size-3.5 transition-transform', open && 'rotate-180')} />
        </button>
      </div>
      <OwnLine r={r} />
      {!open && (
        <button onClick={() => setOpen(true)} className="mt-2 line-clamp-2 w-full text-left text-[13px] leading-relaxed text-fg-3 transition hover:text-fg-2">
          {r.detail || r.acceptance || t.reqs.briefEmpty}
        </button>
      )}
      <Collapse show={open}>
        <div className="mt-3 max-h-[34dvh] space-y-3 overflow-y-auto overscroll-contain rounded-xl bg-surface-2 p-3 text-sm">
          <section>
            <h3 className="mb-0.5 text-xs font-medium text-fg-3">{t.reqs.detail}</h3>
            <AttachZone value={r.files ?? []} onChange={(files) => save({ files: files.map((f) => f.id) })} compact>
              <EditableText multiline value={r.detail} placeholder={t.reqs.detailPlaceholder} onSave={(v) => save({ detail: v })} className="-mx-3 pr-10" />
            </AttachZone>
          </section>
          <section>
            <h3 className="mb-0.5 text-xs font-medium text-fg-3">{t.reqs.acceptance}</h3>
            <EditableText multiline value={r.acceptance} placeholder={t.reqs.acceptancePlaceholder} onSave={(v) => save({ acceptance: v })} className="-mx-3" />
          </section>
          <section>
            <div className="mb-1 flex items-center justify-between">
              <h3 className="text-xs font-medium text-fg-3">{t.reqs.sessions}</h3>
              <Menu
                width="min-w-[280px]"
                trigger={
                  <Button size="sm" variant="ghost" icon={<Link2 className="size-3.5" />} disabled={!linkable.length} className="-my-1">
                    {t.session.link}
                  </Button>
                }
                items={linkable.map((s) => ({
                  label: [s.title || s.project_name || s.cwd, s.device_name].filter(Boolean).join(t.common.sep),
                  hint: s.summary || s.last_prompt,
                  icon: <StatusDot state={s.state} />,
                  onSelect: () => void linkReq(r.code, s.key),
                }))}
              />
            </div>
            {r.sessions.length ? (
              <div className="flex flex-wrap gap-1.5">
                {r.sessions.map((k) => {
                  const s = sessByKey.get(k)
                  return (
                    <span key={k} className="group inline-flex max-w-full items-center gap-1.5 rounded-lg border border-line bg-surface py-1 pr-1 pl-2 text-[13px]">
                      {s ? <StatusDot state={s.state} /> : <span className="size-2 rounded-full bg-line-strong" />}
                      <button onClick={() => s && openSession(k)} className="truncate text-fg-2 hover:text-fg">
                        {s ? [s.title || s.project_name, s.device_name].filter(Boolean).join(t.common.sep) : t.reqs.sessionGone}
                      </button>
                      <button onClick={() => void linkReq(r.code, k, true)} aria-label={t.session.unlink} className="rounded p-0.5 text-fg-3 hover:bg-hover hover:text-fg">
                        <X className="size-3" />
                      </button>
                    </span>
                  )
                })}
              </div>
            ) : (
              <p className="text-[13px] text-fg-3">{t.reqs.noSessions}</p>
            )}
          </section>
          <div className="grid gap-3 sm:grid-cols-2">
            <section>
              <h3 className="mb-0.5 text-xs font-medium text-fg-3">{t.reqs.source}</h3>
              <EditableText value={r.source} placeholder={t.reqs.sourcePlaceholder} onSave={(v) => save({ source: v })} className="-mx-3" />
            </section>
            <section>
              <h3 className="mb-0.5 flex items-center gap-1 text-xs font-medium text-fg-3">
                {t.reqs.docLink}
                {/^https?:\/\//.test(r.doc_link) && (
                  <a href={r.doc_link} target="_blank" rel="noreferrer" className="text-accent">
                    <ExternalLink className="size-3" />
                  </a>
                )}
              </h3>
              <EditableText value={r.doc_link} placeholder={t.reqs.docLinkPlaceholder} onSave={(v) => save({ doc_link: v })} className="-mx-3 font-mono text-[13px]" />
            </section>
          </div>
        </div>
      </Collapse>
    </header>
  )
}

// Composer：底部输入。待验收时——没写意见“通过”是主操作，写了意见或贴了图就“退回修改”；
// 其它状态——可以随时给 Agent 留言（只送达，不改需求状态）。
function Composer({ r, onPassed, pending, onJump, incoming, onIncoming }: { r: Requirement; onPassed?: (code: string) => void; pending: number; onJump: () => void; incoming?: File[]; onIncoming?: () => void }) {
  const { review, note } = useActions()
  const [busy, wrap] = useBusy()
  const [text, setText] = useState('')
  const [files, setFiles] = useState<FileRef[]>([])
  const reviewing = r.status === 'review'
  const dirty = !!text.trim() || files.length > 0
  const clear = () => {
    setText('')
    setFiles([])
  }
  const revise = () =>
    wrap(async () => {
      if (await review([{ ref: r.code, verdict: 'revise', comment: text, files: files.map((f) => f.id) }])) {
        toast(t.review.revised(r.code))
        clear()
      }
    })
  const send = () =>
    wrap(async () => {
      if (await note(r.code, text, files.map((f) => f.id))) {
        toast(t.reqs.noteSent)
        clear()
      }
    })
  // 通过时也可以带意见（作为留言送达，需求仍是已完成）
  const pass = () =>
    wrap(async () => {
      if (await review([{ ref: r.code, verdict: 'accept', comment: text, files: files.map((f) => f.id) }])) {
        if (dirty) toast(t.review.passedWith(r.code))
        clear()
        if (onPassed) onPassed(r.code)
        else toast.success(t.review.passed(r.code))
      }
    })
  const hint = pending ? (
    <button onClick={onJump} className="flex min-w-0 items-center gap-2 text-xs font-medium text-reply transition hover:opacity-80">
      <span className="kite-tug size-2 shrink-0 rotate-45 rounded-[1.5px] bg-reply" />
      <span className="truncate">{t.reqs.lineAsk(pending)}</span>
    </button>
  ) : (
    <span className="flex min-w-0 items-center gap-2 text-xs text-fg-3">
      <span className={cx('size-2 shrink-0 rotate-45 rounded-[1.5px]', statusCls[r.status]?.dot)} />
      <span className="truncate">{reviewing ? (dirty ? t.review.hintRevise : t.review.hintPass) : (t.reqs.line[r.status] ?? t.reqStatus[r.status])}</span>
    </span>
  )
  return (
    <div
      onKeyDown={(e) => {
        if ((e.metaKey || e.ctrlKey) && e.key === 'Enter') {
          e.preventDefault()
          if (reviewing) void (dirty ? revise() : pass())
          else if (dirty) void send()
        }
      }}
    >
      <AttachZone value={files} onChange={setFiles} compact incoming={incoming} onIncoming={onIncoming}>
        <Textarea minRows={1} value={text} placeholder={reviewing ? t.review.composer : t.reqs.notePlaceholder} onChange={(e) => setText(e.target.value)} className="pr-10" />
      </AttachZone>
      <div className="mt-2.5 flex items-center gap-2">
        <div className="min-w-0 flex-1">{hint}</div>
        {reviewing ? (
          <>
            <Button variant={dirty ? 'primary' : 'ghost'} disabled={!dirty || busy} icon={<Undo2 className="size-4" />} onClick={revise}>
              {t.review.sendRevise}
            </Button>
            <Tip content={dirty ? t.review.passWithHint : undefined}>
              <span>
                <Button variant={dirty ? 'secondary' : 'primary'} disabled={busy} loading={busy && !dirty} icon={<Check className="size-4" />} onClick={pass} className="min-w-24">
                  {dirty ? t.review.passWith : t.review.pass}
                </Button>
              </span>
            </Tip>
          </>
        ) : (
          <>
            {r.status !== 'done' && r.status !== 'shelved' && <AssignButton code={r.code} projectId={r.project_id} />}
            <Button variant="primary" disabled={!dirty} loading={busy} icon={<CornerDownLeft className="size-4" />} onClick={send}>
              {t.reqs.noteSend}
            </Button>
          </>
        )}
      </div>
    </div>
  )
}

// ---------- 抽屉：导航与切换 ----------

const slide = {
  enter: (d: Dir) => ({ x: d === 'next' ? 56 : -56, opacity: 0 }),
  center: { x: 0, opacity: 1, rotate: 0 },
  exit: (d: Dir) => ({ x: d === 'next' ? -56 : 96, opacity: 0, rotate: d === 'pass' ? 1.2 : 0 }),
}

export function ReqPanel({ refId, onClose }: { refId: string | null; onClose: () => void }) {
  const now = useNow()
  const all = useRequirements().data ?? []
  const [, setParams] = useSearchParams()
  const { deleteReq } = useActions()
  const confirm = useConfirm()
  const [dir, setDir] = useState<Dir>('next')
  const [stamp, setStamp] = useState(false)
  const [finished, setFinished] = useState<string | null>(null) // 连续验收完时显示的项目名
  const internal = useRef(false)
  const [anchor, setAnchor] = useState<{ project: string; review: boolean } | null>(null)
  const cur = all.find((x) => x.code === refId)

  // 从外部打开（不是抽屉内切换）时，确定这次要连续处理的范围：同一项目的待验收，或同一项目未关闭的需求。
  useEffect(() => {
    if (!refId) {
      setAnchor(null)
      setFinished(null)
      return
    }
    if (internal.current) {
      internal.current = false
      return
    }
    if (cur) setAnchor({ project: cur.project_id || '', review: cur.status === 'review' })
  }, [refId, cur?.id]) // eslint-disable-line react-hooks/exhaustive-deps

  const queue = useMemo(() => {
    if (!anchor) return cur ? [cur] : []
    return all.filter((x) => (x.project_id || '') === anchor.project && (x.code === refId || (anchor.review ? x.status === 'review' : !closed(x)))).sort(sortReqs)
  }, [all, anchor, refId, cur])
  const idx = queue.findIndex((x) => x.code === refId)
  const prev = idx > 0 ? queue[idx - 1] : undefined
  const next = idx >= 0 && idx < queue.length - 1 ? queue[idx + 1] : undefined

  const go = (code: string, d: Dir) => {
    internal.current = true
    setDir(d)
    setFinished(null)
    setParams(
      (p) => {
        const n = new URLSearchParams(p)
        n.set('r', code)
        return n
      },
      { replace: true },
    )
  }
  const onPassed = (code: string) => {
    toast.success(t.review.passed(code))
    const target = queue.filter((x) => x.code !== code && (!anchor?.review || x.status === 'review'))
    const after = target.find((x) => queue.indexOf(x) > idx) ?? target[target.length - 1]
    setStamp(true)
    window.setTimeout(() => {
      setStamp(false)
      if (after) go(after.code, 'pass')
      else {
        setDir('pass')
        setFinished(cur?.project_name || t.reqs.noProject)
      }
    }, 520)
  }

  // ← → 切换（输入时不拦截）
  useEffect(() => {
    if (!refId) return
    const onKey = (e: KeyboardEvent) => {
      const el = e.target as HTMLElement
      if (el.closest('input, textarea, [contenteditable=true], [role=menu]')) return
      if (e.key === 'ArrowLeft' && prev) go(prev.code, 'prev')
      if (e.key === 'ArrowRight' && next) go(next.code, 'next')
    }
    window.addEventListener('keydown', onKey)
    return () => window.removeEventListener('keydown', onKey)
  })

  const navBtn = 'flex size-8 items-center justify-center rounded-lg text-fg-2 transition hover:bg-hover hover:text-fg active:scale-95 disabled:pointer-events-none disabled:opacity-30 pointer-coarse:size-10'
  const top = (
    <div className="flex shrink-0 items-center gap-1 px-3 pt-3 pb-1 sm:px-4">
      {queue.length > 1 && (
        <>
          <Tip content={prev ? `${prev.code} ${prev.title}` : undefined}>
            <button className={navBtn} disabled={!prev} aria-label={t.reqs.prev} onClick={() => prev && go(prev.code, 'prev')}>
              <ChevronLeft className="size-[18px]" />
            </button>
          </Tip>
          <Tip content={next ? `${next.code} ${next.title}` : undefined}>
            <button className={navBtn} disabled={!next} aria-label={t.reqs.next} onClick={() => next && go(next.code, 'next')}>
              <ChevronRight className="size-[18px]" />
            </button>
          </Tip>
          <span className="ml-1 text-xs text-fg-3 tabular">{t.reqs.position(idx + 1, queue.length, !!anchor?.review)}</span>
        </>
      )}
      <span className="ml-auto truncate px-1 text-xs text-fg-3">
        {cur && (
          <>
            <span className="font-mono font-semibold">{cur.code}</span>
            <span className="max-sm:hidden"> · {t.reqs.createdBy(relTime(cur.created_at, now), cur.created_by === 'agent')}</span>
          </>
        )}
      </span>
      {cur && (
        <Menu
          trigger={
            <button className={navBtn} aria-label={t.common.more}>
              <MoreHorizontal className="size-4" />
            </button>
          }
          items={[
            {
              label: t.common.delete,
              icon: <Trash2 />,
              danger: true,
              onSelect: async () => {
                if (await confirm({ title: t.common.delete, description: t.reqs.deleteConfirm, confirmText: t.common.delete, danger: true })) {
                  if (await deleteReq(cur.code)) {
                    toast(t.reqs.deleted)
                    onClose()
                  }
                }
              },
            },
          ]}
        />
      )}
      <button className={navBtn} aria-label={t.common.close} onClick={onClose}>
        <X className="size-[18px]" />
      </button>
    </div>
  )

  return (
    <Panel
      open={!!refId}
      onClose={onClose}
      wide
      bare
      title={cur ? `${cur.code} ${cur.title}` : t.reqs.title}
      onSwipe={(d) => {
        if (d === 'left' && next) go(next.code, 'next')
        else if (d === 'right' && prev) go(prev.code, 'prev')
        else return false
        return true
      }}
    >
      <div className="relative flex min-h-0 flex-1 flex-col overflow-hidden">
        <AnimatePresence initial={false} custom={dir} mode="popLayout">
          <motion.div
            key={finished ? 'finished' : (refId ?? '')}
            custom={dir}
            variants={slide}
            initial="enter"
            animate="center"
            exit="exit"
            transition={{ duration: 0.32, ease: [0.2, 0.8, 0.2, 1] }}
            className="flex min-h-0 flex-1 flex-col bg-surface"
          >
            {finished ? <AllDone project={finished} onClose={onClose} top={top} /> : refId && <ReqView refId={refId} top={top} onPassed={anchor?.review ? onPassed : undefined} />}
          </motion.div>
        </AnimatePresence>
        <AnimatePresence>
          {stamp && (
            <motion.div
              initial={{ opacity: 0, scale: 1.6, rotate: -14 }}
              animate={{ opacity: 1, scale: 1, rotate: -10 }}
              exit={{ opacity: 0 }}
              transition={{ type: 'spring', bounce: 0.35, duration: 0.35 }}
              className="pointer-events-none absolute top-1/3 left-1/2 z-10 -ml-16 flex h-20 w-32 items-center justify-center rounded-xl border-[3px] border-seal font-display text-2xl tracking-[0.2em] text-seal"
            >
              {t.reqs.stamp}
            </motion.div>
          )}
        </AnimatePresence>
      </div>
    </Panel>
  )
}

function AllDone({ project, onClose, top }: { project: string; onClose: () => void; top: ReactNode }) {
  return (
    <div className="flex min-h-0 flex-1 flex-col">
      {top}
      <div className="flex flex-1 flex-col items-center justify-center gap-3 px-6 text-center">
        <span className="flex size-14 items-center justify-center rounded-2xl bg-run-soft text-run">
          <CheckCheck className="size-7" />
        </span>
        <div className="font-display text-lg text-fg">{t.reqs.allReviewed(project)}</div>
        <Button className="mt-2" onClick={onClose}>
          {t.common.close}
        </Button>
      </div>
    </div>
  )
}
