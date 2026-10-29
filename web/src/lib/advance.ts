// 连续审批的自动翻页时机（R-226，docs/principles.md 第 8 条）：判断下了之后停在这一件、盖着撤销遮罩，
// 撤销期走完（进入盖印）才自动翻到下一件；撤销期内撤回、或用户自己翻走，这次自动跳转一并取消。
// 这里只放「什么时候该翻」的判断，计时仍由 verdict.tsx 的 schedule 一套负责。

export type AdvanceEntry = { at: number; phase: 'wait' | 'seal' | 'done' }

// advanceStep：armed = 在这一件上亲眼看着它走完撤销期的那次排上（entry.at），0 表示没有。
// - 撤销期（wait）：登记 armed；
// - 进入盖印 / 已提交：只有 armed 对得上才翻（fire），翻过一次就清零，翻回来看它不会再被翻走；
// - 没有这件事了（撤回、失败回滚）或不再连续审批：清零，不翻。
export function advanceStep(armed: number, e: AdvanceEntry | undefined, on: boolean): { armed: number; fire: boolean } {
  if (!on || !e) return { armed: 0, fire: false }
  if (e.phase === 'wait') return { armed: e.at, fire: false }
  return { armed: 0, fire: armed === e.at }
}
