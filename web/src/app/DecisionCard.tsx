import { useEffect, useMemo, useRef, useState, type KeyboardEvent } from 'react'
import { ArrowRightLeft, Check, ChevronRight, Keyboard, PenLine, RotateCcw, Sparkles, Timer } from 'lucide-react'
import { toast } from 'sonner'
import type { Decision, Requirement, Session } from '../api/client'
import { useRequirements, useSessions } from '../api/queries'
import { t } from '../i18n'
import { cx, relTime, useNow } from '../lib/util'
import { Badge, Button, Card, Input, Tip } from '../ui/base'
import { Menu, useConfirm } from '../ui/overlay'
import { RoleTag, StatusDot, ToolMark } from '../ui/marks'
import { ProxyNote } from './grant'
import { HoldNote } from './morning'
import { ThumbImg, Thumbs, uploadsBusy, ZoomButton } from '../ui/attach'
import { isImage } from '../lib/files'
import { BlockList } from '../ui/blocks'
import { ProtoCover, ProtoList, ProtoOpen } from '../ui/proto'
import { AnnotateTarget } from '../ui/annotate'
import { useActions, useBusy } from './actions'
import { composeAnswer, decDraftKey, decKey, emptyDraft, useOutbox, type Draft } from './outbox'
import { DraftHint, useDraft } from './draft'
import { DEC_CLEAR } from '../lib/drafts'
import { currentPicks, picksText, PlanSummary, Points } from './points'
import { useOpenReq, useOpenSession } from './nav'
import { sessionTitle } from './SessionCard'
import { targetItems } from './assign'
import { Md, plainText } from '../ui/markdown'
import { Linkify, RefChip } from '../ui/refmark'
import { DecisionChain } from './chain'
import { PendingBar, Seal, usePending, Verdict, verdictKey } from './verdict'
import { requestLeft } from './asks'
import { isStatusReq, StatusReqNote } from './statusReq'
import { approvedOf, recOf, titleOf } from '../lib/waiting'

// 推荐内容与某个选项一致时，返回该选项序号。

// 来源行：独立展示时显示项目、设备与需求；嵌在需求时间线里时只显示编号、来源决策与时间。
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
      {/* 嵌在需求时间线里时，需求就是这一页本身，不再重复（设计原则第 5 条） */}
      {d.requirement_code && !embedded && (
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

// AskerLine（R-241）：没挂需求的决策，卡片顶部一行带上提问会话最近在做什么——会话标题、在做的需求、最近交付的几件；点开是那个会话。
// 只读已有记录：会话关联的需求和它们的状态，不猜会话在想什么。
export function AskerLine({ d }: { d: Decision }) {
  const sessions = useSessions()
  const reqs = useRequirements()
  const openSession = useOpenSession()
  const key = d.origin_session || d.session_key || ''
  if (!key) return null
  const s = (sessions.data?.sessions ?? []).find((x) => x.key === key)
  const title = s ? sessionTitle(s) : d.by_title || d.project_name || ''
  const mine = (reqs.data ?? []).filter((r) => r.sessions.includes(key))
  const recent = (a: Requirement, b: Requirement) => b.updated_at.localeCompare(a.updated_at)
  const working = mine.filter((r) => r.status === 'in_progress' || r.status === 'blocked').sort(recent).slice(0, 2)
  const delivered = mine.filter((r) => r.status === 'review' || r.status === 'done').sort(recent).slice(0, 3)
  const tool = key.split(':')[0]
  return (
    <div
      role="button"
      tabIndex={0}
      onClick={() => openSession(key)}
      onKeyDown={(e) => e.key === 'Enter' && openSession(key)}
      aria-label={t.decision.asker.open}
      className="group/asker flex min-w-0 cursor-pointer items-center gap-2 rounded-xl border border-line bg-surface-2/70 px-3 py-2 text-[13px] text-fg-3 transition hover:border-line-strong hover:bg-hover"
    >
      {s ? <StatusDot state={s.state} /> : tool && <ToolMark tool={tool} size="sm" />}
      <span className="flex min-w-0 flex-1 flex-wrap items-center gap-x-2 gap-y-1">
        <span className="max-w-full min-w-0 truncate font-medium text-fg-2 group-hover/asker:text-fg">{title}</span>
        {working.length ? (
          <span className="inline-flex min-w-0 max-w-full items-center gap-1">
            <span className="shrink-0">{t.decision.asker.working}</span>
            {working.map((r) => (
              <RefChip key={r.code} code={r.code} status={r.status} label="title" className="max-w-[16em]" />
            ))}
          </span>
        ) : (
          <span className="shrink-0">{t.decision.asker.idle}</span>
        )}
        {delivered.length > 0 && (
          <span className="inline-flex min-w-0 max-w-full items-center gap-1">
            <span className="shrink-0">{t.decision.asker.delivered}</span>
            {delivered.map((r) => (
              <RefChip key={r.code} code={r.code} status={r.status} />
            ))}
          </span>
        )}
      </span>
      <ChevronRight className="size-4 shrink-0 opacity-60 group-hover/asker:opacity-100" />
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

// autoFocus：在需求时间线上作答这一条（R-241，从「要你处理」打开）时，焦点放进来，⌘↵ / 数字键直接可用。
export function DecisionComposer({ d, embedded, drawer, autoFocus }: { d: Decision; embedded?: boolean; drawer?: boolean; autoFocus?: boolean }) {
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
  const ri = recOf(d) // 推荐选项读字段 rec_option（R-239），不拿推荐文字去匹配
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
  const title = titleOf(d)
  // 方案对比：任一选项带效果图或原型（R-197）时，选项改为看图点选的卡片
  const optProto = (i: number) => d.option_prototypes?.[i]?.[0]
  const visual = (d.option_files ?? []).some((f) => f?.length) || (d.option_prototypes ?? []).some((p) => p?.length)
  const optImgs = (d.option_files ?? []).flatMap((fs) => fs?.slice(0, 1) ?? [])
  // 在选项效果图上“标注并附上”：把标注图附进答复（附言框常驻，R-161）
  const [incoming, setIncoming] = useState<File[]>([])

  useEffect(() => {
    if (draft.choice === 'other' || draft.noteOpen) noteRef.current?.focus()
  }, [draft.choice, draft.noteOpen])
  const box = useRef<HTMLDivElement>(null)
  useEffect(() => {
    if (drawer || autoFocus) box.current?.focus({ preventScroll: true })
  }, [drawer, autoFocus, d.code])

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
        {!embedded && !d.requirement_code && !isStatusReq(d) && <AskerLine d={d} />}
        <Source d={d} now={now} embedded={embedded} />
        <HoldNote code={d.code} />
        <div>
          <h3 className="font-display text-base leading-snug font-semibold text-fg sm:text-[17px]">{title}</h3>
          {titleOf(d) !== d.question && <p className="mt-1 text-sm leading-relaxed text-fg-2">{d.question}</p>}
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
                <ThumbImg id={f.id} alt={f.name} loading="lazy" className="max-h-[360px] w-full object-cover object-top" />
                <span className="absolute top-2 right-2 opacity-0 transition group-hover/ctx:opacity-100 pointer-coarse:opacity-100">
                  <ZoomButton f={f} group={ctxImgs} />
                </span>
              </div>
            ))}
          </div>
        )}
        {ctxOther.length > 0 && <Thumbs files={ctxOther} />}
        <ProtoList ps={d.prototypes} />

        {multi && <Points d={d} picks={picks} onPick={(i, k) => setDraft(d.code, { picks: picks.map((c, j) => (j === i ? k : c)) })} />}

        {options.length > 0 && visual && (
          <div className="grid grid-cols-1 gap-2.5 @md:grid-cols-2" role="radiogroup">
            {options.map((o, i) => {
              const on = draft.choice === i
              const img = d.option_files?.[i]?.[0]
              const pr = optProto(i)
              if (!img && !pr) return null // 没有效果图、原型的选项放到下面的列表里
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
                  {pr ? (
                    <ProtoOpen p={pr} className="border-b border-line">
                      <ProtoCover p={pr} className="aspect-[16/10]" />
                    </ProtoOpen>
                  ) : (
                    img && (
                      <div className="relative aspect-[16/10] overflow-hidden border-b border-line bg-sunken">
                        <ThumbImg id={img.id} alt={o} loading="lazy" className="size-full object-cover object-top transition-transform duration-300 group-hover/opt:scale-[1.02]" />
                        <span className="absolute top-2 right-2 opacity-0 transition group-hover/opt:opacity-100 pointer-coarse:opacity-100" onClick={(e) => e.stopPropagation()}>
                          <ZoomButton f={img} group={optImgs} />
                        </span>
                      </div>
                    )
                  )}
                  <div className="flex items-start gap-2.5 px-3 py-2.5 text-sm">
                    <span className={cx('mt-px flex size-5 shrink-0 items-center justify-center rounded-full border text-[11px] font-semibold tabular transition-colors', on ? 'border-accent bg-accent text-accent-fg' : 'border-line-strong text-fg-3')}>{i + 1}</span>
                    <span className="min-w-0 flex-1 leading-relaxed break-words">
                      {o}
                      {pr && <span className="mt-0.5 block text-xs text-fg-3">{t.proto.tapToOpen(pr.version)}</span>}
                    </span>
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
              if (visual && (d.option_files?.[i]?.length || optProto(i))) return null
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
            draft={dr}
            draftEmpty={DEC_CLEAR}
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
                {(free || drawer) && <DraftHint h={dr} className="ml-1" />}
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
      <div className="text-[15px] font-semibold text-fg">{titleOf(d)}</div>
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
          <span className="block truncate text-sm text-fg">{titleOf(d)}</span>
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
          {titleOf(d) !== d.question && <p className="text-sm text-fg-2">{d.question}</p>}
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

// pickedOf：你选的是哪几个选项。批准类读结论字段 verdict（R-239）；选项类用 choices，没有时按答复开头的选项文字认（答复就是所选选项的原文）
function pickedOf(d: Decision): Set<number> {
  const opts = d.options ?? []
  const ans = (d.answer || '').trim()
  const byAns = opts.findIndex((o) => !!o.trim() && ans.startsWith(o.trim()))
  const ok = approvedOf(d)
  return new Set(d.approval ? (ok === undefined ? [] : [ok ? 0 : 1]) : d.choices?.length ? d.choices : byAns >= 0 ? [byAns] : [])
}

// answerNote：答复里除去所选选项之外你另写的话（附言）。多点决策的答复是「按以下选择：…\n附言：…」，选项类是「选项；附言」。
export function answerNote(d: Decision): string {
  const ans = (d.answer || '').trim()
  if (!ans) return ''
  if (d.points?.length) {
    if (/^(批准，)?按以下选择：/.test(ans)) {
      const i = ans.indexOf('\n附言：')
      return i >= 0 ? ans.slice(i + 4).trim() : ''
    }
    return ans.replace(/^(批准|拒绝)[：:]?/, '').trim()
  }
  const opts = d.options ?? []
  const hit = [...pickedOf(d)].map((i) => opts[i]?.trim() ?? '').find((o) => o && ans.startsWith(o))
  if (hit) return ans.slice(hit.length).replace(/^[；;：:，,\s]+/, '').trim()
  return ans
}

// DecisionOutcome（R-241）：答完的决策当时的选项和你选的那个（标出推荐）。多点决策逐点列；批准类不列「批准 / 拒绝」两个按钮，结论写在头上。
export function DecisionOutcome({ d }: { d: Decision }) {
  const pts = d.points ?? []
  if (pts.length) {
    const ch = d.choices ?? []
    return (
      <ol className="space-y-2">
        {pts.map((p, i) => (
          <li key={p.id || i} className="space-y-1">
            <p className="text-[13px] leading-snug break-words text-fg-2">
              <span className="mr-1 text-fg-3 tabular">{i + 1}.</span>
              <Linkify text={p.title ?? ''} />
            </p>
            <OptionRows labels={(p.items ?? []).map((o) => o.label ?? '')} picked={new Set(ch[i] != null && ch[i] >= 0 ? [ch[i]] : [])} rec={(p.items ?? []).findIndex((o) => o.recommended)} />
          </li>
        ))}
      </ol>
    )
  }
  if (d.approval || !(d.options ?? []).length) return null
  return <OptionRows labels={d.options ?? []} picked={pickedOf(d)} rec={recOf(d)} />
}

function OptionRows({ labels, picked, rec }: { labels: string[]; picked: Set<number>; rec: number }) {
  return (
    <ul className="space-y-1">
      {labels.map((o, i) => {
        const on = picked.has(i)
        return (
          <li key={i} className={cx('flex items-start gap-2 rounded-lg border px-2.5 py-1.5 text-[13px] leading-relaxed', on ? 'border-accent/50 bg-accent-soft/50 text-fg' : 'border-line/70 text-fg-3')}>
            <span className={cx('mt-[3px] flex size-3.5 shrink-0 items-center justify-center rounded-full border', on ? 'border-accent bg-accent text-accent-fg' : 'border-line-strong')}>{on && <Check className="size-2.5" strokeWidth={3} />}</span>
            <span className="min-w-0 flex-1 break-words">
              <Linkify text={o} />
            </span>
            {on && <span className="shrink-0 text-xs font-medium text-accent">{t.decision.picked}</span>}
            {i === rec && <Badge tone="neutral">{t.decision.recommended}</Badge>}
          </li>
        )
      })}
    </ul>
  )
}

// DecisionPeek：引用里点开的决策（R-196，左边那张只读纸）——谁问的、问题、背景、选项（推荐 / 你选的）、答复、后果链。
export function DecisionPeek({ d }: { d: Decision }) {
  const now = useNow()
  const opts = d.options ?? []
  const rec = recOf(d)
  const picked = pickedOf(d)
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
        <Linkify text={titleOf(d)} />
      </h3>
      {titleOf(d) !== d.question && (
        <p className="text-sm leading-relaxed text-fg-2">
          <Linkify text={d.question} />
        </p>
      )}
      {d.context && <Context text={d.context} />}
      <BlockList blocks={d.blocks} />
      {!!d.points?.length && <DecisionOutcome d={d} />}
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
