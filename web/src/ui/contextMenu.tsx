import type { ReactNode } from 'react'
import { ContextMenu } from 'radix-ui'
import { Check, ChevronRight } from 'lucide-react'
import { cx } from '../lib/util'

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

// Ctx：包住一个元素，让它支持右键 / 长按。被按住的元素会轻微下沉并描边，告诉你菜单是给谁的。
export function Ctx({ items, children, className }: { items: CtxItem[]; children: ReactNode; className?: string }) {
  if (!items.length) return <>{children}</>
  return (
    <ContextMenu.Root onOpenChange={(o) => o && navigator.vibrate?.(8)}>
      <ContextMenu.Trigger asChild>
        <div className={cx('ctx-target rounded-2xl transition-[transform,box-shadow] duration-200 data-[state=open]:scale-[0.985] data-[state=open]:ring-2 data-[state=open]:ring-accent/40', className)}>{children}</div>
      </ContextMenu.Trigger>
      <ContextMenu.Portal>
        <ContextMenu.Content collisionPadding={8} className={contentCls}>
          <Items items={items} />
        </ContextMenu.Content>
      </ContextMenu.Portal>
    </ContextMenu.Root>
  )
}
