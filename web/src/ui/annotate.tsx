import { createContext, useCallback, useContext, useEffect, useLayoutEffect, useRef, useState, type PointerEvent as ReactPointerEvent, type SyntheticEvent } from 'react'
import { Dialog } from 'radix-ui'
import { AnimatePresence, motion } from 'motion/react'
import { ArrowUpRight, Check, PenLine, PenTool, Redo2, Square, Type, Undo2, X } from 'lucide-react'
import { t } from '../i18n'
import { cx, usePref } from '../lib/util'
import { Button, Tip } from './base'

// 圈画标注：在截图 / 效果图上画笔、框、箭头、写字，导出为一张新图片附在意见里（D-21 ②）。
// 手写笔：按压力度决定笔画粗细；一旦用过笔，手指触摸不再落笔（防手掌误触）。鼠标、触控板、手指同样可用。
// 手指不画（防误触，R-174）：开着时手指触摸一律不落笔（也不放文字框），只有手写笔和鼠标能画。
// 工具栏开关与设置页「圈画标注 · 手指不画」是同一个本机偏好（按设备记住）。
// 手写笔连续书写（R-180）：WebKit 只看 touch 事件决定要不要做系统手势——React 的 touchstart / touchmove 是 passive 监听，
// 指针事件上 preventDefault 也拦不住，于是笔快速连点被当成双击（选中、放大），笔迹还会被随手写（Scribble）拿去识别成文字、弹出建议。
// 所以画布区域用原生非 passive 的 touchstart / touchmove 一律 preventDefault（文字框除外），并整块 touch-action: none；
// 笔放下的文字框不自动聚焦（不弹键盘、不进随手写），再点一下文字框才输入。
// 所有坐标按图片宽高归一化保存，导出时按原图分辨率重绘，放大后线条依然清楚。

type Pt = { x: number; y: number; p: number }
type Shape =
  | { kind: 'pen'; color: string; w: number; pts: Pt[] }
  | { kind: 'rect' | 'arrow'; color: string; w: number; a: Pt; b: Pt }
  | { kind: 'text'; color: string; size: number; at: Pt; text: string }
type Tool = 'pen' | 'rect' | 'arrow' | 'text'

const TAP_GAP = 150 // 多指“几乎同时”按下的间隔上限（毫秒）
const TAP_MS = 250 // 从第一根手指按下到全部抬起的时长上限
const TAP_MOVE = 10 // 每根手指允许的移动（像素）

const COLORS = ['#e5483a', '#f2b33d', '#3f78c4', '#ffffff', '#1f1d1a']

function draw(ctx: CanvasRenderingContext2D, shapes: Shape[], W: number, H: number) {
  const s = Math.max(W, H)
  ctx.lineCap = 'round'
  ctx.lineJoin = 'round'
  for (const sh of shapes) {
    ctx.strokeStyle = sh.color
    ctx.fillStyle = sh.color
    if (sh.kind === 'pen') {
      const pts = sh.pts
      if (pts.length === 1) {
        ctx.beginPath()
        ctx.arc(pts[0].x * W, pts[0].y * H, (sh.w * s * (0.5 + pts[0].p)) / 2, 0, Math.PI * 2)
        ctx.fill()
        continue
      }
      // 逐段按压力变化线宽，用中点二次曲线平滑
      for (let i = 1; i < pts.length; i++) {
        const a = pts[i - 1]
        const b = pts[i]
        const c = pts[i + 1] ?? b
        ctx.lineWidth = sh.w * s * (0.5 + (a.p + b.p) / 2)
        ctx.beginPath()
        ctx.moveTo(((a.x + b.x) / 2) * W, ((a.y + b.y) / 2) * H)
        ctx.quadraticCurveTo(b.x * W, b.y * H, ((b.x + c.x) / 2) * W, ((b.y + c.y) / 2) * H)
        ctx.stroke()
      }
    } else if (sh.kind === 'rect') {
      ctx.lineWidth = sh.w * s
      const x = Math.min(sh.a.x, sh.b.x) * W
      const y = Math.min(sh.a.y, sh.b.y) * H
      ctx.beginPath()
      ctx.roundRect(x, y, Math.abs(sh.b.x - sh.a.x) * W, Math.abs(sh.b.y - sh.a.y) * H, 6 * (s / 1000))
      ctx.stroke()
    } else if (sh.kind === 'arrow') {
      const ax = sh.a.x * W, ay = sh.a.y * H, bx = sh.b.x * W, by = sh.b.y * H
      const lw = sh.w * s
      ctx.lineWidth = lw
      ctx.beginPath()
      ctx.moveTo(ax, ay)
      ctx.lineTo(bx, by)
      ctx.stroke()
      const ang = Math.atan2(by - ay, bx - ax)
      const head = Math.max(lw * 4, 14 * (s / 1000))
      ctx.beginPath()
      ctx.moveTo(bx, by)
      ctx.lineTo(bx - head * Math.cos(ang - 0.45), by - head * Math.sin(ang - 0.45))
      ctx.lineTo(bx - head * Math.cos(ang + 0.45), by - head * Math.sin(ang + 0.45))
      ctx.closePath()
      ctx.fill()
    } else if (sh.kind === 'text') {
      const fs = sh.size * s
      ctx.font = `600 ${fs}px -apple-system, "PingFang SC", sans-serif`
      ctx.textBaseline = 'top'
      // 白色描边让文字在任何底色上都看得清
      ctx.lineWidth = fs / 6
      ctx.strokeStyle = sh.color === '#ffffff' ? '#1f1d1a' : '#ffffff'
      sh.text.split('\n').forEach((line, i) => {
        ctx.strokeText(line, sh.at.x * W, sh.at.y * H + i * fs * 1.25)
        ctx.fillText(line, sh.at.x * W, sh.at.y * H + i * fs * 1.25)
      })
    }
  }
}

export const PEN_ONLY_KEY = 'ks.annotate.penOnly'
export const usePenOnly = () => usePref(PEN_ONLY_KEY, false)

// shield：全屏浮层（看图、标注）挡住指针 / 触摸事件继续冒泡。浮层虽然渲染在 body 上，React 事件仍沿组件树冒泡，
// 会被外层抽屉当成“左右滑动切换需求”“下拉关闭”——Apple Pencil 画一笔横线就会把抽屉切走（R-39）。
const stop = (e: SyntheticEvent) => e.stopPropagation()
export const shield = {
  onClick: stop,
  onPointerDown: stop,
  onPointerMove: stop,
  onPointerUp: stop,
  onPointerCancel: stop,
  onTouchStart: stop,
  onTouchMove: stop,
  onTouchEnd: stop,
  onMouseDown: stop,
  onDoubleClick: stop,
}

// Annotator：全屏标注编辑器。完成时把“原图 + 标注”导出为 PNG 文件交给 onDone。
export function Annotator({ src, name, open, onClose, onDone }: { src: string; name: string; open: boolean; onClose: () => void; onDone: (f: File) => void }) {
  return (
    <Dialog.Root open={open} onOpenChange={(o) => !o && onClose()}>
      <AnimatePresence>
        {open && (
          <Dialog.Portal forceMount>
            <Dialog.Overlay asChild forceMount>
              <motion.div className="ks-noselect fixed inset-0 z-[80] bg-black/90" initial={{ opacity: 0 }} animate={{ opacity: 1 }} exit={{ opacity: 0 }} />
            </Dialog.Overlay>
            <Dialog.Content forceMount aria-describedby={undefined} data-ks-modal="" {...shield} onContextMenu={(e) => e.preventDefault()} className="ks-noselect fixed inset-0 z-[80] outline-none">
              <Dialog.Title className="sr-only">{t.annotate.title}</Dialog.Title>
              <Editor src={src} name={name} onClose={onClose} onDone={onDone} />
            </Dialog.Content>
          </Dialog.Portal>
        )}
      </AnimatePresence>
    </Dialog.Root>
  )
}

function Editor({ src, name, onClose, onDone }: { src: string; name: string; onClose: () => void; onDone: (f: File) => void }) {
  const img = useRef<HTMLImageElement | null>(null)
  const wrap = useRef<HTMLDivElement>(null)
  const cv = useRef<HTMLCanvasElement>(null)
  const [size, setSize] = useState<{ w: number; h: number } | null>(null)
  const [shapes, setShapes] = useState<Shape[]>([])
  const [redo, setRedo] = useState<Shape[]>([])
  const [tool, setTool] = useState<Tool>('pen')
  const [color, setColor] = useState(COLORS[0])
  const [typing, setTyping] = useState<{ at: Pt; text: string; pen?: boolean } | null>(null)
  const [busy, setBusy] = useState(false)
  const live = useRef<Shape | null>(null)
  const penSeen = useRef(false)
  const [penOnly, setPenOnly] = usePenOnly()
  const active = useRef<number | null>(null)

  // 载入原图（同源，导出不会被污染）
  useEffect(() => {
    const im = new Image()
    im.onload = () => {
      img.current = im
      fit()
    }
    im.src = src
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [src])

  // 画布区域吃掉 touch 的默认行为（双击选中 / 放大、随手写、长按放大镜），指针事件照常派发
  useEffect(() => {
    const el = wrap.current
    if (!el) return
    const block = (e: TouchEvent) => {
      if ((e.target as HTMLElement).closest?.('textarea')) return
      if (e.cancelable) e.preventDefault()
    }
    const opts = { passive: false } as const
    el.addEventListener('touchstart', block, opts)
    el.addEventListener('touchmove', block, opts)
    el.addEventListener('touchend', block, opts)
    return () => {
      el.removeEventListener('touchstart', block)
      el.removeEventListener('touchmove', block)
      el.removeEventListener('touchend', block)
    }
  }, [])

  const fit = useCallback(() => {
    const im = img.current
    const box = wrap.current
    if (!im || !box) return
    const k = Math.min(box.clientWidth / im.naturalWidth, box.clientHeight / im.naturalHeight, 1)
    setSize({ w: Math.round(im.naturalWidth * k), h: Math.round(im.naturalHeight * k) })
  }, [])
  useEffect(() => {
    window.addEventListener('resize', fit)
    return () => window.removeEventListener('resize', fit)
  }, [fit])

  const render = useCallback(() => {
    const c = cv.current
    if (!c || !size) return
    const dpr = window.devicePixelRatio || 1
    c.width = size.w * dpr
    c.height = size.h * dpr
    const ctx = c.getContext('2d')!
    ctx.scale(dpr, dpr)
    draw(ctx, live.current ? [...shapes, live.current] : shapes, size.w, size.h)
  }, [shapes, size])
  useLayoutEffect(render, [render])

  const pos = (e: ReactPointerEvent): Pt => {
    const r = cv.current!.getBoundingClientRect()
    const pen = e.pointerType === 'pen'
    return { x: (e.clientX - r.left) / r.width, y: (e.clientY - r.top) / r.height, p: pen ? Math.max(0.05, e.pressure || 0.5) : 0.5 }
  }

  // 多指轻点（R-174）：两指轻点撤销、三指轻点重做（网页拿不到 Apple Pencil 双击，这是绘图 App 常用的替代）。
  // 判定：几根手指在 TAP_GAP 内先后按下、从第一根按下起 TAP_MS 内全部抬起、每根移动不超过 TAP_MOVE。
  // 第二根手指一落下，正在用手指画的那一笔就作废，所以轻点不会留下线。
  const fingers = useRef(new Map<number, { x: number; y: number }>())
  const tap = useRef<{ t0: number; n: number; ok: boolean } | null>(null)
  const trackDown = (e: ReactPointerEvent) => {
    const now = performance.now()
    if (fingers.current.size === 0) tap.current = { t0: now, n: 0, ok: true }
    fingers.current.set(e.pointerId, { x: e.clientX, y: e.clientY })
    const g = tap.current!
    g.n = Math.max(g.n, fingers.current.size)
    if (fingers.current.size > 1) {
      if (now - g.t0 > TAP_GAP) g.ok = false
      if (live.current && active.current !== null && active.current !== e.pointerId && fingers.current.has(active.current)) {
        live.current = null // 手指画的那一笔作废
        active.current = null
        render()
      }
    }
  }
  const trackMove = (e: ReactPointerEvent) => {
    const f = fingers.current.get(e.pointerId)
    if (f && tap.current && Math.hypot(e.clientX - f.x, e.clientY - f.y) > TAP_MOVE) tap.current.ok = false
  }
  const trackUp = (e: ReactPointerEvent) => {
    if (!fingers.current.delete(e.pointerId) || fingers.current.size) return
    const g = tap.current
    tap.current = null
    if (!g || !g.ok || e.type === 'pointercancel' || performance.now() - g.t0 > TAP_MS || typing) return
    if (g.n === 2) undo()
    else if (g.n === 3) again()
  }

  const down = (e: ReactPointerEvent) => {
    if (e.pointerType === 'touch') {
      trackDown(e)
      if (fingers.current.size > 1) return // 多指不落笔
    }
    if (e.pointerType === 'pen') {
      penSeen.current = true
      e.preventDefault() // 不派生 mouse 兼容事件，不挪焦点
    }
    if ((penOnly || penSeen.current) && e.pointerType === 'touch') return // 只认手写笔 / 用过笔后：忽略手掌 / 手指
    if (typing) {
      // 文字框外落笔 = 写完这段文字（笔不挪焦点，不会触发文字框失焦）
      if (document.activeElement instanceof HTMLTextAreaElement) document.activeElement.blur()
      else commitText()
      return
    }
    if (active.current !== null) return
    e.currentTarget.setPointerCapture(e.pointerId)
    active.current = e.pointerId
    const p = pos(e)
    if (tool === 'text') {
      setTyping({ at: p, text: '', pen: e.pointerType === 'pen' })
      active.current = null
      return
    }
    const w = tool === 'pen' ? 0.0035 : 0.003
    live.current = tool === 'pen' ? { kind: 'pen', color, w, pts: [p] } : { kind: tool, color, w, a: p, b: p }
    render()
  }
  const move = (e: ReactPointerEvent) => {
    if (e.pointerType === 'touch') trackMove(e)
    if (e.pointerId !== active.current || !live.current) return
    // 取合并事件，笔迹更顺
    const evs = (e.nativeEvent as PointerEvent).getCoalescedEvents?.() ?? [e.nativeEvent]
    const sh = live.current
    if (sh.kind === 'pen') for (const ce of evs) sh.pts.push(pos(ce as unknown as ReactPointerEvent))
    else if (sh.kind === 'rect' || sh.kind === 'arrow') sh.b = pos(e)
    render()
  }
  const up = (e: ReactPointerEvent) => {
    if (e.pointerType === 'touch') trackUp(e)
    if (e.pointerId !== active.current) return
    active.current = null
    const sh = live.current
    live.current = null
    if (!sh) return
    if ((sh.kind === 'rect' || sh.kind === 'arrow') && Math.hypot(sh.b.x - sh.a.x, sh.b.y - sh.a.y) < 0.005) return render()
    setShapes((s) => [...s, sh])
    setRedo([])
  }

  const commitText = () => {
    if (typing && typing.text.trim()) {
      setShapes((s) => [...s, { kind: 'text', color, size: 0.022, at: typing.at, text: typing.text.trim() }])
      setRedo([])
    }
    setTyping(null)
  }
  const undo = () => {
    if (!shapes.length) return
    setRedo((r) => [shapes[shapes.length - 1], ...r])
    setShapes((s) => s.slice(0, -1))
  }
  const again = () => {
    if (!redo.length) return
    setShapes((s) => [...s, redo[0]])
    setRedo((r) => r.slice(1))
  }

  useEffect(() => {
    const onKey = (e: KeyboardEvent) => {
      if (typing) return
      if ((e.metaKey || e.ctrlKey) && e.key.toLowerCase() === 'z') {
        e.preventDefault()
        if (e.shiftKey) again()
        else undo()
      }
    }
    window.addEventListener('keydown', onKey)
    return () => window.removeEventListener('keydown', onKey)
  })

  const finish = async () => {
    const im = img.current
    if (!im) return
    setBusy(true)
    const c = document.createElement('canvas')
    c.width = im.naturalWidth
    c.height = im.naturalHeight
    const ctx = c.getContext('2d')!
    ctx.drawImage(im, 0, 0)
    draw(ctx, shapes, c.width, c.height)
    const blob = await new Promise<Blob | null>((r) => c.toBlob(r, 'image/png'))
    setBusy(false)
    if (!blob) return
    onDone(new File([blob], `${name.replace(/\.[^.]+$/, '')}-${t.annotate.suffix}.png`, { type: 'image/png' }))
    onClose()
  }

  const tools: { id: Tool; icon: typeof PenLine; label: string }[] = [
    { id: 'pen', icon: PenLine, label: t.annotate.pen },
    { id: 'rect', icon: Square, label: t.annotate.rect },
    { id: 'arrow', icon: ArrowUpRight, label: t.annotate.arrow },
    { id: 'text', icon: Type, label: t.annotate.text },
  ]
  const btn = 'flex size-10 items-center justify-center rounded-xl text-white/80 transition hover:bg-white/10 hover:text-white active:scale-95 disabled:opacity-30 pointer-coarse:size-12'

  return (
    <div className="flex h-full flex-col">
      <div className="flex shrink-0 flex-wrap items-center gap-1 px-3 pt-[max(0.75rem,env(safe-area-inset-top))] pb-2">
        <button className={btn} aria-label={t.common.cancel} onClick={onClose}>
          <X className="size-5" />
        </button>
        <div className="mx-auto flex items-center gap-1 rounded-2xl bg-white/10 p-1 backdrop-blur">
          {tools.map((x) => (
            <Tip key={x.id} content={x.label}>
              <button aria-label={x.label} aria-pressed={tool === x.id} onClick={() => setTool(x.id)} className={cx(btn, tool === x.id && 'bg-white/20 text-white')}>
                <x.icon className="size-5" />
              </button>
            </Tip>
          ))}
          <span className="mx-1 h-6 w-px bg-white/15" />
          <Tip content={penOnly ? t.annotate.penOnlyOn : t.annotate.penOnlyOff}>
            <button aria-label={t.annotate.penOnly} aria-pressed={penOnly} onClick={() => setPenOnly(!penOnly)} className={cx(btn, penOnly && 'bg-white/20 text-white')}>
              <PenTool className="size-5" />
            </button>
          </Tip>
          <span className="mx-1 h-6 w-px bg-white/15" />
          {COLORS.map((c) => (
            <button key={c} aria-label={c} onClick={() => setColor(c)} className="flex size-10 items-center justify-center pointer-coarse:size-12">
              <span className={cx('size-6 rounded-full border-2 transition', color === c ? 'scale-110 border-white' : 'border-white/30')} style={{ background: c }} />
            </button>
          ))}
          <span className="mx-1 h-6 w-px bg-white/15" />
          <Tip content={t.annotate.undoTip}>
            <button className={btn} aria-label={t.annotate.undo} disabled={!shapes.length} onClick={undo}>
              <Undo2 className="size-5" />
            </button>
          </Tip>
          <Tip content={t.annotate.redoTip}>
            <button className={btn} aria-label={t.annotate.redo} disabled={!redo.length} onClick={again}>
              <Redo2 className="size-5" />
            </button>
          </Tip>
        </div>
        <Button variant="primary" icon={<Check className="size-4" />} loading={busy} disabled={!shapes.length} onClick={finish}>
          {t.annotate.done}
        </Button>
      </div>
      <div ref={wrap} className="relative flex min-h-0 flex-1 touch-none items-center justify-center p-3 pb-[max(0.75rem,env(safe-area-inset-bottom))]">
        {size && (
          <div className="relative overflow-hidden rounded-lg shadow-pop" style={{ width: size.w, height: size.h }}>
            <img src={src} alt={name} draggable={false} className="pointer-events-none absolute inset-0 size-full select-none" />
            <canvas
              ref={cv}
              onPointerDown={down}
              onPointerMove={move}
              onPointerUp={up}
              onPointerCancel={up}
              onDoubleClick={(e) => e.preventDefault()}
              className={cx('absolute inset-0 size-full touch-none', tool === 'text' ? 'cursor-text' : 'cursor-crosshair')}
            />
            {typing && (
              <textarea
                autoFocus={!typing.pen}
                value={typing.text}
                placeholder={typing.pen ? t.annotate.textTapToType : t.annotate.textPlaceholder}
                onChange={(e) => setTyping({ ...typing, text: e.target.value })}
                onBlur={commitText}
                onKeyDown={(e) => {
                  if (e.key === 'Enter' && !e.shiftKey) {
                    e.preventDefault()
                    commitText()
                  }
                  if (e.key === 'Escape') setTyping(null)
                }}
                className="ks-selectable absolute min-w-40 resize-none rounded-md border border-white/60 bg-black/40 px-2 py-1 text-base font-semibold text-white outline-none placeholder:text-white/50"
                style={{ left: `${typing.at.x * 100}%`, top: `${typing.at.y * 100}%`, color }}
              />
            )}
          </div>
        )}
      </div>
    </div>
  )
}

// 标注的结果送去哪里：需求抽屉 / 待验收行等提供一个“收件处”，看图时就能“标注并附上”。
export const AnnotateTarget = createContext<((f: File) => void) | null>(null)
export const useAnnotateTarget = () => useContext(AnnotateTarget)
