import { useCallback, useEffect, useLayoutEffect, useMemo, useRef, useState, type ReactNode } from 'react'
import { ArrowLeftRight, Copy, HelpCircle, Info, ListChecks, MonitorDot, MoreHorizontal, RefreshCw, ShieldAlert, TimerOff, Unlink, X } from 'lucide-react'
import { toast } from 'sonner'
import { useNavigate } from 'react-router'
import type { Schemas, Session } from '../api/client'
import { useSessionDetail, useSessionTurns } from '../api/queries'
import { t } from '../i18n'
import { modelShort } from '../lib/model'
import { countHits, fitRows, mergePages, relatedIds, sortObjects } from '../lib/turns'
import { absTime, copyText, cx, duration, shortDuration, shortPath, useNow, wan } from '../lib/util'
import { Button, ErrorState, Skeleton, Tip } from '../ui/base'
import { Menu, Modal, Panel, type MenuItem } from '../ui/overlay'
import { displayState, RoleBadge, StatusDot, ToolMark } from '../ui/marks'
import { DecGlyph, Linkify, RefChip } from '../ui/refmark'
import { useActions } from './actions'
import { CtxRing, ctxLoad } from './assign'
import { canHandoff, compactCommand, HandoffCard, HandoffIcon, offlineOf, useRequestHandoff, type Handoff } from './compact'
import { canMessage, MessageBox } from './messageBox'
import { PeekScope, RefPeek, usePeek } from './peek'
import { NewReqModal } from './requirements'
import { sessionTitle, splitTool } from './SessionCard'
import { hm, PendingWeb, TurnList, type Hit } from './sessionTurns'

// 会话详情页（R-273，D-1145 选 B）：顶部一处状态 + 对象索引 + 按轮的对话时间线 + 底部输入框。
// 状态只在头部出现一次（设计原则第 5 条）；模型概括标明是概括（第 11 条）；健康提醒是细条，没有时不占位。
// 点索引或时间线里的印记：左边开第二层纸（需求 / 决策抽屉，审批仍在那里，第 2 条），时间线只亮相关条目。

type TurnsPage = Schemas['TurnsOutBody']
type Obj = Schemas['SessionObject']
type Run = Schemas['RunTask']

const offlineHanded = (s: Session, hs: Handoff[]) => hs.some((h) => h.from_session === s.key && (h.status === 'written' || h.status === 'taken'))

// stateLine：头部那一行状态——「待命 · 在等纸鸢的消息，最多等到 17:26」。只认记录（状态、ks wait 截止、下线时间）。
function stateLine(s: Session, hs: Handoff[], now: number): { dot: string; label: string; detail: string } {
  const off = offlineOf(s)
  if (off) return { dot: 'ended', label: t.turns.st.offline, detail: offlineHanded(s, hs) ? t.turns.offlineHanded(hm(off)) : t.turns.offlineBare(hm(off)) }
  if (s.retired_at) return { dot: 'ended', label: t.turns.st.retired, detail: t.turns.retiredBy }
  const ds = displayState(s)
  const label = t.turns.st[ds] ?? t.state[ds] ?? ds
  if (ds === 'parked') return { dot: ds, label, detail: s.parked_until ? t.turns.parkedUntil(hm(s.parked_until)) : t.turns.parkedPlain }
  if (ds === 'ended' || ds === 'done') return { dot: ds, label, detail: '' }
  return { dot: ds, label, detail: s.state_since ? duration(s.state_since, now) : '' }
}

// Notice：头部的细提醒（版本旧、另一个终端开着、上下文过长、待命超时、等你授权 / 在问你）。
function Notice({ icon, tone, children, action }: { icon: ReactNode; tone: 'wait' | 'danger' | 'accent' | 'reply'; children: ReactNode; action?: ReactNode }) {
  const cls = { wait: 'bg-wait-soft/70 text-wait', danger: 'bg-danger-soft/70 text-danger', accent: 'bg-accent-soft/70 text-accent', reply: 'bg-reply-soft/70 text-reply' }[tone]
  return (
    <div className={cx('flex items-start gap-2 rounded-lg px-2.5 py-1.5 text-[12.5px] leading-relaxed', cls)}>
      <span className="mt-[3px] shrink-0">{icon}</span>
      <div className="min-w-0 flex-1 text-fg-2 max-sm:line-clamp-2">{children}</div>
      {action}
    </div>
  )
}

function Notices({ s }: { s: Session }) {
  const ds = displayState(s)
  const c = s.state !== 'ended' ? ctxLoad(s) : null
  const rows: ReactNode[] = []
  if (s.state === 'ended') return null
  if (ds === 'waiting_approval')
    rows.push(
      <Notice key="appr" tone="wait" icon={<ShieldAlert className="size-3.5" />}>
        <span className="font-mono text-[12px] break-all text-fg">{s.state_detail}</span>
        <span className="ml-1.5 text-fg-3">{t.session.approvalHint}</span>
      </Notice>,
    )
  if (ds === 'waiting_answer') {
    const [name, brief] = splitTool(s.state_detail)
    rows.push(
      <Notice key="ask" tone="reply" icon={name === 'ExitPlanMode' ? <ListChecks className="size-3.5" /> : <HelpCircle className="size-3.5" />}>
        <span className="font-medium text-reply">{name === 'ExitPlanMode' ? t.session.planConfirm : t.session.asking}</span>
        {brief && <span className="ml-1.5 text-fg">{brief}</span>}
      </Notice>,
    )
  }
  if (s.tools_stale)
    rows.push(
      <Notice key="stale" tone="wait" icon={<RefreshCw className="size-3.5" />}>
        {t.session.toolsStaleTip(s.tools_stale)}
      </Notice>,
    )
  if (s.other_terminal)
    rows.push(
      <Notice key="term" tone="accent" icon={<MonitorDot className="size-3.5" />}>
        {t.session.otherTerminalShort}
      </Notice>,
    )
  if (c?.level === 'high') {
    const cmd = compactCommand(s)
    rows.push(
      <Notice
        key="ctx"
        tone="danger"
        icon={<CtxRing c={c} />}
        action={
          <button onClick={async () => (await copyText(cmd)) && toast.success(t.session.compactCopied)} className="shrink-0 font-medium text-danger hover:underline">
            {t.session.compactCopy}
          </button>
        }
      >
        {t.session.compactLine(wan(c.used))}
      </Notice>,
    )
  }
  if (s.idle_at && s.role === 'dev' && s.state !== 'ended')
    rows.push(
      <Notice key="idle" tone="wait" icon={<TimerOff className="size-3.5" />}>
        {t.session.idleLine}
      </Notice>,
    )
  if (!rows.length) return null
  return <div className="mt-2 space-y-1">{rows}</div>
}

// Tasks：在跑的子任务 / 后台 shell——名字 · 模型 · 时长，不显示原始命令（验收第 5 条）。多的收起。
function Tasks({ list, now }: { list: Run[]; now: number }) {
  const [open, setOpen] = useState(false)
  if (!list.length) return null
  const shown = open ? list : list.slice(0, 2)
  return (
    <div className="mt-1.5 space-y-0.5 text-[12.5px] text-fg-3" data-running>
      {shown.map((x) => {
        const name = x.name || (x.kind === 'shell' ? (t.desk.tl.sh.kind[x.type ?? ''] ?? t.desk.tl.sh.kind.other) : x.type || t.session.taskUnnamed)
        return (
          <div key={x.id} className="flex min-w-0 items-center gap-1.5">
            <span className="size-1.5 shrink-0 animate-pulse rounded-full bg-run" />
            <span className="shrink-0">{t.turns.runningLine}</span>
            <span className="shrink-0 text-[11px]">{t.session.taskKind[x.kind]}</span>
            <Tip truncated content={name}>
              <span className={cx('min-w-0 truncate', x.name ? 'font-medium text-fg-2' : 'text-fg-3')}>{name}</span>
            </Tip>
            <span className="shrink-0">{[x.model && modelShort(x.model), shortDuration(x.started_at, now)].filter(Boolean).join(t.common.sep)}</span>
          </div>
        )
      })}
      {list.length > 2 && (
        <button onClick={() => setOpen(!open)} className="pl-3 text-accent hover:underline">
          {open ? t.turns.collapse : t.turns.moreTasks(list.length - 2)}
        </button>
      )}
    </div>
  )
}

// Head：头部——身份、名字、一行状态、工具 / 模型 / 设备、模型概括、细提醒、在跑的任务。
function Head({ s, hs, running, now, menu, onClose }: { s: Session; hs: Handoff[]; running: Run[]; now: number; menu: MenuItem[]; onClose: () => void }) {
  const st = stateLine(s, hs, now)
  const working = (s.requirements ?? []).filter((r) => r.status === 'in_progress' || r.status === 'review' || r.status === 'blocked')
  const meta = [t.tool[s.tool] ?? s.tool, s.model && modelShort(s.model), s.device_name].filter(Boolean).join(t.common.sep)
  // 每一段前都带「·」，整行左移一个「点 + 间距」再裁掉左侧：每行行首那一段的点被裁掉，折行时既不悬在行尾也不顶在行首
  // 引用印记之间不画点，只留同样宽的空位，间距和其它段一致
  const seg = (key: string, children: ReactNode, cls = '', dot = true) => (
    <span key={key} className={cx('inline-flex items-center gap-1.5', cls)}>
      <span aria-hidden className={cx('w-1.5 shrink-0 text-center text-fg-3/70', !dot && 'invisible')}>
        ·
      </span>
      {children}
    </span>
  )
  return (
    <div className="shrink-0 border-b border-line px-4 pt-2.5 pb-2 sm:px-5 sm:pt-3.5 sm:pb-3">
      <div className="flex items-center gap-2">
        <RoleBadge s={s} />
        <h2 className="min-w-0 flex-1 truncate font-display text-[17px] font-semibold text-fg">{sessionTitle(s)}</h2>
        <Menu
          width="min-w-[220px]"
          trigger={
            <Button size="icon-sm" variant="ghost" aria-label={t.turns.menu}>
              <MoreHorizontal className="size-4" />
            </Button>
          }
          items={menu}
        />
        <Button size="icon-sm" variant="ghost" aria-label={t.common.close} onClick={onClose}>
          <X className="size-4" />
        </Button>
      </div>
      <div className="mt-1.5 -ml-1 overflow-x-clip pl-1">
        <div className="-ml-3 flex flex-wrap items-center gap-x-1.5 gap-y-1 text-[13px] text-fg-3" data-state-line>
          {seg(
            'state',
            <>
              <span className="inline-flex items-center gap-1.5 font-medium text-fg">
                <StatusDot state={st.dot} />
                {st.label}
              </span>
              {st.detail && <span>{st.detail}</span>}
            </>,
          )}
          {working.length > 0 && (
            <>
              {seg('working', <span>{t.turns.working}</span>)}
              {working.slice(0, 2).map((r) => seg(r.id, <RefChip code={r.code} title={r.title} status={r.status} label="title" />, 'min-w-0', false))}
              {working.length > 2 && seg('more', <span>+{working.length - 2}</span>, '', false)}
            </>
          )}
          {/* 手机宽度下时长、工具、模型、设备收进「会话信息」，头部只留状态（D-1171） */}
          {seg(
            'age',
            <Tip content={absTime(s.started_at)}>
              <span className="tabular">{t.turns.age(duration(s.started_at, s.state === 'ended' && s.last_active_at ? Date.parse(s.last_active_at) : now))}</span>
            </Tip>,
            'max-sm:hidden',
          )}
          {seg(
            'meta',
            <span className="inline-flex min-w-0 items-center gap-1">
              <ToolMark tool={s.tool} size="sm" />
              <span className="truncate">{meta}</span>
            </span>,
            'max-w-full max-sm:hidden',
          )}
        </div>
      </div>
      {s.summary && (
        <div className="mt-1.5 flex items-start gap-1.5 text-[13px] leading-relaxed text-fg-2">
          <Tip content={t.turns.guessTip}>
            <span className="mt-[2px] shrink-0 rounded border border-line px-1 text-[11px] leading-4 text-fg-3">{t.turns.guess}</span>
          </Tip>
          <span className="min-w-0 max-sm:line-clamp-1">
            <Linkify text={s.summary} />
          </span>
        </div>
      )}
      <Notices s={s} />
      <Tasks list={running} now={now} />
    </div>
  )
}

// Index：对象索引——这个会话动过的需求、决策（印记形状区分），待你答的在最前；最多一行半，多的折成「+n」。
function Index({ objs, sel, onPick }: { objs: Obj[]; sel: string | null; onPick: (o: Obj) => void }) {
  const box = useRef<HTMLDivElement>(null)
  const [all, setAll] = useState(false)
  const [shown, setShown] = useState(objs.length)
  const sig = objs.map((o) => o.code + o.status + (o.pending ? 1 : 0)).join(',')
  const measure = useCallback(() => {
    const el = box.current
    if (!el || all) return setShown(objs.length)
    const kids = [...el.querySelectorAll<HTMLElement>('[data-obj]')]
    kids.forEach((k) => (k.style.display = ''))
    const r0 = el.getBoundingClientRect()
    const boxes = kids.map((k) => {
      const r = k.getBoundingClientRect()
      return { left: r.left - r0.left, top: Math.round(r.top - r0.top), width: r.width }
    })
    const n = fitRows(boxes, el.clientWidth, 36)
    kids.forEach((k, i) => (k.style.display = i < n ? '' : 'none'))
    setShown(n)
  }, [all, objs.length])
  useLayoutEffect(() => {
    measure()
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [sig, all])
  useEffect(() => {
    const el = box.current
    if (!el) return
    const ro = new ResizeObserver(() => measure())
    ro.observe(el)
    return () => ro.disconnect()
  }, [measure])
  if (!objs.length) return null
  return (
    <div className="shrink-0 border-b border-line bg-surface-2/60 px-4 py-2 sm:px-5">
      <div ref={box} className="flex flex-wrap items-center gap-1.5" data-index>
        {objs.map((o) => {
          const on = sel === o.code
          if (o.kind === 'agents' || o.kind === 'messages' || o.kind === 'briefs')
            return (
              <button
                key={o.code}
                data-obj={o.code}
                onClick={() => onPick(o)}
                className={cx('inline-flex h-[22px] items-center gap-1 rounded-md border px-1.5 text-[12px] transition', on ? 'border-accent bg-accent-soft text-accent' : 'border-transparent text-fg-3 hover:bg-hover hover:text-fg-2')}
              >
                {t.turns.count[o.kind]}
                <span className="tabular">{(o.items ?? []).length}</span>
              </button>
            )
          return (
            <span key={o.code} data-obj={o.code} className={cx('inline-flex max-w-[15rem] items-center gap-1 rounded-md', on && 'ring-2 ring-accent/40')}>
              {o.pending && (
                <span className="inline-flex shrink-0 items-center gap-0.5 rounded bg-reply px-1 text-[10.5px] leading-4 font-semibold text-seal-fg">
                  <DecGlyph open className="size-2.5 text-seal-fg" />
                  {t.turns.pendingMark}
                </span>
              )}
              {/* 外包一层可收缩的盒子：带「待答复」标时印记不超出 15rem，不压住后面的「+n」 */}
              <span className="flex min-w-0">
                <RefChip code={o.code} title={o.title} status={o.kind === 'req' ? o.status : undefined} label="title" onOpen={() => onPick(o)} className="max-w-full" />
              </span>
            </span>
          )
        })}
        {!all && shown < objs.length && (
          <button onClick={() => setAll(true)} className="h-[22px] rounded-md px-1.5 text-[12px] font-medium text-accent hover:bg-hover">
            {t.turns.more(objs.length - shown)}
          </button>
        )}
        {all && (
          <button onClick={() => setAll(false)} className="h-[22px] rounded-md px-1.5 text-[12px] text-fg-3 hover:bg-hover">
            {t.turns.collapse}
          </button>
        )}
      </div>
    </div>
  )
}

function InfoRow({ label, children, mono }: { label: string; children: ReactNode; mono?: boolean }) {
  return (
    <div className="min-w-0">
      <dt className="text-xs text-fg-3">{label}</dt>
      <dd className={cx('mt-0.5 truncate text-sm text-fg', mono && 'font-mono text-[13px]')}>{children || '—'}</dd>
    </div>
  )
}

// SessionInfo：「会话信息」弹层——设备、工具、模型、上下文、权限、分支、目录、会话 ID、交接。
function SessionInfo({ s, hs, open, onClose, now }: { s: Session; hs: Handoff[]; open: boolean; onClose: () => void; now: number }) {
  const c = ctxLoad(s)
  return (
    <Modal open={open} onClose={onClose} title={t.turns.info}>
      <dl className="grid grid-cols-2 gap-x-4 gap-y-3">
        <InfoRow label={t.session.device}>{s.device_name}</InfoRow>
        <InfoRow label={t.session.client}>{[t.tool[s.tool], s.profile, s.client].filter(Boolean).join(t.common.sep)}</InfoRow>
        <InfoRow label={t.session.model} mono>
          {s.model}
        </InfoRow>
        <InfoRow label={t.session.ctxLabel}>
          {c && (
            <span className="inline-flex items-center gap-1.5">
              <CtxRing c={c} />
              {c.label}
            </span>
          )}
        </InfoRow>
        <InfoRow label={t.session.permMode}>{t.permMode[s.permission_mode] || s.permission_mode}</InfoRow>
        <InfoRow label={t.session.branch} mono>
          {s.branch && `${s.branch}${s.dirty ? ` ±${s.dirty}` : ''}`}
        </InfoRow>
        <InfoRow label={t.session.startedAt}>{`${absTime(s.started_at)}（${duration(s.started_at, now)}）`}</InfoRow>
        <div className="col-span-2">
          <InfoRow label={t.session.cwd} mono>
            <span title={s.cwd}>{shortPath(s.cwd, 64)}</span>
          </InfoRow>
        </div>
        <div className="col-span-2 flex items-end gap-2">
          <InfoRow label={t.session.sessionId} mono>
            {s.session_id}
          </InfoRow>
          <Button size="icon-sm" variant="ghost" aria-label={t.common.copy} onClick={async () => (await copyText(s.session_id)) && toast.success(t.common.copied)}>
            <Copy className="size-3.5" />
          </Button>
        </div>
      </dl>
      {hs.length > 0 && (
        <div className="mt-4 space-y-2">
          <div className="text-xs text-fg-3">{t.turns.handoffs}</div>
          {hs.slice(0, 2).map((h) => (
            <HandoffCard key={h.id} h={h} me={s.key} />
          ))}
        </div>
      )}
    </Modal>
  )
}

// Timeline：可滚动的时间线。打开时停在最底；往上滚到顶自动加载更早的一页，位置不跳；新内容来时人在底部就跟着到底。
function Timeline({ s, pages, more, loadingOlder, loadOlder, hit, filterKey, models, now }: {
  s: Session
  pages: TurnsPage[]
  more: boolean
  loadingOlder: boolean
  loadOlder: () => void
  hit: Hit
  filterKey: string | null
  models: Record<string, string>
  now: number
}) {
  const box = useRef<HTMLDivElement>(null)
  const top = useRef<HTMLDivElement>(null)
  const atBottom = useRef(true)
  const prevH = useRef(0)
  const prevPages = useRef(0)
  const turns = useMemo(() => mergePages(pages), [pages])
  const pending = pages[0]?.pending ?? []
  const ds = displayState(s)
  const busy = ds === 'running' || ds === 'background' || ds === 'waiting_approval' || ds === 'waiting_answer'
  const who = s.role && t.role[s.role] ? t.role[s.role] : t.turns.self
  // 「只记录了开头一段」只在最早出现的那一轮提示一次（D-1171），老会话不必每轮重复
  const briefAt = turns.findIndex((x) => !!x.reply && x.reply_kind === 'brief')
  // 加载了更早的一页：保持视线位置；否则在底部时跟到底
  useLayoutEffect(() => {
    const el = box.current
    if (!el) return
    if (pages.length > prevPages.current && prevPages.current > 0) el.scrollTop += el.scrollHeight - prevH.current
    else if (atBottom.current) el.scrollTop = el.scrollHeight
    prevPages.current = pages.length
    prevH.current = el.scrollHeight
  })
  useEffect(() => {
    const el = top.current
    if (!el || !more) return
    const io = new IntersectionObserver((es) => es.some((e) => e.isIntersecting) && !loadingOlder && loadOlder(), { root: box.current, rootMargin: '200px 0px 0px 0px' })
    io.observe(el)
    return () => io.disconnect()
  }, [more, loadingOlder, loadOlder])
  // 选中对象：滚到第一条相关的
  useEffect(() => {
    if (!filterKey) return
    const first = box.current?.querySelector('[data-hit][data-entry]:not([data-entry=""])')
    first?.scrollIntoView({ block: 'center', behavior: 'smooth' })
  }, [filterKey])
  return (
    <div
      ref={box}
      onScroll={(e) => {
        const el = e.currentTarget
        atBottom.current = el.scrollHeight - el.scrollTop - el.clientHeight < 40
      }}
      className="min-h-0 flex-1 overflow-y-auto overscroll-contain px-4 sm:px-5"
      data-timeline
    >
      <div ref={top} className="py-2 text-center text-[12px] text-fg-3">
        {more ? (loadingOlder ? t.turns.loading : <button onClick={loadOlder} className="hover:text-fg-2">{t.turns.loadOlder}</button>) : turns.length ? t.turns.start : ''}
      </div>
      {!turns.length && !pending.length && <p className="py-8 text-center text-sm text-fg-3">{t.turns.noTurns}</p>}
      <TurnList turns={turns} who={who} busy={busy} models={models} hit={hit} now={now} briefAt={briefAt} />
      {pending.length > 0 && (
        <div className="pb-3">
          {pending.map((p) => (
            <PendingWeb key={p.id} x={p} busy={busy} hit={hit} />
          ))}
        </div>
      )}
    </div>
  )
}

export function SessionPanel({ sessionKey, onClose }: { sessionKey: string | null; onClose: () => void }) {
  const now = useNow()
  const q = useSessionDetail(sessionKey)
  const tq = useSessionTurns(sessionKey)
  const { linkReq } = useActions()
  const [creating, setCreating] = useState(false)
  const [info, setInfo] = useState(false)
  const [kindSel, setKindSel] = useState<string | null>(null)
  // 点需求 / 决策在会话抽屉左边再展开一张纸（R-196）
  const peek = usePeek(null, sessionKey)
  useEffect(() => setKindSel(null), [sessionKey])
  const s: Session | undefined = q.data?.session
  const handoffs = (q.data?.handoffs ?? []) as Handoff[]
  const requestHandoff = useRequestHandoff()
  const navigate = useNavigate()
  const pages = useMemo(() => tq.data?.pages ?? [], [tq.data])
  const objs = useMemo(() => sortObjects(pages[0]?.objects ?? []), [pages])
  const models = pages[0]?.agent_models ?? {}
  // 选中的对象：开着的纸（只认索引里有的），或点了子任务 / 消息 / 简报的计数
  const peekCode = peek.stack[0] ?? null
  const sel = kindSel ?? (peekCode && objs.some((o) => o.code === peekCode) ? peekCode : null)
  const ids = useMemo(() => relatedIds(objs, sel), [objs, sel])
  const hit: Hit = useCallback((id: string) => !ids || ids.has(id), [ids])
  const selObj = objs.find((o) => o.code === sel)
  const loaded = useMemo(() => (ids ? countHits(mergePages(pages), ids) : 0), [ids, pages])
  const clear = () => {
    setKindSel(null)
    peek.close()
  }
  const pick = (o: Obj) => {
    if (o.kind === 'agents' || o.kind === 'messages' || o.kind === 'briefs') {
      peek.close()
      setKindSel((k) => (k === o.code ? null : o.code))
      return
    }
    setKindSel(null)
    peek.open(o.code)
  }
  const loadOlder = useCallback(() => void tq.fetchNextPage(), [tq])

  const menu: MenuItem[] = s
    ? [
        { label: t.turns.info, icon: <Info className="size-3.5" />, onSelect: () => setInfo(true) },
        ...(s.project_id ? [{ label: t.comms.open, icon: <ArrowLeftRight className="size-3.5" />, onSelect: () => navigate(`/comms?project=${encodeURIComponent(s.project_id ?? '')}&session=${encodeURIComponent(s.key)}`) }] : []),
        ...(canHandoff(s) ? [{ label: t.handoff.action, hint: t.menu.handoffHint, icon: <HandoffIcon className="size-3.5" />, onSelect: () => void requestHandoff(s) }] : []),
        { label: t.turns.giveNew, icon: <ListChecks className="size-3.5" />, onSelect: () => setCreating(true) },
        ...((s.requirements ?? []).filter((r) => r.status !== 'done' && r.status !== 'shelved').length
          ? [
              { type: 'separator' as const },
              ...(s.requirements ?? [])
                .filter((r) => r.status !== 'done' && r.status !== 'shelved')
                .slice(0, 8)
                .map((r) => ({ label: t.turns.unlink(r.code), hint: r.title, icon: <Unlink className="size-3.5" />, onSelect: () => void linkReq(r.code, s.key, true) })),
            ]
          : []),
      ]
    : []

  const aside = peek.stack.length && sessionKey ? <RefPeek peek={peek} from={`s:${sessionKey}`} /> : undefined
  return (
    <PeekScope peek={peek}>
      <Panel open={!!sessionKey} aside={aside} onAsideClose={peek.close} onClose={onClose} wide bare title={s ? sessionTitle(s) : t.session.timeline}>
        {q.isError ? (
          <div className="p-5">
            <ErrorState error={q.error} onRetry={() => q.refetch()} />
          </div>
        ) : !s ? (
          <div className="space-y-3 p-5">
            <Skeleton className="h-6 w-48" />
            <Skeleton className="h-16" />
            <Skeleton className="h-32" />
          </div>
        ) : (
          <div className="flex min-h-0 flex-1 flex-col" data-session-panel>
            <Head s={s} hs={handoffs} running={displayState(s) === 'done' ? [] : (pages[0]?.running ?? [])} now={now} menu={menu} onClose={onClose} />
            <Index objs={objs} sel={sel} onPick={pick} />
            {sel && (
              <div className="flex shrink-0 items-center gap-1.5 border-b border-accent/20 bg-accent-soft/60 px-4 py-1.5 text-[12.5px] text-accent sm:px-5" data-filter>
                <span className="min-w-0 truncate">{selObj && (selObj.kind === 'req' || selObj.kind === 'dec') ? t.turns.filterLine(sel, (selObj.items ?? []).length) : t.turns.filterKind(t.turns.count[sel] ?? sel, (selObj?.items ?? []).length)}</span>
                <span>·</span>
                <button onClick={clear} className="shrink-0 font-medium underline-offset-2 hover:underline">
                  {t.turns.clear}
                </button>
                {loaded === 0 && tq.hasNextPage && (
                  <button onClick={loadOlder} className="ml-auto shrink-0 text-fg-3 hover:text-fg-2">
                    {t.turns.loadOlder}
                  </button>
                )}
              </div>
            )}
            {tq.isError ? (
              <div className="p-5">
                <ErrorState error={tq.error} onRetry={() => tq.refetch()} />
              </div>
            ) : tq.isPending ? (
              <div className="flex-1 space-y-3 p-5">
                <Skeleton className="h-10" />
                <Skeleton className="h-24" />
              </div>
            ) : (
              <Timeline s={s} pages={pages} more={!!tq.hasNextPage} loadingOlder={tq.isFetchingNextPage} loadOlder={loadOlder} hit={hit} filterKey={sel} models={models} now={now} />
            )}
            {canMessage(s) ? (
              <MessageBox s={s} who={s.role && t.role[s.role] ? t.role[s.role] : t.turns.self} onNew={() => setCreating(true)} />
            ) : (
              <div className="shrink-0 border-t border-line px-5 py-3 text-center text-[12.5px] text-fg-3">{t.turns.phEnded}</div>
            )}
            <SessionInfo s={s} hs={handoffs} open={info} onClose={() => setInfo(false)} now={now} />
            <NewReqModal open={creating} onClose={() => setCreating(false)} defaultSession={s.key} />
          </div>
        )}
      </Panel>
    </PeekScope>
  )
}
