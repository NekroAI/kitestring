import { useState } from 'react'
import { ShieldCheck, Undo2 } from 'lucide-react'
import { toast } from 'sonner'
import type { Decision, Grant } from '../api/client'
import { useGrants, useProjects, useSessions } from '../api/queries'
import { t } from '../i18n'
import { absTime, cx, useNow } from '../lib/util'
import { Badge, Button, Field, Input, Segmented, Textarea } from '../ui/base'
import { Menu, Panel } from '../ui/overlay'
import { RoleTag } from '../ui/marks'
import { Linkify, RefChip } from '../ui/refmark'
import { useActions } from './actions'
import { sessionTitle } from './SessionCard'

// 参谋授权窗口（R-163）：你不在时授权一个 Adv 会话，在到期前代你答复范围内的决策；代答留痕、可推翻。
// 顶部横幅显示生效中的窗口（至几点、已代答几条、撤销）；授权面板里开窗口、看代答记录与授权记录。

const clock = (iso: string) => new Date(iso).toLocaleTimeString(undefined, { hour: '2-digit', minute: '2-digit', hour12: false })

// isActive：窗口生效中且没到期（到期按本机时间判断，不等服务端刷新）。
const isActive = (g: Grant, now: number) => g.status === 'active' && new Date(g.expires_at).getTime() > now

function useSessionName() {
  const sessions = useSessions()
  return (key?: string | null) => {
    const s = (sessions.data?.sessions ?? []).find((x) => x.key === key)
    return s ? sessionTitle(s) : key ?? ''
  }
}

// GrantBanner：页面顶部「参谋授权中 · 至 9:00 · 已代答 n 条 · 代答记录 · 撤销」。
export function GrantBanner() {
  const now = useNow()
  const grants = useGrants()
  const { revokeGrant } = useActions()
  const [panel, setPanel] = useState<string | null>(null)
  const active = (grants.data?.grants ?? []).filter((g) => isActive(g, now))
  return (
    <>
      {active.map((g) => (
        <div key={g.id} className="mb-4 flex flex-wrap items-center gap-x-3 gap-y-2 rounded-xl border border-accent/30 bg-accent-soft/60 px-3.5 py-2.5 text-sm">
          <ShieldCheck className="size-4 shrink-0 text-accent" />
          <span className="font-medium text-fg">{t.grant.banner(clock(g.expires_at))}</span>
          <span className="min-w-0 truncate text-fg-2">
            {t.grant.bannerProject(g.project_name)}
            {t.common.sep}
            {t.grant.scopeShort[g.max_risk]}
            {t.common.sep}
            {t.grant.proxiedCount(g.proxied)}
          </span>
          <span className="ml-auto flex shrink-0 gap-2">
            <Button size="sm" onClick={() => setPanel(g.project_id)}>
              {t.grant.viewAll}
            </Button>
            <Button size="sm" onClick={async () => (await revokeGrant(g.code)) && toast(t.grant.revoked)}>
              {t.grant.revoke}
            </Button>
          </span>
        </div>
      ))}
      <GrantPanel projectId={panel ?? ''} open={!!panel} onClose={() => setPanel(null)} />
    </>
  )
}

const localInput = (d: Date) => {
  const p = (n: number) => String(n).padStart(2, '0')
  return `${d.getFullYear()}-${p(d.getMonth() + 1)}-${p(d.getDate())}T${p(d.getHours())}:${p(d.getMinutes())}`
}

function OpenForm({ projectId }: { projectId: string }) {
  const now = useNow()
  const sessions = useSessions()
  const { openGrant } = useActions()
  const advs = (sessions.data?.sessions ?? []).filter((s) => s.project_id === projectId && s.role === 'advisor' && s.state !== 'ended' && s.state !== 'lost' && !s.done_at)
  const [key, setKey] = useState('')
  const [until, setUntil] = useState(() => localInput(new Date(Date.now() + 8 * 3600_000))) // 默认 8 小时后，可改
  const [risk, setRisk] = useState<'low' | 'medium'>('low')
  const [busy, setBusy] = useState(false)
  const pick = advs.find((s) => s.key === key) ?? advs[0]
  const presets = [2, 4, 8, 12].map((h) => ({ label: t.grant.presetHours(h), at: new Date(now + h * 3600_000) }))
  if (!advs.length) return <p className="rounded-xl bg-sunken px-3.5 py-3 text-sm text-fg-2">{t.grant.noAdv}</p>
  return (
    <div className="space-y-4">
      <Field label={t.grant.who}>
        <Menu
          align="start"
          width="min-w-[280px]"
          trigger={
            <Button className="w-full justify-start gap-2">
              <RoleTag role="advisor" />
              <span className="min-w-0 truncate">{pick ? sessionTitle(pick) : t.grant.pickAdv}</span>
            </Button>
          }
          items={advs.map((s) => ({ label: sessionTitle(s), hint: `${s.device_name}${t.common.sep}${s.key}`, checked: s.key === pick?.key, onSelect: () => setKey(s.key) }))}
        />
      </Field>
      <Field label={t.grant.until}>
        <div className="flex flex-wrap gap-1.5">
          {presets.map((p) => (
            <Button key={p.label} size="sm" variant={localInput(p.at) === until ? 'soft' : undefined} onClick={() => setUntil(localInput(p.at))}>
              {p.label}
            </Button>
          ))}
        </div>
        <Input type="datetime-local" value={until} onChange={(e) => setUntil(e.target.value)} className="mt-2 tabular" />
      </Field>
      <Field label={t.grant.scope}>
        <Segmented value={risk} onChange={setRisk} options={[{ value: 'low', label: t.grant.scopeLow }, { value: 'medium', label: t.grant.scopeMedium }]} />
      </Field>
      <div className="space-y-1.5 rounded-xl bg-sunken px-3.5 py-3 text-xs leading-relaxed text-fg-2">
        <p>{t.grant.hard}</p>
        <p>{t.grant.quiet}</p>
      </div>
      <Button
        variant="primary"
        icon={<ShieldCheck className="size-4" />}
        disabled={!pick || !until || busy}
        onClick={async () => {
          if (!pick) return
          setBusy(true)
          if (await openGrant({ project_id: projectId, session_key: pick.key, expires_at: new Date(until).toISOString(), max_risk: risk })) toast.success(t.grant.started)
          setBusy(false)
        }}
      >
        {t.grant.start}
      </Button>
    </div>
  )
}

// OverrideBox：推翻参谋的代答，写下你的答复作为更正送回提问方。
// startOpen / onClose：离开期间小结里由行内按钮打开，取消或送出后收回（R-165）。
export function OverrideBox({ d, startOpen = false, onClose }: { d: Decision; startOpen?: boolean; onClose?: () => void }) {
  const { overrideDecision } = useActions()
  const [open, setOpenRaw] = useState(startOpen)
  const setOpen = (v: boolean) => {
    setOpenRaw(v)
    if (!v) onClose?.()
  }
  const [text, setText] = useState('')
  const [busy, setBusy] = useState(false)
  if (!open)
    return (
      <div>
        <Button size="sm" onClick={() => setOpen(true)}>
          <Undo2 className="size-3.5" />
          {t.grant.override}
        </Button>
      </div>
    )
  return (
    <div className="space-y-2">
      <Textarea autoFocus minRows={2} value={text} placeholder={t.grant.overridePlaceholder} onChange={(e) => setText(e.target.value)} />
      <div className="flex justify-end gap-2">
        <Button size="sm" onClick={() => setOpen(false)}>
          {t.common.cancel}
        </Button>
        <Button
          size="sm"
          variant="primary"
          disabled={!text.trim() || busy}
          onClick={async () => {
            setBusy(true)
            if (await overrideDecision(d.code, text.trim())) {
              toast.success(t.grant.overrideDone)
              setOpen(false)
              setText('')
            }
            setBusy(false)
          }}
        >
          {t.grant.overrideSend}
        </Button>
      </div>
    </div>
  )
}

// ProxyNote：决策上的代答留痕——Adv 代答、风险档、理由；推翻后标出并保留代答原文。
export function ProxyNote({ d, withOverride = true }: { d: Decision; withOverride?: boolean }) {
  const over = !!d.overridden_at
  return (
    <div className="space-y-2">
      <div className="flex flex-wrap items-center gap-1.5 text-[13px] text-fg-2">
        <RoleTag role="advisor" />
        <span>{t.grant.proxied}</span>
        {d.proxy_risk && <Badge tone="neutral">{t.grant.risk[d.proxy_risk]}</Badge>}
        {d.proxy_at && <span className="text-fg-3 tabular">{absTime(d.proxy_at)}</span>}
        {over && <Badge tone="accent">{t.grant.overridden}</Badge>}
      </div>
      <div className={cx('rounded-lg px-3 py-2 text-sm leading-relaxed text-fg', over ? 'bg-sunken/70 text-fg-3' : 'bg-sunken/70')}>
        <span className="mr-1.5 text-fg-3">{t.grant.answer}</span>
        <Linkify text={d.proxy_answer ?? ''} />
      </div>
      <p className="text-[13px] leading-relaxed text-fg-2">
        <span className="mr-1.5 text-fg-3">{t.grant.reason}</span>
        <Linkify text={d.proxy_reason ?? ''} />
      </p>
      {over && (
        <div className="rounded-lg bg-accent-soft/60 px-3 py-2 text-sm leading-relaxed text-fg">
          <span className="mr-1.5 text-fg-3">{t.grant.yours}</span>
          <Linkify text={d.answer} />
        </div>
      )}
      {withOverride && !over && d.status !== 'closed' && <OverrideBox d={d} />}
    </div>
  )
}

function GrantRow({ g, now }: { g: Grant; now: number }) {
  const name = useSessionName()
  const status = isActive(g, now) ? 'active' : g.status === 'active' ? 'expired' : g.status
  return (
    <div className="flex flex-wrap items-center gap-x-2 gap-y-1 py-2.5 text-[13px]">
      <span className="font-mono text-xs text-fg-3">{g.code}</span>
      <Badge tone={status === 'active' ? 'run' : 'neutral'}>{t.grant.status[status]}</Badge>
      <span className="min-w-0 truncate text-fg-2">{name(g.session_key)}</span>
      <span className="text-fg-3 tabular">{t.grant.span(absTime(g.created_at), absTime(g.expires_at))}</span>
      {g.revoked_at && <span className="text-fg-3 tabular">{t.grant.revokedAt(absTime(g.revoked_at))}</span>}
      <span className="text-fg-3">
        {t.grant.scopeShort[g.max_risk]}
        {t.common.sep}
        {t.grant.proxiedCount(g.proxied)}
      </span>
    </div>
  )
}

// GrantPanel：一个项目的参谋授权——生效中的窗口（或开窗口的表单）、代答记录、授权记录。
export function GrantPanel({ projectId, open, onClose }: { projectId: string; open: boolean; onClose: () => void }) {
  const now = useNow()
  const grants = useGrants(projectId)
  const projects = useProjects()
  const name = useSessionName()
  const { revokeGrant } = useActions()
  const list = grants.data?.grants ?? []
  const active = list.find((g) => isActive(g, now))
  const project = projects.data?.projects.find((p) => p.id === projectId)
  return (
    <Panel open={open} onClose={onClose} title={t.grant.title} subtitle={project?.name}>
      <div className="space-y-6">
        <p className="text-sm leading-relaxed text-fg-2">{t.grant.intro}</p>
        {active ? (
          <div className="flex flex-wrap items-center gap-x-3 gap-y-2 rounded-xl border border-accent/30 bg-accent-soft/60 px-3.5 py-3 text-sm">
            <ShieldCheck className="size-4 shrink-0 text-accent" />
            <span className="font-medium text-fg">{t.grant.banner(clock(active.expires_at))}</span>
            <span className="flex min-w-0 items-center gap-1.5 text-fg-2">
              <RoleTag role="advisor" />
              <span className="truncate">{name(active.session_key)}</span>
            </span>
            <span className="text-fg-2">{t.grant.scopeShort[active.max_risk]}</span>
            <Button size="sm" className="ml-auto" onClick={async () => (await revokeGrant(active.code)) && toast(t.grant.revoked)}>
              {t.grant.revoke}
            </Button>
          </div>
        ) : (
          projectId && <OpenForm projectId={projectId} />
        )}

        <section>
          <h3 className="mb-2 text-sm font-medium text-fg">{t.grant.records}</h3>
          {(grants.data?.proxied ?? []).length ? (
            <div className="divide-y divide-line rounded-xl border border-line">
              {(grants.data?.proxied ?? []).map((d) => (
                <div key={d.id} className="space-y-2 px-3.5 py-3">
                  <div className="flex items-start gap-2">
                    <RefChip code={d.code} status={d.status} link={false} />
                    <p className="min-w-0 flex-1 text-sm text-fg">
                      <Linkify text={d.short_title || d.question} />
                    </p>
                    {d.requirement_code && <RefChip code={d.requirement_code} label="title" />}
                  </div>
                  <ProxyNote d={d} />
                </div>
              ))}
            </div>
          ) : (
            <p className="text-sm text-fg-3">{t.grant.recordsEmpty}</p>
          )}
        </section>

        {list.length > 0 && (
          <section>
            <h3 className="mb-1 text-sm font-medium text-fg">{t.grant.history}</h3>
            <div className="divide-y divide-line">
              {list.map((g) => (
                <GrantRow key={g.id} g={g} now={now} />
              ))}
            </div>
          </section>
        )}
      </div>
    </Panel>
  )
}
