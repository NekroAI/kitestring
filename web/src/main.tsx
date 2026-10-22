import { StrictMode } from 'react'
import { createRoot } from 'react-dom/client'
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import { BrowserRouter } from 'react-router'
import { Toaster } from 'sonner'
import { ApiError, setUnauthorizedHandler } from './api/client'
import { qk } from './api/queries'
import { OutboxProvider } from './app/outbox'
import { initTheme } from './lib/theme'
import { ConfirmProvider } from './ui/overlay'
import App from './App'
import './index.css'

initTheme()

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
        <ConfirmProvider>
          <OutboxProvider>
            <App />
          </OutboxProvider>
        </ConfirmProvider>
        <Toaster position="top-center" richColors closeButton toastOptions={{ className: 'font-sans' }} />
      </BrowserRouter>
    </QueryClientProvider>
  </StrictMode>,
)
