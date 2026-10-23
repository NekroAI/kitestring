import { useEffect, useMemo, useRef, useState, type KeyboardEvent } from 'react'
import { AnimatePresence, motion } from 'motion/react'
import { ArrowRightLeft, Check, CornerDownLeft, Keyboard, PenLine, Plus, RotateCcw, Sparkles, Undo2, X } from 'lucide-react'
import { toast } from 'sonner'
import type { Decision, Session } from '../api/client'
import { t } from '../i18n'
import { cx, relTime, useNow } from '../lib/util'
import { Badge, Button, Card, Textarea, Tip } from '../ui/base'
import { Menu, useConfirm } from '../ui/overlay'
import { ToolMark } from '../ui/marks'
import { AttachZone, fileUrl, Thumbs, uploadsBusy, useUploading, ZoomButton } from '../ui/attach'
import { BlockList } from '../ui/blocks'
import { AnnotateTarget } from '../ui/annotate'
import { useActions, useBusy } from './actions'
import { composeAnswer, emptyDraft, useOutbox } from './outbox'
import { currentPicks, picksText, PlanSummary, Points } from './points'
import { useOpenReq, useOpenSession } from './nav'
import { targetItems } from './assign'
import { Linkify, RefChip } from '../ui/refmark'

// 推荐内容与某个选项一致时，返回该选项序号。
function recIndex(options: string[], rec: string): number {
  const r = rec.trim()
  if (!r) return -1
  const exact = options.findIndex((o) => o.trim() === r)
  if (exact >= 0) return exact
  return options.findIndex((o) => {
    const x = o.trim()
    return x.length >= 2 && (r.startsWith(x) || r.includes(`「${x}」`) || r.includes(`“${x}”`) || r.includes(`**${x}**`))
  })
}

// 来源行：独立展示时显示项目与设备；嵌在会话卡片里时只显示编号、需求与时间。
function Source({ d, now, embedded }: { d: Decision; now: number; embedded?: boolean }) {
  const openSession = useOpenSession()
  const openReq = useOpenReq()
  const tool = d.session_key?.split(':')[0] ?? ''
  return (
    <div className="flex min-w-0 flex-wrap items-center gap-x-2 gap-y-1 text-[13px] text-fg-3">
      <RefChip code={d.code} status={d.status} link={false} />
      {!embedded && d.session_key && (
        <button onClick={() => openSession(d.session_key!)} className="inline-flex min-w-0 items-center gap-1.5 hover:text-fg">
          {tool && <ToolMark tool={tool} size="sm" />}
          <span className="truncate">{[d.project_name, d.device_name].filter(Boolean).join(t.common.sep)}</span>
        </button>
      )}
      {d.requirement_code && (
        <RefChip code={d.requirement_code} onOpen={openReq} />
      )}
      <span className="ml-auto tabular">{relTime(d.created_at, now)}</span>
    </div>
  )
}

function Context({ text, expanded }: { text: string; expanded?: boolean }) {
  const [open, setOpen] = useState(!!expanded)
  const long = !expanded && (text.length > 140 || text.split('\n').length > 3)
  return (
    <div className="text-sm leading-relaxed text-fg-2">
      <div className={cx('break-words whitespace-pre-wrap', !open && long && 'line-clamp-3')}>
        <Linkify text={text} />
      </div>
      {long && (
        <button onClick={() => setOpen(!open)} className="mt-0.5 text-[13px] font-medium text-accent hover:underline">
          {open ? t.common.collapse : t.common.showAll}
        </button>
      )}
    </div>
  )
}

// DecisionComposer：待决策，像回复消息一样作答。
export function DecisionComposer({ d, embedded }: { d: Decision; embedded?: boolean }) {
  const now = useNow()
  const { drafts, setDraft, queued, send, cancel } = useOutbox()
  const { closeDecision } = useActions()
  const confirm = useConfirm()
  const closeIt = async () => {
    if (await confirm({ title: `${t.decision.closeIt} ${d.code}`, description: t.decision.closeConfirm, confirmText: t.decision.closeIt, danger: true })) {
      if (await closeDecision(d.code)) toast(t.decision.closed)
    }
  }
  const draft = drafts[d.code] ?? emptyDraft
  const options = useMemo(() => d.options ?? [], [d.options])
  const rec = d.recommendation?.trim() ?? ''
  const ri = useMemo(() => recIndex(options, rec), [options, rec])
  // 多点决策（R-112）：逐点选择，默认选推荐；附言和附图照旧
  const multi = !!d.points?.length
  const picks = multi ? currentPicks(d, draft.picks) : []
  const picked = multi && picks.every((c) => c >= 0)
  const answer = multi ? (picked ? picksText(d, picks, draft.note) : '') : composeAnswer(draft, options)
  const [sendingText, setSendingText] = useState('')
  const sendingAt = queued[d.code]
  const noteRef = useRef<HTMLTextAreaElement>(null)
  const approval = !!d.approval
  const free = !multi && (options.length === 0 || draft.choice === 'other')
  // 批准类：附言框常驻（批准和拒绝都可以附言）
  const note = !free && (approval || draft.noteOpen || !!draft.note)
  const title = d.short_title || d.question
  // 方案对比：任一选项带效果图时，选项改为看图点选的卡片
  const visual = (d.option_files ?? []).some((f) => f?.length)
  const optImgs = (d.option_files ?? []).flatMap((fs) => fs?.slice(0, 1) ?? [])
  // 在选项效果图上“标注并附上”：打开补充说明并把标注图附进答复
  const [incoming, setIncoming] = useState<File[]>([])

  useEffect(() => {
    if (draft.choice === 'other' || draft.noteOpen) noteRef.current?.focus()
  }, [draft.choice, draft.noteOpen])

  const choose = (c: number | 'other') => setDraft(d.code, { choice: draft.choice === c ? null : c })
  const uploading = useUploading()
  const submit = () => {
    if (uploadsBusy()) return void toast(t.attach.wait) // 附件没传完先不发
    if (!answer) return
    setSendingText(answer)
    if (multi) send([{ code: d.code, answer: draft.note.trim(), files: draft.files?.map((f) => f.id), choices: picks }])
    else send([{ code: d.code, answer, files: draft.files?.map((f) => f.id) }])
  }
  const reject = () => {
    if (uploadsBusy()) return void toast(t.attach.wait)
    setSendingText([t.decision.reject, draft.note.trim()].filter(Boolean).join(' · '))
    send([{ code: d.code, answer: draft.note.trim(), files: draft.files?.map((f) => f.id), reject: true }])
  }
  const onKey = (e: KeyboardEvent) => {
    if ((e.metaKey || e.ctrlKey) && e.key === 'Enter') {
      e.preventDefault()
      submit()
      return
    }
    if ((e.target as HTMLElement).tagName === 'TEXTAREA' || multi) return
    const k = Number(e.key)
    if (k >= 1 && k <= options.length) choose(k - 1)
    else if (e.key === '0' && options.length) choose('other')
  }

  return (
    <AnnotateTarget.Provider
      value={(f) => {
        if (!free) setDraft(d.code, { noteOpen: true })
        setIncoming((x) => [...x, f])
      }}
    >
    <div
      tabIndex={-1}
      onKeyDown={onKey}
      onClick={(e) => e.stopPropagation()}
      className={cx(
        'group relative overflow-hidden rounded-2xl border bg-surface outline-none transition-[box-shadow,border] focus-within:shadow-pop',
        answer && !sendingAt ? 'border-accent/50' : 'border-line',
        !embedded && 'shadow-card',
      )}
    >
      <div className={cx('space-y-3', embedded ? 'p-3.5 sm:p-4' : 'p-4 sm:p-5')}>
        <Source d={d} now={now} embedded={embedded} />
        <div>
          <h3 className="font-display text-base leading-snug font-semibold text-fg sm:text-[17px]">{title}</h3>
          {d.short_title && <p className="mt-1 text-sm leading-relaxed text-fg-2">{d.question}</p>}
        </div>
        {d.summary?.length ? (
          <PlanSummary lines={d.summary} full={d.context ? <Context text={d.context} expanded /> : undefined} />
        ) : (
          d.context && <Context text={d.context} />
        )}
        <BlockList blocks={d.blocks} />

        {!!d.context_files?.length && (
          <div className={cx('grid gap-2.5', d.context_files.length > 1 && 'grid-cols-2')}>
            {d.context_files.map((f) => (
              <div key={f.id} className="group/ctx relative overflow-hidden rounded-xl border border-line bg-sunken">
                <img src={fileUrl(f.id)} alt={f.name} loading="lazy" className="max-h-[360px] w-full object-cover object-top" />
                <span className="absolute top-2 right-2 opacity-0 transition group-hover/ctx:opacity-100 pointer-coarse:opacity-100">
                  <ZoomButton f={f} group={d.context_files ?? undefined} />
                </span>
              </div>
            ))}
          </div>
        )}

        {multi && <Points d={d} picks={picks} onPick={(i, k) => setDraft(d.code, { picks: picks.map((c, j) => (j === i ? k : c)) })} />}

        {approval && !multi && (
          <div className="grid grid-cols-2 gap-2.5" role="radiogroup">
            {[
              { i: 0, label: t.decision.approve, icon: <Check className="size-5" />, on: 'border-run bg-run-soft text-run ring-2 ring-run/25' },
              { i: 1, label: t.decision.reject, icon: <X className="size-5" />, on: 'border-danger bg-danger-soft text-danger ring-2 ring-danger/25' },
            ].map((b) => (
              <button
                key={b.i}
                role="radio"
                aria-checked={draft.choice === b.i}
                onClick={() => choose(b.i)}
                className={cx(
                  'flex h-14 items-center justify-center gap-2 rounded-xl border text-[15px] font-medium transition-[background,border,box-shadow,transform] active:scale-[0.98] pointer-coarse:h-16',
                  draft.choice === b.i ? b.on : 'border-line bg-surface text-fg-2 hover:border-line-strong hover:bg-hover',
                )}
              >
                {b.icon}
                {b.label}
              </button>
            ))}
          </div>
        )}

        {options.length > 0 && visual && (
          <div className="grid grid-cols-1 gap-2.5 @md:grid-cols-2" role="radiogroup">
            {options.map((o, i) => {
              const on = draft.choice === i
              const img = d.option_files?.[i]?.[0]
              if (!img) return null // 没有效果图的选项放到下面的列表里
              return (
                <div
                  key={i}
                  role="radio"
                  aria-checked={on}
                  tabIndex={0}
                  onClick={() => choose(i)}
                  onKeyDown={(e) => (e.key === 'Enter' || e.key === ' ') && (e.preventDefault(), choose(i))}
                  className={cx(
                    'group/opt flex cursor-pointer flex-col overflow-hidden rounded-xl border bg-surface text-left transition-[border,box-shadow,transform] duration-150 active:scale-[0.99]',
                    on ? 'border-accent shadow-xs ring-2 ring-accent/30' : 'border-line hover:border-line-strong hover:shadow-xs',
                  )}
                >
                  <div className="relative aspect-[16/10] overflow-hidden border-b border-line bg-sunken">
                    <img src={fileUrl(img.id)} alt={o} loading="lazy" className="size-full object-cover object-top transition-transform duration-300 group-hover/opt:scale-[1.02]" />
                    <span className="absolute top-2 right-2 opacity-0 transition group-hover/opt:opacity-100 pointer-coarse:opacity-100" onClick={(e) => e.stopPropagation()}>
                      <ZoomButton f={img} group={optImgs} />
                    </span>
                  </div>
                  <div className="flex items-start gap-2.5 px-3 py-2.5 text-sm">
                    <span className={cx('mt-px flex size-5 shrink-0 items-center justify-center rounded-full border text-[11px] font-semibold tabular transition-colors', on ? 'border-accent bg-accent text-accent-fg' : 'border-line-strong text-fg-3')}>{i + 1}</span>
                    <span className="min-w-0 flex-1 leading-relaxed break-words">{o}</span>
                    {i === ri && (
                      <Badge tone="accent" icon={<Sparkles className="size-3" />}>
                        {t.decision.recommended}
                      </Badge>
                    )}
                  </div>
                </div>
              )
            })}
          </div>
        )}

        {options.length > 0 && !approval && (
          <div className="space-y-1.5" role={visual ? undefined : 'radiogroup'}>
            {options.map((o, i) => {
              if (visual && d.option_files?.[i]?.length) return null
              const on = draft.choice === i
              return (
                <button
                  key={i}
                  role="radio"
                  aria-checked={on}
                  onClick={() => choose(i)}
                  className={cx(
                    'flex w-full items-start gap-3 rounded-xl border px-3 py-2.5 text-left text-sm transition-[background,border,box-shadow] duration-150',
                    on ? 'border-accent bg-accent-soft text-fg shadow-xs' : 'border-line bg-surface hover:border-line-strong hover:bg-hover',
                  )}
                >
                  <span className={cx('mt-px flex size-5 shrink-0 items-center justify-center rounded-full border text-[11px] font-semibold tabular transition-colors', on ? 'border-accent bg-accent text-accent-fg' : 'border-line-strong text-fg-3')}>{i + 1}</span>
                  <span className="min-w-0 flex-1 leading-relaxed break-words">{o}</span>
                  {i === ri && (
                    <Tip content={rec !== o ? rec : undefined}>
                      <span className="shrink-0">
                        <Badge tone="accent" icon={<Sparkles className="size-3" />}>
                          {t.decision.recommended}
                        </Badge>
                      </span>
                    </Tip>
                  )}
                </button>
              )
            })}
            <button
              role="radio"
              aria-checked={draft.choice === 'other'}
              onClick={() => choose('other')}
              className={cx(
                'flex w-full items-center gap-3 rounded-xl border border-dashed px-3 py-2.5 text-left text-sm transition-[background,border]',
                draft.choice === 'other' ? 'border-accent bg-accent-soft text-fg' : 'border-line-strong text-fg-2 hover:bg-hover hover:text-fg',
              )}
            >
              <span className={cx('flex size-5 shrink-0 items-center justify-center rounded-full border', draft.choice === 'other' ? 'border-accent bg-accent text-accent-fg' : 'border-line-strong text-fg-3')}>
                <PenLine className="size-3" />
              </span>
              {t.decision.other}
            </button>
          </div>
        )}

        {rec && ri < 0 && (
          <div className="flex items-start gap-2.5 rounded-xl bg-accent-soft/60 px-3 py-2.5 text-sm">
            <Sparkles className="mt-0.5 size-4 shrink-0 text-accent" />
            <div className="min-w-0 flex-1 leading-relaxed break-words text-fg-2">{rec}</div>
            {options.length === 0 && (
              <Button size="sm" variant="soft" onClick={() => setDraft(d.code, { note: rec })}>
                {t.decision.useRecommendation}
              </Button>
            )}
          </div>
        )}

        <AnimatePresence initial={false}>
          {(free || note) && (
            <motion.div initial={{ height: 0, opacity: 0 }} animate={{ height: 'auto', opacity: 1 }} exit={{ height: 0, opacity: 0 }} transition={{ duration: 0.16 }} className="overflow-hidden px-px pb-px">
              <AttachZone value={draft.files ?? []} onChange={(files) => setDraft(d.code, { files })} compact incoming={incoming} onIncoming={() => setIncoming([])}>
                <Textarea ref={noteRef} minRows={free ? 3 : 2} value={draft.note} placeholder={free ? t.decision.freePlaceholder : approval ? t.decision.approvalNote : t.decision.notePlaceholder} onChange={(e) => setDraft(d.code, { note: e.target.value })} className="pr-10" />
              </AttachZone>
            </motion.div>
          )}
        </AnimatePresence>

        <div className="flex items-center gap-1.5">
          {!free && !note && (multi || typeof draft.choice === 'number') && (
            <Button size="sm" variant="ghost" icon={<Plus className="size-3.5" />} onClick={() => setDraft(d.code, { noteOpen: true })}>
              {t.decision.addNote}
            </Button>
          )}
          {(draft.choice != null || draft.note || !!draft.files?.length || !!draft.picks) && (
            <Tip content={t.decision.clearDrafts}>
              <Button size="icon-sm" variant="ghost" aria-label={t.decision.clearDrafts} onClick={() => setDraft(d.code, { choice: null, note: '', noteOpen: false, files: [], picks: undefined })}>
                <X className="size-3.5" />
              </Button>
            </Tip>
          )}
          <Button size="sm" variant="ghost" className="text-fg-3 hover:text-danger" onClick={closeIt}>
            {t.decision.closeIt}
          </Button>
          <span className="ml-auto" />
          {options.length > 0 && !multi && (
            <Tip content={t.decision.keyTip}>
              <span className="hidden text-fg-3 lg:inline-flex" aria-label={t.decision.keyTip}>
                <Keyboard className="size-4" />
              </span>
            </Tip>
          )}
          {multi && approval && (
            <Button size="sm" variant="ghost" className="h-9" disabled={uploading} onClick={reject} icon={<X className="size-4" />}>
              {t.decision.reject}
            </Button>
          )}
          <Button variant="primary" size="sm" className="h-9 px-4" disabled={!answer || uploading} onClick={submit} icon={multi && approval ? <Check className="size-4" /> : <CornerDownLeft className="size-4" />}>
            {multi ? (approval ? t.decision.approvePicks : t.decision.sendPicks) : t.decision.send}
          </Button>
        </div>
      </div>

      <AnimatePresence>{sendingAt && <SendingOverlay answer={sendingText || answer} at={sendingAt} onUndo={() => cancel(d.code)} />}</AnimatePresence>
    </div>
    </AnnotateTarget.Provider>
  )
}

function SendingOverlay({ answer, at, onUndo }: { answer: string; at: number; onUndo: () => void }) {
  return (
    <motion.div initial={{ opacity: 0 }} animate={{ opacity: 1 }} exit={{ opacity: 0 }} transition={{ duration: 0.15 }} className="absolute inset-0 z-10 flex flex-col items-center justify-center gap-2.5 bg-surface/92 px-6 text-center backdrop-blur-[2px]">
      <div className="text-sm font-medium text-fg">{t.decision.sending}</div>
      <div className="line-clamp-2 max-w-md text-[13px] text-fg-3">{answer}</div>
      <Button size="sm" icon={<Undo2 className="size-3.5" />} onClick={onUndo}>
        {t.common.undo}
      </Button>
      <motion.div className="absolute bottom-0 left-0 h-0.5 bg-accent" initial={{ width: '0%' }} animate={{ width: '100%' }} transition={{ duration: Math.max(0, at - Date.now()) / 1000, ease: 'linear' }} key={at} />
    </motion.div>
  )
}

// OrphanCard：已答复但提问会话已结束，需要转交或关闭（D15）。
export function OrphanCard({ d, sessions }: { d: Decision; sessions: Session[] }) {
  const now = useNow()
  const { reassign, closeDecision } = useActions()
  const [busy, wrap] = useBusy()
  const active = sessions.filter((s) => s.state !== 'ended' && s.state !== 'lost' && s.key !== d.session_key)
  return (
    <Card className="flex h-full flex-col gap-3 p-4">
      <Source d={d} now={now} />
      <div className="text-[15px] font-semibold text-fg">{d.short_title || d.question}</div>
      <AnswerBlock d={d} />
      <div className="mt-auto flex flex-wrap items-center justify-between gap-2">
        <span className="text-xs text-fg-3">{t.decision.orphan}</span>
        <div className="flex gap-2">
          <Button size="sm" variant="ghost" disabled={busy} onClick={() => wrap(async () => (await closeDecision(d.code)) && toast(t.decision.closed))}>
            {t.decision.closeIt}
          </Button>
          <Menu
            width="w-80"
            trigger={
              <Button size="sm" variant="primary" icon={<ArrowRightLeft className="size-3.5" />} disabled={busy || !active.length}>
                {active.length ? t.decision.reassign : t.decision.noActive}
              </Button>
            }
            items={targetItems(sessions, d.project_id, (s) => void wrap(async () => (await reassign(d.code, s.key)) && toast.success(t.decision.reassigned)), { exclude: d.session_key ?? undefined })}
          />
        </div>
      </div>
    </Card>
  )
}

export function AnswerBlock({ d }: { d: Decision }) {
  return (
    <div className="space-y-2 rounded-xl border border-line bg-surface-2 px-3 py-2.5">
      <div>
        <div className="text-xs font-medium text-fg-3">{d.kind === 'feedback' ? t.review.history : d.kind === 'note' ? t.timeline.note : d.kind === 'assign' ? t.assign.timeline : t.decision.answer}</div>
        <div className="mt-0.5 text-sm leading-relaxed break-words whitespace-pre-wrap text-fg">{d.answer}</div>
      </div>
      <Thumbs files={d.answer_files} size="sm" />
    </div>
  )
}

// DecisionRow：决策记录里的一行，点击展开。
export function DecisionRow({ d }: { d: Decision }) {
  const now = useNow()
  const { resendDecision } = useActions()
  const [open, setOpen] = useState(false)
  const tone = d.status === 'delivered' ? 'run' : d.status === 'answered' ? 'reply' : d.status === 'pending' ? 'wait' : 'neutral'
  const when = d.status === 'delivered' ? t.decision.deliveredAt(relTime(d.delivered_at, now)) : d.status === 'answered' ? t.decision.answeredAt(relTime(d.answered_at, now)) : relTime(d.created_at, now)
  return (
    <div className="border-b border-line last:border-0">
      <button onClick={() => setOpen(!open)} className="flex w-full items-center gap-3 px-3.5 py-2.5 text-left hover:bg-hover/60">
        <span className="w-[68px] shrink-0">
          <RefChip code={d.code} status={d.status} link={false} />
        </span>
        <span className="min-w-0 flex-1">
          <span className="block truncate text-sm text-fg">{d.short_title || d.question}</span>
          {d.answer && <span className="block truncate text-[13px] text-fg-3">{d.answer}</span>}
        </span>
        <Tip content={when}>
          <span>
            <Badge tone={tone}>{t.decision.status[d.status]}</Badge>
          </span>
        </Tip>
      </button>
      {open && (
        <div className="rise space-y-3 px-3.5 pb-3.5 sm:pl-[60px]">
          {d.short_title && <p className="text-sm text-fg-2">{d.question}</p>}
          {d.context && <Context text={d.context} />}
          <BlockList blocks={d.blocks} />
          {d.answer && <AnswerBlock d={d} />}
          <div className="flex items-center justify-between text-xs text-fg-3">
            <span>{when}</span>
            {d.status === 'delivered' && (
              <Button size="sm" variant="ghost" icon={<RotateCcw className="size-3.5" />} onClick={async () => (await resendDecision(d.code)) && toast(t.decision.resent)}>
                {t.decision.resend}
              </Button>
            )}
          </div>
        </div>
      )}
    </div>
  )
}
