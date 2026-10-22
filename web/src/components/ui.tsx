import { createContext, useCallback, useContext, useEffect, useRef, useState, type ReactNode } from 'react'
import { t } from '../i18n'

export function cx(...xs: (string | false | null | undefined)[]) {
  return xs.filter(Boolean).join(' ')
}

// ---------- 状态徽标 ----------

const stateStyle: Record<string, string> = {
  running: 'bg-emerald-100 text-emerald-800 dark:bg-emerald-950 dark:text-emerald-300',
  waiting_approval: 'bg-amber-500 text-white dark:bg-amber-500 dark:text-black',
  waiting_reply: 'bg-violet-600 text-white dark:bg-violet-500 dark:text-white',
  idle: 'bg-slate-100 text-slate-700 dark:bg-slate-800 dark:text-slate-300',
  ended: 'bg-transparent text-slate-500 ring-1 ring-slate-300 ring-inset dark:text-slate-500 dark:ring-slate-700',
  lost: 'bg-red-100 text-red-800 dark:bg-red-950 dark:text-red-300',
}

const stateDot: Record<string, string> = {
  running: 'bg-emerald-500',
  waiting_approval: 'bg-white dark:bg-black animate-pulse',
  waiting_reply: 'bg-white animate-pulse',
  idle: 'bg-slate-400',
  ended: 'bg-slate-400',
  lost: 'bg-red-500',
}

export function isWaiting(state: string) {
  return state === 'waiting_approval' || state === 'waiting_reply'
}

export function StateBadge({ state }: { state: string }) {
  return (
    <span className={cx('chip', stateStyle[state] ?? stateStyle.idle)}>
      <span className={cx('size-1.5 rounded-full', stateDot[state] ?? stateDot.idle)} />
      {t.state[state] ?? state}
    </span>
  )
}

const reqStyle: Record<string, string> = {
  draft: 'bg-slate-100 text-slate-600 dark:bg-slate-800 dark:text-slate-400',
  ready: 'bg-indigo-100 text-indigo-800 dark:bg-indigo-950 dark:text-indigo-300',
  in_progress: 'bg-sky-100 text-sky-800 dark:bg-sky-950 dark:text-sky-300',
  blocked: 'bg-red-100 text-red-800 dark:bg-red-950 dark:text-red-300',
  review: 'bg-amber-100 text-amber-900 dark:bg-amber-950 dark:text-amber-300',
  done: 'bg-emerald-100 text-emerald-800 dark:bg-emerald-950 dark:text-emerald-300',
  shelved: 'bg-slate-100 text-slate-500 line-through dark:bg-slate-800 dark:text-slate-500',
}

export function reqStatusClass(status: string) {
  return reqStyle[status] ?? reqStyle.draft
}

export function ReqStatusChip({ status }: { status: string }) {
  return <span className={cx('chip', reqStatusClass(status))}>{t.req.status[status] ?? status}</span>
}

export function CountBadge({ n, tone = 'amber' }: { n: number; tone?: 'amber' | 'red' | 'slate' }) {
  if (!n) return null
  const tones = {
    amber: 'bg-amber-500 text-white dark:text-black',
    red: 'bg-red-600 text-white',
    slate: 'bg-slate-200 text-slate-700 dark:bg-slate-700 dark:text-slate-200',
  }
  return (
    <span className={cx('inline-flex h-5 min-w-5 items-center justify-center rounded-full px-1.5 text-[11px] font-semibold tabular-nums', tones[tone])}>
      {n > 99 ? '99+' : n}
    </span>
  )
}

// ---------- 状态占位 ----------

export function Spinner({ className }: { className?: string }) {
  return <span className={cx('inline-block size-4 animate-spin rounded-full border-2 border-current border-t-transparent', className)} />
}

export function Loading() {
  return (
    <div className="muted flex items-center justify-center gap-2 py-16 text-sm">
      <Spinner /> {t.common.loading}
    </div>
  )
}

export function ErrorBox({ error, onRetry }: { error: unknown; onRetry?: () => void }) {
  return (
    <div className="card flex items-center justify-between gap-3 border-red-200 p-4 text-sm text-red-700 dark:border-red-900 dark:text-red-400">
      <span className="min-w-0 break-words">
        {t.common.labeled(t.common.loadFailed, error instanceof Error ? error.message : String(error))}
      </span>
      {onRetry && (
        <button className="btn btn-sm" onClick={onRetry}>
          {t.common.retry}
        </button>
      )}
    </div>
  )
}

export function Empty({ children }: { children: ReactNode }) {
  return <div className="muted rounded-xl border border-dashed border-slate-300 px-4 py-10 text-center text-sm dark:border-slate-700">{children}</div>
}

// ---------- 弹窗与抽屉 ----------

function useEscape(onClose: () => void) {
  useEffect(() => {
    const h = (e: KeyboardEvent) => e.key === 'Escape' && onClose()
    window.addEventListener('keydown', h)
    return () => window.removeEventListener('keydown', h)
  }, [onClose])
}

function useLockScroll() {
  useEffect(() => {
    const prev = document.body.style.overflow
    document.body.style.overflow = 'hidden'
    return () => {
      document.body.style.overflow = prev
    }
  }, [])
}

export function Modal({ title, onClose, children, footer }: { title: ReactNode; onClose: () => void; children: ReactNode; footer?: ReactNode }) {
  useEscape(onClose)
  useLockScroll()
  return (
    <div className="fixed inset-0 z-50 flex items-end justify-center sm:items-center sm:p-4" role="dialog" aria-modal="true">
      <div className="absolute inset-0 bg-black/40 dark:bg-black/60" onClick={onClose} />
      <div className="card pb-safe relative z-10 flex max-h-[90dvh] w-full flex-col rounded-b-none sm:max-w-lg sm:rounded-b-xl">
        <div className="flex items-center justify-between gap-2 border-b border-slate-200 px-4 py-3 dark:border-slate-800">
          <h2 className="min-w-0 truncate text-base font-semibold">{title}</h2>
          <CloseButton onClick={onClose} />
        </div>
        <div className="overflow-y-auto px-4 py-4">{children}</div>
        {footer && <div className="flex flex-wrap justify-end gap-2 border-t border-slate-200 px-4 py-3 dark:border-slate-800">{footer}</div>}
      </div>
    </div>
  )
}

export function Drawer({ title, onClose, children }: { title: ReactNode; onClose: () => void; children: ReactNode }) {
  useEscape(onClose)
  useLockScroll()
  return (
    <div className="fixed inset-0 z-40 flex justify-end" role="dialog" aria-modal="true">
      <div className="absolute inset-0 hidden bg-black/40 sm:block dark:bg-black/60" onClick={onClose} />
      <div className="pt-safe pb-safe relative z-10 flex h-full w-full flex-col bg-slate-50 shadow-xl sm:max-w-xl sm:border-l sm:border-slate-200 dark:bg-[#0b0f14] sm:dark:border-slate-800">
        <div className="flex items-center justify-between gap-2 border-b border-slate-200 bg-white px-4 py-3 dark:border-slate-800 dark:bg-slate-900">
          <h2 className="min-w-0 truncate text-base font-semibold">{title}</h2>
          <CloseButton onClick={onClose} />
        </div>
        <div className="flex-1 overflow-y-auto overscroll-contain px-4 py-4">{children}</div>
      </div>
    </div>
  )
}

function CloseButton({ onClick }: { onClick: () => void }) {
  return (
    <button className="btn btn-ghost -mr-2 size-10 shrink-0 p-0" onClick={onClick} aria-label={t.common.close}>
      <svg viewBox="0 0 20 20" className="size-5" fill="none" stroke="currentColor" strokeWidth="1.8" strokeLinecap="round">
        <path d="M5 5l10 10M15 5L5 15" />
      </svg>
    </button>
  )
}

// ConfirmButton 点击后弹出确认框，确认后执行 onConfirm。
export function ConfirmButton({
  children,
  message,
  onConfirm,
  className,
  confirmLabel,
  danger = true,
  disabled,
}: {
  children: ReactNode
  message: ReactNode
  onConfirm: () => void | Promise<unknown>
  className?: string
  confirmLabel?: string
  danger?: boolean
  disabled?: boolean
}) {
  const [open, setOpen] = useState(false)
  const [busy, setBusy] = useState(false)
  return (
    <>
      <button type="button" className={className ?? 'btn btn-danger'} onClick={() => setOpen(true)} disabled={disabled}>
        {children}
      </button>
      {open && (
        <Modal
          title={t.common.confirm}
          onClose={() => setOpen(false)}
          footer={
            <>
              <button className="btn" onClick={() => setOpen(false)}>
                {t.common.cancel}
              </button>
              <button
                className={cx('btn', danger ? 'btn-danger' : 'btn-primary')}
                disabled={busy}
                onClick={async () => {
                  setBusy(true)
                  try {
                    await onConfirm()
                    setOpen(false)
                  } finally {
                    setBusy(false)
                  }
                }}
              >
                {busy && <Spinner />}
                {confirmLabel ?? t.common.confirm}
              </button>
            </>
          }
        >
          <p className="text-sm leading-relaxed">{message}</p>
        </Modal>
      )}
    </>
  )
}

// ---------- 轻提示 ----------

type Toast = { id: number; text: string; tone: 'ok' | 'err' }
const ToastCtx = createContext<(text: string, tone?: 'ok' | 'err') => void>(() => {})

export function ToastProvider({ children }: { children: ReactNode }) {
  const [items, setItems] = useState<Toast[]>([])
  const seq = useRef(0)
  const push = useCallback((text: string, tone: 'ok' | 'err' = 'ok') => {
    const id = ++seq.current
    setItems((xs) => [...xs, { id, text, tone }])
    window.setTimeout(() => setItems((xs) => xs.filter((x) => x.id !== id)), tone === 'err' ? 5000 : 2500)
  }, [])
  return (
    <ToastCtx.Provider value={push}>
      {children}
      <div className="pointer-events-none fixed inset-x-0 top-0 z-[60] flex flex-col items-center gap-2 px-4 pt-[max(env(safe-area-inset-top),12px)]">
        {items.map((x) => (
          <div
            key={x.id}
            className={cx(
              'pointer-events-auto max-w-md rounded-lg px-4 py-2 text-sm shadow-lg',
              x.tone === 'ok' ? 'bg-slate-900 text-white dark:bg-slate-100 dark:text-slate-900' : 'bg-red-600 text-white',
            )}
          >
            {x.text}
          </div>
        ))}
      </div>
    </ToastCtx.Provider>
  )
}

export function useToast() {
  return useContext(ToastCtx)
}

// ---------- 其他 ----------

export function CopyButton({ text, className }: { text: string; className?: string }) {
  const [done, setDone] = useState(false)
  return (
    <button
      type="button"
      className={className ?? 'btn btn-sm'}
      onClick={async () => {
        try {
          await navigator.clipboard.writeText(text)
        } catch {
          // 非安全上下文（局域网 http）下 clipboard 不可用，退回旧方法
          const ta = document.createElement('textarea')
          ta.value = text
          document.body.appendChild(ta)
          ta.select()
          document.execCommand('copy')
          ta.remove()
        }
        setDone(true)
        window.setTimeout(() => setDone(false), 1500)
      }}
    >
      {done ? t.common.copied : t.common.copy}
    </button>
  )
}

export function Field({ label, children, hint }: { label: ReactNode; children: ReactNode; hint?: ReactNode }) {
  return (
    <label className="block">
      <span className="label">{label}</span>
      {children}
      {hint && <span className="muted mt-1 block text-xs">{hint}</span>}
    </label>
  )
}

export function Toggle({ checked, onChange, children }: { checked: boolean; onChange: (v: boolean) => void; children: ReactNode }) {
  return (
    <label className="inline-flex min-h-10 cursor-pointer items-center gap-2 text-sm select-none">
      <input type="checkbox" className="size-4 accent-slate-900 dark:accent-slate-100" checked={checked} onChange={(e) => onChange(e.target.checked)} />
      {children}
    </label>
  )
}

// useNow 每隔一段时间刷新，用于相对时间显示。
export function useNow(intervalMs = 30000) {
  const [now, setNow] = useState(Date.now())
  useEffect(() => {
    const id = window.setInterval(() => setNow(Date.now()), intervalMs)
    return () => window.clearInterval(id)
  }, [intervalMs])
  return now
}
