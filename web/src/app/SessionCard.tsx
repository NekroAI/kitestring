import { memo } from 'react'
import { Bot, FileCode2, FileText, Globe, GitBranch, Layers, MessageSquareText, Monitor, Plug, Search, ShieldAlert, Terminal, Wrench, type LucideIcon } from 'lucide-react'
import type { Session } from '../api/client'
import { t } from '../i18n'
import { cx, relTime, shortPath } from '../lib/util'
import { Badge, Card } from '../ui/base'
import { Sparkline, StatusPill, ToolMark, stateStyle } from '../ui/marks'
import { useOpenReq, useOpenSession } from './nav'

// 工具名 → 图标。
export function toolIcon(label: string): LucideIcon {
  const name = label.split(' · ')[0]
  if (/^(Bash|shell|exec_command|local_shell)$/.test(name)) return Terminal
  if (/^(Edit|Write|MultiEdit|NotebookEdit|apply_patch)$/.test(name)) return FileCode2
  if (name === 'Read') return FileText
  if (/^(Grep|Glob)$/.test(name)) return Search
  if (/^Web/.test(name)) return Globe
  if (/^(Task|Agent)$/.test(name)) return Bot
  if (name.includes('__')) return Plug
  return Wrench
}

function ActionLine({ s, now }: { s: Session; now: number }) {
  if (s.state === 'waiting_approval') {
    return (
      <div className="flex items-start gap-2 rounded-xl bg-wait-soft px-3 py-2 text-[13px]">
        <ShieldAlert className="mt-px size-4 shrink-0 text-wait" />
        <span className="min-w-0 flex-1 font-mono text-[12px] leading-relaxed break-all text-fg">{s.state_detail || t.session.waitingApproval}</span>
      </div>
    )
  }
  if (!s.last_tool) return null
  const Icon = toolIcon(s.last_tool)
  const [name, ...rest] = s.last_tool.split(' · ')
  const live = s.state === 'running'
  return (
    <div className={cx('flex items-center gap-2 rounded-xl px-3 py-2 text-[13px]', live ? 'bg-surface-2' : 'bg-transparent px-0 py-0')}>
      <Icon className={cx('size-4 shrink-0', live ? 'text-run' : 'text-fg-3')} />
      <span className="shrink-0 font-medium text-fg-2">{name}</span>
      {rest.length > 0 && <span className="min-w-0 flex-1 truncate font-mono text-[12px] text-fg-2">{rest.join(' · ')}</span>}
      <span className="ml-auto shrink-0 text-xs text-fg-3 tabular">{relTime(s.last_tool_at, now)}</span>
    </div>
  )
}

// SessionCard：实例卡片。一眼回答：它在做什么、做了多久、顺不顺利、要不要我管。
export const SessionCard = memo(function SessionCard({ s, now, showProject = true, showDevice = true }: { s: Session; now: number; showProject?: boolean; showDevice?: boolean }) {
  const open = useOpenSession()
  const openReq = useOpenReq()
  const st = stateStyle[s.state] ?? stateStyle.idle
  const headline = s.summary || s.last_prompt
  const ended = s.state === 'ended'
  const perm = t.permMode[s.permission_mode] ?? ''

  return (
    <Card
      role="button"
      tabIndex={0}
      onClick={() => open(s.key)}
      onKeyDown={(e) => e.key === 'Enter' && open(s.key)}
      className={cx('group flex cursor-pointer flex-col gap-3 p-4 transition-all duration-200 hover:-translate-y-px hover:shadow-pop', st.ring, ended && 'opacity-70 hover:opacity-100')}
    >
      {/* 头部：工具、项目、状态 */}
      <div className="flex items-start gap-3">
        <ToolMark tool={s.tool} />
        <div className="min-w-0 flex-1">
          <div className="flex items-center gap-1.5 text-[15px] font-semibold text-fg">
            <span className="truncate">{showProject ? s.project_name || shortPath(s.cwd, 28) || t.sessions.noProject : s.model || t.tool[s.tool as 'claude']}</span>
          </div>
          <div className="mt-0.5 flex items-center gap-1.5 text-xs text-fg-3">
            {showProject && s.model && <span className="truncate font-mono">{s.model}</span>}
            {showDevice && (
              <>
                {showProject && s.model && <span>·</span>}
                <span className="inline-flex shrink-0 items-center gap-1">
                  <Monitor className="size-3" />
                  {s.device_name}
                </span>
              </>
            )}
            {!showDevice && !showProject && <span className="truncate">{s.client}</span>}
          </div>
        </div>
        <StatusPill state={s.state} since={s.state_since} now={now} />
      </div>

      {/* 主体：在做什么 */}
      {headline ? (
        <div>
          <p className={cx('text-sm leading-relaxed text-fg', s.summary ? 'line-clamp-2' : 'line-clamp-2 text-fg-2')}>
            {!s.summary && <MessageSquareText className="mr-1.5 mb-0.5 inline size-3.5 text-fg-3" />}
            {headline}
          </p>
          {s.summary && s.last_prompt && (
            <p className="mt-1 line-clamp-1 text-[13px] text-fg-3">
              <span className="mr-1 font-medium">{t.sessions.you}</span>
              {s.last_prompt}
            </p>
          )}
        </div>
      ) : null}

      <ActionLine s={s} now={now} />

      {/* 进度指标 */}
      <div className="flex flex-wrap items-center gap-x-3 gap-y-1.5 text-xs text-fg-3">
        {s.branch && (
          <span className="inline-flex min-w-0 items-center gap-1">
            <GitBranch className="size-3.5 shrink-0" />
            <span className="max-w-[160px] truncate font-mono">{s.branch}</span>
            {s.dirty > 0 && <span className="font-medium text-wait tabular">±{s.dirty}</span>}
          </span>
        )}
        {s.worktree && <Badge tone="outline">{t.sessions.worktree}</Badge>}
        {s.turns > 0 && <span className="tabular">{t.sessions.turn(s.turns)}</span>}
        {s.turn_tools > 0 && !ended && <span className="tabular">{t.sessions.toolCalls(s.turn_tools)}</span>}
        {s.subagents > 0 && (
          <span className="inline-flex items-center gap-1 text-accent">
            <Layers className="size-3.5" />
            {t.sessions.subagents(s.subagents)}
          </span>
        )}
        {perm && <Badge tone="outline">{perm}</Badge>}
        {s.compactions > 0 && <span className="tabular">{t.sessions.compactions(s.compactions)}</span>}
      </div>

      {/* 底部：需求、决策、活跃度 */}
      <div className="-mb-0.5 flex items-center gap-2 border-t border-line pt-3">
        <div className="flex min-w-0 flex-1 flex-wrap items-center gap-1.5">
          {s.pending_decisions > 0 && <Badge tone="wait">{t.sessions.pendingDecisions(s.pending_decisions)}</Badge>}
          {s.requirements
            .filter((r) => r.status !== 'done' && r.status !== 'shelved')
            .map((r) => (
            <button
              key={r.id}
              onClick={(e) => {
                e.stopPropagation()
                openReq(r.code)
              }}
              className="inline-flex max-w-[220px] items-center gap-1 rounded-md bg-accent-soft px-1.5 py-0.5 text-xs text-accent hover:brightness-95"
            >
              <span className="shrink-0 font-mono text-[11px] font-semibold whitespace-nowrap">{r.code}</span>
              <span className="truncate">{r.title}</span>
            </button>
            ))}
          {!s.requirements.length && !s.pending_decisions && <span className="text-xs text-fg-3">{relTime(s.last_active_at, now)}</span>}
        </div>
        <Sparkline data={s.activity} state={s.state} />
      </div>
    </Card>
  )
})
