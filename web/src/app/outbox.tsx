import { createContext, useCallback, useContext, useEffect, useRef, useState, type ReactNode } from 'react'
import { toast } from 'sonner'
import { t } from '../i18n'
import { loadPref, savePref } from '../lib/util'
import { useActions } from './actions'

// 决策答复的草稿与发件箱。
// - 草稿：每条决策的选择与文字，保存在本机，刷新不丢。
// - 发件箱：点击发送后先等待几秒，期间可撤销；到时一次性提交。

import type { FileRef } from '../ui/attach'

export type Draft = { choice: number | 'other' | null; note: string; noteOpen?: boolean; files?: FileRef[] }
export const emptyDraft: Draft = { choice: null, note: '' }
const DRAFTS_KEY = 'ks.drafts.v2'
export const UNDO_MS = 4000

// composeAnswer 把草稿合成为最终答复文本；返回空串表示还不能发送。
export function composeAnswer(d: Draft | undefined, options: string[]): string {
  if (!d) return ''
  const note = d.note.trim()
  if (d.choice === 'other' || options.length === 0) return note || (d.files?.length ? t.decision.seeImages : '')
  if (typeof d.choice === 'number' && options[d.choice] != null) {
    const c = options[d.choice]
    return note ? t.decision.joinAnswer(c, note) : c
  }
  return ''
}

type Ctx = {
  drafts: Record<string, Draft>
  setDraft: (code: string, patch: Partial<Draft>) => void
  clearDrafts: (codes?: string[]) => void
  queued: Record<string, number> // code → 计划发送时间
  send: (items: { code: string; answer: string; files?: string[] }[]) => void
  cancel: (code: string) => void
}

const OutboxCtx = createContext<Ctx | null>(null)

export function OutboxProvider({ children }: { children: ReactNode }) {
  const { answer } = useActions()
  const [drafts, setDrafts] = useState<Record<string, Draft>>(() => loadPref(DRAFTS_KEY, {}))
  const [queued, setQueued] = useState<Record<string, number>>({})
  const pending = useRef(new Map<string, { answer: string; files?: string[]; timer: number }>())

  useEffect(() => savePref(DRAFTS_KEY, drafts), [drafts])

  const setDraft = useCallback((code: string, patch: Partial<Draft>) => {
    setDrafts((cur) => {
      const d = { ...emptyDraft, ...cur[code], ...patch }
      const next = { ...cur }
      if (d.choice == null && !d.note && !d.noteOpen && !d.files?.length) delete next[code]
      else next[code] = d
      return next
    })
  }, [])

  const clearDrafts = useCallback((codes?: string[]) => {
    setDrafts((cur) => {
      if (!codes) return {}
      const next = { ...cur }
      codes.forEach((c) => delete next[c])
      return next
    })
  }, [])

  const flush = useCallback(async () => {
    const due = [...pending.current.entries()]
    if (!due.length) return
    pending.current.clear()
    due.forEach(([, v]) => window.clearTimeout(v.timer))
    const codes = due.map(([c]) => c)
    const res = await answer(due.map(([ref, v]) => ({ ref, answer: v.answer, files: v.files })))
    setQueued((cur) => {
      const next = { ...cur }
      codes.forEach((c) => delete next[c])
      return next
    })
    if (res) {
      clearDrafts(codes)
      toast.success(codes.length === 1 ? t.decision.sent(codes[0]) : t.decision.sent(codes.join('、')), { description: t.decision.sentDesc })
    }
  }, [answer, clearDrafts])

  const send = useCallback(
    (items: { code: string; answer: string; files?: string[] }[]) => {
      const at = Date.now() + UNDO_MS
      for (const it of items) {
        const old = pending.current.get(it.code)
        if (old) window.clearTimeout(old.timer)
        // 所有排队项共用一个到期时间，到期时一起提交。
        pending.current.set(it.code, { answer: it.answer, files: it.files, timer: window.setTimeout(() => void flush(), UNDO_MS) })
      }
      setQueued((cur) => {
        const next = { ...cur }
        items.forEach((it) => (next[it.code] = at))
        return next
      })
    },
    [flush],
  )

  const cancel = useCallback((code: string) => {
    const p = pending.current.get(code)
    if (p) window.clearTimeout(p.timer)
    pending.current.delete(code)
    setQueued((cur) => {
      const next = { ...cur }
      delete next[code]
      return next
    })
  }, [])

  // 离开页面前把排队中的答复立即发出，避免丢失。
  useEffect(() => {
    const onHide = () => {
      if (document.visibilityState === 'hidden') void flush()
    }
    document.addEventListener('visibilitychange', onHide)
    return () => document.removeEventListener('visibilitychange', onHide)
  }, [flush])

  return <OutboxCtx.Provider value={{ drafts, setDraft, clearDrafts, queued, send, cancel }}>{children}</OutboxCtx.Provider>
}

export function useOutbox() {
  const c = useContext(OutboxCtx)
  if (!c) throw new Error('OutboxProvider missing')
  return c
}
