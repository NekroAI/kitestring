import { useSyncExternalStore } from 'react'
import { AccelState, DOWN_KEY, type AccelKey, type FileOpts } from './accelcore'

// 加速地址（R-369）在浏览器里的部分。网页入口的启动脚本（internal/hub/accelpage.go）已探测过加速地址，
// 结果在 window.__KS_ACCEL：on 表示这次经加速地址加载；keyP 是和探测并行发出的签名密钥请求。没设加速地址时没有这个对象，
// 这里的一切都不生效，附件地址与改动前相同。

type Boot = { base: string; on?: boolean; keyUrl?: string; keyP?: Promise<AccelKey | null> }
const boot: Boot | undefined = typeof window === 'undefined' ? undefined : (window as unknown as { __KS_ACCEL?: Boot }).__KS_ACCEL

const PROBE_MS = 3000

async function probe(base: string): Promise<boolean> {
  const ctl = new AbortController()
  const t = window.setTimeout(() => ctl.abort(), PROBE_MS)
  try {
    const r = await fetch(`${base}/api/accel/ping`, { mode: 'cors', credentials: 'omit', cache: 'no-store', signal: ctl.signal })
    return r.ok
  } catch {
    return false
  } finally {
    window.clearTimeout(t)
  }
}

const tellSW = (msg: unknown) => {
  try {
    navigator.serviceWorker?.controller?.postMessage(msg)
  } catch {
    /* 没有 Service Worker */
  }
}

export const accel = new AccelState(boot?.base ?? '', !!boot?.on, {
  now: () => Date.now(),
  save: (v) => {
    try {
      localStorage.setItem(DOWN_KEY, v)
    } catch {
      /* 存不了就只在这次页面里记住 */
    }
  },
  probe: () => probe(boot?.base ?? ''),
  later: (fn, ms) => window.setTimeout(fn, ms),
  onDown: (until) => tellSW({ type: 'accel-down', until }),
})

// 签名密钥：首次用启动脚本发出的请求；快过期或还没取到（例如刚登录）时按需重取，最多 10 秒一次。
let keyUrl = boot?.keyUrl ?? '/api/accel/key'
let fetching: Promise<void> | null = null
let lastTry = 0
function takeKey(k: AccelKey | null) {
  if (k && k.key && k.base === accel.base) {
    const had = accel.usable()
    accel.key = k
    if (!had && accel.usable()) emitKey()
  }
}
const keySubs = new Set<() => void>()
const emitKey = () => keySubs.forEach((f) => f())
export function refreshKey(force = false): Promise<void> {
  if (!accel.base) return Promise.resolve()
  if (fetching) return fetching
  if (!force && Date.now() - lastTry < 10_000) return Promise.resolve()
  lastTry = Date.now()
  fetching = fetch(keyUrl, { credentials: 'same-origin', cache: 'no-store' })
    .then((r) => (r.ok ? (r.json() as Promise<AccelKey>) : null))
    .catch(() => null)
    .then(takeKey)
    .finally(() => {
      fetching = null
    })
  return fetching
}

// 访客页换范围：附件取分享里的，签名密钥向访客接口取（R-365 的访客也走加速）
export function setAccelScope(mainBase: string, guestToken?: string) {
  accel.mainBase = mainBase
  const next = guestToken ? `/api/share/${encodeURIComponent(guestToken)}/accel` : '/api/accel/key'
  if (next !== keyUrl) {
    keyUrl = next
    accel.key = null
    void refreshKey(true)
  }
}

// fileUrl：附件地址。加速地址可用且有签名密钥时给签名链接，否则给主地址（与改动前相同）。
export function accelFileUrl(id: string, o: FileOpts = {}) {
  if (accel.on && !accel.usable()) void refreshKey()
  return accel.fileUrl(id, o)
}

// accelReady：渲染前等签名密钥（只在经加速地址加载时等，最多 2 秒），免得首屏的图先从主地址取一遍。
export function accelReady(): Promise<void> {
  if (!boot || !accel.on) return Promise.resolve()
  const p = (boot.keyP ?? Promise.resolve(null)).then(takeKey)
  lastTry = Date.now()
  return Promise.race([p, new Promise<void>((ok) => window.setTimeout(ok, 2000))])
}

// installAccel：加速地址上的图片、录屏加载失败时逐个换回主地址；Service Worker 发现加速地址不可用时同步状态；
// 按需加载的脚本块失败（还没有 Service Worker 接管时）记不可用并刷新一次，刷新后从主地址加载。
export function installAccel() {
  if (!accel.base) return
  window.addEventListener('error', onResourceError, true)
  navigator.serviceWorker?.addEventListener('message', (e: MessageEvent) => {
    if (e.data?.type === 'accel-down' && accel.on) accel.markDown()
  })
  window.addEventListener('vite:preloadError', (e) => {
    if (!accel.on) return
    let tried = false
    try {
      tried = sessionStorage.getItem('ks-accel-reload') === '1'
      sessionStorage.setItem('ks-accel-reload', '1')
    } catch {
      /* 没有会话存储时不刷新，交给原错误处理 */
      tried = true
    }
    if (tried) return
    e.preventDefault()
    accel.markDown()
    window.location.reload()
  })
  // 页面正常起来后清掉「刷新过一次」的记号
  window.setTimeout(() => {
    try {
      sessionStorage.removeItem('ks-accel-reload')
    } catch {
      /* 忽略 */
    }
  }, 10_000)
}

function onResourceError(e: Event) {
  const el = e.target
  if (!(el instanceof HTMLImageElement || el instanceof HTMLVideoElement || el instanceof HTMLSourceElement)) return
  const src = el.getAttribute('src') ?? ''
  const main = accel.failed(src)
  if (main === null) return
  el.setAttribute('src', main)
  if (el instanceof HTMLSourceElement) (el.parentElement as HTMLMediaElement | null)?.load?.()
}

// accelFetch：fetch 一个可能在加速地址上的文件（3D 模型、文本预览），失败时回主地址再取一次。
export async function accelFetch(url: string, init?: RequestInit): Promise<Response> {
  try {
    const r = await fetch(url, init)
    if (r.ok || !url.startsWith(accel.base + '/') || !accel.base) return r
    throw new Error(String(r.status))
  } catch (err) {
    const main = accel.failed(url)
    if (main === null) throw err
    return fetch(main, { ...init, credentials: 'same-origin' })
  }
}

// useAccel：设置页显示本机当前是否经加速地址加载
export function useAccel() {
  return useSyncExternalStore(
    (f) => {
      const a = accel.subscribe(f)
      keySubs.add(f)
      return () => {
        a()
        keySubs.delete(f)
      }
    },
    () => (accel.base ? (accel.on ? 'on' : 'down') : 'off'),
  )
}

export const accelConfiguredBase = () => boot?.base ?? ''
