import { useState } from 'react'
import { Check, CheckCheck, ChevronRight, FileCheck2, PenLine } from 'lucide-react'
import { toast } from 'sonner'
import type { Requirement } from '../api/client'
import { useRequirement } from '../api/queries'
import { t } from '../i18n'
import { cx, relTime, useNow } from '../lib/util'
import { Button, Textarea } from '../ui/base'
import { AttachZone, Thumbs, type FileRef } from '../ui/attach'
import { Collapse, Item, List } from '../ui/motion'
import { useActions, useBusy } from './actions'
import { useOpenReq } from './nav'

// ReviewRow：一条待验收需求——看证据，然后通过或提修改意见（可贴图）。
function ReviewRow({ r, onDone, hideTitle }: { r: Requirement; onDone?: () => void; hideTitle?: boolean }) {
  const now = useNow()
  const detail = useRequirement(r.code)
  const { review } = useActions()
  const openReq = useOpenReq()
  const [busy, wrap] = useBusy()
  const [editing, setEditing] = useState(false)
  const [comment, setComment] = useState('')
  const [files, setFiles] = useState<FileRef[]>([])
  const evidence = (detail.data?.evidence ?? []).filter((e) => !e.voided)

  const pass = () =>
    wrap(async () => {
      if (await review([{ ref: r.code, verdict: 'accept' }])) {
        toast.success(t.review.passed(r.code))
        onDone?.()
      }
    })
  const revise = () =>
    wrap(async () => {
      if (await review([{ ref: r.code, verdict: 'revise', comment, files: files.map((f) => f.id) }])) {
        toast(t.review.revised(r.code))
        onDone?.()
      }
    })

  // 整行可点开需求详情（按钮、改意见区除外）；嵌在需求详情里时不需要。
  const clickable = !hideTitle
  const stop = (e: React.SyntheticEvent) => e.stopPropagation()
  return (
    <div
      role={clickable ? 'link' : undefined}
      tabIndex={clickable ? 0 : undefined}
      onClick={clickable ? () => openReq(r.code) : undefined}
      onKeyDown={clickable ? (e) => e.key === 'Enter' && e.target === e.currentTarget && openReq(r.code) : undefined}
      className={cx(
        'group/review rounded-xl border border-line bg-surface p-3 transition-[background,border,box-shadow,transform] duration-150 sm:p-3.5',
        clickable && 'cursor-pointer hover:border-line-strong hover:bg-hover/50 hover:shadow-xs active:scale-[0.995]',
      )}
    >
      <div className="flex items-start gap-3">
        <span className="mt-0.5 flex size-7 shrink-0 items-center justify-center rounded-lg bg-reply-soft text-reply">
          <FileCheck2 className="size-4" />
        </span>
        <div className="min-w-0 flex-1">
          {hideTitle ? (
            <div className="text-sm font-medium text-reply">{t.reqs.reviewBanner}</div>
          ) : (
            <div className="flex max-w-full items-center gap-2">
              <span className="shrink-0 font-mono text-[11px] font-semibold text-fg-3">{r.code}</span>
              <span className="truncate text-sm font-medium text-fg transition-colors group-hover/review:text-accent">{r.title}</span>
              <ChevronRight className="size-3.5 shrink-0 text-fg-3 transition-transform group-hover/review:translate-x-0.5 group-hover/review:text-accent" />
            </div>
          )}
          {evidence.length ? (
            <ul className="mt-1.5 space-y-1.5">
              {evidence.slice(-3).map((e) => (
                <li key={e.id} className="text-[13px] text-fg-2">
                  <span className="mr-1.5 rounded bg-run-soft px-1 text-[11px] font-medium text-run">{e.kind}</span>
                  {e.summary}
                  <span className="ml-1.5 text-xs text-fg-3">{relTime(e.created_at, now)}</span>
                  {!!e.files?.length && (
                    <div className="mt-1.5 w-fit" onClick={stop} onKeyDown={stop}>
                      <Thumbs files={e.files} size="sm" />
                    </div>
                  )}
                </li>
              ))}
            </ul>
          ) : (
            detail.data && <p className="mt-1 text-xs text-fg-3">{t.review.noEvidence}</p>
          )}
        </div>
        {!editing && (
          <div className="flex shrink-0 gap-1.5" onClick={stop} onKeyDown={stop}>
            <Button size="sm" variant="ghost" icon={<PenLine className="size-3.5" />} disabled={busy} onClick={() => setEditing(true)}>
              <span className="max-sm:hidden">{t.review.revise}</span>
            </Button>
            <Button size="sm" variant="primary" icon={<Check className="size-3.5" />} loading={busy} onClick={pass}>
              {t.review.pass}
            </Button>
          </div>
        )}
      </div>
      <Collapse show={editing}>
        <div className="space-y-2 pt-3 sm:pl-10" onClick={stop} onKeyDown={stop}>
          <AttachZone value={files} onChange={setFiles} compact>
            <Textarea autoFocus minRows={3} value={comment} placeholder={t.review.revisePlaceholder} onChange={(e) => setComment(e.target.value)} className="pr-10" />
          </AttachZone>
          <div className="flex justify-end gap-2">
            <Button size="sm" variant="ghost" onClick={() => setEditing(false)}>
              {t.common.cancel}
            </Button>
            <Button size="sm" variant="primary" loading={busy} disabled={!comment.trim() && !files.length} onClick={revise}>
              {t.review.sendRevise}
            </Button>
          </div>
        </div>
      </Collapse>
    </div>
  )
}

// ReviewList：一组待验收需求；两条以上时可以全部通过。
export function ReviewList({ items, className, scroll }: { items: Requirement[]; className?: string; scroll?: boolean }) {
  const { review } = useActions()
  const [busy, wrap] = useBusy()
  if (!items.length) return null
  return (
    <div className={cx('space-y-2', className)}>
      <div className={cx(scroll && 'max-h-[420px] overflow-y-auto overscroll-contain pr-1')}>
        <List space={8}>
          {items.map((r) => (
            <Item key={r.id} space={8}>
              <ReviewRow r={r} />
            </Item>
          ))}
        </List>
      </div>
      {items.length > 1 && (
        <div className="flex justify-end pt-1">
          <Button
            size="sm"
            icon={<CheckCheck className="size-3.5" />}
            loading={busy}
            onClick={() =>
              wrap(async () => {
                if (await review(items.map((r) => ({ ref: r.code, verdict: 'accept' as const })))) toast.success(t.review.passed(items.map((r) => r.code).join('、')))
              })
            }
          >
            {t.review.passAll(items.length)}
          </Button>
        </div>
      )}
    </div>
  )
}

export { ReviewRow }
