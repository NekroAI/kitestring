// 上下文轻重的判定（R-224、R-225）：全站只这一处。limit = 建议压缩阈值（设置里配，默认 30 万）；知道窗口时取它和窗口 80% 中小的。
// 到 limit 一半偏重（mid，黄），到 limit 很重（high，红）= 建议压缩。ratio 是圆环填充：占窗口的比例，不知道窗口时按 100 万算。
export const DEFAULT_COMPACT = 300_000
export const DEFAULT_WINDOW = 1_000_000

export type CtxLevel = 'ok' | 'mid' | 'high'

export function ctxLevel(used: number, max: number, at: number = DEFAULT_COMPACT): { level: CtxLevel; limit: number; ratio: number } {
  const limit = max > 0 ? Math.min(at, Math.round(max * 0.8)) : at
  const level: CtxLevel = used >= limit ? 'high' : used >= limit / 2 ? 'mid' : 'ok'
  return { level, limit, ratio: Math.min(1, used / (max > 0 ? max : DEFAULT_WINDOW)) }
}
