import { Send, Zap } from 'lucide-react'
import { toast } from 'sonner'
import type { Session } from '../api/client'
import { useSessions } from '../api/queries'
import { t } from '../i18n'
import { StatusDot, displayState } from '../ui/marks'
import { Menu, type MenuItem } from '../ui/overlay'
import { Button } from '../ui/base'
import type { CtxItem } from '../ui/contextMenu'
import { useActions } from './actions'
import { sessionTitle } from './SessionCard'

// 把需求交给某个会话开工（D-14）。选会话时按“能不能马上接手”分三组：
// 待命中的会立刻开工；其它活跃会话在你下次和它对话（或它进入待命）时收到；你收起的会话要你回终端唤醒。

export type TargetGroup = 'now' | 'later' | 'stale'

export function targetGroup(s: Session): TargetGroup {
  const ds = displayState(s)
  // 挂着 ks wait 的会话（哪怕本轮还没结束、仍显示运行中）收到就会立刻响应
  if (ds === 'parked' || (s.parked && ds !== 'done')) return 'now'
  if (ds === 'done') return 'stale'
  return 'later'
}

export function assignCandidates(sessions: Session[], projectId?: string | null): Session[] {
  const order: Record<TargetGroup, number> = { now: 0, later: 1, stale: 2 }
  return sessions
    .filter((s) => s.state !== 'ended' && s.state !== 'lost')
    .sort(
      (a, b) =>
        order[targetGroup(a)] - order[targetGroup(b)] ||
        Number((b.project_id ?? '') === (projectId ?? '')) - Number((a.project_id ?? '') === (projectId ?? '')) ||
        b.last_active_at.localeCompare(a.last_active_at),
    )
}

// defaultTarget：新建需求时默认交给谁——同项目里待命的会话，其次一小时内活跃过的；收起的不算。
export function defaultTarget(sessions: Session[], projectId?: string | null): Session | undefined {
  const same = assignCandidates(sessions, projectId).filter((s) => (s.project_id ?? '') === (projectId ?? '') && targetGroup(s) !== 'stale')
  return same.find((s) => targetGroup(s) === 'now') ?? same.find((s) => Date.now() - Date.parse(s.last_active_at) < 3600_000)
}

// targetItems：按分组生成菜单项（下拉菜单与右键菜单共用）。
export function targetItems(sessions: Session[], projectId: string | null | undefined, onPick: (s: Session) => void, opts: { exclude?: string; checked?: string } = {}): MenuItem[] {
  const list = assignCandidates(sessions, projectId).filter((s) => s.key !== opts.exclude)
  const out: MenuItem[] = []
  for (const g of ['now', 'later', 'stale'] as const) {
    const part = list.filter((s) => targetGroup(s) === g).slice(0, 10)
    if (!part.length) continue
    if (out.length) out.push({ type: 'separator' })
    out.push({ type: 'label', label: t.assign.group[g] })
    for (const s of part)
      out.push({
        label: sessionTitle(s),
        hint: [s.project_name, s.device_name, t.state[displayState(s)]].filter(Boolean).join(t.common.sep),
        icon: g === 'now' ? <Zap className="text-accent" /> : <StatusDot state={displayState(s)} />,
        checked: opts.checked === s.key,
        onSelect: () => onPick(s),
      })
  }
  return out
}

export const targetLabel = (s: Session) => [sessionTitle(s), s.device_name].filter(Boolean).join(t.common.sep)

export function useAssign() {
  const { assign } = useActions()
  return async (code: string, s: Session) => {
    if (await assign(code, s.key)) toast.success(t.assign.done(code, sessionTitle(s)))
  }
}

// assignItems：右键菜单“交给会话”子菜单的条目。
export function assignItems(sessions: Session[], projectId: string | null | undefined, onPick: (s: Session) => void): CtxItem[] {
  const items = targetItems(sessions, projectId, onPick)
  return items.length ? (items as CtxItem[]) : [{ label: t.assign.none, onSelect: () => {}, disabled: true }]
}

// AssignButton：需求抽屉底部的“交给…”。
export function AssignButton({ code, projectId }: { code: string; projectId?: string | null }) {
  const sessions = useSessions()
  const pick = useAssign()
  const list = assignCandidates(sessions.data?.sessions ?? [], projectId)
  return (
    <Menu
      width="w-80"
      trigger={
        <Button variant="secondary" icon={<Send className="size-4" />} disabled={!list.length}>
          {t.assign.button}
        </Button>
      }
      items={targetItems(sessions.data?.sessions ?? [], projectId, (s) => void pick(code, s))}
    />
  )
}
