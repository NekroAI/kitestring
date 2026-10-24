import { useEffect, useRef, useState, type FocusEvent, type PointerEvent } from 'react'
import { spotRule } from '../lib/spot'

// 聚光（R-215，D-532 选定 A）：悬停 / Tab 聚焦「要你处理」的卡片或会话泳道时，同一个会话的卡片和泳道保持原样，其余卡片和泳道淡到 32%；不画框。
// 触屏点卡片开审批抽屉（或点泳道开会话抽屉）期间保持这组关联，关闭即恢复。
// 元素带 data-spot-key（会话键，没有会话的卡片为空串）；淡出由 index.css 的 [data-spot] 规则和这里按当前键生成的一条选择器完成，
// 不给每个元素加 class、不重画列表。延迟 90ms 再淡是 CSS 的 transition-delay（扫过时不闪），恢复没有延迟——没有 JS 定时器，
// 所以不存在「离开后定时器又把状态设回去」。
// 防「离开了聚光不消失」：
//  - 悬停目标每次 pointerover 重新算：落到不是卡片 / 泳道的元素（空隙、滚动条、栏头、风向）立刻清除；
//  - 整个工作台容器 pointerleave 兜底清除，鼠标离开窗口（relatedTarget 为空）也清除；
//  - 窗口失焦、页面隐藏时清除；任意区域滚动时清除悬停（卡片在光标下移走了）；
//  - Tab 聚焦只认键盘聚焦（:focus-visible），鼠标点击后卡片留着焦点不算。

const keyOf = (t: EventTarget | null) => {
  const el = t instanceof Element ? t.closest('[data-spot-key]') : null
  return el?.getAttribute('data-spot-key') || null
}

export function useSpotlight(pinned: string | null) {
  const [hover, setHover] = useState<string | null>(null)
  const [focus, setFocus] = useState<string | null>(null)
  const [touch, setTouch] = useState(false) // 最近一次按下的是不是手指
  const clear = useRef(() => {})
  clear.current = () => {
    setHover(null)
    setFocus(null)
  }
  useEffect(() => {
    const off = () => clear.current()
    const scrolled = () => setHover(null) // 键盘聚焦时浏览器会把卡片滚进视野，不能因此清掉聚焦
    const out = (e: MouseEvent) => !e.relatedTarget && off() // 鼠标离开窗口
    const hidden = () => document.hidden && off()
    window.addEventListener('blur', off)
    document.addEventListener('visibilitychange', hidden)
    document.addEventListener('scroll', scrolled, { capture: true, passive: true })
    document.documentElement.addEventListener('mouseleave', off)
    document.addEventListener('mouseout', out)
    return () => {
      window.removeEventListener('blur', off)
      document.removeEventListener('visibilitychange', hidden)
      document.removeEventListener('scroll', scrolled, { capture: true })
      document.documentElement.removeEventListener('mouseleave', off)
      document.removeEventListener('mouseout', out)
    }
  }, [])
  const spot = touch ? pinned : hover ?? focus ?? pinned
  return {
    spot,
    handlers: {
      onPointerDownCapture: (e: PointerEvent) => setTouch(e.pointerType === 'touch'),
      onPointerOver: (e: PointerEvent) => {
        if (e.pointerType === 'touch') return
        setHover(keyOf(e.target))
      },
      onPointerLeave: (e: PointerEvent) => {
        if (e.pointerType !== 'touch') setHover(null)
      },
      onFocus: (e: FocusEvent) => setFocus((e.target as Element).matches(':focus-visible') ? keyOf(e.target) : null),
      onBlur: () => setFocus(null),
    },
  }
}

// SpotStyle：当前聚光键对应的一条规则（lib/spot.ts）；没有聚光时不输出。
export function SpotStyle({ spot }: { spot: string | null }) {
  return spot ? <style>{spotRule(spot)}</style> : null
}
