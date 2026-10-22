import type { ReactNode } from 'react'
import { AnimatePresence, motion, type Transition } from 'motion/react'
import { cx } from '../lib/util'

// 元素出现与消失都不能“瞬间跳变”：高度平滑展开 / 收起，周围的内容跟着移动。

export const ease = [0.2, 0.8, 0.2, 1] as const
export const spring: Transition = { type: 'spring', bounce: 0.15, duration: 0.4 }
const t: Transition = { duration: 0.28, ease }

// 动画过程中裁剪内容，结束后恢复可见，避免长期裁掉阴影、焦点框与折角。
const closed = { height: 0, opacity: 0, overflow: 'hidden' as const }
const opened = { height: 'auto', opacity: 1, transitionEnd: { overflow: 'visible' as const } }

// Collapse：按 show 展开 / 收起一块内容。
export function Collapse({ show, children, className }: { show: boolean; children: ReactNode; className?: string }) {
  return (
    <AnimatePresence initial={false}>
      {show && (
        <motion.div key="collapse" initial={closed} animate={opened} exit={closed} transition={t} className={className}>
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
    <div className={className} style={space ? { marginBottom: -space } : undefined}>
      <AnimatePresence initial={false}>{children}</AnimatePresence>
    </div>
  )
}

export function Item({ children, space = 0, className, layout }: { children: ReactNode; space?: number; className?: string; layout?: boolean }) {
  return (
    <motion.div initial={closed} animate={opened} exit={closed} transition={t} layout={layout ? 'position' : undefined} className={cx('min-w-0', className)}>
      <div style={space ? { paddingBottom: space } : undefined} className="h-full">
        {children}
      </div>
    </motion.div>
  )
}
