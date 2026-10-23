import { Copy, ScanSearch, Zap } from 'lucide-react'
import { toast } from 'sonner'
import { useSessions } from '../api/queries'
import { t } from '../i18n'
import { copyText } from '../lib/util'
import { Button } from '../ui/base'
import { StatusDot, displayState } from '../ui/marks'
import { Menu, type MenuItem } from '../ui/overlay'
import { useActions } from './actions'
import { assignCandidates, CtxMark, targetGroup } from './assign'
import { sessionTitle } from './SessionCard'

// ReviewerButton：需求抽屉底部的“交给审查员”（R-88）。只列本项目身份为审查员的会话；
// 交给它只送达一条审查任务，不改需求状态、不关联会话，验收照常。没有审查员时给出开审查员会话的那句话。
export function ReviewerButton({ code, projectId, projectName }: { code: string; projectId?: string | null; projectName?: string | null }) {
  const sessions = useSessions()
  const { askReview } = useActions()
  const list = assignCandidates(sessions.data?.sessions ?? [], projectId).filter((s) => s.role === 'reviewer' && (s.project_id ?? '') === (projectId ?? ''))
  const phrase = t.reviewer.joinPhrase(projectName || t.reqs.noProject)
  const items: MenuItem[] = list.length
    ? [
        { type: 'label', label: t.reviewer.title },
        ...list.map(
          (s): MenuItem => ({
            label: sessionTitle(s),
            hint: (
              <span className="flex min-w-0 items-center gap-1.5">
                <span className="truncate">{[t.state[displayState(s)], s.device_name].filter(Boolean).join(t.common.sep)}</span>
                <CtxMark s={s} />
              </span>
            ),
            icon: targetGroup(s) === 'now' ? <Zap className="text-accent" /> : <StatusDot state={displayState(s)} />,
            onSelect: async () => {
              if (await askReview(code, s.key)) toast.success(t.reviewer.done(code, sessionTitle(s), targetGroup(s) === 'now'))
            },
          }),
        ),
      ]
    : [
        {
          type: 'label',
          label: (
            <span className="block space-y-1.5 py-0.5 font-normal whitespace-normal">
              <span className="block">{t.reviewer.none}</span>
              <span className="block rounded-md bg-sunken px-2 py-1.5 text-[13px] leading-relaxed text-fg">{phrase}</span>
            </span>
          ),
        },
        { label: t.reviewer.copy, icon: <Copy />, onSelect: async () => void ((await copyText(phrase)) && toast.success(t.common.copied)) },
      ]
  return (
    <Menu
      width="w-80"
      trigger={
        <Button variant="ghost" icon={<ScanSearch className="size-4" />} aria-label={t.reviewer.button} title={t.reviewer.hint} className="max-sm:px-2">
          <span className="max-sm:hidden">{t.reviewer.button}</span>
        </Button>
      }
      items={items}
    />
  )
}
