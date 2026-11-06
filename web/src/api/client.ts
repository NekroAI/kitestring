import createClient, { type Middleware } from 'openapi-fetch'
import type { components, paths } from './schema'
import { t } from '../i18n'
import { classify, failText, reasonOf } from '../lib/fail'

export type Schemas = components['schemas']
type NN<T, K extends keyof T> = Omit<T, K> & { [P in K]-?: NonNullable<T[P]> }
// 后端总是返回数组，这里去掉 OpenAPI 生成的 null 可能，并在 queries 中做归一化兜底。
export type Session = NN<Schemas['Session'], 'requirements' | 'activity'>
export type Device = Schemas['Device']
export type Decision = Schemas['Decision']
export type Grant = Schemas['Grant']
export type Arrangement = Schemas['Arrangement']
export type MorningReport = Schemas['MorningReport']
export type Advice = Schemas['Advice']
export type AwayStatus = Schemas['AwayStatus']
export type AwayReport = Schemas['AwayReport']
export type Requirement = NN<Schemas['Requirement'], 'sessions'>
export type Project = Schemas['Project']
export type Workspace = Schemas['Workspace']
export type NotifyConfig = Schemas['NotifyConfig']
export type SettingsView = Schemas['SettingsView']
export type ReqFields = Schemas['ReqFields']
export type HubEvent = Schemas['Event']
export type PlanReq = Schemas['PlanReq']
export type Stage = NN<Schemas['Stage'], 'requirements'>
export type Plan = Omit<Schemas['Plan'], 'stages'> & { stages: Stage[] }
// 表达块（R-97）
export type Block = Schemas['Block']
export type BlockItem = Schemas['Item']
export type Payload = Schemas['Payload']
export type Evidence = Schemas['Evidence']
export type Prototype = Schemas['Prototype']
export type StatusChange = Schemas['StatusChange']
export type ReviewState = Schemas['ReviewState']

// 未登录回调：任何接口返回 401 时触发，由 App 跳回登录页。
let onUnauthorized: () => void = () => {}
export function setUnauthorizedHandler(fn: () => void) {
  onUnauthorized = fn
}

const authMiddleware: Middleware = {
  onResponse({ request, response }) {
    if (response.status === 401 && !new URL(request.url).pathname.startsWith('/api/auth/')) {
      onUnauthorized()
    }
    return response
  },
}

// 写操作最多等 30 秒（R-236 / R-265）：纸鸢重启、代理挂住时请求可能一直没有回音，按钮转着、什么提示都没有，像是「点了没反应」；
// 到点按失败处理，提示写明「纸鸢长时间未响应。未做改动，可重试。」。读取（GET）和实时推送不受影响；上传附件走 XHR，另有进度和超时。
const WRITE_TIMEOUT_MS = 30_000
const timedFetch = (req: Request) => (req.method === 'GET' || req.signal.aborted ? fetch(req) : fetch(new Request(req, { signal: AbortSignal.timeout(WRITE_TIMEOUT_MS) })))

export const api = createClient<paths>({ baseUrl: '', credentials: 'same-origin', fetch: timedFetch })
api.use(authMiddleware)

export class ApiError extends Error {
  status: number
  constructor(message: string, status: number) {
    super(message)
    this.status = status
  }
}

type Result<T> = { data?: T; error?: unknown; response: Response }

// unwrap 把 openapi-fetch 的结果转换为数据或抛出带后端说明的错误。
export async function unwrap<T>(p: Promise<Result<T>>): Promise<T> {
  const { data, error, response } = await p
  if (error !== undefined || !response.ok) {
    const e = error as { detail?: string; title?: string; errors?: { message?: string }[] } | undefined
    const extra = e?.errors?.map((x) => x.message).filter(Boolean).join(t.common.listSep)
    const head = e?.detail || e?.title
    const msg = head && extra ? t.common.labeled(head, extra) : head || extra || t.common.withStatus(t.common.requestFailed, response.status)
    throw new ApiError(msg, response.status)
  }
  return data as T
}

// errMsg：失败提示（R-236）。what 写这件事（「关闭 D-964」），提示成「关闭 D-964 未成功：无法连接纸鸢……未做改动，可重试。」；
// 不直接显示英文原文（Failed to fetch、Not Found 之类），纸鸢自己写的中文说明照用。规则在 lib/fail.ts。
export function errMsg(e: unknown, what?: string): string {
  return failText(classify(e), what)
}

// errReason：只要原因（读取失败、登录页这类不必写「未做改动，可重试」的地方），同样不显示英文原文（R-236）。
export function errReason(e: unknown): string {
  return reasonOf(classify(e))
}
