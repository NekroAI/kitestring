import { createContext, useCallback, useContext, useEffect, useLayoutEffect, useRef, useState, type ReactNode } from 'react'
import { Dialog as RDialog, DropdownMenu } from 'radix-ui'
import { AnimatePresence, motion, useMotionValue, useTransform, type AnimationPlaybackControls, type MotionValue } from 'motion/react'
import { Check, X } from 'lucide-react'
import { cx, useDesktop, useMedia } from '../lib/util'
import { t } from '../i18n'
import { Button, Input, Textarea } from './base'
import { DraftLine, useDraft } from '../app/draft'
import { clamp, G, noDrag, PagerBind, rub, scrollerOf, scrollsX, spring, springTo, useDrag, type DragStart, type PagerHandle } from './gesture'

// FullSheet：手机上的全屏编辑页（R-72）。header 里放取消 / 提交，键盘挡不住。
export function FullSheet({ open, onClose, title, header, children }: { open: boolean; onClose: () => void; title: string; header: ReactNode; children: ReactNode }) {
  const [typing, setTyping] = useState(false)
  const off = useRef(0)
  const isField = (el: EventTarget | null) => el instanceof HTMLElement && el.matches('input, textarea, [contenteditable="true"]')
  return (
    <RDialog.Root open={open} onOpenChange={(o) => !o && onClose()}>
      <AnimatePresence>
        {open && (
          <RDialog.Portal forceMount>
            <RDialog.Content forceMount asChild aria-describedby={undefined} data-ks-modal="" onOpenAutoFocus={(e) => e.preventDefault()}>
              <motion.div
                initial={{ y: '100%' }}
                animate={{ y: 0 }}
                exit={{ y: '100%' }}
                transition={spring()}
                className="fixed inset-x-0 top-0 z-50 flex h-[100dvh] flex-col bg-surface outline-none"
              >
                <RDialog.Title className="sr-only">{title}</RDialog.Title>
                <div className="flex shrink-0 items-center gap-2 border-b border-line px-3 pt-[max(0.5rem,env(safe-area-inset-top))] pb-2">{header}</div>
                <div
                  className="min-h-0 flex-1 overflow-y-auto overscroll-contain"
                  onFocus={(e) => {
                    if (!isField(e.target)) return
                    window.clearTimeout(off.current)
                    setTyping(true)
                  }}
                  onBlur={(e) => {
                    // 等键盘收起后再撤掉垫高，和键盘回落是同一个动作
                    if (isField(e.target) && !isField(e.relatedTarget)) off.current = window.setTimeout(() => setTyping(false), 350)
                  }}
                >
                  {children}
                  {/* 键盘的位置：聚焦时先垫出来，iOS 就能在页内把输入框滚到键盘上方 */}
                  {typing && <div aria-hidden className="h-[50dvh]" />}
                </div>
              </motion.div>
            </RDialog.Content>
          </RDialog.Portal>
        )}
      </AnimatePresence>
    </RDialog.Root>
  )
}

// AsideStackedContext（R-196）：左边那张纸此刻是不是盖在面板上（放不下并排时 / 手机），纸顶据此写「‹ 返回 R-x」。
export const AsideStackedContext = createContext(false)

// 左边那张纸（R-196）：面板宽度 + 纸至少 460 + 边距放得下就并排，放不下就盖在面板上（面板略缩、右边露一条边）。
const PANEL_W = { approval: 860, wide: 640, normal: 520 }

// ---------- 抽屉手势（R-73）----------
// 一种弹簧、按下即打断（ui/gesture.tsx）：
// - 手机底部抽屉：按住顶部（把手、顶栏）或内容已滚到顶时往下拉，跟手；松手拖过 G.closeDist 或下甩够快就关，否则弹回。
// - iPad / 桌面右侧面板：按住顶栏向右拖关闭（触屏和鼠标都行）；没有上一件 / 下一件的面板，内容区横向右拖同样关闭。
// - 内容区左右拖：交给里面的 Pager 切换上一件 / 下一件。
// - 左边那张引用预览纸（R-196）：在纸上向右拖收起纸。
// - 屏幕左缘 G.edge 内起手的横滑不接，留给浏览器返回。

// XDrag：一次横向拖动交给谁。
type XDrag = { move: (dx: number) => void; end: (vx: number, cancel: boolean) => void }

// closeDrag：把 v 往右拖（往左越拖越沉），松手够远 / 够快就弹出去并 onGone，否则弹回。
function closeDrag(v: MotionValue<number>, onGone: (vx: number) => void, anim: { current: AnimationPlaybackControls | null }): XDrag {
  const from = v.get()
  return {
    move: (dx) => {
      const x = from + dx
      v.set(x < 0 ? -rub(-x, 120) : x)
    },
    end: (vx, cancel) => {
      anim.current?.stop()
      if (!cancel && ((v.get() > G.closeDist && vx > -200) || vx > G.closeV)) onGone(vx)
      else anim.current = springTo(v, 0, vx)
    },
  }
}

const grabH = 56 // 顶部这么高算「按住顶栏」

// usePanelDrag：Panel 的拖动分派（桌面右侧面板与手机底部抽屉共用）。
function usePanelDrag(
  el: HTMLElement | null,
  o: {
    sheet: boolean
    pager: { current: PagerHandle | null }
    aside: () => XDrag | null
    close: () => XDrag | null
    pull?: (s: DragStart) => { move: (dy: number) => void; end: (vy: number, cancel: boolean) => void } | null
    resume?: () => void
  },
) {
  type S = { p: DragStart; grab: boolean; inAside: boolean; scroller: HTMLElement | null; d0?: number; x?: XDrag | null; y?: ReturnType<NonNullable<typeof o.pull>> }
  useDrag<S>(el, {
    mouse: (p) => !o.sheet && !p.target.closest('button, a, input, textarea, select, [role="button"], [data-aside]') && p.y0 - el!.getBoundingClientRect().top < grabH,
    start: (p) => {
      if (noDrag(p.target)) return null
      const r = el!.getBoundingClientRect()
      const inAside = !!p.target.closest('[data-aside]')
      return { p, grab: p.y0 - r.top < grabH, inAside, scroller: scrollerOf(p.target, el) }
    },
    lock: (s, axis, dx, dy) => {
      if (axis === 'y') {
        if (!o.pull || dy < 0) return false
        if (!s.grab && s.scroller && s.scroller.scrollTop > 0) return false
        s.y = o.pull(s.p)
        s.d0 = dy
        return !!s.y
      }
      s.d0 = dx
      if (s.p.touch && s.p.x0 < G.edge) return false
      // 手指下能横向滚动的代码块、表格、流程图：照常横滚
      if (s.p.touch && scrollsX(s.p.target, -dx, el)) return false
      if (!s.p.touch) {
        s.x = o.close()
        return !!s.x
      }
      if (s.inAside) s.x = dx > 0 ? o.aside() : null
      else if (!o.sheet && s.grab) s.x = dx > 0 ? o.close() : null
      else if (o.pager.current) {
        const h = o.pager.current
        h.begin()
        s.x = { move: h.move, end: (vx) => h.end(vx) }
      } else s.x = !o.sheet && dx > 0 ? o.close() : null
      return !!s.x
    },
    early: (s, dx, dy) => {
      if (!s.p.touch) return false
      if (Math.abs(dx) >= 3 && Math.abs(dx) > Math.abs(dy) * G.ratio) {
        if (s.p.x0 < G.edge || scrollsX(s.p.target, -dx, el)) return false
        return s.inAside || (!o.sheet && s.grab) ? dx > 0 : !!o.pager.current || (!o.sheet && dx > 0)
      }
      if (o.pull && dy >= 3 && dy > Math.abs(dx) * G.ratio) return s.grab || !s.scroller || s.scroller.scrollTop <= 0
      return false
    },
    // 位移从定下方向的那一刻算起：接管时内容不跳一下
    move: (s, dx, dy) => (s.y ? s.y.move(dy - (s.d0 ?? 0)) : s.x?.move(dx - (s.d0 ?? 0))),
    end: (s, v, cancel) => (s.y ? s.y.end(v.y, cancel) : s.x?.end(v.x, cancel)),
    tap: () => o.resume?.(),
  })
}

// useAsideDrag：左边那张纸跟手向右拖收起。出现 / 收起本身也用这个值（从右边一点滑进来）。
function useAsideDrag(onAsideClose?: () => void) {
  const x = useMotionValue(0)
  const anim = useRef<AnimationPlaybackControls | null>(null)
  const el = useRef<HTMLDivElement | null>(null)
  const make = (): XDrag | null => {
    if (!onAsideClose || !el.current) return null
    anim.current?.stop()
    return closeDrag(x, (vx) => {
      anim.current = springTo(x, (el.current?.offsetWidth ?? 600) + 24, vx, () => {
        onAsideClose()
      })
    }, anim)
  }
  return { x, el, make }
}

// Panel：详情面板。桌面端从右侧滑出，移动端为可下拉关闭的底部抽屉。
// bare：内容自己负责标题栏与滚动（例如需求抽屉：顶部固定、中间滚动、底部输入），title 只给读屏用。
// aside：宽屏时在面板左侧并排再开一层（例如计划里选中的需求），和面板同属一个对话框，焦点与点外关闭都按一层算。
// 手机键盘（R-72）：不跟着键盘重新排版，底部抽屉交给 iOS 原生处理（随键盘一次平滑上移、收起时回落）；
// 全屏编辑页高度固定，聚焦时在内容底部垫一段空白，让 iOS 在页内滚动露出输入框而不是平移整页。
// 里面放 Pager 时，左右拖 / 妙控板双指横滑切换上一件 / 下一件（R-73）。
export function Panel({ open, onClose, title, subtitle, children, actions, wide, bare, aside, onAsideClose }: { open: boolean; onClose: () => void; title: ReactNode; subtitle?: ReactNode; children: ReactNode; actions?: ReactNode; wide?: boolean | 'approval'; bare?: boolean; aside?: ReactNode; onAsideClose?: () => void }) {
  const desktop = useDesktop()
  const mainW = wide === 'approval' ? PANEL_W.approval : wide ? PANEL_W.wide : PANEL_W.normal
  const side = useMedia(`(min-width: ${mainW + 24 + 460}px)`)
  const stacked = !!aside && !side
  const pager = useRef<PagerHandle | null>(null)
  const bindPager = useCallback((h: PagerHandle | null) => void (pager.current = h), [])
  const ax = useAsideDrag(onAsideClose)
  const asideExit = { opacity: 0, transition: { duration: 0.18 } }
  // 桌面：面板整体右拖关闭
  const px = useMotionValue(0)
  const pAnim = useRef<AnimationPlaybackControls | null>(null)
  const [panelEl, setPanelEl] = useState<HTMLDivElement | null>(null)
  const [flung, setFlung] = useState(false)
  useEffect(() => {
    if (open) {
      pAnim.current?.stop()
      px.set(0)
      setFlung(false)
    }
  }, [open, px])
  const scrim = useTransform(px, (v) => clamp(1 - v / Math.max(1, panelEl?.offsetWidth ?? 600), 0, 1))
  usePanelDrag(desktop ? panelEl : null, {
    sheet: false,
    pager,
    aside: () => (aside ? ax.make() : null),
    close: () => {
      pAnim.current?.stop()
      return closeDrag(px, (vx) => {
        setFlung(true) // 弹出去之后不再播关闭动画
        pAnim.current = springTo(px, (panelEl?.offsetWidth ?? 600) + 24, vx, onClose)
      }, pAnim)
    },
    resume: () => {
      // 只是按了一下：被打断的弹回 / 弹出接着走完
      if (px.get() > 0.5) {
        setFlung(false)
        pAnim.current = springTo(px, 0)
      }
    },
  })
  const paper = 'flex flex-col overflow-hidden rounded-2xl border border-line shadow-pop'
  const asidePaper = (cls: string, initialX: number | string) => (
    <AnimatePresence initial={false}>
      {aside && (
        <motion.div
          key={cls.includes('absolute') ? 'aside-stack' : 'aside'}
          ref={ax.el}
          data-aside=""
          initial={{ opacity: 0, x: initialX }}
          animate={{ opacity: 1, x: 0, transition: spring() }}
          exit={asideExit}
          style={{ x: ax.x, ['--panel-w' as string]: `${mainW}px` }}
          className={cls}
        >
          {aside}
        </motion.div>
      )}
    </AnimatePresence>
  )
  const body = bare ? (
    <div className="flex min-h-0 flex-1 flex-col">{children}</div>
  ) : null
  if (desktop) {
    return (
      <PagerBind value={bindPager}>
        <RDialog.Root open={open} onOpenChange={(o) => !o && onClose()}>
          <RDialog.Portal>
            {/* 全屏遮罩不用 backdrop-filter：带鱼屏上全屏模糊是一张很大的合成纹理，底下工作台一直重绘时 Chrome 偶尔丢掉这层及其上层一帧，抽屉和遮罩一起闪没（R-184）；用略深的底色代替 */}
            <RDialog.Overlay asChild>
              <motion.div style={{ opacity: scrim }} data-flung={flung ? '' : undefined} className="fixed inset-0 z-40 bg-black/25 data-[flung]:!animate-none data-[state=closed]:animate-out data-[state=open]:animate-in dark:bg-black/45" />
            </RDialog.Overlay>
            <RDialog.Content
              asChild
              aria-describedby={undefined}
              onOpenAutoFocus={(e) => e.preventDefault()}
              {...keepForDock}
              onEscapeKeyDown={(e) => {
                if (aside && onAsideClose) {
                  e.preventDefault()
                  onAsideClose()
                }
              }}
            >
              <motion.div ref={setPanelEl} style={{ x: px }} data-flung={flung ? '' : undefined} className="panel-slide fixed top-2 right-2 bottom-2 z-50 flex gap-2 outline-none">
                <AsideStackedContext value={stacked}>
                  {asidePaper(
                    cx(
                      paper,
                      'bg-surface-2',
                      // 并排：纸宽 = 剩下的地方（最多 640）；盖在上面：和面板左边对齐，右边让出 28px 露出后面那张
                      stacked ? 'absolute inset-y-0 right-7 left-0 z-10' : 'w-[min(640px,calc(100vw-var(--panel-w)-24px))]',
                    ),
                    28,
                  )}
                </AsideStackedContext>
                <div
                  className={cx(
                    paper,
                    'bg-surface transition-[width,transform,filter] duration-300 ease-out',
                    // approval：审批抽屉（需求验收 / 决策 / 计划批准 / 会话等你……）不论内容类型同一个宽度，翻页不跳（R-178）；证据图库排得下三列（R-195）
                    wide === 'approval' ? 'w-[min(860px,calc(100vw-1rem))]' : wide ? 'w-[min(640px,calc(100vw-1rem))]' : 'w-[min(520px,calc(100vw-1rem))]',
                    // 纸盖在上面时，后面这张略缩、变暗，往左让一点（R-196）
                    stacked && '-translate-x-3.5 scale-[.985] brightness-[.93]',
                  )}
                  onClickCapture={(e) => {
                    // 盖在上面时，点后面那张露出的边 = 收起纸回到它
                    if (!stacked || !onAsideClose) return
                    e.stopPropagation()
                    e.preventDefault()
                    onAsideClose()
                  }}
                >
                  {bare ? (
                    <>
                      <RDialog.Title className="sr-only">{title}</RDialog.Title>
                      {body}
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
              </motion.div>
            </RDialog.Content>
          </RDialog.Portal>
        </RDialog.Root>
      </PagerBind>
    )
  }
  return (
    <PagerBind value={bindPager}>
      <BottomSheet
        open={open}
        onClose={onClose}
        title={title}
        className={cx('rounded-t-3xl border-t border-line bg-surface', bare ? 'h-[92dvh]' : 'max-h-[92dvh]')}
        extend={(o) => ({ ...o, pager, aside: () => (aside ? ax.make() : null) })}
        extra={
          // 手机上左边那张纸盖满整个抽屉（R-196），顶上「‹ 返回 R-x」回去
          <AsideStackedContext value>{asidePaper('absolute inset-0 z-10 flex flex-col overflow-hidden rounded-t-3xl bg-surface', '100%')}</AsideStackedContext>
        }
      >
        {bare ? (
          body
        ) : (
          <>
            <div className="flex items-start gap-3 px-5 pt-2 pb-3">
              <div className="min-w-0 flex-1">
                <div className="text-base font-semibold text-fg">{title}</div>
                {subtitle && <div className="mt-1 text-[13px] text-fg-3">{subtitle}</div>}
              </div>
              {actions}
            </div>
            <div className="flex-1 overflow-y-auto overscroll-contain px-5 pb-[max(1.5rem,env(safe-area-inset-bottom))]">{children}</div>
          </>
        )}
      </BottomSheet>
    </PagerBind>
  )
}

type DragOpts = Parameters<typeof usePanelDrag>[1]

// BottomSheet：手机上的底部抽屉（详情面板、长按菜单共用）。出现、下拉关闭、弹回都是同一种弹簧，动画中途可以再按住接着拖。
// handleOnly：只有顶部能拖（长按菜单：整块可拖时点菜单项手指稍动就会被当成拖动）。title 只给读屏用。
export function BottomSheet({
  open,
  onClose,
  title,
  className,
  z = 'z-50',
  scrimClass = 'bg-black/30 dark:bg-black/50',
  handleOnly,
  onClosed,
  extend,
  extra,
  children,
}: {
  open: boolean
  onClose: () => void
  title: ReactNode
  className?: string
  z?: string
  scrimClass?: string
  handleOnly?: boolean
  onClosed?: () => void
  extend?: (o: DragOpts) => DragOpts
  extra?: ReactNode
  children: ReactNode
}) {
  const [mounted, setMounted] = useState(open)
  const [el, setEl] = useState<HTMLDivElement | null>(null)
  const elRef = useRef<HTMLDivElement | null>(null)
  elRef.current = el
  const OFF = 100000
  const y = useMotionValue(OFF)
  const anim = useRef<AnimationPlaybackControls | null>(null)
  const flingV = useRef(0)
  const height = () => elRef.current?.offsetHeight ?? (typeof window === 'undefined' ? 800 : window.innerHeight)
  const done = useRef(onClosed)
  done.current = onClosed
  useEffect(() => {
    if (open) setMounted(true)
  }, [open])
  useLayoutEffect(() => {
    if (!el) return
    anim.current?.stop()
    if (open) {
      if (y.get() >= OFF) y.set(height())
      anim.current = springTo(y, 0, flingV.current)
    } else
      anim.current = springTo(y, height() + 24, flingV.current, () => {
        y.set(OFF)
        setMounted(false)
        done.current?.()
      })
    flingV.current = 0
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [open, el])
  const scrim = useTransform(y, (v) => (v >= OFF ? 0 : clamp(1 - v / Math.max(1, height()), 0, 1)))
  const pagerNone = useRef<PagerHandle | null>(null)
  const base: DragOpts = {
    sheet: true,
    pager: pagerNone,
    aside: () => null,
    close: () => null,
    pull: (p) => {
      if (handleOnly && p.y0 - elRef.current!.getBoundingClientRect().top > 36) return null
      anim.current?.stop()
      const from = y.get()
      return {
        move: (dy) => {
          const v = from + dy
          y.set(v < 0 ? -rub(-v, 120) : v)
        },
        end: (vy, cancel) => {
          anim.current?.stop()
          if (!cancel && ((y.get() > G.closeDist && vy > -200) || vy > G.closeV)) {
            flingV.current = vy
            onClose()
          } else anim.current = springTo(y, 0, vy)
        },
      }
    },
    resume: () => {
      if (open && Math.abs(y.get()) > 0.5 && y.get() < OFF) anim.current = springTo(y, 0)
    },
  }
  // 按下即打断：手指一按下，出现 / 弹回的动画停在当前位置
  useEffect(() => {
    if (!el) return
    const stop = () => anim.current?.stop()
    el.addEventListener('touchstart', stop, { passive: true })
    return () => el.removeEventListener('touchstart', stop)
  }, [el])
  usePanelDrag(el, extend ? extend(base) : base)
  if (!mounted && !open) return null
  return (
    <RDialog.Root open onOpenChange={(o) => !o && onClose()}>
      <RDialog.Portal>
        <RDialog.Overlay asChild>
          <motion.div style={{ opacity: scrim }} className={cx('fixed inset-0', z, scrimClass)} />
        </RDialog.Overlay>
        <RDialog.Content asChild aria-describedby={undefined} onOpenAutoFocus={(e) => e.preventDefault()} {...keepForDock}>
          <motion.div ref={setEl} style={{ y }} className={cx('fixed inset-x-0 bottom-0 flex flex-col outline-none', z, className)}>
            <div className="mx-auto mt-2.5 mb-1 h-1.5 w-10 shrink-0 rounded-full bg-line-strong" />
            <RDialog.Title className="sr-only">{title}</RDialog.Title>
            {children}
            {extra}
          </motion.div>
        </RDialog.Content>
      </RDialog.Portal>
    </RDialog.Root>
  )
}

// Modal：居中对话框（表单、确认）。移动端同样居中，宽度自适应。
// 点顶部撤销浮动条（R-201）不算点到抽屉外面：撤回时抽屉不关
const keepForDock = { onInteractOutside: (e: Event) => (e.target as HTMLElement | null)?.closest?.('[data-undo-dock]') && e.preventDefault() }

export function Modal({ open, onClose, title, description, children, footer }: { open: boolean; onClose: () => void; title: ReactNode; description?: ReactNode; children?: ReactNode; footer?: ReactNode }) {
  return (
    <RDialog.Root open={open} onOpenChange={(o) => !o && onClose()}>
      <RDialog.Portal>
        <RDialog.Overlay className="modal-fade fixed inset-0 z-50 bg-black/35 dark:bg-black/55" />
        <RDialog.Content
          aria-describedby={undefined}
          data-ks-modal=""
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
      <ReasonProvider>{children}</ReasonProvider>
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

// ---------- 原因框（R-157，例如搁置需求；R-161 菜单 / 拖拽退回时的意见）----------
// 点选一个类别、写一句话，默认都可以不填；required 时必须写（退回意见），multiline 时是多行输入（⌘↵ 提交）。
// 返回 null 表示取消，否则返回选的类别与文字。

// draft：草稿键（R-202，如 reason:R-12:back）——取消、点外面关掉后再打开还在；提交成功后由调用方 dropDraft
export type ReasonOpts = { title: ReactNode; description?: ReactNode; confirmText: string; placeholder: string; kinds: { value: string; label: string }[]; required?: boolean; multiline?: boolean; draft?: string }
export type ReasonResult = { kind: string; note: string }
const ReasonCtx = createContext<(o: ReasonOpts) => Promise<ReasonResult | null>>(async () => null)
export const useAskReason = () => useContext(ReasonCtx)

function ReasonProvider({ children }: { children: ReactNode }) {
  const [opts, setOpts] = useState<ReasonOpts | null>(null)
  const dr = useDraft<{ kind: string; note: string }>(opts?.draft, { kind: '', note: '' })
  const { kind, note } = dr.value
  const setKind = (v: string) => dr.set({ kind: v })
  const setNote = (v: string) => dr.set({ note: v })
  const reset = dr.drop
  const resolver = useRef<(v: ReasonResult | null) => void>(undefined)
  const ask = useCallback((o: ReasonOpts) => {
    reset() // 没有草稿键时（关着的时候）只清组件里的
    setOpts(o)
    return new Promise<ReasonResult | null>((res) => {
      resolver.current = res
    })
  }, [])
  const done = (v: ReasonResult | null) => {
    resolver.current?.(v)
    setOpts(null)
  }
  const blank = !!opts?.required && !note.trim()
  const submit = () => !blank && done({ kind, note: note.trim() })
  return (
    <ReasonCtx.Provider value={ask}>
      {children}
      <Modal
        open={!!opts}
        onClose={() => done(null)}
        title={opts?.title}
        description={opts?.description}
        footer={
          <>
            <Button onClick={() => done(null)}>{t.common.cancel}</Button>
            <Button variant="primary" disabled={blank} onClick={submit}>
              {opts?.confirmText}
            </Button>
          </>
        }
      >
        <div className="space-y-3 pb-1">
          {!!opts?.kinds.length && (
          <div className="flex flex-wrap gap-1.5" role="radiogroup">
            {opts.kinds.map((k) => (
              <button
                key={k.value}
                role="radio"
                aria-checked={kind === k.value}
                onClick={() => setKind(kind === k.value ? '' : k.value)}
                className={cx(
                  'rounded-full border px-3 py-1 text-sm transition-colors',
                  kind === k.value ? 'border-accent bg-accent-soft text-fg' : 'border-line bg-surface text-fg-2 hover:border-line-strong hover:bg-hover',
                )}
              >
                {k.label}
              </button>
            ))}
          </div>
          )}
          {opts?.multiline ? (
            <Textarea
              value={note}
              minRows={3}
              placeholder={opts.placeholder}
              onChange={(e) => setNote(e.target.value)}
              onKeyDown={(e) => (e.metaKey || e.ctrlKey) && e.key === 'Enter' && !e.nativeEvent.isComposing && (e.preventDefault(), submit())}
              autoFocus
            />
          ) : (
            <Input
              value={note}
              maxLength={200}
              placeholder={opts?.placeholder}
              onChange={(e) => setNote(e.target.value)}
              onKeyDown={(e) => e.key === 'Enter' && !e.nativeEvent.isComposing && (e.preventDefault(), submit())}
              autoFocus
            />
          )}
          {opts?.draft && <DraftLine h={dr} className="-mt-2" />}
        </div>
      </Modal>
    </ReasonCtx.Provider>
  )
}

// ---------- 下拉菜单 ----------

export type MenuItem =
  | { type?: 'item'; label: ReactNode; icon?: ReactNode; onSelect: () => void; danger?: boolean; checked?: boolean; hint?: ReactNode; trailing?: ReactNode; disabled?: boolean }
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
                {it.trailing && <span className="flex shrink-0 items-center gap-1.5">{it.trailing}</span>}
                {it.checked && <Check className="size-4 shrink-0 text-accent" />}
              </DropdownMenu.Item>
            ),
          )}
        </DropdownMenu.Content>
      </DropdownMenu.Portal>
    </DropdownMenu.Root>
  )
}
