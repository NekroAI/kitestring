import { createContext, useCallback, useContext, useEffect, useRef, useState, type ReactNode } from 'react'
import { Dialog as RDialog, DropdownMenu } from 'radix-ui'
import { Drawer } from 'vaul'
import { AnimatePresence, motion } from 'motion/react'
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
// aside：宽屏时在面板左侧并排再开一层（例如计划里选中的需求），和面板同属一个对话框，焦点与点外关闭都按一层算。
// useKeyboardBox：手机键盘弹出时可见区域的高度与键盘高度（R-72）。
// vaul 自带的 repositionInputs 会在 iOS 上改抽屉高度、键盘收起后不一定恢复，导致抽屉被压成一条或下半截透明；
// 这里关掉它，自己按 visualViewport 算：抽屉底边贴住键盘上沿，高度不超过可见区域。
// iOS 聚焦输入框时还会把整页往上平移（visualViewport.offsetTop > 0），和我们重新排版叠在一起就是“连跳”；
// 弹出层是固定定位、本来就能看到输入框，所以把这次平移撤回去。
function useKeyboardBox(active: boolean) {
  const [box, setBox] = useState<{ kb: number; vh: number } | null>(null)
  useEffect(() => {
    const vv = window.visualViewport
    if (!active || !vv) return
    const update = () => {
      if (vv.offsetTop > 0 || window.scrollY > 0) window.scrollTo(0, 0)
      const kb = Math.max(0, Math.round(window.innerHeight - vv.height))
      setBox(kb > 60 ? { kb, vh: Math.round(vv.height) } : null) // 小于 60px 的差值是地址栏伸缩，不算键盘
    }
    update()
    vv.addEventListener('resize', update)
    vv.addEventListener('scroll', update)
    return () => {
      vv.removeEventListener('resize', update)
      vv.removeEventListener('scroll', update)
    }
  }, [active])
  return box
}

// FullSheet：手机上的全屏编辑页（R-72）。顶部固定、只改高度：键盘升起时只是从下面“截短”，
// 标题栏和正在写的内容都不动，不会像底部抽屉那样跟着键盘上下跳。header 里放取消 / 提交，键盘挡不住。
export function FullSheet({ open, onClose, title, header, children }: { open: boolean; onClose: () => void; title: string; header: ReactNode; children: ReactNode }) {
  const kb = useKeyboardBox(open)
  const body = useRef<HTMLDivElement>(null)
  // 键盘升起后，把正在输入的框滚到可见区域（在页内滚，不让系统平移整页）
  useEffect(() => {
    if (!kb) return
    const el = document.activeElement as HTMLElement | null
    if (el && body.current?.contains(el)) el.scrollIntoView({ block: 'nearest' })
  }, [kb])
  return (
    <RDialog.Root open={open} onOpenChange={(o) => !o && onClose()}>
      <AnimatePresence>
        {open && (
          <RDialog.Portal forceMount>
            <RDialog.Content forceMount asChild aria-describedby={undefined} onOpenAutoFocus={(e) => e.preventDefault()}>
              <motion.div
                initial={{ y: '100%' }}
                animate={{ y: 0 }}
                exit={{ y: '100%' }}
                transition={{ type: 'spring', stiffness: 420, damping: 42 }}
                style={{ height: kb ? kb.vh : '100dvh' }}
                className="fixed inset-x-0 top-0 z-50 flex flex-col bg-surface outline-none"
              >
                <RDialog.Title className="sr-only">{title}</RDialog.Title>
                <div className="flex shrink-0 items-center gap-2 border-b border-line px-3 pt-[max(0.5rem,env(safe-area-inset-top))] pb-2">{header}</div>
                <div ref={body} className="min-h-0 flex-1 overflow-y-auto overscroll-contain">
                  {children}
                </div>
              </motion.div>
            </RDialog.Content>
          </RDialog.Portal>
        )}
      </AnimatePresence>
    </RDialog.Root>
  )
}

export function Panel({ open, onClose, title, subtitle, children, actions, wide, bare, onSwipe, aside }: { open: boolean; onClose: () => void; title: ReactNode; subtitle?: ReactNode; children: ReactNode; actions?: ReactNode; wide?: boolean; bare?: boolean; onSwipe?: (dir: 'left' | 'right') => boolean; aside?: ReactNode }) {
  const desktop = useDesktop()
  const kb = useKeyboardBox(open && !desktop)
  if (desktop) {
    return (
      <RDialog.Root open={open} onOpenChange={(o) => !o && onClose()}>
        <RDialog.Portal>
          <RDialog.Overlay className="fixed inset-0 z-40 bg-black/20 backdrop-blur-[1px] data-[state=closed]:animate-out data-[state=open]:animate-in dark:bg-black/40" />
          <RDialog.Content
            aria-describedby={undefined}
            onOpenAutoFocus={(e) => e.preventDefault()}
            {...swipeToClose(onClose, onSwipe)}
            className="panel-slide fixed top-2 right-2 bottom-2 z-50 flex gap-2 outline-none"
          >
            <AnimatePresence initial={false}>
              {aside && (
                <motion.div
                  key="aside"
                  initial={{ opacity: 0, x: 28 }}
                  animate={{ opacity: 1, x: 0 }}
                  exit={{ opacity: 0, x: 28 }}
                  transition={{ duration: 0.24, ease: [0.2, 0.8, 0.2, 1] }}
                  className="flex w-[min(600px,calc(100vw-660px))] flex-col overflow-hidden rounded-2xl border border-line bg-surface shadow-pop"
                >
                  {aside}
                </motion.div>
              )}
            </AnimatePresence>
            <div className={cx('flex flex-col overflow-hidden rounded-2xl border border-line bg-surface shadow-pop', wide ? 'w-[min(640px,calc(100vw-1rem))]' : 'w-[min(520px,calc(100vw-1rem))]')}>
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
            </div>
          </RDialog.Content>
        </RDialog.Portal>
      </RDialog.Root>
    )
  }
  return (
    // noBodyStyles：不让 vaul 把页面固定住再恢复滚动位置——关掉抽屉时底下的列表会因此跳两下
    <Drawer.Root open={open} onOpenChange={(o) => !o && onClose()} repositionInputs={false} noBodyStyles>
      <Drawer.Portal>
        <Drawer.Overlay className="fixed inset-0 z-40 bg-black/30 dark:bg-black/50" />
        <Drawer.Content
          aria-describedby={undefined}
          style={kb ? { bottom: kb.kb, [bare ? 'height' : 'maxHeight']: kb.vh - 8 } : undefined}
          className={cx('fixed inset-x-0 bottom-0 z-50 flex flex-col rounded-t-3xl border-t border-line bg-surface outline-none', bare ? 'h-[92dvh]' : 'max-h-[92dvh]', kb && 'rounded-b-none')}
        >
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
    <DropdownMenu.Root>
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
