import { useEffect, useMemo, useRef, useState, type KeyboardEvent } from 'react'
import { AnimatePresence, motion } from 'motion/react'
import { ArrowRightLeft, CornerDownLeft, Keyboard, PenLine, Plus, RotateCcw, Sparkles, Undo2, X } from 'lucide-react'
import { toast } from 'sonner'
import type { Decision, Session } from '../api/client'
import { t } from '../i18n'
import { cx, relTime, useNow } from '../lib/util'
import { Badge, Button, Card, Textarea, Tip } from '../ui/base'
import { Menu } from '../ui/overlay'
import { ToolMark } from '../ui/marks'
import { AttachZone, Thumbs } from '../ui/attach'
import { useActions, useBusy } from './actions'
import { composeAnswer, emptyDraft, useOutbox } from './outbox'
import { useOpenReq, useOpenSession } from './nav'

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

function CodeChip({ code }: { code: string }) {
  return <span className="rounded-md bg-sunken px-1.5 py-0.5 font-mono text-[11px] font-semibold text-fg-2">{code}</span>
}

// 来源行：独立展示时显示项目与设备；嵌在会话卡片里时只显示编号、需求与时间。
function Source({ d, now, embedded }: { d: Decision; now: number; embedded?: boolean }) {
  const openSession = useOpenSession()
  const openReq = useOpenReq()
  const tool = d.session_key?.split(':')[0] ?? ''
  return (
    <div className="flex min-w-0 flex-wrap items-center gap-x-2 gap-y-1 text-[13px] text-fg-3">
      <CodeChip code={d.code} />
      {!embedded && d.session_key && (
        <button onClick={() => openSession(d.session_key!)} className="inline-flex min-w-0 items-center gap-1.5 hover:text-fg">
          {tool && <ToolMark tool={tool} size="sm" />}
          <span className="truncate">{[d.project_name, d.device_name].filter(Boolean).join(t.common.sep)}</span>
        </button>
      )}
      {d.requirement_code && (
        <button onClick={() => openReq(d.requirement_code!)} className="rounded-md bg-accent-soft px-1.5 py-0.5 font-mono text-[11px] font-semibold text-accent hover:brightness-95">
          {d.requirement_code}
        </button>
      )}
      <span className="ml-auto tabular">{relTime(d.created_at, now)}</span>
    </div>
  )
}

function Context({ text }: { text: string }) {
  const [open, setOpen] = useState(false)
  const long = text.length > 140 || text.split('\n').length > 3
  return (
    <div className="text-sm leading-relaxed text-fg-2">
      <div className={cx('break-words whitespace-pre-wrap', !open && long && 'line-clamp-3')}>{text}</div>
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
  const draft = drafts[d.code] ?? emptyDraft
  const options = useMemo(() => d.options ?? [], [d.options])
  const rec = d.recommendation?.trim() ?? ''
  const ri = useMemo(() => recIndex(options, rec), [options, rec])
  const answer = composeAnswer(draft, options)
  const sendingAt = queued[d.code]
  const noteRef = useRef<HTMLTextAreaElement>(null)
  const free = options.length === 0 || draft.choice === 'other'
  const note = !free && (draft.noteOpen || !!draft.note)
  const title = d.short_title || d.question

  useEffect(() => {
    if (draft.choice === 'other' || draft.noteOpen) noteRef.current?.focus()
  }, [draft.choice, draft.noteOpen])

  const choose = (c: number | 'other') => setDraft(d.code, { choice: draft.choice === c ? null : c })
  const submit = () => answer && send([{ code: d.code, answer, files: draft.files?.map((f) => f.id) }])
  const onKey = (e: KeyboardEvent) => {
    if ((e.metaKey || e.ctrlKey) && e.key === 'Enter') {
      e.preventDefault()
      submit()
      return
    }
    if ((e.target as HTMLElement).tagName === 'TEXTAREA') return
    const k = Number(e.key)
    if (k >= 1 && k <= options.length) choose(k - 1)
    else if (e.key === '0' && options.length) choose('other')
  }

  return (
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
          <h3 className="text-[15px] leading-snug font-semibold text-fg sm:text-base">{title}</h3>
          {d.short_title && <p className="mt-1 text-sm leading-relaxed text-fg-2">{d.question}</p>}
        </div>
        {d.context && <Context text={d.context} />}

        {options.length > 0 && (
          <div className="space-y-1.5" role="radiogroup">
            {options.map((o, i) => {
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
              <AttachZone value={draft.files ?? []} onChange={(files) => setDraft(d.code, { files })} compact>
                <Textarea ref={noteRef} minRows={free ? 3 : 2} value={draft.note} placeholder={free ? t.decision.freePlaceholder : t.decision.notePlaceholder} onChange={(e) => setDraft(d.code, { note: e.target.value })} className="pr-10" />
              </AttachZone>
            </motion.div>
          )}
        </AnimatePresence>

        <div className="flex items-center gap-1.5">
          {!free && !note && typeof draft.choice === 'number' && (
            <Button size="sm" variant="ghost" icon={<Plus className="size-3.5" />} onClick={() => setDraft(d.code, { noteOpen: true })}>
              {t.decision.addNote}
            </Button>
          )}
          {(draft.choice != null || draft.note || !!draft.files?.length) && (
            <Tip content={t.decision.clearDrafts}>
              <Button size="icon-sm" variant="ghost" aria-label={t.decision.clearDrafts} onClick={() => setDraft(d.code, { choice: null, note: '', noteOpen: false, files: [] })}>
                <X className="size-3.5" />
              </Button>
            </Tip>
          )}
          <span className="ml-auto" />
          {options.length > 0 && (
            <Tip content={t.decision.keyTip}>
              <span className="hidden text-fg-3 lg:inline-flex" aria-label={t.decision.keyTip}>
                <Keyboard className="size-4" />
              </span>
            </Tip>
          )}
          <Button variant="primary" size="sm" className="h-9 px-4" disabled={!answer} onClick={submit} icon={<CornerDownLeft className="size-4" />}>
            {t.decision.send}
          </Button>
        </div>
      </div>

      <AnimatePresence>{sendingAt && <SendingOverlay answer={answer} at={sendingAt} onUndo={() => cancel(d.code)} />}</AnimatePresence>
    </div>
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
            width="min-w-[280px]"
            trigger={
              <Button size="sm" variant="primary" icon={<ArrowRightLeft className="size-3.5" />} disabled={busy || !active.length}>
                {active.length ? t.decision.reassign : t.decision.noActive}
              </Button>
            }
            items={[
              { type: 'label', label: t.decision.reassignTo },
              ...active.map((s) => ({
                label: s.title || s.project_name || s.cwd,
                hint: [s.device_name, s.summary || s.last_prompt].filter(Boolean).join(t.common.sep),
                icon: <ToolMark tool={s.tool} size="sm" />,
                onSelect: () => void wrap(async () => (await reassign(d.code, s.key)) && toast.success(t.decision.reassigned)),
              })),
            ]}
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
        <div className="text-xs font-medium text-fg-3">{d.kind === 'feedback' ? t.review.history : t.decision.answer}</div>
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
        <span className="w-9 shrink-0 font-mono text-[11px] font-semibold text-fg-3">{d.code}</span>
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
