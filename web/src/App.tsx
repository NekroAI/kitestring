import { Navigate, Route, Routes, useLocation } from 'react-router'
import { useAuthStatus } from './api/queries'
import { Layout } from './components/Layout'
import { ErrorBox, Loading } from './components/ui'
import LoginPage from './pages/Login'
import WallPage from './pages/Wall'
import DecisionsPage from './pages/Decisions'
import RequirementsPage from './pages/Requirements'
import SettingsPage from './pages/Settings'

export default function App() {
  const auth = useAuthStatus()
  const loc = useLocation()

  if (auth.isPending) return <Loading />
  if (auth.isError)
    return (
      <div className="mx-auto max-w-md p-4">
        <ErrorBox error={auth.error} onRetry={() => auth.refetch()} />
      </div>
    )

  const loggedIn = auth.data.logged_in
  const next = loc.pathname === '/login' ? '/' : loc.pathname + loc.search

  return (
    <Routes>
      <Route path="/login" element={loggedIn ? <Navigate to={new URLSearchParams(loc.search).get('next') || '/'} replace /> : <LoginPage initialized={auth.data.initialized} />} />
      <Route element={loggedIn ? <Layout /> : <Navigate to={`/login?next=${encodeURIComponent(next)}`} replace />}>
        <Route index element={<WallPage />} />
        <Route path="decisions" element={<DecisionsPage />} />
        <Route path="requirements" element={<RequirementsPage />} />
        <Route path="settings" element={<SettingsPage version={auth.data.version} />} />
        <Route path="*" element={<Navigate to="/" replace />} />
      </Route>
    </Routes>
  )
}
