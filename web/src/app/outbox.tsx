import { createContext, useCallback, useContext, type ReactNode } from 'react'
import { toast } from 'sonner'
import { t } from '../i18n'
import type { Scene } from '../i18n/zh'
import { useActions } from './actions'
import { schedule, scheduleMany, undo, type Act, type ScheduleOpts } from './verdict'
import { dropDraft } from './draft'

// 决策答复的发件箱。
// - 草稿：每条决策的选择与文字，和其它输入框同一个 useDraft（app/draft.tsx，键 dec:D-8，R-202）；原来的 ks.drafts.v2 在那里搬过去。
// - 发件箱：点了回答 / 批准 / 拒绝后进撤销期——和需求验收、计划同一个调度器（verdict.tsx 的 schedule，R-201），
//   计时、盖印、提交、页面关闭时立即提交都在那里；这里只把答复变成一次提交。撤销期的键是 decKey(code)。

import type { FileRef } from '../ui/attach'

// picks：多点决策（R-112）逐点的选择，与 points 一一对应（-1 = 没选）
export type Draft = { choice: number | 'other' | null; note: string; noteOpen?: boolean; files?: FileRef[]; picks?: number[]; reason?: string }
export const emptyDraft: Draft = { choice: null, note: '' }
// decDraftKey：决策答复草稿的键（useDraft）
export const decDraftKey = (code: string) => `dec:${code}`

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

// decKey：决策答复在撤销期里的键
export const decKey = (code: string) => `dec:${code}`

// SendItem：一条答复。多点决策时 answer 是附言，choices 是逐点的选择，reject 表示整体拒绝；act / scene 决定撤销条和印章的用词。
type SendItem = { code: string; answer: string; files?: string[]; choices?: number[]; reject?: boolean; reason?: string; act: Act; scene: Scene }

const toBody = (it: SendItem) => ({ ref: it.code, answer: it.answer, files: it.files, choices: it.choices, reject: it.reject, reason: it.reason })

type Ctx = {
  send: (items: SendItem[]) => void
  cancel: (code: string) => void
}

const OutboxCtx = createContext<Ctx | null>(null)

export function OutboxProvider({ children }: { children: ReactNode }) {
  const { answer } = useActions()
  // 完成反馈统一为印章 + 卡片收起 + 最近动态记一条（R-161 / D-229），不另弹「已答复」
  const opts = useCallback(
    (it: SendItem): { key: string } & ScheduleOpts => ({
      key: decKey(it.code),
      act: it.act,
      scene: it.scene,
      subject: it.code,
      beacon: { method: 'POST', url: '/api/decisions/answers', body: { answers: [toBody(it)] } },
      commit: async () => {
        const res = await answer([toBody(it)])
        if (!res) return false
        // 改状态申请在你批准前状态已被改动（R-188）：后端把它作废了，告诉你没有执行
        if (res.decisions?.some((d) => d.voided)) toast(t.ask.status.voidedToast)
        dropDraft(decDraftKey(it.code)) // 提交成功才清草稿：撤销期里撤回时草稿还在
        return true
      },
    }),
    [answer],
  )

  const send = useCallback(
    (items: SendItem[]) => {
      if (items.length === 1) {
        const { key, ...o } = opts(items[0])
        schedule(key, o)
      } else scheduleMany(items.map(opts))
    },
    [opts],
  )

  const cancel = useCallback((code: string) => void undo(decKey(code)), [])

  return <OutboxCtx.Provider value={{ send, cancel }}>{children}</OutboxCtx.Provider>
}

export function useOutbox() {
  const c = useContext(OutboxCtx)
  if (!c) throw new Error('OutboxProvider missing')
  return c
}
