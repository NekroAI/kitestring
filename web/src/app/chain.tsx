import type { ReactNode } from 'react'
import { useQuery } from '@tanstack/react-query'
import { GitFork } from 'lucide-react'
import { api, unwrap, type Schemas } from '../api/client'
import { t } from '../i18n'
import { RefChip } from '../ui/refmark'
import { CommitLine } from './commits'

// 决策后果链（R-154）：这条决策接着 / 推翻了谁、被谁取代 / 推翻、产生了哪些需求与提交。
// 全部从已有记录读出；什么都没有（例如旧决策）时不占位。

type Chain = Schemas['DecisionChain']

export function useDecisionChain(code: string, enabled = true) {
  return useQuery({
    queryKey: ['decisions', 'chain', code],
    queryFn: () => unwrap(api.GET('/api/decisions/{ref}/chain', { params: { path: { ref: code } } })),
    enabled,
    staleTime: 30_000,
  })
}

function Line({ label, children }: { label: string; children: ReactNode }) {
  return (
    <div className="flex flex-wrap items-center gap-x-1.5 gap-y-1">
      <span className="text-fg-3">{label}</span>
      {children}
    </div>
  )
}

function Dec({ d }: { d: { code: string; title: string; status: string; reason?: string } }) {
  return (
    <span className="inline-flex min-w-0 max-w-full flex-wrap items-center gap-x-1.5">
      <RefChip code={d.code} status={d.status} />
      <span className="min-w-0 truncate text-fg-2">{d.title}</span>
      {d.reason && <span className="min-w-0 basis-full text-xs break-words text-fg-3">{t.decision.reasonLabel}：{d.reason}</span>}
    </span>
  )
}

function empty(c: Chain) {
  return !c.from && !c.replaced_by && !(c.followups ?? []).length && !(c.replaces ?? []).length && !(c.requirements ?? []).length && !(c.edits ?? []).length
}

// skipReq：嵌在某条需求的时间线里时，不再重复列出「针对」这条需求本身
export function DecisionChain({ code, skipReq }: { code: string; skipReq?: string }) {
  const q = useDecisionChain(code)
  const data = q.data
  const c = data && { ...data, requirements: (data.requirements ?? []).filter((r) => !(r.relation === 'about' && r.code === skipReq)) }
  if (!c || empty(c)) return null
  const followups = c.followups ?? []
  const overturned = followups.filter((x) => x.overturns)
  const later = followups.filter((x) => !x.overturns)
  return (
    <div className="space-y-1.5 rounded-xl border border-line bg-surface-2 px-3 py-2.5 text-sm">
      <div className="flex items-center gap-1.5 text-xs font-medium text-fg-3">
        <GitFork className="size-3.5" />
        {t.chain.title}
      </div>
      {c.from && (
        <Line label={c.from.overturns ? t.chain.overturnsFrom : t.chain.follows}>
          <Dec d={c.from} />
        </Line>
      )}
      {c.replaced_by && (
        <Line label={t.chain.replacedBy}>
          <Dec d={c.replaced_by} />
        </Line>
      )}
      {(c.replaces ?? []).map((x) => (
        <Line key={'r' + x.code} label={t.chain.replaces}>
          <Dec d={x} />
        </Line>
      ))}
      {overturned.map((x) => (
        <Line key={'o' + x.code} label={t.chain.overturnedBy}>
          <Dec d={x} />
        </Line>
      ))}
      {later.map((x) => (
        <Line key={'f' + x.code} label={t.chain.followedBy}>
          <Dec d={x} />
        </Line>
      ))}
      {/* 因此改了需求 / 放行（R-241）：按这条决策改了哪些字段、放行了哪条；嵌在这条需求自己的时间线里时不再重复写需求编号 */}
      {(c.edits ?? []).map((x) => (
        <Line key={'e' + x.code} label={x.kind === 'release' ? t.chain.released : t.chain.edited}>
          {x.kind !== 'release' && !!x.fields?.length && <span className="text-fg">{x.fields.join('、')}</span>}
          {x.req_code !== skipReq ? <RefChip code={x.req_code} title={x.req_title} label="title" className="max-w-[18em]" /> : x.kind === 'release' && <span className="text-fg">{t.chain.releasedTo}</span>}
        </Line>
      ))}
      {(c.requirements ?? []).map((r) => (
        <div key={r.code} className="space-y-1">
          <Line label={t.chain.relation[r.relation] ?? r.relation}>
            <RefChip code={r.code} status={r.status} />
            <span className="min-w-0 truncate text-fg-2">{r.title}</span>
          </Line>
          {(r.commits ?? []).map((m, i) => (
            <CommitLine key={i} c={m} all={r.commits ?? []} className="pl-3" />
          ))}
        </div>
      ))}
    </div>
  )
}
