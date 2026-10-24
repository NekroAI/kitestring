// 撤销期的纯逻辑（R-201 / R-200）：浮动条该显示哪些、需求此刻该显示在哪一列、哪些需求换了列。
// 调度器本身（计时、提交、盖印）在 app/verdict.tsx；这里只放不碰 DOM 的部分，方便单测。

export type DockItem = { key: string; at: number; phase: string; group?: string }

// dockView：顶部浮动条显示什么（docs/principles.md 第 8 条）。
// - 只看撤销期（wait）里的事；元素看得见（有可见的锚点）时不出浮动条，撤回就在元素上。
// - 批量操作（同一 group 且至少两件在等）只出一条汇总「n 项 · 全部撤回」，不论卡片看不看得见。
// - 其余看不见的事：只显示最近一件，另写「还有 n 件在等生效」（rest），可展开逐件撤回。
export function dockView<T extends DockItem>(items: T[], visible: (key: string) => boolean) {
  const waiting = items.filter((x) => x.phase === 'wait')
  const byGroup = new Map<string, T[]>()
  for (const x of waiting) if (x.group) byGroup.set(x.group, [...(byGroup.get(x.group) ?? []), x])
  const groups = [...byGroup.entries()].filter(([, xs]) => xs.length > 1).map(([id, xs]) => ({ id, items: xs, at: Math.max(...xs.map((x) => x.at)) }))
  const grouped = new Set(groups.flatMap((g) => g.items.map((x) => x.key)))
  const singles = waiting.filter((x) => !grouped.has(x.key) && !visible(x.key)).sort((a, b) => b.at - a.at)
  return { groups: groups.sort((a, b) => b.at - a.at), latest: singles[0] as T | undefined, rest: singles.slice(1) }
}

// shownStatus：需求此刻该显示的状态——撤销期里（以及提交后等数据刷新的那一小会）显示操作后的状态，结果立刻到位。
export function shownStatus(status: string, pending?: { to?: string }) {
  return pending?.to || status
}

// moves：两次之间换了列的需求（两边都有、状态不同）；新出现 / 消失的不算（筛选、搜索、新建都不是换列）。
// local：这次换列是不是本机的操作带来的（撤销期开始、撤回、提交失败回滚）——这些不另加强调，元素上已有遮罩。
export function moves(prev: Map<string, string>, next: Map<string, string>, prevOver: Map<string, string>, nextOver: Map<string, string>) {
  const out: { code: string; from: string; to: string; local: boolean }[] = []
  for (const [code, to] of next) {
    const from = prev.get(code)
    if (from == null || from === to) continue
    out.push({ code, from, to, local: prevOver.get(code) !== nextOver.get(code) })
  }
  return out
}
