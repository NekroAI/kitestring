import { createContext, useCallback, useContext } from 'react'
import { useSearchParams } from 'react-router'

// 详情面板通过 URL 参数打开（?s=会话键、?r=需求编号、?plan=计划编号、?a=审批条目），任何页面都能打开、链接可分享。

export function useOpenSession() {
  const [, setParams] = useSearchParams()
  return useCallback(
    (key: string) =>
      setParams((p) => {
        const n = new URLSearchParams(p)
        n.delete('r')
        n.delete('a')
        n.delete('bk')
        n.set('s', key)
        return n
      }),
    [setParams],
  )
}

// 并排打开：某个抽屉把“点需求”接过去，在它左侧再展开一张纸（R-196 的引用预览），自己保持不动。
export const SideReqContext = createContext<((ref: string) => void) | null>(null)
// PeekContext（R-196）：抽屉里点任何引用（需求 / 决策 / 计划）都在左边那张纸里打开；没有时照旧整个切过去。
export const PeekContext = createContext<((code: string) => void) | null>(null)

// back：「在这里打开」时记下原来那个抽屉（bk 参数，如 r:R-187、a:r:R-187、plan:P-3、s:会话键），新抽屉顶上出现「‹ 返回」。
const withBack = (n: URLSearchParams, back?: string) => {
  if (back) n.set('bk', back)
  else n.delete('bk')
  return n
}

// useNavReq：直接把需求抽屉切到这一条（不经过左边那张纸）。
export function useNavReq() {
  const [, setParams] = useSearchParams()
  return useCallback(
    (ref: string, back?: string) =>
      setParams((p) => {
        const n = new URLSearchParams(p)
        n.delete('s')
        n.delete('a')
        n.delete('plan')
        n.delete('q')
        n.delete('qp')
        n.set('r', ref)
        return withBack(n, back)
      }),
    [setParams],
  )
}

export function useOpenReq() {
  const side = useContext(SideReqContext)
  const open = useNavReq()
  return side ?? open
}

// 逐条确认草稿（R-133）：打开需求抽屉并按草稿队列连续处理（确认 / 搁置 / 下一条）；project 为空 = 全部项目。
export function useOpenDraftQueue() {
  const [, setParams] = useSearchParams()
  return useCallback(
    (ref: string, project: string) =>
      setParams((p) => {
        const n = new URLSearchParams(p)
        n.delete('s')
        n.delete('a')
        n.set('r', ref)
        n.delete('bk')
        n.set('q', 'drafts')
        if (project) n.set('qp', project)
        else n.delete('qp')
        return n
      }),
    [setParams],
  )
}

export function useOpenPlan() {
  const [, setParams] = useSearchParams()
  return useCallback(
    (ref: string, back?: string) =>
      setParams((p) => {
        const n = new URLSearchParams(p)
        n.delete('s')
        n.delete('r')
        n.delete('a')
        n.set('plan', ref)
        return withBack(n, back)
      }),
    [setParams],
  )
}

export function usePanelParams() {
  const [params, setParams] = useSearchParams()
  const close = useCallback(
    () =>
      setParams((p) => {
        const n = new URLSearchParams(p)
        n.delete('s')
        n.delete('r')
        n.delete('plan')
        n.delete('a')
        n.delete('q')
        n.delete('qp')
        n.delete('bk')
        return n
      }),
    [setParams],
  )
  // a：审批抽屉（R-177），值是「要你处理」条目的键（见 app/asks.tsx）
  return { session: params.get('s'), req: params.get('r'), plan: params.get('plan'), ask: params.get('a'), close }
}

// useOpenAsk：打开审批抽屉（?a=键，R-177；键见 app/asks.tsx），关掉其它抽屉。
export function useOpenAsk() {
  const [, setParams] = useSearchParams()
  return useCallback(
    (key: string, back?: string) =>
      setParams((p) => {
        const n = new URLSearchParams(p)
        for (const k of ['s', 'r', 'plan', 'q', 'qp']) n.delete(k)
        n.set('a', key)
        return withBack(n, back)
      }),
    [setParams],
  )
}

// useGoBack（R-196）：「在这里打开」之后回到原来那个抽屉。label 是顶上「‹ 返回 …」里写的编号（会话写「会话」）。
export function useGoBack() {
  const [params, setParams] = useSearchParams()
  const bk = params.get('bk')
  const go = useCallback(
    () =>
      setParams((p) => {
        const n = new URLSearchParams(p)
        const b = n.get('bk') ?? ''
        const i = b.indexOf(':')
        if (i < 0) return n
        const kind = b.slice(0, i)
        const v = b.slice(i + 1)
        for (const k of ['s', 'r', 'plan', 'a', 'q', 'qp', 'bk']) n.delete(k)
        n.set(kind === 'plan' ? 'plan' : kind, v)
        return n
      }),
    [setParams],
  )
  if (!bk || bk.indexOf(':') < 0) return null
  const kind = bk.slice(0, bk.indexOf(':'))
  const v = bk.slice(bk.indexOf(':') + 1)
  // 审批抽屉的键形如 r:R-187、d:D-12、p:P-3；会话没有编号
  const code = kind === 'a' ? (/^[rdpgxo]:/.test(v) ? v.slice(2) : '') : kind === 's' ? '' : v
  return { kind, code, go }
}
