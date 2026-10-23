// 纸鸢 service worker：只负责系统推送（R-49），不缓存任何请求。
self.addEventListener('install', () => self.skipWaiting())
self.addEventListener('activate', (e) => e.waitUntil(self.clients.claim()))

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
