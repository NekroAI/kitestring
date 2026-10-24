import type { ReactNode } from 'react'
import { GitBranch, MessageSquare, Monitor } from 'lucide-react'
import type { Session } from '../api/client'
import { t } from '../i18n'
import { cx, relTime } from '../lib/util'
import { Card, Tip } from '../ui/base'
import { displayState, RoleBadge, StatusPill, ToolMark, stateStyle } from '../ui/marks'
import { DecisionComposer, OrphanCard } from './DecisionCard'
import type { Thread } from './inbox'
import { useOpenSession } from './nav'
import { Item, List } from '../ui/motion'
import { ReviewList } from './review'
import { Callout, ReqCount, sessionExtras, sessionTitle, toolIcon } from './SessionCard'
import { LiveFeed, Ticker } from './liveFeed'
import { useSessionMenu } from './menus'
import { Ctx } from '../ui/contextMenu'
import { Linkify } from '../ui/refmark'
import { awaitingReply, ReplyBox } from './replyBox'
import { bgCount } from './tasks'

// SessionLine：会话在做什么的一行描述（摘要 → 最后动作 → 你最后说的话）。
function activity(s: Session): { icon: ReactNode; text: string; mono?: boolean } | null {
  const ds = displayState(s)
  if (ds === 'background' && s.state_detail) return { icon: null, text: s.state_detail }
  if (s.summary && ds !== 'waiting_reply') return { icon: null, text: s.summary }
  if (ds === 'running' && s.last_tool) {
    const I = toolIcon(s.last_tool)
    return { icon: <I className="size-3.5 shrink-0 text-run" />, text: s.last_tool, mono: true }
  }
  if (s.last_prompt) return { icon: <MessageSquare className="size-3.5 shrink-0 text-fg-3" />, text: s.last_prompt }
  return null
}

// SessionRow：不需要你处理的会话，一行展示。
export function SessionRow({ s, now, showProject }: { s: Session; now: number; showProject?: boolean }) {
  const open = useOpenSession()
  const menu = useSessionMenu()
  const ds = displayState(s)
  const a = activity(s)
  const ended = s.state === 'ended'
  return (
    <Ctx items={menu(s)} className="rounded-xl">
    <button
      onClick={() => open(s.key)}
      className={cx('group flex w-full items-center gap-3 rounded-xl px-3 py-2.5 text-left transition-colors hover:bg-hover/70', ended && 'opacity-60 hover:opacity-100')}
    >
      <ToolMark tool={s.tool} size="sm" />
      <div className="min-w-0 flex-1">
        <div className="flex min-w-0 items-center gap-2">
          <span className="truncate text-sm font-medium text-fg">{sessionTitle(s)}</span>
          {showProject && s.project_name && <span className="shrink-0 text-xs text-fg-3">{s.project_name}</span>}
          <RoleBadge s={s} />
        </div>
        {a && (
          <Ticker id={a.text} className={cx('mt-0.5 h-5 text-[13px] text-fg-3', a.mono && 'font-mono text-[12px]')}>
            {a.icon}
            <span className="truncate">{a.mono ? a.text : <Linkify text={a.text} />}</span>
          </Ticker>
        )}
      </div>
      {/* 一行档只放：标题、身份、最后一句话、设备、状态（R-147） */}
      <Tip content={sessionExtras(s)}>
        <span className="hidden shrink-0 items-center gap-1 text-xs text-fg-3 @lg:flex">
          <Monitor className="size-3.5" />
          {s.device_name}
        </span>
      </Tip>
      <span className="shrink-0">{ended ? <span className="text-xs text-fg-3">{relTime(s.last_active_at, now)}</span> : <StatusPill state={ds} since={s.state_since} now={now} bg={bgCount(s)} />}</span>
    </button>
    </Ctx>
  )
}

// ThreadCard：「要你处理」档的大卡（R-147）。一个会话所有在等你的事：左边是会话、它在问什么、最近两个动作，
// 右边就地作答（决策 / 逐点、验收、转交）。宽处左右两栏，窄处上下堆叠。没有会话的事项（例如无人接手的待验收需求）只显示右边。
export function ThreadCard({ th, now, projectName, sessions }: { th: Thread; now: number; projectName: string; sessions: Session[] }) {
  const open = useOpenSession()
  const menu = useSessionMenu()
  const s = th.s
  const ds = s ? displayState(s) : 'idle'
  const st = stateStyle[ds] ?? stateStyle.idle
  // 决策、转交放右栏就地作答；验收带证据和截图，横跨整张卡放在下面，窄栏里放不开
  const asks = awaitingReply(s)
  const side = th.decisions.length > 0 || th.orphans.length > 0 || asks
  const items = side && (
    <List space={12}>
      {asks && (
        <Item key="reply" space={12} layout>
          <ReplyBox s={s} />
        </Item>
      )}
      {th.decisions.map((d) => (
        <Item key={d.id} space={12} layout>
          <DecisionComposer d={d} embedded />
        </Item>
      ))}
      {th.orphans.map((d) => (
        <Item key={d.id} space={12} layout>
          <OrphanCard d={d} sessions={sessions} />
        </Item>
      ))}
    </List>
  )

  return (
    <Card className={cx('@container overflow-hidden', st.ring || 'border-reply/30 ring-1 ring-reply/25')}>
      <div className={cx('grid grid-cols-1', s && side && '@3xl:grid-cols-[minmax(0,0.85fr)_minmax(0,1.15fr)]')}>
        {s ? (
          <Ctx items={menu(s)} className="rounded-none">
            <div
              role="button"
              tabIndex={0}
              onClick={() => open(s.key)}
              onKeyDown={(e) => e.key === 'Enter' && e.target === e.currentTarget && open(s.key)}
              className="h-full cursor-pointer space-y-3 p-4 transition-colors hover:bg-hover/40 sm:p-5"
            >
              <div className="flex items-start gap-3">
                <ToolMark tool={s.tool} />
                <div className="min-w-0 flex-1">
                  <div className="flex min-w-0 items-center gap-2">
                    <span className="truncate text-[15px] font-semibold text-fg">{sessionTitle(s)}</span>
                    <RoleBadge s={s} />
                    <span className="ml-auto shrink-0 pl-1">
                      <StatusPill state={ds} since={s.state_since} now={now} bg={bgCount(s)} />
                    </span>
                  </div>
                  <div className="mt-1 flex min-w-0 flex-wrap items-center gap-x-2.5 gap-y-1 text-xs text-fg-3">
                    <ReqCount s={s} />
                    <Tip content={sessionExtras(s)}>
                      <span className="inline-flex items-center gap-1">
                        <Monitor className="size-3.5" />
                        {s.device_name}
                      </span>
                    </Tip>
                    {s.branch && (
                      <span className="inline-flex min-w-0 items-center gap-1 font-mono">
                        <GitBranch className="size-3.5 shrink-0" />
                        <span className="truncate">{s.branch}</span>
                        {s.dirty > 0 && <span className="font-sans font-medium text-wait">±{s.dirty}</span>}
                      </span>
                    )}
                  </div>
                </div>
              </div>
              {(s.summary || s.last_prompt) && !(ds === 'waiting_reply' && (s.digest_done || s.digest_ask)) && (
                <p className="line-clamp-2 text-sm leading-relaxed text-fg-2">
                  <Linkify text={s.summary || s.last_prompt} />
                </p>
              )}
              <Callout s={s} />
              {s.last_tool && (
                <div className="rounded-lg bg-surface-2 px-2.5 py-1.5">
                  <LiveFeed s={s} now={now} lines={2} />
                </div>
              )}
            </div>
          </Ctx>
        ) : (
          <div className="flex items-center gap-2 px-4 pt-4 text-[15px] font-semibold text-fg sm:px-5">{projectName || t.thread.noSession}</div>
        )}
        {items && <div className={cx('border-line bg-surface-2/60 p-3 sm:p-4', s ? 'border-t @3xl:border-t-0 @3xl:border-l' : '')}>{items}</div>}
      </div>
      {th.reviews.length > 0 && (
        <div className={cx('bg-surface-2/60 p-3 sm:p-4', (s || items) && 'border-t border-line')}>
          <ReviewList items={th.reviews} advice />
        </div>
      )}
    </Card>
  )
}
