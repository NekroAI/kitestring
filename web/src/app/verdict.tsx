import { useContext, useEffect, useRef, useState, useSyncExternalStore, type KeyboardEvent, type ReactNode, type RefObject } from 'react'
import { createPortal } from 'react-dom'
import { Check, ChevronDown, CornerDownLeft, Undo2 } from 'lucide-react'
import { AnimatePresence, motion, useIsPresent } from 'motion/react'
import { t } from '../i18n'
import type { Scene } from '../i18n/zh'
import { cx } from '../lib/util'
import { dockView } from '../lib/pending'
import { Button, Textarea } from '../ui/base'
import { AttachZone, useUploading, type FileRef } from '../ui/attach'
import { Leaving, reducedMotion as reduced, spring } from '../ui/gesture'
import { useActions } from './actions'

// 统一的判断动作（R-161）：肯定 / 否定 / 回答 / 撤销在全站只有一种样式、一种节奏。
// - 用词按场景（R-176）：验收「通过 / 退回」、计划「批准 / 退回」、批准类决策和请求「批准 / 拒绝」、选项类决策「回答」；
//   各入口把场景（Scene）传进来，按钮、撤销条、印章、快捷键提示都按场景取词（i18n/zh.ts 的 VERB），行为不随场景变。
// - Verdict：[附言（否定时必填）][否定][肯定]，决策是 [附言][回答]；放在卡片底部右侧，各处同一顺序、颜色、图标。
//
// 撤销期（R-201，docs/principles.md 第 8 条「撤销期不挡下一步」）：全站只有这一个调度器（schedule），需求验收、决策答复（outbox）、
// 计划 / 阶段、改需求状态、开始计划、撤销授权、撤回放行都走它；计时、提交、盖印、页面关闭时立即提交都只在这里写一次。
// - 结果立刻到位：排上后界面马上显示操作后的样子（需求进新列，见 lib/pending 的 shownStatus），元素上盖同一个撤销条（UndoBar）：
//   行内摆法（PendingBar，卡片底部的判断行换成它）和遮罩摆法（PendingCover，盖在需求块上）；不锁界面、不排队，用户可以马上做下一件事。
// - 元素看不见时（翻到下一件、列内滚走、手机上不在当前列）撤回挪到顶部浮动条（UndoDock）；批量只出一条汇总。
// - 到点：盖印（Seal：印章盖下 → 停顿 SEAL_MS）→ 提交 → 卡片收起 / 留在新列；失败时元素回到原处（带同一套换列动画），错误由 useActions 提示。
// - 连续审批（useAdvance）：一进撤销期就在原处盖一下印，SEAL_MS 后翻到下一件；浮动条里点撤回会先回到那一件再撤销。
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
  act: Act
  scene: Scene
  label: string
  subject: string // 浮动条里写在前面的对象（R-12、P-3）
  to?: string // 需求改到的状态：撤销期里需求池按它显示（结果立刻到位）
  group?: string // 批量操作：同一组只出一条浮动条
  stamp?: boolean // 连续审批：撤销期里就先盖一下印（随后翻到下一件）
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
  const timer = window.setTimeout(() => void fire(key), UNDO_MS)
  pending.set(key, { key, at, until, act: o.act, scene: o.scene ?? 'accept', label: o.label ?? '', subject: o.subject ?? key, to: o.to, group: o.group, reopen: o.reopen, phase: 'wait', timer, commit: o.commit, beacon: o.beacon })
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

// useAdvance：连续审批（需求抽屉的连续验收、审批抽屉）——这件事一进撤销期，就在原处盖一下印，SEAL_MS 后翻到下一件，不等 3 秒；
// 刚批的那件不在屏幕上了，撤回出现在顶部浮动条，点了先回到这一件（reopen）再撤销。同一次排上只翻一次（翻回来看它时不会再被翻走）。
// 一般动作（改状态等，act = move）不翻页；only 限定只在某种判断后翻（需求抽屉只在通过后翻，退回留在原处）。
export function useAdvance(key: string | undefined, on: boolean, advance: () => void, reopen?: () => void, only?: Act) {
  const e = usePending(key)
  const waiting = on && !!key && !!e && e.phase === 'wait' && e.act !== 'move' && (!only || e.act === only)
  const handled = useRef(0)
  const timer = useRef(0)
  const at = e?.at ?? 0
  const go = useRef(advance)
  go.current = advance
  useEffect(() => {
    if (!waiting || !key || handled.current === at) return
    handled.current = at
    const stamp = SHOW_STAMP && !reducedMotion()
    patch(key, { stamp, reopen })
    timer.current = window.setTimeout(() => {
      timer.current = 0
      patch(key, { stamp: false }) // 印只盖这一下；翻回来看它时不再顶着印
      go.current()
    }, stamp ? SEAL_MS : 0)
  }, [waiting, key, at]) // eslint-disable-line react-hooks/exhaustive-deps
  // 盖印那一下里就撤回了（或换了一件）：不翻
  useEffect(() => {
    if (waiting || !timer.current) return
    window.clearTimeout(timer.current)
    timer.current = 0
  }, [waiting])
  useEffect(() => () => window.clearTimeout(timer.current), [])
}

// ---------- 锚点：元素看不看得见 ----------

// 每件事在屏幕上可见的撤销条 / 遮罩有几个；为 0 时撤回挪到顶部浮动条。
const anchors = new Map<string, number>()
const bump = (key: string, d: number) => {
  const n = (anchors.get(key) ?? 0) + d
  if (n > 0) anchors.set(key, n)
  else anchors.delete(key)
  emit()
}

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
          bump(key, v ? 1 : -1)
        }
      },
      { threshold: [0, 0.5, 1] },
    )
    io.observe(el)
    return () => {
      io.disconnect()
      if (vis) bump(key, -1)
    }
  }, [el, key])
  return setEl
}

// ---------- 撤销条 ----------

function useSeconds(until: number) {
  const [, tick] = useState(0)
  useEffect(() => {
    const id = window.setInterval(() => tick((n) => n + 1), 250)
    return () => window.clearInterval(id)
  }, [])
  return Math.max(1, Math.ceil((until - Date.now()) / 1000))
}

// UndoBar：全站只有这一个撤销条——中性的墨蓝底，左边写「3 秒后通过」「3 秒后改为进行中」，右边撤销，底边一条倒计时线。
// 三种摆法：行内（卡片底部，PendingBar）、遮罩（compact，盖在需求块上，PendingCover）、浮动（floating，顶部浮动条 UndoDock）。
export function UndoBar({
  until,
  text,
  onUndo,
  floating,
  compact,
  done,
  undoText,
  extra,
  className,
}: {
  until: number
  text: (s: number) => ReactNode
  onUndo: () => void
  floating?: boolean
  compact?: boolean
  done?: boolean
  undoText?: string
  extra?: ReactNode
  className?: string
}) {
  const s = useSeconds(until)
  const left = Math.max(0, until - Date.now())
  return (
    <div
      role="status"
      className={cx(
        'relative flex items-center overflow-hidden border border-accent/30 bg-accent-soft text-fg',
        compact ? 'gap-1.5 rounded-lg py-0.5 pr-0.5 pl-2 text-xs' : 'gap-3 rounded-xl px-3 py-2 text-sm',
        floating && 'w-[min(380px,calc(100vw-2rem))] shadow-pop',
        className,
      )}
    >
      <span className="min-w-0 flex-1 truncate tabular">{text(s)}</span>
      {extra}
      {!done && (
        <Button
          size={compact ? 'icon-sm' : 'sm'}
          variant="ghost"
          icon={<Undo2 className="size-3.5 shrink-0" />}
          onClick={(e) => {
            e.stopPropagation()
            onUndo()
          }}
          onPointerDown={(e) => e.stopPropagation()}
          aria-label={undoText ?? t.verdict.undo}
          title={undoText ?? t.verdict.undo}
          className={cx('shrink-0 text-accent hover:text-accent', compact ? '-my-0.5' : '-my-1')}
        >
          {/* 遮罩摆法（需求块只有 156px 宽）只放图标，把位置留给「2 秒后改为阻塞」 */}
          {!compact && (undoText ?? t.verdict.undo)}
        </Button>
      )}
      {!done && <span aria-hidden className="ks-undo-line absolute bottom-0 left-0 h-0.5 w-full origin-left bg-accent" style={{ animationDuration: `${UNDO_MS}ms`, animationDelay: `${left - UNDO_MS}ms` }} />}
    </div>
  )
}

const entryText = (e: Entry, s: number, prefix?: string) => {
  const word = actWord(e.act, e.label, e.scene)
  return (prefix ? `${prefix} · ` : '') + (e.phase === 'wait' ? t.verdict.pending(word, s) : t.verdict.sealed(word))
}

// PendingBar：某件事在撤销期时，卡片里（行内）显示的撤销条；盖印后变成不能撤销的「已通过」「已拒绝」。
export function PendingBar({ k, className, prefix }: { k: string; className?: string; prefix?: string }) {
  const e = usePending(k)
  const ref = useUndoAnchor(e?.phase === 'wait' ? k : undefined)
  if (!e) return null
  return (
    <div ref={ref} className={className}>
      <UndoBar until={e.until} done={e.phase !== 'wait'} text={(s) => entryText(e, s, prefix)} onUndo={() => undo(k)} />
    </div>
  )
}

// PendingCover：遮罩摆法——盖在需求块（需求池、列表行）上：块已经在新位置，上面一层淡色 + 底部同一个撤销条和倒计时线；
// 走完即生效、遮罩淡出。父元素需要 relative。只盖撤销期，盖印 / 提交后不再挡着。
export function PendingCover({ k, className }: { k: string; className?: string }) {
  const e = usePending(k)
  const on = e?.phase === 'wait'
  const ref = useUndoAnchor(on ? k : undefined)
  return (
    <AnimatePresence>
      {on && e && (
        <motion.div
          key={e.at}
          ref={ref}
          data-cover=""
          initial={{ opacity: 0 }}
          animate={{ opacity: 1 }}
          exit={{ opacity: 0 }}
          transition={{ duration: 0.18 }}
          className={cx('pointer-events-none absolute inset-0 z-10 flex flex-col justify-end rounded-[inherit] bg-surface/35 p-1', className)}
        >
          <UndoBar compact until={e.until} text={(s) => entryText(e, s)} onUndo={() => undo(k)} className="pointer-events-auto" />
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
              until={Math.max(...g.items.map((x) => x.until))}
              undoText={t.verdict.undoAll}
              text={(s) => `${t.verdict.many(g.items.length)} · ${t.verdict.pending(actWord(g.items[0].act, g.items[0].label, g.items[0].scene), s)}`}
              onUndo={() => g.items.forEach((x) => undo(x.key))}
            />
          </motion.div>
        ))}
        {v.latest && (
          <motion.div key="latest" layout initial={{ opacity: 0, y: -8 }} animate={{ opacity: 1, y: 0 }} exit={{ opacity: 0, y: -8 }} transition={{ duration: 0.18 }} className="pointer-events-auto flex flex-col items-center gap-1.5">
            <UndoBar
              floating
              until={v.latest.until}
              text={(s) => entryText(v.latest!, s, v.latest!.subject)}
              onUndo={() => back(v.latest!)}
              extra={
                v.rest.length > 0 && (
                  <button onClick={() => setOpen(!open)} aria-expanded={open} className="flex shrink-0 items-center gap-0.5 text-xs whitespace-nowrap text-fg-3 hover:text-fg">
                    {t.verdict.more(v.rest.length)}
                    <ChevronDown className={cx('size-3 transition-transform', open && 'rotate-180')} />
                  </button>
                )
              }
            />
            {open &&
              v.rest.map((e) => (
                <UndoBar key={e.key} floating compact until={e.until} text={(s) => entryText(e, s, e.subject)} onUndo={() => back(e)} className="w-[min(340px,calc(100vw-3rem))] shadow-card" />
              ))}
          </motion.div>
        )}
      </AnimatePresence>
    </div>,
    document.body,
  )
}

// ---------- 印章 ----------

// Seal：生效反馈（D-229）——到点后在卡片上盖一方印（肯定朱红、回答墨蓝、否定淡墨且轻一些），印文按场景（已通过 / 已批准 / 已拒绝…），停顿片刻后卡片收起。
// 连续审批时撤销期一开始就盖（stamp），随后翻到下一件。卡片需要 relative；k 取模块里的撤销状态，也可以直接给 show / act / scene。减少动效时不盖印。
export function Seal({ k, show, act, scene, className }: { k?: string; show?: boolean; act?: Act; scene?: Scene; className?: string }) {
  const e = usePending(k)
  const on = SHOW_STAMP && !reducedMotion() && (show ?? (!!e && e.act !== 'move' && (e.phase !== 'wait' || !!e.stamp)))
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
  if (pend && p.pendingKey) return <PendingBar k={p.pendingKey} className={p.className} />

  const off = p.disabled || p.busy || uploading
  const pass = () => {
    if (off || p.canPass === false) return
    p.onPass()
  }
  // 否定（退回 / 拒绝）必须附意见：没写就先把焦点放到附言框，并提示要写什么
  const back = () => {
    if (off || !p.onBack) return
    if (!hasNote) {
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
        placeholder={need ? t.verdict.needNote(scene) : (p.placeholder ?? (mode === 'answer' ? t.verdict.noteOptional : t.verdict.note(scene)))}
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
          {t.verdict.back(scene)}
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
      <div className={cx('space-y-2.5', p.className)}>
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
    <div className={cx('flex flex-wrap items-end justify-end gap-2', p.className)}>
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
