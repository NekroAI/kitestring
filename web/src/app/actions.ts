import { useQueryClient } from '@tanstack/react-query'
import { useCallback, useState } from 'react'
import { toast } from 'sonner'
import { api, errMsg, unwrap, type NotifyConfig, type ReqFields } from '../api/client'
import { qk } from '../api/queries'
import { t } from '../i18n'
import { uploadsBusy } from '../ui/attach'

const planKeys = ['plans', 'requirements', 'decisions', 'sessions']

// PlanBody：新建 / 编辑计划。stages 里的需求用 ref（已有）或 title（新建）。
export type PlanBody = {
  title: string
  goal?: string
  project_id?: string
  owner_session?: string
  stages?: { id?: string; title: string; requirements: { ref?: string; title?: string }[] }[]
}

// 所有写操作集中在这里：调用 API、失败时弹出错误、成功后立即刷新相关数据（SSE 也会推送，这里是为了更快）。

export function useActions() {
  const qc = useQueryClient()
  const refresh = useCallback((...keys: string[]) => keys.forEach((k) => void qc.invalidateQueries({ queryKey: [k] })), [qc])

  const run = useCallback(async <T,>(fn: () => Promise<T>, keys: string[]): Promise<T | undefined> => {
    try {
      const r = await fn()
      refresh(...keys)
      return r
    } catch (e) {
      toast.error(errMsg(e))
      return undefined
    }
  }, [refresh])
  // 带附件的发送：附件没传完就先不发，否则发出去的内容里少了它
  const send = useCallback(<T,>(fn: () => Promise<T>, keys: string[]) => {
    if (uploadsBusy()) {
      toast(t.attach.wait)
      return Promise.resolve(undefined)
    }
    return run(fn, keys)
  }, [run])

  return {
    answer: (answers: { ref: string; answer: string; files?: string[] }[]) =>
      send(() => unwrap(api.POST('/api/decisions/answers', { body: { answers } })), ['decisions', 'sessions', 'requirements']),
    reassign: (ref: string, sessionKey: string) =>
      run(() => unwrap(api.POST('/api/decisions/{ref}/reassign', { params: { path: { ref } }, body: { session_key: sessionKey } })), ['decisions', 'sessions']),
    closeDecision: (ref: string) => run(() => unwrap(api.POST('/api/decisions/{ref}/close', { params: { path: { ref } }, body: {} })), ['decisions', 'sessions']),
    review: (items: { ref: string; verdict: 'accept' | 'revise'; comment?: string; files?: string[] }[]) =>
      send(() => unwrap(api.POST('/api/reviews', { body: { items } })), ['requirements', 'decisions', 'sessions']),
    note: (ref: string, comment: string, files: string[]) =>
      send(() => unwrap(api.POST('/api/requirements/{ref}/notes', { params: { path: { ref } }, body: { comment, files } })), ['requirements', 'decisions', 'sessions']),
    assign: (ref: string, sessionKey: string, comment = '') =>
      run(() => unwrap(api.POST('/api/requirements/{ref}/assign', { params: { path: { ref } }, body: { session_key: sessionKey, comment } })), ['requirements', 'decisions', 'sessions']),
    markDone: (key: string, done: boolean) => run(() => unwrap(api.POST('/api/sessions/{key}/done', { params: { path: { key } }, body: { done } })), ['sessions']),
    resendDecision: (ref: string) => run(() => unwrap(api.POST('/api/decisions/{ref}/resend', { params: { path: { ref } }, body: {} })), ['decisions']),

    createReq: (body: ReqFields) => send(() => unwrap(api.POST('/api/requirements', { body })), ['requirements']),
    updateReq: (ref: string, body: ReqFields) => run(() => unwrap(api.PUT('/api/requirements/{ref}', { params: { path: { ref } }, body })), ['requirements', 'sessions']),
    deleteReq: (ref: string) => run(() => unwrap(api.DELETE('/api/requirements/{ref}', { params: { path: { ref } } })), ['requirements', 'sessions']),
    linkReq: (ref: string, sessionKey: string, unlink = false) =>
      run(() => unwrap(api.POST('/api/requirements/{ref}/links', { params: { path: { ref } }, body: { session_key: sessionKey, unlink } })), ['requirements', 'sessions']),

    createPlan: (body: PlanBody & { start?: boolean }) => run(() => unwrap(api.POST('/api/plans', { body: { ...body, stages: body.stages ?? [] } })), planKeys),
    updatePlan: (ref: string, body: PlanBody) => run(() => unwrap(api.PUT('/api/plans/{ref}', { params: { path: { ref } }, body })), planKeys),
    deletePlan: (ref: string) => run(() => unwrap(api.DELETE('/api/plans/{ref}', { params: { path: { ref } } })), planKeys),
    startPlan: (ref: string, comment = '', files: string[] = []) => send(() => unwrap(api.POST('/api/plans/{ref}/start', { params: { path: { ref } }, body: { comment, files } })), planKeys),
    rejectPlan: (ref: string, comment = '', files: string[] = []) => send(() => unwrap(api.POST('/api/plans/{ref}/reject', { params: { path: { ref } }, body: { comment, files } })), planKeys),
    passStage: (ref: string, seq: number, comment = '', files: string[] = []) =>
      send(() => unwrap(api.POST('/api/plans/{ref}/stages/{seq}/pass', { params: { path: { ref, seq } }, body: { comment, files } })), planKeys),
    reviseStage: (ref: string, seq: number, comment: string, files: string[] = []) =>
      send(() => unwrap(api.POST('/api/plans/{ref}/stages/{seq}/revise', { params: { path: { ref, seq } }, body: { comment, files } })), planKeys),

    createDevice: (name: string) => run(() => unwrap(api.POST('/api/devices', { body: { name } })), ['devices', 'sessions']),
    revokeDevice: (id: string) => run(() => unwrap(api.POST('/api/devices/{id}/revoke', { params: { path: { id } } })), ['devices', 'sessions']),

    updateProject: (id: string, body: { name: string; workspace_id?: string; doc_root: string; auto_dispatch?: boolean; hidden?: boolean }) =>
      run(() => unwrap(api.PUT('/api/projects/{id}', { params: { path: { id } }, body })), ['projects', 'sessions', 'requirements']),
    mergeProject: (id: string, into: string) =>
      run(() => unwrap(api.POST('/api/projects/{id}/merge', { params: { path: { id } }, body: { into } })), ['projects', 'sessions', 'requirements', 'decisions', 'plans']),
    createWorkspace: (name: string, notify: NotifyConfig) => run(() => unwrap(api.POST('/api/workspaces', { body: { name, notify } })), ['projects']),
    updateWorkspace: (id: string, name: string, notify: NotifyConfig) =>
      run(() => unwrap(api.PUT('/api/workspaces/{id}', { params: { path: { id } }, body: { name, notify } })), ['projects']),
    deleteWorkspace: (id: string) => run(() => unwrap(api.DELETE('/api/workspaces/{id}', { params: { path: { id } } })), ['projects']),

    logout: async () => {
      await api.POST('/api/auth/logout')
      qc.setQueryData(qk.auth, (cur: { initialized: boolean; version: string } | undefined) => ({ initialized: true, version: cur?.version ?? '', logged_in: false }))
    },
  }
}

// useBusy：给异步按钮提供 loading 状态。
export function useBusy() {
  const [busy, setBusy] = useState(false)
  const wrap = useCallback(async <T,>(fn: () => Promise<T>) => {
    setBusy(true)
    try {
      return await fn()
    } finally {
      setBusy(false)
    }
  }, [])
  return [busy, wrap] as const
}
