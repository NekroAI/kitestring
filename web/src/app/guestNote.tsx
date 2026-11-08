import { useState, type ReactNode } from 'react'
import { toast } from 'sonner'
import { api, errMsg, unwrap, type Decision } from '../api/client'
import { useWorkbench } from './inbox'
import type { ShareComment } from '../api/queries'
import { t } from '../i18n'
import { absTime, cx, relTime, useNow } from '../lib/util'
import { Badge, Button, Segmented, Textarea, Tip } from '../ui/base'
import { Linkify, RefChip } from '../ui/refmark'
import { DraftField, DraftHint, useDraft } from './draft'
import { guestTarget } from './asks'
import { PendingCover, schedule, Seal, usePending } from './verdict'

// 访客留言（R-281 第 4 段）：所有者这一侧。
// 待收的访客留言在「待处理」里，点开进审批抽屉（原则 2）：原样或改写放入需求（都可以加附言，改写保留原话），或忽略；
// 放入 / 忽略都走全站同一个 3 秒撤销期（原则 8，场景 guest：放入 / 忽略）。放入后成为那条需求的留言，时间线写明来源。

const inbox = t.share.inbox

// 放入 / 忽略生效后记在这里：条目离开「待处理」后抽屉里仍显示结果（从缓存打开时不再显示可操作的表单）
const handled = new Map<string, { act: 'place' | 'ignore'; code?: string }>()

type Origin = NonNullable<Decision['guest']>

// GuestSource：放入的访客留言的来源一行——「来自「小王」（经分享链接），由你放入」，针对决策 / 证据 / 阶段时跟着写对象。
export function GuestSource({ g, className }: { g: Pick<Origin, 'name' | 'target_kind' | 'target_code' | 'target_seq' | 'target_title'>; className?: string }) {
  const on = guestTarget(g)
  return (
    <span className={cx('inline-flex min-w-0 flex-wrap items-center gap-x-1.5', className)}>
      <b className="font-semibold text-fg-2">{inbox.from(g.name)}</b>
      {on && (g.target_kind === 'dec' ? <RefChip code={on} /> : <span className="text-fg-3">{on}</span>)}
    </span>
  )
}

// GuestNoteContent：放入后的正文、改写前的原话（折叠）、附言。时间线和抽屉里的预览共用。
export function GuestNoteContent({ text, original, addend, className }: { text: string; original?: string; addend?: string; className?: string }) {
  return (
    <div className={cx('grid gap-1.5', className)}>
      <div className="text-[14px] leading-relaxed break-words whitespace-pre-wrap">
        <Linkify text={text} />
      </div>
      {!!original && (
        <details className="text-[12.5px] text-fg-3">
          <summary className="cursor-pointer select-none">{inbox.original}</summary>
          <p className="mt-1 border-l-2 border-line-strong pl-2.5 break-words whitespace-pre-wrap">{original}</p>
        </details>
      )}
      {!!addend && (
        <p className="border-t border-dashed border-line pt-1.5 text-[13px] break-words whitespace-pre-wrap text-fg-2">
          <b className="mr-1 font-semibold">{inbox.yourAddend}：</b>
          {addend}
        </p>
      )}
    </div>
  )
}

function Scroll({ children }: { children: ReactNode }) {
  return <div className="min-h-0 flex-1 space-y-4 overflow-y-auto overscroll-contain bg-bg/40 px-4 py-5 sm:px-5">{children}</div>
}

// GuestNoteBody：审批抽屉里处理一条访客留言。
export function GuestNoteBody({ c, top }: { c: ShareComment; top: ReactNode }) {
  const now = useNow()
  const wb = useWorkbench()
  const key = `guest:${c.id}`
  const pend = usePending(key)
  const live = wb.guests.some((x) => x.id === c.id)
  const done = handled.get(c.id)
  const init = { mode: 'raw' as 'raw' | 'rewrite', text: c.text, addend: '', req: c.choices?.[0]?.code ?? '' }
  // 只有真写了东西（改写、附言、换了需求）才存草稿（原则 9）
  const draft = useDraft(`guest:${c.id}`, init, (x) => x.mode !== init.mode || x.text !== init.text || x.addend.trim() !== '' || x.req !== init.req)
  const v = draft.value
  const stage = c.target_kind === 'stage'
  const reqCode = stage ? v.req : c.requirement_code || ''
  const body = v.mode === 'rewrite' ? v.text : c.text
  const rewritten = v.mode === 'rewrite' && v.text.trim() !== '' && v.text.trim() !== c.text.trim()
  const [err, setErr] = useState('')

  const go = (act: 'place' | 'ignore') => {
    setErr('')
    if (act === 'place' && v.mode === 'rewrite' && !v.text.trim()) return setErr(inbox.rewriteAria)
    schedule(key, {
      act: act === 'place' ? 'pass' : 'back',
      scene: 'guest',
      subject: reqCode || c.target_code || inbox.title,
      beacon:
        act === 'place'
          ? { method: 'POST', url: `/api/share-comments/${c.id}/place`, body: { text: rewritten ? v.text.trim() : '', addend: v.addend.trim(), requirement: stage ? reqCode : '' } }
          : { method: 'POST', url: `/api/share-comments/${c.id}/ignore`, body: {} },
      commit: async () => {
        try {
          if (act === 'place') {
            const r = await unwrap(
              api.POST('/api/share-comments/{id}/place', {
                params: { path: { id: c.id } },
                body: { text: rewritten ? v.text.trim() : '', addend: v.addend.trim(), requirement: stage ? reqCode : '' },
              }),
            )
            handled.set(c.id, { act, code: r.decision.requirement_code })
          } else {
            await unwrap(api.POST('/api/share-comments/{id}/ignore', { params: { path: { id: c.id } } }))
            handled.set(c.id, { act })
          }
          draft.drop()
          return true
        } catch (e) {
          toast.error(errMsg(e, act === 'place' ? inbox.fail.place : inbox.fail.ignore))
          return false
        }
      },
    })
  }

  const head = (
    <>
      <h3 className="font-display text-[17px] leading-snug font-semibold text-balance text-fg">{stage ? `${inbox.onStage(c.target_code ?? '', c.target_seq ?? 0)}${t.common.sep}${c.target_title}` : c.requirement_title}</h3>
      <div className="flex flex-wrap items-center gap-x-2 gap-y-1 text-[12.5px] text-fg-3">
        <b className="font-semibold tracking-wider text-accent">{t.ask.kind.guest}</b>
        <span className="font-medium text-fg-2">{c.guest_name}</span>
        <span aria-hidden>·</span>
        <span className="min-w-0 truncate">{inbox.via(c.share_name)}</span>
        <span aria-hidden>·</span>
        <Tip content={absTime(c.created_at)}>
          <span className="tabular">{relTime(c.created_at, now)}</span>
        </Tip>
      </div>
    </>
  )

  // 已处理（离开了「待处理」、不在撤销期里）：只显示结果
  if (!live && !pend) {
    return (
      <div className="flex min-h-0 flex-1 flex-col">
        {top}
        <Scroll>
          {head}
          <div className="grid justify-items-start gap-2">
            {done?.act === 'ignore' ? (
              <>
                <Badge>{inbox.ignored}</Badge>
                <span className="text-[13px] text-fg-3">{inbox.ignoredHint}</span>
              </>
            ) : done ? (
              <Badge tone="run">{inbox.placed(done.code ?? '')}</Badge>
            ) : (
              <span className="text-[13px] text-fg-3">{inbox.handled}</span>
            )}
          </div>
        </Scroll>
      </div>
    )
  }

  const target = guestTarget(c)
  return (
    <div className="flex min-h-0 flex-1 flex-col">
      {top}
      <Scroll>
        {head}
        <section className="grid gap-1.5">
          <div className="flex flex-wrap items-center gap-1.5 text-[12.5px] text-fg-3">
            {inbox.to}
            {c.requirement_code && <RefChip code={c.requirement_code} />}
            {target && (c.target_kind === 'dec' ? <RefChip code={target} /> : <span className="text-fg-2">{target}</span>)}
          </div>
          <div className="rounded-xl bg-surface-2 px-3.5 py-3 text-[14px] leading-relaxed break-words whitespace-pre-wrap">{c.text}</div>
        </section>
        {stage && (
          <section className="grid gap-1.5">
            <span className="text-[13px] font-medium text-fg-2">{inbox.pickReq}</span>
            {c.choices?.length ? (
              <div className="grid gap-1">
                {c.choices.map((r) => (
                  <label key={r.code} className={cx('flex h-9 cursor-pointer items-center gap-2 rounded-lg border px-2.5 text-[13.5px] transition-colors', v.req === r.code ? 'border-accent/60 bg-accent-soft/50' : 'border-line hover:border-line-strong')}>
                    <input type="radio" name={`pick-${c.id}`} className="accent-[var(--accent)]" checked={v.req === r.code} onChange={() => draft.set({ req: r.code })} />
                    <span className="shrink-0 font-mono text-[11.5px] text-fg-3">{r.code}</span>
                    <span className="min-w-0 truncate">{r.title}</span>
                  </label>
                ))}
              </div>
            ) : (
              <p className="text-[13px] text-fg-3">{inbox.noReq}</p>
            )}
          </section>
        )}
        <section className="grid gap-1.5">
          <span className="text-[13px] font-medium text-fg-2">{inbox.mode}</span>
          <Segmented
            size="sm"
            value={v.mode}
            onChange={(m) => draft.set({ mode: m })}
            options={[
              { value: 'raw', label: inbox.raw },
              { value: 'rewrite', label: inbox.rewrite },
            ]}
          />
          {v.mode === 'rewrite' && (
            <>
              <Textarea aria-label={inbox.rewriteAria} minRows={3} value={v.text} onChange={(e) => draft.set({ text: e.target.value })} />
              <span className="text-xs text-fg-3">{inbox.keepOriginal}</span>
            </>
          )}
        </section>
        <section className="grid gap-1.5">
          <label htmlFor={`addend-${c.id}`} className="text-[13px] font-medium text-fg-2">
            {inbox.addend}
          </label>
          <DraftField h={draft} empty={init}>
            {(pad) => <Textarea id={`addend-${c.id}`} minRows={2} placeholder={inbox.addendPlaceholder} value={v.addend} onChange={(e) => draft.set({ addend: e.target.value })} className={pad} />}
          </DraftField>
        </section>
        {reqCode && (
          <section className="grid gap-1.5" data-guest-preview="">
            <span className="text-[13px] font-medium text-fg-2">{inbox.preview(reqCode)}</span>
            <div className="grid gap-1.5 rounded-xl border border-line bg-surface px-3.5 py-3">
              <div className="text-[12.5px] text-fg-3">
                <GuestSource g={{ name: c.guest_name, target_kind: c.target_kind, target_code: c.target_code, target_seq: c.target_seq, target_title: c.target_title }} />
              </div>
              <GuestNoteContent text={body} original={rewritten ? c.text : ''} addend={v.addend.trim()} />
            </div>
          </section>
        )}
        {err && <p className="text-[13px] text-danger">{err}</p>}
      </Scroll>
      <footer className="relative shrink-0 border-t border-line bg-surface px-4 pt-3 pb-[max(0.875rem,env(safe-area-inset-bottom))] sm:px-5">
        <div className="flex flex-wrap items-center gap-2">
          <span className="min-w-[140px] flex-1 text-xs text-fg-3">
            <DraftHint h={draft}>{reqCode ? inbox.notify(reqCode) : ''}</DraftHint>
          </span>
          <Button disabled={!!pend} onClick={() => go('ignore')}>
            {inbox.ignore}
          </Button>
          <Button variant="primary" disabled={!!pend || !reqCode} onClick={() => go('place')}>
            {inbox.place(reqCode)}
          </Button>
        </div>
        <PendingCover k={key} />
        <Seal k={key} />
      </footer>
    </div>
  )
}
