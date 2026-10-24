import { useEffect, useMemo } from 'react'
import { cx, loadPref, savePref } from '../lib/util'
import { Skeleton } from './base'

// 按上次的样子画加载骨架（R-211）：区域加载完后记下它每个子块的高度、是不是卡片，下次加载时按同样的块画骨架，
// 加载完成时位置不跳。没记过时用调用方给的默认形状。

export type Block = { h: number; card?: boolean; fill?: boolean }
export type Shape = Block[]

const maxBlocks = 40

// isCard：有底色或阴影的块画成卡片骨架，否则是一行文字（分组标题、时间轴之类）。
function isCard(el: HTMLElement) {
  const cs = getComputedStyle(el)
  if (cs.boxShadow && cs.boxShadow !== 'none') return true
  const bg = cs.backgroundColor
  return !!bg && bg !== 'transparent' && !/rgba\(.*,\s*0\)$/.test(bg)
}

// blocksOf：容器里的各块。带 data-skel-flat 的包装（列表动画的 List）不算一块，取它里面的；带 data-skel-wrap 的（列表动画的 Item）取里面真正的内容。
function blocksOf(el: HTMLElement): HTMLElement[] {
  const out: HTMLElement[] = []
  for (const c of Array.from(el.children) as HTMLElement[]) {
    if (c.dataset.skelFlat !== undefined) out.push(...blocksOf(c))
    else if (c.dataset.skelWrap !== undefined) out.push((c.firstElementChild?.firstElementChild as HTMLElement | null) ?? c)
    else out.push(c)
  }
  return out
}

// useShape：读出上次记下的形状；ready 之后（且 record 为真）持续记下 el 的子块（高度变化时更新）。
// 子块带 data-fill 的在骨架里撑满剩余高度（如「最近动态」），不按记下的高度画。
export function useShape(key: string, el: HTMLElement | null, ready: boolean, fallback: Shape, record = true): Shape {
  // 按键读（版式在首帧量出宽度后才定下来，键会变一次）；之后本页记下的新形状留到下次加载再用
  const shape = useMemo<Shape>(() => {
    const v = loadPref<Shape | null>(key, null)
    return Array.isArray(v) ? v : fallback
  }, [key]) // eslint-disable-line react-hooks/exhaustive-deps
  useEffect(() => {
    if (!el || !ready || !record) return
    let timer = 0
    const save = () => {
      window.clearTimeout(timer)
      timer = window.setTimeout(() => {
        const out: Shape = []
        for (const c of blocksOf(el)) {
          const h = Math.round(c.getBoundingClientRect().height)
          if (!h || c.dataset.skelSkip !== undefined) continue
          out.push({ h, card: isCard(c) || undefined, fill: c.dataset.fill !== undefined || undefined })
          if (out.length >= maxBlocks) break
        }
        savePref(key, out)
      }, 400)
    }
    const ro = new ResizeObserver(save)
    ro.observe(el)
    const watch = () => {
      for (const c of blocksOf(el)) ro.observe(c)
      for (const f of Array.from(el.querySelectorAll('[data-skel-flat]'))) mo.observe(f, { childList: true })
    }
    const mo = new MutationObserver(() => {
      watch()
      save()
    })
    mo.observe(el, { childList: true })
    watch()
    save()
    return () => {
      window.clearTimeout(timer)
      ro.disconnect()
      mo.disconnect()
    }
  }, [key, el, ready, record])
  return shape
}

// CardBlock：一块卡片骨架，按高度放两三行微光条。
export function CardBlock({ h, className, radius = 'rounded-[14px]' }: { h: number; className?: string; radius?: string }) {
  const lines = h >= 120 ? 3 : h >= 64 ? 2 : 1
  return (
    <div className={cx('flex shrink-0 flex-col justify-center gap-2.5 overflow-hidden bg-surface px-4 shadow-card', radius, className)} style={{ height: h }}>
      <Skeleton className="h-3.5 w-1/3" />
      {lines >= 2 && <Skeleton className="h-4 w-4/5" />}
      {lines >= 3 && <Skeleton className="h-3.5 w-1/2" />}
    </div>
  )
}

// ShapeSkeleton：按形状画骨架（放进和真实内容同一个容器里，间距由容器给）。
export function ShapeSkeleton({ shape, className }: { shape: Shape; className?: string }) {
  return (
    <>
      {shape.map((b, i) =>
        b.fill ? (
          <div key={i} className={cx('flex min-h-0 flex-1 flex-col gap-2.5 rounded-[14px] bg-surface px-3.5 py-3 shadow-card', className)}>
            <Skeleton className="h-4 w-1/4" />
            <Skeleton className="h-3.5 w-4/5" />
            <Skeleton className="h-3.5 w-3/5" />
          </div>
        ) : b.card ? (
          <CardBlock key={i} h={b.h} className={className} />
        ) : b.h > 48 ? (
          // 没有底色的大块（如「最近结束」一组）：描边框 + 几行
          <div key={i} aria-hidden className="flex shrink-0 flex-col gap-3 overflow-hidden rounded-[14px] px-4 py-4 ring-1 ring-line ring-inset" style={{ height: b.h }}>
            <Skeleton className="h-3 w-1/5" />
            <Skeleton className="h-3.5 w-3/5" />
            {b.h > 110 && <Skeleton className="h-3.5 w-2/5" />}
          </div>
        ) : (
          <div key={i} aria-hidden className="flex shrink-0 items-center px-1" style={{ height: b.h }}>
            <Skeleton className="h-3 w-1/4" />
          </div>
        ),
      )}
    </>
  )
}

// ---------- 按上次的版面画骨架（盘面这类多栏网格） ----------

// Ghost：区域里各块的位置（x、宽按记录时的容器宽度换成百分比，y、高按像素），kind 0 = 分区底板，1 = 卡片，2 = 一行
export type Ghost = { w: number; h: number; b: [number, number, number, number, 0 | 1 | 2][] }

const ghostSel = 'section, [data-tile], [data-nav-item], [data-skel-kids] > *'
const maxBoxes = 240

function hasBg(el: HTMLElement) {
  const bg = getComputedStyle(el).backgroundColor
  return !!bg && bg !== 'transparent' && !/rgba\(.*,\s*0\)$/.test(bg)
}

// visibleRect：被滚动区裁掉的部分不记（列里滚到下面的需求块不画进骨架）
function visibleRect(c: HTMLElement, root: HTMLElement) {
  const r = c.getBoundingClientRect()
  let top = r.top
  let bottom = r.bottom
  for (let p = c.parentElement; p && p !== root; p = p.parentElement) {
    const oy = getComputedStyle(p).overflowY
    if (oy === 'visible') continue
    const pr = p.getBoundingClientRect()
    top = Math.max(top, pr.top)
    bottom = Math.min(bottom, pr.bottom)
  }
  return { left: r.left, width: r.width, top, height: bottom - top }
}

// useGhost：读出上次记下的版面；ready 之后记下 el 里各块的位置（尺寸变化时更新）。
export function useGhost(key: string, el: HTMLElement | null, ready: boolean): Ghost | null {
  const ghost = useMemo(() => {
    const v = loadPref<Ghost | null>(key, null)
    return v && Array.isArray(v.b) && v.w > 0 ? v : null
  }, [key])
  useEffect(() => {
    if (!el || !ready) return
    let timer = 0
    const save = () => {
      window.clearTimeout(timer)
      timer = window.setTimeout(() => {
        const box = el.getBoundingClientRect()
        if (!box.width) return
        const b: Ghost['b'] = []
        const seen = new Set<Element>()
        for (const c of Array.from(el.querySelectorAll<HTMLElement>(ghostSel))) {
          if (seen.has(c)) continue
          seen.add(c)
          const isSection = c.tagName === 'SECTION'
          if (isSection && !hasBg(c)) continue
          const r = visibleRect(c, el)
          if (r.height < 4 || r.width < 4 || r.top - box.top > box.height + 4) continue
          const kind: 0 | 1 | 2 = isSection ? 0 : r.height > 40 && hasBg(c) ? 1 : 2
          b.push([Math.round(r.left - box.left), Math.round(r.top - box.top), Math.round(r.width), Math.round(r.height), kind])
          if (b.length >= maxBoxes) break
        }
        savePref(key, { w: Math.round(box.width), h: Math.round(box.height), b } satisfies Ghost)
      }, 500)
    }
    const ro = new ResizeObserver(save)
    ro.observe(el)
    const mo = new MutationObserver(save)
    mo.observe(el, { childList: true, subtree: true })
    save()
    return () => {
      window.clearTimeout(timer)
      ro.disconnect()
      mo.disconnect()
    }
  }, [key, el, ready])
  return ghost
}

export function GhostSkeleton({ ghost, className }: { ghost: Ghost; className?: string }) {
  const pct = (v: number) => `${(v / ghost.w) * 100}%`
  const order = [...ghost.b].sort((a, b) => a[4] - b[4])
  return (
    <div aria-hidden className={cx('relative shrink-0 overflow-hidden', className)} style={{ height: ghost.h }}>
      {order.map(([x, y, w, h, k], i) => {
        const style = { left: pct(x), width: pct(w), top: y, height: h }
        if (k === 0)
          return (
            <div key={i} className="absolute rounded-2xl bg-sunken/70" style={style}>
              <Skeleton className="absolute top-3.5 left-3 h-3 w-16" />
            </div>
          )
        if (k === 1)
          return (
            <div key={i} className="absolute flex flex-col justify-center gap-2 overflow-hidden rounded-xl bg-surface px-3 shadow-card" style={style}>
              <Skeleton className="h-3 w-1/3" />
              <Skeleton className="h-3.5 w-4/5" />
            </div>
          )
        return <Skeleton key={i} className="absolute" style={style} />
      })}
    </div>
  )
}
