import type { ReactNode } from 'react'
import { ChevronRight, FileCheck2, Gauge, GitBranch, MessageSquare, Monitor, RefreshCw } from 'lucide-react'
import type { Session } from '../api/client'
import { t } from '../i18n'
import { cx, relTime, usePref } from '../lib/util'
import { Card, Tip } from '../ui/base'
import { displayState, PhaseBadge, Sparkline, StatusPill, ToolMark, stateStyle } from '../ui/marks'
import { DecisionComposer, OrphanCard } from './DecisionCard'
import type { Thread } from './inbox'
import { useOpenSession } from './nav'
import { ctxLoad } from './assign'
import { Collapse, Item, List } from '../ui/motion'
import { ReviewList } from './review'
import { Callout, ReqCount, sessionTitle, toolIcon } from './SessionCard'
import { Ticker } from './liveFeed'
import { useSessionMenu } from './menus'
import { Ctx } from '../ui/contextMenu'

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
          <PhaseBadge phase={s.phase} />
        </div>
        {a && (
          <Ticker id={a.text} className={cx('mt-0.5 h-5 text-[13px] text-fg-3', a.mono && 'font-mono text-[12px]')}>
            {a.icon}
            <span className="truncate">{a.text}</span>
          </Ticker>
        )}
      </div>
      <span className="hidden shrink-0 items-center gap-1 text-xs text-fg-3 @xl:flex">
        <Monitor className="size-3.5" />
        {s.device_name}
      </span>
      <span className="hidden @2xl:block">
        <ReqCount s={s} />
      </span>
      <Sparkline data={s.activity} state={ds} className="hidden @lg:flex" />
      <span className="shrink-0">{ended ? <span className="text-xs text-fg-3">{relTime(s.last_active_at, now)}</span> : <StatusPill state={ds} since={s.state_since} now={now} />}</span>
    </button>
    </Ctx>
  )
}

// Part：对话块里可折叠的一组事项（折叠状态按对话记在本机）。
function Part({ id, icon, title, count, children }: { id: string; icon: ReactNode; title: string; count: number; children: ReactNode }) {
  const [open, setOpen] = usePref(`ks.part.${id}`, true)
  return (
    <div>
      <button onClick={() => setOpen(!open)} className="mb-2 flex items-center gap-1.5 rounded-md text-xs font-medium text-fg-3 hover:text-fg-2">
        <ChevronRight className={cx('size-3.5 transition-transform', open && 'rotate-90')} />
        {icon}
        {title}
        <span className="tabular">{count}</span>
      </button>
      <Collapse show={open}>{children}</Collapse>
    </div>
  )
}

// ThreadCard：一个需要你处理的对话，汇集它所有在等你的事。
export function ThreadCard({ th, now, projectName, sessions }: { th: Thread; now: number; projectName: string; sessions: Session[] }) {
  const open = useOpenSession()
  const menu = useSessionMenu()
  const s = th.s
  const ds = s ? displayState(s) : 'idle'
  const st = stateStyle[ds] ?? stateStyle.idle

  return (
    <Card className={cx('paper-fold overflow-hidden rounded-tr-md', st.ring, ds === 'running' && 'live-edge')}>
      {s ? (
        <Ctx items={menu(s)} className="rounded-none">
        <div
          role="button"
          tabIndex={0}
          onClick={() => open(s.key)}
          onKeyDown={(e) => e.key === 'Enter' && e.target === e.currentTarget && open(s.key)}
          className="cursor-pointer space-y-2.5 p-4 transition-colors hover:bg-hover/40"
        >
          <div className="flex items-start gap-3">
            <ToolMark tool={s.tool} />
            <div className="min-w-0 flex-1">
              <div className="flex min-w-0 items-center gap-2">
                <span className="truncate text-[15px] font-semibold text-fg">{sessionTitle(s)}</span>
                <PhaseBadge phase={s.phase} />
                <span className="ml-auto shrink-0 pl-1">
                  <StatusPill state={ds} since={s.state_since} now={now} />
                </span>
              </div>
              <div className="mt-1 flex min-w-0 flex-wrap items-center gap-x-2.5 gap-y-1 text-xs text-fg-3">
                <span className="inline-flex items-center gap-1">
                  <Monitor className="size-3.5" />
                  {s.device_name}
                </span>
                {(() => {
                  const c = s.state !== 'ended' && ctxLoad(s)
                  return (
                    c &&
                    c.level !== 'ok' && (
                      <Tip content={c.level === 'high' ? t.session.ctxHeavyTip(c.label) : t.session.ctxTip(c.label)}>
                        <span className={cx('inline-flex items-center gap-1', c.level === 'high' ? 'text-danger' : 'text-wait')}>
                          <Gauge className="size-3.5" />
                          {t.session.ctx(c.label)}
                        </span>
                      </Tip>
                    )
                  )
                })()}
                {s.tools_stale && s.state !== 'ended' && (
                  <Tip content={t.session.toolsStaleTip(s.tools_stale)}>
                    <span className="inline-flex items-center gap-1 text-wait">
                      <RefreshCw className="size-3.5" />
                      {t.session.toolsStale}
                    </span>
                  </Tip>
                )}
                {s.branch && (
                  <span className="inline-flex min-w-0 items-center gap-1 font-mono">
                    <GitBranch className="size-3.5 shrink-0" />
                    <span className="truncate">{s.branch}</span>
                    {s.dirty > 0 && <span className="font-sans font-medium text-wait">±{s.dirty}</span>}
                  </span>
                )}
                <ReqCount s={s} />
              </div>
            </div>
          </div>
          {(s.summary || s.last_prompt) && !(ds === 'waiting_reply' && (s.digest_done || s.digest_ask)) && (
            <p className="line-clamp-2 text-sm leading-relaxed text-fg-2">{s.summary || s.last_prompt}</p>
          )}
          <Callout s={s} />
        </div>
        </Ctx>
      ) : (
        <div className="flex items-center gap-2 px-4 pt-4 text-[15px] font-semibold text-fg">{projectName || t.thread.noSession}</div>
      )}

      <Collapse show={th.decisions.length > 0 || th.reviews.length > 0 || th.orphans.length > 0}>
        <div className="@container border-t border-dashed border-line-strong bg-surface-2/60 p-3 sm:p-4">
          <List space={16}>
            {th.decisions.length > 0 && (
              <Item key="decisions" space={16}>
                <List className={cx('grid grid-cols-1 items-start gap-x-3', th.decisions.length > 1 && '@3xl:grid-cols-2')}>
                  {th.decisions.map((d) => (
                    <Item key={d.id} space={12} layout>
                      <DecisionComposer d={d} embedded />
                    </Item>
                  ))}
                </List>
              </Item>
            )}
            {th.reviews.length > 0 && (
              <Item key="reviews" space={16}>
                <Part id={`${th.key}.reviews`} icon={<FileCheck2 className="size-3.5" />} title={t.thread.reviews} count={th.reviews.length}>
                  <ReviewList items={th.reviews} scroll />
                </Part>
              </Item>
            )}
            {th.orphans.length > 0 && (
              <Item key="orphans" space={16}>
                <List className="grid grid-cols-1 gap-x-3 @3xl:grid-cols-2">
                  {th.orphans.map((d) => (
                    <Item key={d.id} space={12} layout>
                      <OrphanCard d={d} sessions={sessions} />
                    </Item>
                  ))}
                </List>
              </Item>
            )}
          </List>
        </div>
      </Collapse>
    </Card>
  )
}
