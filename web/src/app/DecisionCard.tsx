import { useEffect, useMemo, useRef, useState, type KeyboardEvent } from 'react'
import { ArrowRightLeft, Keyboard, PenLine, RotateCcw, Sparkles, Timer } from 'lucide-react'
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
import { isImage } from '../lib/files'
import { BlockList } from '../ui/blocks'
import { AnnotateTarget } from '../ui/annotate'
import { useActions, useBusy } from './actions'
import { composeAnswer, decDraftKey, decKey, emptyDraft, useOutbox, type Draft } from './outbox'
import { DraftLine, useDraft } from './draft'
import { currentPicks, picksText, PlanSummary, Points } from './points'
import { useOpenReq, useOpenSession } from './nav'
import { targetItems } from './assign'
import { Md, plainText } from '../ui/markdown'
import { Linkify, RefChip } from '../ui/refmark'
import { DecisionChain } from './chain'
import { PendingBar, Seal, usePending, Verdict, verdictKey } from './verdict'
import { requestLeft } from './asks'
import { isStatusReq, StatusReqNote } from './statusReq'

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
        <RefChip code={d.requirement_code} label="title" onOpen={openReq} />
      )}
      {d.from_decision && (
        <span className="inline-flex items-center gap-1">
          <span className={d.overturns ? 'text-danger' : ''}>{d.overturns ? t.chain.overturnsFrom : t.chain.follows}</span>
          <RefChip code={d.from_decision} label="title" />
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
// drawer：在审批抽屉里（R-177）——正文在抽屉中部滚动，附言与判断按钮固定在抽屉底部；打开时焦点放进来，⌘↵ / 数字键直接可用。
// RequestNote：Agent 的限时请求（R-169，PM 推荐开 Dev 会话）——开几个、还剩多久；到点没批按规则视为不开。
function RequestNote({ d, now }: { d: Decision; now: number }) {
  const left = requestLeft(d, now)
  return (
    <div className="flex items-start gap-2.5 rounded-xl border border-wait/25 bg-wait-soft/60 px-3 py-2.5 text-sm">
      <Timer className="mt-0.5 size-4 shrink-0 text-wait" />
      <div className="min-w-0 flex-1 leading-relaxed">
        <div className="font-medium text-fg">
          {t.ask.request.what(d.request_count ?? 1)}
          {left && <span className="ml-2 font-semibold text-wait tabular">{left}</span>}
        </div>
        <div className="text-[13px] text-fg-2">
          {t.ask.request.rule}；{t.ask.request.joinAfter}
        </div>
      </div>
    </div>
  )
}

export function DecisionComposer({ d, embedded, drawer }: { d: Decision; embedded?: boolean; drawer?: boolean }) {
  const now = useNow()
  const { send } = useOutbox()
  const dr = useDraft<Draft>(decDraftKey(d.code), emptyDraft)
  const setDraft = (_: string, patch: Partial<Draft>) => dr.set(patch)
  const { closeDecision } = useActions()
  const confirm = useConfirm()
  const closeIt = async () => {
    if (await confirm({ title: `${t.decision.closeIt} ${d.code}`, description: t.decision.closeConfirm, confirmText: t.decision.closeIt, danger: true })) {
      if (await closeDecision(d.code)) toast(t.decision.closed)
    }
  }
  const draft = dr.value
  const options = useMemo(() => d.options ?? [], [d.options])
  const rec = d.recommendation?.trim() ?? ''
  const ri = useMemo(() => recIndex(options, rec), [options, rec])
  // 多点决策（R-112）：逐点选择，默认选推荐；附言和附图照旧
  const multi = !!d.points?.length
  // 背景附件：图片大图显示，其它文件（R-221）显示为文件块
  const ctxImgs = (d.context_files ?? []).filter((f) => isImage(f.mime))
  const ctxOther = (d.context_files ?? []).filter((f) => !isImage(f.mime))
  const approval = !!d.approval
  // 用词场景（R-176）：批准类决策「批准 / 拒绝」，选项类「回答」
  const scene = d.request_kind ? 'request' : approval ? 'approval' : 'answer' // Agent 的限时请求（R-169）同样是「批准 / 拒绝」
  const picks = multi ? currentPicks(d, draft.picks) : []
  const picked = multi && picks.every((c) => c >= 0)
  const answer = multi ? (picked ? picksText(d, picks, draft.note) : '') : composeAnswer(draft, options)
  // 没选推荐项时，给一个可选的一句话理由框（R-157）：不填照常发送，不增加必填步骤
  const deviates = multi
    ? picked && (d.points ?? []).some((p, i) => (p.items ?? []).some((o) => o.recommended) && !p.items?.[picks[i]]?.recommended)
    : !!rec && !approval && ((typeof draft.choice === 'number' && ri >= 0 && draft.choice !== ri) || (draft.choice === 'other' && options.length > 0))
  const reason = deviates ? (draft.reason ?? '').trim() : ''
  // 撤销期 / 盖印中（R-201：和需求验收同一个调度器，键是 dec:D-8）
  const pendE = usePending(decKey(d.code))
  const sendingAt = !!pendE
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
  const box = useRef<HTMLDivElement>(null)
  useEffect(() => {
    if (drawer) box.current?.focus({ preventScroll: true })
  }, [drawer, d.code])

  const choose = (c: number | 'other') => setDraft(d.code, { choice: draft.choice === c ? null : c })
  // 回答 / 批准 / 拒绝（R-161、R-176）：都先进撤销期（发件箱），卡片底部换成同一个撤销条。
  // 批准类单点：批准 = 选项 0，拒绝 = 选项 1（发出去的答复是后端给的选项文字，后端送给 Agent 时写明是用户明确拒绝）；拒绝必须附意见。
  const files = draft.files?.map((f) => f.id)
  const busyUpload = () => (uploadsBusy() ? (toast(t.attach.wait), true) : false) // 附件没传完先不发
  const submit = () => {
    if (busyUpload()) return
    if (approval && !multi) return send([{ code: d.code, answer: composeAnswer({ ...draft, choice: 0 }, options), files, act: 'pass', scene }])
    if (!answer) return
    const act = approval ? 'pass' : 'answer'
    if (multi) send([{ code: d.code, answer: draft.note.trim(), files, choices: picks, reason: reason || undefined, act, scene }])
    else send([{ code: d.code, answer, files, reason: reason || undefined, act, scene }])
  }
  const back = () => {
    if (busyUpload()) return
    if (multi) send([{ code: d.code, answer: draft.note.trim(), files, reject: true, act: 'back', scene }])
    else send([{ code: d.code, answer: composeAnswer({ ...draft, choice: 1 }, options), files, act: 'back', scene }])
  }
  const onKey = (e: KeyboardEvent) => {
    if (sendingAt) return
    if (verdictKey(e, { pass: submit, back: approval ? () => (draft.note.trim() || draft.files?.length ? back() : noteRef.current?.focus()) : undefined })) return
    if ((e.target as HTMLElement).tagName === 'TEXTAREA' || multi || approval) return
    const k = Number(e.key)
    if (k >= 1 && k <= options.length) choose(k - 1)
    else if (e.key === '0' && options.length) choose('other')
  }

  const body = (
    <>
        <Source d={d} now={now} embedded={embedded} />
        <HoldNote code={d.code} />
        <div>
          <h3 className="font-display text-base leading-snug font-semibold text-fg sm:text-[17px]">{title}</h3>
          {d.short_title && <p className="mt-1 text-sm leading-relaxed text-fg-2">{d.question}</p>}
        </div>
        {isStatusReq(d) ? <StatusReqNote d={d} /> : d.request_kind && <RequestNote d={d} now={now} />}
        {isStatusReq(d) ? null : d.summary?.length ? (
          <PlanSummary lines={d.summary} full={d.context ? <Context text={d.context} expanded /> : undefined} />
        ) : (
          d.context && <Context text={d.context} />
        )}
        <BlockList blocks={d.blocks} />

        {!!ctxImgs.length && (
          <div className={cx('grid gap-2.5', ctxImgs.length > 1 && 'grid-cols-2')}>
            {ctxImgs.map((f) => (
              <div key={f.id} className="group/ctx relative overflow-hidden rounded-xl border border-line bg-sunken">
                <img src={fileUrl(f.id)} alt={f.name} loading="lazy" className="max-h-[360px] w-full object-cover object-top" />
                <span className="absolute top-2 right-2 opacity-0 transition group-hover/ctx:opacity-100 pointer-coarse:opacity-100">
                  <ZoomButton f={f} group={ctxImgs} />
                </span>
              </div>
            ))}
          </div>
        )}
        {ctxOther.length > 0 && <Thumbs files={ctxOther} />}

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

    </>
  )
  const foot = (
    <>
        {deviates && !sendingAt && (
          <Input
            value={draft.reason ?? ''}
            maxLength={200}
            placeholder={t.decision.reasonPlaceholder}
            aria-label={t.decision.reasonPlaceholder}
            onChange={(e) => setDraft(d.code, { reason: e.target.value })}
          />
        )}

        {pendE && pendE.phase !== 'wait' ? (
          <PendingBar k={decKey(d.code)} />
        ) : (
          <Verdict
            pendingKey={decKey(d.code)}
            mode={approval ? 'verdict' : 'answer'}
            scene={scene}
            stack={free || drawer}
            size={drawer ? 'md' : undefined}
            noteRef={noteRef}
            note={draft.note}
            onNote={(v) => setDraft(d.code, { note: v })}
            files={draft.files ?? []}
            onFiles={(f) => setDraft(d.code, { files: f })}
            incoming={incoming}
            onIncoming={() => setIncoming([])}
            draft={<DraftLine h={dr} />}
            placeholder={free ? t.decision.freePlaceholder : undefined}
            minRows={free ? 3 : 1}
            canPass={approval && !multi ? true : !!answer}
            onPass={submit}
            onBack={approval ? back : undefined}
            extra={
              <div className={cx('flex items-center gap-1', !free && 'w-full sm:w-auto')}>
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
    </>
  )
  return (
    <AnnotateTarget.Provider
      value={(f) => {
        setIncoming((x) => [...x, f])
      }}
    >
    <div
      ref={box}
      tabIndex={-1}
      onKeyDown={onKey}
      onClick={(e) => e.stopPropagation()}
      className={cx(
        'group relative outline-none',
        drawer ? 'flex min-h-0 flex-1 flex-col' : 'overflow-hidden rounded-2xl border bg-surface transition-[box-shadow,border] focus-within:shadow-pop',
        !drawer && (answer && !sendingAt ? 'border-accent/50' : 'border-line'),
        !embedded && !drawer && 'shadow-card',
      )}
    >
      {drawer ? (
        <>
          <div className="min-h-0 flex-1 space-y-3 overflow-y-auto overscroll-contain bg-bg/40 px-4 py-5 sm:px-5">
            {body}
          </div>
          <footer className="shrink-0 space-y-2.5 border-t border-line bg-surface px-4 pt-3 pb-[max(0.875rem,env(safe-area-inset-bottom))] sm:px-5">
            {foot}
          </footer>
        </>
      ) : (
        <div className={cx('space-y-3', embedded ? 'p-3.5 sm:p-4' : 'p-4 sm:p-5')}>
        {body}
        {foot}
        </div>
      )}
      <Seal k={decKey(d.code)} />
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
        {d.proxy_by && !d.overridden_at && <RoleTag role="aide" />}
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

// DecisionPeek：引用里点开的决策（R-196，左边那张只读纸）——谁问的、问题、背景、选项（推荐 / 你选的）、答复、后果链。
export function DecisionPeek({ d }: { d: Decision }) {
  const now = useNow()
  const opts = d.options ?? []
  const rec = recIndex(opts, d.recommendation || '')
  // 你选的：批准类按答复开头认（choices 在批准类里另有用途），选项类用 choices，没有时同样按答复开头认
  const ans = (d.answer || '').trim()
  const byAns = opts.findIndex((o) => !!o.trim() && ans.startsWith(o.trim()))
  const picked = new Set(!d.approval && d.choices?.length ? d.choices : byAns >= 0 ? [byAns] : [])
  const tone = d.status === 'delivered' ? 'run' : d.status === 'answered' ? 'reply' : d.status === 'pending' ? 'wait' : 'neutral'
  return (
    <div className="space-y-3">
      <div className="flex flex-wrap items-center gap-2 text-xs text-fg-3">
        <Badge tone={tone}>{t.decision.status[d.status]}</Badge>
        {d.requirement_code && <RefChip code={d.requirement_code} title={d.requirement_title} label="title" />}
        {d.plan_code && <RefChip code={d.plan_code} />}
        <span className="ml-auto tabular">{relTime(d.answered_at || d.created_at, now)}</span>
      </div>
      <h3 className="font-display text-[17px] leading-snug font-semibold text-fg">
        <Linkify text={d.short_title || d.question} />
      </h3>
      {d.short_title && (
        <p className="text-sm leading-relaxed text-fg-2">
          <Linkify text={d.question} />
        </p>
      )}
      {d.context && <Context text={d.context} />}
      <BlockList blocks={d.blocks} />
      {opts.length > 0 && (
        <ol className="space-y-1.5">
          {opts.map((o, i) => (
            <li key={i} className={cx('flex items-start gap-2 rounded-lg border px-3 py-2 text-sm leading-relaxed', picked.has(i) ? 'border-accent/50 bg-accent-soft/50 text-fg' : 'border-line bg-surface text-fg-2')}>
              <span className="mt-px shrink-0 font-mono text-xs text-fg-3 tabular">{i + 1}</span>
              <span className="min-w-0 flex-1 break-words">
                <Linkify text={o} />
              </span>
              {i === rec && <Badge tone="neutral">{t.decision.recommended}</Badge>}
            </li>
          ))}
        </ol>
      )}
      {d.recommendation && rec < 0 && (
        <p className="text-sm leading-relaxed text-fg-2">
          <span className="mr-1.5 text-fg-3">{t.decision.recommendation}</span>
          <Linkify text={d.recommendation} />
        </p>
      )}
      {d.proxy_by ? <ProxyNote d={d} /> : d.answer ? <AnswerBlock d={d} /> : d.status === 'pending' && <p className="text-[13px] text-fg-3">{t.peek.pending}</p>}
      {d.kind === 'decision' && <DecisionChain code={d.code} />}
    </div>
  )
}
