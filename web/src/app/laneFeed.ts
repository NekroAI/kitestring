import { useSyncExternalStore } from 'react'
import { api, unwrap } from '../api/client'
import { laneSink, serverNow } from '../api/queries'
import { LaneStore, makeAxis, zoomSpec, ZOOMS, type LaneData, type LanePush, type Zoom } from '../lib/lanedata'

// 泳道时间线的数据源（R-205）：一份全局的 LaneStore。
// 打开工作台先拉当前档位要的那段，再在空闲时把近 1 天的分钟和近 1 周的小时预取好，之后换档不再取数；
// 实时只收 lane 推送（变了的那几格），断线重连后补拉一次。

export const laneStore = new LaneStore()
const subs = new Set<() => void>()
const bump = () => subs.forEach((f) => f())

export function onLaneData(f: () => void) {
  subs.add(f)
  return () => {
    subs.delete(f)
  }
}

// useLaneSnap：按 pick 取一个快照（字符串 / 数字），只有它变了才重渲染。
export function useLaneSnap<T extends string | number>(pick: () => T): T {
  return useSyncExternalStore(onLaneData, pick)
}

laneSink.push = (p: unknown) => {
  laneStore.push(p as LanePush)
  bump()
}
laneSink.resync = () => {
  // 断线期间的分钟补回来（已拉过的范围内）
  const now = serverNow()
  const jobs: Promise<void>[] = []
  if (Number.isFinite(laneStore.minFrom)) jobs.push(load(Math.max(laneStore.minFrom * 60000, now - 24 * 3600e3), 1))
  if (Number.isFinite(laneStore.hourFrom)) jobs.push(load(laneStore.hourFrom * 60000, 60))
  void Promise.all(jobs)
}

const inflight = new Map<string, Promise<void>>()

async function load(from: number, step: 1 | 60, to?: number) {
  const key = `${step}:${from}:${to ?? ''}`
  let p = inflight.get(key)
  if (!p) {
    p = unwrap(api.GET('/api/lanes', { params: { query: { from: Math.floor(from), to: to && Math.ceil(to), step } } }))
      .then((d) => {
        laneStore.merge(d as unknown as LaneData)
        bump()
      })
      .finally(() => inflight.delete(key))
    inflight.set(key, p)
  }
  return p
}

const spanMin: Record<Zoom, number> = { h1: 60, h6: 360, d1: 1440, w1: 7 * 1440 }

// ensureZoom：保证这一档要的数据在手里；只补还没拉过的那段（更早的部分）。
export function ensureZoom(z: Zoom): Promise<void> {
  const ax = makeAxis(z, 1e4, serverNow())
  const start = Math.min(ax.start, ax.end - spanMin[z])
  const hourly = zoomSpec[z].hourly
  const have = hourly ? laneStore.hourFrom : laneStore.minFrom
  if (have <= start) return Promise.resolve()
  return load(start * 60000, hourly ? 60 : 1, Number.isFinite(have) ? have * 60000 : undefined)
}

let prefetched = false
// prefetch：空闲时把各档要的数据都拉好（换档时不用等网络）。
export function prefetchLanes() {
  if (prefetched) return
  prefetched = true
  const idle = (f: () => void) => (typeof window.requestIdleCallback === 'function' ? window.requestIdleCallback(f, { timeout: 3000 }) : window.setTimeout(f, 800))
  idle(() => {
    void ZOOMS.reduce((p, z) => p.then(() => ensureZoom(z).catch(() => {})), Promise.resolve())
  })
}
