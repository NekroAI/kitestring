import { useCallback } from 'react'
import { useSearchParams } from 'react-router'

// 详情面板通过 URL 参数打开（?s=会话键、?r=需求编号），任何页面都能打开、链接可分享。

export function useOpenSession() {
  const [, setParams] = useSearchParams()
  return useCallback(
    (key: string) =>
      setParams((p) => {
        const n = new URLSearchParams(p)
        n.delete('r')
        n.set('s', key)
        return n
      }),
    [setParams],
  )
}

export function useOpenReq() {
  const [, setParams] = useSearchParams()
  return useCallback(
    (ref: string) =>
      setParams((p) => {
        const n = new URLSearchParams(p)
        n.delete('s')
        n.set('r', ref)
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
        return n
      }),
    [setParams],
  )
  return { session: params.get('s'), req: params.get('r'), close }
}
