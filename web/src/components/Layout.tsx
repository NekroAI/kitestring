import { NavLink, Outlet } from 'react-router'
import type { ReactNode } from 'react'
import { useDecisions, useLiveUpdates } from '../api/queries'
import { t } from '../i18n'
import { CountBadge, cx } from './ui'

const icons: Record<string, ReactNode> = {
  wall: (
    <svg viewBox="0 0 24 24" className="size-6" fill="none" stroke="currentColor" strokeWidth="1.7" strokeLinejoin="round">
      <rect x="3.5" y="4" width="7" height="7" rx="1.5" />
      <rect x="13.5" y="4" width="7" height="7" rx="1.5" />
      <rect x="3.5" y="14" width="7" height="6" rx="1.5" />
      <rect x="13.5" y="14" width="7" height="6" rx="1.5" />
    </svg>
  ),
  decisions: (
    <svg viewBox="0 0 24 24" className="size-6" fill="none" stroke="currentColor" strokeWidth="1.7" strokeLinejoin="round" strokeLinecap="round">
      <path d="M4 13l2.5-8h11L20 13v6H4z" />
      <path d="M4 13h5l1 2h4l1-2h5" />
    </svg>
  ),
  requirements: (
    <svg viewBox="0 0 24 24" className="size-6" fill="none" stroke="currentColor" strokeWidth="1.7" strokeLinecap="round">
      <path d="M9 6h11M9 12h11M9 18h11" />
      <path d="M4 6h.01M4 12h.01M4 18h.01" strokeWidth="2.6" />
    </svg>
  ),
  settings: (
    <svg viewBox="0 0 24 24" className="size-6" fill="none" stroke="currentColor" strokeWidth="1.7" strokeLinecap="round">
      <path d="M4 7h10M18 7h2M4 17h4M12 17h8" />
      <circle cx="16" cy="7" r="2" />
      <circle cx="10" cy="17" r="2" />
    </svg>
  ),
}

export function KiteMark({ className }: { className?: string }) {
  return (
    <svg viewBox="0 0 512 512" className={className} aria-hidden="true">
      <rect width="512" height="512" rx="112" className="fill-slate-900 dark:fill-slate-100" />
      <path d="M300 72 L412 196 L292 316 L188 188 Z" className="fill-white dark:fill-slate-900" />
      <path d="M292 316 C 262 360, 300 392, 250 420 S 170 440, 116 452" fill="none" stroke="#38bdf8" strokeWidth="18" strokeLinecap="round" />
    </svg>
  )
}

export function Layout() {
  const live = useLiveUpdates(true)
  const pending = useDecisions('pending')
  const pendingN = pending.data?.length ?? 0

  const items = [
    { to: '/', key: 'wall', label: t.nav.wall, badge: 0 },
    { to: '/decisions', key: 'decisions', label: t.nav.decisions, badge: pendingN },
    { to: '/requirements', key: 'requirements', label: t.nav.requirements, badge: 0 },
    { to: '/settings', key: 'settings', label: t.nav.settings, badge: 0 },
  ]

  return (
    <div className="min-h-dvh">
      <header className="pt-safe sticky top-0 z-30 border-b border-slate-200 bg-white/90 backdrop-blur dark:border-slate-800 dark:bg-[#0b0f14]/90">
        <div className="mx-auto flex h-12 max-w-6xl items-center gap-4 px-4">
          <div className="flex items-center gap-2 font-semibold">
            <KiteMark className="size-6" />
            <span>{t.app.name}</span>
          </div>
          <nav className="hidden items-center gap-1 sm:flex">
            {items.map((it) => (
              <NavLink
                key={it.key}
                to={it.to}
                end={it.to === '/'}
                className={({ isActive }) =>
                  cx(
                    'inline-flex h-9 items-center gap-1.5 rounded-lg px-3 text-sm font-medium',
                    isActive ? 'bg-slate-900 text-white dark:bg-slate-100 dark:text-slate-900' : 'text-slate-600 hover:bg-slate-100 dark:text-slate-300 dark:hover:bg-slate-800',
                  )
                }
              >
                {it.label}
                <CountBadge n={it.badge} />
              </NavLink>
            ))}
          </nav>
          <div className="ml-auto flex items-center gap-2 text-xs" title={live ? t.nav.live : t.nav.offline}>
            <span className={cx('size-2 rounded-full', live ? 'bg-emerald-500' : 'animate-pulse bg-red-500')} />
            {!live && <span className="text-red-600 dark:text-red-400">{t.nav.offline}</span>}
          </div>
        </div>
      </header>

      <main className="mx-auto max-w-6xl px-4 pt-4 pb-28 sm:pb-12">
        <Outlet />
      </main>

      <nav className="pb-safe fixed inset-x-0 bottom-0 z-30 border-t border-slate-200 bg-white/95 backdrop-blur sm:hidden dark:border-slate-800 dark:bg-[#0b0f14]/95">
        <div className="grid grid-cols-4">
          {items.map((it) => (
            <NavLink
              key={it.key}
              to={it.to}
              end={it.to === '/'}
              className={({ isActive }) =>
                cx('relative flex h-14 flex-col items-center justify-center gap-0.5 text-[11px] font-medium', isActive ? 'text-slate-900 dark:text-white' : 'text-slate-500 dark:text-slate-400')
              }
            >
              {icons[it.key]}
              {it.label}
              {it.badge > 0 && (
                <span className="absolute top-1.5 left-1/2 ml-2">
                  <CountBadge n={it.badge} />
                </span>
              )}
            </NavLink>
          ))}
        </div>
      </nav>
    </div>
  )
}
