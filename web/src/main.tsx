import { lazy, StrictMode, Suspense } from 'react'
import { createRoot } from 'react-dom/client'
import { QueryCache, QueryClient, QueryClientProvider } from '@tanstack/react-query'
import { BrowserRouter } from 'react-router'
import { Toaster } from 'sonner'
import { ApiError, setUnauthorizedHandler } from './api/client'
import { qk } from './api/queries'
import { OutboxProvider } from './app/outbox'
import { setSettle, UndoDock } from './app/verdict'
import { waitIdle } from './lib/settle'
import { initTheme } from './lib/theme'
import { registerSW } from './lib/push'
import { ConfirmProvider } from './ui/overlay'
import { TipProvider } from './ui/base'
import { installGestureLog, installWheelGuard } from './ui/gesture'
import App from './App'
import './index.css'

initTheme()
registerSW()
// 妙控板双指横滑归纸鸢，不触发浏览器前进 / 后退（R-73）
installWheelGuard()
// 地址带 ?gesturelog 时记录手势事件序列，供排查真机手感（不带参数时什么都不做）
installGestureLog()

// iOS Safari 无视 viewport 的 user-scalable=no：拦下页面级的捏合与多指缩放。
// 看图器用指针事件自己算缩放，不受影响。
document.addEventListener('gesturestart', (e) => e.preventDefault())
document.addEventListener('touchmove', (e) => e.touches.length > 1 && e.preventDefault(), { passive: false })
// 连点两下会触发 iOS 的双击放大：第二下拦掉默认行为，自己补发点击。看图器、批注板（touch-none）自己处理双击。
let lastTap = { t: 0, x: 0, y: 0 }
document.addEventListener(
  'touchend',
  (e) => {
    const p = e.changedTouches[0]
    if (!p || e.touches.length) return
    const quick = e.timeStamp - lastTap.t < 350 && Math.abs(p.clientX - lastTap.x) < 40 && Math.abs(p.clientY - lastTap.y) < 40
    lastTap = { t: e.timeStamp, x: p.clientX, y: p.clientY }
    const el = e.target as HTMLElement
    if (!quick || el.closest('input, textarea, select, [contenteditable], .touch-none')) return
    e.preventDefault()
    el.click()
  },
  { passive: false },
)

// 访客页（R-281）：/s/<token> 走单独的根，不挂所有者的登录、导航、草稿和撤销条，也不调所有者的接口。
const GuestApp = lazy(() => import('./guest/GuestApp'))
const guestMode = window.location.pathname.startsWith('/s/')

function renderGuest() {
  const qc: QueryClient = new QueryClient({
    // 访客的接口返回 401（要重新输密码）或 410（链接已失效）时，重新确认访问信息，页面随之换成输密码或被拒绝
    queryCache: new QueryCache({
      onError: (err, q) => {
        if (q.queryKey[2] !== 'meta' && err instanceof ApiError && (err.status === 401 || err.status === 410)) void qc.invalidateQueries({ queryKey: ['guest', q.queryKey[1], 'meta'] })
      },
    }),
    defaultOptions: { queries: { staleTime: 15_000, refetchOnWindowFocus: true } },
  })
  createRoot(document.getElementById('root')!).render(
    <StrictMode>
      <QueryClientProvider client={qc}>
        <BrowserRouter>
          <TipProvider delayDuration={700} skipDelayDuration={300}>
            <Suspense fallback={null}>
              <GuestApp />
            </Suspense>
          </TipProvider>
          <Toaster position="top-center" richColors closeButton toastOptions={{ className: 'font-sans' }} />
        </BrowserRouter>
      </QueryClientProvider>
    </StrictMode>,
  )
}

const queryClient = new QueryClient({
  defaultOptions: {
    queries: {
      staleTime: 15_000,
      refetchOnWindowFocus: true,
      retry: (count, err) => !(err instanceof ApiError && err.status < 500) && count < 2,
    },
  },
})

// 撤销期结束、提交成功后，等数据请求都回来再撤掉本地的「已处理」（R-266）
setSettle(() => waitIdle({ busy: () => queryClient.isFetching() > 0, sleep: (ms) => new Promise((ok) => window.setTimeout(ok, ms)), now: () => Date.now() }))

// 任何接口返回 401：把登录状态置为未登录，路由守卫会跳回登录页。
setUnauthorizedHandler(() => {
  const cur = queryClient.getQueryData<{ initialized: boolean; logged_in: boolean; version: string }>(qk.auth)
  if (cur?.logged_in !== false) queryClient.setQueryData(qk.auth, { initialized: true, version: cur?.version ?? '', logged_in: false })
})

if (guestMode) renderGuest()
else createRoot(document.getElementById('root')!).render(
  <StrictMode>
    <QueryClientProvider client={queryClient}>
      <BrowserRouter>
        <TipProvider delayDuration={700} skipDelayDuration={300}>
          <ConfirmProvider>
            <OutboxProvider>
              <App />
            </OutboxProvider>
            {/* 撤销期的顶部浮动条：受影响的元素不在屏幕上时才出现（R-201） */}
            <UndoDock />
          </ConfirmProvider>
        </TipProvider>
        <Toaster position="top-center" richColors closeButton toastOptions={{ className: 'font-sans' }} />
      </BrowserRouter>
    </QueryClientProvider>
  </StrictMode>,
)
