import { useMemo } from 'react'
import { useActivity } from '../api/queries'
import { lineOf } from '../lib/activity'

// 最近动态（R-296）：后端动态接口按时间线记录给出最近 n 条（交证据、提交验收、提决策、受阻与解除、留言、指派、
// 提交评审、评审意见、你的答复与验收、阶段验收），句子由 lib/activity 组。项目页与工作台右栏共用。
export type RecentItem = { at: string; code: string; text: string; dot: string; stage?: boolean }

export function useRecent(projectId: string | undefined, limit: number, mine = false): { items: RecentItem[]; loading: boolean } {
  const q = useActivity(projectId, limit, mine)
  const items = useMemo(
    () =>
      (q.data ?? []).map((a) => {
        const { text, dot } = lineOf(a)
        // 点开：对象是决策时打开所属需求
        const code = a.kind === 'stage' ? a.code : a.req_code || (a.code.startsWith('R-') ? a.code : '')
        return { at: a.at, code, text, dot, stage: a.kind === 'stage' }
      }),
    [q.data],
  )
  return { items, loading: q.isPending }
}
