import { Send } from 'lucide-react'
import { toast } from 'sonner'
import type { Session } from '../api/client'
import { useSessions } from '../api/queries'
import { t } from '../i18n'
import { StatusDot, displayState } from '../ui/marks'
import { Menu } from '../ui/overlay'
import { Button } from '../ui/base'
import type { CtxItem } from '../ui/contextMenu'
import { useActions } from './actions'
import { sessionTitle } from './SessionCard'

// 把需求交给某个会话开工（D-14）：候选是还没结束的会话——同项目优先，其中待命的最前，然后按最近活跃。

const rank: Record<string, number> = { parked: 0, waiting_reply: 1, done: 1, idle: 2, running: 3, background: 3, waiting_answer: 4, waiting_approval: 4, lost: 9 }

export function assignCandidates(sessions: Session[], projectId?: string | null): Session[] {
  return sessions
    .filter((s) => s.state !== 'ended' && s.state !== 'lost')
    .sort(
      (a, b) =>
        Number((b.project_id ?? '') === (projectId ?? '')) - Number((a.project_id ?? '') === (projectId ?? '')) ||
        (rank[displayState(a)] ?? 5) - (rank[displayState(b)] ?? 5) ||
        b.last_active_at.localeCompare(a.last_active_at),
    )
}

// defaultTarget：新建需求时默认交给谁——同项目里待命或一小时内活跃过的会话，没有就不交。
export function defaultTarget(sessions: Session[], projectId?: string | null): Session | undefined {
  const c = assignCandidates(sessions, projectId)[0]
  if (!c || (c.project_id ?? '') !== (projectId ?? '')) return undefined
  return displayState(c) === 'parked' || Date.now() - Date.parse(c.last_active_at) < 3600_000 ? c : undefined
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
  const list = assignCandidates(sessions, projectId)
  if (!list.length) return [{ label: t.assign.none, onSelect: () => {}, disabled: true }]
  return list.slice(0, 12).map((s) => ({ label: sessionTitle(s), hint: [s.project_name, s.device_name, t.state[displayState(s)]].filter(Boolean).join(t.common.sep), icon: <StatusDot state={displayState(s)} />, onSelect: () => onPick(s) }))
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
      items={[
        { type: 'label', label: t.assign.title },
        ...list.slice(0, 12).map((s) => ({ label: sessionTitle(s), hint: [s.project_name, s.device_name, t.state[displayState(s)]].filter(Boolean).join(t.common.sep), icon: <StatusDot state={displayState(s)} />, onSelect: () => void pick(code, s) })),
      ]}
    />
  )
}
