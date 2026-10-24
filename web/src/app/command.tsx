import { createContext, useCallback, useContext, useEffect, useMemo, useRef, useState, type ReactNode } from 'react'
import { useLocation, useNavigate, useSearchParams } from 'react-router'
import { Dialog as RDialog } from 'radix-ui'
import { ArrowRight, Check, FilePlus2, FolderKanban, Keyboard, LayoutDashboard, LayoutGrid, List, ListTodo, Monitor, Moon, Search, Settings2, Sun, Undo2, X } from 'lucide-react'
import { useDecisions, usePlans, useProjects, useRequirements, useSessions } from '../api/queries'
import { t } from '../i18n'
import { helpGroups, isTyping, resolveKey, type KeyAction } from '../lib/keys'
import { flatten, search, type DocKind, type SearchDoc } from '../lib/search'
import { useTheme } from '../lib/theme'
import { cx, loadPref, savePref, shortPath, useDesktop } from '../lib/util'
import { Button } from '../ui/base'
import { FullSheet } from '../ui/overlay'
import { RoleTag, StatusDot, displayState, presence } from '../ui/marks'
import { PlanGlyph, ProjGlyph, RefChip, ReqStatus, TurnSeal, useOpenRef } from '../ui/refmark'
import { useOpenPlan, useOpenReq, useOpenSession } from './nav'
import { NewReqModal } from './requirements'
import { PlanEditor } from './plans'
import { sessionTitle } from './SessionCard'

// 命令面板与全局快捷键（R-127）。⌘K / Ctrl+K 打开面板：搜需求、决策、计划、会话、项目，也能执行常用操作；
// ? 打开快捷键表。单键只在焦点不在输入框、菜单里且没有对话框开着时生效（判定见 lib/keys.ts）。
// 抽屉、页面通过 window 事件接动作：ks:req-step（上下一条）、ks:pass（批准，走撤销期）、ks:revise（聚焦退回意见）、ks:back（⌘⌫ 退回）、ks:reqs-view（切视图）。

export const isMac = typeof navigator !== 'undefined' && /Mac|iPhone|iPad/.test(navigator.platform || navigator.userAgent)
export const modKey = isMac ? '⌘' : 'Ctrl'
const keyCap = (k: string) => (k === 'mod' ? modKey : k)

// 悬停提示里带上按键：「新建需求 · N」。触屏上不带。
const coarse = typeof matchMedia !== 'undefined' && matchMedia('(pointer: coarse)').matches
export const withKey = (label: string, keys: string) => (coarse ? label : `${label} · ${keys.replace('mod', modKey)}`)

export const emit = (name: string, detail?: unknown) => window.dispatchEvent(new CustomEvent(name, { detail }))

// useKsEvent：页面、抽屉订阅上面的动作事件。
export function useKsEvent(name: string, fn: (detail: unknown) => void, enabled = true) {
  const ref = useRef(fn)
  ref.current = fn
  useEffect(() => {
    if (!enabled) return
    const on = (e: Event) => ref.current((e as CustomEvent).detail)
    window.addEventListener(name, on)
    return () => window.removeEventListener(name, on)
  }, [name, enabled])
}

type Ctx = { openPalette: () => void; openHelp: () => void }
const CommandCtx = createContext<Ctx>({ openPalette: () => {}, openHelp: () => {} })
export const useCommand = () => useContext(CommandCtx)

// J / K 在页面上的可选项之间移动焦点（会话卡、需求块、列表行、计划卡都带 data-nav-item），Enter 由它们自己处理。
function stepFocus(dir: 1 | -1) {
  const items = Array.from(document.querySelectorAll<HTMLElement>('main [data-nav-item]')).filter((el) => el.getClientRects().length > 0)
  if (!items.length) return
  const cur = (document.activeElement as HTMLElement | null)?.closest<HTMLElement>('[data-nav-item]')
  const i = cur ? items.indexOf(cur) : -1
  const next = i < 0 ? (dir > 0 ? items[0] : items[items.length - 1]) : items[Math.min(items.length - 1, Math.max(0, i + dir))]
  next.focus({ preventScroll: true })
  next.scrollIntoView({ block: 'nearest', behavior: 'smooth' })
}

const viewOrder = ['pool', 'list', 'plans'] as const

export function CommandProvider({ children }: { children: ReactNode }) {
  const [palette, setPalette] = useState(false)
  const [help, setHelp] = useState(false)
  const [newReq, setNewReq] = useState(false)
  const [newPlan, setNewPlan] = useState(false)
  const nav = useNavigate()
  const { pathname } = useLocation()
  const [params] = useSearchParams()
  const reqs = useRequirements().data
  const pendingG = useRef(0)
  const desktop = useDesktop()

  const drawerReq = params.get('r')
  const otherPanel = !!(params.get('s') || params.get('plan'))
  const cur = drawerReq ? reqs?.find((r) => r.code === drawerReq) : undefined
  const defaultProject = pathname.startsWith('/projects/') ? pathname.split('/')[2] : pathname === '/requirements' ? loadPref('ks.reqs.project', '') || undefined : undefined

  const run = useCallback(
    (a: KeyAction) => {
      switch (a) {
        case 'palette':
          return setPalette((o) => !o)
        case 'help':
          return setHelp(true)
        case 'new-req':
          return setNewReq(true)
        case 'go-home':
          return nav('/')
        case 'go-projects':
          return nav('/projects')
        case 'go-reqs':
          return nav('/requirements')
        case 'go-settings':
          return nav('/settings')
        case 'next':
        case 'prev':
          if (drawerReq) return emit('ks:req-step', a === 'next' ? 1 : -1)
          if (!otherPanel) stepFocus(a === 'next' ? 1 : -1)
          return
        case 'pass':
          return emit('ks:pass', drawerReq)
        case 'revise':
          return emit('ks:revise', drawerReq)
        case 'back':
          return emit('ks:back', drawerReq)
        case 'view':
          return emit('ks:reqs-view', 'next')
      }
    },
    [nav, drawerReq, otherPanel],
  )

  useEffect(() => {
    const onKey = (e: KeyboardEvent) => {
      if (e.defaultPrevented) return
      const res = resolveKey(e, {
        typing: isTyping(e.target as Element) || isTyping(document.activeElement),
        blocked: palette || help || newReq || newPlan || !!document.querySelector('[data-ks-modal]'),
        drawer: !!drawerReq,
        reviewing: cur?.status === 'review',
        reqPage: pathname === '/requirements',
        pendingG: pendingG.current > Date.now(),
      })
      pendingG.current = res.pendingG ? Date.now() + 1200 : 0
      if (!res.action) return
      // 手机上不需要快捷键：只保留 ⌘K（外接键盘的平板）
      if (!desktop && res.action !== 'palette') return
      e.preventDefault()
      run(res.action)
    }
    window.addEventListener('keydown', onKey)
    return () => window.removeEventListener('keydown', onKey)
  }, [palette, help, newReq, newPlan, drawerReq, cur?.status, pathname, run, desktop])

  const ctx = useMemo(() => ({ openPalette: () => setPalette(true), openHelp: () => setHelp(true) }), [])
  return (
    <CommandCtx.Provider value={ctx}>
      {children}
      <Palette
        open={palette}
        onClose={() => setPalette(false)}
        onAction={(a) => {
          setPalette(false)
          // 等面板关掉、焦点回到页面后再执行，抽屉里的输入框才能拿到焦点
          window.setTimeout(() => (a === 'new-plan' ? setNewPlan(true) : run(a)), 60)
        }}
        cur={cur ? { code: cur.code, status: cur.status } : undefined}
      />
      <HelpSheet open={help} onClose={() => setHelp(false)} />
      <NewReqModal open={newReq} onClose={() => setNewReq(false)} defaultProject={defaultProject} />
      <PlanEditor open={newPlan} onClose={() => setNewPlan(false)} defaultProject={defaultProject} />
    </CommandCtx.Provider>
  )
}

// ---------- 面板 ----------

type Item = SearchDoc & { icon: ReactNode; meta?: ReactNode; keys?: string; run: () => void }

function useItems(cur: { code: string; status: string } | undefined, onAction: (a: KeyAction | 'new-plan') => void, close: () => void) {
  const reqs = useRequirements().data
  const decs = useDecisions().data
  const plans = usePlans().data
  const sessions = useSessions().data?.sessions
  const projects = useProjects().data?.projects
  const openReq = useOpenReq()
  const openPlan = useOpenPlan()
  const openSession = useOpenSession()
  const openRef = useOpenRef()
  const nav = useNavigate()
  const [, setTheme] = useTheme()
  const { pathname } = useLocation()

  return useMemo(() => {
    const go = (fn: () => void) => () => {
      close()
      fn()
    }
    const cmds: Item[] = []
    if (cur?.status === 'review') {
      cmds.push({ id: 'c:pass', kind: 'cmd', title: t.cmd.passCur(cur.code), keywords: ['pass', 'accept', 'approve', '验收', '批准', '通过'], icon: <Check />, keys: 'A', run: () => onAction('pass'), boost: 40 })
      cmds.push({ id: 'c:revise', kind: 'cmd', title: t.cmd.reviseCur(cur.code), keywords: ['revise', 'reject', '打回', '退回'], icon: <Undo2 />, keys: 'R', run: () => onAction('revise'), boost: 40 })
    }
    cmds.push({ id: 'c:new-req', kind: 'cmd', title: t.cmd.newReq, keywords: ['new', 'create', '添加'], icon: <FilePlus2 />, keys: 'N', run: () => onAction('new-req') })
    cmds.push({ id: 'c:new-plan', kind: 'cmd', title: t.cmd.newPlan, keywords: ['new', 'plan', '添加'], icon: <PlanGlyph className="text-current" />, run: () => onAction('new-plan') })
    const pages: [string, string, KeyAction, ReactNode, string][] = [
      ['/', t.nav.home, 'go-home', <LayoutDashboard />, 'G H'],
      ['/projects', t.nav.projects, 'go-projects', <FolderKanban />, 'G P'],
      ['/requirements', t.nav.requirements, 'go-reqs', <ListTodo />, 'G R'],
      ['/settings', t.nav.settings, 'go-settings', <Settings2 />, 'G S'],
    ]
    for (const [to, label, a, icon, keys] of pages)
      if (to !== pathname) cmds.push({ id: `c:${a}`, kind: 'cmd', title: t.cmd.goto(label), keywords: ['go', 'goto', '跳转', '打开'], icon, keys, run: () => onAction(a) })
    const views: [string, string, ReactNode][] = [
      ['pool', t.pool.view, <LayoutGrid />],
      ['list', t.reqs.viewList, <List />],
      ['plans', t.plan.view, <PlanGlyph className="text-current" />],
    ]
    for (const [v, label, icon] of views)
      cmds.push({
        id: `c:view-${v}`,
        kind: 'cmd',
        title: t.cmd.view(label),
        keywords: ['view', '视图', '切换'],
        icon,
        run: go(() => {
          if (pathname === '/requirements') emit('ks:reqs-view', v)
          else {
            savePref('ks.reqs.view2', v)
            nav('/requirements')
          }
        }),
      })
    const themes: [Parameters<typeof setTheme>[0], string, ReactNode][] = [
      ['light', t.theme.light, <Sun />],
      ['dark', t.theme.dark, <Moon />],
      ['system', t.theme.system, <Monitor />],
    ]
    for (const [v, label, icon] of themes) cmds.push({ id: `c:theme-${v}`, kind: 'cmd', title: t.cmd.theme(label), keywords: ['theme', '主题', '外观', '深色', '浅色'], icon, run: go(() => setTheme(v)) })
    cmds.push({ id: 'c:help', kind: 'cmd', title: t.cmd.help, keywords: ['help', 'shortcut', '快捷键', '帮助'], icon: <Keyboard />, keys: '?', run: () => onAction('help') })

    const items: Item[] = [...cmds]
    for (const r of reqs ?? []) {
      const turn = r.next_actor === 'user' && r.status !== 'done' && r.status !== 'shelved'
      items.push({
        id: `r:${r.code}`,
        kind: 'req',
        code: r.code,
        title: r.title,
        fields: [r.project_name ?? '', r.plan_code ?? '', t.reqStatus[r.status] ?? ''],
        boost: turn ? 30 : r.status === 'done' || r.status === 'shelved' ? 0 : 12,
        icon: <RefChip code={r.code} status={r.status} link={false} />,
        meta: (
          <>
            {turn && <TurnSeal />}
            <ReqStatus status={r.status} />
          </>
        ),
        run: go(() => openReq(r.code)),
      })
    }
    for (const d of decs ?? [])
      items.push({
        id: `d:${d.code}`,
        kind: 'dec',
        code: d.code,
        title: d.short_title || d.question,
        fields: [d.project_name ?? '', d.requirement_code ?? '', d.question],
        boost: d.status === 'pending' ? 30 : 0,
        icon: <RefChip code={d.code} link={false} />,
        meta: d.status === 'pending' ? <TurnSeal /> : null,
        run: go(() => openRef(d.code)),
      })
    for (const p of plans ?? [])
      items.push({
        id: `p:${p.code}`,
        kind: 'plan',
        code: p.code,
        title: p.title,
        fields: [p.project_name ?? '', p.goal ?? ''],
        boost: p.status === 'active' ? 15 : 0,
        icon: <RefChip code={p.code} status={p.status} link={false} />,
        run: go(() => openPlan(p.code)),
      })
    for (const s of sessions ?? []) {
      const ds = displayState(s)
      const pr = presence(s)
      const title = sessionTitle(s)
      // 标题就是项目名（还没起标题）时，旁边写最近一句话，几个同项目的会话才分得开
      const aside = title === s.project_name ? s.last_prompt : s.project_name || shortPath(s.cwd, 24)
      items.push({
        id: `s:${s.key}`,
        kind: 'session',
        title,
        fields: [s.project_name ?? '', shortPath(s.cwd), s.branch, s.device_name, t.role[s.role] ?? '', s.last_prompt],
        boost: s.state === 'ended' ? 0 : 15,
        icon: (
          <span className="flex size-5 items-center justify-center">
            <StatusDot state={ds} />
          </span>
        ),
        meta: (
          <>
            {s.role && <RoleTag role={s.role} presence={pr || undefined} />}
            <span className="max-w-48 truncate text-xs text-fg-3">{aside}</span>
          </>
        ),
        run: go(() => openSession(s.key)),
      })
    }
    for (const p of projects ?? [])
      if (!p.hidden)
        items.push({
          id: `j:${p.id}`,
          kind: 'project',
          title: p.name,
          fields: [p.remote, p.doc_root],
          icon: (
            <span className="flex size-5 items-center justify-center">
              <ProjGlyph className="size-4" />
            </span>
          ),
          run: go(() => nav(`/projects/${p.id}`)),
        })
    return items
  }, [cur, reqs, decs, plans, sessions, projects, pathname, onAction, close, openReq, openPlan, openSession, openRef, nav, setTheme])
}

type Section = { key: string; label: string; items: Item[] }

function Palette({ open, onClose, onAction, cur }: { open: boolean; onClose: () => void; onAction: (a: KeyAction | 'new-plan') => void; cur?: { code: string; status: string } }) {
  const desktop = useDesktop()
  const [q, setQ] = useState('')
  useEffect(() => {
    if (open) setQ('')
  }, [open])
  const body = <PaletteBody q={q} setQ={setQ} onClose={onClose} onAction={onAction} cur={cur} desktop={desktop} />
  if (!desktop)
    return (
      <FullSheet
        open={open}
        onClose={onClose}
        title={t.cmd.open}
        header={
          <>
            <Search className="ml-1 size-[18px] shrink-0 text-fg-3" />
            <input
              autoFocus
              value={q}
              onChange={(e) => setQ(e.target.value)}
              placeholder={t.cmd.placeholder}
              enterKeyHint="go"
              className="h-10 min-w-0 flex-1 bg-transparent text-[16px] text-fg outline-none placeholder:text-fg-3"
            />
            <Button variant="ghost" size="sm" onClick={onClose}>
              {t.common.cancel}
            </Button>
          </>
        }
      >
        {body}
      </FullSheet>
    )
  return (
    <RDialog.Root open={open} onOpenChange={(o) => !o && onClose()}>
      <RDialog.Portal>
        <RDialog.Overlay className="modal-fade fixed inset-0 z-50 bg-black/25 backdrop-blur-[2px] dark:bg-black/50" />
        <RDialog.Content
          aria-describedby={undefined}
          data-ks-modal=""
          className="cmd-pop fixed top-[14vh] left-1/2 z-50 flex max-h-[min(560px,76vh)] w-[min(640px,calc(100vw-2rem))] -translate-x-1/2 flex-col overflow-hidden rounded-2xl border border-line bg-surface shadow-pop outline-none"
        >
          <RDialog.Title className="sr-only">{t.cmd.open}</RDialog.Title>
          {body}
        </RDialog.Content>
      </RDialog.Portal>
    </RDialog.Root>
  )
}

function PaletteBody({ q, setQ, onClose, onAction, cur, desktop }: { q: string; setQ: (v: string) => void; onClose: () => void; onAction: (a: KeyAction | 'new-plan') => void; cur?: { code: string; status: string }; desktop: boolean }) {
  const items = useItems(cur, onAction, onClose)
  const [active, setActive] = useState(0)
  const list = useRef<HTMLDivElement>(null)
  const sections: Section[] = useMemo(() => {
    if (!q.trim()) {
      // 空输入：先列操作（当前需求的批准 / 退回、新建、跳转），再列轮到你的需求与决策
      const cmds = items.filter((x) => x.kind === 'cmd' && (x.id === 'c:pass' || x.id === 'c:revise' || x.id.startsWith('c:new') || x.id.startsWith('c:go')))
      const turn = items
        .filter((x) => (x.kind === 'req' || x.kind === 'dec') && (x.boost ?? 0) >= 30)
        .sort((a, b) => (a.kind === b.kind ? 0 : a.kind === 'dec' ? -1 : 1))
        .slice(0, 8)
      return [
        { key: 'cmd', label: t.cmd.group.cmd, items: cmds },
        { key: 'turn', label: t.cmd.group.turn, items: turn },
      ].filter((s) => s.items.length)
    }
    return search(items, q, 6).map((g) => ({ key: g.kind, label: t.cmd.group[g.kind as DocKind], items: g.items as Item[] }))
  }, [items, q])
  const flat = useMemo(() => flatten(sections.map((s) => ({ kind: 'cmd' as const, items: s.items, best: 0 }))) as Item[], [sections])
  useEffect(() => setActive(0), [q])
  useEffect(() => {
    list.current?.querySelector(`[data-idx="${active}"]`)?.scrollIntoView({ block: 'nearest' })
  }, [active])

  const onKeyDown = (e: React.KeyboardEvent) => {
    if (e.nativeEvent.isComposing) return
    if (e.key === 'ArrowDown' || (e.ctrlKey && e.key === 'n')) {
      e.preventDefault()
      setActive((i) => (flat.length ? (i + 1) % flat.length : 0))
    } else if (e.key === 'ArrowUp' || (e.ctrlKey && e.key === 'p')) {
      e.preventDefault()
      setActive((i) => (flat.length ? (i - 1 + flat.length) % flat.length : 0))
    } else if (e.key === 'Enter') {
      e.preventDefault()
      flat[active]?.run()
    }
  }

  let idx = -1
  return (
    <div className="flex min-h-0 flex-1 flex-col" onKeyDown={onKeyDown}>
      {desktop && (
        <div className="flex shrink-0 items-center gap-3 border-b border-line px-4">
          <Search className="size-[18px] shrink-0 text-fg-3" />
          <input
            autoFocus
            value={q}
            onChange={(e) => setQ(e.target.value)}
            placeholder={t.cmd.placeholder}
            role="combobox"
            aria-expanded
            aria-controls="ks-cmd-list"
            aria-activedescendant={flat[active] ? `ks-cmd-${active}` : undefined}
            className="h-13 min-w-0 flex-1 bg-transparent text-[15px] text-fg outline-none placeholder:text-fg-3"
          />
          {q && (
            <button aria-label={t.common.close} onClick={() => setQ('')} className="rounded p-1 text-fg-3 hover:text-fg">
              <X className="size-4" />
            </button>
          )}
        </div>
      )}
      <div ref={list} id="ks-cmd-list" role="listbox" className="min-h-0 flex-1 overflow-y-auto overscroll-contain p-2">
        {!flat.length ? (
          <div className="px-4 py-10 text-center">
            <div className="text-sm text-fg-2">{t.cmd.empty}</div>
            <div className="mt-1 text-xs text-fg-3">{t.cmd.emptyHint}</div>
          </div>
        ) : (
          sections.map((s) => (
            <div key={s.key} role="group" aria-label={s.label} className="mb-1 last:mb-0">
              <div className="px-2.5 pt-2 pb-1 text-xs font-medium text-fg-3">{s.label}</div>
              {s.items.map((it) => {
                idx++
                const i = idx
                const on = i === active
                return (
                  <div
                    key={it.id}
                    id={`ks-cmd-${i}`}
                    data-idx={i}
                    role="option"
                    aria-selected={on}
                    onMouseMove={() => !on && setActive(i)}
                    onClick={() => it.run()}
                    className={cx('flex min-h-10 cursor-pointer items-center gap-2.5 rounded-lg px-2.5 py-1.5 text-sm select-none', on ? 'bg-hover text-fg' : 'text-fg-2', !desktop && 'min-h-12')}
                  >
                    <span className={cx('flex shrink-0 items-center text-fg-3 [&>svg]:size-4', it.kind === 'cmd' && 'w-5 justify-center')}>{it.icon}</span>
                    <span className={cx('min-w-0 flex-1 truncate', on && 'text-fg')}>{it.title}</span>
                    {it.meta && <span className="flex shrink-0 items-center gap-1.5">{it.meta}</span>}
                    {desktop && it.keys && <KeyCaps keys={it.keys.split(' ')} />}
                    {on && desktop && !it.keys && <ArrowRight className="size-3.5 shrink-0 text-fg-3" />}
                  </div>
                )
              })}
            </div>
          ))
        )}
      </div>
      {desktop && (
        <div className="flex shrink-0 items-center gap-4 border-t border-line bg-sunken/50 px-4 py-2 text-xs text-fg-3">
          <span className="flex items-center gap-1.5">
            <KeyCaps keys={['↑', '↓']} />
            {t.cmd.foot.move}
          </span>
          <span className="flex items-center gap-1.5">
            <KeyCaps keys={['↵']} />
            {t.cmd.foot.open}
          </span>
          <span className="flex items-center gap-1.5">
            <KeyCaps keys={['Esc']} />
            {t.cmd.foot.close}
          </span>
          <button className="ml-auto flex items-center gap-1.5 hover:text-fg" onClick={() => onAction('help')}>
            <KeyCaps keys={['?']} />
            {t.cmd.foot.help}
          </button>
        </div>
      )}
    </div>
  )
}

// KeyCaps：一组键帽。
export function KeyCaps({ keys, then }: { keys: string[]; then?: boolean }) {
  return (
    <span className="inline-flex shrink-0 items-center gap-1">
      {keys.map((k, i) => (
        <span key={i} className="inline-flex items-center gap-1">
          {then && i > 0 && <span className="text-[11px] text-fg-3">{t.keys.then}</span>}
          <kbd className="inline-flex h-5 min-w-5 items-center justify-center rounded-md border border-line border-b-line-strong bg-surface-2 px-1 font-mono text-[11px] leading-none text-fg-2">{keyCap(k)}</kbd>
        </span>
      ))}
    </span>
  )
}

// ---------- 快捷键表 ----------

function HelpSheet({ open, onClose }: { open: boolean; onClose: () => void }) {
  return (
    <RDialog.Root open={open} onOpenChange={(o) => !o && onClose()}>
      <RDialog.Portal>
        <RDialog.Overlay className="modal-fade fixed inset-0 z-50 bg-black/30 backdrop-blur-[2px] dark:bg-black/50" />
        <RDialog.Content
          aria-describedby={undefined}
          data-ks-modal=""
          onOpenAutoFocus={(e) => e.preventDefault()}
          className="modal-pop fixed top-1/2 left-1/2 z-50 flex max-h-[88dvh] w-[calc(100vw-2rem)] max-w-2xl -translate-x-1/2 -translate-y-1/2 flex-col overflow-hidden rounded-2xl border border-line bg-surface shadow-pop outline-none"
        >
          <div className="flex items-start gap-3 px-6 pt-5">
            <div className="min-w-0 flex-1">
              <RDialog.Title className="font-display text-[17px] font-semibold text-fg">{t.keys.title}</RDialog.Title>
              <p className="mt-1 text-[13px] text-fg-3">{t.keys.hint}</p>
            </div>
            <RDialog.Close asChild>
              <Button variant="ghost" size="icon-sm" aria-label={t.common.close}>
                <X className="size-4" />
              </Button>
            </RDialog.Close>
          </div>
          <div className="grid gap-x-8 gap-y-5 overflow-y-auto px-6 pt-4 pb-6 sm:grid-cols-2">
            {helpGroups.map((g) => (
              <section key={g.id}>
                <h3 className="mb-1.5 text-xs font-medium text-fg-3">{t.keys.group[g.id]}</h3>
                <div className="divide-y divide-line/70">
                  {g.rows.map((r) => (
                    <div key={r.id} className="flex items-center gap-3 py-1.5">
                      <span className="min-w-0 flex-1 text-[13px] text-fg-2">{t.keys.row[r.id]}</span>
                      <KeyCaps keys={r.keys} then={r.then} />
                    </div>
                  ))}
                </div>
              </section>
            ))}
          </div>
        </RDialog.Content>
      </RDialog.Portal>
    </RDialog.Root>
  )
}

// 视图轮换顺序（需求页 V 键）
export const nextView = (v: string) => viewOrder[(viewOrder.indexOf(v as (typeof viewOrder)[number]) + 1) % viewOrder.length]
