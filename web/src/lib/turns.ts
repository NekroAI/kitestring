// 会话详情页按轮的时间线（R-273）的纯计算：对象索引筛选、条目 ID、工具一行、页合并、索引折叠。
// 只放纯计算，不碰 React，单测直接跑（lib/turns.test.ts）。

export type ObjLike = { code: string; kind: string; items: string[] | null; pending?: boolean }
export type ItemLike = { id: string }
export type TurnLike = {
  id: string
  prompt_id?: string
  reply_id?: string
  inbox: ItemLike[] | null
  items: ItemLike[] | null
  after: ItemLike[] | null
}

// relatedIds：选中对象（R-x / D-x / agents / messages / briefs）时相关条目的 ID；没选中返回 null（不筛选）。
export function relatedIds(objects: ObjLike[], code: string | null | undefined): Set<string> | null {
  if (!code) return null
  const o = objects.find((x) => x.code === code)
  return new Set(o?.items ?? [])
}

// 时间线上各条目的 ID：纸鸢记录用记录 ID，提问 / 回复用 p:<事件 ID> / r:<事件 ID>（与服务端对象索引一致）。
export const promptKey = (t: TurnLike) => (t.prompt_id ? `p:${t.prompt_id}` : '')
export const replyKey = (t: TurnLike) => (t.reply_id ? `r:${t.reply_id}` : '')

// countHits：筛选时这几页里亮着的条目数（顶部「只看和 R-x 有关的 n 条」的 n 用对象的全部条目数，这里用来判断这几页有没有）。
export function countHits(turns: TurnLike[], ids: Set<string>): number {
  let n = 0
  for (const t of turns) {
    for (const x of [...(t.inbox ?? []), ...(t.items ?? []), ...(t.after ?? [])]) if (ids.has(x.id)) n++
    if (ids.has(promptKey(t))) n++
    if (ids.has(replyKey(t))) n++
  }
  return n
}

// toolLine：工具调用收成的一行「调用 12 次工具 · Bash 7 · Edit 3」里名字的部分，最多 3 个，其余不写。
export function toolLine(names: { name: string; n: number }[] | null, max = 3): string[] {
  return (names ?? []).slice(0, max).map((x) => (x.n > 1 ? `${x.name} ${x.n}` : x.name))
}

// mergePages：无限分页（新的一页在前）合成时间正序的轮；同一轮出现在两页边界时只留新的那份。
export function mergePages<T extends { id: string; start: string }>(pages: { turns: T[] | null }[]): T[] {
  const seen = new Set<string>()
  const out: T[] = []
  for (const p of pages) {
    const list = [...(p.turns ?? [])].reverse() // 每页内新的在前
    for (const t of list) {
      const k = `${t.id}@${t.start}`
      if (seen.has(k)) continue
      seen.add(k)
      out.push(t)
    }
  }
  return out.reverse()
}

// sortObjects：对象索引顺序——待你答的决策在最前，其余保持服务端顺序（最近动过的在前），计数项在最后。
export function sortObjects<T extends ObjLike>(objs: T[]): T[] {
  const counts = new Set(['agents', 'messages', 'briefs'])
  const rank = (o: T) => (o.pending ? 0 : counts.has(o.kind) ? 2 : 1)
  return objs.map((o, i) => [o, i] as const).sort((a, b) => rank(a[0]) - rank(b[0]) || a[1] - b[1]).map(([o]) => o)
}

// fitRows：索引最多一行半——给每个元素的 [左, 上, 宽]（相对容器）与容器宽度，算能放下几个；
// 第一行放满，第二行只用左半边，还剩时末尾留一个「+n」的位置（宽 plusW）。返回可见个数。
export function fitRows(boxes: { left: number; top: number; width: number }[], width: number, plusW: number): number {
  if (!boxes.length) return 0
  const top0 = boxes[0].top
  const fits = (b: { left: number; top: number; width: number }) => b.top === top0 || (b.top > top0 && b.left + b.width <= width / 2 && isSecondRow(boxes, b, top0))
  let n = 0
  while (n < boxes.length && fits(boxes[n])) n++
  if (n === boxes.length) return n
  // 给 +n 腾位置：+n 放在第 n 个后面，放不进就往回让
  while (n > 0) {
    const last = boxes[n - 1]
    const plusLeft = last.left + last.width + 6
    const onFirst = last.top === top0
    // 第一行末尾放不下时 +n 换到第二行开头
    if (onFirst ? plusLeft + plusW <= width || plusW <= width / 2 : plusLeft + plusW <= width / 2) break
    n--
  }
  return n
}

function isSecondRow(boxes: { top: number }[], b: { top: number }, top0: number) {
  const second = boxes.find((x) => x.top > top0)?.top
  return b.top === second
}
