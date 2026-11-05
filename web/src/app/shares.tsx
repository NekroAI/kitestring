import { useMemo, useState, type ReactNode } from 'react'
import { Link, useNavigate } from 'react-router'
import { toast } from 'sonner'
import { ChevronDown, ChevronLeft, ChevronRight, Copy, Eye, Lock, Milestone, Plus, Share2, SquareTerminal, X } from 'lucide-react'
import { api, errMsg, unwrap } from '../api/client'
import { useProjects, useShareCandidates, type Share, type ShareLinkView } from '../api/queries'
import { t } from '../i18n'
import { copyText, cx, relTime, useNow, usePref } from '../lib/util'
import { buildTree, counts, expiryAt, graph, keyOf, linkURL, missing, resolve, setAuto, tidy, toggle, type ExpiryChoice, type ShareItem, type ShareKind, type ShareNode, type ShareTree } from '../lib/share'
import { Button, Card, Empty, Input, Segmented, Skeleton, Switch, Tip } from '../ui/base'
import { Menu, Modal, useConfirm } from '../ui/overlay'
import { PlanGlyph, ReqGlyph, RefChip } from '../ui/refmark'
import { RoleTag } from '../ui/marks'
import { DraftLine, useDraft } from './draft'
import { useBusy } from './actions'

// 分享（R-281）：所有者这一侧的列表、详情、新建 / 编辑。访客页（/s/<token>）见第 3 段，访客留言见第 4 段。

// ---------- 小零件 ----------

// dayText：到期、撤回、创建这类只看日期的时间：今年「12 月 5 日」，跨年带年份。
export function dayText(iso?: string | null) {
  if (!iso) return ''
  const d = new Date(iso)
  return d.getFullYear() === new Date().getFullYear() ? t.share.day(d.getMonth() + 1, d.getDate()) : t.share.dayY(d.getFullYear(), d.getMonth() + 1, d.getDate())
}

const pad = (n: number) => String(n).padStart(2, '0')
const isoDay = (d: Date) => `${d.getFullYear()}-${pad(d.getMonth() + 1)}-${pad(d.getDate())}`

function countParts(c: { plans: number; stages?: number; reqs: number; sessions: number }) {
  return [c.plans && t.share.plans(c.plans), c.stages && t.share.stages(c.stages), c.reqs && t.share.reqs(c.reqs), c.sessions && t.share.sessions(c.sessions)].filter(Boolean) as string[]
}

const statusPill: Record<string, string> = { active: 'bg-run-soft text-run', expired: 'bg-idle-soft text-idle', revoked: 'bg-idle-soft text-idle' }
function StatusPill({ status }: { status: string }) {
  return <span className={cx('inline-flex shrink-0 items-center rounded-full px-2 text-xs leading-5 font-medium whitespace-nowrap', statusPill[status])}>{t.share.status[status]}</span>
}

export function BackLink({ to, label, className }: { to: string; label: string; className?: string }) {
  return (
    <Link to={to} className={cx('-ml-2 mb-2 inline-flex h-8 items-center gap-1 rounded-lg px-2 text-sm text-fg-3 transition hover:bg-hover hover:text-fg', className)}>
      <ChevronLeft className="size-4" />
      {label}
    </Link>
  )
}

// NodeLabel：勾选列表、内容清单里的一项：印记 + 编号 + 标题；阶段写「阶段 n」，会话写身份 + 名称。
function NodeLabel({ n }: { n: ShareNode }) {
  if (n.kind === 'session')
    return (
      <>
        <SquareTerminal className="size-3.5 shrink-0 text-accent" />
        {n.role && <RoleTag role={n.role} />}
        <span className={cx('min-w-0 truncate', n.status === 'ended' && 'text-fg-3')}>「{n.title || n.ref.slice(-8)}」</span>
      </>
    )
  return (
    <>
      {n.kind === 'plan' ? <PlanGlyph status={n.status} className="size-3.5 shrink-0" /> : n.kind === 'stage' ? <Milestone className="size-3.5 shrink-0 text-fg-3" /> : <ReqGlyph status={n.status ?? 'ready'} className="size-3.5 shrink-0" />}
      <span className="shrink-0 font-mono text-[11.5px] text-fg-3">{n.kind === 'stage' ? t.share.stage(n.seq ?? 0) : n.code}</span>
      <span className="min-w-0 truncate">{n.title}</span>
    </>
  )
}

// ---------- 关系图（只读） ----------

const graphX: Record<ShareKind, number> = { plan: 4, stage: 84, req: 168, session: 252 }
const graphW: Record<ShareKind, number> = { plan: 64, stage: 68, req: 68, session: 80 }
const reqStroke: Record<string, string> = { done: 'var(--run)', draft: 'var(--fg-3)', review: 'var(--reply)', in_progress: 'var(--accent)', blocked: 'var(--wait)', shelved: 'var(--idle)' }

export function ShareGraph({ tree, inc }: { tree: ShareTree; inc: Set<string> }) {
  const { cols, edges } = useMemo(() => graph(tree, inc), [tree, inc])
  const rows = Math.max(0, ...cols.map((c) => c.keys.length))
  if (!rows) return <div className="py-8 text-center text-sm text-fg-3">{t.share.picked}</div>
  const H = 26 + rows * 30
  const pos = new Map<string, { x: number; y: number; w: number }>()
  for (const c of cols) {
    const off = 26 + ((rows - c.keys.length) * 30) / 2
    c.keys.forEach((k, i) => pos.set(k, { x: graphX[c.kind], y: off + i * 30, w: graphW[c.kind] }))
  }
  const label = (n: ShareNode) => {
    if (n.kind === 'stage') return t.share.stage(n.seq ?? 0)
    if (n.kind === 'session') {
      const s = (n.role && t.role[n.role]) || n.title || n.ref
      return s.length > 6 ? s.slice(0, 5) + '…' : s
    }
    return n.code ?? ''
  }
  const full = (n: ShareNode) => (n.kind === 'session' ? [n.role && t.role[n.role], `「${n.title || n.ref}」`].filter(Boolean).join(' ') : n.kind === 'stage' ? `${n.code} ${t.share.stage(n.seq ?? 0)}：${n.title}` : `${n.code} ${n.title}`)
  return (
    <svg viewBox={`0 0 336 ${H}`} className="mx-auto block h-auto w-full max-w-[460px] min-w-[300px]" role="img" aria-label={t.share.graph}>
      {cols.map((c) => (
        <text key={c.kind} x={graphX[c.kind] + graphW[c.kind] / 2} y={12} textAnchor="middle" className="fill-fg-3 text-[10px]">
          {t.share.cols[c.kind]}
        </text>
      ))}
      {edges.map((e) => {
        const a = pos.get(e.from)
        const b = pos.get(e.to)
        if (!a || !b) return null
        const x1 = a.x + a.w
        const y1 = a.y + 11
        const x2 = b.x
        const y2 = b.y + 11
        const mx = (x1 + x2) / 2
        return <path key={`${e.from}>${e.to}`} d={`M${x1} ${y1}C${mx} ${y1} ${mx} ${y2} ${x2} ${y2}`} fill="none" stroke="var(--line-strong)" strokeWidth={1.3} strokeDasharray={e.direct ? undefined : '3 3'} />
      })}
      {cols.flatMap((c) =>
        c.keys.map((k) => {
          const n = tree.nodes.get(k)!
          const p = pos.get(k)!
          const stroke = n.kind === 'req' ? (reqStroke[n.status ?? ''] ?? 'var(--fg-2)') : n.kind === 'session' ? 'var(--accent)' : 'var(--fg-2)'
          return (
            <g key={k}>
              <title>{full(n)}</title>
              <rect x={p.x} y={p.y} width={p.w} height={22} rx={6} fill="var(--surface-2)" stroke={stroke} strokeWidth={1.2} opacity={n.kind === 'session' && n.status === 'ended' ? 0.6 : 1} />
              <text x={p.x + p.w / 2} y={p.y + 15} textAnchor="middle" className={cx('fill-fg-2 text-[10.5px]', n.kind === 'plan' || n.kind === 'req' ? 'font-mono' : '')}>
                {label(n)}
              </text>
            </g>
          )
        }),
      )}
    </svg>
  )
}

// ---------- 分享列表 ----------

function ShareRow({ x }: { x: Share }) {
  const on = x.status === 'active'
  const parts = [...countParts(x.scope), t.share.guests(x.links.filter((l) => !l.revoked_at).length)]
  const when = on ? (x.expires_at ? t.share.until(dayText(x.expires_at)) : t.share.forever) : x.status === 'revoked' ? t.share.revokedAt(dayText(x.revoked_at)) : t.share.expiredAt(dayText(x.expires_at))
  return (
    <Link
      to={`/shares/${x.id}`}
      className={cx(
        'grid w-full grid-cols-[minmax(0,1fr)_auto] items-center gap-x-4 gap-y-0.5 rounded-2xl border border-line px-4 transition-colors hover:border-line-strong',
        on ? 'h-[76px] bg-surface shadow-card' : 'h-14 bg-transparent',
      )}
    >
      <div className={cx('col-start-1 flex min-w-0 items-center gap-2', on ? 'text-[15px] font-semibold text-fg' : 'text-sm font-medium text-fg-2')}>
        <span className="truncate">{x.name}</span>
        {x.has_password && (
          <Tip content={t.share.locked}>
            <Lock className="size-3.5 shrink-0 text-fg-3" aria-label={t.share.locked} />
          </Tip>
        )}
        {!on && <StatusPill status={x.status} />}
      </div>
      <div className="col-start-2 row-span-2 row-start-1 flex items-center gap-2 text-[12.5px] text-fg-3 max-sm:hidden">
        <span className="tabular">{when}</span>
        <ChevronRight className="size-4" />
      </div>
      <div className="col-start-1 min-w-0 truncate text-[12.5px] text-fg-3">
        {parts.join(t.common.sep)}
        <span className="sm:hidden">
          {t.common.sep}
          {when}
        </span>
      </div>
    </Link>
  )
}

export function SharesList({ shares, loading }: { shares: Share[]; loading: boolean }) {
  const [showOff, setShowOff] = usePref('ks.shares.showOff', false)
  const on = shares.filter((x) => x.status === 'active')
  const off = shares.filter((x) => x.status !== 'active')
  return (
    <>
      <BackLink to="/settings" label={t.nav.settings} className="lg:hidden" />
      <div className="mb-5 flex flex-wrap items-end justify-between gap-3 lg:mb-6">
        <div className="min-w-0">
          <h1 className="font-display text-[24px] font-semibold tracking-wide text-fg lg:text-[28px]">{t.share.title}</h1>
          <p className="mt-1 text-sm text-fg-3">{t.share.subtitle}</p>
        </div>
        <Link to="/shares/new">
          <Button variant="primary" icon={<Plus className="size-4" />}>
            {t.share.create}
          </Button>
        </Link>
      </div>
      {loading ? (
        <div className="grid gap-2">
          <Skeleton className="h-[76px] rounded-2xl" />
          <Skeleton className="h-[76px] rounded-2xl" />
        </div>
      ) : !shares.length ? (
        <Card>
          <Empty icon={<Share2 />} title={t.share.empty}>
            {t.share.emptyHint}
          </Empty>
        </Card>
      ) : (
        <>
          {on.length > 0 && (
            <>
              <h2 className="mb-2 flex items-center gap-2 text-xs font-semibold tracking-wide text-fg-3">
                {t.share.status.active}
                <span className="tabular">{on.length}</span>
              </h2>
              <div className="grid gap-2">
                {on.map((x) => (
                  <ShareRow key={x.id} x={x} />
                ))}
              </div>
            </>
          )}
          {off.length > 0 && (
            <>
              <button onClick={() => setShowOff(!showOff)} aria-expanded={showOff} className={cx('mb-2 flex items-center gap-1.5 text-[13px] text-fg-3 transition hover:text-fg-2', on.length ? 'mt-6' : '')}>
                <ChevronRight className={cx('size-3.5 transition-transform', showOff && 'rotate-90')} />
                {t.share.offHead}
                <span className="tabular">{off.length}</span>
              </button>
              {showOff && (
                <div className="grid gap-2">
                  {off.map((x) => (
                    <ShareRow key={x.id} x={x} />
                  ))}
                </div>
              )}
            </>
          )}
        </>
      )}
    </>
  )
}

// ---------- 动作 ----------

const shareFails = t.share.fail

function useShareOps() {
  const confirm = useConfirm()
  const [enableFor, setEnableFor] = useState<Share | null>(null)
  const revoke = async (x: Share) => {
    if (!(await confirm({ title: t.share.revokeTitle, description: t.share.revokeDesc, confirmText: t.share.revokeOk, danger: true }))) return
    try {
      await unwrap(api.POST('/api/shares/{id}/revoke', { params: { path: { id: x.id } } }))
      toast(t.share.revoked)
    } catch (e) {
      toast.error(errMsg(e, shareFails.revoke))
    }
  }
  const expired = (x: Share) => !!x.expires_at && new Date(x.expires_at).getTime() <= Date.now()
  const enable = async (x: Share, body?: { expires_at?: string; forever?: boolean }) => {
    if (!body && expired(x)) return setEnableFor(x)
    try {
      await unwrap(api.POST('/api/shares/{id}/enable', { params: { path: { id: x.id } }, body: body ?? {} }))
      toast.success(t.share.enabled)
      setEnableFor(null)
    } catch (e) {
      toast.error(errMsg(e, shareFails.enable))
    }
  }
  const dialog = <EnableDialog x={enableFor} onClose={() => setEnableFor(null)} onEnable={(b) => enableFor && enable(enableFor, b)} />
  return { revoke, enable, dialog }
}

// ExpiryPicker：有效期（7 天 / 30 天 / 永久 / 自定义），下面一行写到期日或「撤回前一直有效」。
function ExpiryPicker({ exp, date, onChange }: { exp: ExpiryChoice; date: string; onChange: (exp: ExpiryChoice, date: string) => void }) {
  const at = expiryAt(exp, date)
  return (
    <div>
      <span className="mb-1.5 block text-[13px] font-medium text-fg-2">{t.share.expiry}</span>
      <Segmented size="sm" value={exp} onChange={(v) => onChange(v, date)} options={(['7', '30', 'forever', 'custom'] as const).map((v) => ({ value: v, label: t.share.exp[v] }))} />
      {exp === 'custom' && <Input type="date" value={date} min={isoDay(new Date())} aria-label={t.share.expDate} onChange={(e) => onChange(exp, e.target.value)} className="mt-2 max-w-[200px]" />}
      <span className="mt-1.5 block text-xs leading-relaxed text-fg-3">{at ? t.share.expHint(dayText(at.toISOString())) : t.share.expForever}</span>
    </div>
  )
}

function EnableDialog({ x, onClose, onEnable }: { x: Share | null; onClose: () => void; onEnable: (b: { expires_at?: string; forever?: boolean }) => void }) {
  const [exp, setExp] = useState<ExpiryChoice>('30')
  const [date, setDate] = useState(() => isoDay(new Date(Date.now() + 30 * 86_400_000)))
  const [busy, wrap] = useBusy()
  const at = expiryAt(exp, date)
  return (
    <Modal
      open={!!x}
      onClose={onClose}
      title={t.share.enableTitle}
      description={t.share.enableDesc}
      footer={
        <>
          <Button variant="ghost" onClick={onClose}>
            {t.common.cancel}
          </Button>
          <Button variant="primary" loading={busy} onClick={() => void wrap(async () => onEnable(at ? { expires_at: at.toISOString() } : { forever: true }))}>
            {t.share.enable}
          </Button>
        </>
      }
    >
      <ExpiryPicker
        exp={exp}
        date={date}
        onChange={(e, d) => {
          setExp(e)
          setDate(d)
        }}
      />
    </Modal>
  )
}

// ---------- 访客链接 ----------

function LinkRows({ x }: { x: Share }) {
  const now = useNow()
  const [name, setName] = useState('')
  const [busy, wrap] = useBusy()
  const copy = async (l: ShareLinkView) => (await copyText(linkURL(l, location.origin))) && toast.success(t.common.copied)
  const setRevoked = async (l: ShareLinkView, revoked: boolean) => {
    try {
      await unwrap(api.PUT('/api/shares/{id}/links/{link}', { params: { path: { id: x.id, link: l.id } }, body: { revoked } }))
      toast(revoked ? t.share.linkRevoked : t.share.linkRestored)
    } catch (e) {
      toast.error(errMsg(e, revoked ? shareFails.revokeLink : shareFails.restoreLink))
    }
  }
  const add = () =>
    wrap(async () => {
      const n = name.trim()
      if (!n) return
      try {
        await unwrap(api.POST('/api/shares/{id}/links', { params: { path: { id: x.id } }, body: { name: n } }))
        setName('')
      } catch (e) {
        toast.error(errMsg(e, shareFails.addLink))
      }
    })
  return (
    <div>
      {x.links.length ? (
        <div className="-mx-1 overflow-x-auto">
          <table className="w-full border-collapse text-[13px]">
            <thead>
              <tr className="text-left text-xs text-fg-3">
                <th className="px-2 py-1.5 font-medium">{t.share.colGuest}</th>
                <th className="px-2 py-1.5 font-medium">{t.share.colLast}</th>
                <th className="px-2 py-1.5 font-medium max-sm:hidden">{t.share.colVisits}</th>
                <th />
              </tr>
            </thead>
            <tbody>
              {x.links.map((l) => (
                <tr key={l.id} className={cx('border-t border-line', l.revoked_at && 'text-fg-3')}>
                  <td className="px-2 py-2">
                    <span className="flex min-w-0 items-center gap-1.5">
                      <span className={cx('truncate font-semibold', l.revoked_at ? 'text-fg-3' : 'text-fg')}>{l.name}</span>
                      {l.revoked_at && <StatusPill status="revoked" />}
                    </span>
                  </td>
                  <td className="px-2 py-2 whitespace-nowrap tabular text-fg-3">{l.last_visit_at ? relTime(l.last_visit_at, now) : t.share.never}</td>
                  <td className="px-2 py-2 whitespace-nowrap tabular text-fg-3 max-sm:hidden">{t.share.visits(l.visits)}</td>
                  <td className="px-1 py-1.5 text-right whitespace-nowrap">
                    {l.revoked_at ? (
                      <Button size="sm" variant="ghost" onClick={() => void setRevoked(l, false)}>
                        {t.share.restoreLink}
                      </Button>
                    ) : (
                      <>
                        <Tip content={t.share.copyLink}>
                          <Button size="icon-sm" variant="ghost" aria-label={t.share.copyLink} onClick={() => void copy(l)} disabled={x.status !== 'active'}>
                            <Copy className="size-4" />
                          </Button>
                        </Tip>
                        <Button size="sm" variant="ghost" className="text-danger hover:text-danger" onClick={() => void setRevoked(l, true)}>
                          {t.share.revokeLink}
                        </Button>
                      </>
                    )}
                  </td>
                </tr>
              ))}
            </tbody>
          </table>
        </div>
      ) : (
        <div className="py-2 text-sm text-fg-3">{t.share.noLinks}</div>
      )}
      <form
        className="mt-3 flex gap-2"
        onSubmit={(e) => {
          e.preventDefault()
          void add()
        }}
      >
        <Input value={name} onChange={(e) => setName(e.target.value)} placeholder={t.share.linkPlaceholder} maxLength={40} aria-label={t.share.colGuest} />
        <Button type="submit" variant="soft" loading={busy} disabled={!name.trim()} icon={<Plus className="size-4" />}>
          {t.share.addLink}
        </Button>
      </form>
    </div>
  )
}

// ---------- 分享详情 ----------

// ItemList：分享上明确挂着的内容（含「包含以后新增的」标记），被单独取消的只写一个数。
function ItemList({ x, tree }: { x: Share; tree: ShareTree }) {
  const shown = x.items.filter((it) => !it.excluded && tree.nodes.has(keyOf(it.kind, it.ref)))
  // 只列最上面那一层：上级也在分享里的，由上级代表
  const top = shown.filter((it) => !(tree.ups.get(keyOf(it.kind, it.ref)) ?? []).some((p) => tree.nodes.has(p)))
  const cut = x.items.filter((it) => it.excluded).length
  return (
    <div className="grid gap-1.5">
      {top.map((it) => {
        const n = tree.nodes.get(keyOf(it.kind, it.ref))!
        return (
          <div key={keyOf(it.kind, it.ref)} className="flex min-w-0 items-center gap-2 text-[13.5px]">
            {n.kind === 'plan' || n.kind === 'req' ? (
              <span className="flex min-w-0 items-center gap-1.5">
                <RefChip code={n.code!} status={n.status} />
                <span className="min-w-0 truncate text-fg">{n.title}</span>
              </span>
            ) : (
              <span className="flex min-w-0 items-center gap-1.5">
                <NodeLabel n={n} />
              </span>
            )}
            {it.auto && it.kind !== 'session' && <span className="shrink-0 rounded-full bg-run-soft px-2 text-[11.5px] leading-5 font-medium text-run">{t.share.auto}</span>}
          </div>
        )
      })}
      {cut > 0 && <div className="text-xs text-fg-3">{t.share.excluded(cut)}</div>}
    </div>
  )
}

export function ShareDetail({ x }: { x: Share }) {
  const { revoke, enable, dialog } = useShareOps()
  const tree = useMemo(() => buildTree(x.scope.nodes), [x.scope.nodes])
  const inc = useMemo(() => new Set(tree.nodes.keys()), [tree])
  const first = x.links.find((l) => !l.revoked_at)
  const parts = countParts(x.scope)
  const on = x.status === 'active'
  return (
    <>
      <BackLink to="/shares" label={t.share.title} />
      <div className="mb-5 flex flex-wrap items-end justify-between gap-3 lg:mb-6">
        <div className="min-w-0">
          <h1 className="font-display text-[24px] font-semibold tracking-wide break-words text-fg lg:text-[28px]">{x.name}</h1>
          <div className="mt-1.5 flex flex-wrap items-center gap-x-2 gap-y-1 text-[13px] text-fg-3">
            <StatusPill status={x.status} />
            <span>{x.expires_at ? (on ? t.share.until(dayText(x.expires_at)) : t.share.expiredAt(dayText(x.expires_at))) : t.share.forever}</span>
            <span aria-hidden>·</span>
            <span>{x.has_password ? t.share.locked : t.share.noPassword}</span>
            <span aria-hidden>·</span>
            <span>{t.share.createdAt(dayText(x.created_at))}</span>
          </div>
        </div>
        <div className="flex flex-wrap items-center gap-2">
          {on && (
            <Button icon={<Eye className="size-4" />} disabled={!first} title={first ? undefined : t.share.previewNone} onClick={() => first && window.open(linkURL(first, location.origin), '_blank', 'noopener')}>
              {t.share.preview}
            </Button>
          )}
          <Link to={`/shares/${x.id}/edit`}>
            <Button>{t.common.edit}</Button>
          </Link>
          {on ? (
            <Button variant="ghost" className="text-danger hover:text-danger" onClick={() => void revoke(x)}>
              {t.share.revoke}
            </Button>
          ) : (
            <Button variant="soft" onClick={() => void enable(x)}>
              {t.share.enable}
            </Button>
          )}
        </div>
      </div>
      <div className="grid items-start gap-4 lg:grid-cols-2">
        <div className="flex min-w-0 flex-col gap-4">
          <Card className="p-4 sm:p-5">
            <h2 className="mb-1 text-xs font-semibold tracking-wide text-fg-3">{t.share.links}</h2>
            <p className="mb-3 text-xs text-fg-3">{t.share.linksHint}</p>
            <LinkRows x={x} />
          </Card>
        </div>
        <Card className="min-w-0 p-4 sm:p-5">
          <h2 className="mb-3 flex items-center gap-2 text-xs font-semibold tracking-wide text-fg-3">
            {t.share.content}
            <span className="font-normal">{parts.join(t.common.sep)}</span>
          </h2>
          <ItemList x={x} tree={tree} />
          <div className="mt-4 overflow-x-auto border-t border-line pt-4">
            <ShareGraph tree={tree} inc={inc} />
          </div>
        </Card>
      </div>
      {dialog}
    </>
  )
}

// ---------- 新建 / 编辑 ----------

export type ShareForm = {
  project: string
  name: string
  password: string
  clearPw: boolean
  exp: ExpiryChoice
  date: string
  items: ShareItem[]
  links: string[]
}

// normRefs：预选时写的是编号（P-x / R-x），勾选列表到了以后换成 ID，和后端返回的挂载项一致。
function normRefs(tree: ShareTree, items: ShareItem[]): ShareItem[] {
  return items.map((it) => {
    if (tree.nodes.has(keyOf(it.kind, it.ref))) return it
    const k = tree.byCode.get(it.ref)
    return k ? { ...it, ref: k.slice(k.indexOf('|') + 1) } : it
  })
}

const closedReq = (s?: string) => s === 'done' || s === 'shelved'

function TreePicker({ tree, items, onChange, preset }: { tree: ShareTree; items: ShareItem[]; onChange: (items: ShareItem[]) => void; preset?: string }) {
  const r = useMemo(() => resolve(tree, items), [tree, items])
  const [q, setQ] = useState('')
  const [showClosed, setShowClosed] = useState(false)
  // 展开：明确挂上的项的上级都展开（预选的需求能直接看到），勾着的计划也展开到阶段
  const [open, setOpen] = useState<Set<string>>(() => {
    const s = new Set<string>()
    const up = (k: string) => {
      for (const p of tree.ups.get(k) ?? []) {
        if (s.has(p)) continue
        s.add(p)
        up(p)
      }
    }
    for (const it of items) {
      if (it.excluded) continue
      const k = keyOf(it.kind, it.ref)
      up(k)
      if (it.kind === 'plan' || (preset && tree.nodes.get(k)?.code === preset)) s.add(k)
    }
    return s
  })
  const needle = q.trim().toLowerCase()
  const hit = useMemo(() => {
    if (!needle) return null
    const own = (n: ShareNode) => [n.code, n.title, n.role && t.role[n.role], n.kind === 'stage' ? t.share.stage(n.seq ?? 0) : ''].some((v) => v && v.toLowerCase().includes(needle))
    const out = new Set<string>()
    const walk = (k: string): boolean => {
      let any = own(tree.nodes.get(k)!)
      for (const c of tree.kids.get(k) ?? []) if (walk(c)) any = true
      if (any) out.add(k)
      return any
    }
    for (const k of [...tree.plans, ...tree.loose]) walk(k)
    return out
  }, [needle, tree])
  const loose = tree.loose.filter((k) => !closedReq(tree.nodes.get(k)?.status) || r.inc.has(k))
  const closedN = tree.loose.length - loose.length

  const row = (k: string, depth: number, via: string): ReactNode => {
    if (hit && !hit.has(k)) return null
    const n = tree.nodes.get(k)!
    const kids = tree.kids.get(k) ?? []
    const isOpen = !!hit || open.has(k)
    const on = r.inc.has(k)
    const cut = on && kids.length ? missing(tree, r, k) : 0
    const flip = () =>
      setOpen((s) => {
        const x = new Set(s)
        if (x.has(k)) x.delete(k)
        else x.add(k)
        return x
      })
    return (
      <div key={`${via}/${k}`} className="min-w-0">
        <div className="flex min-h-9 items-center gap-2 rounded-lg py-1 pr-2 hover:bg-hover/60" style={{ paddingLeft: 4 + depth * 20 }}>
          {kids.length ? (
            <button type="button" onClick={flip} aria-expanded={isOpen} aria-label={isOpen ? t.share.fold : t.share.expand} className="flex size-5 shrink-0 items-center justify-center rounded text-fg-3 hover:text-fg">
              <ChevronRight className={cx('size-3.5 transition-transform', isOpen && 'rotate-90')} />
            </button>
          ) : (
            <span className="size-5 shrink-0" />
          )}
          <label className={cx('flex min-w-0 flex-1 cursor-pointer items-center gap-2 text-[13.5px]', on ? 'text-fg' : 'text-fg-3')}>
            <input type="checkbox" checked={on} onChange={() => onChange(toggle(tree, items, k))} className="size-4 shrink-0 accent-[var(--accent)]" />
            <NodeLabel n={n} />
          </label>
          {cut > 0 && <span className="shrink-0 text-[11.5px] text-wait">{t.share.cut(cut)}</span>}
          {on && n.kind !== 'session' && (
            <span className="flex shrink-0 items-center gap-1.5 text-xs text-fg-3">
              <span className="max-sm:hidden">{t.share.auto}</span>
              <span aria-label={t.share.autoAria} className="contents">
                <Switch checked={!!r.auto.get(k)} onChange={(v) => onChange(setAuto(tree, items, k, v))} />
              </span>
            </span>
          )}
        </div>
        {isOpen && kids.map((c) => row(c, depth + 1, `${via}/${k}`))}
      </div>
    )
  }

  const plans = tree.plans.map((k) => row(k, 0, ''))
  const looseRows = (hit ? tree.loose : showClosed ? tree.loose : loose).map((k) => row(k, 0, ''))
  const none = hit && !hit.size
  return (
    <>
      <Input value={q} onChange={(e) => setQ(e.target.value)} placeholder={t.share.search} aria-label={t.share.search} className="mb-2" />
      <div className="flex min-w-0 flex-col">
        {none && <div className="px-2 py-3 text-sm text-fg-3">{t.share.noMatch}</div>}
        {!tree.nodes.size && <div className="px-2 py-3 text-sm text-fg-3">{t.share.noContent}</div>}
        {tree.plans.length > 0 && (!hit || tree.plans.some((k) => hit.has(k))) && <div className="mx-1 mt-2 mb-1 text-xs text-fg-3">{t.share.groupPlans}</div>}
        {plans}
        {tree.loose.length > 0 && (!hit || tree.loose.some((k) => hit.has(k))) && <div className="mx-1 mt-3 mb-1 text-xs text-fg-3">{t.share.groupLoose}</div>}
        {looseRows}
        {!hit && closedN > 0 && (
          <button type="button" onClick={() => setShowClosed(!showClosed)} aria-expanded={showClosed} className="mx-1 mt-1 flex items-center gap-1.5 self-start text-[13px] text-fg-3 hover:text-fg-2">
            <ChevronDown className={cx('size-3.5 transition-transform', !showClosed && '-rotate-90')} />
            {t.share.closedReqs(closedN)}
          </button>
        )}
      </div>
    </>
  )
}

export function ShareEditor({ share, init, draftKey, preset }: { share?: Share; init: ShareForm; draftKey: string; preset?: string }) {
  const navigate = useNavigate()
  const baseline = useMemo(() => JSON.stringify(init), [init])
  const h = useDraft<ShareForm>(draftKey, init, (v) => JSON.stringify(v) !== baseline)
  const f = h.value
  const projects = useProjects()
  const cand = useShareCandidates(f.project)
  const tree = useMemo(() => buildTree((cand.data ?? []) as ShareNode[]), [cand.data])
  const items = useMemo(() => normRefs(tree, f.items), [tree, f.items])
  const r = useMemo(() => resolve(tree, items), [tree, items])
  const c = counts(tree, r)
  const parts = countParts(c)
  const [link, setLink] = useState('')
  const [busy, wrap] = useBusy()
  const desktop = typeof window !== 'undefined' && window.matchMedia('(min-width: 1024px)').matches
  const projList = (projects.data?.projects ?? []).filter((p) => !p.hidden || p.id === f.project)
  const projName = projList.find((p) => p.id === f.project)?.name

  const addLink = () => {
    const n = link.trim()
    if (!n) return
    h.set({ links: [...f.links, n] })
    setLink('')
  }
  const missingWhy = !f.name.trim() ? t.share.needName : !r.inc.size ? t.share.needContent : ''
  const save = () =>
    wrap(async () => {
      const at = expiryAt(f.exp, f.date)
      const list = tidy(tree, items)
      try {
        if (!share) {
          const v = await unwrap(
            api.POST('/api/shares', {
              body: { name: f.name.trim(), project_id: f.project || undefined, password: f.password || undefined, expires_at: at?.toISOString(), items: list, links: f.links },
            }),
          )
          h.drop()
          toast.success(t.share.created)
          navigate(`/shares/${v.id}`, { replace: true })
        } else {
          const expChanged = f.exp !== init.exp || f.date !== init.date
          await unwrap(
            api.PUT('/api/shares/{id}', {
              params: { path: { id: share.id } },
              body: {
                name: f.name.trim(),
                password: f.clearPw ? '' : f.password || undefined,
                ...(expChanged ? (at ? { expires_at: at.toISOString() } : { forever: true }) : {}),
                items: list,
              },
            }),
          )
          h.drop()
          toast.success(t.share.saved)
          navigate(`/shares/${share.id}`, { replace: true })
        }
      } catch (e) {
        toast.error(errMsg(e, share ? t.share.fail.save : t.share.fail.create))
      }
    })

  return (
    <>
      <BackLink to={share ? `/shares/${share.id}` : '/shares'} label={share ? share.name : t.share.title} />
      <h1 className="mb-5 font-display text-[24px] font-semibold tracking-wide text-fg lg:mb-6 lg:text-[28px]">{share ? t.share.editTitle : t.share.newTitle}</h1>
      {preset && !share && (
        <div className="mb-4 flex items-center gap-2 rounded-xl bg-accent-soft px-3 py-2 text-[13px] text-accent">
          <Share2 className="size-4 shrink-0" />
          {t.share.preset(preset)}
        </div>
      )}
      <div className="grid items-start gap-5 lg:grid-cols-[minmax(0,1.25fr)_minmax(0,1fr)]">
        <div className="flex min-w-0 flex-col gap-4">
          <Card className="p-4">
            <div className="mb-2 flex items-center gap-2">
              <h2 className="flex-1 text-xs font-semibold tracking-wide text-fg-3">{t.share.content}</h2>
              {projList.length > 1 && (
                <Menu
                  trigger={
                    <Button size="sm" variant="ghost">
                      {projName ?? t.reqs.allProjects}
                      <ChevronDown className="size-3.5" />
                    </Button>
                  }
                  items={projList.map((p) => ({ label: p.name, hint: p.remote, checked: p.id === f.project, onSelect: () => p.id !== f.project && h.set({ project: p.id, items: [] }) }))}
                />
              )}
            </div>
            {cand.isPending ? (
              <div className="grid gap-2 py-2">
                <Skeleton className="h-8" />
                <Skeleton className="h-8" />
                <Skeleton className="h-8" />
              </div>
            ) : (
              <TreePicker key={f.project} tree={tree} items={items} preset={preset} onChange={(x) => h.set({ items: x })} />
            )}
            <div className="mt-3 flex flex-wrap items-center gap-x-3 gap-y-1 border-t border-line pt-3 text-[12.5px] text-fg-3">
              <span>{parts.length ? parts.join(t.common.sep) : t.share.picked}</span>
              <span className="sm:ml-auto">{t.share.followsReq}</span>
            </div>
          </Card>
          <details open={desktop} className="group rounded-2xl border border-line bg-surface p-4 shadow-card">
            <summary className="flex cursor-pointer list-none items-center gap-2 text-xs font-semibold tracking-wide text-fg-3 lg:pointer-events-none [&::-webkit-details-marker]:hidden">
              {t.share.graph}
              <ChevronRight className="ml-auto size-4 transition-transform group-open:rotate-90 lg:hidden" />
            </summary>
            <div className="mt-3 overflow-x-auto">
              <ShareGraph tree={tree} inc={r.inc} />
            </div>
          </details>
        </div>
        <div className="flex min-w-0 flex-col gap-4">
          <Card className="grid gap-4 p-4">
            <h2 className="text-xs font-semibold tracking-wide text-fg-3">{t.share.access}</h2>
            <label className="block">
              <span className="mb-1.5 block text-[13px] font-medium text-fg-2">{t.share.name}</span>
              <Input value={f.name} onChange={(e) => h.set({ name: e.target.value })} placeholder={t.share.namePlaceholder} maxLength={80} />
            </label>
            <div>
              <label className="block">
                <span className="mb-1.5 block text-[13px] font-medium text-fg-2">{t.share.password}</span>
                <Input
                  value={f.password}
                  disabled={f.clearPw}
                  onChange={(e) => h.set({ password: e.target.value })}
                  placeholder={share?.has_password ? t.share.passwordKeep : t.share.passwordPlaceholder}
                  autoComplete="off"
                  maxLength={128}
                />
              </label>
              <div className="mt-1.5 flex items-start gap-2 text-xs leading-relaxed text-fg-3">
                <span className="flex-1">{f.clearPw ? t.share.passwordRemoved : t.share.passwordHint}</span>
                {share?.has_password && (
                  <button type="button" onClick={() => h.set({ clearPw: !f.clearPw, password: '' })} className="shrink-0 text-accent hover:underline">
                    {f.clearPw ? t.share.keepPassword : t.share.removePassword}
                  </button>
                )}
              </div>
            </div>
            <ExpiryPicker exp={f.exp} date={f.date} onChange={(exp, date) => h.set({ exp, date })} />
          </Card>
          {!share && (
            <Card className="p-4">
              <h2 className="text-xs font-semibold tracking-wide text-fg-3">{t.share.links}</h2>
              <p className="mt-1 mb-3 text-xs text-fg-3">{t.share.linksNewHint}</p>
              {f.links.length > 0 && (
                <div className="mb-3 flex flex-wrap gap-1.5">
                  {f.links.map((n, i) => (
                    <span key={`${n}-${i}`} className="inline-flex items-center gap-1 rounded-lg border border-line bg-surface-2 py-0.5 pr-1 pl-2.5 text-[13px] text-fg">
                      {n}
                      <button type="button" aria-label={t.share.removeLink} onClick={() => h.set({ links: f.links.filter((_, j) => j !== i) })} className="flex size-5 items-center justify-center rounded text-fg-3 hover:bg-hover hover:text-fg">
                        <X className="size-3.5" />
                      </button>
                    </span>
                  ))}
                </div>
              )}
              <form
                className="flex gap-2"
                onSubmit={(e) => {
                  e.preventDefault()
                  addLink()
                }}
              >
                <Input value={link} onChange={(e) => setLink(e.target.value)} placeholder={t.share.linkPlaceholder} maxLength={40} aria-label={t.share.colGuest} />
                <Button type="submit" variant="soft" disabled={!link.trim()} icon={<Plus className="size-4" />}>
                  {t.share.addLink}
                </Button>
              </form>
            </Card>
          )}
          {c.sessions > 0 && (
            <div className="flex items-center gap-2 rounded-xl bg-wait-soft px-3 py-2 text-[13px] text-wait">
              <Eye className="size-4 shrink-0" />
              {t.share.sessionsVisible}
            </div>
          )}
          <div>
            <div className="flex flex-wrap items-center justify-end gap-2">
              {missingWhy && <span className="mr-auto text-xs text-fg-3">{missingWhy}</span>}
              <Button variant="ghost" onClick={() => navigate(share ? `/shares/${share.id}` : '/shares')}>
                {t.common.cancel}
              </Button>
              <Button variant="primary" loading={busy} disabled={!!missingWhy} onClick={() => void save()}>
                {share ? t.common.save : t.share.createOk}
              </Button>
            </div>
            <DraftLine h={h} className="justify-end" />
          </div>
        </div>
      </div>
    </>
  )
}

// 新建时的初始值：有效期默认 30 天
export function newForm(project: string, items: ShareItem[]): ShareForm {
  return { project, name: '', password: '', clearPw: false, exp: '30', date: isoDay(new Date(Date.now() + 30 * 86_400_000)), items, links: [] }
}

export function editForm(x: Share): ShareForm {
  return {
    project: x.project_id ?? '',
    name: x.name,
    password: '',
    clearPw: false,
    exp: x.expires_at ? 'custom' : 'forever',
    date: x.expires_at ? isoDay(new Date(x.expires_at)) : isoDay(new Date(Date.now() + 30 * 86_400_000)),
    items: x.items.map((it) => ({ kind: it.kind, ref: it.ref, auto: it.auto, excluded: it.excluded })),
    links: [],
  }
}

