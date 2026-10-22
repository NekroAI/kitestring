import { forwardRef, useLayoutEffect, useRef, type ButtonHTMLAttributes, type InputHTMLAttributes, type ReactNode, type TextareaHTMLAttributes } from 'react'
import { Switch as RSwitch, Tooltip } from 'radix-ui'
import { AlertTriangle, Loader2 } from 'lucide-react'
import { cx } from '../lib/util'
import { t } from '../i18n'

// ---------- Button ----------

type Variant = 'primary' | 'secondary' | 'ghost' | 'danger' | 'soft'
type Size = 'sm' | 'md' | 'lg' | 'icon' | 'icon-sm'

const variants: Record<Variant, string> = {
  // 主要操作是一方朱印：轮到你做的事
  primary: 'bg-seal text-seal-fg hover:bg-seal-hover shadow-[inset_0_1px_0_rgb(255_255_255/0.22),0_6px_14px_-8px_var(--seal)]',
  secondary: 'bg-surface text-fg border border-line hover:bg-hover hover:border-line-strong shadow-xs',
  ghost: 'text-fg-2 hover:bg-hover hover:text-fg',
  danger: 'bg-surface text-danger border border-line hover:bg-danger-soft hover:border-danger/40',
  soft: 'bg-accent-soft text-accent hover:brightness-95 dark:hover:brightness-110',
}
const sizes: Record<Size, string> = {
  sm: 'h-8 px-2.5 text-[13px] gap-1.5 rounded-lg',
  md: 'h-9 px-3.5 text-sm gap-2 rounded-lg',
  lg: 'h-11 px-5 text-[15px] gap-2 rounded-xl',
  icon: 'h-9 w-9 rounded-lg',
  'icon-sm': 'h-7 w-7 rounded-md',
}

export interface ButtonProps extends ButtonHTMLAttributes<HTMLButtonElement> {
  variant?: Variant
  size?: Size
  loading?: boolean
  icon?: ReactNode
}

export const Button = forwardRef<HTMLButtonElement, ButtonProps>(function Button(
  { variant = 'secondary', size = 'md', loading, icon, className, children, disabled, ...rest },
  ref,
) {
  return (
    <button
      ref={ref}
      disabled={disabled || loading}
      className={cx(
        'inline-flex shrink-0 items-center justify-center font-medium whitespace-nowrap transition-[background,border,color,box-shadow,transform] duration-150 select-none active:scale-[0.97] disabled:pointer-events-none disabled:opacity-50',
        variants[variant],
        sizes[size],
        className,
      )}
      {...rest}
    >
      {loading ? <Loader2 className="size-4 animate-spin" /> : icon}
      {children}
    </button>
  )
})

// ---------- 容器与文字 ----------

export function Card({ className, children, ...rest }: React.HTMLAttributes<HTMLDivElement>) {
  return (
    <div className={cx('rounded-2xl border border-line bg-surface shadow-card', className)} {...rest}>
      {children}
    </div>
  )
}

export function SectionTitle({ children, hint, right, count }: { children: ReactNode; hint?: ReactNode; right?: ReactNode; count?: number }) {
  return (
    <div className="mb-3 flex items-end justify-between gap-3">
      <div className="min-w-0">
        <h2 className="flex items-center gap-2 font-display text-base font-semibold text-fg">
          {children}
          {count != null && <span className="rounded-full bg-sunken px-2 py-px text-xs font-semibold text-fg-2 tabular">{count}</span>}
        </h2>
        {hint && <p className="mt-0.5 text-[13px] text-fg-3">{hint}</p>}
      </div>
      {right}
    </div>
  )
}

export function PageHeader({ title, subtitle, actions }: { title: ReactNode; subtitle?: ReactNode; actions?: ReactNode }) {
  return (
    <div className="mb-5 flex flex-wrap items-end justify-between gap-3 lg:mb-6">
      <div className="min-w-0">
        <h1 className="font-display text-[24px] font-semibold tracking-wide text-fg lg:text-[28px]">{title}</h1>
        {subtitle && <p className="mt-1 text-sm text-fg-3">{subtitle}</p>}
      </div>
      {actions && <div className="flex flex-wrap items-center gap-2">{actions}</div>}
    </div>
  )
}

type Tone = 'neutral' | 'accent' | 'run' | 'wait' | 'reply' | 'danger' | 'idle' | 'outline'
const tones: Record<Tone, string> = {
  neutral: 'bg-sunken text-fg-2',
  accent: 'bg-accent-soft text-accent',
  run: 'bg-run-soft text-run',
  wait: 'bg-wait-soft text-wait',
  reply: 'bg-reply-soft text-reply',
  danger: 'bg-danger-soft text-danger',
  idle: 'bg-idle-soft text-idle',
  outline: 'border border-line text-fg-2',
}

export function Badge({ tone = 'neutral', className, children, icon, mono }: { tone?: Tone; className?: string; children: ReactNode; icon?: ReactNode; mono?: boolean }) {
  return (
    <span className={cx('inline-flex max-w-full items-center gap-1 rounded-md px-1.5 py-0.5 text-xs font-medium whitespace-nowrap', mono && 'font-mono text-[11px]', tones[tone], className)}>
      {icon}
      <span className="truncate">{children}</span>
    </span>
  )
}

export function Kbd({ children }: { children: ReactNode }) {
  return <kbd className="rounded border border-line bg-surface-2 px-1 font-mono text-[10px] text-fg-3">{children}</kbd>
}

export function Skeleton({ className }: { className?: string }) {
  return <div className={cx('animate-shimmer rounded-lg bg-[linear-gradient(90deg,var(--bg-sunken)_25%,var(--surface-hover)_50%,var(--bg-sunken)_75%)] bg-[length:200%_100%]', className)} />
}

export function CardSkeleton({ rows = 3 }: { rows?: number }) {
  return (
    <div className="space-y-3">
      {Array.from({ length: rows }).map((_, i) => (
        <Card key={i} className="space-y-3 p-4">
          <Skeleton className="h-4 w-1/3" />
          <Skeleton className="h-5 w-4/5" />
          <Skeleton className="h-4 w-1/2" />
        </Card>
      ))}
    </div>
  )
}

export function Empty({ icon, title, children, action, className }: { icon: ReactNode; title: ReactNode; children?: ReactNode; action?: ReactNode; className?: string }) {
  return (
    <div className={cx('flex flex-col items-center justify-center px-6 py-14 text-center', className)}>
      <div className="mb-4 flex size-12 items-center justify-center rounded-2xl bg-accent-soft text-accent [&_svg]:size-6">{icon}</div>
      <div className="font-display text-base font-semibold text-fg">{title}</div>
      {children && <div className="mt-1.5 max-w-sm text-sm text-fg-3">{children}</div>}
      {action && <div className="mt-5">{action}</div>}
    </div>
  )
}

export function ErrorState({ error, onRetry }: { error: unknown; onRetry?: () => void }) {
  const msg = error instanceof Error ? error.message : t.common.requestFailed
  return (
    <Card className="flex items-start gap-3 border-danger/30 p-4">
      <AlertTriangle className="mt-0.5 size-5 shrink-0 text-danger" />
      <div className="min-w-0 flex-1">
        <div className="text-sm font-medium text-fg">{t.common.loadFailed}</div>
        <div className="mt-0.5 text-sm break-words text-fg-3">{msg}</div>
      </div>
      {onRetry && (
        <Button size="sm" onClick={onRetry}>
          {t.common.retry}
        </Button>
      )}
    </Card>
  )
}

// ---------- 表单 ----------

export function Field({ label, hint, children, className }: { label?: ReactNode; hint?: ReactNode; children: ReactNode; className?: string }) {
  return (
    <label className={cx('block', className)}>
      {label && <span className="mb-1.5 block text-[13px] font-medium text-fg-2">{label}</span>}
      {children}
      {hint && <span className="mt-1.5 block text-xs leading-relaxed text-fg-3">{hint}</span>}
    </label>
  )
}

const inputCls =
  'block w-full min-w-0 rounded-lg border border-line bg-surface px-3 text-[15px] text-fg shadow-xs transition-[border,box-shadow] placeholder:text-fg-3 hover:border-line-strong focus:border-accent focus:ring-3 focus:ring-[var(--ring)] focus:outline-none disabled:opacity-60 sm:text-sm'

export const Input = forwardRef<HTMLInputElement, InputHTMLAttributes<HTMLInputElement>>(function Input({ className, ...rest }, ref) {
  return <input ref={ref} className={cx(inputCls, 'h-10 sm:h-9', className)} {...rest} />
})

// Textarea 随内容自动增高。
export const Textarea = forwardRef<HTMLTextAreaElement, TextareaHTMLAttributes<HTMLTextAreaElement> & { minRows?: number; unstyled?: boolean }>(function Textarea(
  { className, minRows = 2, value, unstyled, ...rest },
  outer,
) {
  const inner = useRef<HTMLTextAreaElement | null>(null)
  useLayoutEffect(() => {
    const el = inner.current
    if (!el) return
    el.style.height = 'auto'
    el.style.height = `${el.scrollHeight + 2}px`
  }, [value])
  return (
    <textarea
      ref={(el) => {
        inner.current = el
        if (typeof outer === 'function') outer(el)
        else if (outer) outer.current = el
      }}
      rows={minRows}
      value={value}
      className={unstyled ? className : cx(inputCls, 'resize-none py-2 leading-relaxed', className)}
      {...rest}
    />
  )
})

export function Switch({ checked, onChange, disabled }: { checked: boolean; onChange: (v: boolean) => void; disabled?: boolean }) {
  return (
    <RSwitch.Root
      checked={checked}
      onCheckedChange={onChange}
      disabled={disabled}
      className="relative h-[22px] w-[38px] shrink-0 rounded-full bg-line-strong transition-colors data-[state=checked]:bg-accent disabled:opacity-50"
    >
      <RSwitch.Thumb className="block size-[18px] translate-x-[2px] rounded-full bg-white shadow-sm transition-transform duration-200 data-[state=checked]:translate-x-[18px]" />
    </RSwitch.Root>
  )
}

export function Checkbox({ checked, onChange, children, hint }: { checked: boolean; onChange: (v: boolean) => void; children: ReactNode; hint?: ReactNode }) {
  return (
    <label className="flex cursor-pointer items-start gap-3 rounded-lg px-1 py-1.5 hover:bg-hover/60">
      <input type="checkbox" checked={checked} onChange={(e) => onChange(e.target.checked)} className="mt-0.5 size-4 shrink-0 accent-[var(--accent)]" />
      <span className="min-w-0">
        <span className="block text-sm text-fg">{children}</span>
        {hint && <span className="block text-xs text-fg-3">{hint}</span>}
      </span>
    </label>
  )
}

// Segmented 分段控件。选中态用静态样式，切换页面时不会闪动。
export function Segmented<T extends string>({
  value,
  onChange,
  options,
  size = 'md',
}: {
  value: T
  onChange: (v: T) => void
  options: { value: T; label: ReactNode; count?: number; tip?: string }[]
  id?: string
  size?: 'sm' | 'md'
}) {
  return (
    <div className="no-scrollbar inline-flex max-w-full overflow-x-auto rounded-xl bg-sunken p-1">
      {options.map((o) => {
        const active = o.value === value
        const btn = (
          <button
            key={o.value}
            onClick={() => onChange(o.value)}
            aria-pressed={active}
            aria-label={o.tip}
            className={cx(
              'flex shrink-0 items-center gap-1.5 rounded-lg font-medium transition-[background,color,box-shadow] duration-150',
              size === 'sm' ? 'h-7 px-2.5 text-[13px]' : 'h-8 px-3 text-sm',
              active ? 'bg-surface text-fg shadow-xs' : 'text-fg-3 hover:text-fg-2',
            )}
          >
            {o.label}
            {o.count != null && o.count > 0 && <span className={cx('rounded-full px-1.5 text-[11px] font-semibold tabular', active ? 'bg-accent-soft text-accent' : 'bg-line text-fg-2')}>{o.count}</span>}
          </button>
        )
        return o.tip ? (
          <Tip key={o.value} content={o.tip}>
            {btn}
          </Tip>
        ) : (
          btn
        )
      })}
    </div>
  )
}

// Tip：悬浮提示。触屏设备上不显示，所以被提示的元素本身要有可读的含义或 aria-label。
export function Tip({ content, children, side = 'top' }: { content: ReactNode; children: ReactNode; side?: 'top' | 'bottom' | 'left' | 'right' }) {
  if (!content) return <>{children}</>
  return (
    <Tooltip.Root>
      <Tooltip.Trigger asChild>{children}</Tooltip.Trigger>
      <Tooltip.Portal>
        <Tooltip.Content side={side} sideOffset={6} collisionPadding={8} className="tip-pop z-[60] max-w-[280px] rounded-lg bg-fg px-2.5 py-1.5 text-xs leading-relaxed text-bg shadow-pop">
          {content}
        </Tooltip.Content>
      </Tooltip.Portal>
    </Tooltip.Root>
  )
}

export const TipProvider = Tooltip.Provider
