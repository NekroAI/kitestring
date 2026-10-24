import { useState } from 'react'
import { CheckCheck, ChevronRight, FileCheck2 } from 'lucide-react'
import type { Requirement } from '../api/client'
import { useRequirement } from '../api/queries'
import { t } from '../i18n'
import { cx, relTime, useNow } from '../lib/util'
import { Button } from '../ui/base'
import { Thumbs, type FileRef } from '../ui/attach'
import { Item, List } from '../ui/motion'
import { AnnotateTarget } from '../ui/annotate'
import { useOpenReq } from './nav'
import { useReqMenu } from './menus'
import { Ctx } from '../ui/contextMenu'
import { Linkify, RefChip } from '../ui/refmark'
import { EvidenceGist, hasPayload } from './evidence'
import { Seal, usePendingMap, useReqVerdict, Verdict, verdictKey } from './verdict'
import { AdviceNote } from './morning'

// evidenceKind：证据类型的中文名（agent 上报的是 test、screenshot 等英文标识）。
export const evidenceKind = (k: string) => t.evidenceKind[k.toLowerCase()] ?? k

// ReviewRow：一条待验收需求——看证据，然后在卡片底部 [附言][退回][通过]（R-161 统一的 Verdict，R-176 验收用「通过」）。
// 通过、退回都先进撤销期，卡片底部换成撤销条；到时提交后这一行收起，最近动态里记一条。
function ReviewRow({ r, hideTitle, advice }: { r: Requirement; hideTitle?: boolean; advice?: boolean }) {
  const now = useNow()
  const detail = useRequirement(r.code)
  const openReq = useOpenReq()
  const verdict = useReqVerdict()
  const [comment, setComment] = useState('')
  const [files, setFiles] = useState<FileRef[]>([])
  const [incoming, setIncoming] = useState<File[]>([])
  const evidence = (detail.data?.evidence ?? []).filter((e) => !e.voided)
  const ids = files.map((f) => f.id)
  const pass = () => verdict.pass(r.code, { comment, files: ids })
  const back = () => verdict.back(r.code, { comment, files: ids })

  // 整行可点开需求详情（附言、按钮区除外）；嵌在需求详情里时不需要。
  const clickable = !hideTitle
  const stop = (e: React.SyntheticEvent) => e.stopPropagation()
  const menu = useReqMenu()
  return (
    <AnnotateTarget.Provider value={(f) => setIncoming((x) => [...x, f])}>
    <Ctx items={clickable ? menu(r) : []} className="rounded-xl">
    <div
      role={clickable ? 'link' : undefined}
      tabIndex={clickable ? 0 : undefined}
      onClick={clickable ? () => openReq(r.code) : undefined}
      onKeyDown={(e) => {
        if (verdictKey(e, { pass, back: () => (comment.trim() || ids.length ? back() : undefined) })) return
        if (clickable && e.key === 'Enter' && e.target === e.currentTarget) openReq(r.code)
      }}
      className={cx(
        'group/review relative overflow-hidden rounded-xl border border-line bg-surface p-3 transition-[background,border,box-shadow,transform] duration-150 sm:p-3.5',
        clickable && 'cursor-pointer hover:border-line-strong hover:bg-hover/50 hover:shadow-xs active:scale-[0.995]',
      )}
    >
      <div className="flex items-start gap-3">
        <span className="mt-0.5 flex size-7 shrink-0 items-center max-sm:hidden justify-center rounded-lg bg-reply-soft text-reply">
          <FileCheck2 className="size-4" />
        </span>
        <div className="min-w-0 flex-1">
          {hideTitle ? (
            <div className="text-sm font-medium text-reply">{t.reqs.reviewBanner}</div>
          ) : (
            <div className="flex max-w-full items-center gap-2">
              <RefChip code={r.code} status={r.status} link={false} />
              <span className="truncate text-sm font-medium text-fg transition-colors group-hover/review:text-accent">{r.title}</span>
              <ChevronRight className="size-3.5 shrink-0 text-fg-3 transition-transform group-hover/review:translate-x-0.5 group-hover/review:text-accent" />
            </div>
          )}
          {advice && <AdviceNote code={r.code} />}
          {evidence.length ? (
            <ul className="mt-1.5 space-y-1.5">
              {evidence.slice(-3).map((e) => (
                <li key={e.id} className="text-[13px] text-fg-2">
                  <span className="mr-1.5 rounded bg-run-soft px-1 text-[11px] font-medium text-run">{evidenceKind(e.kind)}</span>
                  {hasPayload(e) ? <EvidenceGist e={e} /> : <Linkify text={e.summary} />}
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
      </div>
      <Seal k={r.code} />
      <div className="pt-3 sm:pl-10" onClick={stop}>
        <Verdict scene="accept" note={comment} onNote={setComment} files={files} onFiles={setFiles} incoming={incoming} onIncoming={() => setIncoming([])} onPass={pass} onBack={back} pendingKey={r.code} />
      </div>
    </div>
    </Ctx>
    </AnnotateTarget.Provider>
  )
}

// ReviewList：一组待验收需求；两条以上时可以全部通过（同样走撤销，toast 里一次撤销全部）。
export function ReviewList({ items, className, scroll, advice }: { items: Requirement[]; className?: string; scroll?: boolean; advice?: boolean }) {
  const verdict = useReqVerdict()
  const pend = usePendingMap()
  if (!items.length) return null
  const rest = items.filter((r) => !pend.has(r.code))
  return (
    <div className={cx('space-y-2', className)}>
      <div className={cx(scroll && 'max-h-[420px] overflow-y-auto overscroll-contain pr-1')}>
        <List space={8}>
          {items.map((r) => (
            <Item key={r.id} space={8}>
              <ReviewRow r={r} advice={advice} />
            </Item>
          ))}
        </List>
      </div>
      {rest.length > 1 && (
        <div className="flex justify-end pt-1">
          <Button size="sm" icon={<CheckCheck className="size-3.5" />} onClick={() => verdict.passMany(rest.map((r) => r.code))}>
            {t.verdict.passAll(rest.length)}
          </Button>
        </div>
      )}
    </div>
  )
}

export { ReviewRow }
