import { useEffect, type ReactNode } from 'react'
import { NavLink, Outlet, useLocation } from 'react-router'
import { AnimatePresence, motion } from 'motion/react'
import { Inbox, LayoutGrid, ListTodo, Monitor, Settings2, WifiOff } from 'lucide-react'
import { useLiveUpdates } from '../api/queries'
import { t } from '../i18n'
import { cx, relTime, useNow } from '../lib/util'
import { useInbox } from './inbox'
import { usePanelParams } from './nav'
import { ReqPanel } from './requirements'
import { SessionPanel } from './SessionPanel'

const nav = [
  { to: '/', label: t.nav.inbox, icon: Inbox, end: true },
  { to: '/sessions', label: t.nav.sessions, icon: LayoutGrid },
  { to: '/requirements', label: t.nav.requirements, icon: ListTodo },
  { to: '/settings', label: t.nav.settings, icon: Settings2 },
]

function Logo({ className }: { className?: string }) {
  return (
    <svg viewBox="0 0 32 32" className={className} aria-hidden>
      <rect width="32" height="32" rx="9" className="fill-accent" />
      <path d="M16 5l8 9-8 7-8-7z" className="fill-accent-fg" opacity="0.95" />
      <path d="M16 21c0 2.5-1.2 4.2-3.5 5.5" fill="none" className="stroke-accent-fg" strokeWidth="1.6" strokeLinecap="round" />
    </svg>
  )
}

function CountDot({ n, tone = 'accent' }: { n: number; tone?: 'accent' | 'wait' }) {
  if (!n) return null
  return (
    <motion.span
      key={n}
      initial={{ scale: 0.6 }}
      animate={{ scale: 1 }}
      className={cx('flex h-[18px] min-w-[18px] items-center justify-center rounded-full px-1 text-[11px] leading-none font-semibold tabular', tone === 'wait' ? 'bg-wait text-white dark:text-black' : 'bg-accent text-accent-fg')}
    >
      {n > 99 ? '99+' : n}
    </motion.span>
  )
}

function Sidebar({ counts, connected }: { counts: Record<string, number>; connected: boolean }) {
  const inbox = useInbox()
  const now = useNow()
  return (
    <aside className="fixed inset-y-0 left-0 z-30 hidden w-[232px] flex-col border-r border-line bg-sunken/60 px-3 py-4 lg:flex">
      <div className="mb-6 flex items-center gap-2.5 px-2">
        <Logo className="size-8" />
        <div>
          <div className="text-[15px] font-semibold text-fg">{t.app.name}</div>
          <div className="text-[11px] text-fg-3">Kitestring</div>
        </div>
      </div>
      <nav className="space-y-0.5">
        {nav.map((n) => (
          <NavLink
            key={n.to}
            to={n.to}
            end={n.end}
            className={({ isActive }) => cx('relative flex h-9 items-center gap-2.5 rounded-lg px-2.5 text-sm font-medium transition-colors', isActive ? 'text-fg' : 'text-fg-2 hover:bg-hover hover:text-fg')}
          >
            {({ isActive }) => (
              <>
                {isActive && <motion.span layoutId="side-active" className="absolute inset-0 rounded-lg bg-surface shadow-xs" transition={{ type: 'spring', bounce: 0.15, duration: 0.35 }} />}
                <n.icon className="relative size-[18px]" />
                <span className="relative flex-1">{n.label}</span>
                <span className="relative">
                  <CountDot n={counts[n.to] ?? 0} tone={n.to === '/' ? 'wait' : 'accent'} />
                </span>
              </>
            )}
          </NavLink>
        ))}
      </nav>

      <div className="mt-8 px-2.5 text-xs font-medium text-fg-3">{t.settings.sections.devices}</div>
      <div className="mt-2 space-y-0.5">
        {inbox.devices
          .filter((d) => !d.revoked)
          .map((d) => {
            const n = inbox.sessions.filter((s) => s.device_id === d.id && s.state !== 'ended').length
            return (
              <div key={d.id} className="flex items-center gap-2.5 rounded-lg px-2.5 py-1.5 text-[13px]" title={d.online ? t.sessions.deviceOnline : t.sessions.lastSeen(relTime(d.last_seen, now))}>
                <span className="relative">
                  <Monitor className="size-4 text-fg-3" />
                  <span className={cx('absolute -right-0.5 -bottom-0.5 size-2 rounded-full border-2 border-bg', d.online ? 'bg-run' : 'bg-fg-3/50')} />
                </span>
                <span className={cx('min-w-0 flex-1 truncate', d.online ? 'text-fg-2' : 'text-fg-3')}>{d.name}</span>
                {n > 0 && <span className="text-xs text-fg-3 tabular">{n}</span>}
              </div>
            )
          })}
      </div>

      <div className="mt-auto flex items-center gap-2 px-2.5 text-xs text-fg-3">
        <span className={cx('size-1.5 rounded-full', connected ? 'bg-run' : 'animate-pulse bg-danger')} />
        {connected ? t.conn.live : t.conn.offline}
      </div>
    </aside>
  )
}

function TabBar({ counts }: { counts: Record<string, number> }) {
  return (
    <nav className="pb-safe fixed inset-x-0 bottom-0 z-30 border-t border-line bg-surface/85 backdrop-blur-xl lg:hidden">
      <div className="mx-auto grid max-w-lg grid-cols-4">
        {nav.map((n) => (
          <NavLink key={n.to} to={n.to} end={n.end} className={({ isActive }) => cx('relative flex flex-col items-center gap-0.5 pt-2 pb-1.5 text-[11px] font-medium transition-colors', isActive ? 'text-accent' : 'text-fg-3')}>
            <span className="relative">
              <n.icon className="size-[22px]" strokeWidth={1.8} />
              <span className="absolute -top-1.5 -right-2.5">
                <CountDot n={counts[n.to] ?? 0} tone={n.to === '/' ? 'wait' : 'accent'} />
              </span>
            </span>
            {n.label}
          </NavLink>
        ))}
      </div>
    </nav>
  )
}

function MobileHeader({ connected }: { connected: boolean }) {
  return (
    <header className="pt-safe sticky top-0 z-20 border-b border-line/70 bg-bg/80 backdrop-blur-xl lg:hidden">
      <div className="flex h-12 items-center gap-2 px-4">
        <Logo className="size-6" />
        <span className="text-[15px] font-semibold text-fg">{t.app.name}</span>
        <span className="ml-auto">{!connected && <WifiOff className="size-4 animate-pulse text-danger" aria-label={t.conn.offline} />}</span>
      </div>
    </header>
  )
}

export function Shell({ children }: { children?: ReactNode }) {
  const connected = useLiveUpdates(true)
  const inbox = useInbox()
  const loc = useLocation()
  const { session, req, close } = usePanelParams()
  const counts = { '/': inbox.urgent, '/sessions': inbox.running.length }

  useEffect(() => {
    document.title = inbox.urgent ? `(${inbox.urgent}) ${t.app.fullName}` : t.app.fullName
  }, [inbox.urgent])

  return (
    <div className="min-h-dvh">
      <Sidebar counts={counts} connected={connected} />
      <MobileHeader connected={connected} />
      {!connected && <div className="sticky top-12 z-10 bg-danger-soft px-4 py-1.5 text-center text-xs text-danger lg:hidden">{t.conn.offline}</div>}
      <main className="pb-24 lg:pb-10 lg:pl-[232px]">
        <AnimatePresence mode="wait" initial={false}>
          <motion.div key={loc.pathname} initial={{ opacity: 0, y: 6 }} animate={{ opacity: 1, y: 0 }} exit={{ opacity: 0 }} transition={{ duration: 0.18, ease: 'easeOut' }} className="mx-auto max-w-[1360px] px-4 pt-5 sm:px-6 lg:px-10 lg:pt-9">
            {children ?? <Outlet />}
          </motion.div>
        </AnimatePresence>
      </main>
      <TabBar counts={counts} />
      <SessionPanel sessionKey={session} onClose={close} />
      <ReqPanel refId={req} onClose={close} />
    </div>
  )
}
