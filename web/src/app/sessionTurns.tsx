import { useLayoutEffect, useRef, useState, type ReactNode } from 'react'
import { ArrowRightLeft, BellRing, ChevronRight, Copy, FileText, Globe, GitBranch, HelpCircle, Hourglass, Layers, ListChecks, MessageSquare, SquareTerminal } from 'lucide-react'
import { toast } from 'sonner'
import type { Schemas } from '../api/client'
import { t } from '../i18n'
import { modelShort } from '../lib/model'
import { groupWakes, promptKey, replyKey, toolLine, wakeCounts, wakeKind } from '../lib/turns'
import { absTime, copyText, cx, shortDuration } from '../lib/util'
import { Badge, Tip } from '../ui/base'
import { RoleTag } from '../ui/marks'
import { Md } from '../ui/markdown'
import { DecGlyph, Linkify, RefChip, ReqGlyph } from '../ui/refmark'
import { statusTone } from './statusReq'

// 会话详情页按轮的时间线（R-273）：你说的话（终端 / 网页）→ 纸鸢送达的消息、纸鸢动作（印记 + 一句话，点印记开第二层纸）→
// 工具调用收成一行 → 它的回复（Markdown，长的收起）；这一轮结束后子任务做的事单独一小段。
// 动作只来自纸鸢的明确记录（服务端 /api/sessions/{key}/turns），不从工具参数猜（设计原则第 11 条）。

export type Turn = Schemas['Turn']
export type TurnItem = Schemas['TurnItem']
type Delivery = Schemas['WebDelivery']

// Hit：筛选时条目亮不亮。ids 为空表示没在筛选。
export type Hit = (id: string) => boolean

function Dim({ id, hit, children, className }: { id: string; hit: Hit; children: ReactNode; className?: string }) {
  const on = hit(id)
  return (
    <div data-entry={id} data-hit={on ? '' : undefined} className={cx('transition-opacity duration-200', !on && 'opacity-30', className)}>
      {children}
    </div>
  )
}

// hm：今天的写「17:35」，更早的写「11-03 17:35」。
export function hm(iso?: string | null): string {
  if (!iso) return ''
  const d = new Date(iso)
  const clock = d.toLocaleTimeString(undefined, { hour: '2-digit', minute: '2-digit', hour12: false })
  return d.toDateString() === new Date().toDateString() ? clock : `${String(d.getMonth() + 1).padStart(2, '0')}-${String(d.getDate()).padStart(2, '0')} ${clock}`
}

const At = ({ at }: { at: string }) => (
  <Tip content={absTime(at)}>
    <span className="shrink-0 text-[11.5px] text-fg-3 tabular">{hm(at)}</span>
  </Tip>
)

// Node：竖线上的一个节点：左边小图标，中间一句话，右边时间；下面可放引文。
function Node({ icon, children, at, quote, sub }: { icon: ReactNode; children: ReactNode; at?: string; quote?: ReactNode; sub?: ReactNode }) {
  return (
    <div className="relative grid grid-cols-[16px_minmax(0,1fr)_auto] items-start gap-x-2.5 py-[3px]">
      <span className="relative z-10 mt-[3px] flex size-4 items-center justify-center bg-surface text-fg-3">{icon}</span>
      <div className="min-w-0 text-[13px] leading-[22px] text-fg">
        <div className="flex min-w-0 flex-wrap items-center gap-x-1.5 gap-y-0.5">{children}</div>
        {sub && <div className="text-[12.5px] leading-relaxed text-fg-3">{sub}</div>}
        {quote && <div className="mt-0.5 mb-1 border-l-2 border-line pl-2.5 text-[12.5px] leading-relaxed break-words text-fg-2">{quote}</div>}
      </div>
      {at ? <At at={at} /> : <span />}
    </div>
  )
}

const Muted = ({ children }: { children: ReactNode }) => <span className="text-fg-3">{children}</span>

function Flow({ from, to }: { from?: string; to?: string }) {
  return (
    <span className="inline-flex items-center gap-1">
      {from && <Badge tone={statusTone[from]}>{t.reqStatus[from] ?? from}</Badge>}
      {from && <span className="text-fg-3">→</span>}
      {to && <Badge tone={statusTone[to]}>{t.reqStatus[to] ?? to}</Badge>}
    </span>
  )
}

const chip = (code?: string, title?: string, status?: string) => (code ? <RefChip code={code} title={title} status={status} label="title" /> : null)

// who：对方的称呼——有身份写身份，没有写会话标题，都没有写「纸鸢」（订阅提醒等纸鸢自己发的）。
function who(x: TurnItem): string {
  if (x.peer_role === 'user') return t.turns.user
  if (x.peer_role && t.role[x.peer_role]) return t.role[x.peer_role]
  return x.peer || t.turns.kite
}

// ItemNode：一条纸鸢记录或会话事件。
export function ItemNode({ x, models, now }: { x: TurnItem; models?: Record<string, string>; now: number }) {
  const q = x.text ? <Linkify text={x.text} label="title" /> : undefined
  switch (x.kind) {
    case 'status':
      return (
        <Node icon={<ReqGlyph status={x.to ?? ''} />} at={x.at}>
          <span>{x.via === 'link' ? t.turns.startReq : x.to === 'review' ? t.turns.review : t.turns.status}</span>
          {chip(x.req, x.req_title, x.req_status)}
          {x.from !== x.to && <Flow from={x.from} to={x.to} />}
        </Node>
      )
    case 'evidence':
      return (
        <Node icon={<ListChecks className="size-3.5 text-reply" />} at={x.at} sub={x.text}>
          <span>{t.turns.evidence}</span>
          {chip(x.req, x.req_title, x.req_status)}
          {x.note === 'voided' && <Muted>{t.common.sep + t.turns.voided}</Muted>}
        </Node>
      )
    case 'raise':
      return (
        <Node icon={<DecGlyph open={x.dec_status === 'pending'} />} at={x.at}>
          <span>{t.turns.raise}</span>
          {chip(x.dec, x.dec_title)}
          {x.req && <Muted>·</Muted>}
          {chip(x.req, x.req_title)}
          {x.note && <Muted>{t.common.sep + t.turns.withdrawn}</Muted>}
        </Node>
      )
    case 'tell_out': {
      const to = who(x)
      return (
        <Node icon={<MessageSquare className="size-3.5" />} at={x.at} quote={q}>
          <span>{x.agent ? t.turns.subTellTo(to) : t.turns.tellTo(to)}</span>
          {x.fyi && <Muted>{t.common.sep + t.turns.fyi}</Muted>}
          {chip(x.req, x.req_title)}
        </Node>
      )
    }
    case 'comment':
      return (
        <Node icon={<MessageSquare className="size-3.5" />} at={x.at} quote={q}>
          <span>{t.turns.comment}</span>
          {chip(x.req, x.req_title)}
        </Node>
      )
    case 'edit':
      return (
        <Node icon={<ReqGlyph status="" />} at={x.at}>
          <span>{t.turns.edit}</span>
          {chip(x.req, x.req_title)}
        </Node>
      )
    case 'release':
      return (
        <Node icon={<ReqGlyph status="ready" />} at={x.at} quote={q}>
          <span>{t.turns.release}</span>
          {chip(x.req, x.req_title)}
        </Node>
      )
    case 'agent': {
      const m = x.model || (x.agent ? models?.[x.agent] : '')
      return (
        <Node icon={<GitBranch className="size-3.5 text-accent" />} at={x.at}>
          <span>{t.turns.agent}</span>
          <span className="min-w-0 truncate font-medium">{x.text || x.note || t.session.taskUnnamed}</span>
          <Muted>
            {[m && modelShort(m), x.ended_at ? shortDuration(x.at, Date.parse(x.ended_at)) : `${t.turns.agentRunning} ${shortDuration(x.at, now)}`].filter(Boolean).join(t.common.sep)}
          </Muted>
        </Node>
      )
    }
    case 'brief':
      return (
        <Node icon={<FileText className="size-3.5" />} at={x.at}>
          <span>{t.turns.brief}</span>
          {x.text && <span className="min-w-0 truncate text-fg-2">「{x.text}」</span>}
        </Node>
      )
    case 'handoff':
      return (
        <Node icon={<ArrowRightLeft className="size-3.5" />} at={x.at} quote={q}>
          <span>{t.turns.handoff}</span>
        </Node>
      )
    case 'takeover':
      return (
        <Node icon={<ArrowRightLeft className="size-3.5" />} at={x.at}>
          <span>{t.turns.takeover(x.peer ?? '')}</span>
        </Node>
      )
    case 'compact':
      return (
        <Node icon={<Layers className="size-3.5" />} at={x.at}>
          <Muted>{t.turns.compact}</Muted>
        </Node>
      )
    case 'ask':
      return (
        <Node icon={<HelpCircle className="size-3.5 text-reply" />} at={x.at} quote={q}>
          <span>{x.via === 'ExitPlanMode' ? t.turns.plan : t.turns.ask}</span>
        </Node>
      )
  }
  return null
}

// InboxLine：纸鸢送进这一轮的一条消息（团队消息、你的答复、指派的需求……）。网页上发的话另画成「你」的气泡。
// 身份标记 + 类别 + 一句话（D-1171）：发消息的会话画成身份标，纸鸢提醒写类别（服务端已拆好，正文整理成一句话）。
function InboxLine({ x }: { x: TurnItem }) {
  const kind = x.label || (x.via === 'tell' && x.fyi ? t.turns.inbox.fyi : (t.turns.inbox[x.via ?? ''] ?? ''))
  const role = x.peer_role && x.peer_role !== 'user' && t.role[x.peer_role] ? x.peer_role : ''
  return (
    <div className="text-[12.5px] leading-relaxed break-words text-fg-2">
      {role && (
        <Tip content={x.peer || t.role[role]}>
          <span className="mr-1 inline-flex align-[-3px]">
            <RoleTag role={role} />
          </span>
        </Tip>
      )}
      <span className="text-fg-3">
        {role ? kind : who(x) + (kind ? t.common.sep + kind : '')}
        {(x.dec || x.req) && ' '}
      </span>
      {x.via === 'decision' ? chip(x.dec, x.dec_title) : chip(x.req, x.req_title)}
      <span className="text-fg-3">：</span>
      {x.text && <Linkify text={oneLine(x.text)} label="title" />}
    </div>
  )
}

const oneLine = (s: string) => {
  const line = s.trim().split('\n')[0]
  return line.length > 200 ? line.slice(0, 200) + '…' : line
}

function Tools({ sum, label }: { sum: Turn['tools']; label?: string }) {
  const [open, setOpen] = useState(false)
  if (!sum.n) return null
  const names = toolLine(sum.names)
  return (
    <div className="py-[3px]">
      <button onClick={() => setOpen(!open)} aria-expanded={open} className="grid w-full grid-cols-[16px_minmax(0,1fr)] items-center gap-x-2.5 text-left text-[12.5px] text-fg-3 transition hover:text-fg-2">
        <span className="relative z-10 flex size-4 items-center justify-center bg-surface">
          <ChevronRight className={cx('size-3.5 transition-transform', open && 'rotate-90')} />
        </span>
        <span className="truncate">
          {label ?? t.turns.tools(sum.n)}
          {names.length > 0 && t.common.sep + names.join(t.common.sep)}
          {!label && sum.sub > 0 && t.common.sep + t.turns.toolsSub(sum.sub)}
        </span>
      </button>
      {open && (
        <div className="mt-1 ml-[26px] max-h-72 space-y-px overflow-y-auto rounded-lg bg-sunken/60 px-2.5 py-1.5">
          {(sum.list ?? []).map((c, i) => (
            <div key={i} className="flex min-w-0 items-baseline gap-2 text-[11.5px] leading-5">
              <span className={cx('shrink-0 font-medium', c.sub ? 'text-fg-3' : 'text-fg-2')}>{c.name}</span>
              <span className="min-w-0 flex-1 truncate font-mono text-fg-3" title={c.brief}>
                {c.brief || <i className="font-sans">{t.turns.noBrief}</i>}
              </span>
              <span className="shrink-0 text-[10.5px] text-fg-3 tabular">{hm(c.at)}</span>
            </div>
          ))}
        </div>
      )}
    </div>
  )
}

// Rail：串在一根细线上的几个节点。
function Rail({ children }: { children: ReactNode }) {
  return <div className="relative my-1 before:absolute before:top-3 before:bottom-3 before:left-[7.5px] before:w-px before:bg-line">{children}</div>
}

// Reply：它的回复，长的收起；只有摘要用的前一段或没有记录时写明。
const FOLD = 360
function Reply({ text, note, quiet }: { text: string; note: boolean; quiet?: boolean }) {
  const ref = useRef<HTMLDivElement>(null)
  const [long, setLong] = useState(false)
  const [open, setOpen] = useState(false)
  useLayoutEffect(() => {
    if (ref.current) setLong(ref.current.scrollHeight > FOLD + 60)
  }, [text])
  const folded = long && !open
  return (
    <div>
      <div ref={ref} className={cx('relative leading-relaxed', quiet ? 'text-[13px] text-fg-2' : 'text-[14px]')} style={folded ? { maxHeight: FOLD, overflow: 'hidden' } : undefined}>
        <Md text={text} />
        {folded && <div className="pointer-events-none absolute inset-x-0 bottom-0 h-14 bg-linear-to-t from-surface to-transparent" />}
      </div>
      {long && (
        <button onClick={() => setOpen(!open)} className="mt-0.5 text-[12.5px] font-medium text-accent hover:underline">
          {open ? t.turns.fold : t.turns.expand}
        </button>
      )}
      {note && <p className="mt-1 text-[11.5px] text-fg-3">{t.turns.briefReply}</p>}
    </div>
  )
}

// DeliveryLine：网页上发的话的送达状态。
export function DeliveryLine({ d, busy, sending }: { d?: Delivery; busy: boolean; sending?: boolean }) {
  if (sending) return <span className="text-[12px] text-fg-3">{t.turns.dl.sending}</span>
  if (!d || d.status === 'delivered') return null
  if (d.status === 'woke')
    return (
      <span className="inline-flex items-center gap-1.5 text-[12px] text-run">
        <span className="size-1.5 animate-pulse rounded-full bg-run" />
        {t.turns.dl.woke}
      </span>
    )
  if (d.status === 'queued') return <span className="text-[12px] text-wait">{busy ? t.turns.dl.queuedBusy : d.reason}</span>
  if (d.status === 'closed') return <span className="text-[12px] text-fg-3">{t.turns.dl.closed}</span>
  if (!d.command) return <span className="text-[12px] text-danger">{d.reason}</span>
  const cmd = d.command
  return (
    <div className="flex flex-col items-end gap-1">
      <span className="text-[12px] text-danger">{t.turns.dl.offline}</span>
      <div className="flex max-w-full items-center gap-1.5 rounded-lg bg-danger-soft px-2.5 py-1.5 text-[12px] text-danger">
        <span className="min-w-0 truncate">{t.turns.takeoverHint}</span>
        <code className="shrink-0 rounded bg-surface px-1.5 py-px font-mono text-[11.5px] text-fg">{cmd}</code>
        <button onClick={async () => (await copyText(cmd)) && toast.success(t.common.copied)} className="inline-flex shrink-0 items-center gap-0.5 font-medium underline-offset-2 hover:underline">
          <Copy className="size-3" />
          {t.turns.copy}
        </button>
      </div>
      <span className="text-[11.5px] text-fg-3">{t.turns.takeoverQueued}</span>
    </div>
  )
}

// You：你说的话，靠右浅色底，标终端 / 网页。
function You({ text, web, at, files, children }: { text: string; web?: boolean; at: string; files?: number; children?: ReactNode }) {
  return (
    <div className="flex flex-col items-end gap-1 py-1.5">
      <div className="flex items-center gap-1 text-[11.5px] text-fg-3">
        <span className="font-medium text-fg-2">{t.turns.you}</span>
        <span>·</span>
        {web ? <Globe className="size-3" /> : <SquareTerminal className="size-3" />}
        <span>{web ? t.turns.fromWeb : t.turns.fromTerminal}</span>
        <span>·</span>
        <At at={at} />
      </div>
      <div className="max-w-[88%] rounded-2xl rounded-tr-md bg-accent-soft px-3.5 py-2 text-[14px] leading-relaxed break-words whitespace-pre-wrap text-fg">
        <Linkify text={text} label="title" />
        {!!files && <span className="ml-1 text-[12px] text-fg-3">📎{files}</span>}
      </div>
      {children}
    </div>
  )
}

// TurnView：一轮。
// briefNote：这一轮是最早一条只记了开头一段的回复，在这里提示一次（D-1171）。
export function TurnView({ x, last, who: name, busy, models, hit, now, briefNote, grouped }: { x: Turn; last: boolean; who: string; busy: boolean; models: Record<string, string>; hit: Hit; now: number; briefNote?: boolean; grouped?: boolean }) {
  const inbox = x.inbox ?? []
  const web = inbox.filter((i) => i.web)
  const other = inbox.filter((i) => !i.web)
  const items = x.items ?? []
  const after = x.after ?? []
  const running = last && !x.end && busy
  const empty = !x.source && !inbox.length && !items.length && !after.length && !x.tools.n && !x.after_tools.n && !x.reply
  // 自动唤醒（R-287）：只有待命期间纸鸢送达的写「纸鸢送达」；后台任务通知、子任务回报等显示得轻
  const wk = wakeKind(x)
  const quiet = wk !== '' && wk !== 'kite' && !inbox.length
  if (empty) return null
  return (
    <section className={cx('border-b border-line/70 last:border-b-0', quiet ? 'py-2' : 'py-3', grouped && 'border-b-0 py-1.5')} data-turn={x.id}>
      {!x.source && <p className="mb-1 text-[12px] text-fg-3">{t.turns.noPrompt}</p>}
      {x.source === 'terminal' && (
        <Dim id={promptKey(x)} hit={hit}>
          <You text={x.prompt ?? ''} at={x.start} />
        </Dim>
      )}
      {web.map((m) => (
        <Dim key={m.id} id={m.id} hit={hit}>
          <You text={m.text ?? ''} web at={m.delivered_at ?? m.at}>
            <DeliveryLine d={m.delivery} busy={busy} />
          </You>
        </Dim>
      ))}
      {(other.length > 0 || items.length > 0 || x.tools.n > 0 || (x.source === 'wake' && !web.length)) && (
        <Rail>
          {(other.length > 0 || (x.source === 'wake' && !web.length)) && (
            <Dim id={other.map((o) => o.id).find((id) => hit(id)) ?? other[0]?.id ?? `w:${x.id}`} hit={(id) => hit(id) || other.some((o) => hit(o.id))}>
              {quiet ? (
                <Node icon={<WakeIcon kind={wk} />} at={x.start}>
                  <span className="text-fg-2">{t.turns.wake[wk]}</span>
                  {x.wake_note && <span className="min-w-0 truncate text-fg-3">{x.wake_note}</span>}
                </Node>
              ) : (
                <Node icon={<KiteMark />} at={x.start}>
                  <span className="font-medium">{wk === 'kite' ? t.turns.woken : wk ? t.turns.wake[wk] + t.common.sep + t.turns.sentIn : t.turns.sentIn}</span>
                </Node>
              )}
              {other.length > 0 && <div className="mb-1 ml-[26px] space-y-0.5">{other.map((o) => <InboxLine key={o.id} x={o} />)}</div>}
            </Dim>
          )}
          {items.map((it) => (
            <Dim key={it.id} id={it.id} hit={hit}>
              <ItemNode x={it} models={models} now={now} />
            </Dim>
          ))}
          <Dim id="" hit={hit}>
            <Tools sum={x.tools} />
          </Dim>
        </Rail>
      )}
      {x.reply ? (
        <Dim id={replyKey(x)} hit={hit} className="mt-1.5">
          <div className="mb-1 flex items-center gap-1.5 text-[12px] text-fg-3">
            <span className="font-medium text-fg-2">{name}</span>
            {x.end && (
              <>
                <span>·</span>
                <At at={x.end} />
              </>
            )}
          </div>
          <Reply text={x.reply} note={!!briefNote && x.reply_kind === 'brief'} quiet={quiet} />
        </Dim>
      ) : running ? (
        <p className="mt-1 flex items-center gap-1.5 text-[12.5px] text-run">
          <span className="size-1.5 animate-pulse rounded-full bg-run" />
          {t.turns.running}
        </p>
      ) : x.end ? (
        <Dim id={replyKey(x)} hit={hit}>
          <p className="mt-1 text-[12.5px] text-fg-3">{t.turns.noReply}</p>
        </Dim>
      ) : null}
      {(after.length > 0 || x.after_tools.n > 0) && (
        <div className="mt-2.5 rounded-xl bg-sunken/50 px-3 py-1.5">
          <div className="text-[11.5px] text-fg-3">
            {t.turns.between}
            {t.common.sep}
            {t.turns.betweenSub}
          </div>
          <Rail>
            {after.map((it) => (
              <Dim key={it.id} id={it.id} hit={hit}>
                <ItemNode x={it} models={models} now={now} />
              </Dim>
            ))}
            <Dim id="" hit={hit}>
              <Tools sum={x.after_tools} />
            </Dim>
          </Rail>
        </div>
      )}
    </section>
  )
}

// PendingWeb：网页上发的、还没送进某一轮的话，带送达状态（排在时间线最后）。
export function PendingWeb({ x, busy, hit }: { x: TurnItem; busy: boolean; hit: Hit }) {
  return (
    <Dim id={x.id} hit={hit}>
      <You text={x.text ?? ''} web at={x.at}>
        <DeliveryLine d={x.delivery} busy={busy} />
      </You>
    </Dim>
  )
}

// KiteMark：纸鸢送达消息的小风筝。
function KiteMark() {
  return (
    <svg viewBox="0 0 16 16" aria-hidden className="size-3.5 text-seal">
      <path d="M8 1.5l5 5-5 8-5-8z" fill="none" stroke="currentColor" strokeWidth="1.4" strokeLinejoin="round" />
      <path d="M3 6.5h10M8 1.5v13" stroke="currentColor" strokeWidth="1" />
    </svg>
  )
}


// WakeIcon：自动唤醒来源的小图标（R-287），颜色压低。
function WakeIcon({ kind }: { kind: string }) {
  const c = 'size-3.5 text-fg-3'
  if (kind === 'agent') return <GitBranch className={c} />
  if (kind === 'timeout') return <Hourglass className={c} />
  if (kind === 'output') return <SquareTerminal className={c} />
  return <BellRing className={c} />
}

// wakeSummary：一组自动唤醒的说明「后台任务通知 4 · 子任务回报 1」。
export const wakeSummary = (group: { source: string; wake?: string }[]) =>
  wakeCounts(group)
    .map((c) => `${t.turns.wake[c.kind] ?? c.kind} ${c.n}`)
    .join(t.common.sep)

// WakeGroup：连续几轮轻量的自动唤醒收成一行（R-287）：来源计数 + 时间段 + 最后一轮回复的首行，点开逐轮显示。
// 筛选时组里有相关条目就自动展开。
export function WakeGroup({ turns, open: forced, children }: { turns: Turn[]; open?: boolean; children: ReactNode }) {
  const [open, setOpen] = useState(false)
  const shown = open || !!forced
  const first = turns[0]
  const lastT = turns[turns.length - 1]
  const preview = [...turns].reverse().find((x) => x.reply)?.reply
  return (
    <section className="border-b border-line/70 py-2 last:border-b-0" data-wake-group={first.id}>
      <button onClick={() => setOpen(!shown)} aria-expanded={shown} className="grid w-full grid-cols-[16px_minmax(0,1fr)_auto] items-center gap-x-2.5 text-left text-[12.5px] text-fg-3 transition hover:text-fg-2">
        <span className="flex size-4 items-center justify-center">
          <ChevronRight className={cx('size-3.5 transition-transform', shown && 'rotate-90')} />
        </span>
        <span className="min-w-0 truncate">
          <span className="font-medium text-fg-2">{t.turns.wakeGroup(turns.length)}</span>
          {t.common.sep + wakeSummary(turns)}
        </span>
        <span className="shrink-0 text-[11.5px] tabular">
          {hm(first.start)}–{hm(lastT.end ?? lastT.start)}
        </span>
      </button>
      {!shown && preview && <p className="mt-0.5 ml-[26px] truncate text-[12.5px] text-fg-3">{oneLine(preview)}</p>}
      {shown && <div className="mt-1 ml-[7px] border-l border-line pl-[18px]">{children}</div>}
    </section>
  )
}

// TurnList：按轮显示，连续的轻量自动唤醒收成一组（R-287）。
export function TurnList({ turns, who, busy, models, hit, now, briefAt }: { turns: Turn[]; who: string; busy: boolean; models: Record<string, string>; hit: Hit; now: number; briefAt: number }) {
  const filtering = !hit('\u0000')
  return (
    <>
      {groupWakes(turns).map((r) => {
        if (r.turn) {
          const x = r.turn
          return <TurnView key={`${x.id}@${x.start}`} x={x} last={r.i === turns.length - 1} who={who} busy={busy} models={models} hit={hit} now={now} briefNote={r.i === briefAt} />
        }
        const g = r.group
        const lit = filtering && g.some((x) => hit(promptKey(x)) || hit(replyKey(x)))
        return (
          <WakeGroup key={`g:${g[0].id}@${g[0].start}`} turns={g} open={lit}>
            {g.map((x, k) => (
              <TurnView key={`${x.id}@${x.start}`} x={x} last={false} who={who} busy={busy} models={models} hit={hit} now={now} briefNote={r.i + k === briefAt} grouped />
            ))}
          </WakeGroup>
        )
      })}
    </>
  )
}
