import { Navigate, Route, Routes, useLocation } from 'react-router'
import { useAuthStatus } from './api/queries'
import { t } from './i18n'
import { Shell } from './app/Shell'
import { ErrorState } from './ui/base'
import InboxPage from './pages/Inbox'
import LoginPage from './pages/Login'
import RequirementsPage from './pages/Requirements'
import SessionsPage from './pages/Sessions'
import SettingsPage from './pages/Settings'

function Splash() {
  return (
    <div className="flex min-h-dvh items-center justify-center">
      <div className="size-8 animate-pulse rounded-xl bg-accent/70" aria-label={t.app.name} />
    </div>
  )
}

export default function App() {
  const auth = useAuthStatus()
  const loc = useLocation()

  if (auth.isPending) return <Splash />
  if (auth.isError)
    return (
      <div className="mx-auto max-w-md p-6 pt-24">
        <ErrorState error={auth.error} onRetry={() => auth.refetch()} />
      </div>
    )

  const { logged_in, initialized, version } = auth.data
  const next = loc.pathname === '/login' ? '/' : loc.pathname + loc.search

  return (
    <Routes>
      <Route path="/login" element={logged_in ? <Navigate to={new URLSearchParams(loc.search).get('next') || '/'} replace /> : <LoginPage initialized={initialized} />} />
      <Route element={logged_in ? <Shell /> : <Navigate to={`/login?next=${encodeURIComponent(next)}`} replace />}>
        <Route index element={<InboxPage />} />
        <Route path="sessions" element={<SessionsPage />} />
        <Route path="requirements" element={<RequirementsPage />} />
        <Route path="settings" element={<SettingsPage version={version} />} />
        <Route path="decisions" element={<Navigate to="/" replace />} />
        <Route path="*" element={<Navigate to="/" replace />} />
      </Route>
    </Routes>
  )
}
