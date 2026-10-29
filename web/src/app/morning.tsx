import { toast } from 'sonner'
import { titleOf } from '../lib/waiting'
import { Undo2 } from 'lucide-react'
import type { Decision, MorningReport } from '../api/client'
import { useMorning } from '../api/queries'
import { t } from '../i18n'
import { cx } from '../lib/util'
import { Badge, Button } from '../ui/base'
import { RoleTag } from '../ui/marks'
import { Linkify, RefChip, useOpenRef } from '../ui/refmark'
import { useOpenAsk } from './nav'
import { releaseKey, useWithdrawRelease } from './requirements'
import { PendingBar } from './verdict'

// 顶部简报条展开面板里「为你决策」的两种一行（R-165 → R-212）：主助理的代答（可推翻）、放行（可撤回）；
// 简报条本身见 app/brief.tsx。「要你处理」卡上挂的验收建议和没代答的原因也在这里（R-165）。

const rowCls = 'flex min-h-8 min-w-0 items-center gap-2 rounded-lg px-2 text-[13px]'

// ProxyLine：一条代答，一行——问题、主助理答了什么；「推翻」打开审批抽屉写你的答复（R-177：不就地作答）。
export function ProxyLine({ d, onOpen }: { d: Decision; onOpen?: () => void }) {
  const openAsk = useOpenAsk()
  const openRef = useOpenRef()
  const done = !!d.overridden_at
  const rc = d.requirement_code
  return (
    <div
      className={cx(rowCls, rc && 'cursor-pointer hover:bg-hover/60')}
      title={d.proxy_reason ?? ''}
      onClick={
        rc
          ? () => openRef(rc)
          : undefined
      }
    >
      <RefChip code={d.code} status={d.status} link={false} />
      <span className="min-w-0 flex-1 truncate">
        <span className="text-fg">{titleOf(d)}</span>
        <span className="text-fg-3">{t.common.sep}</span>
        <span className={cx(done ? 'text-fg-3 line-through' : 'text-fg-2')}>{d.proxy_answer}</span>
        {d.proxy_risk && <span className="text-fg-3">{t.common.sep + t.grant.risk[d.proxy_risk]}</span>}
      </span>
      {done ? (
        <Badge tone="accent">{t.grant.overridden}</Badge>
      ) : (
        d.status !== 'closed' && (
          <Button
            size="sm"
            variant="ghost"
            className="-my-1 shrink-0"
            onClick={(e) => {
              e.stopPropagation()
              onOpen?.()
              openAsk(`x:${d.code}`)
            }}
          >
            <Undo2 className="size-3.5" />
            {t.grant.override}
          </Button>
        )
      )}
    </div>
  )
}

// ReleaseLine：一条放行，一行——需求、谁放行的、理由（悬停看全）；可撤回。
export function ReleaseLine({ x }: { x: NonNullable<MorningReport['releases']>[number] }) {
  const wr = useWithdrawRelease()
  const openRef = useOpenRef()
  return (
    <div
      className={cx(rowCls, 'cursor-pointer hover:bg-hover/60')}
      title={x.reason}
      onClick={() => openRef(x.requirement_code)}
    >
      <RefChip code={x.requirement_code} />
      <span className="min-w-0 flex-1 truncate">
        <span className="text-fg">{x.requirement_title}</span>
        <span className="text-fg-3">{t.common.sep + t.morning.released}</span>
      </span>
      <RoleTag role={x.role} className="max-sm:hidden" />
      {x.withdrawn ? (
        <Badge tone="accent">{t.morning.withdrawn}</Badge>
      ) : wr.pending(x.requirement_code) ? (
        <PendingBar k={releaseKey(x.requirement_code)} className="-my-1 shrink-0" />
      ) : (
        <Button
          size="sm"
          variant="ghost"
          className="-my-1 shrink-0"
          onClick={(e) => {
            e.stopPropagation()
            wr.withdraw(x.requirement_code, t.morning.withdrawReason, () => toast(t.morning.withdrawDone(x.requirement_code)))
          }}
        >
          <Undo2 className="size-3.5" />
          {t.morning.withdraw}
        </Button>
      )}
    </div>
  )
}

// AdviceNote：「要你处理」验收卡上的参谋建议（R-165）。
export function AdviceNote({ code }: { code: string }) {
  const morning = useMorning()
  const a = (morning.data?.advice ?? []).find((x) => x.requirement_code === code)
  if (!a) return null
  return (
    <p className="mt-1.5 flex min-w-0 items-start gap-1.5 text-[13px] leading-relaxed text-fg-2">
      <RoleTag role="aide" className="mt-0.5" />
      <span className="min-w-0">
        <span className={cx('font-medium', a.verdict === 'pass' ? 'text-run' : 'text-danger')}>{t.morning.verdict[a.verdict]}</span>
        {a.reason && (
          <>
            {t.common.sep}
            <Linkify text={a.reason} />
          </>
        )}
      </span>
    </p>
  )
}

// HoldNote：「要你处理」决策卡上参谋没代答的原因（R-165）。
export function HoldNote({ code }: { code: string }) {
  const morning = useMorning()
  const h = (morning.data?.holds ?? []).find((x) => x.decision_code === code)
  if (!h) return null
  return (
    <p className="flex min-w-0 items-start gap-1.5 text-[13px] leading-relaxed text-fg-2">
      <RoleTag role="aide" className="mt-0.5" />
      <span className="min-w-0">
        <span className="text-fg-3">{t.morning.held}</span>
        {h.reason}
      </span>
    </p>
  )
}
