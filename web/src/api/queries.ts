import { useQuery, useQueryClient, type QueryClient } from '@tanstack/react-query'
import { useEffect, useState } from 'react'
import { api, unwrap, type Requirement, type Session } from './client'

const normSession = (x: Omit<Session, 'requirements' | 'activity'> & { requirements?: Session['requirements'] | null; activity?: number[] | null }): Session => ({ ...x, requirements: x.requirements ?? [], activity: x.activity ?? [] })
const normReq = (x: Omit<Requirement, 'sessions'> & { sessions?: string[] | null }): Requirement => ({ ...x, sessions: x.sessions ?? [] })

// 查询键按 SSE 主题组织，便于收到 changed 事件时整组失效。
export const qk = {
  auth: ['auth'] as const,
  sessions: ['sessions'] as const,
  session: (key: string) => ['sessions', 'detail', key] as const,
  requirements: ['requirements'] as const,
  requirement: (ref: string) => ['requirements', 'detail', ref] as const,
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

export function useSessions() {
  return useQuery({
    queryKey: qk.sessions,
    queryFn: () => unwrap(api.GET('/api/sessions')),
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
  requirements: [['requirements']],
  devices: [['devices'], ['sessions']],
  projects: [['projects'], ['sessions'], ['requirements']],
}

function invalidateTopics(qc: QueryClient, topics: string[]) {
  for (const topic of topics) {
    for (const key of topicKeys[topic] ?? []) {
      void qc.invalidateQueries({ queryKey: key })
    }
  }
}

// useLiveUpdates 订阅 /api/stream，收到 changed 事件时让对应查询失效；断线自动重连。
export function useLiveUpdates(enabled: boolean) {
  const qc = useQueryClient()
  const [connected, setConnected] = useState(true)
  useEffect(() => {
    if (!enabled) return
    let es: EventSource | null = null
    let timer: number | undefined
    let stopped = false
    let everConnected = false

    const connect = () => {
      if (stopped) return
      es = new EventSource('/api/stream')
      es.addEventListener('hello', () => {
        setConnected(true)
        // 重连后补齐断线期间错过的变化。
        if (everConnected) void qc.invalidateQueries()
        everConnected = true
      })
      es.addEventListener('changed', (ev) => {
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
      es?.close()
    }
  }, [enabled, qc])
  return connected
}
