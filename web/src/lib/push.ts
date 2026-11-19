import { api, unwrap } from '../api/client'
import { accelConfiguredBase } from './accel'

// 系统推送（R-49）：在这台设备上订阅 / 退订浏览器推送。iPhone / iPad 上必须先“添加到主屏幕”。

export type PushState = 'unsupported' | 'needs-install' | 'denied' | 'off' | 'on'

const standalone = () => matchMedia('(display-mode: standalone)').matches || (navigator as Navigator & { standalone?: boolean }).standalone === true
const ios = () => /iPad|iPhone|iPod/.test(navigator.userAgent) || (navigator.platform === 'MacIntel' && navigator.maxTouchPoints > 1)

// 设了加速地址时（R-369）把它写进 Service Worker 的地址：Service Worker 据此缓存两边的静态文件、给加速地址上的请求加超时和回退。
// 没设时注册地址与改动前相同，Service Worker 不拦截任何请求。
export function registerSW() {
  const accel = accelConfiguredBase()
  const url = accel ? `/sw.js?accel=${encodeURIComponent(accel)}` : '/sw.js'
  if ('serviceWorker' in navigator && window.isSecureContext) void navigator.serviceWorker.register(url).catch(() => {})
}

async function reg() {
  return navigator.serviceWorker.ready
}

export async function pushState(): Promise<{ state: PushState; endpoint?: string }> {
  if (!('serviceWorker' in navigator) || !('PushManager' in window) || !('Notification' in window)) return { state: ios() && !standalone() ? 'needs-install' : 'unsupported' }
  if (Notification.permission === 'denied') return { state: 'denied' }
  const sub = await (await reg()).pushManager.getSubscription()
  return sub ? { state: 'on', endpoint: sub.endpoint } : { state: 'off' }
}

const b64url = (buf: ArrayBuffer | null) => (buf ? btoa(String.fromCharCode(...new Uint8Array(buf))).replace(/\+/g, '-').replace(/\//g, '_').replace(/=+$/, '') : '')
function fromB64url(s: string): Uint8Array<ArrayBuffer> {
  const raw = atob(s.replace(/-/g, '+').replace(/_/g, '/') + '='.repeat((4 - (s.length % 4)) % 4))
  const out = new Uint8Array(new ArrayBuffer(raw.length))
  for (let i = 0; i < raw.length; i++) out[i] = raw.charCodeAt(i)
  return out
}

// 设备说明：给设置页的列表看，区分是哪台设备
export function deviceName(): string {
  const ua = navigator.userAgent
  const dev = /iPad/.test(ua) || (navigator.platform === 'MacIntel' && navigator.maxTouchPoints > 1) ? 'iPad' : /iPhone/.test(ua) ? 'iPhone' : /Android/.test(ua) ? 'Android' : /Mac/.test(ua) ? 'Mac' : /Windows/.test(ua) ? 'Windows' : '设备'
  const app = standalone() ? '主屏 App' : /Edg\//.test(ua) ? 'Edge' : /Chrome\//.test(ua) ? 'Chrome' : /Firefox\//.test(ua) ? 'Firefox' : /Safari\//.test(ua) ? 'Safari' : '浏览器'
  return `${dev} · ${app}`
}

export async function enablePush(): Promise<string> {
  const perm = await Notification.requestPermission()
  if (perm !== 'granted') throw new Error(perm === 'denied' ? '你拒绝了通知权限，需要在系统设置里重新允许' : '没有获得通知权限')
  const { public_key } = await unwrap(api.GET('/api/push'))
  const r = await reg()
  const sub = (await r.pushManager.getSubscription()) ?? (await r.pushManager.subscribe({ userVisibleOnly: true, applicationServerKey: fromB64url(public_key) }))
  await unwrap(api.POST('/api/push/subscribe', { body: { endpoint: sub.endpoint, p256dh: b64url(sub.getKey('p256dh')), auth: b64url(sub.getKey('auth')), name: deviceName() } }))
  return sub.endpoint
}

export async function disablePush(): Promise<void> {
  const sub = await (await reg()).pushManager.getSubscription()
  if (!sub) return
  await unwrap(api.POST('/api/push/unsubscribe', { body: { ref: sub.endpoint } })).catch(() => {})
  await sub.unsubscribe()
}
