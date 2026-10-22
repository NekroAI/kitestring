import { useEffect, useMemo, useState } from 'react'
import { Link, useSearchParams } from 'react-router'
import { useQueryClient } from '@tanstack/react-query'
import { api, errMsg, unwrap, type Decision } from '../api/client'
import { decisionStatuses, useDecisions, useSessions, type DecisionStatus } from '../api/queries'
import { ConfirmButton, CountBadge, Empty, ErrorBox, Loading, Spinner, StateBadge, cx, useNow, useToast } from '../components/ui'
import { t } from '../i18n'
import { absTime, relTime } from '../lib/time'

type Draft = { choice: string; note: string }
const DRAFT_KEY = 'ks.decision.drafts'

function loadDrafts(): Record<string, Draft> {
  try {
    return JSON.parse(localStorage.getItem(DRAFT_KEY) || '{}') as Record<string, Draft>
  } catch {
    return {}
  }
}

function saveDrafts(d: Record<string, Draft>) {
  try {
    localStorage.setItem(DRAFT_KEY, JSON.stringify(d))
  } catch {
    // 忽略存储不可用
  }
}

// composeAnswer 把选中的选项与补充说明合成为最终答复。
function composeAnswer(d?: Draft): string {
  if (!d) return ''
  const c = d.choice.trim()
  const n = d.note.trim()
  if (c && n) return t.decisions.joinAnswer(c, n)
  return c || n
}

// recommendedOption 找出与推荐一致的选项。
function recommendedOption(options: string[], rec: string): string | null {
  const r = rec.trim()
  if (!r) return null
  return options.find((o) => o.trim() === r) ?? options.find((o) => o.trim() && r.startsWith(o.trim())) ?? null
}

export default function DecisionsPage() {
  const [params, setParams] = useSearchParams()
  const tab = (decisionStatuses.includes(params.get('tab') as DecisionStatus) ? params.get('tab') : 'pending') as DecisionStatus
  const queries = {
    pending: useDecisions('pending'),
    answered: useDecisions('answered'),
    delivered: useDecisions('delivered'),
    closed: useDecisions('closed'),
  }
  const q = queries[tab]
  const now = useNow()

  const setTab = (s: DecisionStatus) => {
    const p = new URLSearchParams(params)
    p.set('tab', s)
    setParams(p, { replace: true })
  }

  return (
    <div className="space-y-4">
      <h1 className="text-lg font-semibold">{t.decisions.title}</h1>
      <div className="-mx-4 overflow-x-auto px-4">
        <div className="inline-flex min-w-full gap-1 rounded-xl bg-slate-200/70 p-1 sm:min-w-0 dark:bg-slate-800/70">
          {decisionStatuses.map((s) => {
            const n = queries[s].data?.length ?? 0
            return (
              <button
                key={s}
                onClick={() => setTab(s)}
                className={cx(
                  'inline-flex h-9 flex-1 items-center justify-center gap-1.5 rounded-lg px-3 text-sm font-medium whitespace-nowrap sm:flex-none',
                  tab === s ? 'bg-white text-slate-900 shadow-sm dark:bg-slate-950 dark:text-white' : 'text-slate-600 dark:text-slate-400',
                )}
              >
                {t.decisions.tabs[s]}
                {s === 'pending' || s === 'answered' ? <CountBadge n={n} tone={s === 'pending' ? 'amber' : 'slate'} /> : null}
              </button>
            )
          })}
        </div>
      </div>

      {q.isPending ? (
        <Loading />
      ) : q.isError ? (
        <ErrorBox error={q.error} onRetry={() => q.refetch()} />
      ) : tab === 'pending' ? (
        <PendingList items={q.data} now={now} />
      ) : q.data.length === 0 ? (
        <Empty>{tab === 'answered' ? t.decisions.emptyAnswered : tab === 'delivered' ? t.decisions.emptyDelivered : t.decisions.emptyClosed}</Empty>
      ) : (
        <div className="space-y-3">
          {q.data.map((d) =>
            tab === 'answered' ? <AnsweredCard key={d.id} d={d} now={now} /> : tab === 'delivered' ? <DeliveredCard key={d.id} d={d} now={now} /> : <ClosedCard key={d.id} d={d} now={now} />,
          )}
        </div>
      )}
    </div>
  )
}

function Source({ d, now }: { d: Decision; now: number }) {
  const parts = [d.project_name, d.device_name].filter(Boolean)
  return (
    <div className="flex flex-wrap items-center gap-x-2 gap-y-1 text-xs">
      <span className="chip chip-gray font-mono">{d.code}</span>
      {parts.length > 0 && <span className="muted min-w-0 truncate">{parts.join(t.common.sep)}</span>}
      {d.requirement_code && (
        <Link to={`/requirements?r=${encodeURIComponent(d.requirement_code)}`} className="chip bg-sky-50 font-mono text-sky-800 dark:bg-sky-950/60 dark:text-sky-300">
          {d.requirement_code}
        </Link>
      )}
      <span className="muted ml-auto shrink-0">{relTime(d.created_at, now)}</span>
    </div>
  )
}

function Question({ d }: { d: Decision }) {
  const [open, setOpen] = useState(false)
  const long = (d.context?.length ?? 0) > 180
  return (
    <div className="space-y-1.5">
      <p className="text-[15px] leading-snug font-semibold break-words">{d.question}</p>
      {d.context && (
        <div className="text-sm">
          <p className={cx('muted break-words whitespace-pre-wrap', long && !open && 'line-clamp-4')}>{d.context}</p>
          {long && (
            <button className="mt-0.5 text-xs font-medium text-sky-700 dark:text-sky-400" onClick={() => setOpen(!open)}>
              {open ? t.common.showLess : t.common.showMore}
            </button>
          )}
        </div>
      )}
    </div>
  )
}

function PendingList({ items, now }: { items: Decision[]; now: number }) {
  const qc = useQueryClient()
  const toast = useToast()
  const [drafts, setDrafts] = useState<Record<string, Draft>>(loadDrafts)
  const [busy, setBusy] = useState(false)

  // 清理已不再待决的草稿
  useEffect(() => {
    const live = new Set(items.map((d) => d.code))
    setDrafts((cur) => {
      const next = Object.fromEntries(Object.entries(cur).filter(([k]) => live.has(k)))
      return Object.keys(next).length === Object.keys(cur).length ? cur : next
    })
  }, [items])
  useEffect(() => saveDrafts(drafts), [drafts])

  const filled = useMemo(() => items.filter((d) => composeAnswer(drafts[d.code])), [items, drafts])

  const update = (code: string, patch: Partial<Draft>) =>
    setDrafts((cur) => {
      const d: Draft = Object.assign({ choice: '', note: '' }, cur[code], patch)
      const next = { ...cur }
      if (!d.choice && !d.note) delete next[code]
      else next[code] = d
      return next
    })

  const submit = async () => {
    setBusy(true)
    try {
      const answers = filled.map((d) => ({ ref: d.code, answer: composeAnswer(drafts[d.code]) }))
      await unwrap(api.POST('/api/decisions/answers', { body: { answers } }))
      setDrafts((cur) => {
        const next = { ...cur }
        for (const a of answers) delete next[a.ref]
        return next
      })
      toast(t.decisions.submitted(answers.length))
      void qc.invalidateQueries({ queryKey: ['decisions'] })
      void qc.invalidateQueries({ queryKey: ['sessions'] })
      void qc.invalidateQueries({ queryKey: ['requirements'] })
    } catch (e) {
      toast(errMsg(e), 'err')
    } finally {
      setBusy(false)
    }
  }

  if (items.length === 0) return <Empty>{t.decisions.emptyPending}</Empty>

  return (
    <>
      <div className={cx('space-y-3', filled.length > 0 && 'pb-20')}>
        {items.map((d) => (
          <PendingCard key={d.id} d={d} now={now} draft={drafts[d.code]} onChange={(p) => update(d.code, p)} />
        ))}
      </div>
      {filled.length > 0 && (
        <div className="fixed inset-x-0 bottom-[calc(3.5rem+env(safe-area-inset-bottom))] z-20 px-3 pb-3 sm:bottom-0 sm:pb-[max(env(safe-area-inset-bottom),1rem)]">
          <div className="mx-auto flex max-w-3xl items-center gap-2 rounded-xl border border-slate-300 bg-white/95 p-2 pl-4 shadow-lg backdrop-blur dark:border-slate-700 dark:bg-slate-900/95">
            <span className="text-sm font-medium">{t.decisions.filled(filled.length)}</span>
            <button className="btn btn-ghost btn-sm ml-auto" onClick={() => setDrafts({})} disabled={busy}>
              {t.decisions.clearDraft}
            </button>
            <button className="btn btn-primary" onClick={submit} disabled={busy}>
              {busy && <Spinner />}
              {t.decisions.submitN(filled.length)}
            </button>
          </div>
        </div>
      )}
    </>
  )
}

function PendingCard({ d, now, draft, onChange }: { d: Decision; now: number; draft?: Draft; onChange: (p: Partial<Draft>) => void }) {
  const options = d.options ?? []
  const rec = d.recommendation?.trim() ?? ''
  const recOpt = recommendedOption(options, rec)
  const choice = draft?.choice ?? ''
  const answer = composeAnswer(draft)
  const usingRec = !!rec && choice === (recOpt ?? rec)

  return (
    <article className={cx('card space-y-3 p-3 sm:p-4', answer && 'border-sky-400 ring-1 ring-sky-400 dark:border-sky-600 dark:ring-sky-600')}>
      <Source d={d} now={now} />
      <Question d={d} />

      {options.length > 0 && (
        <div className="grid gap-2 sm:grid-cols-2">
          {options.map((o, i) => {
            const selected = choice === o
            const isRec = o === recOpt
            return (
              <button
                key={i}
                type="button"
                onClick={() => onChange({ choice: selected ? '' : o })}
                className={cx(
                  'flex min-h-11 items-start gap-2 rounded-lg border px-3 py-2 text-left text-sm transition-colors',
                  selected
                    ? 'border-slate-900 bg-slate-900 text-white dark:border-slate-100 dark:bg-slate-100 dark:text-slate-900'
                    : isRec
                      ? 'border-amber-400 bg-amber-50 hover:bg-amber-100 dark:border-amber-600 dark:bg-amber-950/40 dark:hover:bg-amber-950/70'
                      : 'border-slate-300 hover:bg-slate-50 dark:border-slate-700 dark:hover:bg-slate-800',
                )}
              >
                <span className={cx('font-mono text-xs leading-5 tabular-nums', selected ? 'opacity-80' : 'muted')}>{i + 1}.</span>
                <span className="min-w-0 flex-1 break-words">{o}</span>
                {isRec && <span className={cx('chip shrink-0', selected ? 'bg-amber-400 text-black' : 'bg-amber-500 text-white dark:text-black')}>{t.decisions.recommended}</span>}
              </button>
            )
          })}
        </div>
      )}

      {rec && !recOpt && (
        <div className="rounded-lg bg-amber-50 px-3 py-2 text-sm dark:bg-amber-950/40">
          <span className="mr-1.5 text-xs font-semibold text-amber-800 dark:text-amber-300">{t.decisions.recommendation}</span>
          <span className="break-words">{rec}</span>
        </div>
      )}

      {options.length === 0 && !rec && <p className="muted text-xs">{t.decisions.noOptions}</p>}

      <div className="flex items-start gap-2">
        <textarea
          className="input min-h-10 flex-1 resize-y"
          rows={options.length > 0 || rec ? 1 : 2}
          placeholder={options.length > 0 || rec ? t.decisions.notePlaceholder : t.decisions.answerPlaceholder}
          value={draft?.note ?? ''}
          onChange={(e) => onChange({ note: e.target.value })}
        />
        {rec && (
          <button
            type="button"
            className={cx('btn shrink-0', usingRec ? 'btn-primary' : 'border-amber-400 bg-amber-50 text-amber-900 hover:bg-amber-100 dark:border-amber-600 dark:bg-amber-950/40 dark:text-amber-200 dark:hover:bg-amber-950/70')}
            onClick={() => onChange({ choice: usingRec ? '' : (recOpt ?? rec) })}
          >
            {t.decisions.useRecommended}
          </button>
        )}
      </div>

      {answer && (
        <p className="text-xs break-words">
          <span className="muted mr-1.5">{t.decisions.answerPreview}</span>
          <span className="font-medium">{answer}</span>
        </p>
      )}
    </article>
  )
}

function AnswerBox({ d }: { d: Decision }) {
  return (
    <div className="rounded-lg bg-slate-100 px-3 py-2 text-sm dark:bg-slate-800">
      <div className="muted mb-0.5 text-xs">{t.decisions.answer}</div>
      <div className="break-words whitespace-pre-wrap">{d.answer || t.common.none}</div>
    </div>
  )
}

function AnsweredCard({ d, now }: { d: Decision; now: number }) {
  const qc = useQueryClient()
  const toast = useToast()
  const sessions = useSessions()
  const [target, setTarget] = useState('')
  const [busy, setBusy] = useState(false)
  const gone = !d.session_state || d.session_state === 'ended' || d.session_state === 'lost'
  const active = (sessions.data?.sessions ?? []).filter((s) => s.state !== 'ended' && s.state !== 'lost' && s.key !== d.session_key)

  const act = async (fn: () => Promise<unknown>, okText: string) => {
    setBusy(true)
    try {
      await fn()
      toast(okText)
      void qc.invalidateQueries({ queryKey: ['decisions'] })
    } catch (e) {
      toast(errMsg(e), 'err')
    } finally {
      setBusy(false)
    }
  }

  const close = () => act(() => unwrap(api.POST('/api/decisions/{ref}/close', { params: { path: { ref: d.code } }, body: {} })), t.decisions.closed)

  return (
    <article className={cx('card space-y-3 p-3 sm:p-4', gone && 'border-red-300 dark:border-red-900')}>
      <Source d={d} now={now} />
      <p className="text-[15px] font-semibold break-words">{d.question}</p>
      <AnswerBox d={d} />
      <div className="flex flex-wrap items-center gap-x-3 gap-y-1 text-xs">
        <span className="muted">{t.decisions.answeredAt(relTime(d.answered_at, now))}</span>
        <span className="muted">{t.decisions.targetSession}</span>
        <span className="font-mono break-all">{d.session_key || t.common.none}</span>
        {d.session_state && <StateBadge state={d.session_state} />}
      </div>

      {gone ? (
        <div className="space-y-2 rounded-lg border border-red-300 bg-red-50 p-3 dark:border-red-900 dark:bg-red-950/40">
          <p className="text-sm font-semibold text-red-700 dark:text-red-400">{t.decisions.sessionGone}</p>
          <p className="text-xs text-red-700/80 dark:text-red-300/80">{t.decisions.sessionGoneHint}</p>
          <div className="flex flex-col gap-2 sm:flex-row">
            <select className="input sm:flex-1" value={target} onChange={(e) => setTarget(e.target.value)} disabled={active.length === 0}>
              <option value="">{active.length ? t.decisions.reassignPick : t.decisions.reassignNone}</option>
              {active.map((s) => (
                <option key={s.key} value={s.key}>
                  {[t.tool[s.tool] ?? s.tool, s.project_name || t.wall.noProject, s.device_name, t.state[s.state] ?? s.state, s.last_prompt.slice(0, 24)].filter(Boolean).join(t.common.sep)}
                </option>
              ))}
            </select>
            <div className="flex gap-2">
              <button
                className="btn btn-primary flex-1"
                disabled={!target || busy}
                onClick={() =>
                  act(() => unwrap(api.POST('/api/decisions/{ref}/reassign', { params: { path: { ref: d.code } }, body: { session_key: target } })), t.decisions.reassigned)
                }
              >
                {t.decisions.reassign}
              </button>
              <button className="btn" disabled={busy} onClick={close}>
                {t.decisions.closeDecision}
              </button>
            </div>
          </div>
        </div>
      ) : (
        <div className="flex items-center gap-2">
          <p className="muted flex-1 text-xs">{t.decisions.waitingInject}</p>
          <ConfirmButton className="btn btn-sm" message={t.decisions.closeConfirm(d.code)} onConfirm={close} danger={false}>
            {t.decisions.closeDecision}
          </ConfirmButton>
        </div>
      )}
    </article>
  )
}

function DeliveredCard({ d, now }: { d: Decision; now: number }) {
  const qc = useQueryClient()
  const toast = useToast()
  const [busy, setBusy] = useState(false)
  return (
    <article className="card space-y-3 p-3 sm:p-4">
      <Source d={d} now={now} />
      <p className="text-[15px] font-semibold break-words">{d.question}</p>
      <AnswerBox d={d} />
      <div className="flex flex-wrap items-center gap-x-3 gap-y-2 text-xs">
        <span className="text-emerald-700 dark:text-emerald-400">{t.decisions.deliveredAt(absTime(d.delivered_at))}</span>
        {d.delivered_turn && (
          <span className="muted">
            {t.decisions.turn} <span className="font-mono">{d.delivered_turn}</span>
          </span>
        )}
        <span className="muted font-mono break-all">{d.session_key}</span>
        <button
          className="btn btn-sm ml-auto"
          disabled={busy}
          onClick={async () => {
            setBusy(true)
            try {
              await unwrap(api.POST('/api/decisions/{ref}/resend', { params: { path: { ref: d.code } }, body: {} }))
              toast(t.decisions.resent)
              void qc.invalidateQueries({ queryKey: ['decisions'] })
            } catch (e) {
              toast(errMsg(e), 'err')
            } finally {
              setBusy(false)
            }
          }}
        >
          {t.decisions.resend}
        </button>
      </div>
    </article>
  )
}

function ClosedCard({ d, now }: { d: Decision; now: number }) {
  return (
    <article className="card space-y-2 p-3 opacity-80 sm:p-4">
      <Source d={d} now={now} />
      <p className="text-sm font-semibold break-words">{d.question}</p>
      {d.answer && <AnswerBox d={d} />}
    </article>
  )
}
