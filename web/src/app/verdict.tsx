import { useEffect, useRef, useState, useSyncExternalStore, type KeyboardEvent, type ReactNode, type RefObject } from 'react'
import { Check, CornerDownLeft, Undo2 } from 'lucide-react'
import { AnimatePresence, motion } from 'motion/react'
import { toast } from 'sonner'
import { t } from '../i18n'
import type { Scene } from '../i18n/zh'
import { cx } from '../lib/util'
import { Button, Textarea, Tip } from '../ui/base'
import { AttachZone, useUploading, type FileRef } from '../ui/attach'
import { useActions } from './actions'

// 统一的判断动作（R-161）：肯定 / 否定 / 回答 / 撤销在全站只有一种样式、一种节奏。
// - 用词按场景（R-176）：验收「通过 / 退回」、计划「批准 / 退回」、批准类决策和请求「批准 / 拒绝」、选项类决策「回答」；
//   各入口把场景（Scene）传进来，按钮、撤销条、印章、快捷键提示都按场景取词（i18n/zh.ts 的 VERB），行为不随场景变。
// - Verdict：[附言（否定时必填）][否定][肯定]，决策是 [附言][回答]；放在卡片底部右侧，各处同一顺序、颜色、图标。
// - 节奏：点了先进撤销期（UNDO_MS），卡片里显示行内撤销条，菜单 / 拖拽 / 批量用 toast 显示同一个撤销条；
//   到点后盖印（Seal：印章盖下 → 停顿 SEAL_MS）→ 提交 → 卡片高度收起、下方卡片上移（List / Item 的收起动画），最近动态记一条。
// - 撤销期的状态放在模块里，离开页面（切走路由、标签页隐藏）照常计时、到点提交；页面真的关闭或刷新时用 keepalive 立即提交，不丢。

// 撤销时长（D-229：3 秒，全站一致）。
export const UNDO_MS = 3000
// 生效反馈的印章（D-229：保留并与卡片收起连成一段）；关掉时只做收起。
export const SHOW_STAMP = true
// 印章盖下后停顿多久再提交、收起卡片；系统开了减少动效时不盖印、不停顿，只淡出收起。
export const SEAL_MS = 700
// 提交成功后保持「已盖印」多久：等列表刷新把卡片收起，避免卡片先闪回按钮
const SEALED_HOLD_MS = 2500

export const reducedMotion = () => typeof matchMedia !== 'undefined' && matchMedia('(prefers-reduced-motion: reduce)').matches
const sleep = (ms: number) => new Promise((r) => window.setTimeout(r, ms))

export type Act = 'pass' | 'back' | 'answer' | 'move'
// Phase：wait 撤销期（可撤销）→ seal 盖印中（不能撤销）→ done 已提交、等卡片收起
export type Phase = 'wait' | 'seal' | 'done'

// Beacon：页面关闭时用来立即提交的请求（fetch keepalive，cookie 同源带上）。
export type Beacon = { method: 'POST' | 'PUT'; url: string; body: unknown }

type Entry = { until: number; act: Act; scene: Scene; label: string; phase: Phase; timer: number; commit: () => Promise<boolean>; beacon?: Beacon; toastId?: string }

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

// usePending：某件事是否在撤销期 / 盖印中（key 例如需求编号 R-12、plan:P-3、stage:P-3:2）。
export function usePending(key: string | undefined) {
  return key ? usePendingMap().get(key) : undefined
}

// actWord：撤销条、印章里的动词，按场景取——通过 / 批准 / 退回 / 拒绝 / 回答，改状态时是「改为进行中」。
export const actWord = (act: Act, label = '', scene: Scene = 'accept') =>
  act === 'pass' ? t.verdict.pass(scene) : act === 'back' ? t.verdict.back(scene) : act === 'answer' ? t.verdict.pass('answer') : label

export type ScheduleOpts = {
  act: Act
  scene?: Scene // 用词场景（R-176），不传按需求验收
  commit: () => Promise<boolean> // 返回是否成功；失败时卡片回到原样
  label?: string // act = move 时的动作文字（改为进行中）
  subject?: string // toast 里写在前面的对象（R-12）
  toast?: boolean // 没有卡片可挂撤销条时（菜单、拖拽、批量）用 toast 显示
  beacon?: Beacon
}

// schedule：进入撤销期；同一件事已在撤销期里时不重复排。
export function schedule(key: string, o: ScheduleOpts) {
  if (pending.has(key)) return false
  const until = Date.now() + UNDO_MS
  const timer = window.setTimeout(() => void fire(key), UNDO_MS)
  const scene = o.scene ?? 'accept'
  const e: Entry = { until, act: o.act, scene, label: o.label ?? '', phase: 'wait', timer, commit: o.commit, beacon: o.beacon }
  if (o.toast) e.toastId = undoToast(o.subject ?? key, actWord(o.act, o.label, scene), until, () => undo(key))
  pending.set(key, e)
  emit()
  return true
}

const set = (key: string, e: Entry, phase: Phase) => {
  pending.set(key, { ...e, phase })
  emit()
}

async function fire(key: string) {
  const e = pending.get(key)
  if (!e || e.phase !== 'wait') return
  if (e.toastId) toast.dismiss(e.toastId)
  // 改状态没有卡片可盖印，直接提交；其它先盖印、停顿片刻
  const seal = e.act !== 'move' && SHOW_STAMP && !reducedMotion()
  set(key, e, 'seal')
  if (seal) await sleep(SEAL_MS)
  const ok = await e.commit().catch(() => false)
  if (!ok) {
    pending.delete(key)
    return emit()
  }
  set(key, e, 'done')
  window.setTimeout(() => {
    if (pending.get(key)?.phase === 'done') {
      pending.delete(key)
      emit()
    }
  }, SEALED_HOLD_MS)
}

export function undo(key: string) {
  const e = pending.get(key)
  if (!e || e.phase !== 'wait') return
  window.clearTimeout(e.timer)
  pending.delete(key)
  if (e.toastId) toast.dismiss(e.toastId)
  emit()
}

// scheduleMany：批量操作（全部通过等）——每件各自进撤销期（卡片上各有撤销条），另给一条 toast 一次撤销全部。
export function scheduleMany(items: { key: string; commit: () => Promise<boolean>; beacon?: Beacon }[], act: Act, scene: Scene = 'accept') {
  const fresh = items.filter((x) => !pending.has(x.key))
  fresh.forEach((x) => schedule(x.key, { act, scene, commit: x.commit, beacon: x.beacon }))
  if (fresh.length) undoToast(t.verdict.many(fresh.length), actWord(act, '', scene), Date.now() + UNDO_MS, () => fresh.forEach((x) => undo(x.key)))
}

// undoToast：没有卡片可挂撤销条时，用 toast 显示同一个撤销条（批量回答决策也用它）。
let toastSeq = 0
export function undoToast(subject: string, word: string, until: number, onUndo: () => void) {
  const id = `ks-undo-${++toastSeq}`
  toast.custom(
    () => (
      <UndoBar
        until={until}
        text={(s) => `${subject} · ${t.verdict.pending(word, s)}`}
        onUndo={() => {
          onUndo()
          toast.dismiss(id)
        }}
        floating
      />
    ),
    { id, duration: until - Date.now() + 300, unstyled: true },
  )
  return id
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

// ---------- 撤销条 ----------

function useSeconds(until: number) {
  const [, tick] = useState(0)
  useEffect(() => {
    const id = window.setInterval(() => tick((n) => n + 1), 250)
    return () => window.clearInterval(id)
  }, [])
  return Math.max(1, Math.ceil((until - Date.now()) / 1000))
}

// UndoBar：卡片里和 toast 里是同一个撤销条——中性的墨蓝底，左边写「3 秒后通过」「3 秒后拒绝」，右边撤销，底边一条倒计时线。
export function UndoBar({ until, text, onUndo, floating, done, className }: { until: number; text: (s: number) => ReactNode; onUndo: () => void; floating?: boolean; done?: boolean; className?: string }) {
  const s = useSeconds(until)
  const left = Math.max(0, until - Date.now())
  return (
    <div
      role="status"
      className={cx(
        'relative flex items-center gap-3 overflow-hidden rounded-xl border border-accent/30 bg-accent-soft px-3 py-2 text-sm text-fg',
        floating && 'w-[min(360px,calc(100vw-2rem))] shadow-pop',
        className,
      )}
    >
      <span className="min-w-0 flex-1 truncate tabular">{text(s)}</span>
      {!done && (
        <Button size="sm" variant="ghost" icon={<Undo2 className="size-3.5" />} onClick={onUndo} className="-my-1 shrink-0 text-accent hover:text-accent">
          {t.verdict.undo}
        </Button>
      )}
      {!done && <span aria-hidden className="ks-undo-line absolute bottom-0 left-0 h-0.5 w-full origin-left bg-accent" style={{ animationDuration: `${UNDO_MS}ms`, animationDelay: `${left - UNDO_MS}ms` }} />}
    </div>
  )
}

// PendingBar：某件事在撤销期时，卡片里显示的撤销条；盖印后变成不能撤销的「已通过」「已拒绝」。
export function PendingBar({ k, className, prefix }: { k: string; className?: string; prefix?: string }) {
  const e = usePending(k)
  if (!e) return null
  const word = actWord(e.act, e.label, e.scene)
  return <UndoBar until={e.until} done={e.phase !== 'wait'} text={(s) => (prefix ? `${prefix} · ` : '') + (e.phase === 'wait' ? t.verdict.pending(word, s) : t.verdict.sealed(word))} onUndo={() => undo(k)} className={className} />
}

// ---------- 印章 ----------

// Seal：生效反馈（D-229）——到点后在卡片上盖一方印（肯定朱红、回答墨蓝、否定淡墨且轻一些），印文按场景（已通过 / 已批准 / 已拒绝…），停顿片刻后卡片收起。
// 卡片需要 relative；k 取模块里的撤销状态，也可以直接给 show / act / scene（决策发件箱）。减少动效时不盖印。
export function Seal({ k, show, act, scene, className }: { k?: string; show?: boolean; act?: Act; scene?: Scene; className?: string }) {
  const e = usePending(k)
  const on = SHOW_STAMP && !reducedMotion() && (show ?? (!!e && e.phase !== 'wait' && e.act !== 'move'))
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
            transition={{ type: 'spring', bounce: a === 'back' ? 0.15 : 0.38, duration: 0.42 }}
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
        <Tip content={t.verdict.keyBack(scene)}>
          <span>
            <Button size={size} className={btnCls} disabled={off} icon={<Undo2 className="size-4" />} onClick={back}>
              {t.verdict.back(scene)}
            </Button>
          </span>
        </Tip>
      )}
      <Tip content={t.verdict.keyPass(scene)}>
        <span>
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
        </span>
      </Tip>
    </div>
  )
  if (p.stack)
    return (
      <div className={cx('space-y-2.5', p.className)}>
        {box}
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
// 提交前都先进撤销期；到点盖印、卡片收起、最近动态记一条就是反馈，不另弹「已验收」提示。
export function useReqVerdict() {
  const { review } = useActions()
  const item = (code: string, verdict: ReviewItem['verdict'], comment?: string, files?: string[]): ReviewItem => ({ ref: code, verdict, comment: comment?.trim() || undefined, files: files?.length ? files : undefined })
  return {
    pass: (code: string, o: { comment?: string; files?: string[]; toast?: boolean; after?: () => void } = {}) => {
      const it = item(code, 'accept', o.comment, o.files)
      return schedule(code, { act: 'pass', scene: 'accept', subject: code, toast: o.toast, beacon: reviewBeacon([it]), commit: () => done(review([it]), o.after) })
    },
    back: (code: string, o: { comment: string; files?: string[]; toast?: boolean; after?: () => void }) => {
      const it = item(code, 'revise', o.comment, o.files)
      return schedule(code, { act: 'back', scene: 'accept', subject: code, toast: o.toast, beacon: reviewBeacon([it]), commit: () => done(review([it]), o.after) })
    },
    passMany: (codes: string[]) =>
      scheduleMany(
        codes.map((code) => {
          const it = item(code, 'accept')
          return { key: code, beacon: reviewBeacon([it]), commit: () => done(review([it])) }
        }),
        'pass',
        'accept',
      ),
  }
}
