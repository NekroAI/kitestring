import { useState } from 'react'
import { ChevronDown, GitCommitHorizontal, Undo2 } from 'lucide-react'
import { toast } from 'sonner'
import type { Decision, MorningReport, Project, Session } from '../api/client'
import { useMorning, useProjects } from '../api/queries'
import { t } from '../i18n'
import { cx, relTime, usePref } from '../lib/util'
import { Badge, Button } from '../ui/base'
import { Collapse } from '../ui/motion'
import { presence, RoleTag } from '../ui/marks'
import { Linkify, RefChip } from '../ui/refmark'
import { useActions } from './actions'
import { OverrideBox } from './grant'

// 参谋早报（R-165，D-179；版式按顾问意见）：你离开期间参谋做了什么、替你定了什么，占工作台顶部唯一的简报位。
//   - 有早报的项目由早报占位，PM 简报收成卡里的一行「PM：……」；没有早报的项目照旧显示 PM 简报（见 desk.tsx Briefs）。
//   - 卡里只放：总述、① 完成了什么、② 替你定了什么（代答就地推翻、放行可撤回）；③ 留给你的 ④ 待验收只写一行计数，
//     详情就在下方「要你处理」里，不重复列（同一信息只在一处）。参谋的验收建议、没代答的原因挂在「要你处理」对应的卡上。
//   - 可收起成一行（记在本机）；新早报（总述更新或换了离开期间）来了重新展开。

const briefTTL = 12 * 3600e3

// stamp：一份早报的标识，变了就算新早报。
const stamp = (r: MorningReport) => `${r.since}|${r.note?.at ?? ''}`

function useSessionByKey(sessions: Session[]) {
  return (key?: string) => sessions.find((x) => x.key === key)
}

// 一个小节：标题 + 条数，空时写一句。
function Part({ title, n, empty, children }: { title: string; n: number; empty: string; children: React.ReactNode }) {
  return (
    <section className="min-w-0">
      <h4 className="mb-2 flex items-baseline gap-2 text-[13px] font-semibold text-fg-2">
        {title}
        <span className="text-xs font-normal text-fg-3 tabular">{n}</span>
      </h4>
      {n ? children : <p className="text-[13px] text-fg-3">{empty}</p>}
    </section>
  )
}

function DoneList({ r, now }: { r: MorningReport; now: number }) {
  const [tells, setTells] = useState(false)
  const done = r.done ?? []
  return (
    <Part title={t.morning.done} n={done.length} empty={t.morning.doneEmpty}>
      <ul className="space-y-2.5">
        {done.map((x) => {
          const commits = (x.deliveries ?? []).filter((d) => d.kind === 'commit')
          const last = [...(x.deliveries ?? [])].reverse().find((d) => d.kind !== 'commit') ?? (x.deliveries ?? []).at(-1)
          return (
            <li key={x.requirement_code} className="min-w-0 text-[13px]">
              <div className="flex min-w-0 items-center gap-2">
                <RefChip code={x.requirement_code} status={x.status} />
                <span className="min-w-0 truncate font-medium text-fg">{x.requirement_title}</span>
                <span className="ml-auto shrink-0 text-xs text-fg-3 tabular">{relTime(x.at, now)}</span>
              </div>
              {last && (
                <p className="mt-0.5 line-clamp-2 leading-relaxed text-fg-2">
                  <Linkify text={last.text} />
                </p>
              )}
              {commits.length > 0 && (
                <div className="mt-1 flex flex-wrap gap-1.5">
                  {commits.map((c, i) => (
                    <Badge key={i} tone="outline" mono icon={<GitCommitHorizontal className="size-3 shrink-0" />}>
                      {c.link || c.text}
                    </Badge>
                  ))}
                </div>
              )}
            </li>
          )
        })}
      </ul>
      {r.tell_count > 0 && (
        <div className="mt-3">
          <button onClick={() => setTells(!tells)} aria-expanded={tells} className="flex items-center gap-1 text-xs text-fg-3 hover:text-fg-2">
            <ChevronDown className={cx('size-3.5 transition-transform', !tells && '-rotate-90')} />
            {t.morning.tells(r.tell_count)}
          </button>
          <Collapse show={tells}>
            <ul className="mt-1.5 space-y-1 border-l border-line pl-3">
              {(r.tells ?? []).map((m) => (
                <li key={m.code} className="flex min-w-0 gap-2 text-xs text-fg-2">
                  <span className="min-w-0 flex-1 line-clamp-2">
                    <Linkify text={m.text} />
                  </span>
                  <span className="shrink-0 text-fg-3 tabular">{relTime(m.at, now)}</span>
                </li>
              ))}
            </ul>
          </Collapse>
        </div>
      )}
    </Part>
  )
}

function ReleaseRow({ x }: { x: NonNullable<MorningReport['releases']>[number] }) {
  const { withdrawRelease } = useActions()
  const [busy, setBusy] = useState(false)
  return (
    <div className="space-y-1 text-[13px]">
      <div className="flex min-w-0 items-center gap-2">
        <RefChip code={x.requirement_code} />
        <span className="line-clamp-2 min-w-0 flex-1 font-medium text-fg">{x.requirement_title}</span>
        {x.withdrawn ? (
          <Badge tone="accent">{t.morning.withdrawn}</Badge>
        ) : (
          <Button
            size="sm"
            variant="ghost"
            className="-my-1 shrink-0"
            disabled={busy}
            onClick={async () => {
              setBusy(true)
              if (await withdrawRelease(x.requirement_code, t.morning.withdrawReason)) toast(t.morning.withdrawDone(x.requirement_code))
              setBusy(false)
            }}
          >
            <Undo2 className="size-3.5" />
            {t.morning.withdraw}
          </Button>
        )}
      </div>
      <p className="flex min-w-0 items-start gap-1.5 leading-relaxed text-fg-2">
        <RoleTag role={x.role} className="mt-0.5" />
        <span className="min-w-0">
          <span className="text-fg-3">{t.morning.released}</span>
          {t.common.sep}
          <Linkify text={x.reason} />
        </span>
      </p>
    </div>
  )
}

// ProxyRow：一条代答，紧凑排：问题、Adv 答了什么（风险档）、理由；「推翻」就地展开写你的答复。
function ProxyRow({ d }: { d: Decision }) {
  const [over, setOver] = useState(false)
  const done = !!d.overridden_at
  return (
    <div className="space-y-1 text-[13px]">
      <div className="flex min-w-0 items-center gap-2">
        <RefChip code={d.code} status={d.status} link={false} />
        <span className="line-clamp-2 min-w-0 flex-1 font-medium text-fg">{d.short_title || d.question}</span>
        {done ? (
          <Badge tone="accent">{t.grant.overridden}</Badge>
        ) : (
          d.status !== 'closed' &&
          !over && (
            <Button size="sm" variant="ghost" className="-my-1 shrink-0" onClick={() => setOver(true)}>
              <Undo2 className="size-3.5" />
              {t.grant.override}
            </Button>
          )
        )}
      </div>
      <p className="flex min-w-0 items-start gap-1.5 leading-relaxed text-fg-2">
        <RoleTag role="advisor" className="mt-0.5" />
        <span className="min-w-0">
          <span className={cx('font-medium', done ? 'text-fg-3 line-through' : 'text-fg')}>{d.proxy_answer}</span>
          {d.proxy_risk && <span className="text-fg-3">{t.common.sep + t.grant.risk[d.proxy_risk]}</span>}
          {t.common.sep}
          <Linkify text={d.proxy_reason ?? ''} />
        </span>
      </p>
      {done && (
        <p className="leading-relaxed text-fg">
          <span className="mr-1.5 text-fg-3">{t.grant.yours}</span>
          <Linkify text={d.answer} />
        </p>
      )}
      {over && !done && (
        <div className="pt-1">
          <OverrideBox d={d} startOpen onClose={() => setOver(false)} />
        </div>
      )}
    </div>
  )
}

function DecidedList({ r }: { r: MorningReport }) {
  const proxied = r.proxied ?? []
  const releases = r.releases ?? []
  return (
    <Part title={t.morning.decided} n={proxied.length + releases.length} empty={t.morning.decidedEmpty}>
      <div className="space-y-3">
        {proxied.map((d: Decision) => (
          <ProxyRow key={d.id} d={d} />
        ))}
        {releases.map((x) => (
          <ReleaseRow key={x.code} x={x} />
        ))}
      </div>
    </Part>
  )
}

function MorningCard({ r, project, sessions, now }: { r: MorningReport; project?: Project; sessions: Session[]; now: number }) {
  const [folded, setFolded] = usePref<Record<string, string>>('ks.morning.folded', {})
  const find = useSessionByKey(sessions)
  const adv = find(r.note?.by ?? r.grant?.session_key)
  const pm = project?.brief && project.brief_at && now - Date.parse(project.brief_at) < briefTTL ? project : undefined
  const pmSession = find(pm?.brief_by)
  const open = folded[r.project_id] !== stamp(r)
  const toggle = () => setFolded({ ...folded, [r.project_id]: open ? stamp(r) : '' })
  const blocked = r.blocked ?? []
  const decided = (r.proxied?.length ?? 0) + (r.releases?.length ?? 0)

  return (
    <section className="rounded-xl border border-line bg-surface shadow-xs">
      <button onClick={toggle} aria-expanded={open} className="flex w-full items-center gap-2.5 px-4 pt-3 pb-1 text-left">
        <RoleTag role="advisor" presence={adv ? presence(adv, true) || undefined : undefined} />
        <h3 className="shrink-0 text-sm font-semibold text-fg">{t.morning.title}</h3>
        <span className="min-w-0 truncate text-xs text-fg-3">
          {[project?.name ?? r.project_name, t.morning.since(relTime(r.since, now))].filter(Boolean).join(t.common.sep)}
        </span>
        <span className="ml-auto flex shrink-0 items-center gap-1 text-xs text-fg-3">
          <span className="max-sm:hidden">{open ? t.morning.fold : t.morning.unfold}</span>
          <ChevronDown className={cx('size-4 transition-transform', !open && '-rotate-90')} />
        </span>
      </button>

      <div className="space-y-2 px-4 pb-3">
        <p className={cx('text-sm leading-relaxed', r.note ? 'text-fg' : 'text-fg-3', !open && 'line-clamp-1')}>
          {r.note ? <Linkify text={r.note.text} label="title" /> : t.morning.noNote}
          {r.note && <span className="ml-2 text-xs text-fg-3 tabular">{relTime(r.note.at, now)}</span>}
        </p>
        {pm && (
          <p className={cx('flex min-w-0 items-start gap-2 text-[13px] leading-relaxed text-fg-2', !open && 'max-sm:hidden')}>
            <RoleTag role="pm" presence={pmSession ? presence(pmSession) || undefined : undefined} className="mt-0.5" />
            <span className={cx('min-w-0 flex-1', open ? 'line-clamp-3' : 'line-clamp-1')}>
              <Linkify text={pm.brief} label="title" />
            </span>
            <span className="mt-0.5 shrink-0 text-xs text-fg-3 tabular max-sm:hidden">{relTime(pm.brief_at, now)}</span>
          </p>
        )}
        {!open && (
          <p className="text-xs text-fg-3">
            {[t.morning.doneShort((r.done ?? []).length), t.morning.decidedShort(decided), t.morning.left(r.pending, r.review)].join(t.common.sep)}
          </p>
        )}
      </div>

      <Collapse show={open}>
        <div className="grid grid-cols-1 gap-x-8 gap-y-5 border-t border-line px-4 py-4 lg:grid-cols-2">
          <DoneList r={r} now={now} />
          <DecidedList r={r} />
        </div>
        <div className="flex flex-wrap items-center gap-x-3 gap-y-1.5 rounded-b-xl border-t border-line bg-surface-2/60 px-4 py-2.5 text-[13px] text-fg-2">
          <span>{t.morning.left(r.pending, r.review)}</span>
          {blocked.length > 0 && (
            <span className="inline-flex flex-wrap items-center gap-1.5">
              <span className="text-fg-3">{t.morning.blocked}</span>
              {blocked.map((c) => (
                <RefChip key={c} code={c} />
              ))}
            </span>
          )}
        </div>
      </Collapse>
    </section>
  )
}

// MorningReports：工作台顶部的参谋早报（每个有早报的项目一张）；返回占了简报位的项目，PM 简报不再单独显示。
export function MorningReports({ sessions, now }: { sessions: Session[]; now: number }) {
  const morning = useMorning()
  const projects = useProjects()
  const list = morning.data?.reports ?? []
  if (!list.length) return null
  return (
    <div className="mb-4 space-y-3">
      {list.map((r) => (
        <MorningCard key={r.project_id} r={r} project={projects.data?.projects.find((p) => p.id === r.project_id)} sessions={sessions} now={now} />
      ))}
    </div>
  )
}

// useMorningProjects：有早报的项目（它们的 PM 简报收进早报卡里）。
export function useMorningProjects() {
  const morning = useMorning()
  return new Set((morning.data?.reports ?? []).map((r) => r.project_id))
}

// AdviceNote：「要你处理」验收卡上的参谋建议（R-165）。
export function AdviceNote({ code }: { code: string }) {
  const morning = useMorning()
  const a = (morning.data?.advice ?? []).find((x) => x.requirement_code === code)
  if (!a) return null
  return (
    <p className="mt-1.5 flex min-w-0 items-start gap-1.5 text-[13px] leading-relaxed text-fg-2">
      <RoleTag role="advisor" className="mt-0.5" />
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
      <RoleTag role="advisor" className="mt-0.5" />
      <span className="min-w-0">
        <span className="text-fg-3">{t.morning.held}</span>
        {h.reason}
      </span>
    </p>
  )
}
