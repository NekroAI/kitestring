import { useEffect, useSyncExternalStore } from 'react'
import { AnimatePresence, motion } from 'motion/react'
import type { Session } from '../api/client'
import { cx, relTime } from '../lib/util'
import { splitTool, toolIcon } from './SessionCard'

// 运行中会话的实时动作流：记下页面打开以来每个会话最近的几次工具调用（来自实时推送的会话更新），
// 在卡片上滚动展示，让人一眼看出它确实在动。只存在内存里，刷新页面后从最新一条重新开始。

type Act = { label: string; at: string }
const feeds = new Map<string, Act[]>()
const listeners = new Set<() => void>()
const KEEP = 4

function push(key: string, label: string, at: string) {
  const cur = feeds.get(key) ?? []
  if (cur.length && cur[cur.length - 1].at === at) return
  feeds.set(key, [...cur, { label, at }].slice(-KEEP))
  listeners.forEach((l) => l())
}

function subscribe(fn: () => void) {
  listeners.add(fn)
  return () => listeners.delete(fn)
}

// useToolFeed：会话最近的动作，旧 → 新。
export function useToolFeed(s: Session): Act[] {
  useEffect(() => {
    if (s.last_tool && s.last_tool_at) push(s.key, s.last_tool, s.last_tool_at)
  }, [s.key, s.last_tool, s.last_tool_at])
  return useSyncExternalStore(subscribe, () => feeds.get(s.key) ?? EMPTY)
}
const EMPTY: Act[] = []

function Line({ a, now, dim }: { a: Act; now: number; dim: number }) {
  const Icon = toolIcon(a.label)
  const [name, brief] = splitTool(a.label)
  return (
    <motion.div
      layout="position"
      initial={{ opacity: 0, y: 14 }}
      animate={{ opacity: dim, y: 0 }}
      exit={{ opacity: 0, y: -14 }}
      transition={{ duration: 0.32, ease: [0.2, 0.8, 0.2, 1] }}
      className="flex h-[22px] min-w-0 items-center gap-2 text-[13px]"
    >
      <Icon className="size-3.5 shrink-0 text-run" />
      <span className="shrink-0 font-medium text-fg-2">{name}</span>
      {brief && <span className="min-w-0 flex-1 truncate font-mono text-[12px] text-fg-3">{brief}</span>}
      <span className="ml-auto shrink-0 pl-2 text-[11px] text-fg-3 tabular">{relTime(a.at, now)}</span>
    </motion.div>
  )
}

// LiveFeed：最近几次动作，新的从下面滚上来，旧的往上淡出。
export function LiveFeed({ s, now, lines = 3, className }: { s: Session; now: number; lines?: number; className?: string }) {
  const feed = useToolFeed(s).slice(-lines)
  const dims = [0.4, 0.65, 1].slice(-feed.length)
  return (
    <div className={cx('relative overflow-hidden', className)} style={{ height: lines * 22 }}>
      <div className="absolute inset-x-0 bottom-0 flex flex-col justify-end">
        <AnimatePresence initial={false} mode="popLayout">
          {feed.map((a, i) => (
            <Line key={a.at} a={a} now={now} dim={dims[i] ?? 1} />
          ))}
        </AnimatePresence>
      </div>
    </div>
  )
}

// Ticker：一行，内容变化时向上滚动切换。
export function Ticker({ id, children, className }: { id: string; children: React.ReactNode; className?: string }) {
  return (
    <div className={cx('relative overflow-hidden', className)}>
      <AnimatePresence initial={false} mode="popLayout">
        <motion.div key={id} initial={{ y: '100%', opacity: 0 }} animate={{ y: 0, opacity: 1 }} exit={{ y: '-100%', opacity: 0 }} transition={{ duration: 0.3, ease: [0.2, 0.8, 0.2, 1] }} className="flex min-w-0 items-center gap-1.5">
          {children}
        </motion.div>
      </AnimatePresence>
    </div>
  )
}
