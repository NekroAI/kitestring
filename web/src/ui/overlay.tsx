import { createContext, useCallback, useContext, useRef, useState, type ReactNode } from 'react'
import { Dialog as RDialog, DropdownMenu } from 'radix-ui'
import { Drawer } from 'vaul'
import { Check, X } from 'lucide-react'
import { cx, useDesktop } from '../lib/util'
import { t } from '../i18n'
import { Button } from './base'

// swipeToClose：触屏（如 iPad）在右侧面板上横向轻扫。onSwipe 返回 true 表示已处理（例如切换需求），否则向右扫关闭。
function swipeToClose(onClose: () => void, onSwipe?: (dir: 'left' | 'right') => boolean) {
  let start: { x: number; y: number; t: number } | null = null
  return {
    onTouchStart: (e: React.TouchEvent) => {
      const p = e.touches[0]
      start = e.touches.length === 1 ? { x: p.clientX, y: p.clientY, t: Date.now() } : null
    },
    onTouchEnd: (e: React.TouchEvent) => {
      const p = e.changedTouches[0]
      if (!start || !p) return
      const dx = p.clientX - start.x
      const dy = Math.abs(p.clientY - start.y)
      const adx = Math.abs(dx)
      if (adx > 90 && dy < adx * 0.5 && Date.now() - start.t < 600) {
        const dir = dx > 0 ? 'right' : 'left'
        if (!onSwipe?.(dir) && dir === 'right') onClose()
      }
      start = null
    },
  }
}

// Panel：详情面板。桌面端从右侧滑出，移动端为可下拉关闭的底部抽屉。
// bare：内容自己负责标题栏与滚动（例如需求抽屉：顶部固定、中间滚动、底部输入），title 只给读屏用。
export function Panel({ open, onClose, title, subtitle, children, actions, wide, bare, onSwipe }: { open: boolean; onClose: () => void; title: ReactNode; subtitle?: ReactNode; children: ReactNode; actions?: ReactNode; wide?: boolean; bare?: boolean; onSwipe?: (dir: 'left' | 'right') => boolean }) {
  const desktop = useDesktop()
  if (desktop) {
    return (
      <RDialog.Root open={open} onOpenChange={(o) => !o && onClose()}>
        <RDialog.Portal>
          <RDialog.Overlay className="fixed inset-0 z-40 bg-black/20 backdrop-blur-[1px] data-[state=closed]:animate-out data-[state=open]:animate-in dark:bg-black/40" />
          <RDialog.Content
            aria-describedby={undefined}
            onOpenAutoFocus={(e) => e.preventDefault()}
            {...swipeToClose(onClose, onSwipe)}
            className={cx(
              'panel-slide fixed top-2 right-2 bottom-2 z-50 flex flex-col overflow-hidden rounded-2xl border border-line bg-surface shadow-pop outline-none',
              wide ? 'w-[min(640px,calc(100vw-1rem))]' : 'w-[min(520px,calc(100vw-1rem))]',
            )}
          >
            {bare ? (
              <>
                <RDialog.Title className="sr-only">{title}</RDialog.Title>
                <div className="flex min-h-0 flex-1 flex-col">{children}</div>
              </>
            ) : (
              <>
                <div className="flex items-start gap-3 border-b border-line px-5 py-4">
                  <div className="min-w-0 flex-1">
                    <RDialog.Title className="font-display text-[17px] font-semibold text-fg">{title}</RDialog.Title>
                    {subtitle && <div className="mt-1 text-[13px] text-fg-3">{subtitle}</div>}
                  </div>
                  {actions}
                  <RDialog.Close asChild>
                    <Button variant="ghost" size="icon-sm" aria-label={t.common.close}>
                      <X className="size-4" />
                    </Button>
                  </RDialog.Close>
                </div>
                <div className="flex-1 overflow-y-auto overscroll-contain px-5 py-4">{children}</div>
              </>
            )}
          </RDialog.Content>
        </RDialog.Portal>
      </RDialog.Root>
    )
  }
  return (
    <Drawer.Root open={open} onOpenChange={(o) => !o && onClose()}>
      <Drawer.Portal>
        <Drawer.Overlay className="fixed inset-0 z-40 bg-black/30 dark:bg-black/50" />
        <Drawer.Content aria-describedby={undefined} className={cx('fixed inset-x-0 bottom-0 z-50 flex flex-col rounded-t-3xl border-t border-line bg-surface outline-none', bare ? 'h-[92dvh]' : 'max-h-[92dvh]')}>
          <div className="mx-auto mt-2.5 mb-1 h-1.5 w-10 shrink-0 rounded-full bg-line-strong" />
          {bare ? (
            <>
              <Drawer.Title className="sr-only">{title}</Drawer.Title>
              <div className="flex min-h-0 flex-1 flex-col">{children}</div>
            </>
          ) : (
            <>
              <div className="flex items-start gap-3 px-5 pt-2 pb-3">
                <div className="min-w-0 flex-1">
                  <Drawer.Title className="text-base font-semibold text-fg">{title}</Drawer.Title>
                  {subtitle && <div className="mt-1 text-[13px] text-fg-3">{subtitle}</div>}
                </div>
                {actions}
              </div>
              <div className="flex-1 overflow-y-auto px-5 pb-[max(1.5rem,env(safe-area-inset-bottom))]">{children}</div>
            </>
          )}
        </Drawer.Content>
      </Drawer.Portal>
    </Drawer.Root>
  )
}

// Modal：居中对话框（表单、确认）。移动端同样居中，宽度自适应。
export function Modal({ open, onClose, title, description, children, footer }: { open: boolean; onClose: () => void; title: ReactNode; description?: ReactNode; children?: ReactNode; footer?: ReactNode }) {
  return (
    <RDialog.Root open={open} onOpenChange={(o) => !o && onClose()}>
      <RDialog.Portal>
        <RDialog.Overlay className="modal-fade fixed inset-0 z-50 bg-black/30 backdrop-blur-[2px] dark:bg-black/50" />
        <RDialog.Content
          aria-describedby={undefined}
          className="modal-pop fixed top-1/2 left-1/2 z-50 flex max-h-[88dvh] w-[calc(100vw-2rem)] max-w-md -translate-x-1/2 -translate-y-1/2 flex-col overflow-hidden rounded-2xl border border-line bg-surface shadow-pop outline-none"
        >
          <div className="px-5 pt-5">
            <RDialog.Title className="font-display text-[17px] font-semibold text-fg">{title}</RDialog.Title>
            {description && <RDialog.Description className="mt-1.5 text-sm leading-relaxed text-fg-2">{description}</RDialog.Description>}
          </div>
          {children && <div className="overflow-y-auto px-5 pt-4">{children}</div>}
          <div className="flex justify-end gap-2 px-5 pt-5 pb-5">{footer}</div>
        </RDialog.Content>
      </RDialog.Portal>
    </RDialog.Root>
  )
}

// ---------- 确认框 ----------

type ConfirmOpts = { title: ReactNode; description?: ReactNode; confirmText?: string; danger?: boolean }
const ConfirmCtx = createContext<(o: ConfirmOpts) => Promise<boolean>>(async () => false)

export function ConfirmProvider({ children }: { children: ReactNode }) {
  const [opts, setOpts] = useState<ConfirmOpts | null>(null)
  const resolver = useRef<(v: boolean) => void>(undefined)
  const confirm = useCallback((o: ConfirmOpts) => {
    setOpts(o)
    return new Promise<boolean>((res) => {
      resolver.current = res
    })
  }, [])
  const done = (v: boolean) => {
    resolver.current?.(v)
    setOpts(null)
  }
  return (
    <ConfirmCtx.Provider value={confirm}>
      {children}
      <Modal
        open={!!opts}
        onClose={() => done(false)}
        title={opts?.title}
        description={opts?.description}
        footer={
          <>
            <Button onClick={() => done(false)}>{t.common.cancel}</Button>
            <Button variant={opts?.danger ? 'danger' : 'primary'} onClick={() => done(true)} autoFocus>
              {opts?.confirmText ?? t.common.confirm}
            </Button>
          </>
        }
      />
    </ConfirmCtx.Provider>
  )
}

export const useConfirm = () => useContext(ConfirmCtx)

// ---------- 下拉菜单 ----------

export type MenuItem =
  | { type?: 'item'; label: ReactNode; icon?: ReactNode; onSelect: () => void; danger?: boolean; checked?: boolean; hint?: ReactNode; disabled?: boolean }
  | { type: 'separator' }
  | { type: 'label'; label: ReactNode }

export function Menu({ trigger, items, align = 'end', width }: { trigger: ReactNode; items: MenuItem[]; align?: 'start' | 'end' | 'center'; width?: string }) {
  return (
    <DropdownMenu.Root modal={false}>
      <DropdownMenu.Trigger asChild>{trigger}</DropdownMenu.Trigger>
      <DropdownMenu.Portal>
        <DropdownMenu.Content
          align={align}
          sideOffset={6}
          collisionPadding={8}
          className={cx('menu-pop z-50 max-h-[min(420px,var(--radix-dropdown-menu-content-available-height))] overflow-y-auto rounded-xl border border-line bg-surface p-1 shadow-pop', width ?? 'min-w-[200px]')}
        >
          {items.map((it, i) =>
            it.type === 'separator' ? (
              <DropdownMenu.Separator key={i} className="my-1 h-px bg-line" />
            ) : it.type === 'label' ? (
              <DropdownMenu.Label key={i} className="px-2.5 pt-1.5 pb-1 text-xs font-medium text-fg-3">
                {it.label}
              </DropdownMenu.Label>
            ) : (
              <DropdownMenu.Item
                key={i}
                disabled={it.disabled}
                onSelect={it.onSelect}
                className={cx(
                  'flex cursor-pointer items-center gap-2.5 rounded-lg px-2.5 py-2 text-sm outline-none select-none data-[disabled]:opacity-40 data-[highlighted]:bg-hover',
                  it.danger ? 'text-danger' : 'text-fg',
                )}
              >
                {it.icon && <span className="flex size-4 shrink-0 items-center justify-center text-fg-3 [&_svg]:size-4">{it.icon}</span>}
                <span className="min-w-0 flex-1">
                  <span className="block truncate">{it.label}</span>
                  {it.hint && <span className="block truncate text-xs text-fg-3">{it.hint}</span>}
                </span>
                {it.checked && <Check className="size-4 shrink-0 text-accent" />}
              </DropdownMenu.Item>
            ),
          )}
        </DropdownMenu.Content>
      </DropdownMenu.Portal>
    </DropdownMenu.Root>
  )
}
