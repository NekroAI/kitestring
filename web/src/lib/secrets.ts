// 凭据库（R-295）的纯逻辑：状态筛选、有效期、命令里的「已隐去」、取用记录按天分组、允许的命令编辑。
// 组件在 app/secrets.tsx；这里不依赖 React，node --test 直接测。

export type SecretStatus = 'active' | 'pending' | 'expired' | 'revoked'
export const SECRET_STATUSES: SecretStatus[] = ['active', 'pending', 'expired', 'revoked']

// isSecretReq：这条决策是凭据申请（request_kind = secret）：在审批抽屉里填值授权，不能左滑批准、不能直接点批准
export const isSecretReq = (d?: { request_kind?: string } | null) => d?.request_kind === 'secret'

// 有效期选项（天）：0 = 长期；默认 7 天（D-1438）
export const TTL_DAYS = [1, 7, 30, 0] as const
export const DEFAULT_TTL = 7

// 后端存记录前把已知值换成的文字（internal/hub/redact.go 的 SecretRedacted），网页画成「已隐去」小标
export const REDACTED = '[已隐去]'

type Listed = { name: string; purpose?: string; status: string; projects?: string[] | null; project_names?: string[] | null }

// filterSecrets：按状态（all = 全部）、项目、搜索词（名字、用途、项目名）筛选。
export function filterSecrets<T extends Listed>(list: T[], status: string, project: string, q: string): T[] {
  const words = q.trim().toLowerCase().split(/\s+/).filter(Boolean)
  return list.filter((x) => {
    if (status !== 'all' && x.status !== status) return false
    if (project && !(x.projects ?? []).includes(project)) return false
    const hay = [x.name, x.purpose ?? '', ...(x.project_names ?? [])].join(' ').toLowerCase()
    return words.every((w) => hay.includes(w))
  })
}

// statusCounts：每个状态的条数（状态筛选页签上的数字），含 all。
export function statusCounts(list: { status: string }[]): Record<string, number> {
  const out: Record<string, number> = { all: list.length }
  for (const s of SECRET_STATUSES) out[s] = 0
  for (const x of list) out[x.status] = (out[x.status] ?? 0) + 1
  return out
}

// expiryOf：从 now 起 days 天后的到期时刻；0 = 长期（undefined）。
export function expiryOf(days: number, now = Date.now()): Date | undefined {
  return days > 0 ? new Date(now + days * 86_400_000) : undefined
}

// timeLeft：离到期还剩多久，取整到天（不足 1 天写小时，不足 1 小时算 1 小时）；已过期返回 undefined。
export function timeLeft(iso: string | undefined | null, now = Date.now()): { days: number } | { hours: number } | undefined {
  if (!iso) return undefined
  const ms = new Date(iso).getTime() - now
  if (!(ms > 0)) return undefined
  const h = Math.ceil(ms / 3_600_000)
  return h < 24 ? { hours: h } : { days: Math.floor(h / 24) }
}

// splitRedacted：把命令按「[已隐去]」切开，奇数位是隐去的位置，网页把它画成小标。
export function splitRedacted(cmd: string): { text: string; redacted: boolean }[] {
  const out: { text: string; redacted: boolean }[] = []
  cmd.split(REDACTED).forEach((part, i) => {
    if (i > 0) out.push({ text: '', redacted: true })
    if (part) out.push({ text: part, redacted: false })
  })
  return out
}

// dayKey：取用记录按天分组用的本地日期键。
export function dayKey(iso: string): string {
  const d = new Date(iso)
  return `${d.getFullYear()}-${d.getMonth() + 1}-${d.getDate()}`
}

// groupByDay：记录（新的在前）按本地日期分组，组内顺序不变。
export function groupByDay<T extends { at: string }>(logs: T[]): { day: string; at: string; items: T[] }[] {
  const out: { day: string; at: string; items: T[] }[] = []
  for (const l of logs) {
    const k = dayKey(l.at)
    const last = out.at(-1)
    if (last && last.day === k) last.items.push(l)
    else out.push({ day: k, at: l.at, items: [l] })
  }
  return out
}

// normCommand：允许的命令去掉多余空白；空串返回空。
export const normCommand = (s: string) => s.trim().split(/\s+/).filter(Boolean).join(' ')

// addCommand：往允许的命令里加一条（去空白、去重）。
export function addCommand(list: string[], s: string): string[] {
  const c = normCommand(s)
  return c && !list.includes(c) ? [...list, c] : list
}

// unionCommands：申请里要的命令并进已有的（重新申请时预填：已有授权 + 这次要的）。
export function unionCommands(a: string[], b: string[]): string[] {
  return b.reduce(addCommand, a.reduce(addCommand, [] as string[]))
}

// missingValues：保存前检查——待填写的字段（filled = false）都要有值，返回没填的字段。
export function missingValues<F extends { env: string; filled?: boolean }>(fields: F[], values: Record<string, string>): F[] {
  return fields.filter((f) => !f.filled && !(values[f.env] ?? '').trim())
}

// filledValues：只发填了的值（空的表示保留原值）。
export function filledValues(values: Record<string, string>): Record<string, string> {
  return Object.fromEntries(Object.entries(values).filter(([, v]) => v.trim() !== ''))
}

// envOK：环境变量名只能用字母、数字和下划线，不能以数字开头（与后端一致）。
export const envOK = (s: string) => /^[A-Za-z_][A-Za-z0-9_]*$/.test(s)

// envFromLabel：新建时按显示名推一个环境变量名（API Token → API_TOKEN），推不出时为空。
export function envFromLabel(label: string): string {
  const s = label
    .trim()
    .replace(/[^A-Za-z0-9]+/g, '_')
    .replace(/^_+|_+$/g, '')
    .toUpperCase()
  return envOK(s) ? s : ''
}

// ---------- 敏感标记（R-318） ----------

// 少于这么多字符的值不参与脱敏（internal/hub/redact.go 的 secretRedactMin）
export const REDACT_MIN = 6

// 常见词：作敏感值时，纸鸢里所有同样的文字都会被隐去（R-298 第 3 点）。全小写，比对时忽略大小写。
export const COMMON_WORDS = new Set([
  'production', 'staging', 'development', 'develop', 'testing', 'preview', 'release', 'master', 'default', 'localhost',
  'admin', 'administrator', 'password', 'passwd', 'username', 'secret', 'token', 'public', 'private', 'example', 'sample',
  'changeme', 'welcome', 'letmein', 'qwerty', '123456', '1234567', '12345678', '123456789', 'abc123', 'password1',
  'undefined', 'latest', 'service', 'account', 'database', 'deploy', 'server', 'client', 'system', 'manager', 'support',
])

// valueHint：敏感值填写处的提示——太短（不会隐去）或是常见词（各处同样的文字都会隐去）；不敏感或空值不提示。
export function valueHint(value: string, plain?: boolean): 'short' | 'common' | undefined {
  const v = value.trim()
  if (plain || !v) return undefined
  if ([...value].length < REDACT_MIN) return 'short'
  if (COMMON_WORDS.has(v.toLowerCase())) return 'common'
  return undefined
}

// plainMap：字段的敏感标记（env → 不敏感），保存授权时一并发出。
export function plainMap(fields: { env: string; plain?: boolean }[]): Record<string, boolean> {
  return Object.fromEntries(fields.map((f) => [f.env, !!f.plain]))
}

// changedPlain：和字段原来的标记比，改了的那些（替换值时只发改了的）。
export function changedPlain(fields: { env: string; plain?: boolean }[], next: Record<string, boolean>): Record<string, boolean> {
  return Object.fromEntries(fields.filter((f) => f.env in next && next[f.env] !== !!f.plain).map((f) => [f.env, next[f.env]]))
}

// shownValues：凭据页「值」一栏：不敏感、已填的值明文列出；还有敏感的已填值时 masked = true（画成圆点）。
export function shownValues(fields: { plain?: boolean; filled?: boolean; value?: string }[]): { plain: string[]; masked: boolean } {
  const filled = fields.filter((f) => f.filled)
  return { plain: filled.filter((f) => f.plain && f.value).map((f) => f.value!), masked: filled.some((f) => !f.plain || !f.value) }
}

// ---------- 修改授权申请（R-320） ----------

// 有效期取这个值 = 到期时间不变（只在生效中的凭据上可用，后端 secretKeepExpiry）
export const KEEP_TTL = -1

// isAmend：凭据最近一次申请是修改授权申请（ask.mode = amend）
export const isAmend = (x?: { ask?: { mode?: string } | null } | null) => x?.ask?.mode === 'amend'

type Scope = { projects: string[]; allowed: string[]; disk: boolean }

export type AuthDiff = {
  projects?: { added: string[]; removed: string[] }
  expiry: boolean
  allowed?: { added: string[]; removed: string[]; kept: string[] }
  disk: boolean
}

// authDiff：改前（凭据现在的授权）→ 改后（抽屉里的授权）改了哪几项；没改的项为空 / false。
export function authDiff(before: Scope, after: Scope & { days: number }): AuthDiff {
  const minus = (a: string[], b: string[]) => a.filter((v) => !b.includes(v))
  const pAdd = minus(after.projects, before.projects)
  const pDel = minus(before.projects, after.projects)
  const cAdd = minus(after.allowed, before.allowed)
  const cDel = minus(before.allowed, after.allowed)
  return {
    projects: pAdd.length || pDel.length ? { added: pAdd, removed: pDel } : undefined,
    expiry: after.days !== KEEP_TTL,
    allowed: cAdd.length || cDel.length ? { added: cAdd, removed: cDel, kept: after.allowed.filter((v) => before.allowed.includes(v)) } : undefined,
    disk: before.disk !== after.disk,
  }
}

// diffEmpty：一项都没改
export const diffEmpty = (d: AuthDiff) => !d.projects && !d.expiry && !d.allowed && !d.disk

// ---------- 附言（R-321） ----------

// logNote：时间线上这条授权变更的附言。老数据里拒绝申请的附言记在 reason（R-295），新的都在 note。
export function logNote(l: { kind: string; note?: string; reason?: string }): string {
  if (l.note) return l.note
  return l.kind === 'reject' ? (l.reason ?? '') : ''
}
