import { useContext, useEffect, useLayoutEffect, useRef, useState, useSyncExternalStore, type KeyboardEvent, type ReactNode, type RefObject } from 'react'
import { createPortal } from 'react-dom'
import { Check, ChevronDown, CornerDownLeft, Undo2 } from 'lucide-react'
import { AnimatePresence, motion, useIsPresent } from 'motion/react'
import { t } from '../i18n'
import type { Scene } from '../i18n/zh'
import { cx } from '../lib/util'
import { dockView } from '../lib/pending'
import { secsAt } from '../lib/undo'
import { advanceStep } from '../lib/advance'
import { Button, Textarea } from '../ui/base'
import { AttachZone, useUploading, type FileRef } from '../ui/attach'
import { Leaving, reducedMotion as reduced, spring } from '../ui/gesture'
import { useActions } from './actions'

// 统一的判断动作（R-161）：肯定 / 否定 / 回答 / 撤销在全站只有一种样式、一种节奏。
// - 用词按场景（R-176）：验收「通过 / 退回」、计划「批准 / 退回」、批准类决策和请求「批准 / 拒绝」、选项类决策「回答」；
//   各入口把场景（Scene）传进来，按钮、撤销条、印章、快捷键提示都按场景取词（i18n/zh.ts 的 VERB），行为不随场景变。
// - Verdict：[附言（否定时必填）][否定][肯定]，决策是 [附言][回答]；放在卡片底部右侧，各处同一顺序、颜色、图标。
//
// 撤销期（R-201，docs/principles.md 第 8 条「撤销期不挡用户的下一步，但系统替用户做的下一步要等它结束」）：全站只有这一个调度器（schedule），需求验收、决策答复（outbox）、
// 计划 / 阶段、改需求状态、开始计划、撤销授权、撤回放行都走它；计时、提交、盖印、页面关闭时立即提交都只在这里写一次。
// - 结果立刻到位：排上后界面马上显示操作后的样子（需求进新列，见 lib/pending 的 shownStatus），元素上盖同一个撤销条（UndoBar）：
//   行内摆法（PendingBar，卡片底部的判断行换成它）和遮罩摆法（PendingCover，盖在需求块上）；不锁界面、不排队，用户可以马上做下一件事。
// - 元素看不见时（翻到下一件、列内滚走、手机上不在当前列）撤回挪到顶部浮动条（UndoDock）；批量只出一条汇总。
// - 到点：盖印（Seal：印章盖下 → 停顿 SEAL_MS）→ 提交 → 卡片收起 / 留在新列；失败时元素回到原处（带同一套换列动画），错误由 useActions 提示。
// - 连续审批（useAdvance，R-226）：下了判断后停在原件、盖着撤销遮罩，撤销期走完盖印后才自动翻到下一件；撤回则不翻；撤销期内自己翻走会取消这次自动跳转。
// - 撤销期的状态放在模块里，离开页面（切走路由、标签页隐藏）照常计时、到点提交；页面真的关闭或刷新时用 keepalive 立即提交，不丢。

// 撤销时长（D-229：3 秒，全站一致）。
export const UNDO_MS = 3000
// 生效反馈的印章（D-229：保留并与卡片收起连成一段）；关掉时只做收起。
export const SHOW_STAMP = true
// 印章盖下后停顿多久再提交、收起卡片；连续审批时也是盖印后翻到下一件的时长。系统开了减少动效时不盖印、不停顿。
export const SEAL_MS = 700
// 提交成功后保持「已盖印」多久：等列表刷新把卡片收起 / 需求块留在新列，避免先闪回原样（全站只在这里定义）
export const SEALED_HOLD_MS = 2500

export const reducedMotion = reduced
const sleep = (ms: number) => new Promise((r) => window.setTimeout(r, ms))

// move：带文字的一般动作（改为进行中、开始计划、撤销授权、撤回放行），没有印章
export type Act = 'pass' | 'back' | 'answer' | 'move'
// Phase：wait 撤销期（可撤销）→ seal 盖印中（不能撤销）→ done 已提交、等卡片收起
export type Phase = 'wait' | 'seal' | 'done'

// Beacon：页面关闭时用来立即提交的请求（fetch keepalive，cookie 同源带上）。
export type Beacon = { method: 'POST' | 'PUT'; url: string; body: unknown }

export type Entry = {
  key: string
  at: number // 排上的时间（浮动条按它找最近一件）
  until: number
  start: number // 排上那一刻的 performance.now()：遮罩圆环、淡入都从这里算起（R-218）
  act: Act
  scene: Scene
  label: string
  subject: string // 浮动条里写在前面的对象（R-12、P-3）
  to?: string // 需求改到的状态：撤销期里需求池按它显示（结果立刻到位）
  group?: string // 批量操作：同一组只出一条浮动条
  reopen?: () => void // 浮动条里撤回时先回到这一件
  phase: Phase
  timer: number
  commit: () => Promise<boolean>
  beacon?: Beacon
}

const pending = new Map<string, Entry>()
const subs = new Set<() => void>()
let ver = 0
const emit = () => {
  ver++
  subs.forEach((f) => f())
}

export function usePendingMap() {
  useSyncExternalStore(
    (f) => {
      subs.add(f)
      return () => subs.delete(f)
    },
    () => ver,
  )
  return pending
}

// usePending：某件事是否在撤销期 / 盖印中（key 例如需求编号 R-12、plan:P-3、stage:P-3:2、dec:D-8、grant:G-2、release:R-12）。
export function usePending(key: string | undefined) {
  const m = usePendingMap()
  return key ? m.get(key) : undefined
}

// actWord：撤销条、印章里的动词，按场景取——通过 / 批准 / 退回 / 拒绝 / 回答，一般动作是它自己的文字（改为进行中）。
export const actWord = (act: Act, label = '', scene: Scene = 'accept') =>
  act === 'pass' ? t.verdict.pass(scene) : act === 'back' ? t.verdict.back(scene) : act === 'answer' ? t.verdict.pass('answer') : label

export type ScheduleOpts = {
  act: Act
  scene?: Scene // 用词场景（R-176），不传按需求验收
  commit: () => Promise<boolean> // 返回是否成功；失败时元素回到原样
  label?: string // act = move 时的动作文字（改为进行中）
  subject?: string // 浮动条里写在前面的对象（R-12）
  to?: string
  group?: string
  reopen?: () => void
  beacon?: Beacon
}

// schedule：进入撤销期；同一件事已在撤销期里时不重复排。
export function schedule(key: string, o: ScheduleOpts) {
  if (pending.has(key)) return false
  const at = Date.now()
  const until = at + UNDO_MS
  const start = performance.now()
  const timer = window.setTimeout(() => void fire(key), UNDO_MS)
  pending.set(key, { key, at, until, start, act: o.act, scene: o.scene ?? 'accept', label: o.label ?? '', subject: o.subject ?? key, to: o.to, group: o.group, reopen: o.reopen, phase: 'wait', timer, commit: o.commit, beacon: o.beacon })
  emit()
  return true
}

const patch = (key: string, p: Partial<Entry>) => {
  const e = pending.get(key)
  if (!e) return
  pending.set(key, { ...e, ...p })
  emit()
}

async function fire(key: string) {
  const e = pending.get(key)
  if (!e || e.phase !== 'wait') return
  // 一般动作没有印章，直接提交；其它先盖印、停顿片刻
  const seal = e.act !== 'move' && SHOW_STAMP && !reducedMotion()
  patch(key, { phase: 'seal' })
  if (seal) await sleep(SEAL_MS)
  const ok = await e.commit().catch(() => false)
  if (!ok) {
    pending.delete(key)
    return emit()
  }
  patch(key, { phase: 'done' })
  window.setTimeout(() => {
    if (pending.get(key)?.phase === 'done') {
      pending.delete(key)
      emit()
    }
  }, SEALED_HOLD_MS)
}

export function undo(key: string) {
  const e = pending.get(key)
  if (!e || e.phase !== 'wait') return false
  window.clearTimeout(e.timer)
  pending.delete(key)
  emit()
  return true
}

// scheduleMany：批量操作（全部通过等）——每件各自进撤销期（元素上各盖遮罩），浮动条只出一条「n 项 · 全部撤回」。
let groupSeq = 0
export function scheduleMany(items: ({ key: string } & ScheduleOpts)[]) {
  const group = `g${++groupSeq}`
  return items.filter((x) => schedule(x.key, { ...x, group })).length
}

// 页面真的关闭或刷新时：撤销期 / 盖印中的事立即提交，不丢（标签页只是隐藏时照常计时）。
export function sendBeacon(b: Beacon) {
  try {
    void fetch(b.url, { method: b.method, body: JSON.stringify(b.body), headers: { 'Content-Type': 'application/json' }, credentials: 'same-origin', keepalive: true })
  } catch {
    /* 页面正在关闭，失败也没有地方报 */
  }
}
if (typeof window !== 'undefined')
  window.addEventListener('pagehide', () => {
    for (const [key, e] of [...pending.entries()]) {
      if (e.phase === 'done') continue
      window.clearTimeout(e.timer)
      pending.delete(key)
      if (e.beacon) sendBeacon(e.beacon)
      else void e.commit()
    }
  })

// ---------- 连续审批 ----------

// useAdvance：连续审批（需求抽屉的连续验收、审批抽屉）——下了判断后停在这一件，盖着撤销遮罩；撤销期走完（进入盖印）才自动翻到下一件（R-226），
// 盖印那一下（SEAL_MS）之后滑走。撤销期内撤回：恢复原状、不翻；用户自己翻走（→ / J / 下一件）：立刻生效、自动跳转取消，不会多翻一件，
// 这时撤回在顶部浮动条里，点了先回到这一件（reopen）再撤销。时机判断在 lib/advance.ts；计时仍只有 schedule 这一套，这里不另起计时。
// 一般动作（改状态等，act = move）不翻页；only 限定只在某种判断后翻（需求抽屉只在通过后翻，退回留在原处）。
export function useAdvance(key: string | undefined, on: boolean, advance: () => void, reopen?: () => void, only?: Act) {
  const e = usePending(key)
  const hit = !!e && e.act !== 'move' && (!only || e.act === only)
  const armed = useRef(0)
  const timer = useRef(0)
  const go = useRef(advance)
  go.current = advance
  const phase = hit ? e.phase : undefined
  const at = hit ? e.at : 0
  useEffect(() => {
    const r = advanceStep(armed.current, phase && key ? { at, phase } : undefined, on && !!key)
    if (phase === 'wait' && key && r.armed !== armed.current) patch(key, { reopen })
    armed.current = r.armed
    if (r.fire) {
      timer.current = window.setTimeout(() => {
        timer.current = 0
        go.current()
      }, SHOW_STAMP && !reducedMotion() ? SEAL_MS : 0)
    } else if (!phase && timer.current) {
      // 盖印那一下里失败回滚了：不翻
      window.clearTimeout(timer.current)
      timer.current = 0
    }
  }, [key, on, phase, at]) // eslint-disable-line react-hooks/exhaustive-deps
  // 换了一件（用户自己翻走）或离开：这次自动跳转取消
  useEffect(
    () => () => {
      armed.current = 0
      window.clearTimeout(timer.current)
      timer.current = 0
    },
    [key, on],
  )
}

// ---------- 锚点：元素看不看得见 ----------

// 每件事在视口里的撤销条 / 遮罩；都看不见（或都被抽屉、弹窗盖住）时撤回挪到顶部浮动条。
const anchorEls = new Map<string, Set<HTMLElement>>()
const bump = (key: string, el: HTMLElement, on: boolean) => {
  const set = anchorEls.get(key) ?? new Set<HTMLElement>()
  if (on) set.add(el)
  else set.delete(el)
  if (set.size) anchorEls.set(key, set)
  else anchorEls.delete(key)
  emit()
}
// covered：开着抽屉 / 弹窗（模态）时，不在它里面的元素点不到——算看不见（R-218：手机上审批抽屉盖住了列表，撤回要到浮动条）
const covered = (el: HTMLElement) => {
  const open = document.querySelectorAll('[role="dialog"][data-state="open"], [role="alertdialog"][data-state="open"]')
  return open.length > 0 && ![...open].some((d) => d.contains(el))
}
const anchors = { has: (key: string) => [...(anchorEls.get(key) ?? [])].some((el) => !covered(el)) }

// useUndoAnchor：把撤销条 / 遮罩登记为这件事的锚点；元素进出视口（含列内滚动、抽屉翻走）时更新。
// 正在退场的元素（抽屉翻页时滑走的上一件）不算：它马上就不在了，撤回应当立刻挪到浮动条。
export function useUndoAnchor(k: string | undefined) {
  const present = useIsPresent() && !useContext(Leaving)
  const key = present ? k : undefined
  const [el, setEl] = useState<HTMLElement | null>(null)
  useEffect(() => {
    if (!el || !key || typeof IntersectionObserver === 'undefined') return
    let vis = false
    const io = new IntersectionObserver(
      ([x]) => {
        const v = x.isIntersecting && x.intersectionRatio >= 0.5
        if (v !== vis) {
          vis = v
          bump(key, el, v)
        }
      },
      { threshold: [0, 0.5, 1] },
    )
    io.observe(el)
    return () => {
      io.disconnect()
      if (vis) bump(key, el, false)
    }
  }, [el, key])
  return setEl
}

// ---------- 撤销遮罩（R-218，D-539 定稿：圆环 + 原句 + 半透明遮罩）----------
// 全站只有这一套：整块盖一层半透明遮罩（隐约看得到底下操作后的样子），左边原句「3 秒后改为待开始」，
// 右边撤回按钮占满高度，按钮里一圈倒计时圆环。三种摆法：盖在块上（PendingCover：需求池、要你处理、列表行、审批抽屉底部判断栏）、
// 行内一条（PendingBar：没有可盖的块时）、顶部浮动条（UndoDock，当一张小卡）。
// 进度不靠 React：调度器记下操作那一刻的 performance.now()（start），圆环用 WAAPI 动画并把 startTime 设成 start——
// 重新渲染、换列、翻页重新挂载都从同一处接着走，不会重来，也不会被打断；秒数只改文字节点，不碰动画元素（R-218 的根因：
// 原来每 250ms 重渲染一次，按 Date.now() 重写正在跑的 CSS 动画的 delay，进度被一截截往前推，一顿一顿、提前走完）。
// 系统开了减少动态效果时圆环是静止的整圈，只有秒数在变。

const RING_R = 9
const RING_C = 2 * Math.PI * RING_R

// 圆环进度：startTime = 这件事进撤销期的那一刻
function runRing(el: SVGCircleElement, start: number) {
  if (reducedMotion() || typeof el.animate !== 'function') return null
  const a = el.animate([{ strokeDashoffset: 0 }, { strokeDashoffset: RING_C }], { duration: UNDO_MS, fill: 'forwards', easing: 'linear' })
  a.startTime = start
  return a
}

// 淡入也从那一刻算起：换列 / 翻页重新挂载时不再闪一下
function runFade(el: HTMLElement, start: number) {
  if (reducedMotion() || typeof el.animate !== 'function') return null
  const a = el.animate([{ opacity: 0 }, { opacity: 1 }], { duration: 180, fill: 'both', easing: 'ease-out' })
  a.startTime = start
  return a
}

// hydrateUndo：复制出来的遮罩（需求换列时飞行的复制块，R-200）也接上同一个起点
export function hydrateUndo(root: HTMLElement) {
  root.querySelectorAll<SVGCircleElement>('circle[data-ring]').forEach((c) => runRing(c, Number(c.dataset.ring)))
}

function Ring({ start }: { start: number }) {
  const ref = useRef<SVGCircleElement>(null)
  useLayoutEffect(() => {
    const a = ref.current && runRing(ref.current, start)
    return () => a?.cancel()
  }, [start])
  return (
    <svg viewBox="0 0 24 24" aria-hidden className="size-[22px] shrink-0 -rotate-90">
      <circle cx="12" cy="12" r={RING_R} fill="none" strokeWidth="2.25" className="stroke-accent/20" />
      <circle ref={ref} data-ring={start} cx="12" cy="12" r={RING_R} fill="none" strokeWidth="2.25" strokeLinecap="round" strokeDasharray={RING_C} className="stroke-accent" />
    </svg>
  )
}

const secsLeft = (start: number) => String(secsAt(start, performance.now(), UNDO_MS))

// Secs：剩几秒——自己改自己的文字节点，不让外面重渲染
function Secs({ start }: { start: number }) {
  const ref = useRef<HTMLSpanElement>(null)
  useEffect(() => {
    const id = window.setInterval(() => {
      const el = ref.current
      const v = secsLeft(start)
      if (el && el.textContent !== v) el.textContent = v
    }, 100)
    return () => window.clearInterval(id)
  }, [start])
  return (
    <span ref={ref} className="tabular">
      {secsLeft(start)}
    </span>
  )
}

const MARK = '⁣'
// 原句「3 秒后改为待开始」：数字那一处换成 Secs
function Sentence({ e, prefix }: { e: Entry; prefix?: string }) {
  const word = actWord(e.act, e.label, e.scene)
  if (e.phase !== 'wait') return <>{(prefix ? `${prefix} · ` : '') + t.verdict.sealed(word)}</>
  const [a, b] = t.verdict.pending(word, MARK).split(MARK)
  return (
    <>
      {prefix ? `${prefix} · ` : ''}
      {a}
      <Secs start={e.start} />
      {b}
    </>
  )
}

type VeilProps = { e: Entry; start?: number; text?: ReactNode; prefix?: string; onUndo: () => void; undoText?: string; extra?: ReactNode }

// Veil：遮罩的内容——左边原句，右边满高的撤回按钮（圆环 + 「撤销」）
function Veil({ e, start, text, prefix, onUndo, undoText, extra }: VeilProps) {
  const wait = e.phase === 'wait'
  return (
    <>
      <span role="status" className="flex min-w-0 flex-1 items-center gap-2 px-3 text-[13px] font-medium text-fg">
        <span className="min-w-0 truncate">{text ?? <Sentence e={e} prefix={prefix} />}</span>
        {extra}
      </span>
      {wait && (
        <button
          type="button"
          onClick={(ev) => {
            ev.stopPropagation()
            onUndo()
          }}
          onPointerDown={(ev) => ev.stopPropagation()}
          aria-label={undoText ?? t.verdict.undo}
          className="flex w-16 shrink-0 flex-col items-center justify-center gap-0.5 border-l border-accent/25 bg-surface/55 text-[12px] font-semibold text-accent transition-colors hover:bg-accent-soft active:bg-accent/15"
        >
          <Ring start={start ?? e.start} />
          <span className="leading-4 whitespace-nowrap">{undoText ?? t.verdict.undo}</span>
        </button>
      )}
    </>
  )
}

// UndoBar：没有可盖的块时的一条（行内、浮动条）。和遮罩同一个样子：半透明底、左原句、右满高撤回。
export function UndoBar({ className, floating, ...p }: VeilProps & { className?: string; floating?: boolean }) {
  const ref = useRef<HTMLDivElement>(null)
  const start = p.start ?? p.e.start
  useLayoutEffect(() => {
    const a = ref.current && runFade(ref.current, start)
    return () => a?.cancel()
  }, [start])
  return (
    <div
      ref={ref}
      data-undo=""
      className={cx(
        'relative flex min-h-12 items-stretch overflow-hidden rounded-xl border border-accent/30 bg-accent-soft/80 pointer-coarse:min-h-14',
        floating && 'w-[min(380px,calc(100vw-2rem))] bg-surface/95 shadow-pop backdrop-blur-sm',
        className,
      )}
    >
      <Veil {...p} />
    </div>
  )
}

// PendingBar：某件事在撤销期时，卡片里（行内）显示的撤销条；盖印后变成不能撤销的「已通过」「已拒绝」。
export function PendingBar({ k, className, prefix }: { k: string; className?: string; prefix?: string }) {
  const e = usePending(k)
  const ref = useUndoAnchor(e?.phase === 'wait' ? k : undefined)
  if (!e) return null
  return (
    <div ref={ref} className={className}>
      <UndoBar e={e} prefix={prefix} onUndo={() => undo(k)} />
    </div>
  )
}

// PendingCover：盖在块上（需求池的块、要你处理的条目、列表行、审批抽屉底部的判断栏）：块已经是操作后的样子，
// 上面一层半透明遮罩 + 原句 + 满高撤回。父元素需要 relative。只盖撤销期，盖印 / 提交后淡出。
export function PendingCover({ k, className }: { k: string; className?: string }) {
  const e = usePending(k)
  const on = e?.phase === 'wait'
  const anchor = useUndoAnchor(on ? k : undefined)
  const fade = useRef<Animation | null>(null)
  const ref = (el: HTMLDivElement | null) => {
    anchor(el)
    fade.current?.cancel()
    fade.current = el && e ? runFade(el, e.start) : null
  }
  return (
    <AnimatePresence>
      {on && e && (
        <motion.div
          key={e.start}
          ref={ref}
          data-cover=""
          initial={false}
          exit={{ opacity: 0 }}
          transition={{ duration: 0.18 }}
          className={cx('absolute inset-0 z-10 flex items-stretch overflow-hidden rounded-[inherit] bg-surface/65 backdrop-blur-[1.5px]', className)}
        >
          <Veil e={e} onUndo={() => undo(k)} />
        </motion.div>
      )}
    </AnimatePresence>
  )
}

// ---------- 顶部浮动条 ----------

// 元素刚换位置（进新列、抽屉翻页）时锚点要过一小会才登记上，期间不出浮动条，免得闪一下
const LOST_MS = 120
const lostAt = new Map<string, number>()

// UndoDock：受影响的元素不在屏幕上时，撤回出现在顶部浮动条（docs/principles.md 第 8 条）。
// 只显示最近一件，旁边「还有 n 件在等生效」可展开逐件撤回；批量操作只出一条「n 项 · 全部撤回」。全站只挂一个（main.tsx）。
export function UndoDock() {
  const map = usePendingMap()
  const [, tick] = useState(0)
  const [open, setOpen] = useState(false)
  const now = Date.now()
  const all = [...map.values()]
  for (const k of [...lostAt.keys()]) if (!map.has(k) || anchors.has(k)) lostAt.delete(k)
  for (const e of all) if (e.phase === 'wait' && !anchors.has(e.key) && !lostAt.has(e.key)) lostAt.set(e.key, now)
  const settled = (k: string) => anchors.has(k) || now - (lostAt.get(k) ?? now) < LOST_MS
  const v = dockView(all, settled)
  const soon = all.some((e) => e.phase === 'wait' && !anchors.has(e.key) && now - (lostAt.get(e.key) ?? now) < LOST_MS)
  useEffect(() => {
    if (!soon) return
    const id = window.setTimeout(() => tick((n) => n + 1), LOST_MS)
    return () => window.clearTimeout(id)
  })
  useEffect(() => {
    if (!v.rest.length) setOpen(false)
  }, [v.rest.length])
  // 抽屉、弹窗开关时重新看一遍哪些撤回被盖住了（有事在等生效时才看）
  const waiting = all.some((e) => e.phase === 'wait')
  useEffect(() => {
    if (!waiting || typeof MutationObserver === 'undefined') return
    let raf = 0
    const mo = new MutationObserver(() => {
      if (!raf) raf = requestAnimationFrame(() => ((raf = 0), tick((n) => n + 1)))
    })
    mo.observe(document.body, { childList: true, subtree: true, attributes: true, attributeFilter: ['data-state'] })
    return () => (mo.disconnect(), cancelAnimationFrame(raf))
  }, [waiting])
  const back = (e: Entry) => {
    e.reopen?.()
    undo(e.key)
  }
  // 挂在 body 上、压在抽屉和遮罩之上：审批抽屉里翻到下一件后，撤回就在这里。
  // 桌面右侧抽屉开着时居中在抽屉左边的空处，不挡住抽屉顶栏的翻页按钮
  const panel = typeof document !== 'undefined' ? document.querySelector('.panel-slide')?.getBoundingClientRect() : undefined
  const right = panel && panel.left > 420 ? window.innerWidth - panel.left : 0
  return createPortal(
    <div data-undo-dock="" style={right ? { right } : undefined} className="pointer-events-none fixed inset-x-0 top-3 z-[70] flex flex-col items-center gap-2 px-4">
      <AnimatePresence initial={false}>
        {v.groups.map((g) => (
          <motion.div key={g.id} layout initial={{ opacity: 0, y: -8 }} animate={{ opacity: 1, y: 0 }} exit={{ opacity: 0, y: -8 }} transition={{ duration: 0.18 }} className="pointer-events-auto">
            <UndoBar
              floating
              e={g.items[0]}
              start={Math.max(...g.items.map((x) => x.start))}
              undoText={t.verdict.undoAll}
              text={<GroupText items={g.items} />}
              onUndo={() => g.items.forEach((x) => undo(x.key))}
            />
          </motion.div>
        ))}
        {v.latest && (
          <motion.div key="latest" layout initial={{ opacity: 0, y: -8 }} animate={{ opacity: 1, y: 0 }} exit={{ opacity: 0, y: -8 }} transition={{ duration: 0.18 }} className="pointer-events-auto flex flex-col items-center gap-1.5">
            <UndoBar
              floating
              e={v.latest}
              prefix={v.latest.subject}
              onUndo={() => back(v.latest!)}
              extra={
                v.rest.length > 0 && (
                  <button onClick={() => setOpen(!open)} aria-expanded={open} className="ml-auto flex shrink-0 items-center gap-0.5 text-xs font-normal whitespace-nowrap text-fg-3 hover:text-fg">
                    {t.verdict.more(v.rest.length)}
                    <ChevronDown className={cx('size-3 transition-transform', open && 'rotate-180')} />
                  </button>
                )
              }
            />
            {open &&
              v.rest.map((e) => (
                <UndoBar key={e.key} floating e={e} prefix={e.subject} onUndo={() => back(e)} className="w-[min(340px,calc(100vw-3rem))] shadow-card" />
              ))}
          </motion.div>
        )}
      </AnimatePresence>
    </div>,
    document.body,
  )
}

// 批量的一条：「n 项 · 3 秒后通过」，秒数按最晚排上的那件
function GroupText({ items }: { items: Entry[] }) {
  const last = items.reduce((a, b) => (b.start > a.start ? b : a))
  return (
    <>
      {t.verdict.many(items.length)}
      {' · '}
      <Sentence e={last} />
    </>
  )
}

// ---------- 印章 ----------

// Seal：生效反馈（D-229）——到点后在卡片上盖一方印（肯定朱红、回答墨蓝、否定淡墨且轻一些），印文按场景（已通过 / 已批准 / 已拒绝…），停顿片刻后卡片收起。
// 连续审批时撤销期走完才盖（进入盖印阶段），随后翻到下一件（R-226）。卡片需要 relative；k 取模块里的撤销状态，也可以直接给 show / act / scene。减少动效时不盖印。
export function Seal({ k, show, act, scene, className }: { k?: string; show?: boolean; act?: Act; scene?: Scene; className?: string }) {
  const e = usePending(k)
  const on = SHOW_STAMP && !reducedMotion() && (show ?? (!!e && e.act !== 'move' && e.phase !== 'wait'))
  const a = act ?? e?.act ?? 'pass'
  const tone = a === 'back' ? 'border-fg-3/70 text-fg-3' : a === 'answer' ? 'border-accent text-accent' : 'border-seal text-seal'
  return (
    <AnimatePresence>
      {on && (
        <motion.div
          key="seal"
          initial={{ opacity: 0 }}
          animate={{ opacity: 1 }}
          exit={{ opacity: 0 }}
          transition={{ duration: 0.18 }}
          className={cx('pointer-events-none absolute inset-0 z-10 flex items-center justify-center bg-surface/35', className)}
        >
          <motion.span
            initial={{ opacity: 0, scale: a === 'back' ? 1.3 : 1.7, rotate: -16 }}
            animate={{ opacity: a === 'back' ? 0.85 : 1, scale: 1, rotate: -10 }}
            transition={spring()}
            className={cx('flex items-center justify-center rounded-xl border-[3px] bg-surface/80 font-display tracking-[0.2em] shadow-xs', a === 'back' ? 'h-14 w-28 text-lg' : 'h-20 w-32 text-2xl', tone)}
          >
            {t.verdict.stamp(actWord(a, '', scene ?? e?.scene))}
          </motion.span>
        </motion.div>
      )}
    </AnimatePresence>
  )
}

// ---------- 快捷键 ----------

// verdictKey：⌘↵ 当前场景的肯定动作（通过 / 批准 / 回答），⌘⌫ 否定动作（退回 / 拒绝）；附言框里也能用；处理了返回 true。
export function verdictKey(e: KeyboardEvent, h: { pass?: () => void; back?: () => void }) {
  if (e.nativeEvent.isComposing || !(e.metaKey || e.ctrlKey) || e.altKey || e.shiftKey) return false
  if (e.key === 'Enter' && h.pass) {
    e.preventDefault()
    e.stopPropagation()
    h.pass()
    return true
  }
  if (e.key === 'Backspace' && h.back) {
    e.preventDefault()
    e.stopPropagation()
    h.back()
    return true
  }
  return false
}

// ---------- Verdict ----------

export type VerdictProps = {
  // verdict：肯定 / 否定；answer：只有「回答」（决策选项先选后点）
  mode?: 'verdict' | 'answer'
  // 用词场景（R-176）：accept 验收、plan 计划、approval 批准类决策、request 请求、permit 授权；不传时 verdict 按验收、answer 按回答
  scene?: Scene
  note: string
  onNote: (v: string) => void
  files?: FileRef[]
  onFiles?: (f: FileRef[]) => void
  incoming?: File[]
  onIncoming?: () => void
  onPass: () => void
  onBack?: () => void
  backByAdvice?: boolean // 主助理建议退回（R-242）：不写附言也能退回，按钮写「按建议退回」
  canPass?: boolean
  disabled?: boolean
  busy?: boolean
  placeholder?: string
  minRows?: number
  // stack：附言占满一行，按钮在下一行右侧（抽屉、面板底部）；默认同一行
  stack?: boolean
  extra?: ReactNode // stack 时按钮行左侧的内容（提示、交给审查员等）
  hideNote?: boolean // 不要附言框（例如自由回答的决策，答复框在上面）
  draft?: ReactNode // 附言框下面那行草稿小字（「已恢复上次没写完的内容 · 清空」，R-202）
  noteRef?: RefObject<HTMLTextAreaElement | null>
  pendingKey?: string // 这件事在撤销期时，整行换成撤销条
  className?: string
  size?: 'sm' | 'md'
}

export function Verdict(p: VerdictProps) {
  const mode = p.mode ?? 'verdict'
  const scene: Scene = mode === 'answer' ? 'answer' : (p.scene ?? 'accept')
  const uploading = useUploading()
  const own = useRef<HTMLTextAreaElement>(null)
  const ref = p.noteRef ?? own
  const [need, setNeed] = useState(false)
  const pend = usePending(p.pendingKey)
  const hasNote = !!p.note.trim() || !!p.files?.length
  useEffect(() => {
    if (hasNote) setNeed(false)
  }, [hasNote])
  // 撤销期里：判断栏照原样留着，上面盖同一层遮罩（R-218：审批抽屉不翻页时遮罩盖在底部判断栏上）；盖印 / 提交后换成「已通过」
  if (pend && p.pendingKey && pend.phase !== 'wait') return <PendingBar k={p.pendingKey} className={p.className} />
  const cover = pend && p.pendingKey ? <PendingCover k={p.pendingKey} className="-m-1 rounded-xl" /> : null

  const off = p.disabled || p.busy || uploading
  const pass = () => {
    if (off || p.canPass === false) return
    p.onPass()
  }
  // 否定（退回 / 拒绝）必须附意见：没写就先把焦点放到附言框，并提示要写什么
  const back = () => {
    if (off || !p.onBack) return
    if (!hasNote && !p.backByAdvice) {
      setNeed(true)
      ref.current?.focus()
      return
    }
    p.onBack()
  }
  const size = p.size ?? 'sm'
  const btnCls = size === 'sm' ? 'h-9' : undefined
  const box = !p.hideNote && (
    <AttachZone value={p.files ?? []} onChange={p.onFiles ?? (() => {})} compact incoming={p.incoming} onIncoming={p.onIncoming} className={cx('min-w-0', p.stack ? 'w-full' : 'min-w-[12rem] flex-1')}>
      <Textarea
        ref={ref}
        minRows={p.minRows ?? 1}
        value={p.note}
        placeholder={need ? t.verdict.needNote(scene) : (p.placeholder ?? (mode === 'answer' ? t.verdict.noteOptional : p.backByAdvice ? t.verdict.noteByAdvice : t.verdict.note(scene)))}
        aria-invalid={need || undefined}
        onChange={(e) => p.onNote(e.target.value)}
        onKeyDown={(e) => verdictKey(e, { pass, back: mode === 'verdict' ? back : undefined })}
        className={cx(p.onFiles && 'pr-10', need && 'border-danger/60 ring-2 ring-danger/20 placeholder:text-danger/80')}
      />
    </AttachZone>
  )
  const buttons = (
    <div className="flex shrink-0 items-center gap-2">
      {mode === 'verdict' && p.onBack && (
        <Button size={size} className={btnCls} disabled={off} icon={<Undo2 className="size-4" />} onClick={back}>
          {p.backByAdvice && !hasNote ? t.verdict.backByAdvice : t.verdict.back(scene)}
        </Button>
      )}
      <Button
        size={size}
        variant="primary"
        className={cx(btnCls, 'min-w-20')}
        disabled={off || p.canPass === false}
        loading={p.busy}
        icon={mode === 'answer' ? <CornerDownLeft className="size-4" /> : <Check className="size-4" />}
        onClick={pass}
      >
        {t.verdict.pass(scene)}
      </Button>
    </div>
  )
  if (p.stack)
    return (
      <div className={cx('relative space-y-2.5', p.className)}>
        {cover}
        <div>
          {box}
          {p.draft}
        </div>
        <div className="flex items-center gap-2">
          <div className="min-w-0 flex-1">{p.extra}</div>
          {buttons}
        </div>
      </div>
    )
  return (
    <div className={cx('relative flex flex-wrap items-end justify-end gap-2', p.className)}>
      {cover}
      {p.extra}
      {box}
      {buttons}
      {p.draft && <div className="-mt-1.5 basis-full empty:hidden">{p.draft}</div>}
    </div>
  )
}

// ---------- 需求验收 ----------

type ReviewItem = { ref: string; verdict: 'accept' | 'revise'; comment?: string; files?: string[] }
const reviewBeacon = (items: ReviewItem[]): Beacon => ({ method: 'POST', url: '/api/reviews', body: { items } })

// done：把写操作的结果变成「是否成功」，成功后接着做 after。
export async function done(p: Promise<unknown>, after?: () => void): Promise<boolean> {
  const ok = !!(await p)
  if (ok) after?.()
  return ok
}

// useReqVerdict：需求验收的通过 / 退回，各处（工作台行、抽屉、右键菜单、状态菜单、拖拽、全部通过）共用。
// 提交前都先进撤销期，需求池里立刻显示到通过 / 退回后的列（to）；到点盖印、卡片收起、最近动态记一条就是反馈，不另弹「已验收」提示。
export function useReqVerdict() {
  const { review } = useActions()
  const item = (code: string, verdict: ReviewItem['verdict'], comment?: string, files?: string[]): ReviewItem => ({ ref: code, verdict, comment: comment?.trim() || undefined, files: files?.length ? files : undefined })
  return {
    pass: (code: string, o: { comment?: string; files?: string[]; after?: () => void } = {}) => {
      const it = item(code, 'accept', o.comment, o.files)
      return schedule(code, { act: 'pass', scene: 'accept', subject: code, to: 'done', beacon: reviewBeacon([it]), commit: () => done(review([it]), o.after) })
    },
    // to：退回后落到哪一列（默认进行中；拖到阻塞等别的列时由 after 再改过去）
    back: (code: string, o: { comment: string; files?: string[]; to?: string; after?: () => void }) => {
      const it = item(code, 'revise', o.comment, o.files)
      return schedule(code, { act: 'back', scene: 'accept', subject: code, to: o.to ?? 'in_progress', beacon: reviewBeacon([it]), commit: () => done(review([it]), o.after) })
    },
    passMany: (codes: string[]) =>
      scheduleMany(
        codes.map((code) => {
          const it = item(code, 'accept')
          return { key: code, act: 'pass' as const, scene: 'accept' as const, subject: code, to: 'done', beacon: reviewBeacon([it]), commit: () => done(review([it])) }
        }),
      ),
  }
}
