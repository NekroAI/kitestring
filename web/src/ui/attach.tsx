import { useCallback, useEffect, useRef, useState, type ClipboardEvent, type DragEvent, type PointerEvent as ReactPointerEvent, type ReactNode, type SyntheticEvent } from 'react'
import { AnimatePresence, motion } from 'motion/react'
import { Dialog } from 'radix-ui'
import { ChevronLeft, ChevronRight, ImagePlus, Loader2, Maximize2, PenLine, Play, X } from 'lucide-react'
import { Annotator, shield, useAnnotateTarget } from './annotate'
import { toast } from 'sonner'
import { t } from '../i18n'
import { cx } from '../lib/util'
import { Button, Tip } from './base'

// 附件：任何可以写字的地方都可以贴图（粘贴、拖拽、选择文件），也可以附 MP4 / WebM 录屏。

export type FileRef = { id: string; name: string; mime: string; size: number }

export const fileUrl = (id: string) => `/api/files/${encodeURIComponent(id)}`

async function uploadOne(file: File): Promise<FileRef> {
  const fd = new FormData()
  fd.append('file', file, file.name || 'image.png')
  const res = await fetch('/api/files', { method: 'POST', body: fd, credentials: 'same-origin' })
  const body = await res.json().catch(() => ({}))
  if (!res.ok) throw new Error((body as { detail?: string }).detail || t.attach.failed)
  return body as FileRef
}

const MAX_IMAGE = 10 << 20
const MAX_VIDEO = 50 << 20
const ACCEPT = 'image/png,image/jpeg,image/gif,image/webp,video/mp4,video/webm'
const isVideo = (mime: string) => mime.startsWith('video/')
const accepted = (f: File) => f.type.startsWith('image/') || f.type === 'video/mp4' || f.type === 'video/webm'

// useAttach：管理一组附图的上传状态。
export function useAttach(value: FileRef[], onChange: (v: FileRef[]) => void) {
  const [pending, setPending] = useState<{ key: string; url: string; video: boolean }[]>([])
  const latest = useRef(value)
  latest.current = value

  const add = useCallback(
    async (files: File[]) => {
      const list = files.filter(accepted)
      if (!list.length) return
      for (const f of list) {
        if (f.size > (isVideo(f.type) ? MAX_VIDEO : MAX_IMAGE)) {
          toast.error(isVideo(f.type) ? t.attach.videoTooLarge : t.attach.tooLarge)
          continue
        }
        const key = `${f.name}-${f.size}-${Math.random()}`
        const url = URL.createObjectURL(f)
        setPending((p) => [...p, { key, url, video: isVideo(f.type) }])
        try {
          const ref = await uploadOne(f)
          onChange([...latest.current, ref])
        } catch (e) {
          toast.error(e instanceof Error ? e.message : t.attach.failed)
        } finally {
          setPending((p) => p.filter((x) => x.key !== key))
          URL.revokeObjectURL(url)
        }
      }
    },
    [onChange],
  )

  const onPaste = useCallback(
    (e: ClipboardEvent) => {
      const files = [...e.clipboardData.files]
      if (files.some(accepted)) {
        e.preventDefault()
        void add(files)
      }
    },
    [add],
  )

  return { pending, add, onPaste, remove: (id: string) => onChange(latest.current.filter((f) => f.id !== id)) }
}

// Still：GIF 在列表里只显示第一帧，放大后才播放。
function Still({ src, alt }: { src: string; alt: string }) {
  const ref = useRef<HTMLCanvasElement>(null)
  useEffect(() => {
    const img = new Image()
    img.onload = () => {
      const c = ref.current
      if (!c) return
      c.width = img.naturalWidth
      c.height = img.naturalHeight
      c.getContext('2d')?.drawImage(img, 0, 0)
    }
    img.src = src
  }, [src])
  return <canvas ref={ref} aria-label={alt} className="size-full object-cover" />
}

function Preview({ f }: { f: FileRef }) {
  if (isVideo(f.mime)) return <video src={`${fileUrl(f.id)}#t=0.1`} muted playsInline preload="metadata" className="size-full object-cover" />
  if (f.mime === 'image/gif') return <Still src={fileUrl(f.id)} alt={f.name} />
  return <img src={fileUrl(f.id)} alt={f.name} className="size-full object-cover" loading="lazy" />
}

// Thumb：附件缩略图，点击放大；录屏与动图带播放标记。
// group：同组的其它附件，放大后可以左右切换（R-69）。
export function Thumb({ f, onRemove, onAnnotate, size = 'md', group }: { f: FileRef; onRemove?: () => void; onAnnotate?: () => void; size?: 'sm' | 'md'; group?: FileRef[] }) {
  const box = size === 'sm' ? 'size-12' : 'size-16'
  return (
    <span className={cx('group relative inline-block shrink-0', box)}>
      <Lightbox f={f} group={group}>
        <button className={cx('relative block overflow-hidden rounded-lg border border-line bg-sunken transition hover:border-line-strong', box)} aria-label={f.name}>
          <Preview f={f} />
          {(isVideo(f.mime) || f.mime === 'image/gif') && (
            <span className="absolute inset-0 m-auto flex size-6 items-center justify-center rounded-full bg-black/55 text-white">
              <Play className="size-3 translate-x-px fill-current" />
            </span>
          )}
        </button>
      </Lightbox>
      {onAnnotate && f.mime.startsWith('image/') && (
        <button onClick={onAnnotate} aria-label={t.annotate.title} className="absolute -bottom-1.5 -left-1.5 flex size-5 pointer-coarse:size-7 items-center justify-center rounded-full border border-line bg-surface text-fg-2 shadow-xs opacity-0 transition group-hover:opacity-100 hover:text-accent focus:opacity-100 max-lg:opacity-100 pointer-coarse:opacity-100">
          <PenLine className="size-3" />
        </button>
      )}
      {onRemove && (
        <button onClick={onRemove} aria-label={t.attach.remove} className="absolute -top-1.5 -right-1.5 flex size-5 pointer-coarse:size-7 items-center justify-center rounded-full border border-line bg-surface text-fg-2 shadow-xs opacity-0 transition group-hover:opacity-100 hover:text-danger focus:opacity-100 max-lg:opacity-100 pointer-coarse:opacity-100">
          <X className="size-3" />
        </button>
      )}
    </span>
  )
}

export function Thumbs({ files, size }: { files?: FileRef[] | null; size?: 'sm' | 'md' }) {
  if (!files?.length) return null
  return (
    <div className="flex flex-wrap gap-2">
      {files.map((f) => (
        <Thumb key={f.id} f={f} size={size} group={files} />
      ))}
    </div>
  )
}

function Lightbox({ f, group, children }: { f: FileRef; group?: FileRef[]; children: ReactNode }) {
  const [open, setOpen] = useState(false)
  const [cur, setCur] = useState(f)
  const stop = (e: SyntheticEvent) => e.stopPropagation()
  // 同组里能看的（图片、录屏）；不在组里就只看这一张
  const list = group?.some((x) => x.id === f.id) ? group : [f]
  const i = Math.max(0, list.findIndex((x) => x.id === cur.id))
  const go = (d: number) => setCur(list[(i + d + list.length) % list.length])
  return (
    <Dialog.Root
      open={open}
      onOpenChange={(o) => {
        if (o) setCur(f)
        setOpen(o)
      }}
    >
      <Dialog.Trigger asChild onClick={stop}>
        {children}
      </Dialog.Trigger>
      <AnimatePresence>
        {open && (
          <Dialog.Portal forceMount>
            <Dialog.Overlay asChild forceMount>
              <motion.div className="fixed inset-0 z-[70] bg-black/85" initial={{ opacity: 0 }} animate={{ opacity: 1 }} exit={{ opacity: 0 }} transition={{ duration: 0.2 }} />
            </Dialog.Overlay>
            <Dialog.Content forceMount aria-describedby={undefined} {...shield} className="fixed inset-0 z-[70] outline-none">
              <Dialog.Title className="sr-only">{cur.name}</Dialog.Title>
              <Viewer key={cur.id} f={cur} onClose={() => setOpen(false)} nav={list.length > 1 ? { i, n: list.length, go, next: list[(i + 1) % list.length], prev: list[(i - 1 + list.length) % list.length] } : undefined} />
            </Dialog.Content>
          </Dialog.Portal>
        )}
      </AnimatePresence>
    </Dialog.Root>
  )
}

const clamp = (v: number, lo: number, hi: number) => Math.min(hi, Math.max(lo, v))
type View = { s: number; x: number; y: number }

// Viewer：看大图。触控板双指捏合缩放、双指移动平移；触屏双指缩放、单指拖动、下滑关闭；双击放大 / 复原。
// nav：同组切换。左右箭头、键盘 ← →、触屏左右滑（未放大时）、触控板双指左右滑。
type Nav = { i: number; n: number; go: (d: number) => void; next: FileRef; prev: FileRef }

function Viewer({ f, onClose, nav }: { f: FileRef; onClose: () => void; nav?: Nav }) {
  const box = useRef<HTMLDivElement>(null)
  const [v, setV] = useState<View>({ s: 1, x: 0, y: 0 })
  const [smooth, setSmooth] = useState(false)
  const cur = useRef(v)
  cur.current = v
  const pts = useRef(new Map<number, { x: number; y: number }>())
  const pinch = useRef<{ d: number; c: { x: number; y: number }; base: View } | null>(null)
  const drag = useRef<{ x: number; y: number; base: View; touch: boolean } | null>(null)
  const moved = useRef(false)
  const target = useAnnotateTarget()
  const [annotating, setAnnotating] = useState(false)
  const onBackdrop = useRef(false) // 按下时是否在图片外（指针捕获后 click 的 target 不可靠）
  const video = isVideo(f.mime)

  // 以屏幕点 (cx, cy)（相对视口中心）为锚点缩放到 ns。
  const zoomAt = (ns: number, cx: number, cy: number, base = cur.current): View => {
    const s = clamp(ns, 1, 8)
    if (s === 1) return { s: 1, x: 0, y: 0 }
    const k = s / base.s
    return { s, x: cx - (cx - base.x) * k, y: cy - (cy - base.y) * k }
  }
  const rel = (x: number, y: number) => {
    const r = box.current!.getBoundingClientRect()
    return { x: x - r.left - r.width / 2, y: y - r.top - r.height / 2 }
  }
  const animate = (nv: View) => {
    setSmooth(true)
    setV(nv)
    window.setTimeout(() => setSmooth(false), 280)
  }

  const navRef = useRef(nav)
  navRef.current = nav
  useEffect(() => {
    // 看图时左右键归看图器：先于后面的需求抽屉（它也用左右键切换需求）拿到并拦下
    const onKey = (e: KeyboardEvent) => {
      if (e.key !== 'ArrowRight' && e.key !== 'ArrowLeft') return
      e.stopImmediatePropagation()
      if (!navRef.current || cur.current.s > 1) return
      navRef.current.go(e.key === 'ArrowRight' ? 1 : -1)
    }
    window.addEventListener('keydown', onKey, true)
    return () => window.removeEventListener('keydown', onKey, true)
  }, [])
  // 预取相邻两张，切换时不闪白
  useEffect(() => {
    for (const x of [nav?.next, nav?.prev]) if (x && !isVideo(x.mime)) new Image().src = fileUrl(x.id)
  }, [nav?.next, nav?.prev])

  useEffect(() => {
    const el = box.current
    if (!el || video) return
    // 触控板：捏合是带 ctrlKey 的滚轮事件，双指移动是普通滚轮事件；没放大时双指左右滑切换同组图片。
    let swipe = 0
    let swipeAt = 0
    const onWheel = (e: WheelEvent) => {
      e.preventDefault()
      const p = rel(e.clientX, e.clientY)
      if (e.ctrlKey) setV(zoomAt(cur.current.s * Math.exp(-e.deltaY * 0.01), p.x, p.y))
      else if (cur.current.s > 1) setV({ ...cur.current, x: cur.current.x - e.deltaX, y: cur.current.y - e.deltaY })
      else if (navRef.current && Math.abs(e.deltaX) > Math.abs(e.deltaY)) {
        const now = Date.now()
        if (now - swipeAt < 450) return // 一次滑动的惯性余波不再触发
        swipe += e.deltaX
        if (Math.abs(swipe) > 60) {
          navRef.current.go(swipe > 0 ? 1 : -1)
          swipe = 0
          swipeAt = now
        }
      }
    }
    // Safari 的触控板捏合走手势事件。
    let gBase = cur.current
    const onGStart = (e: Event) => {
      e.preventDefault()
      gBase = cur.current
    }
    const onGChange = (e: Event) => {
      e.preventDefault()
      const g = e as Event & { scale: number; clientX: number; clientY: number }
      const p = rel(g.clientX, g.clientY)
      setV(zoomAt(gBase.s * g.scale, p.x, p.y, gBase))
    }
    el.addEventListener('wheel', onWheel, { passive: false })
    el.addEventListener('gesturestart', onGStart)
    el.addEventListener('gesturechange', onGChange)
    return () => {
      el.removeEventListener('wheel', onWheel)
      el.removeEventListener('gesturestart', onGStart)
      el.removeEventListener('gesturechange', onGChange)
    }
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [video])

  const onDown = (e: ReactPointerEvent) => {
    onBackdrop.current = e.target === e.currentTarget
    if (video) return
    ;(e.currentTarget as HTMLElement).setPointerCapture(e.pointerId)
    pts.current.set(e.pointerId, { x: e.clientX, y: e.clientY })
    moved.current = false
    if (pts.current.size === 2) {
      const [a, b] = [...pts.current.values()]
      pinch.current = { d: Math.hypot(a.x - b.x, a.y - b.y), c: rel((a.x + b.x) / 2, (a.y + b.y) / 2), base: cur.current }
      drag.current = null
    } else if (pts.current.size === 1) {
      drag.current = { x: e.clientX, y: e.clientY, base: cur.current, touch: e.pointerType === 'touch' }
    }
  }
  const onMove = (e: ReactPointerEvent) => {
    if (!pts.current.has(e.pointerId)) return
    pts.current.set(e.pointerId, { x: e.clientX, y: e.clientY })
    if (pinch.current && pts.current.size >= 2) {
      const [a, b] = [...pts.current.values()]
      const c = rel((a.x + b.x) / 2, (a.y + b.y) / 2)
      const { d, c: c0, base } = pinch.current
      const nv = zoomAt((base.s * Math.hypot(a.x - b.x, a.y - b.y)) / d, c0.x, c0.y, base)
      setV(nv.s === 1 ? nv : { ...nv, x: nv.x + c.x - c0.x, y: nv.y + c.y - c0.y })
      moved.current = true
      return
    }
    const g = drag.current
    if (!g) return
    const dx = e.clientX - g.x
    const dy = e.clientY - g.y
    if (Math.abs(dx) + Math.abs(dy) > 4) moved.current = true
    if (g.base.s > 1) setV({ ...g.base, x: g.base.x + dx, y: g.base.y + dy })
    else if (g.touch && nav && Math.abs(dx) > Math.abs(dy)) setV({ s: 1, x: dx, y: 0 }) // 触屏左右滑切换
    else if (g.touch) setV({ s: 1, x: 0, y: Math.max(0, dy) }) // 触屏下滑关闭
  }
  const onUp = (e: ReactPointerEvent) => {
    pts.current.delete(e.pointerId)
    if (pinch.current && pts.current.size < 2) {
      pinch.current = null
      const [rest] = [...pts.current.entries()]
      drag.current = rest ? { x: rest[1].x, y: rest[1].y, base: cur.current, touch: true } : null
      return
    }
    const g = drag.current
    drag.current = null
    if (g && g.base.s === 1 && cur.current.x !== 0) {
      if (nav && Math.abs(cur.current.x) > 70) nav.go(cur.current.x < 0 ? 1 : -1)
      else animate({ s: 1, x: 0, y: 0 })
      return
    }
    if (g && g.base.s === 1 && cur.current.y > 0) {
      if (cur.current.y > 120) onClose()
      else animate({ s: 1, x: 0, y: 0 })
    }
  }
  const onDouble = (e: React.MouseEvent) => {
    if (video) return
    const p = rel(e.clientX, e.clientY)
    animate(cur.current.s > 1 ? { s: 1, x: 0, y: 0 } : zoomAt(2.5, p.x, p.y))
  }
  const fade = v.s === 1 && v.y > 0 ? Math.max(0.3, 1 - v.y / 400) : 1

  return (
    <>
      <div
        ref={box}
        onPointerDown={onDown}
        onPointerMove={onMove}
        onPointerUp={onUp}
        onPointerCancel={onUp}
        onDoubleClick={onDouble}
        onClick={() => onBackdrop.current && !moved.current && onClose()}
        className={cx('absolute inset-0 flex items-center justify-center overflow-hidden p-4 select-none', !video && 'touch-none', v.s > 1 ? 'cursor-grab active:cursor-grabbing' : 'cursor-zoom-in')}
        style={{ opacity: fade }}
      >
        <motion.div initial={{ opacity: 0, scale: 0.94 }} animate={{ opacity: 1, scale: 1 }} exit={{ opacity: 0, scale: 0.94 }} transition={{ duration: 0.24, ease: [0.2, 0.8, 0.2, 1] }} className="pointer-events-none flex max-h-full max-w-full items-center justify-center">
          {video ? (
            <video src={fileUrl(f.id)} controls autoPlay playsInline className="pointer-events-auto max-h-[calc(100dvh-2rem)] max-w-[calc(100vw-2rem)] rounded-lg bg-black shadow-pop" />
          ) : (
            <img
              src={fileUrl(f.id)}
              alt={f.name}
              draggable={false}
              className="pointer-events-auto max-h-[calc(100dvh-2rem)] max-w-[calc(100vw-2rem)] rounded-lg object-contain shadow-pop will-change-transform"
              style={{ transform: `translate3d(${v.x}px, ${v.y}px, 0) scale(${v.s})`, transition: smooth ? 'transform 0.26s cubic-bezier(0.2, 0.8, 0.2, 1)' : 'none' }}
            />
          )}
        </motion.div>
      </div>
      {nav && (
        <>
          <button aria-label={t.attach.prev} onClick={() => nav.go(-1)} className="absolute top-1/2 left-3 flex size-11 -translate-y-1/2 items-center justify-center rounded-full bg-black/45 text-white backdrop-blur transition hover:bg-black/70 active:scale-95 max-sm:hidden">
            <ChevronLeft className="size-6" />
          </button>
          <button aria-label={t.attach.next} onClick={() => nav.go(1)} className="absolute top-1/2 right-3 flex size-11 -translate-y-1/2 items-center justify-center rounded-full bg-black/45 text-white backdrop-blur transition hover:bg-black/70 active:scale-95 max-sm:hidden">
            <ChevronRight className="size-6" />
          </button>
        </>
      )}
      <motion.div initial={{ opacity: 0 }} animate={{ opacity: 1 }} exit={{ opacity: 0 }} className="pointer-events-none absolute inset-x-0 top-0 flex items-center justify-between p-3 pt-[max(0.75rem,env(safe-area-inset-top))]">
        <span className="rounded-full bg-black/50 px-3 py-1 text-xs text-white tabular backdrop-blur" style={{ visibility: nav ? 'visible' : 'hidden' }}>
          {nav ? t.attach.position(nav.i + 1, nav.n) : ''}
        </span>
        <Dialog.Close asChild>
          <button aria-label={t.common.close} className="pointer-events-auto flex size-11 items-center justify-center rounded-full bg-black/50 text-white backdrop-blur transition hover:bg-black/70 active:scale-95">
            <X className="size-5" />
          </button>
        </Dialog.Close>
      </motion.div>
      <motion.div initial={{ opacity: 0 }} animate={{ opacity: 1 }} exit={{ opacity: 0 }} className="pointer-events-none absolute inset-x-0 bottom-0 flex items-center justify-between gap-2 p-4 pb-[max(1rem,env(safe-area-inset-bottom))]">
        <span className="flex items-center gap-2">
          {target && !video && (
            <button onClick={() => setAnnotating(true)} className="pointer-events-auto flex items-center gap-1.5 rounded-lg bg-white/90 px-3 py-1.5 text-xs font-medium text-black backdrop-blur hover:bg-white">
              <PenLine className="size-3.5" />
              {t.annotate.attach}
            </button>
          )}
          {v.s > 1 && (
            <button onClick={() => animate({ s: 1, x: 0, y: 0 })} className="pointer-events-auto rounded-lg bg-black/55 px-3 py-1.5 text-xs text-white tabular backdrop-blur hover:bg-black/75">
              {t.attach.zoomReset(Math.round(v.s * 100))}
            </button>
          )}
        </span>
        <a href={fileUrl(f.id)} target="_blank" rel="noreferrer" className="pointer-events-auto rounded-lg bg-black/55 px-3 py-1.5 text-xs text-white backdrop-blur hover:bg-black/75">
          {video ? t.attach.originalFile : t.attach.original}
        </a>
      </motion.div>
      {target && (
        <Annotator
          src={fileUrl(f.id)}
          name={f.name}
          open={annotating}
          onClose={() => setAnnotating(false)}
          onDone={(file) => {
            target(file)
            onClose()
          }}
        />
      )}
    </>
  )
}

// ZoomButton：放大查看某张图（用于方案对比卡等不想显示缩略图的地方）。
export function ZoomButton({ f, className, group }: { f: FileRef; className?: string; group?: FileRef[] }) {
  return (
    <Lightbox f={f} group={group}>
      <button aria-label={t.attach.zoom} className={cx('flex size-8 items-center justify-center rounded-lg bg-black/45 text-white backdrop-blur transition hover:bg-black/65 active:scale-95 pointer-coarse:size-10', className)}>
        <Maximize2 className="size-4" />
      </button>
    </Lightbox>
  )
}

// AttachZone：包住输入框，支持粘贴与拖拽；下方显示附图与“添加图片”按钮。
export function AttachZone({ value, onChange, children, className, compact, incoming, onIncoming }: { value: FileRef[]; onChange: (v: FileRef[]) => void; children: ReactNode; className?: string; compact?: boolean; incoming?: File[]; onIncoming?: () => void }) {
  const { pending, add, onPaste, remove } = useAttach(value, onChange)
  const [annotating, setAnnotating] = useState<FileRef | null>(null)
  // 从看图处“标注并附上”送来的图片：上传后加进来
  useEffect(() => {
    if (incoming?.length) {
      void add(incoming)
      onIncoming?.()
    }
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [incoming])
  const [over, setOver] = useState(false)
  const input = useRef<HTMLInputElement>(null)
  const onDrop = (e: DragEvent) => {
    e.preventDefault()
    setOver(false)
    void add([...e.dataTransfer.files])
  }
  return (
    <div
      onPaste={onPaste}
      onDragOver={(e) => {
        if ([...e.dataTransfer.items].some((i) => i.kind === 'file')) {
          e.preventDefault()
          setOver(true)
        }
      }}
      onDragLeave={() => setOver(false)}
      onDrop={onDrop}
      className={cx('relative rounded-xl transition', over && 'ring-2 ring-accent/50', className)}
    >
      {children}
      <div className={cx('flex flex-wrap items-center gap-2', (value.length || pending.length || !compact) && 'mt-2')}>
        {value.map((f) => (
          <Thumb key={f.id} f={f} size="sm" onRemove={() => remove(f.id)} onAnnotate={() => setAnnotating(f)} group={value} />
        ))}
        {pending.map((p) => (
          <span key={p.key} className="relative size-12 overflow-hidden rounded-lg border border-line">
            {p.video ? <video src={p.url} muted className="size-full object-cover opacity-50" /> : <img src={p.url} alt="" className="size-full object-cover opacity-50" />}
            <Loader2 className="absolute inset-0 m-auto size-4 animate-spin text-fg" />
          </span>
        ))}
        {!compact && <AddImageButton onPick={() => input.current?.click()} />}
      </div>
      <input ref={input} type="file" accept={ACCEPT} multiple hidden onChange={(e) => void add([...(e.target.files ?? [])]).then(() => (e.target.value = ''))} />
      {compact && <AttachTrigger onPick={() => input.current?.click()} />}
      {over && <div className="pointer-events-none absolute inset-0 flex items-center justify-center rounded-xl bg-accent-soft/80 text-sm font-medium text-accent">{t.attach.drop}</div>}
      {annotating && (
        <Annotator
          src={fileUrl(annotating.id)}
          name={annotating.name}
          open
          onClose={() => setAnnotating(null)}
          onDone={(file) => {
            const old = annotating.id
            void add([file]).then(() => remove(old)) // 标注后的图替换原图
          }}
        />
      )}
    </div>
  )
}

function AddImageButton({ onPick }: { onPick: () => void }) {
  return (
    <Tip content={t.attach.hint}>
      <button type="button" onClick={onPick} className="flex h-8 items-center gap-1.5 rounded-lg border border-dashed border-line-strong px-2.5 text-xs text-fg-3 transition hover:border-accent hover:text-accent">
        <ImagePlus className="size-3.5" />
        {t.attach.add}
      </button>
    </Tip>
  )
}

// 紧凑模式下的触发按钮：放在输入框右下角。
function AttachTrigger({ onPick }: { onPick: () => void }) {
  return (
    <span className="absolute top-1.5 right-1.5">
      <Tip content={t.attach.hint}>
        <Button type="button" size="icon-sm" variant="ghost" aria-label={t.attach.add} onClick={onPick}>
          <ImagePlus className="size-4" />
        </Button>
      </Tip>
    </span>
  )
}
