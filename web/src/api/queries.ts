import { useInfiniteQuery, useQuery, useQueryClient, type QueryClient } from '@tanstack/react-query'
import { useEffect, useState, useSyncExternalStore } from 'react'
import { api, unwrap, type Plan, type Requirement, type Schemas, type Session } from './client'

const normSession = (x: Omit<Session, 'requirements' | 'activity'> & { requirements?: Session['requirements'] | null; activity?: number[] | null }): Session => ({ ...x, requirements: x.requirements ?? [], activity: x.activity ?? [], ticks: x.ticks ?? [] })
const normReq = (x: Omit<Requirement, 'sessions'> & { sessions?: string[] | null }): Requirement => ({ ...x, sessions: x.sessions ?? [] })
const normPlan = (x: Schemas['Plan']): Plan => ({ ...x, stages: (x.stages ?? []).map((st) => ({ ...st, requirements: st.requirements ?? [] })) })

// 查询键按 SSE 主题组织，便于收到 changed 事件时整组失效。
export const qk = {
  auth: ['auth'] as const,
  sessions: ['sessions'] as const,
  session: (key: string) => ['sessions', 'detail', key] as const,
  turns: (key: string) => ['session-turns', key] as const,
  bgSessions: ['sessions', 'background'] as const,
  requirements: ['requirements'] as const,
  requirement: (ref: string) => ['requirements', 'detail', ref] as const,
  plans: ['plans'] as const,
  devices: ['devices'] as const,
  projects: ['projects'] as const,
  settings: ['settings'] as const,
  tokenPrices: ['settings', 'token-prices'] as const,
  cacheHolds: ['settings', 'cache-holds'] as const,
  rolePrompts: ['role-prompts'] as const,
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
  if (s.background) {
    // 后台任务（R-179）不进主列表，只让 ⌘K 里按需拉取的那份重新取
    void qc.invalidateQueries({ queryKey: qk.bgSessions })
    void qc.invalidateQueries({ queryKey: qk.session(s.key), exact: true })
    return
  }
  const cur = patches.get(s.key)
  if (cur && cur.rev >= rev) return // 同一会话只认更新的推送
  patches.set(s.key, { rev, session: s })
  qc.setQueryData<RawSessions>(qk.sessions, (old) => (old ? { ...old, sessions: upsert(old.sessions ?? [], s) } : old))
  void qc.invalidateQueries({ queryKey: qk.session(s.key), exact: true }) // 打开着的会话详情补拉时间线
  void qc.invalidateQueries({ queryKey: qk.turns(s.key) }) // 按轮的时间线（R-273）
}

export function useSessions() {
  return useQuery({
    queryKey: qk.sessions,
    queryFn: loadSessions,
    select: (d) => ({ sessions: (d.sessions ?? []).map(normSession), devices: d.devices ?? [] }),
  })
}

// 后台任务（桌面应用自己发起的，如 Codex 项目建议，R-179）：不在 useSessions 里，只在 ⌘K 里按需拉取。
export function useBackgroundSessions(enabled: boolean) {
  return useQuery({
    queryKey: qk.bgSessions,
    queryFn: () => unwrap(api.GET('/api/sessions', { params: { query: { background: true } } })),
    select: (d) => (d.sessions ?? []).map(normSession),
    enabled,
  })
}

// 系统推送的公钥与已订阅的设备（R-49）
export function usePush() {
  return useQuery({ queryKey: ['settings', 'push'], queryFn: () => unwrap(api.GET('/api/push')), select: (d) => ({ publicKey: d.public_key, subs: d.subs ?? [] }) })
}

export function useSessionDetail(key: string | null) {
  return useQuery({
    queryKey: qk.session(key ?? ''),
    queryFn: () => unwrap(api.GET('/api/sessions/{key}', { params: { path: { key: key! } } })),
    select: (d) => ({ session: normSession(d.session), events: d.events ?? [], lastReply: d.last_reply, handoffs: d.handoffs ?? [] }),
    enabled: !!key,
  })
}

// 会话按轮的时间线（R-273）：最新一页在前，往上滚动时按 before 取更早的一页。
export type TurnsPage = Schemas['TurnsOutBody']
export function useSessionTurns(key: string | null) {
  return useInfiniteQuery({
    queryKey: qk.turns(key ?? ''),
    queryFn: ({ pageParam }) => unwrap(api.GET('/api/sessions/{key}/turns', { params: { path: { key: key! }, query: pageParam ? { before: pageParam } : {} } })),
    initialPageParam: '',
    getNextPageParam: (last) => (last.more && last.before ? last.before : undefined),
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

// 参谋授权窗口与代答记录（R-163），projectId 为空时为全部项目。
export function useGrants(projectId?: string) {
  return useQuery({
    queryKey: ['grants', projectId ?? ''],
    queryFn: () => unwrap(api.GET('/api/grants', { params: { query: { project_id: projectId || undefined } } })),
    select: (d) => ({ grants: d.grants ?? [], proxied: d.proxied ?? [] }),
  })
}

// 当前安排（R-245）：各项目生效中的安排，新的在前。
export function useArrangements() {
  return useQuery({
    queryKey: ['arrangements'],
    queryFn: () => unwrap(api.GET('/api/arrangements', { params: { query: {} } })),
    select: (d) => d.arrangements ?? [],
  })
}

// 离开期间小结（R-165）：各项目离开期间的汇总、待验收需求上的参谋建议、参谋没代答的原因。
export function useMorning() {
  return useQuery({
    queryKey: ['morning'],
    queryFn: () => unwrap(api.GET('/api/morning')),
    select: (d) => ({ reports: d.reports ?? [], advice: d.advice ?? [], holds: d.holds ?? [] }),
  })
}

// 暂时离开（R-210）：离开中 / 最近一次离开，离开多久、主助理处理几件、推送几条。
export function useAway() {
  return useQuery({
    queryKey: ['away'],
    queryFn: () => unwrap(api.GET('/api/away')),
  })
}

// 离开时段报告（R-210）：你在各设备上处理了什么等；enabled 为 false 时不取。
export function useAwayReport(enabled: boolean) {
  return useQuery({
    queryKey: ['away', 'report'],
    queryFn: () => unwrap(api.GET('/api/away/report', { params: { query: {} } })),
    enabled,
  })
}

export function useRequirements() {
  return useQuery({
    queryKey: qk.requirements,
    queryFn: () => unwrap(api.GET('/api/requirements')),
    select: (d) => (d.requirements ?? []).map(normReq),
  })
}

// 需求池盘面指标（R-133）：各状态条数、昨天新增 / 完成、草稿积压、近 7 天每日完成、每条进入当前状态的时间。
// 挂在 requirements 主题下，需求一变就跟着刷新；按浏览器时区算「昨天」。
export function usePool(projectId?: string) {
  return useQuery({
    queryKey: ['requirements', 'pool', projectId ?? ''],
    queryFn: () => unwrap(api.GET('/api/requirement-pool', { params: { query: { project_id: projectId || undefined, tz_offset: String(-new Date().getTimezoneOffset()) } } })),
    select: (d) => ({ ...d, items: d.items ?? [], done_daily: d.done_daily ?? [] }),
  })
}

export const requirementQuery = (ref: string) => ({
  queryKey: qk.requirement(ref),
  queryFn: () => unwrap(api.GET('/api/requirements/{ref}', { params: { path: { ref } } })),
})

export function useRequirement(ref: string | null) {
  return useQuery({
    ...requirementQuery(ref ?? ''),
    select: normReq,
    enabled: !!ref,
  })
}

// usePrefetchReqs：抽屉里提前取再往外一件的详情（邻件本身已经画在带子上，数据随之取好），连续翻页时露出来的不是骨架（R-73）
export function usePrefetchReqs(codes: (string | undefined)[]) {
  const qc = useQueryClient()
  const key = codes.filter(Boolean).join(',')
  useEffect(() => {
    for (const c of key ? key.split(',') : []) void qc.prefetchQuery({ ...requirementQuery(c), staleTime: 15_000 })
  }, [key, qc])
}

export function usePlans() {
  return useQuery({
    queryKey: qk.plans,
    queryFn: () => unwrap(api.GET('/api/plans')),
    select: (d) => (d.plans ?? []).map(normPlan),
  })
}

// 分享（R-281）：所有者的分享列表、单个分享、新建 / 编辑时的勾选列表。挂在 shares 主题下，需求和计划变了也跟着刷新（标题、状态、范围）。
export type ShareView = Schemas['ShareView']
export type ShareLinkView = Schemas['ShareLinkView']
const normShare = (x: ShareView) => ({ ...x, items: x.items ?? [], links: x.links ?? [], scope: { ...x.scope, nodes: x.scope.nodes ?? [] } })
export type Share = ReturnType<typeof normShare>

export function useShares() {
  return useQuery({
    queryKey: ['shares', 'list'],
    queryFn: () => unwrap(api.GET('/api/shares')),
    select: (d) => (d.shares ?? []).map(normShare),
  })
}

export function useShare(id: string | undefined) {
  return useQuery({
    queryKey: ['shares', 'detail', id ?? ''],
    queryFn: () => unwrap(api.GET('/api/shares/{id}', { params: { path: { id: id! } } })),
    select: normShare,
    enabled: !!id,
  })
}

// 待收的访客留言（R-281 第 4 段）：全部分享的，早的在前；进「待处理」，侧边栏「分享」的数字也数它。
export type ShareComment = Schemas['ShareCommentView']
export function useShareComments() {
  return useQuery({
    queryKey: ['shares', 'comments'],
    queryFn: () => unwrap(api.GET('/api/share-comments')),
    select: (d) => (d.comments ?? []).map((c) => ({ ...c, choices: c.choices ?? [] })),
  })
}

export function useShareCandidates(projectId: string | undefined, enabled = true) {
  return useQuery({
    queryKey: ['shares', 'candidates', projectId ?? ''],
    queryFn: () => unwrap(api.GET('/api/shares/candidates', { params: { query: { project_id: projectId || undefined } } })),
    select: (d) => (d.nodes ?? []).map((n) => ({ ...n, parents: n.parents ?? [] })),
    enabled,
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

// 各模型的价格（R-206，选填）：四项都填了的模型才显示金额
export function useTokenPrices() {
  return useQuery({
    queryKey: qk.tokenPrices,
    queryFn: () => unwrap(api.GET('/api/token-prices')),
    staleTime: 5 * 60_000,
  })
}

// 各来源手填的缓存保留时长（R-207，选填）：拿不到档位的来源填了才显示缓存余温
export function useCacheHolds() {
  return useQuery({
    queryKey: qk.cacheHolds,
    queryFn: () => unwrap(api.GET('/api/cache-holds')),
    staleTime: 5 * 60_000,
  })
}

// 通讯页（R-249）：项目里的在线会话、消息与广播、送达状态。挂在 decisions 主题下，团队消息一变就重取；
// 送达状态还跟着会话状态变（会话待命 / 结束），会话推送不走 changed 主题，另外每 30 秒补拉一次。
export function useComms(projectId: string, since: string, dayStart: string, ended: boolean) {
  return useQuery({
    queryKey: ['decisions', 'comms', projectId, since, dayStart, ended],
    queryFn: () => unwrap(api.GET('/api/comms', { params: { query: { project_id: projectId, since, day_start: dayStart, ended: ended || undefined } } })),
    enabled: !!projectId,
    refetchInterval: 30_000,
    placeholderData: (prev) => prev,
  })
}

// 导航「通讯」旁的黄色数字：所有项目里还在等对方醒来的消息（发给已结束会话的不算，D-1071）。
export function useCommsPending() {
  return useQuery({
    queryKey: ['decisions', 'comms-pending'],
    queryFn: () => unwrap(api.GET('/api/comms/pending')),
    select: (d) => d.count,
    refetchInterval: 60_000,
  })
}

// 主题 → 需要失效的查询前缀。
const topicKeys: Record<string, readonly (readonly string[])[]> = {
  sessions: [['sessions']],
  decisions: [['decisions'], ['grants'], ['morning'], ['session-turns']],
  grants: [['grants'], ['morning']],
  morning: [['morning']],
  away: [['away'], ['morning'], ['grants']],
  requirements: [['requirements'], ['plans'], ['morning'], ['session-turns'], ['shares']],
  plans: [['plans'], ['shares']],
  shares: [['shares']],
  devices: [['devices'], ['sessions']],
  projects: [['projects'], ['sessions'], ['requirements'], ['plans'], ['morning'], ['session-turns']],
  settings: [['settings']],
  orphans: [['orphans']],
  arrangements: [['arrangements']],
}

// laneSink：泳道时间线（R-205）的实时推送与断线后补拉，由 app/laneFeed 接上。
export const laneSink: { push: (p: unknown) => void; resync: () => void } = { push: () => {}, resync: () => {} }

function invalidateTopics(qc: QueryClient, topics: string[]) {
  for (const topic of topics) {
    if (topic === 'lanes') laneSink.resync() // 推送丢过（慢客户端），泳道时间线补拉一次
    for (const key of topicKeys[topic] ?? []) {
      void qc.invalidateQueries({ queryKey: key })
    }
  }
}

// 服务端每 20 秒发一次心跳；超过这么久什么都没收到，就认定连接已经悄悄断了（手机切后台再回来常见）。
const SILENT_MS = 50_000

// 实时连接的状态与服务端时钟（R-170）：工作台断线时整体静止变灰；时间线按服务端时间画，
// 本机时钟偏了也不会把刻度画到「现在」右边。偏差取自 hello / ping 里带的服务端时间。
let liveOn = true
let skew = 0
const liveSubs = new Set<() => void>()
const setLive = (on: boolean) => {
  if (on === liveOn) return
  liveOn = on
  liveSubs.forEach((f) => f())
}
const syncClock = (data: string) => {
  try {
    const n = (JSON.parse(data) as { now?: number }).now
    if (typeof n === 'number' && n > 0) skew = n - Date.now()
  } catch {
    /* 旧版 Hub 的心跳不带时间 */
  }
}
// serverNow：按服务端时钟的「现在」（毫秒）
export const serverNow = () => Date.now() + skew
export const useConnected = () =>
  useSyncExternalStore(
    (f) => {
      liveSubs.add(f)
      return () => liveSubs.delete(f)
    },
    () => liveOn,
  )

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
      es.addEventListener('ping', (ev) => {
        seen()
        syncClock((ev as MessageEvent<string>).data)
      })
      es.addEventListener('hello', (ev) => {
        seen()
        syncClock((ev as MessageEvent<string>).data)
        setConnected(true)
        setLive(true)
        // 重连后补齐断线期间错过的变化。
        if (everConnected) {
          void qc.invalidateQueries()
          laneSink.resync()
        }
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
      es.addEventListener('lane', (ev) => {
        seen()
        try {
          laneSink.push(JSON.parse((ev as MessageEvent<string>).data))
        } catch {
          laneSink.resync()
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
        setLive(false)
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
