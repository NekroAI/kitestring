import { useState } from 'react'
import { ChevronRight } from 'lucide-react'
import type { Session } from '../api/client'
import { t } from '../i18n'
import { cx, shortDuration, useMedia } from '../lib/util'
import { Tip } from '../ui/base'
import { splitTool } from './SessionCard'

// 后台任务子泳道（R-162）：会话下在跑的后台 shell 与子代理。明显从属于主会话——
// 缩进挂在会话下面、树状连线，不放状态灯与波纹，刻度细而灰，字号与颜色都比会话低一级；类型只用文字小标。

export type BgTask = NonNullable<Session['tasks']>[number]

const WINDOW = 10 * 60 * 1000 // 时间线：近 10 分钟，右边是现在

export const liveTasks = (s: Session): BgTask[] => (s.state === 'ended' ? [] : (s.tasks ?? []))

// Codex 经 app-server / 桌面 App 跑时一个进程承载多个会话，分不清后台 shell 归属
export const tasksUnknown = (s: Session) => s.tool === 'codex' && (s.client === 'app-server' || s.client === 'desktop')

// bgCount：状态胶囊里“后台 n 个任务”的 n
export const bgCount = (s: Session) => liveTasks(s).length || s.subagents

function Ticks({ task, now }: { task: BgTask; now: number }) {
  const x = (iso: string) => Math.max(0, Math.min(120, 120 - ((now - Date.parse(iso)) / WINDOW) * 120))
  const from = x(task.started_at)
  return (
    <svg viewBox="0 0 120 10" preserveAspectRatio="none" className="h-2.5 w-full text-fg-3" aria-hidden>
      <line x1="0" y1="5" x2="120" y2="5" stroke="currentColor" strokeOpacity="0.25" strokeWidth="1" vectorEffect="non-scaling-stroke" />
      {/* 在跑的那一段稍深一点 */}
      <line x1={from} y1="5" x2="120" y2="5" stroke="currentColor" strokeOpacity="0.5" strokeWidth="1" vectorEffect="non-scaling-stroke" />
      {(task.ticks ?? []).map((at, i) => {
        const tx = x(at)
        return <line key={i} x1={tx} y1="1.5" x2={tx} y2="8.5" stroke="currentColor" strokeOpacity="0.7" strokeWidth="1" vectorEffect="non-scaling-stroke" />
      })}
    </svg>
  )
}

function Lane({ task, now, last, full }: { task: BgTask; now: number; last: boolean; full?: boolean }) {
  const label = task.label || task.agent_type || t.session.taskKind[task.kind]
  const [tool, brief] = task.last_action ? splitTool(task.last_action) : ['', '']
  return (
    <div className="flex min-w-0 items-center gap-2 py-[3px] text-[12px] leading-5">
      {/* 树状连线：竖线接上一行，横线指向这一行；最后一行竖线只到中间 */}
      <span className="relative w-3 shrink-0 self-stretch" aria-hidden>
        <span className={cx('absolute left-0 top-0 border-l border-line', last ? 'h-1/2' : 'h-full')} />
        <span className="absolute top-1/2 left-0 w-full border-t border-line" />
      </span>
      <span className="w-9 shrink-0 text-[11px] text-fg-3">{t.session.taskKind[task.kind]}</span>
      <Tip truncated content={[label, task.kind === 'agent' && task.agent_type && task.label && task.agent_type].filter(Boolean).join(t.common.sep)}>
        <span className={cx('min-w-0 truncate text-fg-2', full ? 'flex-1' : 'flex-1 @lg:max-w-[38%] @lg:flex-none')}>
          {label}
          {task.kind === 'agent' && task.agent_type && task.label && <span className="ml-1.5 text-[11px] text-fg-3">{task.agent_type}</span>}
        </span>
      </Tip>
      <span className={cx('shrink-0', full ? 'hidden w-24 sm:block' : 'hidden w-16 @xs:block @md:w-24 @lg:w-28')}>
        <Ticks task={task} now={now} />
      </span>
      {!full && (
        <span className="hidden min-w-0 flex-1 truncate font-mono text-[11px] text-fg-3 @lg:block">{tool && `${tool}${brief ? ' ' + brief : ''}`}</span>
      )}
      <span className="ml-auto shrink-0 text-[11px] text-fg-3 tabular">{shortDuration(task.started_at, now)}</span>
    </div>
  )
}

function counts(tasks: BgTask[]) {
  const sh = tasks.filter((x) => x.kind === 'shell').length
  return t.session.taskCounts(sh, tasks.length - sh)
}

// TaskLanes：「在跑」中卡底下的子泳道。手机上收成一行计数，点开看列表。
export function TaskLanes({ s, now }: { s: Session; now: number }) {
  const phone = useMedia('(max-width: 767px)')
  const [open, setOpen] = useState(false)
  const tasks = liveTasks(s)
  if (!tasks.length) return null
  if (phone && !open)
    return (
      <button
        onClick={(e) => {
          e.stopPropagation()
          setOpen(true)
        }}
        className="-mt-1 flex w-full min-w-0 items-center gap-1.5 pl-1 text-left text-[12px] text-fg-3"
      >
        <span className="relative h-3 w-3 shrink-0" aria-hidden>
          <span className="absolute top-0 left-0 h-1/2 border-l border-line" />
          <span className="absolute top-1/2 left-0 w-full border-t border-line" />
        </span>
        <span className="truncate">{counts(tasks)}</span>
        <ChevronRight className="size-3.5 shrink-0" />
      </button>
    )
  return (
    <div className="@container -mt-1 pl-1" onClick={phone ? (e) => e.stopPropagation() : undefined}>
      {tasks.map((x, i) => (
        <Lane key={x.id} task={x} now={now} last={i === tasks.length - 1} />
      ))}
      {phone && (
        <button onClick={() => setOpen(false)} className="mt-0.5 pl-5 text-[11px] text-fg-3">
          {t.common.collapse}
        </button>
      )}
    </div>
  )
}

// TaskList：会话面板里的完整列表；识别不了的会话说明原因，免得以为没有后台任务。
export function TaskList({ s, now }: { s: Session; now: number }) {
  const tasks = liveTasks(s)
  if (!tasks.length) return tasksUnknown(s) && s.state !== 'ended' ? <p className="text-xs text-fg-3">{t.session.tasksUnknown}</p> : null
  return (
    <div className="pl-1">
      {tasks.map((x, i) => (
        <div key={x.id}>
          <Lane task={x} now={now} last={i === tasks.length - 1} full />
          {x.last_action && (
            <div className="flex min-w-0">
              <span className="relative w-3 shrink-0" aria-hidden>
                {i < tasks.length - 1 && <span className="absolute top-0 left-0 h-full border-l border-line" />}
              </span>
              <span className="min-w-0 truncate pb-0.5 pl-[3.25rem] font-mono text-[11px] leading-4 text-fg-3">{x.last_action}</span>
            </div>
          )}
        </div>
      ))}
      <p className="mt-1.5 pl-16 text-[11px] text-fg-3">{t.session.tasksHint}</p>
    </div>
  )
}
