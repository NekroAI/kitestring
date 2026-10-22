import { createContext, useCallback, useContext, useEffect, useLayoutEffect, useRef, useState, type PointerEvent as ReactPointerEvent } from 'react'
import { Dialog } from 'radix-ui'
import { AnimatePresence, motion } from 'motion/react'
import { ArrowUpRight, Check, PenLine, Redo2, Square, Type, Undo2, X } from 'lucide-react'
import { t } from '../i18n'
import { cx } from '../lib/util'
import { Button, Tip } from './base'

// 圈画标注：在截图 / 效果图上画笔、框、箭头、写字，导出为一张新图片附在意见里（D-21 ②）。
// 手写笔：按压力度决定笔画粗细；一旦用过笔，手指触摸不再落笔（防手掌误触）。鼠标、触控板、手指同样可用。
// 所有坐标按图片宽高归一化保存，导出时按原图分辨率重绘，放大后线条依然清楚。

type Pt = { x: number; y: number; p: number }
type Shape =
  | { kind: 'pen'; color: string; w: number; pts: Pt[] }
  | { kind: 'rect' | 'arrow'; color: string; w: number; a: Pt; b: Pt }
  | { kind: 'text'; color: string; size: number; at: Pt; text: string }
type Tool = 'pen' | 'rect' | 'arrow' | 'text'

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

// Annotator：全屏标注编辑器。完成时把“原图 + 标注”导出为 PNG 文件交给 onDone。
export function Annotator({ src, name, open, onClose, onDone }: { src: string; name: string; open: boolean; onClose: () => void; onDone: (f: File) => void }) {
  return (
    <Dialog.Root open={open} onOpenChange={(o) => !o && onClose()}>
      <AnimatePresence>
        {open && (
          <Dialog.Portal forceMount>
            <Dialog.Overlay asChild forceMount>
              <motion.div className="fixed inset-0 z-[80] bg-black/90" initial={{ opacity: 0 }} animate={{ opacity: 1 }} exit={{ opacity: 0 }} />
            </Dialog.Overlay>
            <Dialog.Content forceMount aria-describedby={undefined} onClick={(e) => e.stopPropagation()} className="fixed inset-0 z-[80] outline-none">
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
  const [typing, setTyping] = useState<{ at: Pt; text: string } | null>(null)
  const [busy, setBusy] = useState(false)
  const live = useRef<Shape | null>(null)
  const penSeen = useRef(false)
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

  const down = (e: ReactPointerEvent) => {
    if (e.pointerType === 'pen') penSeen.current = true
    if (penSeen.current && e.pointerType === 'touch') return // 用过笔后忽略手掌 / 手指
    if (active.current !== null || typing) return
    e.currentTarget.setPointerCapture(e.pointerId)
    active.current = e.pointerId
    const p = pos(e)
    if (tool === 'text') {
      setTyping({ at: p, text: '' })
      active.current = null
      return
    }
    const w = tool === 'pen' ? 0.0035 : 0.003
    live.current = tool === 'pen' ? { kind: 'pen', color, w, pts: [p] } : { kind: tool, color, w, a: p, b: p }
    render()
  }
  const move = (e: ReactPointerEvent) => {
    if (e.pointerId !== active.current || !live.current) return
    // 取合并事件，笔迹更顺
    const evs = (e.nativeEvent as PointerEvent).getCoalescedEvents?.() ?? [e.nativeEvent]
    const sh = live.current
    if (sh.kind === 'pen') for (const ce of evs) sh.pts.push(pos(ce as unknown as ReactPointerEvent))
    else if (sh.kind === 'rect' || sh.kind === 'arrow') sh.b = pos(e)
    render()
  }
  const up = (e: ReactPointerEvent) => {
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
          {COLORS.map((c) => (
            <button key={c} aria-label={c} onClick={() => setColor(c)} className="flex size-10 items-center justify-center pointer-coarse:size-12">
              <span className={cx('size-6 rounded-full border-2 transition', color === c ? 'scale-110 border-white' : 'border-white/30')} style={{ background: c }} />
            </button>
          ))}
          <span className="mx-1 h-6 w-px bg-white/15" />
          <Tip content={t.annotate.undo}>
            <button className={btn} aria-label={t.annotate.undo} disabled={!shapes.length} onClick={undo}>
              <Undo2 className="size-5" />
            </button>
          </Tip>
          <Tip content={t.annotate.redo}>
            <button className={btn} aria-label={t.annotate.redo} disabled={!redo.length} onClick={again}>
              <Redo2 className="size-5" />
            </button>
          </Tip>
        </div>
        <Button variant="primary" icon={<Check className="size-4" />} loading={busy} disabled={!shapes.length} onClick={finish}>
          {t.annotate.done}
        </Button>
      </div>
      <div ref={wrap} className="relative flex min-h-0 flex-1 items-center justify-center p-3 pb-[max(0.75rem,env(safe-area-inset-bottom))]">
        {size && (
          <div className="relative overflow-hidden rounded-lg shadow-pop" style={{ width: size.w, height: size.h }}>
            <img src={src} alt={name} draggable={false} className="absolute inset-0 size-full select-none" />
            <canvas
              ref={cv}
              onPointerDown={down}
              onPointerMove={move}
              onPointerUp={up}
              onPointerCancel={up}
              className={cx('absolute inset-0 size-full touch-none', tool === 'text' ? 'cursor-text' : 'cursor-crosshair')}
            />
            {typing && (
              <textarea
                autoFocus
                value={typing.text}
                placeholder={t.annotate.textPlaceholder}
                onChange={(e) => setTyping({ ...typing, text: e.target.value })}
                onBlur={commitText}
                onKeyDown={(e) => {
                  if (e.key === 'Enter' && !e.shiftKey) {
                    e.preventDefault()
                    commitText()
                  }
                  if (e.key === 'Escape') setTyping(null)
                }}
                className="absolute min-w-40 resize-none rounded-md border border-white/60 bg-black/40 px-2 py-1 text-base font-semibold text-white outline-none placeholder:text-white/50"
                style={{ left: `${typing.at.x * 100}%`, top: `${typing.at.y * 100}%`, color }}
              />
            )}
          </div>
        )}
        <p className="pointer-events-none absolute bottom-[max(0.5rem,env(safe-area-inset-bottom))] text-xs text-white/50">{t.annotate.hint}</p>
      </div>
    </div>
  )
}

// 标注的结果送去哪里：需求抽屉 / 待验收行等提供一个“收件处”，看图时就能“标注并附上”。
export const AnnotateTarget = createContext<((f: File) => void) | null>(null)
export const useAnnotateTarget = () => useContext(AnnotateTarget)
