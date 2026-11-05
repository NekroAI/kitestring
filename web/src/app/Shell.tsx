import { useEffect } from 'react'
import { NavLink, Outlet, useLocation } from 'react-router'
import { RefIndexProvider } from '../ui/refmark'
import { ArrowLeftRight, FolderKanban, LayoutDashboard, ListTodo, Monitor, PanelLeftClose, PanelLeftOpen, Search, Settings2, Share2, WifiOff } from 'lucide-react'
import { useCommsPending, useLiveUpdates } from '../api/queries'
import { t } from '../i18n'
import { cx, relTime, useNow, usePref } from '../lib/util'
import { Tip } from '../ui/base'
import { useWorkbench } from './inbox'
import { useTodo } from './desk'
import { usePanelParams } from './nav'
import { AwayBar } from './brief'
import { planState, PlanPanel } from './plans'
import { ApprovalPanel } from './approval'
import { usePlans } from '../api/queries'
import { ReqPanel } from './reqDrawer'
import { SessionPanel } from './SessionPanel'
import { useDraftSweep } from './draft'
import { useCompactSync } from './assign'
import { CommandProvider, KeyCaps, modKey, useCommand, withKey } from './command'

// 页面切换不做进出场动画：路由直接切换，避免闪烁与白屏。

const nav: { to: string; label: string; icon: typeof Share2; end?: boolean; keys?: string; desktop?: boolean }[] = [
  { to: '/', label: t.nav.home, icon: LayoutDashboard, end: true, keys: 'G H' },
  { to: '/projects', label: t.nav.projects, icon: FolderKanban, keys: 'G P' },
  { to: '/requirements', label: t.nav.requirements, icon: ListTodo, keys: 'G R' },
  { to: '/comms', label: t.nav.comms, icon: ArrowLeftRight, keys: 'G C' },
  // 分享（R-281）：只在桌面侧边栏；手机底部导航保持 5 格，入口在设置页
  { to: '/shares', label: t.nav.shares, icon: Share2, desktop: true },
  { to: '/settings', label: t.nav.settings, icon: Settings2, keys: 'G S' },
]

// Logo：折纸菱形鸢，四个折面靠明暗分出折痕。
export function Logo({ className }: { className?: string }) {
  return (
    <svg viewBox="0 0 128 128" className={className} aria-hidden>
      <g transform="rotate(-10 64 64)">
        <polygon points="64,10 22,54 64,54" fill="#D9503D" />
        <polygon points="64,10 64,54 106,54" fill="#EE7A60" />
        <polygon points="22,54 64,118 64,54" fill="#A9311F" />
        <polygon points="64,54 64,118 106,54" fill="#C2402D" />
      </g>
    </svg>
  )
}

function Count({ n, tone = 'seal' }: { n: number; tone?: 'seal' | 'wait' }) {
  if (!n) return null
  return <span key={n} className={cx('pop flex h-[18px] min-w-[18px] items-center justify-center rounded-full px-1 text-[11px] leading-none font-semibold tabular', tone === 'wait' ? 'bg-wait text-white dark:text-bg' : 'bg-seal text-seal-fg')}>{n > 99 ? '99+' : n}</span>
}

// 通讯旁的黄色数字（R-249）：还在等对方醒来的消息。只是提示，不进「要你处理」，所以用黄色而不是朱砂
// 分享旁的数字只数待收的访客留言（R-281 第 4 段接上），同样用黄色
function navCount(to: string, todo: number, unsent: number): { n: number; tone: 'seal' | 'wait'; tip?: string } {
  if (to === '/') return { n: todo, tone: 'seal' }
  if (to === '/comms') return { n: unsent, tone: 'wait', tip: unsent ? t.nav.commsTip(unsent) : undefined }
  return { n: 0, tone: 'seal' }
}

// 侧边栏可折叠成图标栏（R-18）：⌘\ 或底部按钮切换，记在本机。折叠后名称在悬停提示里。
function Sidebar({ todo, unsent, connected, collapsed, onToggle }: { todo: number; unsent: number; connected: boolean; collapsed: boolean; onToggle: () => void }) {
  const wb = useWorkbench()
  const now = useNow()
  const { openPalette } = useCommand()
  const c = collapsed
  return (
    <aside className={cx('fixed inset-y-0 left-0 z-30 hidden flex-col overflow-hidden border-r border-line bg-sunken/60 py-4 transition-[width] duration-200 ease-out lg:flex', c ? 'w-16 px-2.5' : 'w-[232px] px-3')}>
      <div className={cx('mb-6 flex items-center gap-2.5', c ? 'justify-center' : 'px-2')}>
        <Logo className="size-8 shrink-0" />
        {!c && (
          <div className="leading-tight whitespace-nowrap">
            <div className="font-display text-[17px] font-semibold tracking-wider text-fg">{t.app.name}</div>
            <div className="text-[11px] text-fg-3">Kitestring</div>
          </div>
        )}
      </div>
      {/* 搜索与命令（R-127）：⌘K 也能打开 */}
      <Tip side="right" content={c ? withKey(t.cmd.open, 'mod K') : null}>
        <button
          onClick={openPalette}
          aria-label={t.cmd.open}
          className={cx('mb-3 flex h-9 w-full items-center gap-2.5 rounded-lg border border-line bg-surface/70 text-sm whitespace-nowrap text-fg-3 transition-colors hover:border-line-strong hover:text-fg-2', c ? 'justify-center' : 'px-2.5')}
        >
          <Search className="size-4 shrink-0" />
          {!c && (
            <>
              <span className="flex-1 text-left">{t.cmd.open}</span>
              <KeyCaps keys={[modKey, 'K']} />
            </>
          )}
        </button>
      </Tip>
      <nav className="space-y-0.5">
        {nav.map((n) => {
          const k = navCount(n.to, todo, unsent)
          return (
          <Tip key={n.to} side="right" content={c ? (n.keys ? withKey(k.tip ? `${n.label} · ${k.tip}` : n.label, n.keys) : n.label) : null}>
            {/* 外面包一层：Tip 的 asChild 会把 NavLink 的函数 className 拼成字符串 */}
            <div>
              <NavLink
                to={n.to}
                end={n.end}
                viewTransition
                aria-label={n.label}
                className={({ isActive }) => cx('relative flex h-9 items-center gap-2.5 rounded-lg text-sm font-medium whitespace-nowrap transition-colors', c ? 'justify-center' : 'px-2.5', isActive ? 'bg-surface text-fg shadow-xs' : 'text-fg-2 hover:bg-hover hover:text-fg')}
              >
                <n.icon className="size-[18px] shrink-0" />
                {!c && <span className="flex-1">{n.label}</span>}
                {c ? k.n > 0 && <span className={cx('absolute top-1.5 right-2 size-2 rounded-full', k.tone === 'wait' ? 'bg-wait' : 'bg-seal')} /> : <Count n={k.n} tone={k.tone} />}
              </NavLink>
            </div>
          </Tip>
          )
        })}
      </nav>

      {!c && <div className="mt-8 px-2.5 text-xs font-medium whitespace-nowrap text-fg-3">{t.settings.sections.devices}</div>}
      <div className={cx('space-y-0.5', c ? 'mt-6 border-t border-line pt-4' : 'mt-1.5')}>
        {wb.devices
          .filter((d) => !d.revoked)
          .map((d) => {
            const n = wb.sessions.filter((s) => s.device_id === d.id && s.state !== 'ended').length
            const status = d.online ? t.device.online : d.last_seen ? t.device.lastSeen(relTime(d.last_seen, now)) : t.device.never
            return (
              <Tip key={d.id} side="right" content={c ? `${d.name} · ${status}${n ? ` · ${t.device.sessions(n)}` : ''}` : status}>
                <div className={cx('flex items-center gap-2.5 rounded-lg py-1.5 text-[13px] whitespace-nowrap', c ? 'justify-center' : 'px-2.5')}>
                  <span className="relative shrink-0">
                    <Monitor className="size-4 text-fg-3" />
                    <span className={cx('absolute -right-0.5 -bottom-0.5 size-2 rounded-full border-2 border-bg', d.online ? 'bg-run' : 'bg-fg-3/50')} />
                  </span>
                  {!c && <span className={cx('min-w-0 flex-1 truncate', d.online ? 'text-fg-2' : 'text-fg-3')}>{d.name}</span>}
                  {!c && n > 0 && <span className="text-xs text-fg-3 tabular">{n}</span>}
                </div>
              </Tip>
            )
          })}
      </div>

      <div className={cx('mt-auto flex items-center gap-2', c ? 'flex-col' : 'px-1')}>
        <Tip side="right" content={connected ? t.conn.live : t.conn.offline}>
          <span className={cx('flex h-8 items-center gap-1.5 px-1.5 text-xs', connected ? 'text-fg-3' : 'text-danger')} aria-label={connected ? t.conn.live : t.conn.offline}>
            <span className={cx('size-1.5 shrink-0 rounded-full', connected ? 'breathe bg-run' : 'animate-pulse bg-danger')} />
            {!connected && !c && <span className="whitespace-nowrap">{t.conn.offlineShort}</span>}
          </span>
        </Tip>
        <Tip side="right" content={`${c ? t.nav.expand : t.nav.collapse} · ⌘\\`}>
          <button onClick={onToggle} aria-label={c ? t.nav.expand : t.nav.collapse} className={cx('flex size-8 items-center justify-center rounded-lg text-fg-3 transition hover:bg-hover hover:text-fg', !c && 'ml-auto')}>
            {c ? <PanelLeftOpen className="size-[18px]" /> : <PanelLeftClose className="size-[18px]" />}
          </button>
        </Tip>
      </div>
    </aside>
  )
}

function TabBar({ todo, unsent }: { todo: number; unsent: number }) {
  const { pathname } = useLocation()
  // 分享页从设置页进入，底部导航把它算在「设置」下
  const inSettings = pathname.startsWith('/shares')
  return (
    <nav className="pb-safe fixed inset-x-0 bottom-0 z-30 border-t border-line bg-surface/90 backdrop-blur-xl lg:hidden">
      <div className="mx-auto grid max-w-md grid-cols-5">
        {nav.filter((n) => !n.desktop).map((n) => (
          <NavLink key={n.to} to={n.to} end={n.end} viewTransition className={({ isActive }) => cx('flex flex-col items-center gap-0.5 pt-2 pb-1.5 text-[11px] font-medium transition-colors', isActive || (inSettings && n.to === '/settings') ? 'text-accent' : 'text-fg-3')}>
            <span className="relative">
              <n.icon className="size-[22px]" strokeWidth={1.8} />
              <span className="absolute -top-1.5 -right-2.5">
                <Count n={navCount(n.to, todo, unsent).n} tone={navCount(n.to, todo, unsent).tone} />
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
  const { openPalette } = useCommand()
  return (
    <header className="pt-safe sticky top-0 z-20 border-b border-line/70 bg-bg/85 backdrop-blur-xl lg:hidden">
      <div className="flex h-12 items-center gap-2 px-4">
        <Logo className="size-6" />
        <span className="font-display text-[17px] font-semibold tracking-wider text-fg">{t.app.name}</span>
        {!connected && (
          <span className="ml-auto flex items-center gap-1.5 text-xs text-danger">
            <WifiOff className="size-4" />
            {t.conn.offline}
          </span>
        )}
        <button onClick={openPalette} aria-label={t.cmd.open} className={cx('-mr-2 flex size-10 items-center justify-center rounded-lg text-fg-2 transition active:scale-95 active:bg-hover', connected && 'ml-auto')}>
          <Search className="size-5" />
        </button>
      </div>
    </header>
  )
}

export function Shell() {
  const { pathname } = useLocation()
  const now = useNow()
  const connected = useLiveUpdates(true)
  useDraftSweep() // 对象关闭后自动清掉它的输入草稿（R-202）
  useCompactSync() // 上下文圆环 / 建议压缩用设置里的阈值（R-224、R-225）
  const wb = useWorkbench()
  const todo = useTodo(wb).length // 与工作台「待你处理」同一口径
  const unsent = useCommsPending().data ?? 0
  const { session, req, plan, ask, close } = usePanelParams()
  // 在等你的计划（待批准、待阶段验收）一律在审批抽屉里处理（R-177）：?plan= 打开它时落到审批抽屉
  const plans = usePlans()
  const px = plan ? (plans.data ?? []).find((x) => x.code === plan) : undefined
  const planAsk = px && planState(px).needsYou ? `${planState(px).tone === 'approve' ? 'p' : 'g'}:${px.code}` : null
  const [collapsed, setCollapsed] = usePref('ks.sidebar.collapsed', false)
  // 侧边栏宽度给其它固定定位的元素用（如工作台底部的批量操作条）
  useEffect(() => document.documentElement.style.setProperty('--sidebar-w', collapsed ? '4rem' : '232px'), [collapsed])
  useEffect(() => {
    const onKey = (e: KeyboardEvent) => {
      if ((e.metaKey || e.ctrlKey) && e.key === '\\') {
        e.preventDefault()
        setCollapsed(!collapsed)
      }
    }
    window.addEventListener('keydown', onKey)
    return () => window.removeEventListener('keydown', onKey)
  }, [collapsed, setCollapsed])

  useEffect(() => {
    document.title = todo ? `(${todo}) ${t.app.fullName}` : t.app.fullName
  }, [todo])

  return (
    <RefIndexProvider>
    <CommandProvider>
    <div className="min-h-dvh">
      <Sidebar todo={todo} unsent={unsent} connected={connected} collapsed={collapsed} onToggle={() => setCollapsed(!collapsed)} />
      <MobileHeader connected={connected} />
      <main className={cx('pb-24 transition-[padding] duration-200 ease-out', pathname === '/' ? 'lg:pb-5' : 'lg:pb-10', collapsed ? 'lg:pl-16' : 'lg:pl-[232px]')}>
        {/* 工作台是一屏驾驶舱，用满任意宽度（R-170，去掉原来 1880px 的上限）、需求池盘面随屏幕一直放宽到 3600px（R-133，2K / 4K / 21:9 用满），其余页面保持 1360px 的阅读宽度 */}
        <div className={cx('mx-auto px-4 pt-5 sm:px-6 lg:px-10', pathname === '/' ? 'lg:pt-6' : 'lg:pt-9', pathname === '/' ? '' : pathname === '/requirements' ? 'max-w-[3600px]' : 'max-w-[1360px]')}>
          {pathname !== '/' && <AwayBar now={now} />}
          <Outlet />
        </div>
      </main>
      <TabBar todo={todo} unsent={unsent} />
      <SessionPanel sessionKey={session} onClose={close} />
      <ReqPanel refId={req} onClose={close} />
      <PlanPanel code={planAsk ? null : plan} onClose={close} />
      <ApprovalPanel askKey={ask ?? planAsk} onClose={close} />
    </div>
    </CommandProvider>
    </RefIndexProvider>
  )
}
