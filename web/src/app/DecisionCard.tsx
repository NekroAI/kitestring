import { useEffect, useMemo, useRef, useState, type KeyboardEvent } from 'react'
import { ArrowRightLeft, Keyboard, PenLine, RotateCcw, Sparkles, X } from 'lucide-react'
import { toast } from 'sonner'
import type { Decision, Session } from '../api/client'
import { t } from '../i18n'
import { cx, relTime, useNow } from '../lib/util'
import { Badge, Button, Card, Input, Tip } from '../ui/base'
import { Menu, useConfirm } from '../ui/overlay'
import { RoleTag, ToolMark } from '../ui/marks'
import { ProxyNote } from './grant'
import { HoldNote } from './morning'
import { fileUrl, Thumbs, uploadsBusy, ZoomButton } from '../ui/attach'
import { BlockList } from '../ui/blocks'
import { AnnotateTarget } from '../ui/annotate'
import { useActions, useBusy } from './actions'
import { composeAnswer, emptyDraft, useOutbox } from './outbox'
import { currentPicks, picksText, PlanSummary, Points } from './points'
import { useOpenReq, useOpenSession } from './nav'
import { targetItems } from './assign'
import { Md, plainText } from '../ui/markdown'
import { Linkify, RefChip } from '../ui/refmark'
import { DecisionChain } from './chain'
import { actWord, Seal, UndoBar, Verdict, verdictKey } from './verdict'

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
      {d.from_decision && (
        <span className="inline-flex items-center gap-1">
          <span className={d.overturns ? 'text-danger' : ''}>{d.overturns ? t.chain.overturnsFrom : t.chain.follows}</span>
          <RefChip code={d.from_decision} />
        </span>
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
      {open || !long ? (
        <Md text={text} className="break-words" />
      ) : (
        <div className="line-clamp-3 break-words whitespace-pre-wrap">
          <Linkify text={plainText(text)} />
        </div>
      )}
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
  const { drafts, setDraft, queued, sealed, send, cancel } = useOutbox()
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
  const approval = !!d.approval
  const picks = multi ? currentPicks(d, draft.picks) : []
  const picked = multi && picks.every((c) => c >= 0)
  const answer = multi ? (picked ? picksText(d, picks, draft.note) : '') : composeAnswer(draft, options)
  // 没选推荐项时，给一个可选的一句话理由框（R-157）：不填照常发送，不增加必填步骤
  const deviates = multi
    ? picked && (d.points ?? []).some((p, i) => (p.items ?? []).some((o) => o.recommended) && !p.items?.[picks[i]]?.recommended)
    : !!rec && !approval && ((typeof draft.choice === 'number' && ri >= 0 && draft.choice !== ri) || (draft.choice === 'other' && options.length > 0))
  const reason = deviates ? (draft.reason ?? '').trim() : ''
  const [sendingAct, setSendingAct] = useState<'pass' | 'back' | 'answer'>('answer')
  const sealedNow = sealed[d.code]
  const sendingAt = queued[d.code] || (sealedNow ? 1 : 0)
  const noteRef = useRef<HTMLTextAreaElement>(null)
  const free = !multi && (options.length === 0 || draft.choice === 'other')
  const title = d.short_title || d.question
  // 方案对比：任一选项带效果图时，选项改为看图点选的卡片
  const visual = (d.option_files ?? []).some((f) => f?.length)
  const optImgs = (d.option_files ?? []).flatMap((fs) => fs?.slice(0, 1) ?? [])
  // 在选项效果图上“标注并附上”：把标注图附进答复（附言框常驻，R-161）
  const [incoming, setIncoming] = useState<File[]>([])

  useEffect(() => {
    if (draft.choice === 'other' || draft.noteOpen) noteRef.current?.focus()
  }, [draft.choice, draft.noteOpen])

  const choose = (c: number | 'other') => setDraft(d.code, { choice: draft.choice === c ? null : c })
  // 回答 / 批准 / 退回（R-161）：都先进撤销期（发件箱），卡片底部换成同一个撤销条。
  // 批准类单点：通过 = 选项 0，退回 = 选项 1（发出去的答复文字仍是后端给的选项，Agent 侧不变）；退回必须附意见。
  const files = draft.files?.map((f) => f.id)
  const busyUpload = () => (uploadsBusy() ? (toast(t.attach.wait), true) : false) // 附件没传完先不发
  const submit = () => {
    if (busyUpload()) return
    if (approval && !multi) {
      setSendingAct('pass')
      return send([{ code: d.code, answer: composeAnswer({ ...draft, choice: 0 }, options), files }])
    }
    if (!answer) return
    setSendingAct(approval ? 'pass' : 'answer')
    if (multi) send([{ code: d.code, answer: draft.note.trim(), files, choices: picks, reason: reason || undefined }])
    else send([{ code: d.code, answer, files, reason: reason || undefined }])
  }
  const back = () => {
    if (busyUpload()) return
    setSendingAct('back')
    if (multi) send([{ code: d.code, answer: draft.note.trim(), files, reject: true }])
    else send([{ code: d.code, answer: composeAnswer({ ...draft, choice: 1 }, options), files }])
  }
  const onKey = (e: KeyboardEvent) => {
    if (sendingAt) return
    if (verdictKey(e, { pass: submit, back: approval ? () => (draft.note.trim() || draft.files?.length ? back() : noteRef.current?.focus()) : undefined })) return
    if ((e.target as HTMLElement).tagName === 'TEXTAREA' || multi || approval) return
    const k = Number(e.key)
    if (k >= 1 && k <= options.length) choose(k - 1)
    else if (e.key === '0' && options.length) choose('other')
  }

  return (
    <AnnotateTarget.Provider
      value={(f) => {
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
        <HoldNote code={d.code} />
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

        {deviates && !sendingAt && (
          <Input
            value={draft.reason ?? ''}
            maxLength={200}
            placeholder={t.decision.reasonPlaceholder}
            aria-label={t.decision.reasonPlaceholder}
            onChange={(e) => setDraft(d.code, { reason: e.target.value })}
          />
        )}

        {sendingAt ? (
          <UndoBar until={sendingAt} done={!!sealedNow} text={(s) => (sealedNow ? t.verdict.sealed(actWord(sendingAct)) : t.verdict.pending(actWord(sendingAct), s))} onUndo={() => cancel(d.code)} />
        ) : (
          <Verdict
            mode={approval ? 'verdict' : 'answer'}
            stack={free}
            noteRef={noteRef}
            note={draft.note}
            onNote={(v) => setDraft(d.code, { note: v })}
            files={draft.files ?? []}
            onFiles={(f) => setDraft(d.code, { files: f })}
            incoming={incoming}
            onIncoming={() => setIncoming([])}
            placeholder={free ? t.decision.freePlaceholder : undefined}
            minRows={free ? 3 : 1}
            canPass={approval && !multi ? true : !!answer}
            onPass={submit}
            onBack={approval ? back : undefined}
            extra={
              <div className={cx('flex items-center gap-1', !free && 'w-full sm:w-auto')}>
                {(draft.choice != null || draft.note || !!draft.files?.length || !!draft.picks || !!draft.reason) && (
                  <Tip content={t.decision.clearDrafts}>
                    <Button size="icon-sm" variant="ghost" aria-label={t.decision.clearDrafts} onClick={() => setDraft(d.code, { choice: null, note: '', noteOpen: false, files: [], picks: undefined, reason: undefined })}>
                      <X className="size-3.5" />
                    </Button>
                  </Tip>
                )}
                <Button size="sm" variant="ghost" className="text-fg-3 hover:text-danger" onClick={closeIt}>
                  {t.decision.closeIt}
                </Button>
                {options.length > 0 && !multi && !approval && (
                  <Tip content={t.decision.keyTip}>
                    <span className="hidden text-fg-3 lg:inline-flex" aria-label={t.decision.keyTip}>
                      <Keyboard className="size-4" />
                    </span>
                  </Tip>
                )}
              </div>
            }
          />
        )}
      </div>
      <Seal show={!!sealedNow} act={sendingAct} />
    </div>
    </AnnotateTarget.Provider>
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
      {d.answer_reason && (
        <div>
          <div className="text-xs font-medium text-fg-3">{t.decision.reasonLabel}</div>
          <div className="mt-0.5 text-sm leading-relaxed break-words text-fg-2">{d.answer_reason}</div>
        </div>
      )}
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
        {d.proxy_by && !d.overridden_at && <RoleTag role="advisor" />}
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
          {d.proxy_by ? <ProxyNote d={d} /> : d.answer && <AnswerBlock d={d} />}
          {d.kind === 'decision' && <DecisionChain code={d.code} />}
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
