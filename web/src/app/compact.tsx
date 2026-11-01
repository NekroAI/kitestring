import { ArrowRightLeft, Copy, TimerOff } from 'lucide-react'
import { toast } from 'sonner'
import { api, errMsg, unwrap, type Session } from '../api/client'
import { t } from '../i18n'
import { copyText, cx, relTime, wan } from '../lib/util'
import { Button, Tip } from '../ui/base'
import { ctxColor, ctxLoad, ctxTip, CtxRing } from './assign'
import { useQueryClient } from '@tanstack/react-query'

// 上下文过长时以压缩为主（R-224）：超过设置里的阈值标「建议压缩」，点一下复制按身份写好的 /compact 命令。
// /compact 是终端里的用户命令，纸鸢不替你执行，只提示和给出命令。

// compactCommand：按身份生成的压缩命令（Claude Code 的 /compact 可带「保留什么」；Codex 的不带）。
export function compactCommand(s: Session): string {
  if (s.tool === 'codex') return '/compact'
  return t.compactCmd(s.role, s.project_name || t.session.noProject)
}

async function copyCompact(s: Session) {
  if (await copyText(compactCommand(s))) toast.success(t.session.compactCopied)
}

function CompactTip({ s }: { s: Session }) {
  const c = ctxLoad(s)!
  return (
    <div className="space-y-1">
      <div>{ctxTip(c)}</div>
      {c.level === 'high' && <div className="opacity-80">{s.tool === 'codex' ? t.session.compactCopyTipCodex : t.session.compactCopyTip}</div>}
    </div>
  )
}

// CtxBadge：工作台泳道标题旁常显的上下文圆环（R-225），不带「上下文」字样，悬停看用量和比例。
// 超过阈值时圆环变红、后面跟「建议压缩」，点它复制命令；标题行放不下时只留红圆环（点圆环同样复制）。已结束的会话不显示。
export function CtxBadge({ s }: { s: Session }) {
  const c = s.state !== 'ended' ? ctxLoad(s) : null
  if (!c) return null
  if (c.level !== 'high') {
    return (
      <Tip content={<CompactTip s={s} />}>
        <span className={cx('inline-flex shrink-0', ctxColor(c))} aria-label={ctxTip(c)}>
          <CtxRing c={c} />
        </span>
      </Tip>
    )
  }
  return (
    <Tip content={<CompactTip s={s} />}>
      <button
        type="button"
        onClick={(e) => {
          e.stopPropagation()
          void copyCompact(s)
        }}
        onKeyDown={(e) => e.stopPropagation()}
        className="-mx-1 inline-flex shrink-0 items-center gap-1 rounded-full px-1 py-px text-danger transition hover:bg-danger-soft"
        aria-label={t.session.compactLine(wan(c.used))}
      >
        <CtxRing c={c} />
        <span className="text-[11.5px] leading-4 font-medium whitespace-nowrap @max-[15.5rem]:hidden">{t.session.compact}</span>
      </button>
    </Tip>
  )
}

// CompactRow：会话面板顶部的一行「上下文 48 万 · 建议压缩」+ 复制命令（只在超过阈值时出现）。
export function CompactRow({ s }: { s: Session }) {
  const c = s.state !== 'ended' ? ctxLoad(s) : null
  if (!c || c.level !== 'high') return null
  const cmd = compactCommand(s)
  return (
    <div className="flex items-center gap-2.5 rounded-xl bg-danger-soft px-3 py-2">
      <CtxRing c={c} className="text-danger" />
      <div className="min-w-0 flex-1">
        <div className="text-[13px] font-medium text-danger">{t.session.compactLine(wan(c.used))}</div>
        <Tip truncated content={cmd}>
          <code className="block truncate font-mono text-[11.5px] text-fg-2">{cmd}</code>
        </Tip>
      </div>
      <Button size="sm" variant="ghost" onClick={() => void copyCompact(s)}>
        <Copy className="size-3.5" />
        {t.session.compactCopy}
      </Button>
    </div>
  )
}

// ---------- 待命超时（R-229） ----------

// 开发会话待命超过项目设置的时长、手上没有排给它的需求：Hub 记下 idle_at（明确记录，不在网页上猜）。
const idleOf = (s: Session) => (s.idle_at && s.role === 'dev' && s.state !== 'ended' ? s.idle_at : '')

// IdleTag：泳道状态标后面的「待命超时 · 建议收尾」；窄处只留沙漏。
export function IdleTag({ s }: { s: Session }) {
  if (!idleOf(s)) return null
  return (
    <Tip content={t.session.idleTip}>
      <span className="inline-flex shrink-0 items-center gap-1 text-[11.5px] leading-4 font-medium whitespace-nowrap text-wait" aria-label={t.session.idle}>
        <TimerOff className="size-3.5" />
        <span className="@max-[12rem]:hidden">{t.session.idle}</span>
      </span>
    </Tip>
  )
}

// IdleRow：会话面板顶部一行「待命超时、没有排给它的需求 · 建议收尾」，右侧是标记时间。
export function IdleRow({ s, now }: { s: Session; now: number }) {
  if (!idleOf(s)) return null
  return (
    <div className="flex items-start gap-2.5 rounded-xl bg-wait-soft px-3 py-2">
      <TimerOff className="mt-0.5 size-4 shrink-0 text-wait" />
      <div className="min-w-0 flex-1">
        <div className="flex items-baseline gap-2">
          <span className="min-w-0 flex-1 text-[13px] font-medium text-wait">{t.session.idleLine}</span>
          <span className="shrink-0 text-[11.5px] text-fg-3 tabular">{t.session.idleSince(relTime(s.idle_at, now))}</span>
        </div>
        <div className="text-[12px] leading-relaxed text-fg-2">{t.session.idleTip}</div>
      </div>
    </div>
  )
}

// ---------- 交接 ----------

type HandoffOut = { command: string; waiting: number }

// useRequestHandoff：请会话交接——纸鸢通知它写 handoff_write，返回新会话接任的命令（复制到剪贴板）。
export function useRequestHandoff() {
  const qc = useQueryClient()
  return async (s: Session) => {
    try {
      const out = (await unwrap(api.POST('/api/sessions/{key}/handoff', { params: { path: { key: s.key } } }))) as HandoffOut
      void qc.invalidateQueries({ queryKey: ['sessions'] })
      const copied = await copyText(out.command)
      toast.success(out.waiting ? t.handoff.askedWait(out.waiting) : t.handoff.asked, { description: copied ? t.handoff.copiedCmd(out.command) : t.handoff.runCmd(out.command) })
      return out
    } catch (e) {
      toast.error(errMsg(e, t.fail.compact))
      return undefined
    }
  }
}

export const canHandoff = (s: Session) => !!s.role && !!s.project_id && s.state !== 'ended' && !s.retired_at

export const HandoffIcon = ArrowRightLeft

export type Handoff = {
  id: string
  role: string
  from_session: string
  from_title: string
  status: 'requested' | 'written' | 'taken'
  doing: string
  agreements?: string
  caveats?: string
  waiting: string
  rules: string
  next: string
  notes: string
  requested_at?: string
  written_at?: string
  taken_by?: string
  taken_title?: string
  taken_at?: string
}

// HandoffCard：会话面板里的交接进度与内容（R-224）：等它写 → 已写好（复制接任命令）→ 已由谁接任；
// 内容按三栏列出（R-245：做到一半的事、口头约定、注意事项），老交接的旧栏目照样显示。
export function HandoffCard({ h, me }: { h: Handoff; me: string }) {
  const cmd = `ks takeover ${h.role}`
  const mine = h.from_session === me
  const head =
    h.status === 'requested' ? t.handoff.requested : h.status === 'written' ? t.handoff.written : mine ? t.handoff.taken(h.taken_title || h.taken_by || '') : t.handoff.takenFrom(h.from_title || h.from_session)
  const parts = (['doing', 'agreements', 'caveats', 'notes', 'waiting', 'rules', 'next'] as const).filter((k) => (h[k] ?? '').trim())
  return (
    <div className={cx('space-y-2.5 rounded-2xl border p-3.5', h.status === 'taken' ? 'border-line' : 'border-accent/40 bg-accent-soft/40')}>
      <div className="flex items-center gap-2">
        <HandoffIcon className={cx('size-4 shrink-0', h.status === 'taken' ? 'text-fg-3' : 'text-accent')} />
        <span className="min-w-0 flex-1 text-[13px] font-medium text-fg">{head}</span>
        {h.status === 'written' && mine && (
          <Button size="sm" variant="ghost" onClick={async () => (await copyText(cmd)) && toast.success(t.common.copied)}>
            <Copy className="size-3.5" />
            {t.handoff.copyCmd}
          </Button>
        )}
      </div>
      {h.status === 'written' && mine && <code className="block truncate rounded-lg bg-sunken px-2.5 py-1.5 font-mono text-[12px] text-fg">{cmd}</code>}
      {parts.length > 0 && (
        <dl className="space-y-1.5 text-[13px] leading-relaxed">
          {parts.map((k) => (
            <div key={k}>
              <dt className="text-xs text-fg-3">{t.handoff.sections[k]}</dt>
              <dd className="break-words whitespace-pre-wrap text-fg-2">{h[k]}</dd>
            </div>
          ))}
        </dl>
      )}
    </div>
  )
}
