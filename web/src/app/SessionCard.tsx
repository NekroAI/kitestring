import {
  Bot,
  CheckCircle2,
  ChevronDown,
  Hourglass,
  FileCode2,
  FileText,
  Globe,
  HelpCircle,
  ListChecks,
  Plug,
  Search,
  ShieldAlert,
  Terminal,
  Wrench,
  type LucideIcon,
} from 'lucide-react'
import type { Session } from '../api/client'
import { t } from '../i18n'
import { duration, shortPath, useNow } from '../lib/util'

const shortClock = (iso: string) => new Date(iso).toLocaleTimeString(undefined, { hour: '2-digit', minute: '2-digit', hour12: false })
import { Tip } from '../ui/base'
import { Menu } from '../ui/overlay'
import { ctxLoad, ctxTip } from './assign'
import { displayState } from '../ui/marks'
import { useOpenReq } from './nav'
import { Linkify, RefChip, ReqGlyph } from '../ui/refmark'

// 工具名 → 图标。
export function toolIcon(label: string): LucideIcon {
  const name = label.split(' · ')[0]
  if (/^(Bash|shell|exec_command|local_shell)$/.test(name)) return Terminal
  if (/^(Edit|Write|MultiEdit|NotebookEdit|apply_patch)$/.test(name)) return FileCode2
  if (name === 'Read') return FileText
  if (/^(Grep|Glob)$/.test(name)) return Search
  if (/^Web/.test(name)) return Globe
  if (/^(Task|Agent)$/.test(name)) return Bot
  if (name === 'AskUserQuestion') return HelpCircle
  if (name === 'ExitPlanMode') return ListChecks
  if (name.includes('__')) return Plug
  return Wrench
}

// 拆分“工具 · 简述”。
export function splitTool(label: string): [string, string] {
  const [name, ...rest] = label.split(' · ')
  return [name.replace(/^mcp__/, ''), rest.join(' · ')]
}

// sessionExtras：不常看的会话细节（轮数、工具、子任务、权限、worktree、上下文、工具过时），放在设备的悬停里（R-147）。
export function sessionExtras(s: Session): string {
  const ctx = s.state !== 'ended' ? ctxLoad(s) : null
  return [
    t.session.device + '：' + s.device_name,
    s.turns > 0 && t.session.turns(s.turns),
    s.turn_tools > 0 && s.state !== 'ended' && t.session.toolsTip(s.turn_tools),
    s.subagents > 0 && t.session.subagentsTip(s.subagents),
    t.permMode[s.permission_mode],
    s.worktree && t.session.worktree,
    ctx && ctxTip(ctx),
    s.tools_stale && s.state !== 'ended' && t.session.toolsStaleTip(s.tools_stale),
  ]
    .filter(Boolean)
    .join(t.common.sep)
}

export function sessionTitle(s: Session): string {
  return s.title || s.project_name || shortPath(s.cwd, 28) || t.session.noProject
}

// 等你处理时的提示块：在问你 / 请你确认计划 / 请求执行 / 回复要点。
export function Callout({ s }: { s: Session }) {
  const ds = displayState(s)
  const now = useNow()
  if (ds === 'waiting_approval') {
    return (
      <div className="flex items-start gap-2.5 rounded-xl bg-wait-soft px-3 py-2.5">
        <ShieldAlert className="mt-0.5 size-4 shrink-0 text-wait" />
        <div className="min-w-0 flex-1">
          <div className="font-mono text-[12px] leading-relaxed break-all text-fg">{s.state_detail}</div>
          <div className="mt-0.5 text-xs text-fg-3">{t.session.approvalHint}</div>
        </div>
      </div>
    )
  }
  if (ds === 'parked') {
    return (
      <div className="flex items-start gap-2.5 rounded-xl bg-accent-soft px-3 py-2.5">
        <Hourglass className="mt-0.5 size-4 shrink-0 animate-pulse text-accent" />
        <div className="min-w-0 flex-1 text-sm">
          <div className="flex flex-wrap items-baseline gap-x-2 text-xs">
            <span className="font-medium text-accent">{t.session.parked}</span>
            {/* 已等多久、最长等到何时（R-74）：Codex 在前台等，最多 30 分钟 */}
            <span className="text-fg-3 tabular">
              {[t.session.parkedFor(duration(s.state_since, now)), s.parked_until && t.session.parkedUntil(shortClock(s.parked_until), duration(new Date(now).toISOString(), Date.parse(s.parked_until)))].filter(Boolean).join(t.common.sep)}
            </span>
          </div>
          <div className="mt-0.5 leading-relaxed text-fg">{s.tool === 'codex' ? t.session.parkedHintCodex : t.session.parkedHint}</div>
        </div>
      </div>
    )
  }
  if (ds === 'waiting_answer') {
    const [name, brief] = splitTool(s.state_detail)
    const plan = name === 'ExitPlanMode'
    const Icon = plan ? ListChecks : HelpCircle
    return (
      <div className="flex items-start gap-2.5 rounded-xl bg-reply-soft px-3 py-2.5">
        <Icon className="mt-0.5 size-4 shrink-0 text-reply" />
        <div className="min-w-0 flex-1 text-sm">
          <div className="text-xs font-medium text-reply">{plan ? t.session.planConfirm : t.session.asking}</div>
          {brief && <div className="mt-0.5 leading-relaxed text-fg">{brief}</div>}
          <div className="mt-0.5 text-xs text-fg-3">{t.session.approvalHint}</div>
        </div>
      </div>
    )
  }
  if (ds === 'background') {
    return (
      <Tip content={t.session.background}>
        <div className="flex items-center gap-2.5 rounded-xl bg-accent-soft px-3 py-2">
          <Hourglass className="size-4 shrink-0 animate-pulse text-accent" />
          <span className="min-w-0 flex-1 truncate text-[13px] text-fg">{s.state_detail}</span>
        </div>
      </Tip>
    )
  }
  if (ds === 'waiting_reply' && (s.digest_done || s.digest_ask)) {
    return (
      <div className="space-y-1.5 rounded-xl bg-reply-soft px-3 py-2.5 text-sm">
        {s.digest_done && (
          <div className="flex items-start gap-2">
            <CheckCircle2 className="mt-0.5 size-4 shrink-0 text-run" />
            <span className="leading-relaxed text-fg">
              <Linkify text={s.digest_done} />
            </span>
          </div>
        )}
        {s.digest_ask && (
          <div className="flex items-start gap-2">
            <HelpCircle className="mt-0.5 size-4 shrink-0 text-reply" />
            <span className="leading-relaxed font-medium text-fg">
              <Linkify text={s.digest_ask} />
            </span>
          </div>
        )}
      </div>
    )
  }
  return null
}

export function ReqCount({ s }: { s: Session }) {
  const openReq = useOpenReq()
  const open = s.requirements.filter((r) => r.status !== 'done' && r.status !== 'shelved')
  if (!open.length) return null
  const shown = open.length > 2 ? open.slice(0, 1) : open
  const rest = open.slice(shown.length)
  return (
    <span className="inline-flex min-w-0 items-center gap-1" onPointerDown={(e) => e.stopPropagation()}>
      {shown.map((r) => (
        <RefChip key={r.id} code={r.code} status={r.status} title={r.title} onOpen={openReq} />
      ))}
      {rest.length > 0 && (
        <Menu
          align="start"
          width="w-72"
          trigger={
            <button onClick={(e) => e.stopPropagation()} className="inline-flex items-center gap-0.5 rounded-md border border-line bg-surface-2 px-1.5 text-[11px] leading-[18px] font-semibold text-fg-2 transition hover:border-accent pointer-coarse:py-0.5">
              {t.ref.more(rest.length)}
              <ChevronDown className="size-3 opacity-70" />
            </button>
          }
          items={rest.map((r) => ({ label: r.title, hint: r.code, icon: <ReqGlyph status={r.status} />, onSelect: () => openReq(r.code) }))}
        />
      )}
    </span>
  )
}
