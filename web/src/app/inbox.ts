import { useMemo } from 'react'
import type { Decision, Requirement, Session } from '../api/client'
import { useDecisions, useRequirements, useSessions } from '../api/queries'
import { displayState } from '../ui/marks'

// 工作台数据模型：项目 → 对话。
// 一个“线程”是一个需要你处理的对话，汇集它所有在等你的事：决策、待验收、待转交、提问 / 授权 / 回复。

export type Thread = {
  key: string
  s?: Session // 没有会话时为项目级（例如需求未关联任何会话）
  decisions: Decision[]
  reviews: Requirement[]
  orphans: Decision[]
  urgency: number
  items: number // 计入“要你处理”的数量
}

export type ProjectGroup = {
  key: string
  name: string
  threads: Thread[]
  active: Session[] // 不需要你处理、仍在进行的会话
  ended: Session[]
  urgency: number
  lastActive: string
}

// 越小越紧急：有决策 / 授权 > 提问 > 待验收 / 待转交 > 等你回复。
function sessionUrgency(s: Session): number {
  const ds = displayState(s)
  if (ds === 'waiting_approval') return 1
  if (ds === 'waiting_answer' || ds === 'parked') return 2
  if (ds === 'waiting_reply') return 5
  return 9
}

const activeRank: Record<string, number> = { running: 0, background: 1, idle: 2, lost: 3 }

export function useWorkbench() {
  const sessions = useSessions()
  const decisions = useDecisions()
  const reqs = useRequirements()

  return useMemo(() => {
    const ss = sessions.data?.sessions ?? []
    const ds = decisions.data ?? []
    const rs = reqs.data ?? []
    const byKey = new Map(ss.map((s) => [s.key, s]))
    const projectOf = (s?: Session) => (s ? s.project_id || `cwd:${s.cwd}` : '')
    const threads = new Map<string, Thread>()
    const projectNames = new Map<string, string>()
    const threadProject = new Map<string, string>()

    const thread = (key: string, project: string, s?: Session) => {
      let th = threads.get(key)
      if (!th) {
        th = { key, s, decisions: [], reviews: [], orphans: [], urgency: s ? sessionUrgency(s) : 9, items: 0 }
        threads.set(key, th)
        threadProject.set(key, project)
      }
      return th
    }
    const projectThread = (pid: string, name: string) => {
      projectNames.set(pid || 'none', name)
      return thread(`proj:${pid || 'none'}`, pid || 'none')
    }

    // 待决策
    for (const d of ds.filter((x) => x.status === 'pending').sort((a, b) => a.created_at.localeCompare(b.created_at))) {
      const s = d.session_key ? byKey.get(d.session_key) : undefined
      const th = s ? thread(s.key, projectOf(s), s) : projectThread(d.project_id ?? '', d.project_name ?? '')
      th.decisions.push(d)
      th.urgency = Math.min(th.urgency, 0)
    }
    // 待验收：归到最近活跃的关联会话
    for (const r of rs.filter((x) => x.status === 'review')) {
      const linked = r.sessions.map((k) => byKey.get(k)).filter((x): x is Session => !!x)
      linked.sort((a, b) => b.last_active_at.localeCompare(a.last_active_at))
      const s = linked.find((x) => x.state !== 'ended') ?? linked[0]
      const th = s ? thread(s.key, projectOf(s), s) : projectThread(r.project_id ?? '', r.project_name ?? '')
      th.reviews.push(r)
      th.urgency = Math.min(th.urgency, 3)
    }
    // 待转交：已答复但目标会话已结束
    for (const d of ds.filter((x) => x.status === 'answered')) {
      const s = d.session_key ? byKey.get(d.session_key) : undefined
      if (s && s.state !== 'ended' && s.state !== 'lost') continue
      const th = s ? thread(s.key, projectOf(s), s) : projectThread(d.project_id ?? '', d.project_name ?? '')
      th.orphans.push(d)
      th.urgency = Math.min(th.urgency, 4)
    }
    // 自身在等你的会话（授权 / 提问 / 回复）
    for (const s of ss) {
      if (s.state !== 'ended' && sessionUrgency(s) < 9) thread(s.key, projectOf(s), s)
    }
    for (const th of threads.values()) {
      th.items = th.decisions.length + th.reviews.length + th.orphans.length + (th.s && th.s.state !== 'ended' && sessionUrgency(th.s) < 9 ? 1 : 0)
    }

    // 组装项目
    const groups = new Map<string, ProjectGroup>()
    const group = (key: string, name: string) => {
      let g = groups.get(key)
      if (!g) {
        g = { key, name, threads: [], active: [], ended: [], urgency: 9, lastActive: '' }
        groups.set(key, g)
      }
      if (!g.name && name) g.name = name
      return g
    }
    for (const s of ss) {
      const g = group(projectOf(s), s.project_name ?? "")
      if (s.last_active_at > g.lastActive) g.lastActive = s.last_active_at
      if (threads.has(s.key)) continue
      if (s.state === 'ended' || displayState(s) === 'done') g.ended.push(s)
      else g.active.push(s)
    }
    for (const th of threads.values()) {
      const pk = threadProject.get(th.key) ?? 'none'
      const g = group(pk, th.s?.project_name || projectNames.get(pk) || '')
      g.threads.push(th)
      g.urgency = Math.min(g.urgency, th.urgency)
    }
    for (const g of groups.values()) {
      g.threads.sort((a, b) => a.urgency - b.urgency || (b.s?.last_active_at ?? '').localeCompare(a.s?.last_active_at ?? ''))
      g.active.sort((a, b) => (activeRank[a.state] ?? 9) - (activeRank[b.state] ?? 9) || b.last_active_at.localeCompare(a.last_active_at))
      g.ended.sort((a, b) => b.last_active_at.localeCompare(a.last_active_at))
    }
    const projects = [...groups.values()].sort((a, b) => a.urgency - b.urgency || b.lastActive.localeCompare(a.lastActive))
    const all = [...threads.values()]

    return {
      loading: sessions.isPending || decisions.isPending || reqs.isPending,
      error: sessions.error || decisions.error || reqs.error,
      refetch: () => {
        void sessions.refetch()
        void decisions.refetch()
        void reqs.refetch()
      },
      sessions: ss,
      devices: sessions.data?.devices ?? [],
      decisions: ds,
      projects,
      threads: all,
      running: ss.filter((s) => s.state === 'running' || s.state === 'background'),
      todo: all.reduce((n, th) => n + th.items, 0),
    }
  }, [sessions, decisions, reqs])
}
