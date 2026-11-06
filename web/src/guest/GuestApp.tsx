import { useEffect, useMemo, useRef, useState, type FormEvent } from 'react'
import { Link, NavLink, Route, Routes, useLocation, useNavigate, useParams } from 'react-router'
import { useQueryClient } from '@tanstack/react-query'
import { Ban, List, X } from 'lucide-react'
import { ApiError } from '../api/client'
import { t } from '../i18n'
import { actKey, freshOf, guestPath, guestTree, navRows, navSessions, refIndex } from '../lib/guest'
import { reasonOf, classify } from '../lib/fail'
import { cx } from '../lib/util'
import { setFileBase } from '../ui/attach'
import { Button, Input } from '../ui/base'
import { displayState, StatusDot } from '../ui/marks'
import { DeadRefContext, PlanGlyph, RefIndexContext, ReqGlyph, type RefInfo } from '../ui/refmark'
import { PeekContext } from '../app/nav'
import { Logo } from '../app/Shell'
import { gk, unlock, useGuestLive, useGuestMeta, useOverview, type GuestMeta } from './data'
import { Failed, Loading, OverviewView, PlanView, ReqView, SessionView, type Overview } from './views'
import { ComposeScope } from './slots'

// 访客页（R-281 第 3 段）：/s/<token>，免登录、只读。
// 和所有者的网页完全分开：不挂所有者的导航、设置和任何写操作入口，不调所有者的接口（不会借访客页拿到所有者的登录态），
// 只调 /api/share/<token>/…。设了访问密码先输密码；链接撤回、分享撤回或到期后显示被拒绝的页面。

export default function GuestApp() {
  return (
    <Routes>
      <Route path="/s/:token/*" element={<Guest />} />
    </Routes>
  )
}

function Splash() {
  return (
    <div className="flex min-h-dvh items-center justify-center bg-bg">
      <div className="size-8 animate-pulse rounded-xl bg-accent/70" aria-label={t.app.name} />
    </div>
  )
}

function Guest() {
  const token = useParams().token ?? ''
  const meta = useGuestMeta(token)
  useEffect(() => {
    setFileBase(`/api/share/${encodeURIComponent(token)}/files/`)
    // 访客页不给搜索引擎收录
    const m = document.createElement('meta')
    m.name = 'robots'
    m.content = 'noindex, nofollow'
    document.head.appendChild(m)
    return () => m.remove()
  }, [token])
  useEffect(() => {
    document.title = meta.data?.name ? `${meta.data.name}${t.common.sep}${t.app.name}` : t.app.name
  }, [meta.data?.name])
  if (meta.isPending) return <Splash />
  if (meta.isError) return <Denied error={meta.error} />
  if (meta.data.needs_password && !meta.data.unlocked) return <Gate token={token} meta={meta.data} />
  return <Space token={token} meta={meta.data} />
}

// Denied：链接无效、撤回、分享撤回或到期。说明句来自纸鸢（「这条链接已撤回」「分享已到期」……）。
function Denied({ error }: { error: unknown }) {
  const gone = error instanceof ApiError && (error.status === 404 || error.status === 410)
  return (
    <div className="flex min-h-dvh flex-col bg-bg">
      <Bar />
      <div className="grid flex-1 place-items-center px-4 py-10">
        <div className="grid w-full max-w-[360px] justify-items-center gap-3 rounded-[14px] border border-line bg-surface px-6 py-8 text-center shadow-[0_1px_2px_rgb(70_50_18/0.05)]">
          <Ban className="size-8 text-fg-3" aria-hidden />
          <h1 className="font-display text-[20px] text-fg">{gone ? error.message : t.guest.deniedTitle}</h1>
          <p className="text-[13px] text-fg-3">{gone ? t.guest.deniedHint : reasonOf(classify(error))}</p>
        </div>
      </div>
    </div>
  )
}

function Gate({ token, meta }: { token: string; meta: GuestMeta }) {
  const qc = useQueryClient()
  const [pwd, setPwd] = useState('')
  const [err, setErr] = useState('')
  const [busy, setBusy] = useState(false)
  const submit = async (e: FormEvent) => {
    e.preventDefault()
    if (!pwd || busy) return
    setBusy(true)
    setErr('')
    try {
      await unlock(token, pwd)
      await qc.invalidateQueries({ queryKey: gk.meta(token) })
    } catch (x) {
      setErr(x instanceof ApiError && x.status < 500 ? x.message : reasonOf(classify(x)))
      setBusy(false)
    }
  }
  return (
    <div className="flex min-h-dvh flex-col bg-bg">
      <Bar meta={meta} />
      <div className="grid flex-1 place-items-center px-4 py-10">
        <form onSubmit={submit} className="grid w-full max-w-[360px] gap-4 rounded-[14px] border border-line bg-surface px-6 py-7 shadow-[0_1px_2px_rgb(70_50_18/0.05)]">
          <Logo className="size-10 justify-self-center" />
          <h1 className="text-center font-display text-[20px] text-fg">{meta.name}</h1>
          <label className="grid gap-1.5">
            <span className="text-[13px] font-medium text-fg-2">{t.guest.password}</span>
            <Input type="password" autoComplete="off" autoFocus value={pwd} onChange={(e) => setPwd(e.target.value)} aria-invalid={!!err} />
            {err && <span className="text-[12.5px] text-danger">{err}</span>}
          </label>
          <Button type="submit" variant="primary" loading={busy} disabled={!pwd}>
            {t.guest.enter}
          </Button>
        </form>
      </div>
    </div>
  )
}

const fmtDay = (iso: string) => {
  const d = new Date(iso)
  return d.getFullYear() === new Date().getFullYear() ? t.share.day(d.getMonth() + 1, d.getDate()) : t.share.dayY(d.getFullYear(), d.getMonth() + 1, d.getDate())
}

// Bar：顶栏。只有纸鸢标志、分享名称、「实时」、新动态、有效期和访客名字，没有所有者的导航和设置。
function Bar({ meta, token, live, news, onMenu, menu }: { meta?: GuestMeta; token?: string; live?: boolean; news?: number; onMenu?: () => void; menu?: boolean }) {
  return (
    <header className="sticky top-0 z-30 flex h-12 shrink-0 items-center gap-2.5 border-b border-line bg-bg/90 px-3 backdrop-blur sm:px-5">
      {onMenu && (
        <button type="button" onClick={onMenu} aria-label={t.guest.contents} aria-expanded={menu} className="-ml-1 flex size-8 items-center justify-center rounded-lg text-fg-2 hover:bg-hover lg:hidden">
          {menu ? <X className="size-[18px]" /> : <List className="size-[18px]" />}
        </button>
      )}
      {token ? (
        <Link to={guestPath(token)} className="flex min-w-0 items-center gap-2">
          <Logo className="size-[22px] shrink-0" />
          <span className="truncate font-display text-[16px] tracking-[0.02em] text-fg">{meta?.name}</span>
        </Link>
      ) : (
        <span className="flex min-w-0 items-center gap-2">
          <Logo className="size-[22px] shrink-0" />
          <span className="truncate font-display text-[16px] tracking-[0.02em] text-fg">{meta?.name ?? t.app.name}</span>
        </span>
      )}
      {live !== undefined && (
        <span title={t.guest.liveTip} className={cx('inline-flex shrink-0 items-center gap-1.5 text-xs', live ? 'text-run' : 'text-fg-3')}>
          <span className={cx('size-1.5 rounded-full', live ? 'guest-breathe bg-run' : 'bg-fg-3/60')} />
          {live ? t.guest.live : t.guest.reconnecting}
        </span>
      )}
      {!!news && token && (
        <Link to={guestPath(token)} className="shrink-0 rounded-full bg-accent-soft px-2 py-0.5 text-xs font-medium text-accent hover:bg-accent/15">
          {t.guest.newActivity(news)}
        </Link>
      )}
      {meta && (
        <span className="ml-auto flex shrink-0 items-center gap-2 text-[12.5px] text-fg-3">
          {meta.expires_at && <span className="hidden sm:inline">{t.share.until(fmtDay(meta.expires_at)) + t.common.sep}</span>}
          <span className="max-w-[8em] truncate">{meta.guest}</span>
        </span>
      )}
    </header>
  )
}

function Space({ token, meta }: { token: string; meta: GuestMeta }) {
  const ov = useOverview(token)
  const live = useGuestLive(token, true)
  const nav = useNavigate()
  const loc = useLocation()
  const [menu, setMenu] = useState(false)
  const home = loc.pathname.replace(/\/+$/, '') === guestPath(token)

  // 新动态：上次在概览时看到的之外的，离开概览后在顶栏计数；概览里新出现的那几条闪一下
  const acts = ov.data?.activity
  const seen = useRef<Set<string> | null>(null)
  const prev = useRef<Set<string> | null>(null)
  const [fresh, setFresh] = useState<Set<string>>(new Set())
  useEffect(() => {
    if (!acts) return
    setFresh(freshOf(prev.current, acts))
    prev.current = new Set(acts.map(actKey))
  }, [acts])
  if (acts && (home || !seen.current)) seen.current = new Set(acts.map(actKey))
  const news = !home && acts && seen.current ? acts.filter((a) => !seen.current!.has(actKey(a))).length : 0

  useEffect(() => setMenu(false), [loc.pathname])

  const index = useMemo(() => refIndex(ov.data?.nodes ?? [], ov.data?.decs ?? []) as unknown as Map<string, RefInfo>, [ov.data?.nodes, ov.data?.decs])
  const peek = useMemo(
    () => (code: string) => {
      const info = index.get(code)
      if (!info) return
      if (info.kind === 'req') nav(guestPath(token, 'req', code))
      else if (info.kind === 'plan') nav(guestPath(token, 'plan', code))
      else if (info.reqCode) nav(`${guestPath(token, 'req', info.reqCode)}#${code}`)
    },
    [index, nav, token],
  )

  const tree = useMemo(() => guestTree(ov.data?.nodes ?? []), [ov.data?.nodes])
  const reqs = useMemo(() => navRows(tree).filter((r, i, all) => r.node.kind === 'req' && all.findIndex((x) => x.key === r.key) === i).map((r) => ({ code: r.node.code ?? '', title: r.node.title, status: r.node.status ?? '' })), [tree])

  return (
    <RefIndexContext.Provider value={index}>
      <DeadRefContext.Provider value={true}>
        <PeekContext.Provider value={peek}>
          <div className="flex h-dvh flex-col bg-bg">
            <Bar meta={meta} token={token} live={live} news={news} onMenu={() => setMenu((v) => !v)} menu={menu} />
            <div className="relative flex min-h-0 flex-1">
              <nav aria-label={t.guest.contentsAria} className={cx('shrink-0 overflow-y-auto px-2.5 py-3.5', menu ? 'absolute inset-0 z-20 block bg-bg lg:static lg:w-[280px] lg:border-r lg:border-line lg:bg-sunken/40' : 'hidden w-[280px] border-r border-line bg-sunken/40 lg:block')}>
                {ov.data && <Tree token={token} ov={ov.data} />}
              </nav>
              <main className="min-w-0 flex-1 overflow-y-auto bg-surface">
                {ov.isPending ? (
                  <Loading />
                ) : ov.isError ? (
                  <Failed error={ov.error} onRetry={() => ov.refetch()} />
                ) : (
                  <ComposeScope key={loc.pathname} token={token}>
                  <Routes>
                    <Route index element={<OverviewView ov={ov.data} token={token} reqs={reqs} fresh={fresh} />} />
                    <Route path="r/:code" element={<ReqRoute token={token} ov={ov.data} />} />
                    <Route path="p/:code" element={<PlanRoute token={token} />} />
                    <Route path="s/:key" element={<SessionRoute token={token} ov={ov.data} />} />
                    <Route path="*" element={<OverviewView ov={ov.data} token={token} reqs={reqs} fresh={fresh} />} />
                  </Routes>
                  </ComposeScope>
                )}
              </main>
            </div>
          </div>
        </PeekContext.Provider>
      </DeadRefContext.Provider>
    </RefIndexContext.Provider>
  )
}

function ReqRoute({ token, ov }: { token: string; ov: Overview }) {
  const code = useParams().code ?? ''
  return <ReqView key={code} token={token} code={code} ov={ov} />
}
function PlanRoute({ token }: { token: string }) {
  const code = useParams().code ?? ''
  return <PlanView key={code} token={token} code={code} />
}
function SessionRoute({ token, ov }: { token: string; ov: Overview }) {
  const key = useParams().key ?? ''
  return <SessionView key={key} token={token} sessionKey={key} ov={ov} />
}

// Tree：目录。概览，然后计划 → 阶段 → 需求（到需求为止），再单列「会话」一组、每个会话只出现一次；主调度不能点进对话，只是一行。
function Tree({ token, ov }: { token: string; ov: Overview }) {
  const rows = useMemo(() => navRows(guestTree(ov.nodes ?? [])), [ov.nodes])
  const sess = useMemo(() => navSessions(guestTree(ov.nodes ?? [])), [ov.nodes])
  const sessions = new Map(ov.sessions.map((s) => [s.key, s]))
  const item = 'flex min-h-8 w-full min-w-0 items-center gap-1.5 rounded-lg py-1 pr-2 text-left text-[13px] text-fg-2 transition-colors'
  const link = ({ isActive }: { isActive: boolean }) => cx(item, isActive ? 'bg-surface text-fg shadow-[0_1px_2px_rgb(70_50_18/0.06)]' : 'hover:bg-hover hover:text-fg')
  return (
    <div className="grid gap-px">
      <NavLink end to={guestPath(token)} className={link} style={{ paddingLeft: 8 }}>
        <span className="truncate">{t.guest.overview}</span>
      </NavLink>
      <div className="mx-2 mt-2.5 mb-1 text-xs text-fg-3">{t.guest.contentsAria}</div>
      {rows.map((r, i) => {
        const n = r.node
        const pad = { paddingLeft: 8 + r.depth * 14 }
        if (n.kind === 'plan')
          return (
            <NavLink key={i} to={guestPath(token, 'plan', n.code)} className={link} style={pad}>
              <PlanGlyph status={n.status} />
              <span className="shrink-0 font-mono text-[11px] text-fg-3">{n.code}</span>
              <span className="truncate">{n.title}</span>
            </NavLink>
          )
        if (n.kind === 'stage')
          return (
            <div key={i} className={cx(item, 'text-fg-3')} style={pad}>
              <span className="shrink-0 text-[11.5px]">{t.guest.stage(n.seq ?? 0)}</span>
              <span className="truncate">{n.title}</span>
            </div>
          )
        if (n.kind === 'req')
          return (
            <NavLink key={i} to={guestPath(token, 'req', n.code)} className={link} style={pad}>
              <ReqGlyph status={n.status ?? ''} />
              <span className="shrink-0 font-mono text-[11px] text-fg-3">{n.code}</span>
              <span className="truncate">{n.title}</span>
            </NavLink>
          )
        return null
      })}
      {sess.length > 0 && <div className="mx-2 mt-3.5 mb-1 text-xs text-fg-3">{t.guest.sessions}</div>}
      {sess.map((n) => {
        const s = sessions.get(n.ref)
        const pad = { paddingLeft: 8 }
        const body = (
          <>
            <StatusDot state={s ? displayState(s) : (n.status ?? 'idle')} />
            {n.role && t.role[n.role] && <span className="shrink-0 text-[11.5px] text-fg-3">{t.role[n.role]}</span>}
            <span className="truncate">{n.title || t.guest.agent}</span>
          </>
        )
        return s && !s.conversation ? (
          <div key={n.ref} className={cx(item, 'text-fg-3')} style={pad}>
            {body}
          </div>
        ) : (
          <NavLink key={n.ref} to={guestPath(token, 'session', n.ref)} className={link} style={pad}>
            {body}
          </NavLink>
        )
      })}
    </div>
  )
}
