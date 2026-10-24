import type { ReactNode } from 'react'
import { MessageSquare, Monitor } from 'lucide-react'
import type { Session } from '../api/client'
import { cx, relTime } from '../lib/util'
import { displayState, RoleBadge, StatusPill, ToolMark } from '../ui/marks'
import { useOpenSession } from './nav'
import { sessionTitle, toolIcon } from './SessionCard'
import { Ticker } from './liveFeed'
import { useSessionMenu } from './menus'
import { Ctx } from '../ui/contextMenu'
import { Linkify } from '../ui/refmark'
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
  const fade = ended && 'opacity-60 transition-opacity group-hover:opacity-100'
  return (
    <Ctx items={menu(s)} className="rounded-xl">
    <button
      onClick={() => open(s.key)}
      className="group flex w-full items-center gap-3 rounded-xl px-3 py-2.5 text-left transition-colors hover:bg-hover/70"
    >
      {/* 结束的会话整行变淡，但工具图标不跟着淡，一眼认出是 Claude 还是 Codex（R-187） */}
      <ToolMark tool={s.tool} size="sm" />
      <div className={cx('min-w-0 flex-1', fade)}>
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
      <span className={cx('hidden shrink-0 items-center gap-1 text-xs text-fg-3 @lg:flex', fade)}>
        <Monitor className="size-3.5" />
        {s.device_name}
      </span>
      <span className={cx('shrink-0', fade)}>{ended ? <span className="text-xs text-fg-3">{relTime(s.last_active_at, now)}</span> : <StatusPill state={ds} since={s.state_since} now={now} bg={bgCount(s)} />}</span>
    </button>
    </Ctx>
  )
}
