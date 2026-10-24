import { useLayoutEffect, useRef, useState, type ReactNode } from 'react'
import { ArrowRightLeft, CheckCheck, FileCheck2, GitCommitHorizontal, HelpCircle, LockOpen, MessageSquareText, PencilLine, ScanSearch, Send, Sparkles, Undo2 } from 'lucide-react'
import { toast } from 'sonner'
import type { Decision, Requirement, StatusChange } from '../api/client'
import { t } from '../i18n'
import { absTime, cx, relTime, useNow } from '../lib/util'
import { Badge, Button, Tip } from '../ui/base'
import { Thumbs } from '../ui/attach'
import { Md } from '../ui/markdown'
import { Item, List } from '../ui/motion'
import { DecisionComposer } from './DecisionCard'
import { useActions } from './actions'
import { ProxyNote } from './grant'
import { evidenceKind } from './review'
import { EvidenceView, hasPayload, VoidedEvidence } from './evidence'
import { useSessions } from '../api/queries'
import { sessionTitle } from './SessionCard'
import { Linkify, RefChip } from '../ui/refmark'
import { DecisionChain } from './chain'
import { CommitTags, shortSha, useReqCommits } from './commits'
import { isStatusReq, StatusArrow, statusTone } from './statusReq'

// 需求进展：一条竖向时间线，从上往下按时间排，最新在最下面（紧挨底部的输入区）。
// 每次退回修改开启新的一轮，复杂需求反复几轮也能看清来龙去脉；留言不开新一轮。

type Entry = { at: string; key: string; round: number; node: ReactNode }

const tone = {
  create: 'bg-sunken text-fg-3',
  evidence: 'bg-run-soft text-run',
  review: 'bg-wait-soft text-wait',
  revise: 'bg-reply-soft text-reply',
  note: 'bg-accent-soft text-accent',
  decision: 'bg-accent-soft text-accent',
  ask: 'bg-reply text-seal-fg',
  done: 'bg-run text-seal-fg',
}

// Node：时间线上的一个节点。圆点在左侧竖线上，内容占满右侧。
function Node({ icon, tone: cls, title, at, now, muted, children }: { icon: ReactNode; tone: string; title: ReactNode; at?: string; now: number; muted?: boolean; children?: ReactNode }) {
  return (
    <div className="relative flex gap-3">
      <span className={cx('relative z-[1] mt-0.5 flex size-7 shrink-0 items-center justify-center rounded-full ring-4 ring-surface [&_svg]:size-3.5', cls)}>{icon}</span>
      <div className={cx('min-w-0 flex-1 pt-1', muted && 'opacity-55')}>
        <div className="flex items-baseline justify-between gap-3">
          <div className="flex min-w-0 flex-wrap items-center gap-1.5 text-[13px] font-medium text-fg-2">{title}</div>
          {at && (
            <Tip content={absTime(at)}>
              <span className="shrink-0 text-xs text-fg-3 tabular">{relTime(at, now)}</span>
            </Tip>
          )}
        </div>
        {children && <div className="mt-1.5 space-y-2">{children}</div>}
      </div>
    </div>
  )
}

// ReviewBody：审查意见正文（Markdown）。长意见先收起到一屏左右，点“展开全部”看完整内容。
function ReviewBody({ text }: { text: string }) {
  const ref = useRef<HTMLDivElement>(null)
  const [open, setOpen] = useState(false)
  const [long, setLong] = useState(false)
  useLayoutEffect(() => {
    const el = ref.current
    if (el) setLong(el.scrollHeight > el.clientHeight + 4)
  }, [text])
  return (
    <div className="rounded-xl border border-wait/25 bg-wait-soft/40 text-sm leading-relaxed break-words text-fg">
      <div ref={ref} className={cx('relative px-3 py-2', !open && 'max-h-80 overflow-hidden')}>
        <Md text={text} />
        {long && !open && <span aria-hidden className="pointer-events-none absolute inset-x-0 bottom-0 h-12 bg-gradient-to-t from-wait-soft to-transparent" />}
      </div>
      {long && (
        <button onClick={() => setOpen(!open)} className="w-full border-t border-wait/20 px-3 py-1.5 text-left text-xs font-medium text-wait transition hover:opacity-80">
          {open ? t.reviewer.less : t.reviewer.more}
        </button>
      )}
    </div>
  )
}

// 状态变化（R-153）：「草稿 → 待开始」两枚小标签
function StatusFlow({ c }: { c: StatusChange }) {
  return (
    <span className="inline-flex items-center gap-1 font-normal">
      {c.from && <Badge tone={statusTone[c.from]}>{t.reqStatus[c.from] ?? c.from}</Badge>}
      {c.from && <span className="text-fg-3">→</span>}
      <Badge tone={statusTone[c.to]}>{t.reqStatus[c.to] ?? c.to}</Badge>
    </span>
  )
}

function Quote({ children, cls }: { children: ReactNode; cls: string }) {
  return <p className={cx('rounded-xl px-3 py-2 text-sm leading-relaxed whitespace-pre-wrap text-fg', cls)}>{children}</p>
}

// EditDiff：修改记录（R-134）的改前改后。标题、优先级、类型这类短字段一行「改前 → 改后」；
// 描述、验收标准默认收起，点开上下对照。行是 [字段, 中文名, 改前, 改后]。
function EditDiff({ rows }: { rows: (string[] | null)[] }) {
  return (
    <div className="space-y-1.5 text-sm">
      {rows.map((row) => {
        const [field, label, before, after] = row ?? []
        if (field === 'detail' || field === 'acceptance') {
          return (
            <details key={field} className="rounded-xl bg-sunken/60 px-3 py-2">
              <summary className="cursor-pointer text-fg-2 select-none marker:text-fg-3">
                <span className="font-medium">{label}</span>
                <span className="ml-1.5 text-xs text-fg-3">{t.timeline.editExpand}</span>
              </summary>
              <div className="mt-2 space-y-2">
                <div>
                  <p className="mb-0.5 text-xs text-fg-3">{t.timeline.editBefore}</p>
                  <p className="rounded-lg bg-danger-soft/50 px-2.5 py-1.5 leading-relaxed whitespace-pre-wrap break-words text-fg-2">{before || t.timeline.editEmpty}</p>
                </div>
                <div>
                  <p className="mb-0.5 text-xs text-fg-3">{t.timeline.editAfter}</p>
                  <p className="rounded-lg bg-run-soft/50 px-2.5 py-1.5 leading-relaxed whitespace-pre-wrap break-words text-fg">{after || t.timeline.editEmpty}</p>
                </div>
              </div>
            </details>
          )
        }
        return (
          <p key={field} className="flex flex-wrap items-baseline gap-x-1.5 rounded-xl bg-sunken/60 px-3 py-2 leading-relaxed break-words">
            <span className="font-medium text-fg-2">{label}</span>
            <span className="text-fg-3 line-through">{before || t.timeline.editEmpty}</span>
            <span className="text-fg-3">→</span>
            <span className="text-fg">{after || t.timeline.editEmpty}</span>
          </p>
        )
      })}
    </div>
  )
}

export function ReqTimeline({ r, decisions }: { r: Requirement; decisions: Decision[] }) {
  const now = useNow()
  const sessions = useSessions()
  const { withdrawRelease } = useActions()
  const commitsQ = useReqCommits(r.code)
  const [withdrawing, setWithdrawing] = useState(false)
  const canWithdraw = !!r.release && (r.status === 'ready' || r.status === 'in_progress' || r.status === 'blocked')
  const who = (key?: string | null) => {
    const s = (sessions.data?.sessions ?? []).find((x) => x.key === key)
    return s ? [sessionTitle(s), s.device_name].filter(Boolean).join(t.common.sep) : ''
  }
  const mine = [...(r.feedback ?? [])].sort((a, b) => (a.answered_at ?? a.created_at).localeCompare(b.answered_at ?? b.created_at))
  const revises = mine.filter((f) => f.kind === 'feedback')
  const bounds = revises.map((f) => f.answered_at ?? f.created_at)
  const roundOf = (at: string) => 1 + bounds.filter((b) => b <= at).length
  const current = 1 + revises.length
  // 状态变更历史（R-153）。放行、退回修改的那次变化引用了对应记录（ref = D-x），并进那条记录的节点里显示；
  // 新建时的初始状态并进「提出了这条需求」；其余每次变化单独一个节点。旧需求没有记录，照旧显示
  const changes = r.status_changes ?? []
  const merged = new Map<string, StatusChange>()
  for (const c of changes) {
    if (c.ref && (c.via === 'release' || c.via === 'revise') && mine.some((f) => f.code === c.ref)) merged.set(c.ref, c)
    // 用户批准了 Agent 的改状态申请（R-188）：并进那条申请的节点
    if (c.ref && c.via === 'request' && decisions.some((d) => d.code === c.ref)) merged.set(c.ref, c)
  }
  const created = changes.find((c) => c.via === 'create')
  const flowOf = (code: string) => {
    const c = merged.get(code)
    return c ? <StatusFlow c={c} /> : null
  }

  const entries: Entry[] = [
    {
      at: r.created_at,
      key: 'create',
      round: 1,
      node: (
        <Node
          icon={<Sparkles />}
          tone={tone.create}
          title={
            <>
              {r.created_by === 'agent' ? t.timeline.createdAgent : t.timeline.createdUser}
              {created && created.to !== 'draft' && <StatusFlow c={created} />}
            </>
          }
          at={r.created_at}
          now={now}
        />
      ),
    },
  ]
  for (const e of r.evidence ?? []) {
    entries.push({
      at: e.created_at,
      key: e.id,
      round: roundOf(e.created_at),
      node: (
        <Node
          icon={<FileCheck2 />}
          tone={tone.evidence}
          title={
            <>
              {t.timeline.evidence}
              <Badge tone="run">{evidenceKind(e.kind)}</Badge>
            </>
          }
          at={e.created_at}
          now={now}
          muted={e.voided}
        >
          {(() => {
            const body = (
              <>
                {hasPayload(e) ? <EvidenceView e={e} acceptance={r.acceptance} /> : <Md text={e.summary} className="text-sm leading-relaxed text-fg" />}
                {e.link && <p className="truncate font-mono text-xs text-fg-3">{e.link}</p>}
                {!hasPayload(e) && !!e.files?.length && <Thumbs files={e.files} />}
              </>
            )
            return e.voided && e.void_reason ? (
              <VoidedEvidence e={e} by={who(e.voided_by)}>
                {body}
              </VoidedEvidence>
            ) : (
              body
            )
          })()}
        </Node>
      ),
    })
  }
  // 提交与回滚（R-156）：标题写了编号、Agent 手动关联的提交按提交时间排进时间线；交证据时附的提交已在证据里，不重复。
  // 纸鸢不改仓库：被回滚时只提示该怎么做
  const commits = commitsQ.data?.commits ?? []
  for (const c of commits) {
    if (c.source === 'evidence') continue
    const target = c.reverts ? commits.find((x) => x.reverted_by === c.link) : undefined
    const active = !!target && !c.reverted_by
    entries.push({
      at: c.at,
      key: 'commit-' + c.link,
      round: roundOf(c.at),
      node: (
        <Node
          icon={c.reverts ? <Undo2 /> : <GitCommitHorizontal />}
          tone={c.reverts ? tone.revise : tone.create}
          title={
            <>
              {c.reverts ? t.commits.revert : t.commits.commit}
              <Badge mono>{shortSha(c.link)}</Badge>
              {c.source && t.commits.source[c.source] && <span className="font-normal text-fg-3">{t.commits.source[c.source]}</span>}
            </>
          }
          at={c.at}
          now={now}
        >
          <p className={cx('text-sm break-words text-fg', c.reverted_by && 'text-fg-2 line-through decoration-fg-3/60')}>{c.summary}</p>
          {(c.reverts || c.reverted_by || c.ci) && (
            <div className="flex flex-wrap items-center gap-1.5">
              <CommitTags c={c} all={commits} />
            </div>
          )}
          {active && <p className="rounded-lg bg-reply-soft px-2.5 py-1.5 text-xs leading-relaxed break-words text-reply">{t.commits.revertHint(shortSha(c.link), r.code)}</p>}
          {c.ci?.state === 'failure' && <p className="rounded-lg bg-danger-soft px-2.5 py-1.5 text-xs leading-relaxed break-words text-danger">{t.commits.ciHint}</p>}
        </Node>
      ),
    })
  }
  // 审查意见（R-88）：审查员给的参考，和验收证据分开标明，不开新一轮
  for (const e of r.reviews ?? []) {
    entries.push({
      at: e.created_at,
      key: e.id,
      round: roundOf(e.created_at),
      node: (
        <Node
          icon={<ScanSearch />}
          tone={tone.review}
          title={
            <>
              {t.reviewer.review}
              <Badge tone="wait">{t.reviewer.badge}</Badge>
              <span className="truncate font-normal text-fg-3">{who(e.session_key)}</span>
            </>
          }
          at={e.created_at}
          now={now}
          muted={e.voided}
        >
          <ReviewBody text={e.summary} />
          {!!e.files?.length && <Thumbs files={e.files} />}
        </Node>
      ),
    })
  }
  for (const f of mine) {
    const at = f.answered_at ?? f.created_at
    if (f.kind === 'review_task') {
      entries.push({
        at,
        key: f.id,
        round: roundOf(at),
        node: (
          <Node
            icon={<ScanSearch />}
            tone={tone.review}
            title={
              <>
                {t.reviewer.timeline}
                <span className="truncate font-normal text-fg-3">{who(f.session_key) || f.device_name}</span>
                <Badge tone={f.status === 'delivered' ? 'run' : 'neutral'}>{t.decision.status[f.status]}</Badge>
              </>
            }
            at={at}
            now={now}
          />
        ),
      })
      continue
    }
    // 修改记录（R-134）：Agent 改了标题 / 描述 / 验收标准 / 优先级 / 类型，记下是谁、什么身份、改前改后
    if (f.kind === 'edit') {
      const role = t.role[f.context ?? ''] || f.context || 'Agent'
      const rows = f.blocks?.find((b) => b.type === 'table')?.rows ?? []
      entries.push({
        at,
        key: f.id,
        round: roundOf(at),
        node: (
          <Node
            icon={<PencilLine />}
            tone={tone.create}
            title={
              <>
                {t.timeline.edit(role)}
                <span className="truncate font-normal text-fg-3">{who(f.assigned_by)}</span>
              </>
            }
            at={at}
            now={now}
          >
            {!!f.answer && (
              <p className="text-sm text-fg-2">
                <Linkify text={f.answer} />
              </p>
            )}
            <EditDiff rows={rows} />
          </Node>
        ),
      })
      continue
    }
    // 放行（R-166）：PM / Adv 把草稿改为待开始，写明理由；当前有效的放行可以一键撤回（改回草稿）
    if (f.kind === 'release') {
      const withdrawn = !!f.close_reason
      const role = t.role[f.context ?? ''] || f.context || 'Agent'
      entries.push({
        at,
        key: f.id,
        round: roundOf(at),
        node: (
          <Node
            icon={<LockOpen />}
            tone={tone.note}
            title={
              <>
                {t.timeline.release(role)}
                <span className="truncate font-normal text-fg-3">{who(f.assigned_by)}</span>
                {flowOf(f.code)}
                {withdrawn && <Badge tone="neutral">{t.timeline.withdrawn}</Badge>}
              </>
            }
            at={at}
            now={now}
            muted={withdrawn}
          >
            <Quote cls="bg-accent-soft/60">
              <span className="mr-1.5 text-fg-3">{t.timeline.releaseReason}</span>
              <Linkify text={f.answer ?? ''} />
            </Quote>
            {withdrawn && <p className="text-sm text-fg-2">{f.close_reason}</p>}
            {!withdrawn && canWithdraw && r.release?.code === f.code && (
              <div>
                <Button
                  size="sm"
                  disabled={withdrawing}
                  onClick={async () => {
                    setWithdrawing(true)
                    if (await withdrawRelease(r.code)) toast(t.timeline.releaseWithdrawn)
                    setWithdrawing(false)
                  }}
                >
                  <Undo2 className="size-3.5" />
                  {t.timeline.releaseWithdraw}
                </Button>
              </div>
            )}
          </Node>
        ),
      })
      continue
    }
    if (f.kind === 'assign') {
      entries.push({
        at,
        key: f.id,
        round: roundOf(at),
        node: (
          <Node
            icon={<Send />}
            tone={tone.note}
            title={
              <>
                {f.assigned_by ? t.assign.timelinePm : t.assign.timeline}
                <span className="truncate font-normal text-fg-3">{who(f.session_key) || f.device_name}</span>
                <Badge tone={f.status === 'delivered' ? 'run' : 'neutral'}>{t.decision.status[f.status]}</Badge>
              </>
            }
            at={at}
            now={now}
          />
        ),
      })
      continue
    }
    const note = f.kind === 'note'
    entries.push({
      at,
      key: f.id,
      round: note ? roundOf(at) : roundOf(at) - 1, // 修改意见收尾它所在的那一轮
      node: (
        <Node
          icon={note ? <MessageSquareText /> : <Undo2 />}
          tone={note ? tone.note : tone.revise}
          title={
            <>
              {note ? t.timeline.note : t.timeline.revise}
              {flowOf(f.code)}
              <Badge tone={f.status === 'delivered' ? 'run' : 'neutral'}>{t.decision.status[f.status]}</Badge>
            </>
          }
          at={at}
          now={now}
        >
          {f.answer && <Quote cls={note ? 'bg-accent-soft/60' : 'bg-reply-soft/60'}>
              <Linkify text={f.answer} />
            </Quote>}
          {!!f.answer_files?.length && <Thumbs files={f.answer_files} size="sm" />}
        </Node>
      ),
    })
  }
  for (const d of decisions.filter((x) => x.kind === 'decision')) {
    if (d.status === 'pending') {
      entries.push({
        at: '￿', // 等你回答的问题放在最后，紧挨着底部的输入区
        key: d.id,
        round: current,
        node: (
          <Node icon={<HelpCircle />} tone={tone.ask} title={t.timeline.ask} now={now}>
            <DecisionComposer d={d} embedded />
          </Node>
        ),
      })
      continue
    }
    if (isStatusReq(d) && d.status !== 'closed') {
      // 改状态申请（R-188）：批准 / 拒绝 / 作废三种结局，批准的那次状态变化并在这里
      const ok = !d.voided && (d.answer ?? '').trim().startsWith('批准')
      const at = d.answered_at ?? d.created_at
      const asker = t.role[(sessions.data?.sessions ?? []).find((x) => x.key === d.session_key)?.role ?? ''] || 'Agent'
      const note = d.voided ? '' : (d.answer ?? '').trim().replace(/^(批准|拒绝)[；;：:，,\s]*/, '')
      entries.push({
        at,
        key: d.id,
        round: roundOf(at),
        node: (
          <Node
            icon={d.voided ? <Undo2 /> : ok ? <CheckCheck /> : <ArrowRightLeft />}
            tone={ok ? tone.evidence : tone.decision}
            muted={d.voided}
            title={
              <>
                {d.voided ? t.timeline.statusReq.voided : ok ? t.timeline.statusReq.approved : t.timeline.statusReq.rejected}
                {merged.get(d.code) ? <StatusFlow c={merged.get(d.code)!} /> : <StatusArrow from={d.request_from} to={d.request_to} />}
                <RefChip code={d.code} status={d.status} link={false} />
              </>
            }
            at={at}
            now={now}
          >
            {d.context && (
              <p className="text-sm leading-relaxed break-words text-fg-2">
                <span className="mr-1.5 text-fg-3">{t.timeline.statusReq.why(asker)}</span>
                <Linkify text={d.context} />
              </p>
            )}
            {d.voided && <p className="text-sm text-fg-3">{t.timeline.statusReq.voidedWhy}</p>}
            {note && (
              <Quote cls="bg-sunken/70">
                <span className="mr-1.5 text-fg-3">{t.timeline.statusReq.note}</span>
                <Linkify text={note} />
              </Quote>
            )}
          </Node>
        ),
      })
      continue
    }
    entries.push({
      at: d.created_at,
      key: d.id,
      round: roundOf(d.created_at),
      node: (
        <Node
          icon={<HelpCircle />}
          tone={tone.decision}
          title={
            <>
              {t.timeline.decision}
              <RefChip code={d.code} status={d.status} link={false} />
              {d.status === 'closed' && <Badge tone="neutral">{t.timeline.withdrawn}</Badge>}
            </>
          }
          at={d.answered_at ?? d.created_at}
          now={now}
          muted={d.status === 'closed'}
        >
          <p className="text-sm text-fg">
            <Linkify text={d.short_title || d.question} />
          </p>
          {d.status === 'closed' && d.close_reason && (
            <p className="text-sm text-fg-2">
              {t.timeline.withdrawnReason}：<Linkify text={d.close_reason} />
            </p>
          )}
          {d.proxy_by ? (
            <ProxyNote d={d} />
          ) : (
            d.answer && (
              <Quote cls="bg-sunken/70">
                <span className="mr-1.5 text-fg-3">{t.timeline.answer}</span>
                <Linkify text={d.answer} />
              </Quote>
            )
          )}
          {d.answer_reason && !d.proxy_by && (
            <p className="text-sm leading-relaxed break-words text-fg-2">
              <span className="mr-1.5 text-fg-3">{t.decision.reasonLabel}</span>
              <Linkify text={d.answer_reason} />
            </p>
          )}
          {!!d.answer_files?.length && <Thumbs files={d.answer_files} size="sm" />}
          {/* 后果链（R-154）：取代 / 推翻、产生的需求与提交；被取代也在这里显示 */}
          <DecisionChain code={d.code} skipReq={r.code} />
        </Node>
      ),
    })
  }
  for (const c of changes) {
    if (c.via === 'create' || (c.ref && merged.get(c.ref) === c)) continue
    const accept = c.to === 'done' && c.actor === 'user'
    let actor: ReactNode
    if (c.actor === 'user') actor = t.timeline.byUser
    else if (c.actor === 'agent')
      actor = (
        <>
          {t.role[c.role ?? ''] || 'Agent'}
          {who(c.session) && <span className="truncate font-normal text-fg-3">{who(c.session)}</span>}
        </>
      )
    else actor = t.timeline.bySystem
    const via = t.timeline.via[c.via]
    entries.push({
      at: c.at,
      key: c.id,
      round: roundOf(c.at),
      node: (
        <Node
          icon={accept ? <CheckCheck /> : <ArrowRightLeft />}
          tone={accept ? tone.done : tone.create}
          title={
            <>
              {accept ? t.timeline.done : actor}
              {!accept && <span className="font-normal text-fg-3">{t.timeline.changed}</span>}
              <StatusFlow c={c} />
              {(via || c.ref) && (
                <span className="inline-flex items-center gap-1 font-normal text-fg-3">
                  {via}
                  {c.ref && <RefChip code={c.ref} label="title" />}
                </span>
              )}
            </>
          }
          at={c.at}
          now={now}
        >
          {c.to === 'shelved' && (c.reason_kind || c.reason) && (
            <p className="text-sm leading-relaxed break-words text-fg-2">
              <span className="mr-1.5 text-fg-3">{t.timeline.shelveWhy}</span>
              <Linkify text={t.reqs.shelve.why(t.reqs.shelve.kinds[c.reason_kind ?? ''] ?? '', c.reason ?? '')} />
            </p>
          )}
        </Node>
      ),
    })
  }
  if (r.status === 'done' && !changes.some((c) => c.to === 'done')) {
    entries.push({ at: r.updated_at, key: 'done', round: current, node: <Node icon={<CheckCheck />} tone={tone.done} title={t.timeline.done} at={r.updated_at} now={now} /> })
  }
  entries.sort((a, b) => a.at.localeCompare(b.at))

  // 竖线画在每项左侧（最后一项不画）；AnimatePresence 只认直接子元素，这里展开成一维列表。
  const showRounds = revises.length > 0
  const items: ReactNode[] = []
  let last = 0
  entries.forEach((e, i) => {
    if (showRounds && e.round !== last) {
      items.push(
        <Item key={'round' + e.round}>
          <div className="flex items-center gap-2 pt-1 pb-4 text-xs text-fg-3">
            <span className="font-display text-[13px] text-fg-2">{t.timeline.round(e.round)}</span>
            {e.round === current && r.status !== 'done' && <Badge tone="accent">{t.timeline.current}</Badge>}
            <span aria-hidden className="h-0 flex-1 border-t border-dashed border-line-strong/80" />
          </div>
        </Item>,
      )
    }
    last = e.round
    const end = i === entries.length - 1
    items.push(
      <Item key={e.key}>
        <div className={cx('relative', !end && 'pb-5')}>
          {!end && <span aria-hidden className="absolute top-8 bottom-0 left-[13px] w-px bg-line" />}
          {e.node}
        </div>
      </Item>,
    )
  })
  return <List>{items}</List>
}
