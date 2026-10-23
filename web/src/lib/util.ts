import { clsx, type ClassValue } from 'clsx'
import { useEffect, useState, useSyncExternalStore } from 'react'
import { t } from '../i18n'

export const cx = (...v: ClassValue[]) => clsx(v)

// ---------- 时间 ----------

function ts(iso?: string | null): number {
  if (!iso) return 0
  const v = new Date(iso).getTime()
  return Number.isNaN(v) || v <= 0 ? 0 : v
}

// relTime：“3 分钟前”。
export function relTime(iso?: string | null, now = Date.now()): string {
  const v = ts(iso)
  if (!v) return t.time.never
  const s = Math.max(0, Math.round((now - v) / 1000))
  if (s < 10) return t.time.justNow
  if (s < 60) return t.time.secondsAgo(s)
  const m = Math.floor(s / 60)
  if (m < 60) return t.time.minutesAgo(m)
  const h = Math.floor(m / 60)
  if (h < 48) return t.time.hoursAgo(h)
  return t.time.daysAgo(Math.floor(h / 24))
}

// duration：从 iso 到 now 的时长，“12 分钟”“1 小时 5 分”。
export function duration(iso?: string | null, now = Date.now()): string {
  const v = ts(iso)
  if (!v) return ''
  const s = Math.max(0, Math.round((now - v) / 1000))
  if (s < 60) return t.time.durSeconds(s)
  const m = Math.floor(s / 60)
  if (m < 60) return t.time.durMinutes(m)
  const h = Math.floor(m / 60)
  if (h < 24) return t.time.durHours(h, m % 60)
  return t.time.durDays(Math.floor(h / 24), h % 24)
}

// shortDuration：只保留最大单位，用在状态胶囊等狭窄位置（完整时长放在悬停提示里）。
export function shortDuration(iso?: string | null, now = Date.now()): string {
  const v = ts(iso)
  if (!v) return ''
  const s = Math.max(0, Math.round((now - v) / 1000))
  if (s < 60) return t.time.durSeconds(s)
  const m = Math.floor(s / 60)
  if (m < 60) return t.time.durMinutes(m)
  const h = Math.floor(m / 60)
  if (h < 24) return t.time.durHours(h, 0)
  return t.time.durDays(Math.floor(h / 24), 0)
}

export function absTime(iso?: string | null): string {
  const v = ts(iso)
  if (!v) return t.time.never
  return new Date(v).toLocaleString(undefined, { month: '2-digit', day: '2-digit', hour: '2-digit', minute: '2-digit', hour12: false })
}

export function clockTime(iso?: string | null): string {
  const v = ts(iso)
  if (!v) return ''
  return new Date(v).toLocaleTimeString(undefined, { hour: '2-digit', minute: '2-digit', second: '2-digit', hour12: false })
}

// useNow：每 interval 毫秒刷新一次的当前时间，所有组件共享一个计时器。
const nowListeners = new Set<() => void>()
let nowValue = Date.now()
let nowTimer: number | undefined
function subscribeNow(fn: () => void) {
  nowListeners.add(fn)
  if (!nowTimer) {
    nowTimer = window.setInterval(() => {
      nowValue = Date.now()
      nowListeners.forEach((l) => l())
    }, 5000)
  }
  return () => {
    nowListeners.delete(fn)
    if (!nowListeners.size && nowTimer) {
      window.clearInterval(nowTimer)
      nowTimer = undefined
    }
  }
}
export function useNow(): number {
  return useSyncExternalStore(subscribeNow, () => nowValue)
}

// ---------- 响应式 ----------

export function useMedia(query: string): boolean {
  const [match, setMatch] = useState(() => (typeof window === 'undefined' ? false : window.matchMedia(query).matches))
  useEffect(() => {
    const m = window.matchMedia(query)
    const on = () => setMatch(m.matches)
    on()
    m.addEventListener('change', on)
    return () => m.removeEventListener('change', on)
  }, [query])
  return match
}

export const useDesktop = () => useMedia('(min-width: 1024px)')

// noKbd：触屏设备上去掉提示文字里的键盘快捷键（“ · ⌘↵ …”）。
const coarse = typeof matchMedia !== 'undefined' && matchMedia('(pointer: coarse)').matches
export const noKbd = (s: string) => (coarse ? s.replace(/\s*·?\s*⌘↵.*$/, '') : s)

// ---------- 本机偏好（失败时静默） ----------

export function loadPref<T>(key: string, fallback: T): T {
  try {
    const v = localStorage.getItem(key)
    return v == null ? fallback : (JSON.parse(v) as T)
  } catch {
    return fallback
  }
}

export function savePref(key: string, v: unknown) {
  try {
    localStorage.setItem(key, JSON.stringify(v))
  } catch {
    // 隐私模式等情况下忽略
  }
}

export function usePref<T>(key: string, fallback: T): [T, (v: T) => void] {
  const [v, setV] = useState<T>(() => loadPref(key, fallback))
  return [
    v,
    (nv: T) => {
      setV(nv)
      savePref(key, nv)
    },
  ]
}

// ---------- 文本 ----------

// shortPath 把家目录替换为 ~，过长时保留末尾。
export function shortPath(p: string, max = 48): string {
  let s = p.replace(/^\/(Users|home)\/[^/]+/, '~')
  if (s.length > max) s = '…' + s.slice(s.length - max + 1)
  return s
}

// shortRemote：github.com/owner/repo → owner/repo
export function shortRemote(r: string): string {
  const parts = r.split('/')
  return parts.length >= 3 ? parts.slice(1).join('/') : r
}

export async function copyText(text: string): Promise<boolean> {
  try {
    await navigator.clipboard.writeText(text)
    return true
  } catch {
    return false
  }
}
