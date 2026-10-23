import { Palette } from 'lucide-react'
import type { Requirement } from '../api/client'
import { useDecisions } from '../api/queries'
import { t } from '../i18n'
import { cx } from '../lib/util'

// 设计类需求（D-21 ③）：只是标注，Agent 按需要交方案图（R-75）。徽章：设计 → 待你选方案 → 实现中。
// 阶段由已有数据推出：有没有带效果图的决策、它答了没有，不另设状态。

export type DesignPhase = 'propose' | 'choose' | 'build'

export function useDesignPhase(r: Requirement): DesignPhase | null {
  const decisions = useDecisions()
  if (r.kind !== 'design' || r.status === 'review' || r.status === 'done' || r.status === 'shelved') return null
  const mine = (decisions.data ?? []).filter((d) => d.requirement_id === r.id && (d.option_files ?? []).some((f) => f?.length))
  if (mine.some((d) => d.status === 'pending')) return 'choose'
  if (mine.length) return 'build'
  return 'propose'
}

const tone: Record<DesignPhase, string> = {
  propose: 'bg-accent-soft text-accent',
  choose: 'bg-reply-soft text-reply',
  build: 'bg-run-soft text-run',
}

// DesignBadge：“设计 · 待你选方案”。不是设计需求时不显示。
export function DesignBadge({ r, className }: { r: Requirement; className?: string }) {
  const phase = useDesignPhase(r)
  if (r.kind !== 'design') return null
  return (
    <span className={cx('inline-flex shrink-0 items-center gap-1 rounded-md px-1.5 py-0.5 text-xs font-medium', phase ? tone[phase] : 'bg-sunken text-fg-2', className)}>
      <Palette className="size-3.5" />
      {phase ? t.design.phase[phase] : t.design.label}
    </span>
  )
}
