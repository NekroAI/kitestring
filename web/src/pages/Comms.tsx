import { useEffect, useMemo, useState } from 'react'
import { useSearchParams } from 'react-router'
import { ArrowRight, BellOff, ChevronDown, ChevronRight, Radio, Search, X } from 'lucide-react'
import { useComms, useProjects } from '../api/queries'
import type { Schemas } from '../api/client'
import { t } from '../i18n'
import { matches, rangeStart, dayStart, type CommsFilter, type CommsKind, type CommsRange } from '../lib/comms'
import { cx, duration, shortDuration, useNow, usePref } from '../lib/util'
import { Button, Card, ErrorState, Input, PageHeader, Segmented, Skeleton, Tip } from '../ui/base'
import { Menu, type MenuItem } from '../ui/overlay'
import { ToolMark } from '../ui/marks'
import { RefChip } from '../ui/refmark'

// 通讯页（R-249，D-1063 方案 B）：左栏在线会话，右栏按时间列会话之间的消息和广播、送达状态。只看，不发。
// 纸鸢自动发的提醒默认收起，开关上写条数；打开后按时间插回原位、灰显。
// 还在等对方醒来的消息置顶（导航上的黄色数字也是它）；发给已结束会话的永远送不到，单独折叠（D-1071）。

type Msg = Schemas['CommsMsg']
type Sess = Schemas['CommsSession']
type Rcpt = Schemas['Delivery']

const statusDot: Record<string, string> = { standby: 'bg-run', busy: 'bg-accent', online: 'bg-wait', offline: 'bg-fg-3/50' }
const statusPill: Record<string, string> = { standby: 'bg-accent-soft text-accent', busy: 'bg-accent-soft text-accent', online: 'bg-wait-soft text-wait', offline: 'bg-sunken text-fg-3' }
const deliverTone: Record<string, string> = { delivered: 'text-run', pending_wake: 'text-wait', offline: 'text-fg-3' }
const deliverDot: Record<string, string> = { delivered: 'bg-run', pending_wake: 'border border-wait', offline: 'bg-fg-3/50' }
const roleList = ['pm', 'aide', 'advisor', 'reviewer', 'dev']

const ts = (iso?: string | null) => (iso ? Date.parse(iso) : 0)
const isToday = (iso: string, now: number) => new Date(iso).toDateString() === new Date(now).toDateString()
const hm = (iso: string) => new Date(iso).toLocaleTimeString(undefined, { hour: '2-digit', minute: '2-digit', hour12: false })
const md = (iso: string) => new Date(iso).toLocaleDateString(undefined, { month: '2-digit', day: '2-digit' })
const when = (iso: string, now: number) => (isToday(iso, now) ? hm(iso) : md(iso))
const roleName = (role?: string) => (role ? t.role[role] : '') || ''
const short = (s: string, n: number) => (s.length > n ? s.slice(0, n) + '…' : s)

// 一方：身份胶囊（没身份时写会话标题），前面是会话现在的状态点；点它把右栏过滤到这个会话。
function PeerChip({ k, role, title, tool, sessions, onPick }: { k: string; role?: string; title?: string; tool?: string; sessions: Map<string, Sess>; onPick: (k: string) => void }) {
  const s = sessions.get(k)
  const label = roleName(role) || short(title || s?.title || t.comms.untitled(tool || s?.harness || ''), 10)
  const tip = [roleName(role) || t.comms.noRole, title || s?.title, tool || s?.harness, s ? s.status_label : t.comms.gone].filter(Boolean).join(t.common.sep)
  return (
    <Tip content={tip}>
      <button
        type="button"
        onClick={(e) => {
          e.stopPropagation()
          onPick(k)
        }}
        className={cx('inline-flex h-[20px] max-w-[10rem] shrink-0 items-center gap-1 rounded-full border border-line-strong px-2 text-[11px] font-medium whitespace-nowrap hover:border-accent/60', s ? 'text-fg-2' : 'text-fg-3')}
      >
        <span className={cx('size-1.5 shrink-0 rounded-full', statusDot[s?.status ?? 'offline'])} />
        <span className="truncate">{label}</span>
      </button>
    </Tip>
  )
}

function Delivered({ m, now }: { m: Msg; now: number }) {
  const rs = m.recipients ?? []
  if (m.broadcast) {
    const n = (st: string) => rs.filter((r) => r.status === st).length
    return (
      <Tip content={rs.map((r) => `${roleName(r.role) || r.title || r.to}：${r.status_label}${r.reason && r.status !== 'delivered' ? `（${r.reason}）` : ''}`).join('\n')}>
        <span className="flex items-center gap-1.5 text-[11.5px] whitespace-nowrap text-fg-3">
          <span className="flex items-center gap-0.5">
            {rs.map((r, i) => (
              <span key={i} className={cx('size-1.5 rounded-full', deliverDot[r.status])} />
            ))}
          </span>
          {t.comms.castSum(n('delivered'), n('pending_wake'), n('offline'))}
        </span>
      </Tip>
    )
  }
  const r = rs[0] as Rcpt | undefined
  if (!r) return null
  let extra = ''
  if (r.status === 'delivered' && r.delivered_at) {
    const late = ts(r.delivered_at) - ts(m.created_at)
    if (late >= 2 * 60_000) extra = t.comms.late(shortDuration(m.created_at, ts(r.delivered_at)))
  } else if (r.status === 'pending_wake') extra = t.comms.waited(shortDuration(m.created_at, now))
  return (
    <Tip content={r.reason}>
      <span className={cx('flex items-center gap-1.5 text-[11.5px] whitespace-nowrap', deliverTone[r.status])}>
        <span className={cx('size-1.5 rounded-full', deliverDot[r.status])} />
        {r.status_label}
        {extra && <span className="text-fg-3">· {extra}</span>}
      </span>
    </Tip>
  )
}

// 一条消息：固定两行高（原则第 3 条）。第一行谁 → 谁、要动手 / 只告知、回复哪条、关联需求、送达状态；第二行正文，截断时悬停看全文。
function Row({ m, now, sessions, onPick, muted }: { m: Msg; now: number; sessions: Map<string, Sess>; onPick: (k: string) => void; muted?: boolean }) {
  const rs = m.recipients ?? []
  const dim = muted || m.system
  return (
    <div className={cx('flex h-[58px] flex-col justify-center gap-1 border-b border-line/70 px-4 last:border-b-0', dim && 'opacity-60')}>
      <div className="flex min-w-0 items-center gap-1.5 text-[12px]">
        <span className="w-11 shrink-0 text-fg-3 tabular">{when(m.created_at, now)}</span>
        {m.system ? (
          <span className="inline-flex h-[20px] shrink-0 items-center rounded-full border border-dashed border-line-strong px-2 text-[11px] whitespace-nowrap text-fg-3">
            {t.comms.system}
            {m.system_label && ` · ${m.system_label}`}
          </span>
        ) : (
          m.from && (
            <>
              <PeerChip k={m.from.key} role={m.from.role} title={m.from.title} tool={m.from.tool} sessions={sessions} onPick={onPick} />
              {m.from.agent && (
                <Tip content={`${t.comms.sub} ${m.from.agent_name || m.from.agent}`} truncated>
                  <span className="max-w-[8rem] shrink truncate text-[11.5px] text-fg-3">
                    {t.comms.sub} {m.from.agent_name || m.from.agent}
                  </span>
                </Tip>
              )}
            </>
          )
        )}
        <ArrowRight className="size-3 shrink-0 text-fg-3" />
        {m.broadcast ? (
          <span className="inline-flex h-[20px] shrink-0 items-center gap-1 rounded-full border border-line-strong px-2 text-[11px] font-medium whitespace-nowrap text-fg-2">
            <Radio className="size-3" />
            {t.comms.broadcast(rs.length)}
          </span>
        ) : (
          rs[0] && <PeerChip k={rs[0].to} role={rs[0].role} title={rs[0].title} tool={rs[0].tool} sessions={sessions} onPick={onPick} />
        )}
        {!m.system && (
          <span className={cx('shrink-0 rounded px-1.5 text-[11px] leading-[18px] whitespace-nowrap', m.fyi ? 'border border-dashed border-line-strong text-fg-3' : 'bg-accent-soft text-accent')}>
            {m.fyi ? t.comms.kind.fyi : t.comms.kind.action}
          </span>
        )}
        {m.reply_to && (
          <span className="shrink-0 text-[11.5px] whitespace-nowrap text-fg-3">
            {t.comms.replyTo} <span className="font-mono">{m.reply_to}</span>
          </span>
        )}
        {m.requirement && (
          <span className="min-w-0 shrink">
            <RefChip code={m.requirement} title={m.requirement_title} label="title" />
          </span>
        )}
        <span className="ml-auto flex shrink-0 items-center gap-2 pl-2">
          <Delivered m={m} now={now} />
          <span className="font-mono text-[11px] text-fg-3">{m.code}</span>
        </span>
      </div>
      <Tip content={<span className="whitespace-pre-wrap">{m.text}</span>} truncated>
        <div className="truncate pl-[3.125rem] text-[13.5px] text-fg">{m.text}</div>
      </Tip>
    </div>
  )
}

function SessionItem({ s, now, active, onClick }: { s: Sess; now: number; active: boolean; onClick: () => void }) {
  const open = s.open ?? []
  return (
    <button
      type="button"
      onClick={onClick}
      aria-pressed={active}
      className={cx('block h-[118px] w-full rounded-xl border bg-surface px-3.5 py-2.5 text-left transition-colors', active ? 'border-accent ring-1 ring-accent/40' : 'border-line hover:border-line-strong')}
    >
      <div className="flex min-w-0 items-center gap-2">
        <ToolMark tool={s.tool} size="sm" />
        <Tip content={s.title} truncated>
          <span className="min-w-0 truncate text-[14.5px] font-semibold text-fg">{s.title || t.comms.untitled(s.harness)}</span>
        </Tip>
        <span className={cx('inline-flex shrink-0 items-center rounded-full border border-line-strong px-2 text-[11px] leading-[18px] whitespace-nowrap', s.role ? 'text-fg-2' : 'text-fg-3')}>{s.role_name}</span>
      </div>
      <div className="mt-1.5 flex min-w-0 items-center gap-2 text-[12.5px] text-fg-2">
        <span className={cx('inline-flex shrink-0 items-center gap-1 rounded-full px-2 leading-[20px]', statusPill[s.status])}>
          <span className={cx('size-1.5 rounded-full', statusDot[s.status])} />
          {s.status_label} {shortDuration(s.last_active_at, now)}
        </span>
        <span className="truncate text-fg-3">
          {s.harness}
          {s.device && ` · ${s.device}`}
        </span>
      </div>
      <Tip content={s.wake_why} truncated>
        <div className={cx('mt-1 truncate text-[12px]', s.waiting ? 'text-wait' : 'text-fg-3')}>
          {s.waiting && s.waiting_since ? t.comms.waitingOn(s.waiting, duration(s.waiting_since, now)) : s.wake_now ? `${t.comms.wakeNow} · ${s.wake_why}` : s.wake_why}
        </div>
      </Tip>
      <div className="mt-1 flex min-w-0 items-center gap-2 text-[12px] text-fg-3">
        <span className="flex min-w-0 flex-1 items-center gap-1 overflow-hidden">
          {open.length === 0 ? t.comms.noReq : open.slice(0, 2).map((r) => <RefChip key={r.code} code={r.code} title={r.title} status={r.status} label="title" />)}
        </span>
        <span className="shrink-0 tabular">{t.comms.inOut(s.in, s.out)}</span>
      </div>
    </button>
  )
}

export default function CommsPage() {
  const now = useNow()
  const projects = useProjects()
  const projList = useMemo(() => (projects.data?.projects ?? []).filter((p) => !p.hidden), [projects.data])
  const [params, setParams] = useSearchParams()
  const [projectPref, setProject] = usePref<string>('ks.comms.project', '')
  const project = params.get('project') || projectPref || projList[0]?.id || ''
  const session = params.get('session') || ''
  const [range, setRange] = usePref<CommsRange>('ks.comms.range', 'today')
  const [ended, setEnded] = useState(false)
  const [kind, setKind] = useState<CommsKind>('all')
  const [role, setRole] = useState('')
  const [req, setReq] = useState('')
  const [onlyUnsent, setOnlyUnsent] = useState(false)
  const [showSys, setShowSys] = usePref('ks.comms.sys', false)
  const [q, setQ] = useState('')
  const [offOpen, setOffOpen] = useState(false)

  const since = rangeStart(range, now).toISOString()
  const day = dayStart(now).toISOString()
  const comms = useComms(project, since, day, ended)
  const d = comms.data

  const setParam = (k: string, v: string) =>
    setParams((p) => {
      const n = new URLSearchParams(p)
      if (v) n.set(k, v)
      else n.delete(k)
      return n
    })
  const pickProject = (id: string) => {
    setProject(id)
    setParams((p) => {
      const n = new URLSearchParams(p)
      n.delete('project')
      n.delete('session')
      return n
    })
    setReq('')
  }
  const pickSession = (k: string) => setParam('session', k === session ? '' : k)
  // 从会话面板进来时带着 project：记住它，之后刷新也停在这个项目
  const fromLink = params.get('project')
  useEffect(() => {
    if (fromLink && fromLink !== projectPref) setProject(fromLink)
  }, [fromLink, projectPref, setProject])

  const sessions = useMemo(() => new Map((d?.sessions ?? []).map((s) => [s.key, s])), [d?.sessions])
  const filter: CommsFilter = { session: session || undefined, role: role || undefined, req: req || undefined, kind, unsent: onlyUnsent, q }
  const waitingCodes = useMemo(() => new Set((d?.waiting ?? []).map((m) => m.code)), [d?.waiting])
  const all = (d?.messages ?? []).filter((m) => !waitingCodes.has(m.code)) // 置顶的不在下面重复（原则第 5 条）
  const scoped = all.filter((m) => matches(m, filter))
  const sysCount = scoped.filter((m) => m.system).length
  const shown = scoped.filter((m) => showSys || !m.system)
  const waiting = (d?.waiting ?? []).filter((m) => matches(m, { ...filter, unsent: false }))
  const offline = (d?.offline ?? []).filter((m) => matches(m, { ...filter, unsent: false }))
  const reqs = useMemo(() => {
    const seen = new Map<string, string>()
    for (const m of [...(d?.waiting ?? []), ...(d?.messages ?? [])]) if (m.requirement && !seen.has(m.requirement)) seen.set(m.requirement, m.requirement_title ?? '')
    return [...seen].sort((a, b) => Number(b[0].slice(2)) - Number(a[0].slice(2)))
  }, [d?.messages, d?.waiting])
  const filtered = !!(session || role || req || kind !== 'all' || onlyUnsent || q.trim())
  const clear = () => {
    setParam('session', '')
    setRole('')
    setReq('')
    setKind('all')
    setOnlyUnsent(false)
    setQ('')
  }

  const live = (d?.sessions ?? []).filter((s) => s.status !== 'offline')
  const standby = live.filter((s) => s.status === 'standby').length
  const projName = projList.find((p) => p.id === project)?.name ?? d?.project_name ?? ''
  const sessName = session ? sessions.get(session)?.title || session : ''

  const projectMenu = (
    <Menu
      align="start"
      trigger={
        <button className="inline-flex items-center gap-1 rounded-md font-medium text-fg-2 hover:text-fg" aria-label={t.comms.pickProject}>
          {projName || t.comms.pickProject}
          <ChevronDown className="size-3.5" />
        </button>
      }
      items={projList.map((p) => ({ label: p.name, hint: p.remote, checked: p.id === project, onSelect: () => pickProject(p.id) }))}
    />
  )
  const roleItems: MenuItem[] = [
    { label: t.comms.anyRole, checked: !role, onSelect: () => setRole('') },
    ...roleList.map((r) => ({ label: t.role[r], checked: role === r, onSelect: () => setRole(r) })),
    { label: t.comms.noRole, checked: role === 'none', onSelect: () => setRole('none') },
  ]
  const reqItems: MenuItem[] = [
    { label: t.comms.anyReq, checked: !req, onSelect: () => setReq('') },
    ...(reqs.length ? reqs.map(([code, title]) => ({ label: `${code} ${title}`, checked: req === code, onSelect: () => setReq(code) })) : [{ type: 'label' as const, label: t.comms.noReqs }]),
  ]
  const chipBtn = (on: boolean) => cx('inline-flex h-8 items-center gap-1 rounded-lg border px-2.5 text-[13px] font-medium whitespace-nowrap transition-colors', on ? 'border-accent/60 bg-accent-soft text-accent' : 'border-line bg-surface text-fg-2 hover:border-line-strong')

  if (projects.isSuccess && projList.length === 0) return <PageHeader title={t.comms.title} subtitle={t.comms.noProject} />

  return (
    <div>
      <PageHeader
        title={t.comms.title}
        subtitle={
          <span className="flex flex-wrap items-center gap-1.5">
            {projectMenu}
            <span>· {t.comms.subtitle}</span>
          </span>
        }
        actions={<Segmented size="sm" value={range} onChange={setRange} options={(['h1', 'h6', 'today', 'week'] as const).map((v) => ({ value: v, label: t.comms.range[v] }))} />}
      />
      {comms.isError ? (
        <ErrorState error={comms.error} onRetry={() => comms.refetch()} />
      ) : (
        <div className="grid gap-5 lg:grid-cols-[300px_minmax(0,1fr)] xl:grid-cols-[320px_minmax(0,1fr)]">
          {/* 左栏：在线会话 */}
          <section className="min-w-0">
            <div className="mb-2.5 flex items-center gap-2">
              <h2 className="text-[15px] font-semibold text-fg">{t.comms.sessions}</h2>
              <span className="text-[12.5px] text-fg-3">{d ? t.comms.sessionsHint(live.length, standby) : ''}</span>
              <button type="button" onClick={() => setEnded(!ended)} aria-pressed={ended} className={cx('ml-auto', chipBtn(ended), 'h-7 text-[12.5px]')}>
                {t.comms.withEnded}
              </button>
            </div>
            <div className="space-y-2.5">
              <button
                type="button"
                onClick={() => setParam('session', '')}
                aria-pressed={!session}
                className={cx('flex h-11 w-full items-center gap-2 rounded-xl border bg-surface px-3.5 text-left', !session ? 'border-accent ring-1 ring-accent/40' : 'border-line hover:border-line-strong')}
              >
                <span className="text-[14px] font-semibold text-fg">{t.comms.allSessions}</span>
                <span className="ml-auto text-[12px] text-fg-3">{d ? t.comms.allHint(d.today, d.today_broadcasts) : ''}</span>
              </button>
              {!d ? (
                [0, 1, 2].map((i) => <Skeleton key={i} className="h-[118px] rounded-xl" />)
              ) : (
                (d.sessions ?? []).map((s) => <SessionItem key={s.key} s={s} now={now} active={s.key === session} onClick={() => pickSession(s.key)} />)
              )}
            </div>
          </section>

          {/* 右栏：消息 */}
          <section className="min-w-0">
            <div className="mb-2.5 flex flex-wrap items-center gap-1.5">
              <Segmented size="sm" value={kind} onChange={setKind} options={(['all', 'action', 'fyi', 'cast'] as const).map((v) => ({ value: v, label: t.comms.kind[v] }))} />
              <Menu align="start" trigger={<button className={chipBtn(!!role)}>{role ? (role === 'none' ? t.comms.noRole : t.role[role]) : t.comms.role}<ChevronDown className="size-3.5" /></button>} items={roleItems} />
              <Menu align="start" width="min-w-[260px] max-w-[360px]" trigger={<button className={chipBtn(!!req)}>{req || t.comms.req}<ChevronDown className="size-3.5" /></button>} items={reqItems} />
              <button type="button" onClick={() => setOnlyUnsent(!onlyUnsent)} aria-pressed={onlyUnsent} className={chipBtn(onlyUnsent)}>
                {t.comms.onlyUnsent}
              </button>
              {session && (
                <span className={cx(chipBtn(true), 'max-w-[14rem]')}>
                  <span className="truncate">{sessName}</span>
                  <button type="button" aria-label={t.comms.clear} onClick={() => setParam('session', '')} className="-mr-1 rounded p-0.5 hover:bg-accent/10">
                    <X className="size-3.5" />
                  </button>
                </span>
              )}
              <span className="ml-auto flex items-center gap-1.5">
                <button type="button" onClick={() => setShowSys(!showSys)} aria-pressed={showSys} className={chipBtn(showSys)}>
                  <BellOff className={cx('size-3.5', showSys && 'hidden')} />
                  {t.comms.sys(sysCount)}
                </button>
                <span className="relative w-36">
                  <Search className="pointer-events-none absolute top-1/2 left-2.5 size-3.5 -translate-y-1/2 text-fg-3" />
                  <Input value={q} onChange={(e) => setQ(e.target.value)} placeholder={t.comms.search} aria-label={t.comms.search} className="h-8 pl-8 text-[13px] sm:h-8" />
                </span>
              </span>
            </div>

            {/* 没送到：还在等对方醒来的置顶；对方已结束的折叠 */}
            {(waiting.length > 0 || offline.length > 0) && (
              <Card className="mb-3 overflow-hidden border-wait/40 bg-wait-soft/60 p-0">
                {waiting.length > 0 && (
                  <>
                    <div className="flex items-center gap-2 px-4 pt-2.5 pb-1 text-[13px]">
                      <span className="font-semibold text-wait">{t.comms.waiting}</span>
                      <span className="text-fg-3">{t.comms.waitingHint(waiting.length)}</span>
                    </div>
                    {waiting.map((m) => (
                      <Row key={m.code} m={m} now={now} sessions={sessions} onPick={pickSession} />
                    ))}
                  </>
                )}
                {offline.length > 0 && (
                  <>
                    <Tip content={t.comms.offlineHint}>
                      <button type="button" onClick={() => setOffOpen(!offOpen)} aria-expanded={offOpen} className={cx('flex w-full items-center gap-1.5 px-4 py-2 text-left text-[12.5px] text-fg-3 hover:text-fg-2', waiting.length > 0 && 'border-t border-wait/25')}>
                        {offOpen ? <ChevronDown className="size-3.5" /> : <ChevronRight className="size-3.5" />}
                        {t.comms.offline(offline.length)}
                      </button>
                    </Tip>
                    {offOpen && (
                      <div className="max-h-[348px] overflow-y-auto border-t border-wait/25 bg-surface/50">
                        {offline.map((m) => (
                          <Row key={m.code} m={m} now={now} sessions={sessions} onPick={pickSession} muted />
                        ))}
                      </div>
                    )}
                  </>
                )}
              </Card>
            )}

            <Card className="overflow-hidden p-0">
              <div className="flex items-center gap-2 border-b border-line px-4 py-2.5 text-[13px]">
                <span className="font-semibold text-fg">{t.comms.rangeHead[range]}</span>
                <span className="text-fg-3">{t.comms.listHead(scoped.length - sysCount, sysCount, showSys)}</span>
                {filtered && (
                  <Button size="sm" variant="ghost" className="ml-auto h-7" onClick={clear}>
                    {t.comms.clear}
                  </Button>
                )}
              </div>
              {!d ? (
                <div className="space-y-2 p-4">
                  {[0, 1, 2, 3].map((i) => (
                    <Skeleton key={i} className="h-10" />
                  ))}
                </div>
              ) : shown.length === 0 ? (
                <div className="px-4 py-10 text-center text-sm text-fg-3">{filtered || sysCount ? t.comms.emptyFiltered : t.comms.empty}</div>
              ) : (
                shown.map((m) => <Row key={m.code} m={m} now={now} sessions={sessions} onPick={pickSession} />)
              )}
              {d?.truncated && <div className="border-t border-line px-4 py-2 text-xs text-fg-3">{t.comms.truncated}</div>}
            </Card>
          </section>
        </div>
      )}
    </div>
  )
}
