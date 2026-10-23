import { useEffect, useRef, useState, type ReactNode } from 'react'
import { createPortal } from 'react-dom'

// 触屏拖拽（R-55）：网页原生拖放在 iPad / 手机上不起作用，这里用触摸事件自己做。
// 长按约 0.3 秒卡片“浮起”（轻震），随后移动手指即进入拖拽，影子跟着手指走；
// 松手时落在哪个带 data-drop 的区域，就把它交给 onDrop。浮起后不动继续按住，照常弹出长按菜单（Ctx）。

export const dragState = { active: false }
export const DRAG_START = 'ks-touch-drag-start'

const LIFT_MS = 300
const SLOP = 10

type Drag = { id: string; x: number; y: number; w: number; ghost: ReactNode }

export function useTouchDrag(onDrop: (id: string, target: string) => void) {
  const [drag, setDrag] = useState<Drag | null>(null)
  const [over, setOver] = useState<string | null>(null)
  const st = useRef<{ id: string; x0: number; y0: number; lifted: boolean; moving: boolean; timer: number; w: number; ghost: ReactNode } | null>(null)
  const drop = useRef(onDrop)
  drop.current = onDrop

  useEffect(() => {
    // 拖到屏幕上下边缘时自动滚动，目标列在屏幕外也能放
    let raf = 0
    let last = { x: 0, y: 0 }
    const EDGE = 72
    const tick = () => {
      const s = st.current
      if (!s?.moving) return
      const { y } = last
      const v = y < EDGE ? -Math.ceil((EDGE - y) / 6) : y > innerHeight - EDGE ? Math.ceil((y - innerHeight + EDGE) / 6) : 0
      if (v) {
        window.scrollBy(0, v)
        setOver(target(last.x, last.y))
      }
      raf = requestAnimationFrame(tick)
    }
    const target = (x: number, y: number) => (document.elementFromPoint(x, y)?.closest('[data-drop]') as HTMLElement | null)?.dataset.drop ?? null
    const move = (e: TouchEvent) => {
      const s = st.current
      if (!s || e.touches.length !== 1) return
      const t = e.touches[0]
      if (!s.lifted) {
        if (Math.hypot(t.clientX - s.x0, t.clientY - s.y0) > SLOP) end() // 还没浮起就移动：是在滚动
        return
      }
      e.preventDefault() // 浮起后不滚动页面
      if (!s.moving && Math.hypot(t.clientX - s.x0, t.clientY - s.y0) > SLOP) {
        s.moving = true
        dragState.active = true
        document.dispatchEvent(new Event(DRAG_START))
      }
      if (s.moving) {
        if (!raf) raf = requestAnimationFrame(tick)
        last = { x: t.clientX, y: t.clientY }
        setDrag({ id: s.id, x: t.clientX, y: t.clientY, w: s.w, ghost: s.ghost })
        setOver(target(t.clientX, t.clientY))
      }
    }
    const up = (e: TouchEvent) => {
      const s = st.current
      if (s?.moving) {
        const t = e.changedTouches[0]
        const to = t && target(t.clientX, t.clientY)
        if (to) drop.current(s.id, to)
        // 吞掉松手后的那次点击，不要打开卡片
        const swallow = (ev: Event) => {
          ev.stopPropagation()
          ev.preventDefault()
        }
        window.addEventListener('click', swallow, { capture: true, once: true })
        window.setTimeout(() => window.removeEventListener('click', swallow, { capture: true }), 400)
      }
      end()
    }
    const end = () => {
      cancelAnimationFrame(raf)
      raf = 0
      if (st.current) window.clearTimeout(st.current.timer)
      st.current = null
      dragState.active = false
      setDrag(null)
      setOver(null)
    }
    document.addEventListener('touchmove', move, { passive: false })
    document.addEventListener('touchend', up)
    document.addEventListener('touchcancel', end)
    return () => {
      document.removeEventListener('touchmove', move)
      document.removeEventListener('touchend', up)
      document.removeEventListener('touchcancel', end)
    }
  }, [])

  // 给可拖的元素用：onTouchStart 开始计时
  const source = (id: string, ghost: ReactNode) => ({
    onTouchStart: (e: React.TouchEvent) => {
      if (e.touches.length !== 1) return
      const t = e.touches[0]
      const w = (e.currentTarget as HTMLElement).getBoundingClientRect().width
      const s = { id, x0: t.clientX, y0: t.clientY, lifted: false, moving: false, w, ghost, timer: 0 }
      s.timer = window.setTimeout(() => {
        s.lifted = true
        navigator.vibrate?.(8)
      }, LIFT_MS)
      st.current = s
    },
    'data-dragging': drag?.id === id ? '' : undefined,
  })

  const layer =
    drag &&
    createPortal(
      <div className="pointer-events-none fixed z-[70] -translate-x-1/2 -translate-y-1/2 rotate-[-2deg] rounded-xl shadow-pop" style={{ left: drag.x, top: drag.y, width: Math.min(drag.w, 320) }}>
        {drag.ghost}
      </div>,
      document.body,
    )

  return { source, over, dragging: drag?.id ?? null, layer }
}
