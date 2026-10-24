import { useMemo } from 'react'
import type { Requirement } from '../api/client'
import { useDecisions, usePlans, useRequirements } from '../api/queries'
import { t } from '../i18n'

// 最近动态：跨会话汇总交证据、退回、留言、派活、完成与决策答复。项目页与工作台右栏共用。
export type RecentItem = { at: string; code: string; text: string; dot: string }

export function useRecent(projectId?: string, limit = 8, reqs?: Requirement[]): RecentItem[] {
  const all = useRequirements()
  const decisions = useDecisions()
  const plans = usePlans()
  const list = reqs ?? all.data
  return useMemo(() => {
    const out: RecentItem[] = []
    for (const r of list ?? []) {
      if (projectId && r.project_id !== projectId) continue
      for (const e of r.evidence ?? []) if (!e.voided) out.push({ at: e.created_at, code: r.code, text: t.projects.evEvidence(r.code), dot: 'bg-run' })
      for (const f of r.feedback ?? []) {
        const at = f.answered_at ?? f.created_at
        if (f.kind === 'assign') out.push({ at, code: r.code, text: t.projects.evAssign(r.code), dot: 'bg-accent' })
        else if (f.kind === 'note') out.push({ at, code: r.code, text: t.projects.evNote(r.code, f.answer ?? ''), dot: 'bg-accent' })
        else out.push({ at, code: r.code, text: t.projects.evRevise(r.code, f.answer ?? ''), dot: 'bg-reply' })
      }
      if (r.status === 'done') out.push({ at: r.updated_at, code: r.code, text: t.projects.evDone(r.code), dot: 'bg-run' })
    }
    for (const d of decisions.data ?? [])
      if ((!projectId || d.project_id === projectId) && d.kind === 'decision' && d.answered_at)
        out.push({ at: d.answered_at, code: d.requirement_code ?? '', text: d.timed_out ? t.projects.evTimeout(d.code) : d.proxy_by && !d.overridden_at ? t.projects.evProxy(d.code) : t.projects.evAnswer(d.code, answerVerb(d)), dot: 'bg-wait' })
    // 阶段验收通过也记一条（R-161：判断完成后卡片收起、时间线记一条）
    for (const x of plans.data ?? [])
      if (!projectId || x.project_id === projectId)
        for (const st of x.stages) if (st.passed_at) out.push({ at: st.passed_at, code: x.code, text: t.projects.evStage(x.code, st.seq, st.title), dot: 'bg-run' })
    return out.sort((a, b) => b.at.localeCompare(a.at)).slice(0, limit)
  }, [list, projectId, limit, decisions.data, plans.data])
}

// answerVerb：最近动态里你对决策做了什么（R-176）——批准类决策写「批准 / 拒绝」，选项类写「回答」。
function answerVerb(d: { approval?: boolean; options?: string[] | null; answer?: string }) {
  if (!d.approval) return t.verdict.pass('answer')
  const opt0 = d.options?.[0]
  return opt0 && d.answer?.startsWith(opt0) ? t.verdict.pass('approval') : t.verdict.back('approval')
}
