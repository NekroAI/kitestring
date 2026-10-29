// 提交成功后，等列表数据刷新到位再撤掉本地的「已处理」（R-266）。
// 撤销期结束、提交成功后，条目靠本地记录先离开「要你处理」（R-230）；这条本地记录原来固定 2.5 秒后撤掉，
// 网络慢、列表还没刷新回来时，撤掉的一刻条目按旧数据又弹回来，刷新到了再消失——就是「先消失、又弹回、再消失」。
// 现在本地记录至少留 2.5 秒，并且等手头的数据请求都回来（最多等 max 毫秒）再撤，旧数据不会再把它带回来。

export type SettleDeps = { busy: () => boolean; sleep: (ms: number) => Promise<void>; now: () => number }

export async function waitIdle(d: SettleDeps, max = 15_000, step = 100): Promise<boolean> {
  const end = d.now() + max
  while (d.busy()) {
    if (d.now() >= end) return false
    await d.sleep(step)
  }
  return true
}
