import { useEffect, useLayoutEffect, useRef, useState, type ReactNode } from 'react'
import { ChevronDown, Undo2 } from 'lucide-react'
import { toast } from 'sonner'
import type { Decision, Requirement, StatusChange } from '../api/client'
import { useMorning } from '../api/queries'
import { t } from '../i18n'
import { latestEvidence, roundsOf } from '../lib/proofs'
import { absTime, cx, relTime, useNow } from '../lib/util'
import { Badge, Button, Tip } from '../ui/base'
import { Thumbs } from '../ui/attach'
import { BlockList } from '../ui/blocks'
import { Md } from '../ui/markdown'
import { Item, List } from '../ui/motion'
import { Linkify, RefChip } from '../ui/refmark'
import { answerNote, DecisionComposer, DecisionOutcome } from './DecisionCard'
import { ProxyNote } from './grant'
import { EvidenceBody, EvidenceCard, EvidenceRow, gist, hasPayload, VoidedEvidence } from './evidence'
import { DecisionChain } from './chain'
import { CommitTags, shortSha, useReqCommits } from './commits'
import { isStatusReq, StatusArrow, statusTone } from './statusReq'
import { Who } from './who'
import { releaseKey, useWithdrawRelease } from './requirements'
import { PendingBar } from './verdict'
import { PlanSummary } from './points'
import { approvedOf } from '../lib/waiting'
import { GuestNoteContent, GuestSource } from './guestNote'

// 需求抽屉只有这一条时间线（R-195）：从上往下按时间排，每份证据只出现一次——本轮最新的一份展开，其余收成一行，点开就地展开。
// 每条记录都写清是谁做的：身份胶囊 + 会话标题（动作当时的标题，悬停看现在的），点开是那个会话。
// 每次退回修改开启新的一轮，复杂需求反复几轮也能看清来龙去脉；留言不开新一轮。主助理的验收建议贴着最新证据。
// 打开抽屉时停在最新那份证据的开头（ReqView 按 data-latest 定位）。

type Entry = { at: string; key: string; round: number; node: ReactNode }

const dot = {
  base: 'bg-line-strong',
  evidence: 'bg-run',
  revise: 'bg-reply',
  accent: 'bg-accent',
  wait: 'bg-wait',
  ask: 'bg-reply',
  done: 'bg-run',
}

// Node：时间线上的一个节点。左边是竖线上的小圆点；head 是一行「谁 做了什么 …… 时间」，下面放内容。
function Node({ tone = dot.base, head, at, now, muted, children }: { tone?: string; head?: ReactNode; at?: string; now: number; muted?: boolean; children?: ReactNode }) {
  return (
    <div className="relative pl-[22px]">
      <span aria-hidden className={cx('absolute top-[3px] left-0 size-[15px] rounded-full border-2 border-surface', tone)} />
      <div className={cx('min-w-0', muted && 'opacity-55')}>
        {head && (
          <div className="flex items-baseline gap-2 text-[12.5px] leading-normal text-fg-3">
            <div className="flex min-w-0 flex-1 flex-wrap items-center gap-x-1.5 gap-y-0.5">{head}</div>
            {at && (
              <Tip content={absTime(at)}>
                <span className="shrink-0 tabular">{relTime(at, now)}</span>
              </Tip>
            )}
          </div>
        )}
        {children && <div className={cx('space-y-2', head && 'mt-1.5')}>{children}</div>}
      </div>
    </div>
  )
}

// Bubble：你退回 / 留言、主助理的验收建议这类「说的话」，头部写谁、时间，下面是原话。
function Bubble({ cls, head, at, now, children }: { cls: string; head: ReactNode; at: string; now: number; children?: ReactNode }) {
  return (
    <div className={cx('rounded-xl px-3 py-2 text-[13.5px] leading-relaxed break-words text-fg', cls)}>
      <div className="mb-0.5 flex items-baseline gap-2 text-xs text-fg-3">
        <div className="flex min-w-0 flex-1 flex-wrap items-center gap-x-1.5 gap-y-0.5">{head}</div>
        <Tip content={absTime(at)}>
          <span className="shrink-0 tabular">{relTime(at, now)}</span>
        </Tip>
      </div>
      {children}
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
                  <p className="rounded-lg bg-danger-soft/50 px-2.5 py-1.5 leading-relaxed break-words whitespace-pre-wrap text-fg-2">{before || t.timeline.editEmpty}</p>
                </div>
                <div>
                  <p className="mb-0.5 text-xs text-fg-3">{t.timeline.editAfter}</p>
                  <p className="rounded-lg bg-run-soft/50 px-2.5 py-1.5 leading-relaxed break-words whitespace-pre-wrap text-fg">{after || t.timeline.editEmpty}</p>
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

const Act = ({ children }: { children: ReactNode }) => <span className="text-fg-2">{children}</span>

// focus（R-241）：从「要你处理」点开挂在这条需求上的待答决策时，它是时间线的最后一步、原地作答；
// 早期的记录折叠起来（只留最近几步），证据都收成一行，打开时停在这条决策上。
const KEEP = 3

export function ReqTimeline({ r, decisions, focus }: { r: Requirement; decisions: Decision[]; focus?: string }) {
  const now = useNow()
  const [early, setEarly] = useState(false) // 展开了前面折叠的记录
  useEffect(() => setEarly(false), [r.id, focus])
  const morning = useMorning()
  const wr = useWithdrawRelease()
  const commitsQ = useReqCommits(r.code)
  const [opened, setOpened] = useState<Set<string>>(() => new Set()) // 点开的旧证据
  const toggle = (id: string) =>
    setOpened((s) => {
      const n = new Set(s)
      if (n.has(id)) n.delete(id)
      else n.add(id)
      return n
    })
  const canWithdraw = !!r.release && (r.status === 'ready' || r.status === 'in_progress' || r.status === 'blocked')
  const mine = [...(r.feedback ?? [])].sort((a, b) => (a.answered_at ?? a.created_at).localeCompare(b.answered_at ?? b.created_at))
  const { revises, roundOf, current } = roundsOf(r)
  const latest = latestEvidence(r)
  // 状态变更历史（R-153）。放行、退回修改的那次变化引用了对应记录（ref = D-x），并进那条记录的节点里显示；
  // 新建时的初始状态并进「记下这条需求」；其余每次变化单独一个节点。旧需求没有记录，照旧显示
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
  const who = (c: StatusChange) => <Who actor={c.actor} session={c.session} role={c.role} title={c.session_title} className="max-w-[16em]" />

  const entries: Entry[] = [
    {
      at: r.created_at,
      key: 'create',
      round: 1,
      node: (
        <Node
          now={now}
          at={r.created_at}
          head={
            <>
              {created ? who(created) : <Who actor={r.created_by === 'user' ? 'user' : 'agent'} />}
              <Act>{t.proof.created}</Act>
              {created && created.to !== 'draft' && <StatusFlow c={created} />}
            </>
          }
        />
      ),
    },
  ]
  for (const e of r.evidence ?? []) {
    const round = roundOf(e.created_at)
    const isLatest = e.id === latest && !focus
    let body: ReactNode
    if (e.voided && e.void_reason) body = <VoidedEvidence e={e} r={r} />
    else if (isLatest) body = <EvidenceCard e={e} r={r} latest />
    else if (opened.has(e.id)) body = <EvidenceCard e={e} r={r} onFold={() => toggle(e.id)} />
    else body = <EvidenceRow e={e} onOpen={() => toggle(e.id)} voidedOnBack={e.voided} />
    entries.push({
      at: e.created_at,
      key: e.id,
      round,
      node: (
        <div data-latest={isLatest ? '' : undefined}>
          <Node tone={dot.evidence} now={now}>
            {body}
          </Node>
        </div>
      ),
    })
  }
  // 主助理的验收建议：贴着最新证据（只有当前有效的那条，交了新证据后旧建议不再显示）
  const advice = r.status === 'review' ? (morning.data?.advice ?? []).find((a) => a.requirement_code === r.code) : undefined
  if (advice) {
    entries.push({
      at: advice.at,
      key: 'advice',
      round: current,
      node: (
        <Node tone={dot.accent} now={now}>
          <Bubble
            cls="bg-accent-soft"
            at={advice.at}
            now={now}
            head={
              <>
                <Who actor="agent" session={advice.by} role={advice.by_role || 'aide'} title={advice.by_title} className="max-w-[16em]" />
                <span className={cx('font-semibold', advice.verdict === 'pass' ? 'text-run' : 'text-danger')}>{t.proof.advice(advice.verdict)}</span>
              </>
            }
          >
            <Linkify text={advice.reason} />
          </Bubble>
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
          tone={c.reverts ? dot.revise : dot.base}
          now={now}
          at={c.at}
          head={
            <>
              <Act>{c.reverts ? t.commits.revert : t.commits.commit}</Act>
              <Badge mono>{shortSha(c.link)}</Badge>
              {c.source && t.commits.source[c.source] && <span>{t.commits.source[c.source]}</span>}
            </>
          }
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
  // 审查意见（R-88）：审查员给的参考，和验收证据分开标明，不开新一轮。
  // 带结构化结果的（R-232 评审验证：一句结论、checklist、截图）按证据的样子画，套在评审的底色里
  for (const e of r.reviews ?? []) {
    entries.push({
      at: e.created_at,
      key: e.id,
      round: roundOf(e.created_at),
      node: (
        <Node
          tone={dot.wait}
          now={now}
          at={e.created_at}
          muted={e.voided}
          head={
            <>
              <Who actor="agent" session={e.session_key} role={e.role || 'reviewer'} title={e.session_title} className="max-w-[16em]" />
              <Act>{t.reviewer.review}</Act>
              <Badge tone="wait">{t.reviewer.badge}</Badge>
            </>
          }
        >
          {focus && !opened.has(e.id) ? (
            // 作答决策时（R-241）审查意见也先收成一行，点开就地展开
            <button onClick={() => toggle(e.id)} className="flex w-full items-center gap-2 rounded-[10px] border border-wait/25 bg-wait-soft/30 px-2.5 py-2 text-left text-[13px] text-fg-2 transition hover:border-wait/50">
              <span className="min-w-0 flex-1 truncate">
                <Linkify text={gist(e)} />
              </span>
              <ChevronDown className="size-3.5 shrink-0 -rotate-90 text-fg-3" />
            </button>
          ) : hasPayload(e) ? (
            <div className="@container rounded-xl border border-wait/25 bg-wait-soft/40 px-3.5 py-3">
              <EvidenceBody e={e} r={r} />
            </div>
          ) : (
            <>
              <ReviewBody text={e.summary} />
              {!!e.files?.length && <Thumbs files={e.files} />}
            </>
          )}
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
            tone={dot.wait}
            now={now}
            at={at}
            head={
              <>
                {f.assigned_by === 'auto' ? <Act>{t.verify.auto}</Act> : (
                  <>
                    <Who actor="user" />
                    <Act>{t.proof.reviewTask}</Act>
                  </>
                )}
                <Who actor="agent" session={f.session_key} className="max-w-[16em]" />
                <Badge tone={f.status === 'delivered' ? 'run' : 'neutral'}>{t.decision.status[f.status]}</Badge>
              </>
            }
          />
        ),
      })
      continue
    }
    // 修改记录（R-134）：Agent 改了标题 / 描述 / 验收标准 / 优先级 / 类型，记下是谁、什么身份、改前改后
    if (f.kind === 'edit') {
      const rows = f.blocks?.find((b) => b.type === 'table')?.rows ?? []
      entries.push({
        at,
        key: f.id,
        round: roundOf(at),
        node: (
          <Node
            now={now}
            at={at}
            head={
              <>
                <Who actor="agent" session={f.assigned_by} role={f.context} title={f.by_title} className="max-w-[16em]" />
                <Act>{t.proof.edited}</Act>
                {f.from_decision && (
                  <span className="inline-flex items-center gap-1">
                    {t.timeline.editFrom}
                    <RefChip code={f.from_decision} />
                  </span>
                )}
              </>
            }
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
    // 放行（R-166）：PM / 主助理把草稿改为待开始，写明理由；当前有效的放行可以一键撤回（改回草稿）
    if (f.kind === 'release') {
      const withdrawn = !!f.close_reason
      entries.push({
        at,
        key: f.id,
        round: roundOf(at),
        node: (
          <Node
            tone={dot.accent}
            now={now}
            at={at}
            muted={withdrawn}
            head={
              <>
                <Who actor="agent" session={f.assigned_by} role={f.context} title={f.by_title} className="max-w-[16em]" />
                <Act>{t.proof.released}</Act>
                {withdrawn && <Badge tone="neutral">{t.timeline.withdrawn}</Badge>}
              </>
            }
          >
            <p className="text-[12.5px] leading-relaxed break-words text-fg-3">
              <span className="mr-1">{t.timeline.releaseReason}：</span>
              <Linkify text={f.answer ?? ''} />
            </p>
            {withdrawn && <p className="text-sm text-fg-2">{f.close_reason}</p>}
            {!withdrawn && canWithdraw && r.release?.code === f.code && (
              <div>
                {wr.pending(r.code) ? (
                  <PendingBar k={releaseKey(r.code)} />
                ) : (
                  <Button size="sm" onClick={() => wr.withdraw(r.code, '', () => toast(t.timeline.releaseWithdrawn))}>
                    <Undo2 className="size-3.5" />
                    {t.timeline.releaseWithdraw}
                  </Button>
                )}
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
            tone={dot.accent}
            now={now}
            at={at}
            head={
              <>
                {f.assigned_by ? <Who actor="agent" session={f.assigned_by} role="pm" title={f.by_title} className="max-w-[14em]" /> : <Who actor="user" />}
                <Act>{t.proof.assigned}</Act>
                <Who actor="agent" session={f.session_key} className="max-w-[14em]" />
                <Badge tone={f.status === 'delivered' ? 'run' : 'neutral'}>{t.decision.status[f.status]}</Badge>
              </>
            }
          />
        ),
      })
      continue
    }
    // 会话留言（R-249）：会话在需求下写的细节，和你的留言在同一条时间线；署名是身份胶囊 + 会话名（你的留言写「你」），
    // 底色用中性的灰，与你的留言区分；留言本身不送达任何会话，所以没有送达状态
    if (f.kind === 'comment') {
      entries.push({
        at,
        key: f.id,
        round: roundOf(at),
        node: (
          <Node tone={dot.base} now={now}>
            <Bubble
              cls="border border-line bg-sunken/60"
              at={at}
              now={now}
              head={
                <>
                  <Who actor="agent" session={f.assigned_by} role={f.from_role} title={f.by_title} className="max-w-[16em]" />
                  <span className="font-semibold text-fg-2">{t.proof.noted}</span>
                </>
              }
            >
              {f.answer && <Md text={f.answer} />}
              {!!f.blocks?.length && <BlockList blocks={f.blocks} className="mt-1.5" />}
              {!!f.answer_files?.length && (
                <div className="mt-1.5">
                  <Thumbs files={f.answer_files} size="sm" />
                </div>
              )}
            </Bubble>
          </Node>
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
        <Node tone={note ? dot.accent : dot.revise} now={now}>
          <Bubble
            cls={note ? 'bg-accent-soft/70' : 'bg-reply-soft'}
            at={at}
            now={now}
            head={
              <>
                {f.guest ? (
                  // 放入的访客留言（R-281）：写明来自谁、经分享链接、由你放入
                  <GuestSource g={f.guest} />
                ) : (
                  <>
                    {f.assigned_by ? <Who actor="agent" session={f.assigned_by} title={f.by_title} className="max-w-[16em]" /> : <Who actor="user" />}
                    <span className={cx('font-semibold', note ? 'text-fg-2' : 'text-reply')}>{note ? t.proof.noted : t.proof.revised}</span>
                  </>
                )}
                {flowOf(f.code)}
                <Badge tone={f.status === 'delivered' ? 'run' : 'neutral'}>{t.decision.status[f.status]}</Badge>
              </>
            }
          >
            {f.guest ? <GuestNoteContent text={f.answer ?? ''} original={f.guest.original} addend={f.guest.addend} /> : f.answer && <Linkify text={f.answer} />}
            {!!f.answer_files?.length && (
              <div className="mt-1.5">
                <Thumbs files={f.answer_files} size="sm" />
              </div>
            )}
          </Bubble>
        </Node>
      ),
    })
  }
  for (const d of decisions.filter((x) => x.kind === 'decision')) {
    const asker = <Who actor="agent" session={d.origin_session || d.session_key} title={d.by_title} className="max-w-[16em]" />
    if (d.status === 'pending') {
      const focused = d.code === focus
      entries.push({
        at: focused ? '￿￿' : '￿', // 等你回答的问题放在最后，紧挨着底部的输入区；从「要你处理」点开的那条排在最后
        key: d.id,
        round: current,
        node: (
          <div data-latest={focused ? '' : undefined}>
          <Node
            tone={dot.ask}
            now={now}
            head={
              <>
                {asker}
                <span className="font-semibold text-reply">{t.timeline.ask}</span>
              </>
            }
          >
            <DecisionComposer d={d} embedded autoFocus={focused} />
          </Node>
          </div>
        ),
      })
      continue
    }
    if (isStatusReq(d) && d.status !== 'closed') {
      // 改状态申请（R-188）：批准 / 拒绝 / 作废三种结局，批准的那次状态变化并在这里
      const ok = !d.voided && approvedOf(d) === true // 读结论字段（R-239）
      const at = d.answered_at ?? d.created_at
      const note = d.voided ? '' : (d.answer ?? '').trim().replace(/^(批准|拒绝)[；;：:，,\s]*/, '')
      entries.push({
        at,
        key: d.id,
        round: roundOf(at),
        node: (
          <Node
            tone={ok ? dot.done : dot.accent}
            now={now}
            at={at}
            muted={d.voided}
            head={
              <>
                <Act>{d.voided ? t.timeline.statusReq.voided : ok ? t.timeline.statusReq.approved : t.timeline.statusReq.rejected}</Act>
                {merged.get(d.code) ? <StatusFlow c={merged.get(d.code)!} /> : <StatusArrow from={d.request_from} to={d.request_to} />}
                <RefChip code={d.code} status={d.status} link={false} />
              </>
            }
          >
            {d.context && (
              <p className="flex flex-wrap items-baseline gap-x-1.5 text-sm leading-relaxed break-words text-fg-2">
                {asker}
                <span className="text-fg-3">{t.proof.reason}</span>
                <Linkify text={d.context} />
              </p>
            )}
            {d.voided && <p className="text-sm text-fg-3">{t.timeline.statusReq.voidedWhy}</p>}
            {note && (
              <p className="rounded-xl bg-sunken/70 px-3 py-2 text-sm leading-relaxed break-words whitespace-pre-wrap text-fg">
                <span className="mr-1.5 text-fg-3">{t.timeline.statusReq.note}</span>
                <Linkify text={note} />
              </p>
            )}
          </Node>
        ),
      })
      continue
    }
    // 你的答复：选了选项的只显示另写的附言（选项已经在上面标出来了）；批准类的结论写在头上
    const structured = !!d.points?.length || (!!d.options?.length && !d.approval) || (d.approval && approvedOf(d) !== undefined)
    const note = structured ? answerNote(d) : (d.answer ?? '')
    const noteLabel = structured ? t.decision.noteLabel : t.timeline.answer
    const ok = d.status !== 'closed' ? approvedOf(d) : undefined
    entries.push({
      at: d.created_at,
      key: d.id,
      round: roundOf(d.created_at),
      node: (
        <Node
          tone={dot.accent}
          now={now}
          at={d.answered_at ?? d.created_at}
          muted={d.status === 'closed'}
          head={
            <>
              {asker}
              <Act>{t.timeline.decision}</Act>
              <RefChip code={d.code} status={d.status} link={false} />
              {d.status === 'closed' && <Badge tone="neutral">{t.timeline.withdrawn}</Badge>}
              {ok !== undefined && !d.proxy_by && <Badge tone={ok ? 'run' : 'danger'}>{t.decision.verdict[ok ? 'approved' : 'rejected']}</Badge>}
            </>
          }
        >
          {/* 原问题照原样显示（R-238：短标题可能改了意思） */}
          <p className="text-sm leading-relaxed break-words text-fg">
            <Linkify text={d.question} />
          </p>
          {d.status !== 'closed' && d.approval && !!d.summary?.length && <PlanSummary lines={d.summary} />}
          {/* 当时的选项和你选的那个（R-241） */}
          {d.status !== 'closed' && <DecisionOutcome d={d} />}
          {d.status === 'closed' && d.close_reason && (
            <p className="text-sm text-fg-2">
              {t.timeline.withdrawnReason}：<Linkify text={d.close_reason} />
            </p>
          )}
          {d.proxy_by ? (
            <ProxyNote d={d} />
          ) : (
            note && (
              <p className="rounded-xl bg-sunken/70 px-3 py-2 text-sm leading-relaxed break-words whitespace-pre-wrap text-fg">
                <span className="mr-1.5 text-fg-3">{noteLabel}</span>
                <Linkify text={note} />
              </p>
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
    // Agent 交完证据改为待验收：证据本身就说明了，不再单列一条（原型）
    if (c.to === 'review' && c.actor === 'agent') continue
    const accept = c.to === 'done' && c.actor === 'user'
    const start = c.via === 'link' && c.to === 'in_progress'
    const via = start ? '' : c.to === 'blocked' && c.ref ? t.timeline.waitOn : t.timeline.via[c.via] // 受阻指向（R-243）：「改为 受阻 · 等待 R-212」
    entries.push({
      at: c.at,
      key: c.id,
      round: roundOf(c.at),
      node: (
        <Node
          tone={accept ? dot.done : dot.base}
          now={now}
          at={c.at}
          head={
            accept ? (
              <span className="font-semibold text-run">{t.timeline.done}</span>
            ) : (
              <>
                {who(c)}
                {start ? (
                  <Act>{t.proof.started}</Act>
                ) : (
                  <>
                    <span>{t.proof.changed}</span>
                    <StatusFlow c={c} />
                    {(via || c.ref) && (
                      <span className="inline-flex items-center gap-1">
                        {via && <span aria-hidden>·</span>}
                        {via}
                        {c.ref && <RefChip code={c.ref} label="title" />}
                      </span>
                    )}
                  </>
                )}
              </>
            )
          }
        >
          {c.to === 'shelved' && (c.reason_kind || c.reason) ? (
            <p className="text-sm leading-relaxed break-words text-fg-2">
              <span className="mr-1.5 text-fg-3">{t.timeline.shelveWhy}</span>
              <Linkify text={t.reqs.shelve.why(t.reqs.shelve.kinds[c.reason_kind ?? ''] ?? '', c.reason ?? '')} />
            </p>
          ) : c.from === 'blocked' && c.reason ? (
            // 解除受阻（R-269）：自动解除或会话关联开工，写明原来在等什么
            <p className="text-sm leading-relaxed break-words text-fg-2">
              <span className="mr-1.5 text-fg-3">{t.timeline.unblockWhy}</span>
              <Linkify text={c.reason} />
            </p>
          ) : c.to === 'blocked' && c.reason ? (
            // 受阻的原因（R-243）；在等的 R-x / D-x 已画在上面的印记里
            <p className="text-sm leading-relaxed break-words text-fg-2">
              <span className="mr-1.5 text-fg-3">{t.timeline.blockedWhy}</span>
              <Linkify text={c.reason} />
            </p>
          ) : undefined}
        </Node>
      ),
    })
  }
  if (r.status === 'done' && !changes.some((c) => c.to === 'done')) {
    entries.push({
      at: r.updated_at,
      key: 'done',
      round: current,
      node: <Node tone={dot.done} now={now} at={r.updated_at} head={<span className="font-semibold text-run">{t.timeline.done}</span>} />,
    })
  }
  entries.sort((a, b) => a.at.localeCompare(b.at))

  // 竖线画在整列左侧，「第 n 轮」分隔线盖在上面；AnimatePresence 只认直接子元素，这里展开成一维列表。
  const showRounds = revises.length > 0
  const items: ReactNode[] = []
  let last = 0
  // 作答这条决策时只留最近几步，前面的折叠成一行（R-241）
  const hidden = focus && !early ? Math.max(0, entries.length - 1 - KEEP) : 0
  if (focus && entries.length - 1 > KEEP) {
    items.push(
      <Item key="earlier">
        <div className="relative z-[1] pb-3">
          <button onClick={() => setEarly(!early)} className="relative flex w-full items-center gap-2 bg-surface py-1 pl-[22px] text-left text-[12.5px] text-fg-3 transition hover:text-fg">
            <span aria-hidden className="absolute top-1/2 left-[3px] size-[9px] -translate-y-1/2 rounded-full border-2 border-dashed border-line-strong bg-surface" />
            <span>{early ? t.timeline.earlierFold : t.timeline.earlier(entries.length - 1 - KEEP)}</span>
            <ChevronDown className={cx('size-3.5 transition-transform', early && 'rotate-180')} />
          </button>
        </div>
      </Item>,
    )
  }
  entries.forEach((e, i) => {
    if (i < hidden) return
    if (showRounds && e.round !== last) {
      items.push(
        <Item key={'round' + e.round}>
          <div className="relative z-[1] flex items-center gap-2.5 bg-surface py-3 text-xs text-fg-3">
            <span className="font-display text-[13px] text-fg-2">{t.timeline.round(e.round)}</span>
            {e.round === current && r.status !== 'done' && <Badge tone="accent">{t.timeline.current}</Badge>}
            <span aria-hidden className="h-px flex-1 bg-line" />
          </div>
        </Item>,
      )
    }
    last = e.round
    items.push(
      <Item key={e.key}>
        <div className={cx(i < entries.length - 1 && 'pb-3')}>{e.node}</div>
      </Item>,
    )
  })
  return (
    <div className="relative before:absolute before:top-1.5 before:bottom-1.5 before:left-[6.5px] before:w-[1.5px] before:bg-line">
      <List>{items}</List>
    </div>
  )
}
