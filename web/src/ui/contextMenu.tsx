import { useRef, useState, type ReactNode } from 'react'
import { Drawer } from 'vaul'
import { ContextMenu } from 'radix-ui'
import { Check, ChevronRight } from 'lucide-react'
import { cx, useMedia } from '../lib/util'
import { t } from '../i18n'

// 右键（桌面）/ 长按（iPad、手机）菜单：对一个对象的深入操作，不用精确点小按钮。
// 菜单项和下拉菜单同一种样式；支持一层子菜单（例如“改状态”）。

export type CtxItem =
  | { type?: 'item'; label: ReactNode; icon?: ReactNode; onSelect: () => void; danger?: boolean; checked?: boolean; hint?: ReactNode; disabled?: boolean }
  | { type: 'sub'; label: ReactNode; icon?: ReactNode; items: CtxItem[] }
  | { type: 'separator' }
  | { type: 'label'; label: ReactNode }

const itemCls = 'flex cursor-pointer items-center gap-2.5 rounded-lg px-2.5 py-2 text-sm outline-none select-none data-[disabled]:opacity-40 data-[highlighted]:bg-hover pointer-coarse:py-2.5'
const contentCls = 'menu-pop z-50 min-w-[220px] max-h-[min(460px,var(--radix-context-menu-content-available-height))] overflow-y-auto rounded-xl border border-line bg-surface p-1 shadow-pop'

function Items({ items }: { items: CtxItem[] }) {
  return (
    <>
      {items.map((it, i) =>
        it.type === 'separator' ? (
          <ContextMenu.Separator key={i} className="my-1 h-px bg-line" />
        ) : it.type === 'label' ? (
          <ContextMenu.Label key={i} className="truncate px-2.5 pt-1.5 pb-1 text-xs font-medium text-fg-3">
            {it.label}
          </ContextMenu.Label>
        ) : it.type === 'sub' ? (
          <ContextMenu.Sub key={i}>
            <ContextMenu.SubTrigger className={cx(itemCls, 'text-fg data-[state=open]:bg-hover')}>
              {it.icon && <span className="flex size-4 shrink-0 items-center justify-center text-fg-3 [&_svg]:size-4">{it.icon}</span>}
              <span className="min-w-0 flex-1 truncate">{it.label}</span>
              <ChevronRight className="size-4 text-fg-3" />
            </ContextMenu.SubTrigger>
            <ContextMenu.Portal>
              <ContextMenu.SubContent sideOffset={4} collisionPadding={8} className={contentCls}>
                <Items items={it.items} />
              </ContextMenu.SubContent>
            </ContextMenu.Portal>
          </ContextMenu.Sub>
        ) : (
          <ContextMenu.Item key={i} disabled={it.disabled} onSelect={it.onSelect} className={cx(itemCls, it.danger ? 'text-danger' : 'text-fg')}>
            {it.icon && <span className="flex size-4 shrink-0 items-center justify-center text-fg-3 [&_svg]:size-4">{it.icon}</span>}
            <span className="min-w-0 flex-1">
              <span className="block truncate">{it.label}</span>
              {it.hint && <span className="block truncate text-xs text-fg-3">{it.hint}</span>}
            </span>
            {it.checked && <Check className="size-4 shrink-0 text-accent" />}
          </ContextMenu.Item>
        ),
      )}
    </>
  )
}

// Sheet：触屏上的操作面板——从底部升起、整行可点，比浮动菜单好按，也不会超出屏幕。
function Sheet({ items, open, onClose }: { items: CtxItem[]; open: boolean; onClose: () => void }) {
  const [sub, setSub] = useState<number | null>(null)
  const run = (fn: () => void) => {
    onClose()
    window.setTimeout(fn, 120)
  }
  const row = 'flex w-full items-center gap-3 rounded-xl px-3 py-3 text-left text-[15px] active:bg-hover disabled:opacity-40'
  return (
    <Drawer.Root open={open} onOpenChange={(o) => !o && onClose()} onAnimationEnd={() => !open && setSub(null)}>
      <Drawer.Portal>
        <Drawer.Overlay className="fixed inset-0 z-[60] bg-black/40" />
        <Drawer.Content aria-describedby={undefined} className="fixed inset-x-0 bottom-0 z-[60] max-h-[85dvh] overflow-y-auto rounded-t-3xl border-t border-line bg-surface px-3 pt-2 pb-[max(1rem,env(safe-area-inset-bottom))] outline-none">
          <div className="mx-auto mt-1 mb-2 h-1.5 w-10 rounded-full bg-line-strong" />
          <Drawer.Title className="sr-only">{t.common.more}</Drawer.Title>
          {items.map((it, i) =>
            it.type === 'separator' ? (
              <div key={i} className="mx-3 my-1 h-px bg-line" />
            ) : it.type === 'label' ? (
              <div key={i} className="truncate px-3 pt-1 pb-2 text-sm font-medium text-fg-3">
                {it.label}
              </div>
            ) : it.type === 'sub' ? (
              <div key={i}>
                <button className={cx(row, 'text-fg')} onClick={() => setSub(sub === i ? null : i)}>
                  {it.icon && <span className="flex size-5 shrink-0 items-center justify-center text-fg-3 [&_svg]:size-5">{it.icon}</span>}
                  <span className="min-w-0 flex-1 truncate">{it.label}</span>
                  <ChevronRight className={cx('size-5 text-fg-3 transition-transform', sub === i && 'rotate-90')} />
                </button>
                {sub === i && (
                  <div className="mb-1 ml-8 border-l border-line pl-2">
                    {it.items.map((x, j) =>
                      x.type === 'item' || x.type === undefined ? (
                        <button key={j} disabled={x.disabled} className={cx(row, x.danger ? 'text-danger' : 'text-fg')} onClick={() => run(x.onSelect)}>
                          {x.icon && <span className="flex size-5 shrink-0 items-center justify-center text-fg-3 [&_svg]:size-5">{x.icon}</span>}
                          <span className="min-w-0 flex-1">
                            <span className="block truncate">{x.label}</span>
                            {x.hint && <span className="block truncate text-xs text-fg-3">{x.hint}</span>}
                          </span>
                          {x.checked && <Check className="size-5 shrink-0 text-accent" />}
                        </button>
                      ) : x.type === 'label' ? (
                        <div key={j} className="px-3 pt-2 pb-1 text-xs text-fg-3">
                          {x.label}
                        </div>
                      ) : null,
                    )}
                  </div>
                )}
              </div>
            ) : (
              <button key={i} disabled={it.disabled} className={cx(row, it.danger ? 'text-danger' : 'text-fg')} onClick={() => run(it.onSelect)}>
                {it.icon && <span className="flex size-5 shrink-0 items-center justify-center text-fg-3 [&_svg]:size-5">{it.icon}</span>}
                <span className="min-w-0 flex-1">
                  <span className="block truncate">{it.label}</span>
                  {it.hint && <span className="block truncate text-xs text-fg-3">{it.hint}</span>}
                </span>
                {it.checked && <Check className="size-5 shrink-0 text-accent" />}
              </button>
            ),
          )}
        </Drawer.Content>
      </Drawer.Portal>
    </Drawer.Root>
  )
}

// useLongPress：触屏长按（约半秒、手指没怎么移动）时触发，并吞掉随后的那次点击。
function useLongPress(onLong: () => void) {
  const timer = useRef(0)
  const start = useRef<{ x: number; y: number } | null>(null)
  const fired = useRef(false)
  const cancel = () => {
    window.clearTimeout(timer.current)
    start.current = null
  }
  return {
    onPointerDown: (e: React.PointerEvent) => {
      if (e.pointerType !== 'touch') return
      fired.current = false
      start.current = { x: e.clientX, y: e.clientY }
      timer.current = window.setTimeout(() => {
        fired.current = true
        navigator.vibrate?.(8)
        onLong()
      }, 480)
    },
    onPointerMove: (e: React.PointerEvent) => {
      if (start.current && Math.hypot(e.clientX - start.current.x, e.clientY - start.current.y) > 10) cancel()
    },
    onPointerUp: cancel,
    onPointerCancel: cancel,
    onClickCapture: (e: React.MouseEvent) => {
      if (fired.current) {
        e.preventDefault()
        e.stopPropagation()
        fired.current = false
      }
    },
    onContextMenu: (e: React.MouseEvent) => {
      if (start.current || fired.current) e.preventDefault() // 触屏长按不要再弹系统菜单
    },
  }
}

// Ctx：包住一个元素，让它支持右键（桌面，浮动菜单）/ 长按（触屏，底部操作面板）。
export function Ctx({ items, children, className }: { items: CtxItem[]; children: ReactNode; className?: string }) {
  const coarse = useMedia('(pointer: coarse)')
  const [open, setOpen] = useState(false)
  const press = useLongPress(() => setOpen(true))
  if (!items.length) return <>{children}</>
  const target = cx('ctx-target rounded-2xl transition-[transform,box-shadow] duration-200', className)
  if (coarse)
    return (
      <>
        <div {...press} data-state={open ? 'open' : 'closed'} className={cx(target, 'data-[state=open]:scale-[0.985] data-[state=open]:ring-2 data-[state=open]:ring-accent/40')}>
          {children}
        </div>
        <Sheet items={items} open={open} onClose={() => setOpen(false)} />
      </>
    )
  return (
    <ContextMenu.Root onOpenChange={(o) => o && navigator.vibrate?.(8)}>
      <ContextMenu.Trigger asChild>
        <div className={cx(target, 'data-[state=open]:scale-[0.985] data-[state=open]:ring-2 data-[state=open]:ring-accent/40')}>{children}</div>
      </ContextMenu.Trigger>
      <ContextMenu.Portal>
        <ContextMenu.Content collisionPadding={8} className={contentCls}>
          <Items items={items} />
        </ContextMenu.Content>
      </ContextMenu.Portal>
    </ContextMenu.Root>
  )
}
