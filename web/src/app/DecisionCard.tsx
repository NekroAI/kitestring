import { useEffect, useMemo, useRef, useState, type KeyboardEvent } from 'react'
import { AnimatePresence, motion } from 'motion/react'
import { ArrowRightLeft, CornerDownLeft, PenLine, Plus, RotateCcw, Sparkles, Undo2, XCircle } from 'lucide-react'
import { toast } from 'sonner'
import type { Decision, Session } from '../api/client'
import { t } from '../i18n'
import { cx, relTime, useNow } from '../lib/util'
import { Badge, Button, Card, Kbd, Textarea } from '../ui/base'
import { Menu } from '../ui/overlay'
import { StatusDot, ToolMark } from '../ui/marks'
import { useActions, useBusy } from './actions'
import { composeAnswer, emptyDraft, UNDO_MS, useOutbox } from './outbox'
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

function Source({ d, now }: { d: Decision; now: number }) {
  const openSession = useOpenSession()
  const openReq = useOpenReq()
  const tool = d.session_key?.split(':')[0] ?? ''
  return (
    <div className="flex flex-wrap items-center gap-x-2 gap-y-1 text-[13px] text-fg-3">
      <span className="rounded-md bg-sunken px-1.5 py-0.5 font-mono text-[11px] font-semibold text-fg-2">{d.code}</span>
      {d.session_key ? (
        <button onClick={() => openSession(d.session_key!)} className="inline-flex min-w-0 items-center gap-1.5 rounded-md hover:text-fg">
          {tool && <ToolMark tool={tool} size="sm" />}
          <span className="truncate">{[d.project_name, d.device_name].filter(Boolean).join(t.common.sep)}</span>
        </button>
      ) : (
        d.project_name && <span>{d.project_name}</span>
      )}
      {d.requirement_code && (
        <button onClick={() => openReq(d.requirement_code!)} className="rounded-md bg-accent-soft px-1.5 py-0.5 font-mono text-[11px] font-medium text-accent hover:brightness-95">
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
        <button onClick={() => setOpen(!open)} className="mt-1 text-[13px] font-medium text-accent hover:underline">
          {open ? t.common.collapse : t.common.showAll}
        </button>
      )}
    </div>
  )
}

// DecisionComposer：待决策卡片，像回复消息一样作答。
export function DecisionComposer({ d }: { d: Decision }) {
  const now = useNow()
  const { drafts, setDraft, queued, send, cancel } = useOutbox()
  const draft = drafts[d.code] ?? emptyDraft
  const options = d.options ?? []
  const rec = d.recommendation?.trim() ?? ''
  const ri = useMemo(() => recIndex(options, rec), [options, rec])
  const answer = composeAnswer(draft, options)
  const sendingAt = queued[d.code]
  const noteRef = useRef<HTMLTextAreaElement>(null)
  const showFree = options.length === 0 || draft.choice === 'other'
  const showNote = !showFree && (draft.noteOpen || !!draft.note)

  useEffect(() => {
    if (draft.choice === 'other' || draft.noteOpen) noteRef.current?.focus()
  }, [draft.choice, draft.noteOpen])

  const choose = (c: number | 'other') => setDraft(d.code, { choice: draft.choice === c ? null : c })
  const submit = () => answer && send([{ code: d.code, answer }])

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
    <Card tabIndex={-1} onKeyDown={onKey} className={cx('group relative overflow-hidden outline-none transition-shadow focus-within:shadow-pop', answer && !sendingAt && 'border-accent/50')}>
      <div className="space-y-3.5 p-4 sm:p-5">
        <Source d={d} now={now} />
        <h3 className="text-base leading-snug font-semibold text-fg sm:text-[17px]">{d.question}</h3>
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
                    'flex w-full items-start gap-3 rounded-xl border px-3 py-2.5 text-left text-sm transition-all duration-150',
                    on ? 'border-accent bg-accent-soft text-fg shadow-xs' : 'border-line bg-surface hover:border-line-strong hover:bg-hover',
                  )}
                >
                  <span className={cx('mt-px flex size-5 shrink-0 items-center justify-center rounded-full border text-[11px] font-semibold tabular transition-colors', on ? 'border-accent bg-accent text-accent-fg' : 'border-line-strong text-fg-3')}>{i + 1}</span>
                  <span className="min-w-0 flex-1 leading-relaxed break-words">{o}</span>
                  {i === ri && (
                    <Badge tone="accent" icon={<Sparkles className="size-3" />}>
                      {t.decision.recommended}
                    </Badge>
                  )}
                </button>
              )
            })}
            <button
              role="radio"
              aria-checked={draft.choice === 'other'}
              onClick={() => choose('other')}
              className={cx(
                'flex w-full items-center gap-3 rounded-xl border border-dashed px-3 py-2.5 text-left text-sm transition-all',
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
            <div className="min-w-0 flex-1">
              <div className="text-xs font-medium text-accent">{t.decision.recommendation}</div>
              <div className="mt-0.5 leading-relaxed break-words text-fg-2">{rec}</div>
            </div>
            {options.length === 0 && (
              <Button size="sm" variant="soft" onClick={() => setDraft(d.code, { note: rec })}>
                {t.decision.useRecommendation}
              </Button>
            )}
          </div>
        )}

        <AnimatePresence initial={false}>
          {(showFree || showNote) && (
            <motion.div initial={{ height: 0, opacity: 0 }} animate={{ height: 'auto', opacity: 1 }} exit={{ height: 0, opacity: 0 }} transition={{ duration: 0.18 }} className="overflow-hidden px-px pb-px">
              <Textarea
                ref={noteRef}
                minRows={showFree ? 3 : 2}
                value={draft.note}
                placeholder={showFree ? (options.length ? t.decision.otherPlaceholder : t.decision.freePlaceholder) : t.decision.notePlaceholder}
                onChange={(e) => setDraft(d.code, { note: e.target.value })}
              />
            </motion.div>
          )}
        </AnimatePresence>

        <div className="flex flex-wrap items-center gap-2 pt-0.5">
          {!showFree && !showNote && typeof draft.choice === 'number' && (
            <Button size="sm" variant="ghost" icon={<Plus className="size-3.5" />} onClick={() => setDraft(d.code, { noteOpen: true })}>
              {t.decision.addNote}
            </Button>
          )}
          <span className="hidden items-center gap-1 text-xs text-fg-3 lg:inline-flex lg:opacity-0 lg:transition-opacity lg:group-focus-within:opacity-100">
            <Kbd>1</Kbd>–<Kbd>{Math.max(1, options.length)}</Kbd> {t.decision.keyHint}
          </span>
          <Button variant="primary" className="ml-auto" disabled={!answer} onClick={submit} icon={<CornerDownLeft className="size-4" />}>
            {t.decision.send}
          </Button>
        </div>
      </div>

      <AnimatePresence>{sendingAt && <SendingOverlay code={d.code} answer={answer} at={sendingAt} onUndo={() => cancel(d.code)} />}</AnimatePresence>
    </Card>
  )
}

function SendingOverlay({ code, answer, at, onUndo }: { code: string; answer: string; at: number; onUndo: () => void }) {
  return (
    <motion.div initial={{ opacity: 0 }} animate={{ opacity: 1 }} exit={{ opacity: 0 }} className="absolute inset-0 z-10 flex flex-col items-center justify-center gap-3 bg-surface/92 px-6 text-center backdrop-blur-[2px]">
      <div className="text-sm font-medium text-fg">{t.decision.sending(code)}</div>
      <div className="line-clamp-2 max-w-md text-[13px] text-fg-3">{answer}</div>
      <Button size="sm" icon={<Undo2 className="size-3.5" />} onClick={onUndo}>
        {t.common.undo}
      </Button>
      <motion.div className="absolute bottom-0 left-0 h-0.5 bg-accent" initial={{ width: '0%' }} animate={{ width: '100%' }} transition={{ duration: Math.max(0, at - Date.now()) / 1000, ease: 'linear' }} key={at} />
      <span className="sr-only">{UNDO_MS}</span>
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
    <Card className="space-y-3 p-4 sm:p-5">
      <Source d={d} now={now} />
      <div className="text-[15px] font-semibold text-fg">{d.question}</div>
      <AnswerBlock d={d} />
      <div className="flex items-center gap-2 rounded-xl bg-wait-soft px-3 py-2 text-[13px] text-wait">
        <StatusDot state="lost" />
        {t.decision.sessionEnded}
      </div>
      <div className="flex flex-wrap justify-end gap-2">
        <Button size="sm" variant="ghost" icon={<XCircle className="size-3.5" />} disabled={busy} onClick={() => wrap(() => closeDecision(d.code))}>
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
              label: [s.project_name || s.cwd, s.device_name].filter(Boolean).join(t.common.sep),
              hint: s.summary || s.last_prompt,
              icon: <ToolMark tool={s.tool} size="sm" />,
              onSelect: () => void wrap(async () => (await reassign(d.code, s.key)) && toast.success(t.decision.reassigned)),
            })),
          ]}
        />
      </div>
    </Card>
  )
}

export function AnswerBlock({ d }: { d: Decision }) {
  return (
    <div className="rounded-xl border border-line bg-surface-2 px-3 py-2.5">
      <div className="text-xs font-medium text-fg-3">{t.decision.answer}</div>
      <div className="mt-0.5 text-sm leading-relaxed break-words whitespace-pre-wrap text-fg">{d.answer}</div>
    </div>
  )
}

// DecisionRow：历史记录里的一行。
export function DecisionRow({ d }: { d: Decision }) {
  const now = useNow()
  const { resendDecision } = useActions()
  const [open, setOpen] = useState(false)
  const tone = d.status === 'delivered' ? 'run' : d.status === 'answered' ? 'reply' : d.status === 'pending' ? 'wait' : 'neutral'
  const when = d.status === 'delivered' ? t.decision.deliveredAt(relTime(d.delivered_at, now)) : d.status === 'answered' ? t.decision.answeredAt(relTime(d.answered_at, now)) : relTime(d.created_at, now)
  return (
    <div className="border-b border-line last:border-0">
      <button onClick={() => setOpen(!open)} className="flex w-full items-center gap-3 px-4 py-3 text-left hover:bg-hover/60">
        <span className="w-10 shrink-0 font-mono text-[11px] font-semibold text-fg-3">{d.code}</span>
        <span className="min-w-0 flex-1">
          <span className="block truncate text-sm text-fg">{d.question}</span>
          {d.answer && <span className="block truncate text-[13px] text-fg-3">→ {d.answer}</span>}
        </span>
        <span className="hidden shrink-0 text-xs text-fg-3 sm:block">{when}</span>
        <Badge tone={tone}>{t.decision.status[d.status]}</Badge>
      </button>
      <AnimatePresence initial={false}>
        {open && (
          <motion.div initial={{ height: 0 }} animate={{ height: 'auto' }} exit={{ height: 0 }} className="overflow-hidden">
            <div className="space-y-3 px-4 pb-4 sm:pl-[68px]">
              <Source d={d} now={now} />
              {d.context && <Context text={d.context} />}
              {d.answer && <AnswerBlock d={d} />}
              {d.status === 'delivered' && (
                <Button size="sm" icon={<RotateCcw className="size-3.5" />} onClick={async () => (await resendDecision(d.code)) && toast.success(t.decision.resent)}>
                  {t.decision.resend}
                </Button>
              )}
            </div>
          </motion.div>
        )}
      </AnimatePresence>
    </div>
  )
}
