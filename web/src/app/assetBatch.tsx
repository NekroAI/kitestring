import { createContext, useContext, useEffect, useMemo, useState, useSyncExternalStore } from 'react'
import { Check, ShieldCheck, SquareCheckBig, Undo2, X } from 'lucide-react'
import { toast } from 'sonner'
import { useAssetReviewers, useSessions } from '../api/queries'
import { t } from '../i18n'
import { MAX_BATCH, movable, pruneSel, rangeSel, sharedCommit, skipGroups, toggleCodes, type Verdict } from '../lib/assetBatch'
import { reviewKey, type AssetLite } from '../lib/assets'
import { cx, useMedia } from '../lib/util'
import { Button, Textarea, Tip } from '../ui/base'
import { Menu, Modal, useConfirm } from '../ui/overlay'
import { useActions } from './actions'
import { DraftHint, useDraft } from './draft'
import { useOpenAsk } from './nav'
import { sessionTitle } from './SessionCard'
import { done, pendingEntry, scheduleMany } from './verdict'

// 资源库批量审核（R-393）：图片墙多选（点选、Shift 连选、全选当前筛选结果），一次通过或返工。
// 先向 Hub 预检会改几个、跳过哪些，确认框里写清楚再执行；执行沿用 3 秒撤销期（每个素材立刻按审核后的状态显示，
// 顶部浮动条只出一条「n 项」，docs/principles.md 第 8 条），到点只发一个请求；生效后页头可撤回最近一次批量审核。
// 另有：会话的批量审核授权（ks asset review，默认不开）、申请标为已在用的标记（等你批准那条决策）。

// ---------- 多选 ----------

export type SelectApi = { on: boolean; has: (code: string) => boolean; toggle: (codes: string[], shift: boolean) => void; enter: (codes: string[]) => void }
// SelectCtx：图片墙的格子从这里读多选状态；访客页没有，格子照常只打开详情。
export const SelectCtx = createContext<SelectApi | null>(null)

const inField = (el: HTMLElement) => !!el.closest?.('input, textarea, [contenteditable=true], [role=menu]')

// useWallSelect：order 是墙上的素材编号（按顺序）。筛选、目录换了以后，选中的只留墙上还在的。
export function useWallSelect(order: string[]) {
  const [on, setOn] = useState(false)
  const [raw, setRaw] = useState<ReadonlySet<string>>(() => new Set())
  const [anchor, setAnchor] = useState<string | null>(null)
  const sel = useMemo(() => pruneSel(raw, order), [raw, order])
  const exit = () => {
    setOn(false)
    setRaw(new Set())
    setAnchor(null)
  }
  // Esc 退出多选（正在输入、有弹层盖着时不拦截）
  useEffect(() => {
    if (!on) return
    const onKey = (e: KeyboardEvent) => {
      if (e.key !== 'Escape' || e.defaultPrevented || inField(e.target as HTMLElement) || document.querySelector('[data-ks-modal]')) return
      e.preventDefault()
      exit()
    }
    window.addEventListener('keydown', onKey)
    return () => window.removeEventListener('keydown', onKey)
  }, [on])
  const api: SelectApi = {
    on,
    has: (c) => sel.has(c),
    toggle: (codes, shift) => {
      setRaw((s) => (shift && anchor ? rangeSel(order, pruneSel(s, order), anchor, codes[codes.length - 1]) : toggleCodes(pruneSel(s, order), codes)))
      setAnchor(codes[0])
    },
    enter: (codes) => {
      setOn(true)
      setRaw(new Set(codes))
      setAnchor(codes[0])
    },
  }
  return {
    api,
    sel,
    start: () => setOn(true),
    exit,
    all: () => setRaw(new Set(order)),
    clear: () => setRaw(new Set()),
  }
}

// PickBox：格子右上角的勾选框，放在格子外层（不嵌进打开详情的按钮里）。多选时一直显示；不在多选时只在能悬停的设备上悬停才出现，点了进入多选。
export function PickBox({ codes, name }: { codes: string[]; name: string }) {
  const s = useContext(SelectCtx)
  const hover = useMedia('(hover: hover)')
  if (!s || (!s.on && !hover)) return null
  const checked = codes.every((c) => s.has(c))
  return (
    <button
      role="checkbox"
      aria-checked={checked}
      aria-label={t.asset.multi.pick(name)}
      data-pick={codes.join(',')}
      onClick={(e) => (s.on ? s.toggle(codes, e.shiftKey) : s.enter(codes))}
      className={cx(
        'absolute top-2 right-2 z-[1] flex size-6 items-center justify-center rounded-md border shadow-xs transition',
        checked ? 'border-accent bg-accent text-white' : 'border-white/80 bg-black/35 text-transparent hover:bg-black/50',
        !s.on && 'opacity-0 group-hover/tile:opacity-100 focus-visible:opacity-100',
      )}
    >
      <Check className="size-4" strokeWidth={3} />
    </button>
  )
}

// useTileClick：多选时点格子是勾选（Shift 连选），否则打开详情。
// pickable：格子右上角有勾选框（右上角的其它标记让开）。
export function useTileClick(codes: string[], open: () => void) {
  const s = useContext(SelectCtx)
  const hover = useMedia('(hover: hover)')
  return {
    pickable: !!s && (s.on || hover),
    picked: !!s?.on && codes.every((c) => s.has(c)),
    onClick: (e: React.MouseEvent) => (s?.on ? s.toggle(codes, e.shiftKey) : open()),
  }
}

// ---------- 底部操作条 ----------

// BatchBar：多选时贴在图片墙底部（手机在底部导航上方）：已选几个、全选当前筛选结果、清空、返工…、通过…
export function BatchBar({ sel, list, total, onAll, onClear, onExit }: { sel: ReadonlySet<string>; list: AssetLite[]; total: number; onAll: () => void; onClear: () => void; onExit: () => void }) {
  const M = t.asset.multi
  const [ask, setAsk] = useState<Verdict | null>(null)
  const picked = useMemo(() => list.filter((a) => sel.has(a.code)), [list, sel])
  const over = picked.length > MAX_BATCH
  const btn = (v: Verdict) => {
    const n = movable(picked, v)
    const b = (
      <Button size="sm" variant={v === 'approved' ? 'primary' : 'secondary'} disabled={over || !n} onClick={() => setAsk(v)} data-batch={v}>
        {v === 'approved' ? M.pass : M.back}
        {n > 0 && <span className="tabular">{n}</span>}
      </Button>
    )
    return over ? (
      <Tip content={M.tooMany(MAX_BATCH)}>
        <span>{b}</span>
      </Tip>
    ) : (
      b
    )
  }
  return (
    <div className="sticky bottom-0 z-10 pt-3 max-lg:bottom-[calc(4rem+env(safe-area-inset-bottom))]" data-batch-bar="">
      <div className="flex flex-wrap items-center gap-x-2 gap-y-2 rounded-2xl border border-line bg-surface/95 px-3 py-2.5 shadow-pop backdrop-blur">
        <Button variant="ghost" size="icon-sm" aria-label={M.exit} onClick={onExit}>
          <X className="size-4" />
        </Button>
        <span className="text-sm font-medium text-fg tabular">{picked.length ? M.picked(picked.length) : <span className="font-normal text-fg-3">{M.none}</span>}</span>
        {picked.length < total && (
          <Tip content={M.allTip}>
            <Button size="sm" variant="ghost" onClick={onAll} data-batch-all="">
              {M.all(total)}
            </Button>
          </Tip>
        )}
        {picked.length > 0 && (
          <Button size="sm" variant="ghost" onClick={onClear}>
            {M.clear}
          </Button>
        )}
        <span className="flex-1" />
        {/* 手机上两个按钮另起一行、各占一半 */}
        <div className="flex gap-2 max-sm:grid max-sm:w-full max-sm:grid-cols-2 max-sm:[&_button]:w-full">
          {btn('rework')}
          {btn('approved')}
        </div>
      </div>
      {ask && <BatchConfirm verdict={ask} codes={picked.map((a) => a.code)} onClose={() => setAsk(null)} onDone={onExit} />}
    </div>
  )
}

type BatchOut = { count: number; assets: string[]; skipped: { code: string; why: string }[] | null; batch_id?: string }

// BatchConfirm：先预检（Hub 按当前状态算会改几个、跳过哪些），确认框写清楚个数；返工在这里写共用的意见（存草稿）。
function BatchConfirm({ verdict, codes, onClose, onDone }: { verdict: Verdict; codes: string[]; onClose: () => void; onDone: () => void }) {
  const M = t.asset.multi
  const { reviewAssetBatch } = useActions()
  const [pre, setPre] = useState<BatchOut | null | undefined>(undefined)
  const dr = useDraft<{ text: string }>('asset-batch', { text: '' })
  const text = dr.value.text
  const back = verdict === 'rework'
  useEffect(() => {
    let live = true
    void reviewAssetBatch({ assets: codes, verdict, dry_run: true }).then((r) => live && setPre((r as BatchOut | undefined) ?? null))
    return () => {
      live = false
    }
  }, []) // eslint-disable-line react-hooks/exhaustive-deps
  useEffect(() => {
    if (pre === null) onClose() // 预检失败：错误已经弹出，关掉确认框
  }, [pre]) // eslint-disable-line react-hooks/exhaustive-deps
  const n = pre?.count ?? 0
  const go = () => {
    if (!pre || !n || (back && !text.trim())) return
    const comment = back ? text.trim() : undefined
    const commits = new Set<() => Promise<boolean>>()
    const shared = sharedCommit(
      pre.assets,
      (c) => {
        const e = pendingEntry(reviewKey(c))
        return !!e && commits.has(e.commit)
      },
      (assets) =>
        done(
          reviewAssetBatch({ assets, verdict, comment }).then((r) => {
            const out = r as BatchOut | undefined
            if (out?.batch_id) setLast({ id: out.batch_id, verdict, n: out.count })
            const changed = out ? assets.length - out.count : 0
            if (changed > 0) toast(M.changed(changed))
            return out
          }),
          back ? dr.drop : undefined,
        ),
    )
    scheduleMany(
      pre.assets.map((code) => {
        const commit = () => shared(code)
        commits.add(commit)
        return { key: reviewKey(code), act: back ? ('back' as const) : ('pass' as const), scene: 'asset' as const, subject: code, to: verdict, commit }
      }),
    )
    onClose()
    onDone()
  }
  const skipped = pre?.skipped ?? []
  const skips = skipGroups(skipped)
  return (
    <Modal
      open
      onClose={onClose}
      wide={back}
      title={pre ? (back ? M.askBack(n) : M.askPass(n)) : M.checking}
      footer={
        <>
          <Button onClick={onClose}>{t.common.cancel}</Button>
          <Button variant={back ? 'danger' : 'primary'} disabled={!pre || !n || (back && !text.trim())} onClick={go} autoFocus={!back} data-batch-go="">
            {t.verdict[back ? 'back' : 'pass']('asset')}
          </Button>
        </>
      }
    >
      {pre && (
        <div className="space-y-2.5 text-sm leading-relaxed text-fg-2" data-batch-pre={n}>
          {!n && <p>{M.nothing}</p>}
          {skips.length > 0 && (
            <p>
              {M.skipped(skipped.length)}
              {skips.map((s) => M.skipItem(s.why, s.n)).join(t.asset.listSep)}
            </p>
          )}
          {n > 0 && <p>{back ? `${M.effect}${M.merge}` : M.effect}</p>}
          {back && n > 0 && (
            <div>
              <Textarea value={text} onChange={(e) => dr.set({ text: e.target.value })} placeholder={M.note} minRows={3} autoFocus />
              <DraftHint h={dr} className="mt-1" />
            </div>
          )}
        </div>
      )}
    </Modal>
  )
}

// ---------- 撤回最近一次 ----------

type Last = { id: string; verdict: Verdict; n: number }
let last: Last | null = null
const lastSubs = new Set<() => void>()
function setLast(v: Last | null) {
  last = v
  lastSubs.forEach((f) => f())
}
const useLast = () =>
  useSyncExternalStore(
    (f) => (lastSubs.add(f), () => lastSubs.delete(f)),
    () => last,
  )

// UndoLastBatch：页头「撤回批量审核…」，撤回这个页面里最近一次生效的批量审核（Hub 只认你最近的那一次）。
export function UndoLastBatch() {
  const M = t.asset.multi
  const l = useLast()
  const confirm = useConfirm()
  const { undoAssetBatch } = useActions()
  if (!l) return null
  const run = async () => {
    if (!(await confirm({ title: M.askUndo, description: M.undoBody(l.n), confirmText: M.undoOk }))) return
    const r = await undoAssetBatch(l.id)
    setLast(null)
    if (r) toast(M.undone(r.restored?.length ?? 0, r.kept?.length ?? 0))
  }
  return (
    <Tip content={M.undoTip(t.verdict[l.verdict === 'approved' ? 'pass' : 'back']('asset'), l.n)}>
      <Button size="sm" variant="ghost" className="text-fg-2" icon={<Undo2 className="size-4" />} onClick={run} data-batch-undo="">
        {M.undo}
      </Button>
    </Tip>
  )
}

// ---------- 进入多选、授权 ----------

export function SelectButton({ on, onClick }: { on: boolean; onClick: () => void }) {
  return (
    <Button size="sm" variant={on ? 'secondary' : 'ghost'} className={cx(!on && 'text-fg-2')} icon={<SquareCheckBig className="size-4" />} onClick={onClick} aria-pressed={on} data-select-mode="">
      {on ? t.asset.multi.exit : t.asset.multi.enter}
    </Button>
  )
}

// ReviewerMenu：本项目的会话里，哪些可以用 ks asset review 批量审（默认都不开）。已结束但还授权着的也列出，方便收回。
export function ReviewerMenu({ project }: { project: string }) {
  const R = t.asset.reviewers
  const sessions = useSessions().data?.sessions ?? []
  const allowed = new Set(useAssetReviewers().data ?? [])
  const { setAssetReviewer } = useActions()
  const list = sessions.filter((s) => s.project_id === project && (s.state !== 'ended' || allowed.has(s.key)))
  const n = list.filter((s) => allowed.has(s.key)).length
  return (
    <Menu
      width="min-w-[260px] max-w-[340px]"
      trigger={
        <Button size="sm" variant="ghost" className="text-fg-2" icon={<ShieldCheck className="size-4" />} data-reviewers={n}>
          {R.menu}
          {n > 0 && <span className="text-accent tabular">{n}</span>}
        </Button>
      }
      items={[
        { type: 'label', label: R.hint },
        ...(list.length
          ? list.map((s) => ({
              label: sessionTitle(s),
              hint: s.state === 'ended' ? R.ended : undefined,
              checked: allowed.has(s.key),
              onSelect: () => void setAssetReviewer(s.key, !allowed.has(s.key)),
            }))
          : [{ label: R.none, disabled: true, onSelect: () => {} }]),
      ]}
    />
  )
}

// ---------- 申请标为已在用 ----------

// BaselineChip：图片墙格子上的小标记（格子本身是按钮，这里只是文字）。
export function BaselineChip({ code }: { code?: string }) {
  if (!code) return null
  return (
    <span className="rounded-md bg-black/55 px-1.5 text-[11px] leading-[20px] text-white" data-baseline={code} title={t.asset.baseline.tip(code)}>
      {t.asset.baseline.mark}
    </span>
  )
}

// BaselineLink：素材详情里的标记，点开那条批准类决策（在审批抽屉里批准或拒绝，docs/principles.md 第 2 条）。
export function BaselineLink({ code }: { code?: string }) {
  const openAsk = useOpenAsk()
  if (!code) return null
  return (
    <Tip content={t.asset.baseline.tip(code)}>
      <Button size="sm" className="mt-0.5 shrink-0" onClick={() => openAsk(`d:${code}`)} data-baseline-open={code}>
        {t.asset.baseline.mark}
        <span className="font-mono text-fg-3">{code}</span>
      </Button>
    </Tip>
  )
}
