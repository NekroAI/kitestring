import { createContext, useCallback, useContext, useEffect, useRef, useState, type ReactNode } from 'react'
import { t } from '../i18n'
import { loadPref, savePref } from '../lib/util'
import { useActions } from './actions'
import { reducedMotion, SEAL_MS, sendBeacon, SHOW_STAMP, UNDO_MS } from './verdict'

// 决策答复的草稿与发件箱。
// - 草稿：每条决策的选择与文字，保存在本机，刷新不丢。
// - 发件箱：点了回答 / 批准 / 退回后先进撤销期（与全站同一时长 UNDO_MS，R-161），期间可撤销；到时一次性提交。

import type { FileRef } from '../ui/attach'

// picks：多点决策（R-112）逐点的选择，与 points 一一对应（-1 = 没选）
export type Draft = { choice: number | 'other' | null; note: string; noteOpen?: boolean; files?: FileRef[]; picks?: number[]; reason?: string }
export const emptyDraft: Draft = { choice: null, note: '' }
const DRAFTS_KEY = 'ks.drafts.v2'

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

// SendItem：一条答复。多点决策时 answer 是附言，choices 是逐点的选择，reject 表示整体拒绝。
type SendItem = { code: string; answer: string; files?: string[]; choices?: number[]; reject?: boolean; reason?: string }

type Payload = { answer: string; files?: string[]; choices?: number[]; reject?: boolean; reason?: string }
const toBody = (due: [string, Payload][]) => due.map(([ref, v]) => ({ ref, answer: v.answer, files: v.files, choices: v.choices, reject: v.reject, reason: v.reason }))

type Ctx = {
  drafts: Record<string, Draft>
  setDraft: (code: string, patch: Partial<Draft>) => void
  clearDrafts: (codes?: string[]) => void
  queued: Record<string, number> // code → 计划发送时间
  sealed: Record<string, 'seal' | 'done'> // 到点后：盖印中 / 已提交等卡片收起（D-229）
  send: (items: SendItem[]) => void
  cancel: (code: string) => void
}

const OutboxCtx = createContext<Ctx | null>(null)

export function OutboxProvider({ children }: { children: ReactNode }) {
  const { answer } = useActions()
  const [drafts, setDrafts] = useState<Record<string, Draft>>(() => loadPref(DRAFTS_KEY, {}))
  const [queued, setQueued] = useState<Record<string, number>>({})
  const pending = useRef(new Map<string, Payload & { timer: number }>())
  const sealing = useRef(new Map<string, Payload>()) // 盖印中、还没发出的答复（页面关闭时也要发）
  const [sealed, setSealed] = useState<Record<string, 'seal' | 'done'>>({})

  useEffect(() => savePref(DRAFTS_KEY, drafts), [drafts])

  const setDraft = useCallback((code: string, patch: Partial<Draft>) => {
    setDrafts((cur) => {
      const d = { ...emptyDraft, ...cur[code], ...patch }
      const next = { ...cur }
      if (d.choice == null && !d.note && !d.noteOpen && !d.files?.length && !d.picks && !d.reason) delete next[code]
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

  // 到点：先盖印、停顿片刻（与需求验收同一段生效动画），再一次性提交；成功后保持「已盖印」直到卡片收起。
  const flush = useCallback(async () => {
    const due: [string, Payload][] = [...pending.current.entries()].map(([c, v]) => [c, v])
    if (!due.length) return
    pending.current.forEach((v) => window.clearTimeout(v.timer))
    pending.current.clear()
    const codes = due.map(([c]) => c)
    const mark = (v?: 'seal' | 'done') =>
      setSealed((cur) => {
        const next = { ...cur }
        codes.forEach((c) => (v ? (next[c] = v) : delete next[c]))
        return next
      })
    due.forEach(([c, v]) => sealing.current.set(c, v))
    mark('seal')
    if (SHOW_STAMP && !reducedMotion()) await new Promise((r) => window.setTimeout(r, SEAL_MS))
    if (codes.some((c) => !sealing.current.has(c))) return // 页面关闭时已经发出
    codes.forEach((c) => sealing.current.delete(c))
    const res = await answer(toBody(due))
    setQueued((cur) => {
      const next = { ...cur }
      codes.forEach((c) => delete next[c])
      return next
    })
    // 完成反馈统一为印章 + 卡片收起 + 最近动态记一条（R-161 / D-229），不另弹「已答复」
    if (!res) return mark()
    clearDrafts(codes)
    mark('done')
    window.setTimeout(() => mark(), 2500)
  }, [answer, clearDrafts])

  const send = useCallback(
    (items: SendItem[]) => {
      const at = Date.now() + UNDO_MS
      for (const it of items) {
        const old = pending.current.get(it.code)
        if (old) window.clearTimeout(old.timer)
        // 所有排队项共用一个到期时间，到期时一起提交。
        pending.current.set(it.code, { answer: it.answer, files: it.files, choices: it.choices, reject: it.reject, reason: it.reason, timer: window.setTimeout(() => void flush(), UNDO_MS) })
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

  // 标签页只是隐藏时照常计时、到点提交（撤销机会不丢）；页面真的关闭或刷新时用 keepalive 立即发出，避免丢失（R-161）。
  useEffect(() => {
    const onHide = () => {
      const due: [string, Payload][] = [...pending.current.entries(), ...sealing.current.entries()]
      if (!due.length) return
      pending.current.forEach((v) => window.clearTimeout(v.timer))
      pending.current.clear()
      sealing.current.clear()
      sendBeacon({ method: 'POST', url: '/api/decisions/answers', body: { answers: toBody(due) } })
    }
    window.addEventListener('pagehide', onHide)
    return () => window.removeEventListener('pagehide', onHide)
  }, [])

  return <OutboxCtx.Provider value={{ drafts, setDraft, clearDrafts, queued, sealed, send, cancel }}>{children}</OutboxCtx.Provider>
}

export function useOutbox() {
  const c = useContext(OutboxCtx)
  if (!c) throw new Error('OutboxProvider missing')
  return c
}
