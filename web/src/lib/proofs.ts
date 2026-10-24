import type { Block, BlockItem, Evidence, Requirement } from '../api/client'
import { isImage } from './files.ts'

// 证据整理（R-195）：把一份证据里的截图、改动框和验收清单对上号——
// 抽屉里验收点旁边的「证据小图」、点开后的大切片、看图右栏的「这张图证明了哪几条验收点」都从这里取。
// 只放纯计算，不碰 React，单测直接跑（lib/proofs.test.ts）。

export type Box = {
  x: number
  y: number
  w: number
  h: number
  label?: string | null
  criterion?: number | null
}
export type ShotFile = {
  id: string
  name: string
  mime: string
  size: number
  title?: string | null
  boxes?: Box[] | null
}

// Shot：证据里的一张图。figure 块、compare 块（改后为主图，改前放 before）、没进块的附图，按 Agent 给的顺序。
// named：有没有真正的标题（没有时界面用文件名兜底，字体换成等宽）。
export type Shot = {
  id: string
  file: ShotFile
  before?: ShotFile
  boxes: Box[]
  named: boolean
}
// Proof：证明某条验收点的一处：第 si 张图的第 bi 个框；bi 为 null 表示整张图（清单项的 see 指向它，但没有框）。
export type Proof = { si: number; bi: number | null }
// Cite：看图右栏的一条「这张图证明」：第 k 条清单项（编号 n），点它放大到第 bi 个框。
export type Cite = {
  k: number
  n: number
  text: string
  result?: string
  bi: number | null
}

// blockTitle：截图 / 对比块的标题：块的 title，没有时用图注，再没有用图片自己的标题。
function blockTitle(b: Block, f: ShotFile) {
  return b.title?.trim() || b.caption?.trim() || f.title?.trim() || ''
}

// shotsOf：一份证据的全部截图。块里的图在前（按块的顺序），没进块的附图在后（按交的顺序）；不是图片 / 录屏的附件不算。
export function shotsOf(e: Pick<Evidence, 'payload' | 'files'>): Shot[] {
  const out: Shot[] = []
  const used = new Set<string>()
  for (const b of e.payload?.blocks ?? []) {
    for (const it of b.items ?? []) if (it.image) used.add(it.image.id)
    if (b.type === 'compare' && b.after) {
      used.add(b.after.id)
      if (b.before) used.add(b.before.id)
      const title = blockTitle(b, b.after)
      out.push({
        id: b.id,
        file: { ...b.after, title },
        before: b.before ? { ...b.before, title, boxes: null } : undefined,
        boxes: b.after.boxes ?? [],
        named: !!title,
      })
    } else if (b.type === 'figure' && b.image) {
      used.add(b.image.id)
      const title = blockTitle(b, b.image)
      out.push({
        id: b.id,
        file: { ...b.image, title },
        boxes: b.image.boxes ?? [],
        named: !!title,
      })
    }
  }
  for (const f of e.files ?? []) {
    if (used.has(f.id) || !isImage(f.mime)) continue
    const title = f.title?.trim() || ''
    out.push({
      id: 'file-' + f.id,
      file: { ...f, title },
      boxes: f.boxes ?? [],
      named: !!title,
    })
  }
  return out
}

// otherFiles：没进截图的附件（录屏附件、文档等），照常列在图库后面。
export function otherFiles(e: Pick<Evidence, 'payload' | 'files'>, shots: Shot[]) {
  const used = new Set(shots.flatMap((s) => [s.file.id, s.before?.id]))
  return (e.files ?? []).filter((f) => !used.has(f.id) && !isImage(f.mime))
}

export function checklistOf(e: Pick<Evidence, 'payload'>): BlockItem[] {
  return e.payload?.blocks?.find((b) => b.type === 'checklist')?.items ?? []
}

// itemNo：清单项的编号：写了 criterion 用它，否则按顺序。
export const itemNo = (it: BlockItem, k: number) => it.criterion || k + 1

// leadNum：标注文字开头的编号（「3 泳道加图标」→ 3），老证据靠它把框对应到验收点；没有编号时为 0。
export function leadNum(label?: string | null) {
  const m = /^\s*(\d{1,2})(?=[\s.、:：)）]|$)/.exec(label ?? '')
  return m ? Number(m[1]) : 0
}

// explicitMode：这份证据有没有框写了 criterion（R-195 之后的新写法）。写了就只认 criterion，不再猜标注开头的编号。
export const explicitMode = (shots: Shot[]) => shots.some((s) => s.boxes.some((b) => !!b.criterion))

// boxNo：框对应的验收点编号：新写法看 criterion，老证据按标注文字开头的编号临时对应；对不上为 0。
export const boxNo = (b: Box, explicit: boolean) => (explicit ? b.criterion || 0 : leadNum(b.label))

// boxLabel：标注的文字。老证据靠开头的编号对应验收点，显示时去掉这个编号（列表前面已有框的序号）。
export function boxLabel(b: Box, explicit: boolean) {
  const s = (b.label ?? '').trim()
  return explicit ? s : s.replace(/^\s*\d{1,2}(?:[\s.、:：)）]+|$)/, '').trim() || s
}

// proofsOf：每条验收点 → 证明它的框；一个框都对不上时，退回到清单项 see 指向的那张图（整张）。
export function proofsOf(shots: Shot[], items: BlockItem[]): Proof[][] {
  const explicit = explicitMode(shots)
  return items.map((it, k) => {
    const n = itemNo(it, k)
    const ps: Proof[] = []
    shots.forEach((s, si) => s.boxes.forEach((b, bi) => boxNo(b, explicit) === n && ps.push({ si, bi })))
    if (!ps.length && it.see) {
      const si = shots.findIndex((s) => s.id === it.see)
      if (si >= 0) ps.push({ si, bi: null })
    }
    return ps
  })
}

// citesOf：第 si 张图证明了哪几条验收点（看图右栏），每条带它在这张图上的第一个框。
export function citesOf(si: number, items: BlockItem[], proofs: Proof[][], criteria?: Record<number, string>): Cite[] {
  const out: Cite[] = []
  items.forEach((it, k) => {
    const p = proofs[k]?.find((q) => q.si === si)
    if (!p) return
    const n = itemNo(it, k)
    out.push({
      k,
      n,
      text: it.text || criteria?.[n] || '',
      result: it.result,
      bi: p.bi,
    })
  })
  return out
}

// ---------- 切片与放大的几何 ----------

type Size = { w: number; h: number }
export type CropLayout = {
  imgW: number
  left: number
  top: number
  ol?: { left: number; top: number; w: number; h: number }
}

const clampOffset = (off: number, img: number, view: number) => (img <= view ? (view - img) / 2 : Math.min(0, Math.max(view - img, off)))

// cropLayout：在 view 大小的窗口里显示 nat 大小的图上的 box（比例坐标）：框加一圈边距、按窗口比例补齐后铺满窗口。
// legible（验收点展开后的大切片）：至少按「看得清」的比例放大（1 个图片像素不小于 0.62 个屏幕像素），
// 框太宽 / 太高放不下时从框的开头（左上）显示。图不会被拖出窗口边缘。没有框时按宽铺满、从顶部显示。
export function cropLayout(box: Box | null | undefined, nat: Size, view: Size, legible = false): CropLayout {
  const { w: W, h: H } = view
  const { w: nw, h: nh } = nat
  if (!nw || !nh || !W || !H) return { imgW: W, left: 0, top: 0 }
  if (!box) return { imgW: W, left: 0, top: 0 }
  const { x, y, w, h } = box
  let cw = Math.max(w * 1.25, 0.12)
  let ch = Math.max(h * 1.25, ((0.12 * nw) / nh) * (H / W))
  if ((cw * nw) / (ch * nh) > W / H) ch = (cw * nw * (H / W)) / nh
  else cw = (ch * nh * (W / H)) / nw
  cw = Math.min(cw, 1)
  let scale = W / (cw * nw)
  if (legible) scale = Math.max(scale, 0.62)
  const vw = W / (nw * scale)
  const vh = H / (nh * scale)
  const cx = w <= vw * 0.9 ? x + w / 2 : x - vw * 0.04 + vw / 2
  const cy = h <= vh * 0.9 ? y + h / 2 : y - vh * 0.06 + vh / 2
  const imgW = nw * scale
  const imgH = nh * scale
  const left = clampOffset(-(cx * imgW - W / 2), imgW, W)
  const top = clampOffset(-(cy * imgH - H / 2), imgH, H)
  return {
    imgW,
    left,
    top,
    ol: {
      left: left + x * imgW,
      top: top + y * imgH,
      w: w * imgW,
      h: h * imgH,
    },
  }
}

// focusView：看图时放大到一个框。disp 是图在舞台上按适配缩放后的大小，stage 是舞台大小，natW 是原图宽。
// 返回看图器的视图：s 倍放大，(x, y) 是图中心相对舞台中心的位移。框占舞台六成左右，最多 4 倍；
// 框很宽（如横跨整屏的简报条）时按「看得清」放大，从框的开头显示。
export function focusView(box: Box, disp: Size, stage: Size, natW: number) {
  const { w: dw, h: dh } = disp
  const { w: W, h: H } = stage
  let s = Math.min(4, Math.max(1, Math.min((W * 0.6) / (box.w * dw), (H * 0.6) / (box.h * dh))))
  s = Math.max(s, Math.min(4, (0.62 * natW) / dw))
  const vw = W / s
  const vh = H / s
  const tx = box.w * dw <= vw * 0.9 ? (box.x + box.w / 2) * dw : box.x * dw - vw * 0.04 + vw / 2
  const ty = box.h * dh <= vh * 0.9 ? (box.y + box.h / 2) * dh : box.y * dh - vh * 0.06 + vh / 2
  return { s, x: -(tx - dw / 2) * s, y: -(ty - dh / 2) * s }
}

// ---------- 时间线 ----------

// roundsOf：按退回意见分轮（每次退回开启新的一轮）。
export function roundsOf(r: Pick<Requirement, 'feedback'>) {
  const revises = [...(r.feedback ?? [])].filter((f) => f.kind === 'feedback').sort((a, b) => (a.answered_at ?? a.created_at).localeCompare(b.answered_at ?? b.created_at))
  const bounds = revises.map((f) => f.answered_at ?? f.created_at)
  const roundOf = (at: string) => 1 + bounds.filter((b) => b <= at).length
  return { revises, roundOf, current: revises.length + 1 }
}

// latestEvidence：默认展开的那份证据——本轮最新、没作废的一份。同一轮交了好几份不当成替换：其余照常收成一行。
// 本轮还没有证据（刚被退回）时为空。
export function latestEvidence(r: Pick<Requirement, 'feedback' | 'evidence'>) {
  const { roundOf, current } = roundsOf(r)
  const cur = (r.evidence ?? []).filter((e) => roundOf(e.created_at) === current && !e.voided)
  return cur[cur.length - 1]?.id
}

// splitLegacy：老格式证据（一段文字）的第一句当结论显示，其余是正文。
export function splitLegacy(summary: string) {
  const all = (summary ?? '').trim()
  const m = /^[^\n]*?[。；！？](?=.)/.exec(all)
  const first = m ? m[0] : all.split('\n')[0]
  return {
    first: first.replace(/^#+\s*/, ''),
    rest: all.slice(first.length).replace(/^\s*\n/, ''),
  }
}

// ---------- 谁做的 ----------

// ActorView：一条记录是谁做的。session：某个会话（显示身份胶囊 + 会话标题，点开是那个会话）；
// unknown：Agent 做的但没记录是哪个会话（上线前的老数据）。
export type ActorView = { kind: 'user' } | { kind: 'system' } | { kind: 'session'; key: string; role?: string; title?: string } | { kind: 'unknown'; role?: string }

export function actorOf(x: { actor?: string | null; session?: string | null; role?: string | null; title?: string | null }): ActorView {
  if (x.actor === 'user') return { kind: 'user' }
  if (x.actor === 'system') return { kind: 'system' }
  if (x.session)
    return {
      kind: 'session',
      key: x.session,
      role: x.role || undefined,
      title: x.title || undefined,
    }
  return { kind: 'unknown', role: x.role || undefined }
}
