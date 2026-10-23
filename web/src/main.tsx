import { StrictMode } from 'react'
import { createRoot } from 'react-dom/client'
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import { BrowserRouter } from 'react-router'
import { Toaster } from 'sonner'
import { ApiError, setUnauthorizedHandler } from './api/client'
import { qk } from './api/queries'
import { OutboxProvider } from './app/outbox'
import { initTheme } from './lib/theme'
import { registerSW } from './lib/push'
import { ConfirmProvider } from './ui/overlay'
import { TipProvider } from './ui/base'
import App from './App'
import './index.css'

initTheme()
registerSW()

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

const queryClient = new QueryClient({
  defaultOptions: {
    queries: {
      staleTime: 15_000,
      refetchOnWindowFocus: true,
      retry: (count, err) => !(err instanceof ApiError && err.status < 500) && count < 2,
    },
  },
})

// 任何接口返回 401：把登录状态置为未登录，路由守卫会跳回登录页。
setUnauthorizedHandler(() => {
  const cur = queryClient.getQueryData<{ initialized: boolean; logged_in: boolean; version: string }>(qk.auth)
  if (cur?.logged_in !== false) queryClient.setQueryData(qk.auth, { initialized: true, version: cur?.version ?? '', logged_in: false })
})

createRoot(document.getElementById('root')!).render(
  <StrictMode>
    <QueryClientProvider client={queryClient}>
      <BrowserRouter>
        <TipProvider delayDuration={350} skipDelayDuration={200}>
          <ConfirmProvider>
            <OutboxProvider>
              <App />
            </OutboxProvider>
          </ConfirmProvider>
        </TipProvider>
        <Toaster position="top-center" richColors closeButton toastOptions={{ className: 'font-sans' }} />
      </BrowserRouter>
    </QueryClientProvider>
  </StrictMode>,
)
