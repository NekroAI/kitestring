import { useEffect, useMemo, useState, type ReactNode } from 'react'
import { useSearchParams } from 'react-router'
import { useQueryClient } from '@tanstack/react-query'
import { toast } from 'sonner'
import { Ban, Check, ChevronDown, Eye, EyeOff, KeyRound, Plus, Search, ShieldCheck, X } from 'lucide-react'
import { api, errMsg, unwrap, type Decision } from '../api/client'
import { useProjects, useSecret, useSecrets, useSessions, type SecretLog, type SecretView } from '../api/queries'
import { t } from '../i18n'
import { cx, relTime, useNow } from '../lib/util'
import { addCommand, isSecretReq, DEFAULT_TTL, expiryOf, filledValues, filterSecrets, groupByDay, missingValues, envFromLabel, envOK, splitRedacted, statusCounts, timeLeft, TTL_DAYS, unionCommands } from '../lib/secrets'
import { Button, Card, Empty, Input, Segmented, Skeleton, Switch, Textarea, Tip } from '../ui/base'
import { Menu, Modal, Panel, useAskReason } from '../ui/overlay'
import { Md } from '../ui/markdown'
import { RefChip } from '../ui/refmark'
import { displayState, RoleBadge, RoleTag, StatusPill as SessionPill } from '../ui/marks'
import { PendingCover, schedule, usePending } from './verdict'
import { decKey } from './outbox'
import { useOpenAsk, useOpenSession } from './nav'
import { sessionTitle } from './SessionCard'
import { dayText } from './shares'

// 凭据库（R-295，D-1438 方案图）：
//   - 「待处理」里的凭据申请（SecretRequestBody，在审批抽屉里）：用途、关联需求、每个值的获取方式和密码框；授权（项目、有效期、允许的命令、写入磁盘）；
//     「拒绝…」/「保存并授权」走全站同一个撤销期（docs/principles.md 第 8 条），保存后申请方立即收到。
//   - 侧边栏「凭据」页（SecretsPage）：列表（值永远掩码）、状态筛选、搜索、项目；替换值、撤销 / 重新授权、新建。
//   - 详情抽屉（SecretDetail）：字段、授权、取用记录时间线（含授权变更，命令里的已知值显示「已隐去」）。
// 值只在提交时随请求发出，不进本机草稿（principles 第 9 条的草稿只存在浏览器里，凭据的值不该留在那里）。

export { isSecretReq }

// ---------- 小零件 ----------

const pillTone: Record<string, string> = { active: 'bg-run-soft text-run', pending: 'bg-wait-soft text-wait', expired: 'bg-idle-soft text-idle', revoked: 'bg-idle-soft text-idle' }
const dotTone: Record<string, string> = { active: 'bg-run', pending: 'bg-wait', expired: 'bg-idle', revoked: 'bg-idle' }

export function SecretStatusPill({ status }: { status: string }) {
  return (
    <span data-secret-status={status} className={cx('inline-flex shrink-0 items-center gap-1.5 rounded-full px-2 text-xs leading-5 font-medium whitespace-nowrap', pillTone[status])}>
      <span className={cx('size-1.5 rounded-full', dotTone[status])} />
      {t.secret.status[status]}
    </span>
  )
}

const hhmm = (iso: string) => new Date(iso).toLocaleTimeString(undefined, { hour: '2-digit', minute: '2-digit', hour12: false })
const dayTime = (iso: string) => `${dayText(iso)} ${hhmm(iso)}`

// authLine：状态下面的一行——剩多久 / 长期（可写入磁盘）/ 申请于多久前 / 哪天到期 / 哪天撤回。
function authLine(x: SecretView, now: number) {
  if (x.status === 'pending') return t.secret.requestedAt(relTime(x.updated_at, now))
  if (x.status === 'revoked') return t.secret.revokedAt(dayText(x.revoked_at))
  if (x.status === 'expired') return t.secret.expiredAt(dayText(x.expires_at))
  const left = timeLeft(x.expires_at, now)
  const base = !x.expires_at ? t.secret.forever : left && 'days' in left ? t.secret.leftDays(left.days) : t.secret.leftHours(left && 'hours' in left ? left.hours : 1)
  return x.disk ? `${base}${t.common.sep}${t.secret.diskOk}` : base
}

const Dots = ({ className }: { className?: string }) => (
  <span aria-label={t.secret.masked} className={cx('font-mono tracking-[0.2em] text-fg-2 select-none', className)}>
    ••••••••
  </span>
)

function CmdChip({ cmd, muted, onRemove }: { cmd: string; muted?: boolean; onRemove?: () => void }) {
  return (
    <span className={cx('inline-flex max-w-full min-w-0 items-center gap-1 rounded-md bg-sunken px-2 py-0.5 font-mono text-[12px] leading-5', muted ? 'text-fg-3' : 'text-fg-2')}>
      <span className="truncate">{cmd}</span>
      {onRemove && (
        <button type="button" aria-label={t.secret.removeCmd(cmd)} onClick={onRemove} className="-mr-1 rounded p-0.5 text-fg-3 transition hover:bg-hover hover:text-fg">
          <X className="size-3" />
        </button>
      )}
    </span>
  )
}

function AnyCmd() {
  return <span className="inline-flex shrink-0 items-center rounded-full bg-wait-soft px-2 text-xs leading-5 font-medium text-wait">{t.secret.anyCommand}</span>
}

// Cmd：取用记录里的命令，已知值画成「已隐去」小标。
function Cmd({ cmd }: { cmd: string }) {
  return (
    <span className="min-w-0 truncate font-mono text-[12.5px] text-fg-2">
      {splitRedacted(cmd).map((p, i) =>
        p.redacted ? (
          <span key={i} className="mx-0.5 rounded bg-idle-soft px-1 font-sans text-[11px] text-fg-3">
            {t.secret.redacted}
          </span>
        ) : (
          <span key={i}>{p.text}</span>
        ),
      )}
    </span>
  )
}

// Who：会话（身份 + 会话名），记录里存的是当时的快照。
function Who({ role, title, fallback }: { role?: string; title?: string; fallback?: string }) {
  const name = title || fallback || ''
  return (
    <span className="flex min-w-0 items-center gap-1.5">
      {role && <RoleTag role={role} presence="standby" />}
      {name && <span className="min-w-0 truncate text-[13px] text-fg">「{name}」</span>}
    </span>
  )
}

// ---------- 值和授权的表单 ----------

// ValueInput：密码框，右侧眼睛切换显示；不让浏览器记住、不拼写检查。
function ValueInput({ value, onChange, placeholder, label, autoFocus }: { value: string; onChange: (v: string) => void; placeholder: string; label: string; autoFocus?: boolean }) {
  const [show, setShow] = useState(false)
  return (
    <div className="relative">
      <Input
        type={show ? 'text' : 'password'}
        value={value}
        autoComplete="new-password"
        spellCheck={false}
        autoCapitalize="off"
        aria-label={label}
        data-1p-ignore=""
        data-lpignore="true"
        placeholder={placeholder}
        autoFocus={autoFocus}
        onChange={(e) => onChange(e.target.value)}
        className="pr-10 font-mono"
      />
      <button type="button" aria-label={show ? t.secret.hide : t.secret.show} onClick={() => setShow(!show)} className="absolute top-1/2 right-2 -translate-y-1/2 rounded-md p-1 text-fg-3 transition hover:bg-hover hover:text-fg">
        {show ? <EyeOff className="size-4" /> : <Eye className="size-4" />}
      </button>
    </div>
  )
}

type Field = { env: string; label: string; how?: string; filled?: boolean }

// FieldValues：每个值一张卡：显示名 + 环境变量名、获取方式、密码框。已填过的空着表示保留原值。
function FieldValues({ fields, values, onChange, autoFocus }: { fields: Field[]; values: Record<string, string>; onChange: (v: Record<string, string>) => void; autoFocus?: boolean }) {
  return (
    <div className="grid gap-3">
      {fields.map((f, i) => (
        <div key={f.env} data-secret-field={f.env} className="rounded-xl border border-line bg-surface px-4 py-3">
          <div className="flex min-w-0 items-baseline gap-2">
            <b className="shrink-0 text-[14px] font-semibold text-fg">{f.label}</b>
            <span className="min-w-0 truncate font-mono text-[12px] text-fg-3">{f.env}</span>
          </div>
          {f.how && <Md text={f.how} className="mt-1 text-[13px] leading-relaxed text-fg-2" />}
          <div className="mt-2">
            <ValueInput label={f.label} value={values[f.env] ?? ''} onChange={(v) => onChange({ ...values, [f.env]: v })} placeholder={f.filled ? t.secret.keep : t.secret.paste} autoFocus={autoFocus && i === 0} />
          </div>
        </div>
      ))}
    </div>
  )
}

export type AuthState = { projects: string[]; days: number; allowed: string[]; disk: boolean }

function Row({ label, children, last }: { label: string; children: ReactNode; last?: boolean }) {
  return (
    <div className={cx('grid grid-cols-[5.5rem_minmax(0,1fr)] items-start gap-3 py-3', !last && 'border-b border-line')}>
      <span className="pt-1.5 text-[13px] text-fg-2">{label}</span>
      <div className="min-w-0">{children}</div>
    </div>
  )
}

// AuthFields：项目、有效期（默认 7 天）、允许的命令、写入磁盘。suggest 是会话建议的有效期（申请时才有）。
function AuthFields({ value, onChange, suggest }: { value: AuthState; onChange: (v: AuthState) => void; suggest?: number }) {
  const projects = useProjects()
  const [cmd, setCmd] = useState('')
  const list = (projects.data?.projects ?? []).filter((p) => !p.hidden || value.projects.includes(p.id))
  const name = (id: string) => list.find((p) => p.id === id)?.name ?? id
  const set = (p: Partial<AuthState>) => onChange({ ...value, ...p })
  const at = expiryOf(value.days)
  const addCmd = () => {
    set({ allowed: addCommand(value.allowed, cmd) })
    setCmd('')
  }
  const rest = list.filter((p) => !value.projects.includes(p.id))
  return (
    <div>
      <Row label={t.secret.project}>
        <div className="flex flex-wrap items-center gap-1.5">
          {value.projects.map((id) => (
            <span key={id} className="inline-flex items-center gap-1 rounded-lg border border-line bg-surface px-2.5 py-1 text-[13px] text-fg">
              {name(id)}
              {value.projects.length > 1 && (
                <button type="button" aria-label={t.secret.removeProject(name(id))} onClick={() => set({ projects: value.projects.filter((x) => x !== id) })} className="-mr-1 rounded p-0.5 text-fg-3 hover:bg-hover hover:text-fg">
                  <X className="size-3" />
                </button>
              )}
            </span>
          ))}
          {rest.length > 0 && (
            <Menu
              align="start"
              trigger={
                <Button size="sm" variant="ghost" icon={<Plus className="size-3.5" />}>
                  {t.secret.addProject}
                </Button>
              }
              items={rest.map((p) => ({ label: p.name, onSelect: () => set({ projects: [...value.projects, p.id] }) }))}
            />
          )}
        </div>
      </Row>
      <Row label={t.secret.expiry}>
        <div className="flex flex-wrap items-center gap-x-3 gap-y-1.5">
          <Segmented size="sm" value={String(value.days)} onChange={(v) => set({ days: Number(v) })} options={TTL_DAYS.map((d) => ({ value: String(d), label: t.secret.ttl(d) }))} />
          <span className="text-[13px] text-fg-2 tabular">{at ? t.secret.until(dayTime(at.toISOString())) : t.secret.forever}</span>
          {suggest !== undefined && <span className="ml-auto text-xs text-fg-3">{t.secret.suggest(suggest)}</span>}
        </div>
      </Row>
      <Row label={t.secret.allowed}>
        <div className="flex flex-wrap items-center gap-1.5">
          {value.allowed.map((c) => (
            <CmdChip key={c} cmd={c} onRemove={() => set({ allowed: value.allowed.filter((x) => x !== c) })} />
          ))}
          {!value.allowed.length && <AnyCmd />}
          <span className="flex min-w-[14rem] flex-1 items-center gap-1.5">
            <Input
              value={cmd}
              aria-label={t.secret.allowed}
              placeholder={t.secret.cmdPlaceholder}
              spellCheck={false}
              autoCapitalize="off"
              onChange={(e) => setCmd(e.target.value)}
              onKeyDown={(e) => e.key === 'Enter' && !e.nativeEvent.isComposing && (e.preventDefault(), addCmd())}
              className="h-8 font-mono text-[12.5px]"
            />
            <Button size="sm" variant="ghost" icon={<Plus className="size-3.5" />} disabled={!cmd.trim()} onClick={addCmd}>
              {t.secret.addCmd}
            </Button>
          </span>
        </div>
        <p className="mt-1.5 text-xs leading-relaxed text-fg-3">{t.secret.cmdHint}</p>
      </Row>
      <Row label={t.secret.disk} last>
        <label className="flex items-center gap-2.5 pt-1">
          <Switch checked={value.disk} onChange={(v) => set({ disk: v })} />
          <span className="text-[13px] text-fg-2">{value.disk ? t.secret.diskOn : t.secret.diskOff}</span>
        </label>
        {value.disk && <p className="mt-1.5 text-xs leading-relaxed text-fg-3">{t.secret.diskHint}</p>}
      </Row>
    </div>
  )
}

function Guard({ className }: { className?: string }) {
  return (
    <p data-secret-guard="" className={cx('flex items-start gap-2 text-[13px] leading-relaxed text-fg-3', className)}>
      <ShieldCheck className="mt-0.5 size-4 shrink-0" />
      {t.secret.guard}
    </p>
  )
}

function Section({ title, children }: { title: string; children: ReactNode }) {
  return (
    <section>
      <h4 className="mb-2 text-[13px] font-semibold text-fg-2">{title}</h4>
      {children}
    </section>
  )
}

// ---------- 接口 ----------

function useSecretApi() {
  const qc = useQueryClient()
  const refresh = () => void qc.invalidateQueries({ queryKey: ['secrets'] })
  const call = async <T,>(fn: () => Promise<T>, what: string, also: string[] = []) => {
    try {
      const r = await fn()
      refresh()
      for (const k of also) void qc.invalidateQueries({ queryKey: [k] })
      return r
    } catch (e) {
      toast.error(errMsg(e, what), { duration: 8000 })
      return undefined
    }
  }
  return {
    authorize: (x: { id: string; name: string }, a: AuthState, values: Record<string, string>) =>
      call(() => unwrap(api.POST('/api/secrets/{ref}/authorize', { params: { path: { ref: x.id } }, body: { ...a, values: filledValues(values) } })), t.secret.fail.save(x.name), ['decisions', 'sessions']),
    replace: (x: { id: string; name: string }, values: Record<string, string>) =>
      call(() => unwrap(api.POST('/api/secrets/{ref}/values', { params: { path: { ref: x.id } }, body: { values: filledValues(values) } })), t.secret.fail.replace(x.name)),
    revoke: (x: { id: string; name: string }) => call(() => unwrap(api.POST('/api/secrets/{ref}/revoke', { params: { path: { ref: x.id } } })), t.secret.fail.revoke(x.name)),
    reject: (x: { id: string }, code: string, note: string) =>
      call(() => unwrap(api.POST('/api/secrets/{ref}/reject', { params: { path: { ref: x.id } }, body: { note } })), t.secret.fail.reject(code), ['decisions', 'sessions']),
    create: (body: { name: string; purpose: string; fields: { env: string; label: string; value: string }[] } & AuthState) =>
      call(() => unwrap(api.POST('/api/secrets', { body })), t.secret.fail.create),
  }
}

// checkAuth：保存前的检查；有问题时提示并返回 false。
function checkAuth(fields: Field[], values: Record<string, string>, a: AuthState) {
  const miss = missingValues(fields, values)
  if (miss.length) {
    toast.error(t.secret.needValue(miss[0].label))
    return false
  }
  if (!a.projects.length) {
    toast.error(t.secret.needProject)
    return false
  }
  return true
}

// ---------- 待处理里的凭据申请 ----------

// SecretReqLine：「待处理」条目的第三行——关联需求、会话建议的有效期。
export function SecretReqLine({ d }: { d: Decision }) {
  const list = useSecrets()
  const x = (list.data ?? []).find((v) => v.request_code === d.code)
  return (
    <span className="flex min-w-0 items-center gap-2">
      {d.requirement_code && (
        <span className="flex min-w-0 items-center gap-1 rounded-md border border-line px-1.5 leading-[18px]">
          <span className="shrink-0 font-mono text-[11px] text-fg-3">{d.requirement_code}</span>
          <span className="min-w-0 truncate">{d.requirement_title}</span>
        </span>
      )}
      {x?.ask && <span className="shrink-0 text-fg-3">{t.secret.suggestShort(x.ask.days)}</span>}
    </span>
  )
}

// initAuth：申请抽屉的初始授权——待填写的用申请里的建议；已有授权的保留原来的项目和命令，再并上这次要的。
function initAuth(x: SecretView): AuthState {
  const ask = x.ask
  const fresh = x.status === 'pending'
  const projects = fresh ? [] : [...x.projects]
  if (ask?.project_id && !projects.includes(ask.project_id)) projects.push(ask.project_id)
  return {
    projects: projects.length ? projects : x.projects,
    days: ask?.days ?? DEFAULT_TTL,
    allowed: fresh ? (ask?.allowed ?? x.allowed) : unionCommands(x.allowed, ask?.allowed ?? []),
    disk: x.disk,
  }
}

// SecretRequestBody：审批抽屉里的凭据申请（D-1438 图 1）。
export function SecretRequestBody({ d, top }: { d: Decision; top: ReactNode }) {
  const q = useSecret(d.code)
  const x = q.data?.secret
  if (!x)
    return (
      <div className="flex min-h-0 flex-1 flex-col">
        {top}
        <div className="space-y-3 px-5 py-6">
          <Skeleton className="h-6 w-1/2" />
          <Skeleton className="h-24" />
          <Skeleton className="h-24" />
        </div>
      </div>
    )
  return <RequestForm key={x.id + d.code} d={d} x={x} top={top} />
}

function RequestForm({ d, x, top }: { d: Decision; x: SecretView; top: ReactNode }) {
  const ops = useSecretApi()
  const ask = useAskReason()
  const sessions = useSessions()
  const openSession = useOpenSession()
  const now = useNow()
  const [values, setValues] = useState<Record<string, string>>({})
  const [auth, setAuth] = useState<AuthState>(() => initAuth(x))
  const s = (sessions.data?.sessions ?? []).find((v) => v.key === d.session_key)
  const who = s ? sessionTitle(s) : ''
  const key = decKey(d.code)
  const pend = usePending(key)
  const save = () => {
    if (pend || !checkAuth(x.fields, values, auth)) return
    const v = values
    const a = auth
    schedule(key, { act: 'pass', scene: 'request', subject: d.code, commit: async () => !!(await ops.authorize(x, a, v)) })
  }
  const reject = async () => {
    const r = await ask({ title: t.verdict.rejectTitle('request', d.code), description: t.secret.rejectDesc, confirmText: t.verdict.back('request'), placeholder: t.verdict.rejectPlaceholder('request'), kinds: [] })
    if (r) schedule(key, { act: 'back', scene: 'request', subject: d.code, commit: async () => !!(await ops.reject(x, d.code, r.note)) })
  }
  useEffect(() => {
    const onKey = (e: KeyboardEvent) => {
      if ((e.metaKey || e.ctrlKey) && e.key === 'Enter') {
        e.preventDefault()
        save()
      }
    }
    window.addEventListener('keydown', onKey)
    return () => window.removeEventListener('keydown', onKey)
  })
  return (
    <div data-secret-request={d.code} className="flex min-h-0 flex-1 flex-col">
      {top}
      <div className="min-h-0 flex-1 space-y-6 overflow-y-auto overscroll-contain px-4 pt-2 pb-6 sm:px-6">
        <div>
          {/* 类型词「凭据申请」已在抽屉顶栏，这里只写项目（principles 第 5 条） */}
          {d.project_name && <div className="text-[12.5px] text-fg-3">{d.project_name}</div>}
          <h3 className="mt-1 font-display text-[22px] leading-snug font-semibold text-fg">{x.name}</h3>
          {s && (
            <button type="button" onClick={() => openSession(s.key)} className="mt-3 flex w-full min-w-0 items-center gap-2 rounded-xl border border-line bg-surface px-3 py-2 text-left text-[13px] transition hover:border-line-strong">
              <RoleBadge s={s} />
              <span className="min-w-0 truncate font-medium text-fg">「{who}」</span>
              <span className="shrink-0 text-fg-3">{s.device_name}</span>
              <SessionPill state={displayState(s)} since={s.state_since} now={now} />
              <ChevronDown className="ml-auto size-4 shrink-0 -rotate-90 text-fg-3" />
            </button>
          )}
        </div>
        <Section title={t.secret.purpose}>
          {d.context && <Md text={d.context} className="text-[14px] leading-relaxed text-fg" />}
          {d.requirement_code && (
            <div className="mt-2">
              <RefChip code={d.requirement_code} />
            </div>
          )}
        </Section>
        <Section title={t.secret.values}>
          <FieldValues fields={x.fields} values={values} onChange={setValues} autoFocus />
        </Section>
        <Section title={t.secret.auth}>
          <AuthFields value={auth} onChange={setAuth} suggest={x.ask?.days} />
        </Section>
        <Guard />
      </div>
      <footer className="relative flex shrink-0 items-center gap-3 border-t border-line bg-surface px-4 pt-3 pb-[max(0.875rem,env(safe-area-inset-bottom))] sm:px-6">
        <Button onClick={() => void reject()} disabled={!!pend}>
          {t.secret.reject}
        </Button>
        <span className="ml-auto min-w-0 truncate text-[12.5px] text-fg-3 max-sm:hidden">{who ? t.secret.deliverHint(who) : t.secret.deliverAny}</span>
        <Button variant="primary" onClick={save} disabled={!!pend} className="max-sm:ml-auto">
          {t.secret.save}
          <span className="ml-1 text-[11px] opacity-70">⌘↵</span>
        </Button>
        <PendingCover k={key} />
      </footer>
    </div>
  )
}

// ---------- 凭据页 ----------

function useOpenSecret() {
  const [, setParams] = useSearchParams()
  return (id: string | null) =>
    setParams((p) => {
      const n = new URLSearchParams(p)
      if (id) n.set('k', id)
      else n.delete('k')
      return n
    })
}

type Dialog = { kind: 'replace' | 'edit' | 'regrant'; x: SecretView } | { kind: 'create' } | null

// useRevoke：撤销授权走全站同一个撤销期（principles 第 8 条）。
function useRevoke() {
  const ops = useSecretApi()
  return (x: SecretView) =>
    schedule(`secret:${x.id}`, {
      act: 'move',
      label: t.secret.revokeAct,
      subject: x.name,
      beacon: { method: 'POST', url: `/api/secrets/${x.id}/revoke`, body: {} },
      commit: async () => !!(await ops.revoke(x)),
    })
}

function RowActions({ x, onDialog }: { x: SecretView; onDialog: (d: Dialog) => void }) {
  const openAsk = useOpenAsk()
  const revoke = useRevoke()
  const stop = (e: React.SyntheticEvent) => e.stopPropagation()
  if (x.status === 'pending')
    return (
      <Button size="sm" onClick={(e) => (stop(e), x.request_code && openAsk(`d:${x.request_code}`))} disabled={!x.request_pending}>
        {t.secret.fill}
      </Button>
    )
  return (
    <span className="flex items-center gap-1" onClick={stop}>
      <Button size="sm" variant="ghost" onClick={() => onDialog({ kind: 'replace', x })}>
        {t.secret.replace}
      </Button>
      {x.status === 'active' ? (
        <Button size="sm" variant="ghost" className="!text-danger" onClick={() => revoke(x)}>
          {t.secret.revoke}
        </Button>
      ) : (
        <Button size="sm" variant="ghost" onClick={() => onDialog({ kind: 'regrant', x })}>
          {t.secret.regrant}
        </Button>
      )}
    </span>
  )
}

function LastUse({ x, now }: { x: SecretView; now: number }) {
  const l = x.last_use
  if (!l) return <span className="text-[13px] text-fg-3">{t.secret.none}</span>
  return (
    <div className="min-w-0">
      <Who role={l.session_role} title={l.session_title} fallback={l.session_key} />
      <div className="mt-0.5 text-[12.5px] text-fg-3 tabular">{relTime(l.at, now)}</div>
    </div>
  )
}

const gridCols = 'lg:grid-cols-[minmax(0,1.6fr)_6.5rem_minmax(0,0.9fr)_minmax(0,1fr)_minmax(0,1.3fr)_minmax(0,1.2fr)_12.5rem]'

function SecretRow({ x, now, onOpen, onDialog }: { x: SecretView; now: number; onOpen: () => void; onDialog: (d: Dialog) => void }) {
  const off = x.status === 'expired' || x.status === 'revoked'
  const pend = usePending(`secret:${x.id}`)
  return (
    <div
      role="button"
      tabIndex={0}
      data-secret={x.name}
      onClick={onOpen}
      onKeyDown={(e) => e.key === 'Enter' && onOpen()}
      className={cx('relative grid cursor-pointer grid-cols-[minmax(0,1fr)_auto] items-center gap-x-4 gap-y-1 border-b border-line px-4 py-3 transition-colors last:border-b-0 hover:bg-hover/60', gridCols)}
    >
      <div className="min-w-0">
        <div className={cx('truncate text-[15px] font-semibold', off ? 'text-fg-2' : 'text-fg')}>{x.name}</div>
        <div className="truncate text-[13px] text-fg-3">{x.purpose}</div>
      </div>
      <div className="min-w-0 max-lg:hidden">
        {x.status === 'pending' ? <div className="text-[13px] text-fg-3">{t.secret.unfilled}</div> : <Dots className="text-[13px]" />}
        <div className="text-[12.5px] text-fg-3">{t.secret.fields(x.fields.length)}</div>
      </div>
      <div className="min-w-0 truncate text-[13px] text-fg max-lg:hidden">{x.project_names.join('、')}</div>
      <div className="min-w-0 max-lg:col-start-2 max-lg:row-start-1 max-lg:text-right">
        <SecretStatusPill status={x.status} />
        <div className="mt-0.5 truncate text-[12.5px] text-fg-3 tabular">{authLine(x, now)}</div>
      </div>
      <div className="flex min-w-0 items-center gap-1.5 max-lg:hidden">
        {x.allowed.length ? (
          <>
            <CmdChip cmd={x.allowed[0]} muted={off} />
            {x.allowed.length > 1 && (
              <Tip content={x.allowed.slice(1).join('\n')}>
                <span className="shrink-0 text-xs text-fg-3">{t.secret.moreCmds(x.allowed.length - 1)}</span>
              </Tip>
            )}
          </>
        ) : (
          <AnyCmd />
        )}
      </div>
      <div className="min-w-0 max-lg:hidden">
        <LastUse x={x} now={now} />
      </div>
      <div className="flex justify-end max-lg:col-span-2">
        <RowActions x={x} onDialog={onDialog} />
      </div>
      {pend && <PendingCover k={`secret:${x.id}`} />}
    </div>
  )
}

export function SecretsPage() {
  const list = useSecrets()
  const projects = useProjects()
  const now = useNow()
  const [params] = useSearchParams()
  const open = useOpenSecret()
  const [status, setStatus] = useState('all')
  const [project, setProject] = useState('')
  const [q, setQ] = useState('')
  const [dialog, setDialog] = useState<Dialog>(null)
  const all = list.data ?? []
  const counts = statusCounts(all)
  const shown = useMemo(() => filterSecrets(all, status, project, q), [all, status, project, q])
  const projName = (projects.data?.projects ?? []).find((p) => p.id === project)?.name
  const usedProjects = (projects.data?.projects ?? []).filter((p) => all.some((x) => x.projects.includes(p.id)))
  const openID = params.get('k')
  return (
    <>
      <div className="mb-5 flex flex-wrap items-center justify-between gap-3 lg:mb-6">
        <h1 className="font-display text-[24px] font-semibold tracking-wide text-fg lg:text-[28px]">{t.secret.title}</h1>
        <div className="flex flex-wrap items-center gap-2">
          <label className="relative">
            <Search className="pointer-events-none absolute top-1/2 left-3 size-4 -translate-y-1/2 text-fg-3" />
            <Input value={q} onChange={(e) => setQ(e.target.value)} placeholder={t.secret.search} aria-label={t.secret.search} className="w-56 pl-9" />
          </label>
          <Menu
            trigger={
              <Button icon={<ChevronDown className="size-4" />} className="flex-row-reverse">
                {projName ?? t.secret.allProjects}
              </Button>
            }
            items={[{ label: t.secret.allProjects, checked: !project, onSelect: () => setProject('') }, ...usedProjects.map((p) => ({ label: p.name, checked: project === p.id, onSelect: () => setProject(p.id) }))]}
          />
          <Button variant="primary" icon={<Plus className="size-4" />} onClick={() => setDialog({ kind: 'create' })}>
            {t.secret.create}
          </Button>
        </div>
      </div>
      <div className="mb-4">
        <Segmented value={status} onChange={setStatus} options={(['all', 'active', 'pending', 'expired', 'revoked'] as const).map((s) => ({ value: s, label: s === 'all' ? t.secret.all : t.secret.status[s], count: counts[s] }))} />
      </div>
      {list.isPending ? (
        <div className="grid gap-2">
          <Skeleton className="h-16 rounded-2xl" />
          <Skeleton className="h-16 rounded-2xl" />
        </div>
      ) : !all.length ? (
        <Card>
          <Empty icon={<KeyRound />} title={t.secret.empty}>
            {t.secret.emptyHint}
          </Empty>
        </Card>
      ) : (
        <div className="overflow-hidden rounded-2xl border border-line bg-surface shadow-card">
          <div className={cx('grid gap-x-4 border-b border-line bg-sunken/50 px-4 py-2 text-xs font-medium text-fg-3 max-lg:hidden', gridCols)}>
            <span>{t.secret.cols.name}</span>
            <span>{t.secret.cols.value}</span>
            <span>{t.secret.cols.project}</span>
            <span>{t.secret.cols.auth}</span>
            <span>{t.secret.cols.allowed}</span>
            <span>{t.secret.cols.last}</span>
            <span className="sr-only">{t.secret.cols.actions}</span>
          </div>
          {shown.length ? shown.map((x) => <SecretRow key={x.id} x={x} now={now} onOpen={() => open(x.id)} onDialog={setDialog} />) : <div className="px-4 py-10 text-center text-sm text-fg-3">{t.secret.noMatch}</div>}
        </div>
      )}
      <SecretDetail id={openID} onClose={() => open(null)} onDialog={setDialog} />
      {dialog?.kind === 'replace' && <ReplaceDialog x={dialog.x} onClose={() => setDialog(null)} />}
      {(dialog?.kind === 'edit' || dialog?.kind === 'regrant') && <AuthDialog x={dialog.x} regrant={dialog.kind === 'regrant'} onClose={() => setDialog(null)} />}
      {dialog?.kind === 'create' && <CreateDialog onClose={() => setDialog(null)} />}
    </>
  )
}

// ---------- 详情抽屉 ----------

function dayLabel(iso: string, now: number) {
  const d = new Date(iso)
  const today = new Date(now)
  const y = new Date(now - 86_400_000)
  const same = (a: Date, b: Date) => a.getFullYear() === b.getFullYear() && a.getMonth() === b.getMonth() && a.getDate() === b.getDate()
  return same(d, today) ? t.secret.today : same(d, y) ? t.secret.yesterday : dayText(iso)
}

// changeText：授权变更在时间线上的一句话。
function changeText(l: SecretLog, x: SecretView) {
  const ttl = t.secret.ttlText(l.days)
  const who = l.session_title || ''
  switch (l.kind) {
    case 'request':
      return t.secret.log.request(who || l.session_key || '')
    case 'grant':
      return t.secret.log.grant(x.name, ttl, who)
    case 'create':
      return t.secret.log.create(x.name, ttl)
    case 'edit':
      return t.secret.log.edit(ttl)
    case 'regrant':
      return t.secret.log.regrant(ttl)
    case 'replace':
      return t.secret.log.replace
    case 'revoke':
      return t.secret.log.revoke
    case 'reject':
      return t.secret.log.reject(who)
    default:
      return t.secret.log.expire
  }
}

function Result({ l }: { l: SecretLog }) {
  const r = l.result ?? ''
  if (r === 'ok')
    return (
      <span className="flex items-center gap-1 text-[13px] text-run">
        <Check className="size-3.5" />
        {t.secret.result.ok}
      </span>
    )
  if (r === 'failed')
    return (
      <span className="text-[13px]">
        <span className="text-danger">{t.secret.result.failed}</span>
        <span className="text-fg-3">
          {t.common.sep}
          {t.secret.exitCode(l.exit_code)}
        </span>
      </span>
    )
  if (r === 'denied')
    return (
      <span className="flex min-w-0 items-center gap-1 text-[13px]">
        <Ban className="size-3.5 shrink-0 text-wait" />
        <span className="shrink-0 text-wait">{t.secret.result.denied}</span>
        {l.reason && (
          <span className="truncate text-fg-3">
            {t.common.sep}
            {l.reason}
          </span>
        )}
      </span>
    )
  return <span className="text-[13px] text-fg-3">{t.secret.result.running}</span>
}

const recCols = 'grid-cols-[3rem_minmax(0,10rem)_4.75rem_minmax(0,1fr)_minmax(0,11rem)]'

function Records({ logs, x, now }: { logs: SecretLog[]; x: SecretView; now: number }) {
  const [only, setOnly] = useState('all')
  const denied = logs.filter((l) => l.result === 'denied')
  const shown = only === 'denied' ? denied : logs
  const uses = logs.filter((l) => l.kind === 'use' || l.kind === 'write')
  return (
    <section>
      <div className="mb-2 flex items-center gap-2">
        <h4 className="font-display text-[17px] font-semibold text-fg">{t.secret.records}</h4>
        <span className="text-sm text-fg-3 tabular">{t.secret.recordsN(uses.length)}</span>
        <span className="ml-auto">
          <Segmented size="sm" value={only} onChange={setOnly} options={[{ value: 'all', label: t.secret.all }, { value: 'denied', label: t.secret.rejectedTab, count: denied.length }]} />
        </span>
      </div>
      {!shown.length ? (
        <p className="py-8 text-center text-sm text-fg-3">{t.secret.noRecords}</p>
      ) : (
        <div className="overflow-x-auto">
          <div className="min-w-[36rem]">
            <div className={cx('grid gap-x-3 px-2 py-2 text-xs font-medium text-fg-3', recCols)}>
              <span>{t.secret.col.time}</span>
              <span>{t.secret.col.session}</span>
              <span>{t.secret.col.req}</span>
              <span>{t.secret.col.cmd}</span>
              <span>{t.secret.col.result}</span>
            </div>
            {groupByDay(shown).map((g) => (
              <div key={g.day}>
                <div className="px-2 pt-3 pb-1 text-xs text-fg-3">{dayLabel(g.at, now)}</div>
                {g.items.map((l) =>
                  l.kind === 'use' || l.kind === 'write' ? (
                    <div key={l.id} data-secret-log={l.result} className={cx('grid items-center gap-x-3 border-t border-line px-2 py-2.5', recCols)}>
                      <span className="text-[13px] text-fg tabular">{hhmm(l.at)}</span>
                      <Who role={l.session_role} title={l.session_title} fallback={l.session_key} />
                      <span>{l.requirement_code ? <RefChip code={l.requirement_code} /> : <span className="text-fg-3">—</span>}</span>
                      <Tip content={l.kind === 'write' ? t.secret.writeTo(l.path ?? '') : (l.command ?? '')} truncated>
                        <span className="flex min-w-0">{l.kind === 'write' ? <span className="truncate font-mono text-[12.5px] text-fg-2">{t.secret.writeTo(l.path ?? '')}</span> : <Cmd cmd={l.command ?? ''} />}</span>
                      </Tip>
                      <Result l={l} />
                    </div>
                  ) : (
                    <div key={l.id} data-secret-change={l.kind} className="grid grid-cols-[3rem_minmax(0,1fr)] gap-x-3 border-t border-line px-2 py-2.5 text-[13px] text-fg-2">
                      <span className="tabular">{hhmm(l.at)}</span>
                      <span className="min-w-0">
                        {changeText(l, x)}
                        {l.reason && (
                          <span className="text-fg-3">
                            {t.common.sep}
                            {l.reason}
                          </span>
                        )}
                      </span>
                    </div>
                  ),
                )}
              </div>
            ))}
          </div>
        </div>
      )}
    </section>
  )
}

function SecretDetail({ id, onClose, onDialog }: { id: string | null; onClose: () => void; onDialog: (d: Dialog) => void }) {
  const q = useSecret(id ?? undefined)
  const now = useNow()
  const revoke = useRevoke()
  const openAsk = useOpenAsk()
  const x = q.data?.secret
  const left = x ? authLine(x, now) : ''
  return (
    <Panel open={!!id} onClose={onClose} wide="approval" bare title={x?.name ?? t.secret.title}>
      {!x ? (
        <div className="space-y-3 px-6 py-6">
          <Skeleton className="h-7 w-1/2" />
          <Skeleton className="h-24" />
        </div>
      ) : (
        <div className="flex min-h-0 flex-1 flex-col">
          <header className="shrink-0 border-b border-line px-5 pt-4 pb-4 sm:px-6">
            <div className="flex items-center gap-2">
              <SecretStatusPill status={x.status} />
              <span className="text-[13px] text-fg-3 tabular">{left}</span>
              <button aria-label={t.common.close} onClick={onClose} className="ml-auto flex size-8 items-center justify-center rounded-lg text-fg-2 transition hover:bg-hover">
                <X className="size-[18px]" />
              </button>
            </div>
            <div className="mt-2 flex flex-wrap items-center gap-2">
              <h3 className="min-w-0 flex-1 font-display text-[22px] leading-snug font-semibold text-fg">{x.name}</h3>
              {x.status === 'pending' ? (
                <Button size="sm" variant="primary" disabled={!x.request_pending} onClick={() => x.request_code && openAsk(`d:${x.request_code}`)}>
                  {t.secret.fill}
                </Button>
              ) : (
                <>
                  <Button size="sm" onClick={() => onDialog({ kind: 'replace', x })}>
                    {t.secret.replace}
                  </Button>
                  <Button size="sm" onClick={() => onDialog({ kind: x.status === 'active' ? 'edit' : 'regrant', x })}>
                    {x.status === 'active' ? t.secret.edit : t.secret.regrant}
                  </Button>
                  {x.status === 'active' && (
                    <Button size="sm" variant="danger" onClick={() => revoke(x)}>
                      {t.secret.revoke}
                    </Button>
                  )}
                </>
              )}
            </div>
            {x.purpose && <p className="mt-1 text-[13.5px] text-fg-2">{x.purpose}</p>}
          </header>
          <div className="relative min-h-0 flex-1 space-y-6 overflow-y-auto overscroll-contain px-5 py-5 sm:px-6">
            <div className="text-[13.5px]">
              <Row label={t.secret.fieldsLabel}>
                <div className="flex flex-wrap gap-x-5 gap-y-1 pt-1">
                  {x.fields.map((f) => (
                    <span key={f.env} className="flex items-center gap-2 font-mono text-[12.5px] text-fg">
                      {f.env}
                      {f.filled ? <Dots /> : <span className="font-sans text-fg-3">{t.secret.unfilled}</span>}
                    </span>
                  ))}
                </div>
              </Row>
              <Row label={t.secret.authLabel}>
                <span className="block pt-1 text-fg">{t.secret.authLine(x.project_names.join('、'), x.expires_at ? `${t.secret.until(dayTime(x.expires_at))}` : t.secret.forever)}</span>
              </Row>
              <Row label={t.secret.allowed}>
                <div className="flex flex-wrap gap-1.5 pt-0.5">{x.allowed.length ? x.allowed.map((c) => <CmdChip key={c} cmd={c} />) : <AnyCmd />}</div>
              </Row>
              <Row label={t.secret.disk} last>
                <span className="block pt-1 text-fg">{x.disk ? t.secret.diskOn : t.secret.diskOff}</span>
              </Row>
            </div>
            <Guard />
            <Records logs={q.data?.logs ?? []} x={x} now={now} />
            {x.status === 'active' && <PendingCover k={`secret:${x.id}`} />}
          </div>
        </div>
      )}
    </Panel>
  )
}

// ---------- 对话框 ----------

function ReplaceDialog({ x, onClose }: { x: SecretView; onClose: () => void }) {
  const ops = useSecretApi()
  const [values, setValues] = useState<Record<string, string>>({})
  const [busy, setBusy] = useState(false)
  const submit = async () => {
    if (!Object.keys(filledValues(values)).length) return
    setBusy(true)
    const r = await ops.replace(x, values)
    setBusy(false)
    if (r) {
      toast.success(t.secret.replaced)
      onClose()
    }
  }
  return (
    <Modal
      open
      wide
      onClose={onClose}
      title={`${t.secret.replaceTitle}${t.common.sep}${x.name}`}
      description={t.secret.replaceDesc}
      footer={
        <>
          <Button variant="ghost" onClick={onClose}>
            {t.common.cancel}
          </Button>
          <Button variant="primary" loading={busy} disabled={!Object.keys(filledValues(values)).length} onClick={() => void submit()}>
            {t.common.save}
          </Button>
        </>
      }
    >
      <FieldValues fields={x.fields} values={values} onChange={setValues} autoFocus />
    </Modal>
  )
}

function AuthDialog({ x, regrant, onClose }: { x: SecretView; regrant: boolean; onClose: () => void }) {
  const ops = useSecretApi()
  const left = timeLeft(x.expires_at)
  const [auth, setAuth] = useState<AuthState>({ projects: x.projects, days: !x.expires_at ? 0 : left && 'days' in left && [1, 7, 30].includes(left.days + 1) ? left.days + 1 : DEFAULT_TTL, allowed: x.allowed, disk: x.disk })
  const [busy, setBusy] = useState(false)
  const submit = async () => {
    if (!checkAuth(x.fields, {}, auth)) return
    setBusy(true)
    const r = await ops.authorize(x, auth, {})
    setBusy(false)
    if (r) {
      toast.success(t.secret.updated)
      onClose()
    }
  }
  return (
    <Modal
      open
      wide
      onClose={onClose}
      title={`${regrant ? t.secret.regrantTitle : t.secret.editTitle}${t.common.sep}${x.name}`}
      description={t.secret.regrantDesc}
      footer={
        <>
          <Button variant="ghost" onClick={onClose}>
            {t.common.cancel}
          </Button>
          <Button variant="primary" loading={busy} onClick={() => void submit()}>
            {t.common.save}
          </Button>
        </>
      }
    >
      <AuthFields value={auth} onChange={setAuth} />
    </Modal>
  )
}

type NewField = { label: string; env: string; value: string; envTouched?: boolean }

function CreateDialog({ onClose }: { onClose: () => void }) {
  const ops = useSecretApi()
  const projects = useProjects()
  const first = (projects.data?.projects ?? []).find((p) => !p.hidden)?.id
  const [name, setName] = useState('')
  const [purpose, setPurpose] = useState('')
  const [fields, setFields] = useState<NewField[]>([{ label: '', env: '', value: '' }])
  const [auth, setAuth] = useState<AuthState>({ projects: first ? [first] : [], days: DEFAULT_TTL, allowed: [], disk: false })
  const [busy, setBusy] = useState(false)
  useEffect(() => {
    if (!auth.projects.length && first) setAuth((a) => ({ ...a, projects: [first] }))
  }, [first]) // eslint-disable-line react-hooks/exhaustive-deps
  const setField = (i: number, p: Partial<NewField>) =>
    setFields((fs) =>
      fs.map((f, j) => {
        if (j !== i) return f
        const n = { ...f, ...p }
        if (p.label !== undefined && !f.envTouched) n.env = envFromLabel(p.label)
        return n
      }),
    )
  const submit = async () => {
    if (!name.trim()) return void toast.error(t.secret.needName)
    const fs = fields.filter((f) => f.env.trim() || f.value)
    if (!fs.length) return void toast.error(t.secret.needField)
    const bad = fs.find((f) => !envOK(f.env.trim()))
    if (bad) return void toast.error(t.secret.envBad(bad.env))
    const miss = fs.find((f) => !f.value.trim())
    if (miss) return void toast.error(t.secret.needValue(miss.label || miss.env))
    if (!auth.projects.length) return void toast.error(t.secret.needProject)
    setBusy(true)
    const r = await ops.create({ name: name.trim(), purpose: purpose.trim(), fields: fs.map((f) => ({ env: f.env.trim(), label: f.label.trim() || f.env.trim(), value: f.value })), ...auth })
    setBusy(false)
    if (r) {
      toast.success(t.secret.created(name.trim()))
      onClose()
    }
  }
  return (
    <Modal
      open
      wide
      onClose={onClose}
      title={t.secret.createTitle}
      footer={
        <>
          <Button variant="ghost" onClick={onClose}>
            {t.common.cancel}
          </Button>
          <Button variant="primary" loading={busy} onClick={() => void submit()}>
            {t.secret.save}
          </Button>
        </>
      }
    >
      <div className="grid gap-3">
        <Input value={name} onChange={(e) => setName(e.target.value)} placeholder={t.secret.namePh} aria-label={t.secret.name} autoFocus />
        <Textarea value={purpose} onChange={(e) => setPurpose(e.target.value)} placeholder={t.secret.purposePh} aria-label={t.secret.purpose} minRows={2} />
        {fields.map((f, i) => (
          <div key={i} className="grid gap-2 rounded-xl border border-line px-3 py-3">
            <div className="grid grid-cols-2 gap-2">
              <Input value={f.label} onChange={(e) => setField(i, { label: e.target.value })} placeholder={t.secret.fieldLabel} aria-label={t.secret.fieldLabel} />
              <Input value={f.env} onChange={(e) => setField(i, { env: e.target.value, envTouched: true })} placeholder={t.secret.fieldEnv} aria-label={t.secret.fieldEnv} spellCheck={false} autoCapitalize="off" className="font-mono" />
            </div>
            <div className="flex items-center gap-2">
              <div className="flex-1">
                <ValueInput label={t.secret.fieldValue} value={f.value} onChange={(v) => setField(i, { value: v })} placeholder={t.secret.paste} />
              </div>
              {fields.length > 1 && (
                <Button size="icon-sm" variant="ghost" aria-label={t.secret.removeField} onClick={() => setFields((fs) => fs.filter((_, j) => j !== i))}>
                  <X className="size-4" />
                </Button>
              )}
            </div>
          </div>
        ))}
        <div>
          <Button size="sm" variant="ghost" icon={<Plus className="size-3.5" />} onClick={() => setFields((fs) => [...fs, { label: '', env: '', value: '' }])}>
            {t.secret.addField}
          </Button>
        </div>
        <AuthFields value={auth} onChange={setAuth} />
        <Guard />
      </div>
    </Modal>
  )
}
