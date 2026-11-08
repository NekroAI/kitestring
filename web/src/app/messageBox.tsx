import { ArrowUp, ClipboardList, Plus } from 'lucide-react'
import type { Session } from '../api/client'
import { useRequirements } from '../api/queries'
import { t } from '../i18n'
import { cx } from '../lib/util'
import { Button, Textarea, Tip } from '../ui/base'
import { AttachZone, useUploading, type FileRef } from '../ui/attach'
import { Menu } from '../ui/overlay'
import { displayState } from '../ui/marks'
import { ReqGlyph } from '../ui/refmark'
import { useActions, useBusy } from './actions'
import { useAssign } from './assign'
import { offlineOf } from './compact'
import { DraftField, DraftHint, useDraft } from './draft'

// canMessage：会话面板上能不能直接给它发一句话——没结束、没被接任卸任的会话都能发；
// 送达方式同留言：挂着 ks wait 时立刻叫醒，在跑时排到下一轮，已交接下线的排队等新会话接任（R-273）。
export const canMessage = (s: Session) => s.state !== 'ended' && !s.retired_at

// sendHint：输入框里写的「发出后会怎样」，按会话现在的状态（只认记录：待命 / 在跑 / 已下线）。
export function sendHint(s: Session): string {
  if (!canMessage(s)) return t.turns.phEnded
  if (offlineOf(s)) return t.turns.phOffline
  const ds = displayState(s)
  if (ds === 'parked') return t.turns.phParked
  if (ds === 'running' || ds === 'background' || ds === 'waiting_approval' || ds === 'waiting_answer') return t.turns.phBusy
  return t.turns.phIdle
}

const rank: Record<string, number> = { P0: 0, P1: 1, P2: 2, P3: 3 }

// GiveButton：输入框左侧「交需求」——交一条需求给它开工，或新建一条交给它。
function GiveButton({ s, onNew }: { s: Session; onNew: () => void }) {
  const reqs = useRequirements()
  const assign = useAssign()
  // 能交给它开工的需求：没做完、没锁在计划后面阶段、不是它已经在做的；同项目、高优先级排前面
  const givable = (reqs.data ?? [])
    .filter((r) => ['draft', 'ready', 'in_progress', 'blocked'].includes(r.status) && r.stage_status !== 'locked' && !(r.status === 'in_progress' && r.sessions.includes(s.key)) && r.offered_to !== s.key)
    .sort((a, b) => Number(b.project_id === s.project_id) - Number(a.project_id === s.project_id) || (rank[a.priority] ?? 9) - (rank[b.priority] ?? 9) || a.code.localeCompare(b.code, undefined, { numeric: true }))
  return (
    <Menu
      align="start"
      width="min-w-[300px]"
      trigger={
        <span className="inline-flex">
          <Tip content={t.turns.giveTip}>
            <Button size="icon" variant="ghost" aria-label={t.turns.give} className="shrink-0 text-fg-2">
              <ClipboardList className="size-[18px]" />
            </Button>
          </Tip>
        </span>
      }
      items={[
        { label: t.turns.giveNew, icon: <Plus className="size-3.5" />, onSelect: onNew },
        ...(givable.length ? [{ type: 'separator' as const }, { type: 'label' as const, label: t.session.givePick }] : []),
        ...givable.slice(0, 30).map((r) => ({
          label: `${r.code} ${r.title}`,
          icon: <ReqGlyph status={r.status} />,
          hint: [t.reqStatus[r.status], r.priority, r.project_id !== s.project_id && r.project_name].filter(Boolean).join(t.common.sep),
          onSelect: () => void assign(r.code, s),
        })),
      ]}
    />
  )
}

// MessageBox：会话面板底部固定的输入框（R-273）。和在终端里给它发话一样，发出的话在时间线上标「网页」并显示送达状态。
// 只是留言，不算待办、不进「要你处理」（R-239）。草稿存本机（设计原则第 9 条）。
export function MessageBox({ s, who, onNew, onSent }: { s: Session; who: string; onNew: () => void; onSent?: () => void }) {
  const { replySession } = useActions()
  const [busy, wrap] = useBusy()
  const uploading = useUploading()
  // 草稿（R-202）：reply:会话键
  const dr = useDraft<{ text: string; files: FileRef[] }>(`reply:${s.key}`, { text: '', files: [] })
  const { text, files } = dr.value
  const ok = canMessage(s)
  const dirty = dr.dirty && ok
  const send = () =>
    wrap(async () => {
      if (!dirty || uploading) return
      if (await replySession(s.key, text, files.map((f) => f.id))) {
        dr.drop()
        onSent?.()
      }
    })
  const coarse = typeof window !== 'undefined' && window.matchMedia?.('(pointer: coarse)').matches
  return (
    <div className="border-t border-line bg-surface px-3 pt-2.5 pb-[max(0.75rem,env(safe-area-inset-bottom))] sm:px-4">
      <div className="flex items-end gap-1.5">
        {ok && <GiveButton s={s} onNew={onNew} />}
        <div
          className="min-w-0 flex-1"
          onKeyDown={(e) => {
            if (e.key !== 'Enter' || e.nativeEvent.isComposing) return
            // 桌面上 Enter 发送、Shift+Enter 换行；触屏上 Enter 换行，用发送按钮；⌘ / Ctrl+Enter 都发送
            if (e.metaKey || e.ctrlKey || (!coarse && !e.shiftKey)) {
              e.preventDefault()
              void send()
            }
          }}
        >
          <AttachZone value={files} onChange={(v) => dr.set({ files: v })} compact>
            <DraftField h={dr}>{(pad) => <Textarea minRows={1} value={text} disabled={!ok} placeholder={t.turns.to(who) + sendHint(s)} onChange={(e) => dr.set({ text: e.target.value })} className={cx('max-h-48', pad)} />}</DraftField>
          </AttachZone>
        </div>
        <Button size="icon" variant="primary" aria-label={t.turns.send} disabled={!dirty || uploading} loading={busy} icon={<ArrowUp className="size-4" />} onClick={() => void send()} className="shrink-0" />
      </div>
      <div className={cx('mt-1 flex items-center gap-2 text-[11.5px] text-fg-3', !ok && 'hidden')}>
        <span className="min-w-0 flex-1">
          <DraftHint h={dr} className="text-[11.5px]" />
        </span>
        <span className="ml-auto hidden shrink-0 items-center gap-1 pointer-fine:flex">
          <kbd className="rounded border border-line px-1 font-mono text-[10.5px]">Enter</kbd>
          {t.turns.keys}
          <kbd className="ml-1.5 rounded border border-line px-1 font-mono text-[10.5px]">⇧Enter</kbd>
          {t.turns.newline}
        </span>
      </div>
    </div>
  )
}
