import { useEffect, useMemo, useRef, useState, type ReactNode } from 'react'
import { useSearchParams } from 'react-router'
import { useQueryClient } from '@tanstack/react-query'
import { toast } from 'sonner'
import { Ban, Check, ChevronDown, Eye, EyeOff, KeyRound, Pencil, Plus, Search, ShieldCheck, X } from 'lucide-react'
import { api, errMsg, unwrap, type Decision } from '../api/client'
import { useProjects, useSecret, useSecrets, useSessions, type SecretLog, type SecretView } from '../api/queries'
import { t } from '../i18n'
import { cx, relTime, useNow } from '../lib/util'
import { addCommand, amendKind, secretRun, authDiff, logNote, missingReplace, replaceFields, replaceLabels, changedPlain, diffEmpty, isAmend, isSecretReq, DEFAULT_TTL, KEEP_TTL, expiryOf, filledValues, filterSecrets, groupByDay, missingValues, envFromLabel, envOK, plainMap, shownValues, splitRedacted, statusCounts, timeLeft, TTL_DAYS, unionCommands, valueHint } from '../lib/secrets'
import { Button, Card, Empty, Input, Segmented, Skeleton, Switch, Textarea, Tip } from '../ui/base'
import { Menu, Modal, Panel } from '../ui/overlay'
import { Md } from '../ui/markdown'
import { RefChip } from '../ui/refmark'
import { displayState, RoleBadge, RoleTag, StatusPill as SessionPill } from '../ui/marks'
import { done, PendingCover, schedule, usePending } from './verdict'
import { decDraftKey, decKey } from './outbox'
import { DraftField, DraftHint, useDraft } from './draft'
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

const Dots = ({ className, short }: { className?: string; short?: boolean }) => (
  <span aria-label={t.secret.masked} className={cx('font-mono tracking-[0.2em] text-fg-2 select-none', className)}>
    {short ? '••••' : '••••••••'}
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

// ValueInput：密码框，右侧眼睛切换显示；不让浏览器记住、不拼写检查。不敏感的值（R-318）直接明文、没有眼睛。
function ValueInput({ value, onChange, placeholder, label, autoFocus, plain }: { value: string; onChange: (v: string) => void; placeholder: string; label: string; autoFocus?: boolean; plain?: boolean }) {
  const [show, setShow] = useState(false)
  return (
    <div className="relative">
      <Input
        type={show || plain ? 'text' : 'password'}
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
        className={cx('font-mono', plain ? 'pr-3' : 'pr-10')}
      />
      {!plain && (
        <button type="button" aria-label={show ? t.secret.hide : t.secret.show} onClick={() => setShow(!show)} className="absolute top-1/2 right-2 -translate-y-1/2 rounded-md p-1 text-fg-3 transition hover:bg-hover hover:text-fg">
          {show ? <EyeOff className="size-4" /> : <Eye className="size-4" />}
        </button>
      )}
    </div>
  )
}

// SensitiveSwitch：值的「敏感」开关（R-318），默认开；悬停看说明。
function SensitiveSwitch({ env, plain, onChange, disabled }: { env: string; plain: boolean; onChange: (plain: boolean) => void; disabled?: boolean }) {
  return (
    <label data-sensitive={env} className="ml-auto flex shrink-0 cursor-pointer items-center gap-2 self-center text-[12.5px] text-fg-2">
      <Tip content={t.secret.sensitiveTip}>
        <span>{t.secret.sensitive}</span>
      </Tip>
      <Switch checked={!plain} onChange={(v) => onChange(!v)} disabled={disabled} />
    </label>
  )
}

// ValueHint：敏感值太短（不会隐去）或是常见词（各处同样的文字都会隐去）时的提示（R-318 / R-298 第 3 点）。
function ValueHint({ value, plain }: { value: string; plain?: boolean }) {
  const h = valueHint(value, plain)
  if (!h) return null
  return (
    <p data-value-hint={h} className="mt-1.5 text-[12.5px] leading-relaxed text-wait">
      {h === 'short' ? t.secret.hintShort : t.secret.hintCommon}
    </p>
  )
}

type Field = { env: string; label: string; how?: string; filled?: boolean; plain?: boolean; value?: string }

// FieldValues：每个值一张卡：显示名 + 环境变量名、「敏感」开关、获取方式、输入框。已填过的空着表示保留原值；
// 不敏感的值明文输入，已填的在占位里写出当前值。
// fresh：替换值（R-325）——已填过的也要填新值，占位写「粘贴新值」。
function FieldValues({ fields, values, onChange, plain, onPlain, autoFocus, fresh }: { fields: Field[]; values: Record<string, string>; onChange: (v: Record<string, string>) => void; plain: Record<string, boolean>; onPlain: (v: Record<string, boolean>) => void; autoFocus?: boolean; fresh?: boolean }) {
  return (
    <div className="grid gap-3">
      {fields.map((f, i) => {
        const p = !!plain[f.env]
        const v = values[f.env] ?? ''
        return (
          <div key={f.env} data-secret-field={f.env} className="rounded-xl border border-line bg-surface px-4 py-3">
            <div className="flex min-w-0 items-baseline gap-2">
              <b className="shrink-0 text-[14px] font-semibold text-fg">{f.label}</b>
              <span className="min-w-0 truncate font-mono text-[12px] text-fg-3">{f.env}</span>
              <SensitiveSwitch env={f.env} plain={p} onChange={(x) => onPlain({ ...plain, [f.env]: x })} />
            </div>
            {f.how && <Md text={f.how} className="mt-1 text-[13px] leading-relaxed text-fg-2" />}
            <div className="mt-2">
              <ValueInput label={f.label} plain={p} value={v} onChange={(x) => onChange({ ...values, [f.env]: x })} placeholder={fresh ? t.secret.pasteNew : !f.filled ? t.secret.paste : p && f.value ? t.secret.keepValue(f.value) : t.secret.keep} autoFocus={autoFocus && i === 0} />
              <ValueHint value={v} plain={p} />
            </div>
          </div>
        )
      })}
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
// keep：可以选「不变」（保留现在的到期时间，R-320 修改授权申请里、凭据生效中时）；at 是现在的到期时间，没有 = 长期。
function AuthFields({ value, onChange, suggest, keep }: { value: AuthState; onChange: (v: AuthState) => void; suggest?: number; keep?: { at?: string | null } }) {
  const projects = useProjects()
  const [cmd, setCmd] = useState('')
  const list = (projects.data?.projects ?? []).filter((p) => !p.hidden || value.projects.includes(p.id))
  const name = (id: string) => list.find((p) => p.id === id)?.name ?? id
  const set = (p: Partial<AuthState>) => onChange({ ...value, ...p })
  const kept = value.days === KEEP_TTL
  const at = kept ? (keep?.at ? new Date(keep.at) : undefined) : expiryOf(value.days)
  const ttls = [...(keep ? [{ value: String(KEEP_TTL), label: t.secret.keepTtl }] : []), ...TTL_DAYS.map((d) => ({ value: String(d), label: t.secret.ttl(d) }))]
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
          <Segmented size="sm" value={String(value.days)} onChange={(v) => set({ days: Number(v) })} options={ttls} />
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
    authorize: (x: { id: string; name: string }, a: AuthState, values: Record<string, string>, plain?: Record<string, boolean>, note?: string, name?: string) =>
      call(() => unwrap(api.POST('/api/secrets/{ref}/authorize', { params: { path: { ref: x.id } }, body: { ...a, values: filledValues(values), plain, note: note?.trim() || undefined, name: name?.trim() || undefined } })), t.secret.fail.save(x.name), ['decisions', 'sessions']),
    rename: (x: { id: string; name: string }, name: string) => call(() => unwrap(api.POST('/api/secrets/{ref}/rename', { params: { path: { ref: x.id } }, body: { name } })), t.secret.fail.rename(x.name)),
    replace: (x: { id: string; name: string }, values: Record<string, string>, plain: Record<string, boolean>) =>
      call(() => unwrap(api.POST('/api/secrets/{ref}/values', { params: { path: { ref: x.id } }, body: { values: filledValues(values), plain } })), t.secret.fail.replace(x.name)),
    mark: (x: { id: string; name: string }, plain: Record<string, boolean>) => call(() => unwrap(api.POST('/api/secrets/{ref}/plain', { params: { path: { ref: x.id } }, body: { plain } })), t.secret.fail.mark(x.name)),
    revoke: (x: { id: string; name: string }) => call(() => unwrap(api.POST('/api/secrets/{ref}/revoke', { params: { path: { ref: x.id } } })), t.secret.fail.revoke(x.name)),
    reject: (x: { id: string }, code: string, note: string) =>
      call(() => unwrap(api.POST('/api/secrets/{ref}/reject', { params: { path: { ref: x.id } }, body: { note } })), t.secret.fail.reject(code), ['decisions', 'sessions']),
    create: (body: { name: string; purpose: string; fields: { env: string; label: string; value: string; plain: boolean }[] } & AuthState) =>
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
      {x?.ask && <span className="shrink-0 text-fg-3">{isAmend(x) ? { replace: t.secret.replaceShort, rename: t.secret.renameShort, amend: t.secret.amendShort }[amendKind(x.ask)] : t.secret.suggestShort(x.ask.days)}</span>}
    </span>
  )
}

// initAuth：申请抽屉的初始授权——待填写的用申请里的建议；已有授权的保留原来的项目和命令，再并上这次要的。
function initAuth(x: SecretView): AuthState {
  const ask = x.ask
  // 修改授权申请（R-320）：预填会话要的改后授权；到期时间不变只在生效中时可选
  if (ask && isAmend(x))
    return { projects: ask.projects?.length ? [...ask.projects] : [...x.projects], days: ask.days === KEEP_TTL && x.status !== 'active' ? DEFAULT_TTL : ask.days, allowed: [...(ask.allowed ?? [])], disk: !!ask.disk }
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
  const sessions = useSessions()
  const openSession = useOpenSession()
  const now = useNow()
  const [values, setValues] = useState<Record<string, string>>({})
  const [plain, setPlain] = useState<Record<string, boolean>>(() => plainMap(x.fields))
  const [auth, setAuth] = useState<AuthState>(() => initAuth(x))
  const s = (sessions.data?.sessions ?? []).find((v) => v.key === d.session_key)
  const who = s ? sessionTitle(s) : ''
  const key = decKey(d.code)
  const pend = usePending(key)
  const amend = isAmend(x) // 修改授权申请（R-320）：显示改前 / 改后
  // 替换值（R-325）：申请里要替换的值显示填写框和获取方式，都填了新值才能保存并授权
  const swap = amend ? replaceFields(x.fields, x.ask?.replace) : []
  // 改名（R-326）：申请里提出了新名称时可再调整
  const [name, setName] = useState(amend ? (x.ask?.name ?? '') : '')
  const renaming = amend && !!x.ask?.name
  // 附言（R-321）：保存并授权、拒绝都带上，随结果送达申请方；草稿和决策答复同一个键（答复后自动清）。值不进草稿。
  const dr = useDraft<{ text: string }>(decDraftKey(d.code), { text: '' })
  const note = dr.value.text
  const [need, setNeed] = useState(false)
  const noteRef = useRef<HTMLTextAreaElement>(null)
  const save = () => {
    if (pend || !checkAuth(x.fields, values, auth)) return
    const miss = missingReplace(swap, values)
    if (miss.length) {
      toast.error(t.secret.needNewValue(miss[0].label))
      return
    }
    const v = values
    const a = auth
    const p = amend ? (swap.length ? changedPlain(swap, plain) : undefined) : plain
    const n = note
    const nm = renaming ? name : undefined
    schedule(key, { act: 'pass', scene: 'request', subject: d.code, commit: async () => done(ops.authorize(x, a, v, p, n, nm), dr.drop) })
  }
  // 拒绝和其他审批一样要写明理由：没写就把焦点放到附言框
  const reject = () => {
    if (pend) return
    if (!note.trim()) {
      setNeed(true)
      noteRef.current?.focus()
      return
    }
    const n = note
    schedule(key, { act: 'back', scene: 'request', subject: d.code, commit: async () => done(ops.reject(x, d.code, n), dr.drop) })
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
          <div className="flex items-center gap-2 text-[12.5px] text-fg-3">
            {d.project_name && <span>{d.project_name}</span>}
            <CodeChip code={x.code} />
          </div>
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
        <Section title={amend ? t.secret.reason : t.secret.purpose}>
          {d.context && <Md text={d.context} className="text-[14px] leading-relaxed text-fg" />}
          {d.requirement_code && (
            <div className="mt-2">
              <RefChip code={d.requirement_code} />
            </div>
          )}
        </Section>
        {swap.length > 0 && (
          <Section title={t.secret.newValues}>
            <FieldValues fields={swap} values={values} onChange={setValues} plain={plain} onPlain={setPlain} fresh autoFocus />
          </Section>
        )}
        {renaming && (
          <Section title={t.secret.name}>
            <Input data-secret-rename="" value={name} aria-label={t.secret.name} onChange={(e) => setName(e.target.value)} />
            <p className="mt-1.5 text-xs leading-relaxed text-fg-3">{t.secret.renameHint}</p>
          </Section>
        )}
        {amend ? (
          <Section title={t.secret.change}>
            <AuthDiffView x={x} auth={auth} name={renaming ? name : undefined} />
          </Section>
        ) : (
          <Section title={t.secret.values}>
            <FieldValues fields={x.fields} values={values} onChange={setValues} plain={plain} onPlain={setPlain} autoFocus />
          </Section>
        )}
        <Section title={t.secret.auth}>
          <AuthFields value={auth} onChange={setAuth} suggest={x.ask?.days} keep={amend && x.status === 'active' ? { at: x.expires_at } : undefined} />
        </Section>
        <Guard />
        {amend && <p className="-mt-4 pl-6 text-[13px] leading-relaxed text-fg-3">{swap.length ? t.secret.replaceGuard : t.secret.amendGuard}</p>}
      </div>
      <footer className="relative shrink-0 space-y-2.5 border-t border-line bg-surface px-4 pt-3 pb-[max(0.875rem,env(safe-area-inset-bottom))] sm:px-6">
        <DraftField h={dr}>
          {(pad) => (
            <Textarea
              ref={noteRef}
              data-secret-note=""
              minRows={1}
              value={note}
              aria-invalid={need || undefined}
              placeholder={need ? t.verdict.needNote('request') : t.secret.notePh}
              onChange={(e) => {
                dr.set({ text: e.target.value })
                if (e.target.value.trim()) setNeed(false)
              }}
              className={cx(pad, need && 'border-danger/60 ring-2 ring-danger/20 placeholder:text-danger/80')}
            />
          )}
        </DraftField>
        <div className="flex items-center gap-3">
          <Button onClick={reject} disabled={!!pend}>
            {t.secret.reject}
          </Button>
          <span className="ml-auto min-w-0 truncate text-[12.5px] text-fg-3 max-sm:hidden">
            <DraftHint h={dr}>{who ? t.secret.deliverHint(who) : t.secret.deliverAny}</DraftHint>
          </span>
          <Button variant="primary" onClick={save} disabled={!!pend} className="max-sm:ml-auto">
            {t.secret.save}
            <span className="ml-1 text-[11px] opacity-70">⌘↵</span>
          </Button>
        </div>
        <PendingCover k={key} />
      </footer>
    </div>
  )
}

// AuthDiffView：修改授权申请的改前 / 改后（R-320）。改前是凭据现在的授权，改后是抽屉里的授权（随你的调整更新）；只列改了的项。
function AuthDiffView({ x, auth, name: newName }: { x: SecretView; auth: AuthState; name?: string }) {
  const projects = useProjects()
  const name = (id: string) => (projects.data?.projects ?? []).find((p) => p.id === id)?.name ?? x.project_names[x.projects.indexOf(id)] ?? id
  const diff = authDiff(x, auth)
  const renamed = newName !== undefined && newName.trim() !== '' && newName.trim() !== x.name
  if (diffEmpty(diff) && !renamed) return <p className="text-[13px] text-fg-3">{t.secret.noChange}</p>
  const at = auth.days === KEEP_TTL ? x.expires_at : expiryOf(auth.days)?.toISOString()
  const expBefore = x.status === 'revoked' ? t.secret.status.revoked : x.status === 'expired' ? t.secret.expiredAt(dayText(x.expires_at)) : x.expires_at ? t.secret.until(dayTime(x.expires_at)) : t.secret.forever
  const expAfter = at ? t.secret.until(dayTime(at)) : t.secret.forever
  const Names = ({ ids, mark, tone }: { ids: string[]; mark: string[]; tone: 'add' | 'del' }) => (
    <span className="flex flex-wrap gap-1.5">
      {ids.map((id) => (
        <span key={id} className={cx('rounded-md px-1.5 text-[13px] leading-6', mark.includes(id) ? (tone === 'add' ? 'bg-run-soft text-run' : 'bg-idle-soft text-fg-3 line-through') : 'text-fg')}>
          {name(id)}
        </span>
      ))}
    </span>
  )
  // 改前 / 改后的命令完整显示（换行，不截断）：去掉的划线，加上的高亮
  const Cmds = ({ list, mark, tone }: { list: string[]; mark: string[]; tone: 'add' | 'del' }) =>
    list.length ? (
      <span className="flex min-w-0 flex-col items-start gap-1.5">
        {list.map((c) => {
          const hit = mark.includes(c)
          return (
            <span key={c} data-secret-diff={hit ? tone : undefined} className={cx('max-w-full rounded-md px-2 py-0.5 font-mono text-[12px] leading-5 break-all', !hit ? 'bg-sunken text-fg-2' : tone === 'add' ? 'bg-run-soft text-run' : 'bg-idle-soft text-fg-3 line-through')}>
              {c}
            </span>
          )
        })}
      </span>
    ) : (
      <AnyCmd />
    )
  const rows: { label: string; before: ReactNode; after: ReactNode }[] = []
  if (renamed) rows.push({ label: t.secret.name, before: <span data-secret-diff="del" className="text-[13px] text-fg-3 line-through">{x.name}</span>, after: <span data-secret-diff="add" className="rounded-md bg-run-soft px-1.5 text-[13px] leading-6 text-run">{newName!.trim()}</span> })
  if (diff.projects) rows.push({ label: t.secret.project, before: <Names ids={x.projects} mark={diff.projects.removed} tone="del" />, after: <Names ids={auth.projects} mark={diff.projects.added} tone="add" /> })
  if (diff.expiry) rows.push({ label: t.secret.expiry, before: <span className="text-[13px] text-fg-2 tabular">{expBefore}</span>, after: <span className="text-[13px] text-fg tabular">{expAfter}</span> })
  if (diff.allowed) rows.push({ label: t.secret.allowed, before: <Cmds list={x.allowed} mark={diff.allowed.removed} tone="del" />, after: <Cmds list={auth.allowed} mark={diff.allowed.added} tone="add" /> })
  if (diff.disk) rows.push({ label: t.secret.disk, before: <span className="text-[13px] text-fg-2">{x.disk ? t.secret.diskOn : t.secret.diskOff}</span>, after: <span className="text-[13px] text-fg">{auth.disk ? t.secret.diskOn : t.secret.diskOff}</span> })
  return (
    <div data-secret-amend="" className="rounded-xl border border-line">
      <div className="grid grid-cols-[5.5rem_minmax(0,1fr)_minmax(0,1fr)] gap-3 border-b border-line px-3 py-2 text-xs font-medium text-fg-3">
        <span />
        <span>{t.secret.before}</span>
        <span>{t.secret.after}</span>
      </div>
      {rows.map((r, i) => (
        <div key={r.label} className={cx('grid grid-cols-[5.5rem_minmax(0,1fr)_minmax(0,1fr)] items-start gap-3 px-3 py-2.5', i < rows.length - 1 && 'border-b border-line')}>
          <span className="pt-0.5 text-[13px] text-fg-2">{r.label}</span>
          <div className="min-w-0">{r.before}</div>
          <div className="min-w-0">{r.after}</div>
        </div>
      ))}
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

const gridCols = 'lg:grid-cols-[minmax(0,1.6fr)_9.5rem_minmax(0,0.9fr)_minmax(0,1fr)_minmax(0,1.3fr)_minmax(0,1.2fr)_12.5rem]'

// RowValues：「值」一栏——不敏感的值明文（R-318），还有敏感的值再画圆点。
function RowValues({ x }: { x: SecretView }) {
  const v = shownValues(x.fields)
  return (
    <div className="flex min-w-0 items-center gap-2 text-[13px]">
      {v.plain.length > 0 && (
        <Tip content={v.plain.join('、')} truncated>
          <span className="min-w-0 truncate font-mono text-[12.5px] text-fg">{v.plain.join('、')}</span>
        </Tip>
      )}
      {v.masked && (v.plain.length > 0 ? <Dots short className="shrink-0" /> : <Dots className="shrink-0" />)}
    </div>
  )
}

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
        <div className="flex min-w-0 items-center gap-1.5">
          <CodeChip code={x.code} />
          <span className={cx('min-w-0 truncate text-[15px] font-semibold', off ? 'text-fg-2' : 'text-fg')}>{x.name}</span>
        </div>
        <div className="truncate text-[13px] text-fg-3">{x.purpose}</div>
      </div>
      <div className="min-w-0 max-lg:hidden">
        {x.status === 'pending' ? <div className="text-[13px] text-fg-3">{t.secret.unfilled}</div> : <RowValues x={x} />}
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
  // 按修改授权申请改的（R-320）：改了什么（含有效期）跟在后面，这里不再写有效期
  const ttl = l.reason && (l.kind === 'edit' || l.kind === 'regrant') ? '' : t.secret.ttlText(l.days)
  const who = l.session_title || ''
  switch (l.kind) {
    case 'request':
      return t.secret.log.request(who || l.session_key || '')
    case 'amend':
      return t.secret.log.amend(who || l.session_key || '')
    case 'grant':
      return t.secret.log.grant(x.name, ttl, who)
    case 'create':
      return t.secret.log.create(x.name, ttl)
    case 'edit':
      return t.secret.log.edit(ttl, who || l.session_key || '')
    case 'regrant':
      return t.secret.log.regrant(ttl, who || l.session_key || '')
    case 'replace':
      return t.secret.log.replace(replaceLabels(l.reason), who || l.session_key || '')
    case 'revoke':
      return t.secret.log.revoke
    case 'reject':
      return t.secret.log.reject(who)
    case 'plain':
      return t.secret.log.plain(l.reason ?? '')
    case 'sensitive':
      return t.secret.log.sensitive(l.reason ?? '')
    case 'rename':
      return t.secret.log.rename(l.reason ?? '', who || l.session_key || '')
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
                        {/* 修改授权申请（R-320）：申请的理由、批准时改了什么 */}
                        {l.reason && ['amend', 'edit', 'regrant'].includes(l.kind) && <span className="text-fg-3">：{l.reason}</span>}
                        {/* 你保存授权 / 拒绝时写的附言（R-321） */}
                        {logNote(l) && (
                          <span data-secret-log-note="" className="mt-1 block text-fg">
                            {t.secret.noteLine(logNote(l))}
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
  const ops = useSecretApi()
  const [marking, setMarking] = useState(false)
  const x = q.data?.secret
  const left = x ? authLine(x, now) : ''
  const mark = async (env: string, p: boolean) => {
    if (!x) return
    setMarking(true)
    await ops.mark(x, { [env]: p })
    setMarking(false)
  }
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
              <CodeChip code={x.code} />
              <span className="text-[13px] text-fg-3 tabular">{left}</span>
              <button aria-label={t.common.close} onClick={onClose} className="ml-auto flex size-8 items-center justify-center rounded-lg text-fg-2 transition hover:bg-hover">
                <X className="size-[18px]" />
              </button>
            </div>
            <div className="mt-2 flex flex-wrap items-center gap-2">
              <NameTitle x={x} />
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
                <div className="grid gap-1.5 pt-0.5">
                  {x.fields.map((f) => (
                    <div key={f.env} data-secret-field={f.env} className="flex min-w-0 items-center gap-3">
                      <span className="w-[42%] min-w-0 shrink-0 truncate font-mono text-[12.5px] text-fg-2">{f.env}</span>
                      <span className="min-w-0 flex-1 truncate">
                        {!f.filled ? <span className="text-fg-3">{t.secret.unfilled}</span> : f.plain && f.value ? <span className="font-mono text-[12.5px] text-fg">{f.value}</span> : <Dots />}
                      </span>
                      <SensitiveSwitch env={f.env} plain={!!f.plain} disabled={marking} onChange={(p) => void mark(f.env, p)} />
                    </div>
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
  const [plain, setPlain] = useState<Record<string, boolean>>(() => plainMap(x.fields))
  const [busy, setBusy] = useState(false)
  const marks = changedPlain(x.fields, plain)
  const dirty = Object.keys(filledValues(values)).length > 0 || Object.keys(marks).length > 0
  const submit = async () => {
    if (!dirty) return
    setBusy(true)
    const r = await ops.replace(x, values, marks)
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
          <Button variant="primary" loading={busy} disabled={!dirty} onClick={() => void submit()}>
            {t.common.save}
          </Button>
        </>
      }
    >
      <FieldValues fields={x.fields} values={values} onChange={setValues} plain={plain} onPlain={setPlain} autoFocus />
    </Modal>
  )
}

function AuthDialog({ x, regrant, onClose }: { x: SecretView; regrant: boolean; onClose: () => void }) {
  const ops = useSecretApi()
  const left = timeLeft(x.expires_at)
  const [auth, setAuth] = useState<AuthState>({ projects: x.projects, days: !x.expires_at ? 0 : left && 'days' in left && [1, 7, 30].includes(left.days + 1) ? left.days + 1 : DEFAULT_TTL, allowed: x.allowed, disk: x.disk })
  const [busy, setBusy] = useState(false)
  // 附言（R-321）：手动修改 / 重新授权时没有申请方，只记在时间线上
  const dr = useDraft<{ text: string }>(`secretnote:${x.id}`, { text: '' })
  const submit = async () => {
    if (!checkAuth(x.fields, {}, auth)) return
    setBusy(true)
    const r = await ops.authorize(x, auth, {}, undefined, dr.value.text)
    setBusy(false)
    if (r) {
      dr.drop()
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
      <DraftField h={dr} className="mt-3">
        {(pad) => <Textarea data-secret-note="" minRows={2} value={dr.value.text} placeholder={t.secret.notePhManual} onChange={(e) => dr.set({ text: e.target.value })} className={pad} />}
      </DraftField>
    </Modal>
  )
}

type NewField = { label: string; env: string; value: string; plain?: boolean; envTouched?: boolean }

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
    const r = await ops.create({ name: name.trim(), purpose: purpose.trim(), fields: fs.map((f) => ({ env: f.env.trim(), label: f.label.trim() || f.env.trim(), value: f.value, plain: !!f.plain })), ...auth })
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
            <div className="flex items-center gap-3">
              <div className="flex-1">
                <ValueInput label={t.secret.fieldValue} plain={f.plain} value={f.value} onChange={(v) => setField(i, { value: v })} placeholder={t.secret.paste} />
              </div>
              <SensitiveSwitch env={f.env || String(i)} plain={!!f.plain} onChange={(p) => setField(i, { plain: p })} />
              {fields.length > 1 && (
                <Button size="icon-sm" variant="ghost" aria-label={t.secret.removeField} onClick={() => setFields((fs) => fs.filter((_, j) => j !== i))}>
                  <X className="size-4" />
                </Button>
              )}
            </div>
            <ValueHint value={f.value} plain={f.plain} />
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

// CodeChip：凭据编号 S-x 的印记（R-326），悬停说明会话按编号取用。凭据页里不可点（就在这条凭据上）。
function CodeChip({ code }: { code?: string }) {
  if (!code) return null
  return <RefChip code={code} link={false} hint={t.secret.codeTip(code, secretRun(code))} />
}

// NameTitle：详情抽屉的标题，旁边的笔可以直接改名（R-326）；回车保存、Esc 取消。名称只用来显示，编号不变。
function NameTitle({ x }: { x: SecretView }) {
  const ops = useSecretApi()
  const [edit, setEdit] = useState<string | null>(null)
  const save = async () => {
    const v = (edit ?? '').trim()
    if (!v || v === x.name) return setEdit(null)
    if (await ops.rename(x, v)) setEdit(null)
  }
  if (edit !== null)
    return (
      <span className="flex min-w-0 flex-1 items-center gap-2">
        <Input
          autoFocus
          data-secret-rename=""
          value={edit}
          aria-label={t.secret.name}
          onChange={(e) => setEdit(e.target.value)}
          onKeyDown={(e) => {
            if (e.nativeEvent.isComposing) return
            if (e.key === 'Enter') void save()
            if (e.key === 'Escape') (e.stopPropagation(), setEdit(null))
          }}
          className="h-9 text-[16px] font-semibold"
        />
        <Button size="sm" variant="primary" onClick={() => void save()}>
          {t.common.save}
        </Button>
      </span>
    )
  return (
    <span className="group flex min-w-0 flex-1 items-center gap-1">
      <h3 className="min-w-0 font-display text-[22px] leading-snug font-semibold text-fg">{x.name}</h3>
      <Tip content={t.secret.renameHint}>
        <button type="button" aria-label={t.secret.rename} onClick={() => setEdit(x.name)} className="shrink-0 rounded-md p-1 text-fg-3 transition hover:bg-hover hover:text-fg">
          <Pencil className="size-3.5" />
        </button>
      </Tip>
    </span>
  )
}
