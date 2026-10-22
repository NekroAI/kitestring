import { useMemo } from 'react'
import type { Decision, Session } from '../api/client'
import { useDecisions, useRequirements, useSessions } from '../api/queries'
import { displayState } from '../ui/marks'

// 需要你处理的会话优先级：有待决策 > 等你授权 > 等你回答 > 等你回复。
function urgency(s: Session, pending: number): number {
  if (pending) return 0
  const ds = displayState(s)
  if (ds === 'waiting_approval') return 1
  if (ds === 'waiting_answer') return 2
  if (ds === 'waiting_reply') return 3
  return 9
}

const activeRank: Record<string, number> = { running: 0, background: 1, idle: 2, lost: 3 }

// useWorkbench 汇总工作台数据；导航角标与页面使用同一套计数。
export function useWorkbench() {
  const sessions = useSessions()
  const decisions = useDecisions()
  const reqs = useRequirements()

  return useMemo(() => {
    const ss = sessions.data?.sessions ?? []
    const ds = decisions.data ?? []
    const byKey = new Map(ss.map((s) => [s.key, s]))

    const pendingBySession = new Map<string, Decision[]>()
    const looseDecisions: Decision[] = [] // 没有对应会话的待决策（例如从命令行提出）
    for (const d of ds.filter((x) => x.status === 'pending').sort((a, b) => a.created_at.localeCompare(b.created_at))) {
      if (d.session_key && byKey.has(d.session_key)) pendingBySession.set(d.session_key, [...(pendingBySession.get(d.session_key) ?? []), d])
      else looseDecisions.push(d)
    }

    const attention = ss
      .map((s) => ({ s, decisions: pendingBySession.get(s.key) ?? [], u: urgency(s, pendingBySession.get(s.key)?.length ?? 0) }))
      .filter((x) => x.u < 9 && x.s.state !== 'ended')
      .sort((a, b) => a.u - b.u || (a.s.state_since ?? '').localeCompare(b.s.state_since ?? ''))
    const attentionKeys = new Set(attention.map((x) => x.s.key))
    // 已结束但仍有待决策的会话也要出现在“要你处理”里。
    for (const s of ss) {
      if (!attentionKeys.has(s.key) && pendingBySession.has(s.key)) {
        attention.push({ s, decisions: pendingBySession.get(s.key)!, u: 0 })
        attentionKeys.add(s.key)
      }
    }

    const reviews = (reqs.data ?? []).filter((r) => r.status === 'review')
    const orphans = ds.filter((d) => {
      if (d.status !== 'answered') return false
      const s = d.session_key ? byKey.get(d.session_key) : undefined
      return !s || s.state === 'ended' || s.state === 'lost'
    })
    const active = ss.filter((s) => !attentionKeys.has(s.key) && s.state !== 'ended').sort((a, b) => (activeRank[a.state] ?? 9) - (activeRank[b.state] ?? 9) || b.last_active_at.localeCompare(a.last_active_at))
    const ended = ss.filter((s) => !attentionKeys.has(s.key) && s.state === 'ended')
    const running = ss.filter((s) => s.state === 'running' || s.state === 'background')

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
      attention,
      looseDecisions,
      reviews,
      orphans,
      active,
      ended,
      running,
      todo: attention.length + looseDecisions.length + reviews.length + orphans.length,
    }
  }, [sessions, decisions, reqs])
}
