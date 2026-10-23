import { useEffect, useLayoutEffect, useMemo, useRef, useState, type ReactNode } from 'react'
import { useSearchParams } from 'react-router'
import { AnimatePresence, motion } from 'motion/react'
import { Check, CheckCheck, ChevronDown, CornerDownLeft, ChevronLeft, ChevronRight, ExternalLink, Link2, MoreHorizontal, Trash2, Undo2, X } from 'lucide-react'
import { toast } from 'sonner'
import { ApiError, type ReqFields, type Requirement } from '../api/client'
import { useDecisions, useProjects, useRequirement, useRequirements, useSessions } from '../api/queries'
import { t } from '../i18n'
import { noKbd, cx, relTime, useNow } from '../lib/util'
import { Button, ErrorState, Skeleton, Textarea, Tip } from '../ui/base'
import { AttachZone, useUploading, type FileRef } from '../ui/attach'
import { AnnotateTarget } from '../ui/annotate'
import { Collapse } from '../ui/motion'
import { Menu, Panel, useConfirm } from '../ui/overlay'
import { PriorityDot, StatusDot } from '../ui/marks'
import { useActions, useBusy } from './actions'
import { useOpenSession } from './nav'
import { EditableMd, EditableText, priorities, ReleaseBadge, StatusMenu, toFields, useSetStatus } from './requirements'
import { sortDrafts } from './pool'
import { ReqTimeline } from './reqTimeline'
import { AcceptView } from './accept'
import { AssignButton } from './assign'
import { ReviewerButton } from './reviewer'
import { DesignBadge } from './design'
import { PlanBadge } from './plans'
import { OwnLine, useOwnership } from './ownership'
import { useKsEvent, withKey } from './command'
import { PassingRow, schedulePass, usePassing } from './pass'
import { plainText } from '../ui/markdown'
import { Linkify, ProjGlyph, RefChip, ReqGlyph } from '../ui/refmark'

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
  const count = (r?.evidence?.length ?? 0) + (r?.reviews?.length ?? 0) + (r?.feedback?.length ?? 0) + related.length

  // 待验收时第一屏是验收视图（R-111），从顶上看起；其它状态停在最新的记录，有新记录时如果本来就在底部附近就跟上去。
  const reviewing = r?.status === 'review'
  const stick = useRef(true)
  useLayoutEffect(() => {
    const el = scroller.current
    if (el && r) el.scrollTop = reviewing ? 0 : el.scrollHeight
  }, [r?.id, reviewing]) // eslint-disable-line react-hooks/exhaustive-deps
  useEffect(() => {
    const el = scroller.current
    if (el && stick.current && !reviewing) window.setTimeout(() => el.scrollTo({ top: el.scrollHeight, behavior: 'smooth' }), 320)
  }, [count]) // eslint-disable-line react-hooks/exhaustive-deps

  return (
    <AnnotateTarget.Provider value={(f) => setIncoming((x) => [...x, f])}>
    <div className="flex min-h-0 flex-1 flex-col">
      {top}
      {q.isError && q.error instanceof ApiError && q.error.status === 404 ? null : q.isError ? ( // 刚被删除：列表马上会收起这一条，不闪报错
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
          <Brief r={r} compact={reviewing} />
          <div
            ref={scroller}
            onScroll={(e) => {
              const el = e.currentTarget
              stick.current = el.scrollHeight - el.scrollTop - el.clientHeight < 80
            }}
            className="min-h-0 flex-1 overflow-y-auto overscroll-contain bg-bg/40 px-4 py-5 sm:px-5"
          >
            {reviewing ? <AcceptView r={r} decisions={related} /> : <ReqTimeline r={r} decisions={related} />}
          </div>
          <footer className="shrink-0 border-t border-line bg-surface px-4 pt-3 pb-[max(0.875rem,env(safe-area-inset-bottom))] sm:px-5">
            <Composer
              key={r.code}
              r={r}
              onPassed={onPassed}
              pending={related.filter((d) => d.status === 'pending').length}
              onJump={() => scroller.current?.scrollTo({ top: reviewing ? 0 : scroller.current.scrollHeight, behavior: 'smooth' })}
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
// compact：验收时手机上不显示两行摘要，把第一屏留给证据（说明仍可展开）。
function Brief({ r, compact }: { r: Requirement; compact?: boolean }) {
  const projects = useProjects()
  const sessions = useSessions()
  const { updateReq, linkReq } = useActions()
  const openSession = useOpenSession()
  // 每次打开需求都先收起说明，只显示两行摘要（R-76）；不再记住上次展开过
  const [open, setOpen] = useState(false)
  useEffect(() => setOpen(false), [r.id])
  const save = (patch: Partial<ReqFields>) => void updateReq(r.code, toFields(r, patch))
  const sessByKey = new Map((sessions.data?.sessions ?? []).map((s) => [s.key, s]))
  const linkable = (sessions.data?.sessions ?? []).filter((s) => !r.sessions.includes(s.key) && s.state !== 'ended')
  const chip = 'inline-flex max-w-[220px] items-center gap-1.5 rounded-md bg-sunken px-2.5 py-1 text-sm text-fg-2 transition hover:text-fg active:scale-[0.97]'

  return (
    <header className="shrink-0 border-b border-line px-4 pt-1 pb-3 sm:px-5">
      <EditableText wrap value={r.title} placeholder={t.reqs.titlePlaceholder} onSave={(v) => v && save({ title: v })} className="-mx-3 h-auto py-1 font-display text-lg leading-snug" />
      <div className="mt-2 flex flex-wrap items-center gap-2">
        <StatusMenu r={r} size="md" />
        <PlanBadge r={r} className="px-2.5 py-1 text-sm" />
        <DesignBadge r={r} className="px-2.5 py-1 text-sm" />
        <ReleaseBadge r={r} />
        <span className={cx('contents', compact && 'max-sm:hidden')}>
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
              <ProjGlyph />
              <span className="truncate">{r.project_name || t.reqs.noProject}</span>
              <ChevronDown className="size-3 shrink-0 opacity-70" />
            </button>
          }
          items={[
            { label: t.reqs.noProject, checked: !r.project_id, onSelect: () => save({ project_id: undefined }) },
            ...(projects.data?.projects ?? []).filter((p) => !p.hidden || p.id === r.project_id).map((p) => ({ label: p.name, hint: p.remote, checked: p.id === r.project_id, onSelect: () => save({ project_id: p.id }) })),
          ]}
        />
        </span>
        <button onClick={() => setOpen(!open)} className="ml-auto inline-flex items-center gap-1 rounded-md px-2 py-1 text-[13px] text-fg-3 transition hover:bg-hover hover:text-fg">
          {t.reqs.brief}
          <ChevronDown className={cx('size-3.5 transition-transform', open && 'rotate-180')} />
        </button>
      </div>
      <OwnLine r={r} />
      {!open && (
        <button onClick={() => setOpen(true)} className={cx('mt-2 line-clamp-2 w-full text-left text-[13px] leading-relaxed text-fg-3 transition hover:text-fg-2', compact && 'max-sm:hidden')}>
          <Linkify text={plainText(r.detail || r.acceptance) || t.reqs.briefEmpty} />
        </button>
      )}
      <Collapse show={open}>
        <div className="mt-3 max-h-[34dvh] space-y-3 overflow-y-auto overscroll-contain rounded-xl bg-surface-2 p-3 text-sm">
          <section>
            <h3 className="mb-0.5 text-xs font-medium text-fg-3">{t.reqs.detail}</h3>
            <AttachZone value={r.files ?? []} onChange={(files) => save({ files: files.map((f) => f.id) })} compact>
              <EditableMd value={r.detail} placeholder={t.reqs.detailPlaceholder} onSave={(v) => save({ detail: v })} className="-mx-3 pr-10" />
            </AttachZone>
          </section>
          <section>
            <h3 className="mb-0.5 text-xs font-medium text-fg-3">{t.reqs.acceptance}</h3>
            <EditableMd value={r.acceptance} placeholder={t.reqs.acceptancePlaceholder} onSave={(v) => save({ acceptance: v })} className="-mx-3" />
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
  const own = useOwnership()(r)
  const [busy, wrap] = useBusy()
  const uploading = useUploading()
  const [text, setText] = useState('')
  const [files, setFiles] = useState<FileRef[]>([])
  const reviewing = r.status === 'review'
  const dirty = !!text.trim() || files.length > 0
  const box = useRef<HTMLTextAreaElement>(null)
  const passingNow = usePassing().get(r.code)
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
  // 快捷键 A / 面板里的「通过」（R-127）：不立即生效，先进 5 秒撤销期（与工作台就地通过同一套）；
  // 到时还停在这条需求上才接着走连续验收（盖章、滑到下一条），抽屉已关就只通过。
  useKsEvent('ks:pass', (code) => {
    if (code !== r.code || !reviewing) return
    if (dirty) return toast(t.review.passBlocked)
    schedulePass(r.code, async () => {
      if (!(await review([{ ref: r.code, verdict: 'accept' }]))) return
      if (onPassed && new URLSearchParams(window.location.search).get('r') === r.code) onPassed(r.code)
      else toast.success(t.review.passed(r.code))
    })
  })
  // 快捷键 R：只聚焦退回意见框，不直接退回
  useKsEvent('ks:revise', (code) => {
    if (code === r.code && reviewing) box.current?.focus()
  })
  const hint = pending ? (
    <button onClick={onJump} className="flex min-w-0 items-center gap-2 text-xs font-medium text-reply transition hover:opacity-80">
      <span className="kite-tug size-2 shrink-0 rotate-45 rounded-[1.5px] bg-reply" />
      <span className="truncate">{t.reqs.lineAsk(pending)}</span>
    </button>
  ) : (
    <span className="flex min-w-0 items-center gap-2 text-xs text-fg-3">
      <ReqGlyph status={r.status} />
      <span className="truncate">{reviewing ? noKbd(dirty ? t.review.hintRevise : t.review.hintPass) : (own?.short ?? t.reqs.line[r.status] ?? t.reqStatus[r.status])}</span>
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
      {passingNow && <PassingRow code={r.code} until={passingNow.until} className="mb-2.5 flex items-center gap-3 rounded-xl bg-run-soft/60 px-3 py-2" />}
      <AttachZone value={files} onChange={setFiles} compact incoming={incoming} onIncoming={onIncoming}>
        <Textarea ref={box} minRows={1} value={text} placeholder={reviewing ? t.review.composer : t.reqs.notePlaceholder} onChange={(e) => setText(e.target.value)} className="pr-10" />
      </AttachZone>
      <div className="mt-2.5 flex items-center gap-2">
        <div className="min-w-0 flex-1">{hint}</div>
        {r.status !== 'shelved' && <ReviewerButton code={r.code} projectId={r.project_id} projectName={r.project_name} />}
        {reviewing ? (
          <>
            <Tip content={dirty ? undefined : withKey(t.review.revise, 'R')}>
              <span>
                <Button variant={dirty ? 'primary' : 'ghost'} disabled={!dirty || busy || uploading} icon={<Undo2 className="size-4" />} onClick={revise}>
                  {t.review.sendRevise}
                </Button>
              </span>
            </Tip>
            <Tip content={dirty ? t.review.passWithHint : withKey(t.review.pass, 'A')}>
              <span>
                <Button variant={dirty ? 'secondary' : 'primary'} disabled={busy || !!passingNow} loading={busy && !dirty} icon={<Check className="size-4" />} onClick={pass} className="min-w-24">
                  {dirty ? t.review.passWith : t.review.pass}
                </Button>
              </span>
            </Tip>
          </>
        ) : (
          <>
            {r.status !== 'done' && r.status !== 'shelved' && <AssignButton code={r.code} projectId={r.project_id} />}
            <Button variant="primary" disabled={!dirty || uploading} loading={busy} icon={<CornerDownLeft className="size-4" />} onClick={send}>
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
  const [params, setParams] = useSearchParams()
  const draftMode = params.get('q') === 'drafts' // 逐条确认草稿（R-133）
  const draftProject = params.get('qp') ?? ''
  const setStatus = useSetStatus()
  const [acting, setActing] = useState(false)
  const { deleteReq } = useActions()
  const confirm = useConfirm()
  const [dir, setDir] = useState<Dir>('next')
  const [stamp, setStamp] = useState(false)
  const [finished, setFinished] = useState<string | null>(null) // 连续处理完时显示的一句话（某项目的待验收 / 草稿都处理完了）
  const internal = useRef(false)
  const [anchor, setAnchor] = useState<{ project: string; review: boolean; drafts?: boolean } | null>(null)
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
    if (cur) setAnchor(draftMode ? { project: draftProject, review: false, drafts: true } : { project: cur.project_id || '', review: cur.status === 'review' })
  }, [refId, cur?.id, draftMode]) // eslint-disable-line react-hooks/exhaustive-deps

  const queue = useMemo(() => {
    if (!anchor) return cur ? [cur] : []
    // 草稿队列：范围内全部草稿（可跨项目），处理过的那条留在原位，前后顺序不乱
    if (anchor.drafts) return all.filter((x) => (!anchor.project || x.project_id === anchor.project) && (x.code === refId || x.status === 'draft')).sort(sortDrafts)
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
        setFinished(t.reqs.allReviewed(cur?.project_name || t.reqs.noProject))
      }
    }, 520)
  }

  // 逐条确认：确认（改为待开始）或搁置后滑到下一条草稿；没有了就显示「草稿都处理完了」
  const draftAct = async (status: 'ready' | 'shelved') => {
    if (!cur || acting) return
    setActing(true)
    const ok = await setStatus(cur, status).finally(() => setActing(false))
    if (!ok) return
    const rest = queue.filter((x) => x.code !== cur.code && x.status === 'draft')
    const after = rest.find((x) => queue.indexOf(x) > idx) ?? rest[0]
    if (after) go(after.code, 'pass')
    else {
      setDir('pass')
      setFinished(t.pool.queue.allDone)
    }
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
  // J / K（R-127）：与 ← → 同样切换上下一条
  useKsEvent('ks:req-step', (d) => {
    if (d === 1 && next) go(next.code, 'next')
    if (d === -1 && prev) go(prev.code, 'prev')
  }, !!refId)

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
          <span className="ml-1 text-xs text-fg-3 tabular">{anchor?.drafts ? t.pool.queue.position(idx + 1, queue.length) : t.reqs.position(idx + 1, queue.length, !!anchor?.review)}</span>
        </>
      )}
      <span className="ml-auto flex min-w-0 items-center gap-1 truncate px-1 text-xs text-fg-3">
        {cur && (
          <>
            <RefChip code={cur.code} status={cur.status} link={false} />
            <span className="truncate max-sm:hidden"> · {t.reqs.createdBy(relTime(cur.created_at, now), cur.created_by === 'agent')}</span>
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
  const draftBar = anchor?.drafts && cur && !finished && (
    <div className="mx-3 mt-1 mb-1 flex shrink-0 flex-wrap items-center gap-2 rounded-xl bg-sunken/80 px-3 py-2 sm:mx-4">
      <span className="text-xs text-fg-3">{t.pool.draftHintShort}</span>
      <div className="ml-auto flex items-center gap-1.5">
        <Button size="sm" variant="ghost" disabled={!next || acting} onClick={() => next && go(next.code, 'next')}>
          {t.pool.queue.skip}
        </Button>
        <Button size="sm" disabled={cur.status !== 'draft' || acting} onClick={() => void draftAct('shelved')}>
          {t.pool.queue.shelve}
        </Button>
        <Tip content={t.pool.queue.confirmTip}>
          <span>
            <Button size="sm" variant="primary" icon={<Check className="size-3.5" />} disabled={cur.status !== 'draft'} loading={acting} onClick={() => void draftAct('ready')}>
              {t.pool.queue.confirm}
            </Button>
          </span>
        </Tip>
      </div>
    </div>
  )
  const head = draftBar ? (
    <>
      {top}
      {draftBar}
    </>
  ) : (
    top
  )

  return (
    <Panel
      open={!!refId}
      onClose={onClose}
      wide={cur?.status === 'review' ? 'xl' : true}
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
            {finished ? <AllDone message={finished} onClose={onClose} top={top} /> : refId && <ReqView refId={refId} top={head} onPassed={anchor?.review ? onPassed : undefined} />}
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

function AllDone({ message, onClose, top }: { message: string; onClose: () => void; top: ReactNode }) {
  return (
    <div className="flex min-h-0 flex-1 flex-col">
      {top}
      <div className="flex flex-1 flex-col items-center justify-center gap-3 px-6 text-center">
        <span className="flex size-14 items-center justify-center rounded-2xl bg-run-soft text-run">
          <CheckCheck className="size-7" />
        </span>
        <div className="font-display text-lg text-fg">{message}</div>
        <Button className="mt-2" onClick={onClose}>
          {t.common.close}
        </Button>
      </div>
    </div>
  )
}
