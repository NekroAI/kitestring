import { useEffect, useLayoutEffect, useRef, useState, type ReactNode } from 'react'
import { Link } from 'react-router'
import { ChevronRight, Radio } from 'lucide-react'
import { ApiError, type Decision, type Evidence, type Requirement } from '../api/client'
import { t } from '../i18n'
import { actKey, guestPath, statusBar } from '../lib/guest'
import { absTime, cx, relTime, useNow } from '../lib/util'
import { Badge, CardSkeleton, ErrorState, Tip } from '../ui/base'
import { BlockList } from '../ui/blocks'
import { Thumbs } from '../ui/attach'
import { Md } from '../ui/markdown'
import { displayState, RoleTag, StatusPill } from '../ui/marks'
import { DecGlyph, Linkify, PlanGlyph, RefChip, ReqGlyph, ReqStatus, statusCls } from '../ui/refmark'
import { EvidenceBody } from '../app/evidence'
import { hm, ItemNode, type TurnItem } from '../app/sessionTurns'
import { useGuestPlan, useGuestReq, useGuestSession, useMyComments, type GuestItem, type GuestOverview, type GuestSession, type GuestTurn, type ShareActivity } from './data'
import { CommentSlot, Compose, MyComments } from './slots'

// 访客页的各个视图（R-281 第 3 段）：概览、需求、计划、会话。全部只读，没有任何改动入口。

type Overview = Omit<GuestOverview, 'nodes' | 'codes' | 'sessions' | 'activity' | 'decs'> & {
  nodes: NonNullable<GuestOverview['nodes']>
  sessions: GuestSession[]
  activity: ShareActivity[]
  decs: NonNullable<GuestOverview['decs']>
}

const sep = t.common.sep
const active = ['in_progress', 'review', 'blocked']
const ended = (s: GuestSession) => s.state === 'ended' || s.state === 'lost'

function Section({ title, extra, children, className }: { title: ReactNode; extra?: ReactNode; children: ReactNode; className?: string }) {
  return (
    <section className={cx('min-w-0', className)}>
      <h2 className="mb-2 flex items-center gap-2 text-xs font-semibold tracking-[0.04em] text-fg-3">
        {title}
        {extra && <span className="font-normal tabular">{extra}</span>}
      </h2>
      {children}
    </section>
  )
}

const Rel = ({ at, now, className }: { at?: string | null; now: number; className?: string }) =>
  at ? (
    <Tip content={absTime(at)}>
      <span className={cx('shrink-0 text-xs text-fg-3 tabular', className)}>{relTime(at, now)}</span>
    </Tip>
  ) : null

export function Loading() {
  return (
    <div className="p-6">
      <CardSkeleton rows={2} />
    </div>
  )
}

// Failed：读取失败。「不在这个分享里」这类 4xx 重试也没用，不给重试按钮。
export function Failed({ error, onRetry }: { error: unknown; onRetry?: () => void }) {
  const final = error instanceof ApiError && error.status < 500
  return (
    <div className="mx-auto max-w-md p-6 pt-16">
      <ErrorState error={error} onRetry={final ? undefined : onRetry} />
    </div>
  )
}

// ---------- 会话行 ----------

// LaneRow：一个会话，固定 64px 两行：身份 + 会话名 + 状态（带时长）+ 多久前有动静 / 在推进的需求 + 当前这一轮的一句话。
// 主调度点不进对话，只是一行。
export function LaneRow({ s, token, now }: { s: GuestSession; token: string; now: number }) {
  const st = displayState(s)
  // 在推进的需求：进行中、待验收、受阻的在前
  const req = [...(s.requirements ?? [])].sort((a, b) => Number(!active.includes(a.status)) - Number(!active.includes(b.status)))[0]
  const more = (s.requirements?.length ?? 0) - 1
  const body = (
    <>
      <span className="flex min-w-0 items-center gap-1.5 text-[13px]">
        <RoleTag role={s.role} />
        <b className="min-w-0 truncate font-semibold text-fg">{s.title ? `「${s.title}」` : t.guest.agent}</b>
        <StatusPill state={st} since={s.state_since} now={now} bg={s.background} />
        <Rel at={s.last_active_at} now={now} className="ml-auto" />
      </span>
      <span className="flex min-w-0 items-center gap-1.5 text-[12.5px] text-fg-2">
        {req && (
          <span className="inline-flex shrink-0 items-center gap-1 font-mono text-[11.5px] text-fg-3">
            <ReqGlyph status={req.status} />
            {req.code}
            {more > 0 && <span>+{more}</span>}
          </span>
        )}
        <span className="min-w-0 truncate">{s.line}</span>
      </span>
    </>
  )
  const cls = 'grid h-16 w-full grid-rows-[22px_20px] content-center gap-1 rounded-xl border border-line bg-surface px-3 text-left shadow-[0_1px_2px_rgb(70_50_18/0.05)]'
  if (!s.conversation) return <div className={cls}>{body}</div>
  return (
    <Link to={guestPath(token, 'session', s.key)} className={cx(cls, 'transition-colors hover:border-line-strong')}>
      {body}
    </Link>
  )
}

function Lanes({ list, token, now }: { list: GuestSession[]; token: string; now: number }) {
  const [showEnded, setShowEnded] = useState(false)
  const on = list.filter((s) => !ended(s))
  const off = list.filter(ended)
  if (!list.length) return <p className="text-[13px] text-fg-3">{t.guest.noSessions}</p>
  return (
    <div className="grid gap-1.5">
      {on.map((s) => (
        <LaneRow key={s.key} s={s} token={token} now={now} />
      ))}
      {off.length > 0 && (
        <button type="button" onClick={() => setShowEnded((v) => !v)} aria-expanded={showEnded} className="flex items-center gap-1 py-1 text-left text-[12.5px] text-fg-3 hover:text-fg-2">
          <ChevronRight className={cx('size-3.5 transition-transform', showEnded && 'rotate-90')} />
          {t.guest.endedN(off.length)}
        </button>
      )}
      {showEnded && off.map((s) => <LaneRow key={s.key} s={s} token={token} now={now} />)}
    </div>
  )
}

// ---------- 最近动态 ----------

function Subject({ a, token, sessions }: { a: ShareActivity; token: string; sessions: Map<string, GuestSession> }) {
  if (a.actor === 'user') return <b className="shrink-0 font-medium text-fg">{t.guest.owner}</b>
  if (a.actor === 'system') return <span className="shrink-0 font-medium text-fg-2">{t.guest.system}</span>
  const s = a.session ? sessions.get(a.session) : undefined
  const name = a.by || s?.title || t.guest.agent
  return (
    <span className="inline-flex min-w-0 items-center gap-1 align-[-3px]">
      {(a.role || s?.role) && <RoleTag role={a.role || s?.role || ''} />}
      {s?.conversation ? (
        <Link to={guestPath(token, 'session', s.key)} className="truncate font-medium text-fg hover:text-accent">
          「{name}」
        </Link>
      ) : (
        <span className="truncate font-medium text-fg">「{name}」</span>
      )}
    </span>
  )
}

function sentence(a: ShareActivity): string {
  const f = t.guest.act[a.kind]
  if (!f) return a.req
  if (a.kind === 'status') return f(a.req, t.reqStatus[a.to ?? ''] ?? a.to ?? '')
  if (a.kind === 'decision' || a.kind === 'answer') return f(a.dec || a.req, '')
  return f(a.req, '')
}

const clip = (s: string) => (s.length > 120 ? s.slice(0, 120) + '…' : s)

export function Feed({ list, token, sessions, fresh, now }: { list: ShareActivity[]; token: string; sessions: GuestSession[]; fresh?: Set<string>; now: number }) {
  const byKey = new Map(sessions.map((s) => [s.key, s]))
  if (!list.length)
    return (
      <div className="text-[13px] text-fg-3">
        <p>{t.guest.noActivity}</p>
        <p className="mt-0.5 text-xs">{t.guest.noActivityHint}</p>
      </div>
    )
  return (
    <ol className="ml-1.5 grid border-l border-line-strong">
      {list.map((a) => {
        const k = actKey(a)
        return (
          <li key={k} className={cx('guest-feed relative rounded-r-lg py-1 pr-1.5 pb-2.5 pl-4 text-[13px] leading-relaxed text-fg-2', fresh?.has(k) && 'guest-fresh')}>
            <span className="flex flex-wrap items-baseline gap-x-1.5 gap-y-0.5">
              <span className="min-w-0">
                <Subject a={a} token={token} sessions={byKey} /> <Linkify text={sentence(a)} label={a.kind === 'decision' && a.text ? 'code' : 'title'} />
                {a.text && (
                  <>
                    <span className="text-fg-3">：</span>
                    <span className="break-words">{clip(a.text)}</span>
                  </>
                )}
              </span>
              <Rel at={a.at} now={now} />
            </span>
          </li>
        )
      })}
    </ol>
  )
}

// ---------- 概览 ----------

export function OverviewView({ ov, token, reqs, fresh }: { ov: Overview; token: string; reqs: { code: string; title: string; status: string }[]; fresh: Set<string> }) {
  const now = useNow()
  const bar = statusBar(ov.counts)
  const total = bar.reduce((n, [, c]) => n + c, 0)
  const running = ov.sessions.filter((s) => ['running', 'background', 'waiting_approval', 'waiting_answer'].includes(displayState(s))).length
  return (
    <div>
      <header className="border-b border-line px-4 py-5 sm:px-6">
        <h1 className="font-display text-[22px] leading-snug tracking-[0.02em] text-balance text-fg">{ov.name}</h1>
        {total > 0 && (
          <>
            <div className="mt-3 flex h-1.5 max-w-[420px] gap-0.5 overflow-hidden rounded-full" aria-hidden>
              {bar.map(([k, n]) => (
                <span key={k} className={statusCls[k]?.dot ?? 'bg-line-strong'} style={{ flex: n }} />
              ))}
            </div>
            <p className="mt-1.5 text-xs text-fg-3">
              {t.guest.reqTotal(total)}：{bar.map(([k, n]) => `${t.reqStatus[k] ?? k} ${n}`).join(sep)}
            </p>
          </>
        )}
      </header>
      <div className="grid gap-6 px-4 py-5 sm:px-6 lg:grid-cols-[minmax(0,1.4fr)_minmax(0,1fr)]">
        <div className="grid min-w-0 content-start gap-6">
          <Section title={t.guest.sessions} extra={running ? t.guest.runningN(running) : undefined}>
            <Lanes list={ov.sessions} token={token} now={now} />
          </Section>
          <Section title={t.guest.reqs}>
            {reqs.length ? (
              <div className="grid gap-1.5">
                {reqs.map((r) => (
                  <Link key={r.code} to={guestPath(token, 'req', r.code)} className="flex h-10 min-w-0 items-center gap-2 rounded-[10px] border border-line bg-surface-2 px-3 text-[13.5px] transition-colors hover:border-line-strong">
                    <ReqGlyph status={r.status} className={undefined} />
                    <span className="shrink-0 font-mono text-[11.5px] text-fg-3">{r.code}</span>
                    <span className="min-w-0 flex-1 truncate text-fg">{r.title}</span>
                    <ReqStatus status={r.status} />
                  </Link>
                ))}
              </div>
            ) : (
              <p className="text-[13px] text-fg-3">{t.guest.noReqs}</p>
            )}
          </Section>
        </div>
        <Section title={t.guest.activity}>
          <Feed list={ov.activity} token={token} sessions={ov.sessions} fresh={fresh} now={now} />
        </Section>
      </div>
    </div>
  )
}

// ---------- 需求 ----------

function Card({ children, className, id }: { children: ReactNode; className?: string; id?: string }) {
  return (
    <div id={id} className={cx('scroll-mt-4 rounded-[14px] border border-line bg-surface px-4 py-3.5 shadow-[0_1px_2px_rgb(70_50_18/0.05)]', className)}>
      {children}
    </div>
  )
}

// Byline：一条记录是谁做的：会话写身份 + 会话名，所有者写「分享者」。
function Byline({ role, title, user, children, at, now, right }: { role?: string; title?: string; user?: boolean; children?: ReactNode; at?: string; now: number; right?: ReactNode }) {
  return (
    <div className="mb-2 flex flex-wrap items-center gap-x-2 gap-y-1 text-xs text-fg-3">
      {user ? (
        <b className="font-medium text-fg">{t.guest.owner}</b>
      ) : (
        <span className="inline-flex min-w-0 items-center gap-1">
          {role && <RoleTag role={role} />}
          <span className="max-w-[16em] truncate font-medium text-fg-2">{title ? `「${title}」` : t.guest.agent}</span>
        </span>
      )}
      {children}
      <Rel at={at} now={now} />
      {right && <span className="ml-auto">{right}</span>}
    </div>
  )
}

// evidenceLine：证据的一句话（结构化证据的结论，老格式取第一行），留言对象的标签用
function evidenceLine(e: Evidence) {
  const s = (e.payload?.verdict || e.summary || '').trim().split('\n')[0] ?? ''
  return s.length > 30 ? `${s.slice(0, 30)}…` : s
}

function EvidenceItem({ e, r, review, now }: { e: Evidence; r: Requirement; review?: boolean; now: number }) {
  return (
    <Card className="@container" id={`ev-${e.id}`}>
      <Byline role={e.role} title={e.session_title} at={e.created_at} now={now} right={<CommentSlot target={{ kind: 'evidence', ref: e.id, req: r.code, label: t.guest.onEvidence(evidenceLine(e)) }} />}>
        <span>{review ? t.guest.reviewed : t.guest.submitted}</span>
      </Byline>
      <EvidenceBody e={e} r={r} />
    </Card>
  )
}

function DecisionItem({ d, now }: { d: Decision; now: number }) {
  const answered = !!d.answer && d.status !== 'pending'
  return (
    <Card id={d.code}>
      <Byline role={d.from_role || undefined} title={d.by_title} at={d.created_at} now={now} right={<CommentSlot target={{ kind: 'dec', ref: d.code, req: d.requirement_code, label: d.code }} />}>
        <span className="inline-flex items-center gap-1 font-mono text-[11px] font-semibold text-fg-2">
          <DecGlyph open={d.status === 'pending'} />
          {d.code}
        </span>
        <Badge tone={d.status === 'pending' ? 'reply' : 'neutral'}>{t.decision.status[d.status] ?? d.status}</Badge>
      </Byline>
      <Md text={d.question} className="text-[14px] font-semibold" />
      {d.context && d.context !== 'aide' && <Md text={d.context} className="mt-1 text-[13px] text-fg-2" />}
      {!!d.options?.length && (
        <ol className="mt-2 grid gap-1 text-[13px] text-fg-2">
          {d.options.map((o, i) => (
            <li key={i} className="flex gap-2">
              <span className="shrink-0 text-fg-3 tabular">{i + 1}.</span>
              <span className="min-w-0 break-words">
                <Linkify text={o} />
              </span>
            </li>
          ))}
        </ol>
      )}
      {!!d.blocks?.length && <BlockList blocks={d.blocks} className="mt-2" />}
      {!!d.context_files?.length && (
        <div className="mt-2">
          <Thumbs files={d.context_files} size="sm" />
        </div>
      )}
      {answered && (
        <div className="mt-2.5 rounded-[10px] bg-surface-2 px-3 py-2 text-[13.5px] leading-relaxed">
          <div className="mb-0.5 flex items-center gap-2 text-xs text-fg-3">
            {d.proxy_by ? <RoleTag role="aide" /> : <b className="font-medium text-fg">{t.guest.owner}</b>}
            {d.proxy_by ? t.guest.proxy : t.guest.answer}
            <Rel at={d.answered_at} now={now} />
          </div>
          <span className="break-words whitespace-pre-wrap">
            <Linkify text={d.answer} />
          </span>
          {!!d.answer_files?.length && (
            <div className="mt-1.5">
              <Thumbs files={d.answer_files} size="sm" />
            </div>
          )}
        </div>
      )}
    </Card>
  )
}

// 留言：会话的留言（comment）、分享者的留言（note）和退回修改（feedback）
const noteKinds = ['comment', 'note', 'feedback']
function NoteItem({ f, now }: { f: Decision; now: number }) {
  const user = f.kind !== 'comment' && !f.assigned_by
  const g = f.guest
  if (g)
    // 分享者放入的访客留言（R-281）：写明来自谁、经分享链接、由分享者放入；改写前的原话不给访客看
    return (
      <Card id={f.code}>
        <div className="mb-2 flex flex-wrap items-center gap-x-2 gap-y-1 text-xs text-fg-3">
          <b className="font-medium text-fg">{t.guest.from(g.name)}</b>
          {g.target_kind === 'dec' && g.target_code && <RefChip code={g.target_code} />}
          {g.target_kind === 'evidence' && <span>{t.guest.onEvidence(g.target_title ?? '')}</span>}
          {g.target_kind === 'stage' && <span>{t.guest.onStage(g.target_code ?? '', g.target_seq ?? 0)}</span>}
          <Rel at={f.answered_at || f.created_at} now={now} />
        </div>
        <p className="text-[13.5px] break-words whitespace-pre-wrap">
          <Linkify text={f.answer} />
        </p>
        {g.addend && (
          <p className="mt-1.5 border-t border-dashed border-line pt-1.5 text-[13px] break-words whitespace-pre-wrap text-fg-2">
            <b className="mr-1 font-semibold">{t.guest.ownerAddend}：</b>
            {g.addend}
          </p>
        )}
      </Card>
    )
  return (
    <Card id={f.code} className={f.kind === 'feedback' ? 'border-reply/30' : undefined}>
      <Byline user={user} role={f.from_role || undefined} title={f.by_title} at={f.answered_at || f.created_at} now={now}>
        <span className={f.kind === 'feedback' ? 'font-semibold text-reply' : undefined}>{f.kind === 'feedback' ? t.guest.revised : t.guest.noted}</span>
      </Byline>
      {f.answer && <Md text={f.answer} className="text-[13.5px]" />}
      {!!f.blocks?.length && <BlockList blocks={f.blocks} className="mt-1.5" />}
      {!!f.answer_files?.length && (
        <div className="mt-1.5">
          <Thumbs files={f.answer_files} size="sm" />
        </div>
      )}
    </Card>
  )
}

export function ReqView({ token, code, ov }: { token: string; code: string; ov: Overview }) {
  const q = useGuestReq(token, code)
  const mine = useMyComments(token)
  const now = useNow()
  useScrollToHash(!!q.data)
  if (q.isPending) return <Loading />
  if (q.isError) return <Failed error={q.error} onRetry={() => q.refetch()} />
  const { requirement: r, decisions, sessions, activity } = q.data
  const evidence = (r.evidence ?? []).filter((e) => !e.voided).reverse()
  const voided = (r.evidence ?? []).filter((e) => e.voided).length
  const reviews = [...(r.reviews ?? [])].reverse()
  const notes = (r.feedback ?? []).filter((f) => noteKinds.includes(f.kind))
  const myNotes = (mine.data ?? []).filter((x) => x.req === r.code)
  return (
    <div className="flex min-h-full flex-col">
    <div className="mx-auto w-full max-w-3xl px-4 py-5 sm:px-6">
      <header className="mb-5">
        <h1 className="font-display text-[20px] leading-snug tracking-[0.02em] text-balance text-fg">{r.title}</h1>
        <div className="mt-2 flex flex-wrap items-center gap-2 text-xs text-fg-3">
          <ReqStatus status={r.status} />
          <span className="font-mono">{r.code}</span>
          {r.plan_code && <RefChip code={r.plan_code} label="title" suffix={r.stage_seq ? t.guest.stage(r.stage_seq) : undefined} />}
          <span>{t.guest.updated(relTime(r.updated_at, now))}</span>
          <span className="ml-auto">
            <CommentSlot target={{ kind: 'req', ref: r.code, label: r.code }} />
          </span>
        </div>
        {r.status === 'blocked' && (r.blocked_on || r.blocked_reason) && (
          <p className="mt-2.5 rounded-lg bg-wait-soft px-3 py-1.5 text-[13px] text-wait">
            {t.guest.blocked}：{r.blocked_on && <Linkify text={t.guest.blockedOn(r.blocked_on)} />}
            {r.blocked_on && r.blocked_reason && sep}
            {r.blocked_reason}
          </p>
        )}
      </header>
      <div className="grid gap-6">
        {sessions.length > 0 && (
          <Section title={t.guest.sessions}>
            <div className="grid gap-1.5">
              {sessions.map((s) => (
                <LaneRow key={s.key} s={s} token={token} now={now} />
              ))}
            </div>
          </Section>
        )}
        {(r.detail || !!r.files?.length) && (
          <Section title={t.guest.detail}>
            <div className="rounded-xl bg-surface-2 px-4 py-3">
              {r.detail && <Md text={r.detail} className="text-[14px]" />}
              {!!r.files?.length && (
                <div className="mt-2">
                  <Thumbs files={r.files} />
                </div>
              )}
            </div>
          </Section>
        )}
        {r.acceptance && (
          <Section title={t.guest.acceptance}>
            <Md text={r.acceptance} className="text-[14px]" />
          </Section>
        )}
        {evidence.length > 0 && (
          <Section title={t.guest.evidence} extra={voided ? t.guest.voided(voided) : undefined}>
            <div className="grid gap-3">
              {evidence.map((e) => (
                <EvidenceItem key={e.id} e={e} r={r} now={now} />
              ))}
            </div>
          </Section>
        )}
        {reviews.length > 0 && (
          <Section title={t.guest.reviews}>
            <div className="grid gap-3">
              {reviews.map((e) => (
                <EvidenceItem key={e.id} e={e} r={r} review now={now} />
              ))}
            </div>
          </Section>
        )}
        {decisions.length > 0 && (
          <Section title={t.guest.decisions}>
            <div className="grid gap-3">
              {decisions.map((d) => (
                <DecisionItem key={d.id} d={d} now={now} />
              ))}
            </div>
          </Section>
        )}
        {notes.length > 0 && (
          <Section title={t.guest.comments}>
            <div className="grid gap-3">
              {notes.map((f) => (
                <NoteItem key={f.id} f={f} now={now} />
              ))}
            </div>
          </Section>
        )}
        <MyComments items={myNotes} />
        <Section title={t.guest.progress}>{activity.length ? <Feed list={activity} token={token} sessions={ov.sessions} now={now} /> : <p className="text-[13px] text-fg-3">{t.guest.noProgress}</p>}</Section>
      </div>
    </div>
    <Compose fallback={{ kind: 'req', ref: r.code, label: r.code }} />
    </div>
  )
}

// 从印记点开决策时地址带 #D-x：数据到了再滚过去
function useScrollToHash(ready: boolean) {
  useEffect(() => {
    if (!ready || !location.hash) return
    const el = document.getElementById(decodeURIComponent(location.hash.slice(1)))
    el?.scrollIntoView({ block: 'start' })
  }, [ready])
}

// ---------- 计划 ----------

export function PlanView({ token, code }: { token: string; code: string }) {
  const q = useGuestPlan(token, code)
  const mine = useMyComments(token)
  const now = useNow()
  if (q.isPending) return <Loading />
  if (q.isError) return <Failed error={q.error} onRetry={() => q.refetch()} />
  const p = q.data
  const myNotes = (mine.data ?? []).filter((x) => x.target_kind === 'stage' && x.target_code === p.code)
  return (
    <div className="flex min-h-full flex-col">
    <div className="mx-auto w-full max-w-3xl px-4 py-5 sm:px-6">
      <header className="mb-5">
        <h1 className="flex items-center gap-2 font-display text-[20px] leading-snug tracking-[0.02em] text-balance text-fg">
          <PlanGlyph status={p.status} className="size-4" />
          {p.title || p.code}
        </h1>
        <div className="mt-2 flex flex-wrap items-center gap-2 text-xs text-fg-3">
          <span className="font-mono">{p.code}</span>
          <span>{t.guest.updated(relTime(p.updated_at, now))}</span>
        </div>
      </header>
      <div className="grid gap-6">
        {p.goal && (
          <Section title={t.guest.goal}>
            <Md text={p.goal} className="text-[14px]" />
          </Section>
        )}
        {(p.stages ?? []).map((s) => (
          <Section
            key={s.id}
            title={
              <>
                {t.guest.stage(s.seq)}
                <span className="font-normal text-fg-2">{s.title}</span>
                <CommentSlot target={{ kind: 'stage', ref: s.id, label: t.guest.onStage(p.code, s.seq) }} className="order-last ml-auto" />
              </>
            }
            extra={t.plan.stageState[s.status] ?? s.status}
          >
            <div className="grid gap-1.5">
              {(s.requirements ?? []).map((r) =>
                r.id ? (
                  <Link key={r.code} to={guestPath(token, 'req', r.code)} className="flex h-10 min-w-0 items-center gap-2 rounded-[10px] border border-line bg-surface-2 px-3 text-[13.5px] transition-colors hover:border-line-strong">
                    <ReqGlyph status={r.status} />
                    <span className="shrink-0 font-mono text-[11.5px] text-fg-3">{r.code}</span>
                    <span className="min-w-0 flex-1 truncate text-fg">{r.title}</span>
                    <ReqStatus status={r.status} />
                  </Link>
                ) : (
                  <div key={r.code} className="flex h-10 items-center px-3">
                    <Linkify text={r.code} />
                  </div>
                ),
              )}
            </div>
            {(s.summary || !!s.summary_blocks?.length) && (
              <Card className="mt-2.5">
                <div className="mb-1 flex items-center gap-2 text-xs text-fg-3">
                  {t.guest.stageSummary}
                  <Rel at={s.summary_at} now={now} />
                </div>
                {s.summary && <Md text={s.summary} className="text-[13.5px]" />}
                {!!s.summary_blocks?.length && <BlockList blocks={s.summary_blocks} className="mt-1.5" />}
              </Card>
            )}
          </Section>
        ))}
        <MyComments items={myNotes} />
      </div>
    </div>
    <Compose />
    </div>
  )
}

// ---------- 会话 ----------

// toolName：MCP 工具只留工具名（mcp__服务__工具 → 工具）
const toolName = (n: string) => (n.startsWith('mcp__') ? n.split('__').slice(2).join('__') || n : n)

// Tools：工具调用的几行摘要：最近 3 次（工具名 + 一句说明），其余收起；改过的文件另起一行，展开看文件名。
// 纸鸢没有记录工具的输出，这里只有名字和说明。正在运行的一轮，最后一次调用标「运行中」。
function Tools({ x }: { x: GuestTurn }) {
  const [all, setAll] = useState(false)
  const [files, setFiles] = useState(false)
  const list = x.tools ?? []
  const fl = x.files ?? []
  if (!list.length && !fl.length) return null
  const shown = all ? list : list.slice(-3)
  return (
    <div className="my-1.5 grid gap-px">
      {list.length > shown.length && (
        <button type="button" onClick={() => setAll(true)} className="flex h-7 items-center gap-2 rounded-md px-2 text-left text-[12.5px] text-fg-3 hover:bg-hover hover:text-fg-2">
          <ChevronRight className="size-3.5" />
          {t.guest.toolsAll(x.tool_count || list.length)}
        </button>
      )}
      {shown.map((c, i) => {
        const last = x.running && i === shown.length - 1
        return (
          <div key={`${c.at}-${i}`} className="flex min-h-7 min-w-0 items-center gap-2 rounded-md px-2 text-[12.5px] text-fg-2">
            <span className="max-w-[10em] min-w-[3.5em] shrink-0 truncate font-mono text-[11.5px] text-fg-3">{toolName(c.name)}</span>
            <span className="min-w-0 truncate">{c.brief || t.turns.noBrief}</span>
            {c.sub && <span className="shrink-0 text-[11.5px] text-fg-3">{t.guest.sub}</span>}
            {last && (
              <span className="ml-auto inline-flex shrink-0 items-center gap-1.5 rounded-full bg-run-soft px-2 py-0.5 text-[11.5px] font-medium text-run">
                <span className="size-1.5 animate-pulse rounded-full bg-run" />
                {t.guest.toolRunning}
              </span>
            )}
          </div>
        )
      })}
      {all && list.length > 3 && (
        <button type="button" onClick={() => setAll(false)} className="h-6 rounded-md px-2 text-left text-[12px] text-fg-3 hover:text-fg-2">
          {t.guest.toolsFold}
        </button>
      )}
      {fl.length > 0 && (
        <div>
          <button type="button" onClick={() => setFiles((v) => !v)} aria-expanded={files} className="flex h-7 w-full items-center gap-2 rounded-md px-2 text-left text-[12.5px] text-fg-3 hover:bg-hover hover:text-fg-2">
            <ChevronRight className={cx('size-3.5 transition-transform', files && 'rotate-90')} />
            {t.guest.files(fl.length)}
          </button>
          {files && (
            <ul className="mt-0.5 ml-[30px] grid gap-0.5 rounded-lg bg-sunken/60 px-2.5 py-1.5 font-mono text-[11.5px] break-all text-fg-2">
              {fl.map((f) => (
                <li key={f}>{f}</li>
              ))}
            </ul>
          )}
        </div>
      )}
    </div>
  )
}

const oneLine = (s: string) => {
  const line = s.trim().split('\n')[0]
  return line.length > 200 ? line.slice(0, 200) + '…' : line
}

function TurnBlock({ x, name, now }: { x: GuestTurn; name: string; now: number }) {
  const inbox = x.inbox ?? []
  const web = inbox.filter((i) => i.web)
  const other = inbox.filter((i) => !i.web)
  const items = x.items ?? []
  const prompt = x.source === 'terminal' ? x.prompt : x.source === 'web' ? x.prompt || web.map((i) => i.text).join('\n\n') : ''
  if (!x.source && !inbox.length && !items.length && !(x.tools ?? []).length && !x.reply) return null
  return (
    <section data-turn={x.id} className={cx('border-b border-line/70 py-3.5 last:border-b-0', x.running && 'border-l-2 border-l-run pl-3')}>
      {x.running && (
        <p className="mb-1.5 inline-flex items-center gap-1.5 text-[12px] font-medium text-run">
          <Radio className="size-3.5" />
          {t.guest.current}
        </p>
      )}
      {prompt ? (
        <div className="mb-1.5">
          <div className="mb-1 flex items-center gap-1.5 text-[12px] text-fg-3">
            <b className="font-medium text-fg-2">{t.guest.owner}</b>
            <span>{x.source === 'web' ? t.turns.fromWeb : t.turns.fromTerminal}</span>
            <Tip content={absTime(x.start)}>
              <span className="tabular">{hm(x.start)}</span>
            </Tip>
          </div>
          <div className="rounded-xl bg-surface-2 px-3 py-2 text-[14px] leading-relaxed break-words whitespace-pre-wrap text-fg">
            <Linkify text={prompt} label="title" />
          </div>
        </div>
      ) : x.source === 'wake' || other.length ? (
        <div className="mb-1.5">
          <div className="mb-0.5 flex items-center gap-1.5 text-[12px] text-fg-3">
            <span className="font-medium text-fg-2">{x.source === 'wake' ? t.turns.woken : t.turns.sentIn}</span>
            <span className="tabular">{hm(x.start)}</span>
          </div>
          {other.map((o, i) => (
            <InboxLine key={o.id || i} x={o} />
          ))}
        </div>
      ) : null}
      {items.length > 0 && (
        <div className="my-1">
          {items.map((it, i) => (
            <ItemNode key={it.id || i} x={it as unknown as TurnItem} now={now} />
          ))}
        </div>
      )}
      <Tools x={x} />
      {x.reply ? (
        <div className="mt-1.5">
          <div className="mb-1 flex items-center gap-1.5 text-[12px] text-fg-3">
            <span className="font-medium text-fg-2">{name}</span>
            {x.end && <span className="tabular">{hm(x.end)}</span>}
          </div>
          <Md text={x.reply} className="text-[14px]" />
        </div>
      ) : x.running ? (
        <p className="mt-1 flex items-center gap-1.5 text-[12.5px] text-run">
          <span className="size-1.5 animate-pulse rounded-full bg-run" />
          {t.turns.running}
        </p>
      ) : x.end ? (
        <p className="mt-1 text-[12.5px] text-fg-3">{t.turns.noReply}</p>
      ) : null}
    </section>
  )
}

function InboxLine({ x }: { x: GuestItem }) {
  const role = x.peer_role && x.peer_role !== 'user' && t.role[x.peer_role] ? x.peer_role : ''
  const kind = x.label || t.turns.inbox[x.via ?? ''] || ''
  return (
    <div className="text-[12.5px] leading-relaxed break-words text-fg-2">
      {role && (
        <span className="mr-1 inline-flex align-[-3px]">
          <RoleTag role={role} />
        </span>
      )}
      {kind && <span className="text-fg-3">{kind}</span>}
      {(x.dec || x.req) && <> <Linkify text={x.dec || x.req || ''} label="title" /></>}
      {x.text && (
        <>
          <span className="text-fg-3">：</span>
          <Linkify text={oneLine(x.text)} label="title" />
        </>
      )}
    </div>
  )
}

export function SessionView({ token, sessionKey, ov }: { token: string; sessionKey: string; ov: Overview }) {
  const q = useGuestSession(token, sessionKey)
  const now = useNow()
  const end = useRef<HTMLDivElement>(null)
  const first = useRef(true)
  const pages = q.data?.pages ?? []
  const turns = pages
    .slice()
    .reverse()
    .flatMap((p) => p.turns ?? [])
  // 第一次打开停在最新一轮（底部）
  useLayoutEffect(() => {
    if (!first.current || !turns.length) return
    first.current = false
    end.current?.scrollIntoView({ block: 'end' })
  }, [turns.length])
  if (q.isPending) return <Loading />
  if (q.isError) return <Failed error={q.error} onRetry={() => q.refetch()} />
  const s = pages[0].session
  const live = ov.sessions.find((x) => x.key === s.key) ?? s
  const name = s.title || t.guest.agent
  return (
    <div className="mx-auto max-w-3xl px-4 py-5 sm:px-6">
      <header className="mb-3 border-b border-line pb-4">
        <h1 className="flex min-w-0 items-center gap-2 font-display text-[20px] leading-snug tracking-[0.02em] text-fg">
          <RoleTag role={s.role} className="font-sans" />
          <span className="min-w-0 truncate">{name}</span>
        </h1>
        <div className="mt-2 flex flex-wrap items-center gap-2 text-xs text-fg-3">
          <StatusPill state={displayState(live)} since={live.state_since} now={now} bg={live.background} />
          {(live.requirements ?? []).map((r) => (
            <RefChip key={r.code} code={r.code} label="title" />
          ))}
          <span>{t.guest.lastActive(relTime(live.last_active_at, now))}</span>
        </div>
      </header>
      {!s.conversation ? (
        <div className="grid gap-2 py-2">
          {live.line && (
            <Card>
              <div className="mb-1 text-xs text-fg-3">{t.guest.current}</div>
              <p className="text-[14px] break-words text-fg">
                <Linkify text={live.line} label="title" />
              </p>
            </Card>
          )}
          <p className="text-[13px] text-fg-3">{t.guest.noTalk(t.role[s.role] ?? '')}</p>
        </div>
      ) : (
        <>
          {q.hasNextPage ? (
            <button type="button" disabled={q.isFetchingNextPage} onClick={() => q.fetchNextPage()} className="mb-1 w-full rounded-lg py-1.5 text-[12.5px] text-accent hover:bg-hover disabled:text-fg-3">
              {q.isFetchingNextPage ? t.turns.loading : t.turns.loadOlder}
            </button>
          ) : (
            turns.length > 0 && <p className="mb-1 text-center text-[12px] text-fg-3">{t.turns.start}</p>
          )}
          {turns.length ? turns.map((x) => <TurnBlock key={x.id} x={x} name={name} now={now} />) : <p className="py-6 text-center text-[13px] text-fg-3">{t.turns.noTurns}</p>}
          <div ref={end} />
        </>
      )}
    </div>
  )
}

export type { Overview }
