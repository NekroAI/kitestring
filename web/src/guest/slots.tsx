import { createContext, useContext, useEffect, useRef, useState, type ReactNode } from 'react'
import { useQueryClient } from '@tanstack/react-query'
import { MessageSquare, X } from 'lucide-react'
import { toast } from 'sonner'
import { errMsg } from '../api/client'
import { t } from '../i18n'
import { absTime, cx, relTime, useNow } from '../lib/util'
import { Badge, Button, Textarea, Tip } from '../ui/base'
import { RefChip } from '../ui/refmark'
import { DraftLine, useDraft } from '../app/draft'
import { gk, postComment, type GuestComment } from './data'

// 访客留言（R-281 第 4 段）：留言挂在需求、决策、证据、阶段这些具体对象上。
// 对象旁的「留言」按钮把页面底部的留言框指向它；需求页默认留给整条需求。留言先到分享者的待收队列，确认后才加进需求留言。
export type CommentTarget = { kind: 'req' | 'dec' | 'evidence' | 'stage'; ref: string; req?: string; label?: string }

type Ctx = { target: CommentTarget | null; setTarget: (x: CommentTarget | null) => void; token: string }
const ComposeContext = createContext<Ctx | null>(null)

const same = (a: CommentTarget | null, b: CommentTarget | null) => !!a && !!b && a.kind === b.kind && a.ref === b.ref

// ComposeScope：一页一个留言框；换页时回到这一页的默认对象。
export function ComposeScope({ token, children }: { token: string; children: ReactNode }) {
  const [target, setTarget] = useState<CommentTarget | null>(null)
  return <ComposeContext.Provider value={{ target, setTarget, token }}>{children}</ComposeContext.Provider>
}

export function CommentSlot({ target, className }: { target: CommentTarget; className?: string }) {
  const c = useContext(ComposeContext)
  if (!c) return null
  const on = same(c.target, target)
  return (
    <button
      type="button"
      data-comment-slot={target.kind}
      aria-pressed={on}
      aria-label={t.guest.leaveAria(target.label ?? target.ref)}
      onClick={() => c.setTarget(target)}
      className={cx('inline-flex items-center gap-1 rounded-md px-1.5 py-0.5 text-xs font-medium text-accent transition-colors hover:bg-accent-soft', on && 'bg-accent-soft', className)}
    >
      <MessageSquare className="size-3.5" aria-hidden />
      {t.guest.leave}
    </button>
  )
}

// TargetLabel：留言对象写成印记（需求、决策）或一句短标签（证据、阶段）。
export function TargetLabel({ x }: { x: Pick<CommentTarget, 'kind' | 'ref' | 'label'> }) {
  if (x.kind === 'req' || x.kind === 'dec') return <RefChip code={x.label ?? x.ref} />
  return <span className="text-fg-2">{x.label}</span>
}

// Compose：页面底部的留言框。fallback 是这一页默认的对象（需求页是整条需求）；没有对象时不显示。
export function Compose({ fallback }: { fallback?: CommentTarget }) {
  const c = useContext(ComposeContext)
  const qc = useQueryClient()
  const target = c?.target ?? fallback ?? null
  const draft = useDraft(target && c ? `guestc:${c.token}:${target.kind}:${target.ref}` : undefined, { text: '' })
  const [busy, setBusy] = useState(false)
  const box = useRef<HTMLTextAreaElement>(null)
  // 点了某个对象的「留言」：聚焦输入框
  const picked = c?.target
  useEffect(() => {
    if (picked) box.current?.focus({ preventScroll: true })
  }, [picked])
  if (!c || !target) return null
  const send = async () => {
    const text = draft.value.text.trim()
    if (!text || busy) return
    setBusy(true)
    try {
      await postComment(c.token, { target_kind: target.kind, target_ref: target.ref, text })
      draft.drop()
      toast.success(t.guest.sent)
      void qc.invalidateQueries({ queryKey: gk.comments(c.token) })
    } catch (e) {
      toast.error(errMsg(e, t.guest.sendFail))
    } finally {
      setBusy(false)
    }
  }
  return (
    <form
      data-guest-compose=""
      onSubmit={(e) => {
        e.preventDefault()
        void send()
      }}
      className="sticky bottom-0 z-10 mt-auto border-t border-line bg-surface/95 px-4 pt-3 pb-[max(0.75rem,env(safe-area-inset-bottom))] backdrop-blur sm:px-6"
    >
      <div className="mx-auto grid w-full max-w-3xl gap-2">
      <div className="flex flex-wrap items-center gap-1.5 text-[12.5px] text-fg-3">
        {t.guest.leaveTo}
        <TargetLabel x={target} />
        {c.target && fallback && !same(c.target, fallback) && (
          <button type="button" onClick={() => c.setTarget(null)} className="rounded-md px-1.5 py-0.5 text-xs text-fg-3 hover:bg-hover hover:text-fg">
            {t.guest.toWhole}
          </button>
        )}
        {c.target && !fallback && (
          <button type="button" aria-label={t.guest.closeCompose} onClick={() => c.setTarget(null)} className="ml-auto flex size-6 items-center justify-center rounded-md text-fg-3 hover:bg-hover hover:text-fg">
            <X className="size-3.5" />
          </button>
        )}
      </div>
      <div className="flex items-end gap-2">
        <Textarea
          ref={box}
          minRows={2}
          maxLength={2000}
          placeholder={t.guest.placeholder}
          value={draft.value.text}
          onChange={(e) => draft.set({ text: e.target.value })}
          onKeyDown={(e) => {
            if (e.key === 'Enter' && (e.metaKey || e.ctrlKey) && !e.nativeEvent.isComposing) {
              e.preventDefault()
              void send()
            }
          }}
          className="flex-1"
        />
        <Button type="submit" variant="primary" loading={busy} disabled={!draft.value.text.trim()}>
          {t.guest.send}
        </Button>
      </div>
      <DraftLine h={draft} className="mt-0" />
      </div>
    </form>
  )
}

const statusTone: Record<string, 'neutral' | 'run' | 'wait'> = { pending: 'wait', placed: 'run', ignored: 'neutral' }

// commentTarget：访客自己的一条留言针对的对象。
export function commentTarget(x: GuestComment): CommentTarget {
  if (x.target_kind === 'dec') return { kind: 'dec', ref: x.target_code ?? '', label: x.target_code }
  if (x.target_kind === 'evidence') return { kind: 'evidence', ref: x.target_ref, label: t.guest.onEvidence(x.target_title ?? '') }
  if (x.target_kind === 'stage') return { kind: 'stage', ref: x.target_ref, label: t.guest.onStage(x.target_code ?? '', x.target_seq ?? 0) }
  return { kind: 'req', ref: x.req ?? '', label: x.req }
}

// MyComments：访客自己写过的留言和处理结果（待分享者确认 / 已放入需求 / 分享者已查看）。
export function MyComments({ items }: { items: GuestComment[] }) {
  const now = useNow()
  if (!items.length) return null
  return (
    <section className="min-w-0" data-guest-mine="">
      <h2 className="mb-2 text-xs font-semibold tracking-[0.04em] text-fg-3">{t.guest.mine}</h2>
      <div className="grid gap-1.5">
        {items.map((x) => (
          <div key={x.id} className="rounded-[10px] bg-surface-2 px-3 py-2 text-[13px]">
            <div className="mb-0.5 flex flex-wrap items-center gap-1.5 text-xs text-fg-3">
              {t.guest.leaveTo}
              <TargetLabel x={commentTarget(x)} />
              <Tip content={absTime(x.created_at)}>
                <span className="tabular">{relTime(x.created_at, now)}</span>
              </Tip>
              <Badge tone={statusTone[x.status] ?? 'neutral'} className="ml-auto">
                {t.guest.mineStatus[x.status] ?? x.status}
              </Badge>
            </div>
            <p className="break-words whitespace-pre-wrap text-fg-2">{x.text}</p>
          </div>
        ))}
      </div>
    </section>
  )
}
