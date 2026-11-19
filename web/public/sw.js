// 纸鸢 service worker：负责系统推送（R-49）。
// 加速地址（R-369）：页面注册时带 ?accel=<加速地址> 才启用下面的拦截，没设加速地址时不拦截、不缓存任何请求（与改动前相同）。
//   - 静态文件（/assets/，文件名带哈希、内容不变）：主地址和加速地址的同一个文件共用一条缓存（按主地址的路径存），缓存优先；
//     缓存里没有时先从加速地址取（5 秒内没有回应或出错就记不可用），取不到从主地址取。两边切换不用重新下载，离线时也能从缓存打开。
//   - 加速地址上的附件签名链接：记了不可用就直接报错（页面随即换回主地址），否则 5 秒内没有回应就报错并记不可用。
//     录屏的分段请求（Range）不经这里，页面自己回退。私有文件不进缓存。
//   - 页面、接口、实时推送一律不拦截。
const ACCEL = new URL(self.location.href).searchParams.get('accel') || ''
const ASSETS = 'ks-assets-v1'
const ASSET_MAX = 300
const DOWN_MS = 10 * 60 * 1000
const WAIT_MS = 5000
let downUntil = 0

self.addEventListener('install', () => self.skipWaiting())
self.addEventListener('activate', (e) =>
  e.waitUntil(
    (async () => {
      // 不再用加速地址（或换了缓存版本）时清掉静态文件缓存
      for (const k of await caches.keys()) if (k.startsWith('ks-assets-') && (k !== ASSETS || !ACCEL)) await caches.delete(k)
      await self.clients.claim()
    })(),
  ),
)

self.addEventListener('message', (e) => {
  if (e.data && e.data.type === 'accel-down') downUntil = Math.max(downUntil, Number(e.data.until) || 0)
})

async function markDown() {
  downUntil = Date.now() + DOWN_MS
  for (const c of await self.clients.matchAll({ type: 'window' })) c.postMessage({ type: 'accel-down', until: downUntil })
}

// timed：等到响应头的时间超过 ms 就放弃
function timed(url, init, ms) {
  const ctl = new AbortController()
  const t = setTimeout(() => ctl.abort(), ms)
  return fetch(url, { ...init, signal: ctl.signal }).finally(() => clearTimeout(t))
}

async function trim(cache) {
  const keys = await cache.keys()
  if (keys.length > ASSET_MAX) for (const k of keys.slice(0, keys.length - Math.floor(ASSET_MAX * 0.8))) await cache.delete(k)
}

async function asset(rel, fromAccel) {
  const key = self.location.origin + rel
  const cache = await caches.open(ASSETS)
  const hit = await cache.match(key)
  if (hit) return hit
  let res = null
  if (fromAccel && Date.now() >= downUntil) {
    try {
      res = await timed(ACCEL + rel, { mode: 'cors', credentials: 'omit' }, WAIT_MS)
      if (!res.ok) res = null
    } catch {
      res = null
    }
    if (!res) await markDown()
  }
  if (!res) res = await fetch(key)
  if (res.status === 200 && (res.type === 'basic' || res.type === 'cors')) {
    // 存一份不带来源地址的副本：命中缓存时文件的地址就是这次请求的地址，脚本、样式里的相对引用跟着当前选的地址走，
    // 不会因为缓存是当初从加速地址取的，就在加速地址不可用时仍去请求加速地址
    await cache.put(key, new Response(await res.clone().blob(), { status: 200, headers: res.headers }))
    void trim(cache)
  }
  return res
}

async function file(req) {
  if (Date.now() < downUntil) return Response.error()
  try {
    return await timed(req.url, { mode: 'cors', credentials: 'omit' }, WAIT_MS)
  } catch {
    await markDown()
    return Response.error()
  }
}

if (ACCEL) {
  self.addEventListener('fetch', (e) => {
    const req = e.request
    if (req.method !== 'GET' || req.headers.has('range')) return
    const url = req.url
    const fromAccel = url.startsWith(ACCEL + '/')
    const rel = fromAccel ? url.slice(ACCEL.length) : url.startsWith(self.location.origin + '/') ? url.slice(self.location.origin.length) : null
    if (rel === null) return
    if (rel.startsWith('/assets/')) return e.respondWith(asset(rel, fromAccel))
    if (fromAccel && rel.startsWith('/api/accel/f/')) return e.respondWith(file(req))
  })
}

self.addEventListener('push', (e) => {
  let d = {}
  try {
    d = e.data ? e.data.json() : {}
  } catch {
    d = { title: '纸鸢', body: e.data ? e.data.text() : '' }
  }
  e.waitUntil(
    self.registration.showNotification(d.title || '纸鸢', {
      body: d.body || '',
      icon: '/icon-192.png',
      badge: '/icon-192.png',
      tag: d.tag || undefined,
      renotify: !!d.tag,
      data: { url: d.url || '/' },
    }),
  )
})

// 点通知：已经开着的纸鸢页面就切过去并跳到对应位置，没有就新开一个
self.addEventListener('notificationclick', (e) => {
  e.notification.close()
  const url = new URL(e.notification.data?.url || '/', self.location.origin).href
  e.waitUntil(
    (async () => {
      const all = await self.clients.matchAll({ type: 'window', includeUncontrolled: true })
      const win = all.find((c) => new URL(c.url).origin === self.location.origin)
      if (win) {
        await win.focus()
        if ('navigate' in win) return win.navigate(url)
        return
      }
      return self.clients.openWindow(url)
    })(),
  )
})
