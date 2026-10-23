import createClient, { type Middleware } from 'openapi-fetch'
import type { components, paths } from './schema'
import { t } from '../i18n'

export type Schemas = components['schemas']
type NN<T, K extends keyof T> = Omit<T, K> & { [P in K]-?: NonNullable<T[P]> }
// 后端总是返回数组，这里去掉 OpenAPI 生成的 null 可能，并在 queries 中做归一化兜底。
export type Session = NN<Schemas['Session'], 'requirements' | 'activity'>
export type Device = Schemas['Device']
export type Decision = Schemas['Decision']
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

export const api = createClient<paths>({ baseUrl: '', credentials: 'same-origin' })
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

export function errMsg(e: unknown): string {
  if (e instanceof Error) return e.message
  return t.common.requestFailed
}
