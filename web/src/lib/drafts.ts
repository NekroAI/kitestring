// 输入草稿（R-202，docs/principles.md 第 9 条「输入的内容不因关闭、翻页、刷新而丢失」）的纯逻辑：
// 存哪些、存多大、什么时候清。只存在本机浏览器（localStorage），界面与 hook 在 app/draft.tsx。
//
// 键（谁的草稿）：
//   req:R-12            需求抽屉底部的验收附言 / 退回意见 / 留言
//   edit:R-12:title     编辑需求标题 / 描述（detail）/ 验收标准（acceptance）
//   reason:R-12:back    退回 / 搁置原因弹窗（back / shelve）
//   dec:D-8             决策答复（原 ks.drafts.v2，按决策编号存）
//   override:D-8        推翻参谋代答
//   plan:P-3            计划批准 / 退回附言；stage:P-3:2 阶段验收附言
//   planedit:P-3        编辑计划；planedit:new 新建计划
//   reply:会话键         会话面板上「给它发消息」的输入框
//   newreq              新建需求

export type DraftData = Record<string, unknown>
export type DraftEntry = { at: number; d: DraftData } // at：最后一次改动的时间
export type DraftMap = Record<string, DraftEntry>

export const DRAFT_STORE = 'ks.drafts.v3'
export const LEGACY_STORE = 'ks.drafts.v2' // 决策答复的老草稿：{ [决策编号]: Draft }
export const MAX_TEXT = 20_000 // 单段文字最多存这么多字（界面里照常能写更长，只是存下来的截到这里）
export const MAX_BYTES = 1024 * 1024 // 总量上限；超出先清最久没动的
export const TTL_MS = 7 * 86_400_000 // 7 天没动的清掉

// hasContent：有没有用户写下的东西——非空文字、非空列表（贴图只存已上传文件的引用）、选了的选项（数字）。开关类（布尔）不算。
export function hasContent(v: unknown): boolean {
  if (typeof v === 'string') return v.trim() !== ''
  if (typeof v === 'number') return true
  if (Array.isArray(v)) return v.length > 0
  if (v && typeof v === 'object') return Object.values(v).some(hasContent)
  return false
}

// clip：存下来之前把每段文字截到 MAX_TEXT。
export function clip(v: unknown): unknown {
  if (typeof v === 'string') return v.length > MAX_TEXT ? v.slice(0, MAX_TEXT) : v
  if (Array.isArray(v)) return v.map(clip)
  if (v && typeof v === 'object') return Object.fromEntries(Object.entries(v).map(([k, x]) => [k, clip(x)]))
  return v
}

// localStorage 按 UTF-16 计，一个字两个字节
export const sizeOf = (m: DraftMap) => JSON.stringify(m).length * 2

// prune：去掉 7 天没动的。
export function prune(m: DraftMap, now: number): DraftMap {
  const out: DraftMap = {}
  for (const [k, e] of Object.entries(m)) if (e && typeof e.at === 'number' && now - e.at <= TTL_MS && e.d && typeof e.d === 'object') out[k] = e
  return out
}

// fit：总量超出上限时，从最久没动的开始清，直到放得下。keep 是正在写的那一条，最后才动它。
export function fit(m: DraftMap, max = MAX_BYTES, keep?: string): DraftMap {
  if (sizeOf(m) <= max) return m
  const out = { ...m }
  const order = Object.keys(out).sort((a, b) => Number(a === keep) - Number(b === keep) || out[a].at - out[b].at)
  for (const k of order) {
    if (sizeOf(out) <= max) break
    delete out[k]
  }
  return out
}

// serialize：存进 localStorage 的样子——截字、过期、限总量。
export function serialize(m: DraftMap, now: number, keep?: string): DraftMap {
  const clipped: DraftMap = {}
  for (const [k, e] of Object.entries(prune(m, now))) clipped[k] = { at: e.at, d: clip(e.d) as DraftData }
  return fit(clipped, MAX_BYTES, keep)
}

// migrate：把 ks.drafts.v2 的决策答复草稿搬到新格式（键 dec:决策编号），时间按搬过来的那一刻算。
export function migrate(legacy: unknown, now: number): DraftMap {
  const out: DraftMap = {}
  if (!legacy || typeof legacy !== 'object') return out
  for (const [code, d] of Object.entries(legacy as Record<string, unknown>)) {
    if (!d || typeof d !== 'object' || !hasContent(d)) continue
    out[`dec:${code}`] = { at: now, d: d as DraftData }
  }
  return out
}

// parse：读出来的东西不可信（手改、旧版本），坏的丢掉。
export function parse(raw: string | null): DraftMap {
  if (!raw) return {}
  try {
    const v = JSON.parse(raw)
    return v && typeof v === 'object' && !Array.isArray(v) ? (v as DraftMap) : {}
  } catch {
    return {}
  }
}

// ownerOf：草稿属于哪件事（需求 / 决策 / 计划编号，会话为 s:会话键，没有归属为空）——「草稿」记号和自动清理都按它找。
export function ownerOf(key: string): string {
  const [kind, ...rest] = key.split(':')
  switch (kind) {
    case 'req':
    case 'edit':
    case 'reason':
    case 'dec':
    case 'override':
    case 'plan':
    case 'stage':
    case 'planedit':
      return rest[0] === 'new' ? '' : (rest[0] ?? '')
    case 'reply':
      return `s:${rest.join(':')}`
    default:
      return ''
  }
}

// Closed：对象关了没有。at 是关闭的时间（需求的最后更新时间）：比它更晚写的草稿不清（例如在已完成的需求上留言）；
// 没有 at 表示这个输入框已经不会再出现（决策已答复、计划已批准），草稿直接清。
export type Closed = (key: string) => { at?: number } | undefined

// sweep：清掉对象已关闭（已完成 / 已答复 / 已搁置）的草稿，返回要清的键。
export function sweep(m: DraftMap, closed: Closed): string[] {
  const out: string[] = []
  for (const [k, e] of Object.entries(m)) {
    const c = closed(k)
    if (c && (c.at == null || e.at <= c.at)) out.push(k)
  }
  return out
}
