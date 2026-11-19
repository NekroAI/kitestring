import { useEffect, useRef } from 'react'
import { ChevronLeft, ChevronRight } from 'lucide-react'
import type { Schemas } from '../api/client'
import { useSessions, type Asset } from '../api/queries'
import { t } from '../i18n'
import { canReview, recipientOf, reviewKey, type AssetStatus } from '../lib/assets'
import { cx, relTime, useNow } from '../lib/util'
import { Thumbs, uploadFile, type FileRef } from '../ui/attach'
import { toast } from 'sonner'
import type { Frame } from '../lib/anim'
import { FrameChip, useFrameSink, type FrameMark } from './animBar'
import { useActions } from './actions'
import { KeyCaps } from './command'
import { DraftHint, useDraft } from './draft'
import { done, PendingBar, schedule, usePending, Verdict } from './verdict'

// 资源库的审核（R-351 第 4 步，D-1714）：素材抽屉底部的判断栏（通过 / 返工，3 秒撤销期后生效）、审核记录、审片模式的顶栏。
// 意见送回提交素材的会话（已结束时送同项目主调度，Hub 决定）；判断栏旁先提示会送到哪里。
// 草稿（docs/principles.md 第 9 条）按素材存：asset:A-x，翻到下一个、关抽屉后回来还在，提交成功才清。

type Review = Schemas['AssetReview']
type Author = { kind: string; ref?: string; name?: string }

// authorName：审核记录的作者（D-1782）——你 / 会话名 / 访客「名字」；访客给 R-365 资源库分享预留。
export function authorName(a?: Author) {
  const by = t.asset.review.by
  if (!a?.kind || a.kind === 'user') return by.user
  if (a.kind === 'guest') return by.guest(a.name || a.ref || '')
  return by.session(a.name ?? '')
}

// ReviewBar：抽屉底部的判断栏。审核的是最新版本（看旧版本时写明）；制作中不能审；已通过只能改判返工，已返工只能改判通过。
export function ReviewBar({ a, shown, keys, barRef }: { a: Asset; shown: number; keys?: boolean; barRef?: React.RefObject<HTMLDivElement | null> }) {
  const { reviewAsset } = useActions()
  const sessions = useSessions()
  const k = reviewKey(a.code)
  const pend = usePending(k)
  const dr = useDraft<{ text: string; files: FileRef[]; frame?: Frame | null }>(`asset:${a.code}`, {
    text: '',
    files: [],
    frame: null,
  })
  const { text, files } = dr.value
  const frame = dr.value.frame ?? null
  const box = useRef<HTMLTextAreaElement>(null)
  const status = a.status as AssetStatus
  const R = t.asset.review
  const run = (verdict: 'approved' | 'rework') => {
    if (pend || !canReview(status, verdict)) return
    const body = {
      verdict,
      comment: text.trim() || undefined,
      files: files.length ? files.map((f) => f.id) : undefined,
      version: a.version,
      from: status,
      frame: frame ?? undefined,
    }
    schedule(k, {
      act: verdict === 'approved' ? 'pass' : 'back',
      scene: 'asset',
      subject: a.code,
      to: verdict,
      beacon: {
        method: 'POST',
        url: `/api/assets/${encodeURIComponent(a.code)}/review`,
        body,
      },
      commit: () => done(reviewAsset(a.code, body), dr.drop),
    })
  }
  const pass = () => run('approved')
  const back = () => {
    if (pend || !canReview(status, 'rework')) return
    if (!text.trim() && !files.length) return box.current?.focus()
    run('rework')
  }
  // 审片模式：P 通过、R 返工（没写意见时先聚焦意见框）；输入时、看图时不拦截
  const keyRef = useRef({ pass, back })
  keyRef.current = { pass, back }
  useEffect(() => {
    if (!keys) return
    const onKey = (e: KeyboardEvent) => {
      if (e.defaultPrevented || e.metaKey || e.ctrlKey || e.altKey || e.repeat || e.isComposing) return
      const el = e.target as HTMLElement
      if (el.closest?.('input, textarea, [contenteditable=true], [role=menu]')) return
      if ([...document.querySelectorAll('[data-ks-modal]')].some((m) => !barRef?.current || !m.contains(barRef.current))) return
      const key = e.key.toLowerCase()
      if (key === 'p') {
        e.preventDefault()
        keyRef.current.pass()
      } else if (key === 'r') {
        e.preventDefault()
        keyRef.current.back()
      }
    }
    window.addEventListener('keydown', onKey)
    return () => window.removeEventListener('keydown', onKey)
  }, [keys, barRef])

  // 3D 查看里「标记此帧」（R-392）：帧记进草稿，当前画面上传后作为附图，光标落到意见框
  const latest = useRef(dr.value)
  latest.current = dr.value
  const formOpen = !(status === 'wip' && !pend) && !(pend && pend.phase !== 'wait')
  useFrameSink(
    formOpen
      ? (m: FrameMark) => {
          dr.set({ frame: m.frame })
          box.current?.focus()
          uploadFile(new File([m.shot], `frame-${m.frame.anim}-${m.frame.t.toFixed(2)}.jpg`, { type: m.shot.type }))
            .then((f) => dr.set({ files: [...latest.current.files, f] }))
            .catch((e) => toast.error(e instanceof Error && e.message ? e.message : t.asset.anim.markFail))
        }
      : null,
  )

  if (status === 'wip' && !pend) return <div className="shrink-0 border-t border-line px-5 py-3 text-[13px] text-fg-3">{R.wip}</div>
  const to = recipientOf(a.submitter, sessions.data?.sessions ?? [])
  const where = to.kind === 'submitter' ? (to.name ? R.sendTo(to.name) : R.sendToSession) : to.kind === 'pm' ? R.sendToPM : R.sendToSession
  const old = shown !== a.version ? R.latestOnly(t.asset.ver(a.version)) : ''
  return (
    <div className="shrink-0 border-t border-line px-5 pt-3 pb-4 max-lg:pb-[max(1rem,env(safe-area-inset-bottom))]" data-review-bar="">
      {pend && pend.phase !== 'wait' ? (
        <PendingBar k={k} />
      ) : (
        <Verdict
          pendingKey={k}
          scene="asset"
          stack
          noteRef={box}
          note={text}
          onNote={(v) => dr.set({ text: v })}
          files={files}
          onFiles={(v) => dr.set({ files: v })}
          draft={dr}
          placeholder={R.note}
          onPass={pass}
          onBack={canReview(status, 'rework') ? back : undefined}
          canPass={canReview(status, 'approved')}
          size="md"
          extra={
            <>
            {frame && (
              <div className="mb-1">
                <FrameChip frame={frame} onRemove={() => dr.set({ frame: null })} />
              </div>
            )}
            <DraftHint h={dr}>
              <span className="block truncate text-xs text-fg-3" title={old ? `${old}${t.common.sep}${where}` : where}>
                {old ? `${old}${t.common.sep}${where}` : where}
              </span>
            </DraftHint>
            </>
          }
        />
      )}
    </div>
  )
}

// ReviewHistory：审核记录，新的在前。每条写谁、哪一版、通过还是返工、意见和附图，以及送到了哪里。
export function ReviewHistory({ reviews }: { reviews: Review[] }) {
  const now = useNow()
  const R = t.asset.review
  if (!reviews.length) return null
  return (
    <section className="mt-5" data-reviews="">
      <h3 className="mb-2 text-[13px] font-medium text-fg-3">{R.history}</h3>
      <ol className="space-y-2">
        {reviews.map((r) => {
          const verdict = t.asset.status[r.verdict]
          const guest = r.author?.kind === 'guest' // 访客的意见（R-365）：由你放入，写明经由的分享
          const head = guest ? (r.verdict === 'note' ? R.noted(authorName(r.author)) : R.reworkBy(authorName(r.author))) : R.did(authorName(r.author), verdict)
          return (
            <li key={r.id} className="rounded-xl border border-line px-3 py-2.5 text-[13px]" data-review-author={r.author?.kind}>
              <div className="flex min-w-0 items-center gap-2">
                <span className={cx('size-1.5 shrink-0 rounded-full', r.verdict === 'approved' ? 'bg-run' : r.verdict === 'note' ? 'bg-fg-3' : 'bg-danger')} />
                <span className="min-w-0 flex-1 truncate text-fg">
                  {head} <b className="font-mono font-medium">{t.asset.ver(r.version)}</b>
                </span>
                <span className="shrink-0 text-xs text-fg-3">{relTime(r.created_at, now)}</span>
              </div>
              {guest && r.via && <div className="mt-0.5 truncate text-xs text-fg-3">{R.via(r.via)}</div>}
              {r.frame && <FrameChip frame={r.frame} className="mt-1.5" />}
              {r.comment && <p className="mt-1.5 text-[13px] leading-relaxed whitespace-pre-wrap text-fg-2">{r.comment}</p>}
              {r.addend && (
                <p className="mt-1.5 border-t border-dashed border-line pt-1.5 text-[13px] whitespace-pre-wrap text-fg-2">
                  <b className="mr-1 font-semibold">{R.addend}：</b>
                  {r.addend}
                </p>
              )}
              {(r.files?.length ?? 0) > 0 && (
                <div className="mt-2">
                  <Thumbs files={r.files} size="sm" />
                </div>
              )}
              <div className="mt-1.5 truncate text-xs text-fg-3">{deliveryText(r)}</div>
            </li>
          )
        })}
      </ol>
    </section>
  )
}

function deliveryText(r: Review): string {
  const R = t.asset.review
  if (!r.sent_to) return R.noTarget
  const name = r.sent_name || R.anySession
  const head = r.delivery === 'delivered' ? R.delivered(name) : r.delivery === 'closed' ? R.closed : R.waiting(name)
  return r.sent_as === 'pm' ? `${head}${t.common.sep}${R.viaPM}` : head
}

// ReviewNav：审片模式的顶栏——‹ › 切换、第几个、按键提示、退出。
export function ReviewNav({
  pos,
  total,
  prev,
  next,
  finished,
  onPrev,
  onNext,
  onExit,
}: {
  pos: number
  total: number
  prev: boolean
  next: boolean
  finished: boolean
  onPrev: () => void
  onNext: () => void
  onExit: () => void
}) {
  const R = t.asset.review
  const btn =
    'flex size-8 items-center justify-center rounded-lg text-fg-2 transition hover:bg-hover hover:text-fg active:scale-95 disabled:pointer-events-none disabled:opacity-30 pointer-coarse:size-10'
  return (
    <div className="flex shrink-0 items-center gap-1 border-b border-line bg-accent-soft/40 px-3 py-1.5" data-review-nav="">
      <button className={btn} disabled={!prev} aria-label={R.prev} onClick={onPrev}>
        <ChevronLeft className="size-[18px]" />
      </button>
      <button className={btn} disabled={!next} aria-label={R.next} onClick={onNext}>
        <ChevronRight className="size-[18px]" />
      </button>
      <span className="ml-1 text-xs font-medium whitespace-nowrap text-fg-2 tabular">{finished ? R.allDone : R.position(pos, total)}</span>
      <span className="ml-auto hidden items-center gap-3 text-[11px] text-fg-3 lg:flex">
        <span className="flex items-center gap-1">
          <KeyCaps keys={['←', '→']} />
          {R.keySwitch}
        </span>
        <span className="flex items-center gap-1">
          <KeyCaps keys={['P']} />
          {t.verdict.pass('asset')}
        </span>
        <span className="flex items-center gap-1">
          <KeyCaps keys={['R']} />
          {t.verdict.back('asset')}
        </span>
      </span>
      <button className="ml-3 h-7 shrink-0 rounded-lg px-2 text-xs font-medium text-fg-2 transition hover:bg-hover hover:text-fg max-lg:ml-auto" onClick={onExit}>
        {R.exit}
      </button>
    </div>
  )
}
