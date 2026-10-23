import { useMemo } from 'react'
import type { Requirement } from '../api/client'
import { useDecisions, useRequirements } from '../api/queries'
import { t } from '../i18n'

// 最近动态：跨会话汇总交证据、退回、留言、派活、完成与决策答复。项目页与工作台右栏共用。
export type RecentItem = { at: string; code: string; text: string; dot: string }

export function useRecent(projectId?: string, limit = 8, reqs?: Requirement[]): RecentItem[] {
  const all = useRequirements()
  const decisions = useDecisions()
  const list = reqs ?? all.data
  return useMemo(() => {
    const out: RecentItem[] = []
    for (const r of list ?? []) {
      if (projectId && r.project_id !== projectId) continue
      for (const e of r.evidence ?? []) if (!e.voided) out.push({ at: e.created_at, code: r.code, text: t.projects.evEvidence(r.code, r.title), dot: 'bg-run' })
      for (const f of r.feedback ?? []) {
        const at = f.answered_at ?? f.created_at
        if (f.kind === 'assign') out.push({ at, code: r.code, text: t.projects.evAssign(r.code, r.title), dot: 'bg-accent' })
        else if (f.kind === 'note') out.push({ at, code: r.code, text: t.projects.evNote(r.code, f.answer ?? ''), dot: 'bg-accent' })
        else out.push({ at, code: r.code, text: t.projects.evRevise(r.code, f.answer ?? ''), dot: 'bg-reply' })
      }
      if (r.status === 'done') out.push({ at: r.updated_at, code: r.code, text: t.projects.evDone(r.code, r.title), dot: 'bg-run' })
    }
    for (const d of decisions.data ?? [])
      if ((!projectId || d.project_id === projectId) && d.kind === 'decision' && d.answered_at)
        out.push({ at: d.answered_at, code: d.requirement_code ?? '', text: (d.proxy_by && !d.overridden_at ? t.projects.evProxy : t.projects.evAnswer)(d.code, d.short_title || d.question), dot: 'bg-wait' })
    return out.sort((a, b) => b.at.localeCompare(a.at)).slice(0, limit)
  }, [list, projectId, limit, decisions.data])
}
