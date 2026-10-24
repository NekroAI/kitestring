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
        n.set('s', key)
        return n
      }),
    [setParams],
  )
}

// 并排打开：宽屏下某个抽屉（如会话详情）把“点需求”接过去，在它左侧再开一层，自己保持不动。
export const SideReqContext = createContext<((ref: string) => void) | null>(null)

export function useOpenReq() {
  const [, setParams] = useSearchParams()
  const side = useContext(SideReqContext)
  const open = useCallback(
    (ref: string) =>
      setParams((p) => {
        const n = new URLSearchParams(p)
        n.delete('s')
        n.delete('a')
        n.delete('q')
        n.delete('qp')
        n.set('r', ref)
        return n
      }),
    [setParams],
  )
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
    (ref: string) =>
      setParams((p) => {
        const n = new URLSearchParams(p)
        n.delete('s')
        n.delete('r')
        n.delete('a')
        n.set('plan', ref)
        return n
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
    (key: string) =>
      setParams((p) => {
        const n = new URLSearchParams(p)
        for (const k of ['s', 'r', 'plan', 'q', 'qp']) n.delete(k)
        n.set('a', key)
        return n
      }),
    [setParams],
  )
}
