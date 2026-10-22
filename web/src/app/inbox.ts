import { useMemo } from 'react'
import { useDecisions, useRequirements, useSessions } from '../api/queries'

// useInbox 汇总所有“在等你”的事，供首页和导航角标共用。
export function useInbox() {
  const sessions = useSessions()
  const decisions = useDecisions()
  const reqs = useRequirements()

  return useMemo(() => {
    const ss = sessions.data?.sessions ?? []
    const ds = decisions.data ?? []
    const byKey = new Map(ss.map((s) => [s.key, s]))
    const byOldest = <T extends { state_since?: string }>(a: T, b: T) => (a.state_since ?? '').localeCompare(b.state_since ?? '')

    const pending = ds.filter((d) => d.status === 'pending').sort((a, b) => a.created_at.localeCompare(b.created_at))
    const approvals = ss.filter((s) => s.state === 'waiting_approval').sort(byOldest)
    const replies = ss.filter((s) => s.state === 'waiting_reply' && !s.pending_decisions).sort(byOldest)
    const reviews = (reqs.data ?? []).filter((r) => r.status === 'review')
    const orphans = ds.filter((d) => {
      if (d.status !== 'answered') return false
      const s = d.session_key ? byKey.get(d.session_key) : undefined
      return !s || s.state === 'ended' || s.state === 'lost'
    })
    const running = ss.filter((s) => s.state === 'running')
    const delivered = ds
      .filter((d) => d.status === 'delivered' && d.delivered_at)
      .sort((a, b) => (b.delivered_at ?? '').localeCompare(a.delivered_at ?? ''))
      .slice(0, 5)

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
      pending,
      approvals,
      replies,
      reviews,
      orphans,
      running,
      delivered,
      // 导航角标只计真正阻塞 agent 的事：决策、授权、验收、需转交。
      urgent: pending.length + approvals.length + reviews.length + orphans.length,
      total: pending.length + approvals.length + reviews.length + orphans.length + replies.length,
    }
  }, [sessions, decisions, reqs])
}
