import { useEffect, useState } from 'react'
import { useInfiniteQuery, useQuery, useQueryClient } from '@tanstack/react-query'
import { ApiError, type Schemas } from '../api/client'
import { t } from '../i18n'

// 访客页的数据（R-281 第 3 段）：只调 /api/share/<token>/…（免登录、只读），不用所有者的接口客户端，
// 也不会触发所有者的「未登录跳登录页」。设了访问密码时，输对后服务端发一个只在这条链接路径下有效的 cookie。

export type GuestMeta = Schemas['GuestMetaOutBody']
export type GuestOverview = Schemas['GuestOverviewOutBody']
export type GuestReq = Schemas['GuestReqOutBody']
export type GuestSession = Schemas['GuestSession']
export type GuestTurn = Schemas['GuestTurn']
export type GuestItem = Schemas['GuestItem']
export type GuestSessionPage = Schemas['GuestSessionOutBody']
export type ShareActivity = Schemas['ShareActivity']
export type GuestPlan = Schemas['Plan']

const base = (token: string) => `/api/share/${encodeURIComponent(token)}`

async function call<T>(url: string, init?: RequestInit): Promise<T> {
  const r = await fetch(url, { credentials: 'same-origin', ...init })
  if (!r.ok) {
    let msg = ''
    try {
      const j = (await r.json()) as { detail?: string; title?: string }
      msg = j.detail || j.title || ''
    } catch {
      // 不是 JSON
    }
    throw new ApiError(msg || t.common.withStatus(t.common.requestFailed, r.status), r.status)
  }
  return (await r.json()) as T
}

export const gk = {
  all: (token: string) => ['guest', token] as const,
  meta: (token: string) => ['guest', token, 'meta'] as const,
  overview: (token: string) => ['guest', token, 'overview'] as const,
  req: (token: string, code: string) => ['guest', token, 'req', code] as const,
  plan: (token: string, code: string) => ['guest', token, 'plan', code] as const,
  session: (token: string, key: string) => ['guest', token, 'session', key] as const,
}

const noRetry = (n: number, e: unknown) => !(e instanceof ApiError && e.status < 500) && n < 2

// useGuestMeta：分享名称、访客名字、要不要输密码。每次调用记一次访问，所以只在打开页面、输完密码、链接失效时取。
export function useGuestMeta(token: string) {
  return useQuery({ queryKey: gk.meta(token), queryFn: () => call<GuestMeta>(base(token)), staleTime: Infinity, refetchOnWindowFocus: false, retry: noRetry })
}

export async function unlock(token: string, password: string) {
  return call<{ ok: boolean }>(`${base(token)}/unlock`, { method: 'POST', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify({ password }) })
}

const norm = <T>(v: T[] | null | undefined): T[] => v ?? []

export function useOverview(token: string) {
  return useQuery({
    queryKey: gk.overview(token),
    queryFn: () => call<GuestOverview>(`${base(token)}/overview`),
    select: (d) => ({ ...d, nodes: norm(d.nodes), codes: norm(d.codes), sessions: norm(d.sessions), activity: norm(d.activity), decs: norm(d.decs) }),
    retry: noRetry,
  })
}

export function useGuestReq(token: string, code: string) {
  return useQuery({
    queryKey: gk.req(token, code),
    queryFn: () => call<GuestReq>(`${base(token)}/requirements/${encodeURIComponent(code)}`),
    select: (d) => ({ ...d, requirement: { ...d.requirement, sessions: norm(d.requirement.sessions) }, decisions: norm(d.decisions), sessions: norm(d.sessions), activity: norm(d.activity) }),
    retry: noRetry,
  })
}

export function useGuestPlan(token: string, code: string) {
  return useQuery({ queryKey: gk.plan(token, code), queryFn: () => call<GuestPlan>(`${base(token)}/plans/${encodeURIComponent(code)}`), retry: noRetry })
}

// useGuestSession：会话详情，最新一页在前取；「加载更早的记录」往上翻。实时更新时整组重取。
export function useGuestSession(token: string, key: string) {
  return useInfiniteQuery({
    queryKey: gk.session(token, key),
    initialPageParam: '',
    queryFn: ({ pageParam }) => call<GuestSessionPage>(`${base(token)}/sessions/${encodeURIComponent(key)}${pageParam ? `?before=${encodeURIComponent(pageParam)}` : ''}`),
    getNextPageParam: (last) => (last.more && last.before ? last.before : undefined),
    retry: noRetry,
  })
}

// useGuestLive：访客的实时通道。内容有变化（changed）时重取这条链接下的数据（不重取访问信息，免得每次都记一次访问）；
// 链接撤回、分享撤回或到期、改了密码（gone）时重取访问信息，页面随之换成被拒绝或输密码。断线自动重连。
export function useGuestLive(token: string, enabled: boolean) {
  const qc = useQueryClient()
  const [live, setLive] = useState(true)
  useEffect(() => {
    if (!enabled) return
    let es: EventSource | null = null
    let timer: number | undefined
    let stopped = false
    let ever = false
    const refresh = () => qc.invalidateQueries({ predicate: (q) => q.queryKey[0] === 'guest' && q.queryKey[1] === token && q.queryKey[2] !== 'meta' })
    const recheck = () => qc.invalidateQueries({ queryKey: gk.meta(token) })
    const connect = () => {
      if (stopped) return
      es = new EventSource(`${base(token)}/stream`)
      es.addEventListener('hello', () => {
        setLive(true)
        if (ever) void refresh() // 重连后补上断线期间的变化
        ever = true
      })
      es.addEventListener('changed', () => void refresh())
      es.addEventListener('gone', () => {
        stopped = true
        es?.close()
        void recheck()
      })
      es.onerror = () => {
        setLive(false)
        if (es && es.readyState === EventSource.CLOSED) {
          // 服务端拒绝（链接失效、要重新输密码）时浏览器不会自己重连：先确认链接还能用，再重连
          es = null
          void recheck()
          timer = window.setTimeout(connect, 3000)
        }
      }
    }
    connect()
    const onWake = () => {
      if (document.visibilityState === 'visible' && es?.readyState !== EventSource.OPEN) {
        es?.close()
        if (timer) window.clearTimeout(timer)
        connect()
      }
    }
    document.addEventListener('visibilitychange', onWake)
    return () => {
      stopped = true
      if (timer) window.clearTimeout(timer)
      document.removeEventListener('visibilitychange', onWake)
      es?.close()
    }
  }, [token, enabled, qc])
  return live
}
