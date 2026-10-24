import type { ReactNode } from 'react'
import { AnimatePresence, motion, type Transition } from 'motion/react'
import { useEffect, useState } from 'react'
import { cx } from '../lib/util'
import { spring } from './gesture'

// 元素出现与消失都不能“瞬间跳变”：高度平滑展开 / 收起，周围的内容跟着移动。

export const ease = [0.2, 0.8, 0.2, 1] as const
const t: Transition = { duration: 0.28, ease }
// fluid：工作台的列表（R-216）用 R-73 的那一套弹簧（ui/gesture 的 spring，减少动态效果时直接到位），不用上面的缓动。
const tr = (fluid?: boolean): Transition => (fluid ? spring() : t)

// useSettled：ready 持续为真 ms 毫秒之后才返回 true。首次加载（骨架换成内容、几路数据陆续到齐）这段时间里的出现不算进场，不播动画；
// 之后的增、删、换位置才播。
export function useSettled(ready: boolean, ms = 800) {
  const [on, setOn] = useState(false)
  useEffect(() => {
    if (!ready) return
    const id = window.setTimeout(() => setOn(true), ms)
    return () => window.clearTimeout(id)
  }, [ready, ms])
  return on
}

// 动画过程中裁剪内容，结束后恢复可见，避免长期裁掉阴影、焦点框与折角。
const closed = { height: 0, opacity: 0, overflow: 'hidden' as const }
const opened = { height: 'auto', opacity: 1, transitionEnd: { overflow: 'visible' as const } }

// Collapse：按 show 展开 / 收起一块内容。
export function Collapse({ show, children, className, fluid, still }: { show: boolean; children: ReactNode; className?: string; fluid?: boolean; still?: boolean }) {
  return (
    <AnimatePresence initial={false}>
      {show && (
        <motion.div key="collapse" initial={still ? false : closed} animate={opened} exit={still ? undefined : closed} transition={tr(fluid)} className={className}>
          {children}
        </motion.div>
      )}
    </AnimatePresence>
  )
}

// List / Item：列表增删时逐项展开或收起高度，其余项平滑让位。
// 项之间的间距由 Item 的 space 提供（不要在外层用 space-y / gap，否则收起到最后会跳一下）。
export function List({ children, space = 0, className }: { children: ReactNode; space?: number; className?: string }) {
  return (
    <div data-skel-flat className={cx('min-w-0', className)} style={space ? { marginBottom: -space } : undefined}>
      <AnimatePresence initial={false}>{children}</AnimatePresence>
    </div>
  )
}

// fluid：用全站那套弹簧；still：这一刻不播进出场（首次加载中），换位置仍然平滑。
export function Item({ children, space = 0, className, layout, fluid, still }: { children: ReactNode; space?: number; className?: string; layout?: boolean; fluid?: boolean; still?: boolean }) {
  return (
    <motion.div data-skel-wrap initial={still ? false : closed} animate={opened} exit={still ? undefined : closed} transition={still ? { duration: 0 } : tr(fluid)} layout={layout ? 'position' : undefined} className={cx('min-w-0', className)}>
      <div style={space ? { paddingBottom: space } : undefined} className="h-full">
        {children}
      </div>
    </motion.div>
  )
}
