import { useQuery, useQueryClient, type QueryClient } from '@tanstack/react-query'
import { useEffect, useState } from 'react'
import { api, unwrap, type Plan, type Requirement, type Schemas, type Session } from './client'

const normSession = (x: Omit<Session, 'requirements' | 'activity'> & { requirements?: Session['requirements'] | null; activity?: number[] | null }): Session => ({ ...x, requirements: x.requirements ?? [], activity: x.activity ?? [] })
const normReq = (x: Omit<Requirement, 'sessions'> & { sessions?: string[] | null }): Requirement => ({ ...x, sessions: x.sessions ?? [] })
const normPlan = (x: Schemas['Plan']): Plan => ({ ...x, stages: (x.stages ?? []).map((st) => ({ ...st, requirements: st.requirements ?? [] })) })

// 查询键按 SSE 主题组织，便于收到 changed 事件时整组失效。
export const qk = {
  auth: ['auth'] as const,
  sessions: ['sessions'] as const,
  session: (key: string) => ['sessions', 'detail', key] as const,
  requirements: ['requirements'] as const,
  requirement: (ref: string) => ['requirements', 'detail', ref] as const,
  plans: ['plans'] as const,
  devices: ['devices'] as const,
  projects: ['projects'] as const,
  settings: ['settings'] as const,
}

export function useAuthStatus() {
  return useQuery({
    queryKey: qk.auth,
    queryFn: () => unwrap(api.GET('/api/auth/status')),
    staleTime: Infinity,
  })
}

// 会话增量推送（R-56）：Hub 每推一个会话就带一个递增序号。列表接口返回查询开始时的序号；
// 列表请求在途期间收到的、序号更大的推送，在列表回来后重新叠加上去，旧列表不会盖掉新推送。
type RawSessions = Awaited<ReturnType<typeof fetchSessions>>
type RawSession = RawSessions['sessions'] extends (infer T)[] | null | undefined ? T : never
const fetchSessions = () => unwrap(api.GET('/api/sessions'))
const patches = new Map<string, { rev: number; session: RawSession }>()
function upsert(list: RawSession[], s: RawSession): RawSession[] {
  const next = list.some((x) => x.key === s.key) ? list.map((x) => (x.key === s.key ? s : x)) : [s, ...list]
  return next.sort((a, b) => b.last_active_at.localeCompare(a.last_active_at))
}
async function loadSessions(): Promise<RawSessions> {
  const d = await fetchSessions()
  let list = d.sessions ?? []
  for (const [k, p] of patches) {
    if (p.rev <= d.rev) patches.delete(k)
    else list = upsert(list, p.session)
  }
  return { ...d, sessions: list }
}
function applySessionPatch(qc: QueryClient, rev: number, s: RawSession) {
  const cur = patches.get(s.key)
  if (cur && cur.rev >= rev) return // 同一会话只认更新的推送
  patches.set(s.key, { rev, session: s })
  qc.setQueryData<RawSessions>(qk.sessions, (old) => (old ? { ...old, sessions: upsert(old.sessions ?? [], s) } : old))
  void qc.invalidateQueries({ queryKey: qk.session(s.key), exact: true }) // 打开着的会话详情补拉时间线
}

export function useSessions() {
  return useQuery({
    queryKey: qk.sessions,
    queryFn: loadSessions,
    select: (d) => ({ sessions: (d.sessions ?? []).map(normSession), devices: d.devices ?? [] }),
  })
}

export function useSessionDetail(key: string | null) {
  return useQuery({
    queryKey: qk.session(key ?? ''),
    queryFn: () => unwrap(api.GET('/api/sessions/{key}', { params: { path: { key: key! } } })),
    select: (d) => ({ session: normSession(d.session), events: d.events ?? [] }),
    enabled: !!key,
  })
}

export type DecisionStatus = 'pending' | 'answered' | 'delivered' | 'closed'

// 全部决策一次取回，在前端按状态分组（单用户场景数量很小）。
export function useDecisions() {
  return useQuery({
    queryKey: ['decisions', 'all'],
    queryFn: () => unwrap(api.GET('/api/decisions')),
    select: (d) => d.decisions ?? [],
  })
}

export function useRequirements() {
  return useQuery({
    queryKey: qk.requirements,
    queryFn: () => unwrap(api.GET('/api/requirements')),
    select: (d) => (d.requirements ?? []).map(normReq),
  })
}

export function useRequirement(ref: string | null) {
  return useQuery({
    queryKey: qk.requirement(ref ?? ''),
    queryFn: () => unwrap(api.GET('/api/requirements/{ref}', { params: { path: { ref: ref! } } })),
    select: normReq,
    enabled: !!ref,
  })
}

export function usePlans() {
  return useQuery({
    queryKey: qk.plans,
    queryFn: () => unwrap(api.GET('/api/plans')),
    select: (d) => (d.plans ?? []).map(normPlan),
  })
}

export function useDevices() {
  return useQuery({
    queryKey: qk.devices,
    queryFn: () => unwrap(api.GET('/api/devices')),
    select: (d) => d.devices ?? [],
  })
}

export function useProjects() {
  return useQuery({
    queryKey: qk.projects,
    queryFn: () => unwrap(api.GET('/api/projects')),
    select: (d) => ({ projects: d.projects ?? [], workspaces: d.workspaces ?? [] }),
  })
}

export function useSettings() {
  return useQuery({
    queryKey: qk.settings,
    queryFn: () => unwrap(api.GET('/api/settings')),
  })
}

// 主题 → 需要失效的查询前缀。
const topicKeys: Record<string, readonly (readonly string[])[]> = {
  sessions: [['sessions']],
  decisions: [['decisions']],
  requirements: [['requirements'], ['plans']],
  plans: [['plans']],
  devices: [['devices'], ['sessions']],
  projects: [['projects'], ['sessions'], ['requirements'], ['plans']],
}

function invalidateTopics(qc: QueryClient, topics: string[]) {
  for (const topic of topics) {
    for (const key of topicKeys[topic] ?? []) {
      void qc.invalidateQueries({ queryKey: key })
    }
  }
}

// 服务端每 20 秒发一次心跳；超过这么久什么都没收到，就认定连接已经悄悄断了（手机切后台再回来常见）。
const SILENT_MS = 50_000

// useLiveUpdates 订阅 /api/stream，收到 changed 事件时让对应查询失效；断线自动重连。
// 浏览器在切后台、换网络后常常留着一条“看似打开、其实已断”的连接，不报错也不重连（R-65）：
// 心跳超时、回到前台、网络恢复时都主动重连，并在重连后补拉全部数据。
export function useLiveUpdates(enabled: boolean) {
  const qc = useQueryClient()
  const [connected, setConnected] = useState(true)
  useEffect(() => {
    if (!enabled) return
    let es: EventSource | null = null
    let timer: number | undefined
    let stopped = false
    let everConnected = false
    let lastSeen = Date.now()
    const seen = () => {
      lastSeen = Date.now()
    }

    const reconnect = () => {
      if (stopped) return
      es?.close()
      es = null
      if (timer) window.clearTimeout(timer)
      timer = undefined
      connect()
    }
    // 回到前台 / 网络恢复：连接可能早就断了，静默太久就直接重连（重连后会补拉）
    const onWake = () => {
      if (document.visibilityState === 'visible' && Date.now() - lastSeen > 20_000) reconnect()
    }
    const watchdog = window.setInterval(() => {
      if (document.visibilityState === 'visible' && Date.now() - lastSeen > SILENT_MS) reconnect()
    }, 10_000)
    document.addEventListener('visibilitychange', onWake)
    window.addEventListener('online', reconnect)
    window.addEventListener('pageshow', onWake)

    const connect = () => {
      if (stopped) return
      seen()
      es = new EventSource('/api/stream')
      es.addEventListener('ping', seen)
      es.addEventListener('hello', () => {
        seen()
        setConnected(true)
        // 重连后补齐断线期间错过的变化。
        if (everConnected) void qc.invalidateQueries()
        everConnected = true
      })
      es.addEventListener('session', (ev) => {
        seen()
        try {
          const p = JSON.parse((ev as MessageEvent<string>).data) as { rev: number; session: RawSession }
          applySessionPatch(qc, p.rev, p.session)
        } catch {
          void qc.invalidateQueries({ queryKey: qk.sessions })
        }
      })
      es.addEventListener('changed', (ev) => {
        seen()
        let raw = (ev as MessageEvent<string>).data
        try {
          raw = JSON.parse(raw) as string
        } catch {
          // 非 JSON 字符串时按原文处理
        }
        invalidateTopics(qc, String(raw).split(',').map((s) => s.trim()).filter(Boolean))
      })
      es.onerror = () => {
        setConnected(false)
        if (es && es.readyState === EventSource.CLOSED) {
          // 服务端拒绝（例如登录失效）时浏览器不会自动重连：检查登录状态后手动重连。
          es = null
          void qc.invalidateQueries({ queryKey: qk.auth })
          timer = window.setTimeout(connect, 3000)
        }
      }
    }
    connect()
    return () => {
      stopped = true
      if (timer) window.clearTimeout(timer)
      window.clearInterval(watchdog)
      document.removeEventListener('visibilitychange', onWake)
      window.removeEventListener('online', reconnect)
      window.removeEventListener('pageshow', onWake)
      es?.close()
    }
  }, [enabled, qc])
  return connected
}
