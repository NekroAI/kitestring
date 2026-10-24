import { createContext, useContext, useMemo, useState, useSyncExternalStore, type ReactNode } from 'react'
import { useNavigate } from 'react-router'
import { useDecisions, usePlans, useRequirements } from '../api/queries'
import { useOpenAsk, useOpenPlan, useOpenReq } from '../app/nav'
import { t } from '../i18n'
import { cx } from '../lib/util'
import { Tip } from './base'

// 印记（R-96）：形状只表示类型（需求 = 折角纸片、计划 = 一根线串起阶段、决策 = 菱形、会话 = 圆、项目 = 页签），
// 颜色只表示状态；编号永远跟着形状出现。全站的需求 / 计划 / 决策编号都走 RefChip，别处不再手写编号样式。
// 引用别的对象时（正文、Markdown、关联、时间线）印记显示「图标 + 短标题 + 小编号」（R-151，D-183 方案 A）；
// 旁边已经写着完整标题的自身编号（列表行、抽屉头、⌘K、盘面卡片）和很窄的会话卡 ReqCount 仍只显示编号。

// 需求状态 → 纸片颜色。草稿、待开始灰，进行中靛蓝，阻塞藤黄，待验收朱砂，已完成石绿，搁置灰。
export const reqTone: Record<string, string> = {
  draft: 'text-fg-3',
  ready: 'text-fg-2',
  in_progress: 'text-accent',
  blocked: 'text-wait',
  review: 'text-reply', // 亮色下与朱砂同色；暗色下朱砂对比度不够 3:1，用更亮的 reply
  done: 'text-run',
  shelved: 'text-idle',
}

// 状态药丸与纸片同色（Tailwind 需要静态可见的类名，不能拼接）。
// dot 只给进度条这类色块用；表示单个需求的状态一律画纸片（ReqGlyph）。
export const statusCls: Record<string, { pill: string; dot: string }> = {
  draft: { pill: 'bg-idle-soft text-fg-3', dot: 'bg-fg-3/60' },
  ready: { pill: 'bg-sunken text-fg-2', dot: 'bg-fg-2/70' },
  in_progress: { pill: 'bg-accent-soft text-accent', dot: 'bg-accent' },
  blocked: { pill: 'bg-wait-soft text-wait', dot: 'bg-wait' },
  review: { pill: 'bg-reply-soft text-reply', dot: 'bg-reply' },
  done: { pill: 'bg-run-soft text-run', dot: 'bg-run' },
  shelved: { pill: 'bg-idle-soft text-idle', dot: 'bg-idle/60' },
}

const planTone: Record<string, string> = { draft: 'text-fg-3', active: 'text-fg-2', done: 'text-run', shelved: 'text-idle' }

const paper = 'M3 1.5h7l3 3v10H3z'
const fold = 'M10 1.5v3h3'

// ReqGlyph：需求纸片。虚线草稿 → 实线待开始 → 装一半进行中（阻塞时上方多一道杠）→ 装满待验收 → 打勾已完成；搁置画斜线。
export function ReqGlyph({ status, className }: { status: string; className?: string }) {
  const body = (() => {
    switch (status) {
      case 'draft':
        return <path d={paper} fill="none" stroke="currentColor" strokeWidth="1.3" strokeDasharray="2 1.6" />
      case 'in_progress':
      case 'blocked':
        return (
          <>
            <path d={paper} fill="none" stroke="currentColor" strokeWidth="1.4" />
            <path d="M3 9h10v5.5H3z" fill="currentColor" />
            {status === 'blocked' ? <path d="M5 6.2h6" stroke="currentColor" strokeWidth="1.8" strokeLinecap="round" /> : <path d={fold} fill="none" stroke="currentColor" strokeWidth="1.2" />}
          </>
        )
      case 'review':
        return (
          <>
            <path d={paper} fill="currentColor" />
            <circle cx="8" cy="9.5" r="2.2" fill="var(--surface)" />
          </>
        )
      case 'done':
        return (
          <>
            <path d={paper} fill="currentColor" />
            <path d="M5.4 9.2l1.9 1.9 3.4-3.6" fill="none" stroke="var(--surface)" strokeWidth="1.6" strokeLinecap="round" strokeLinejoin="round" />
          </>
        )
      case 'shelved':
        return (
          <>
            <path d={paper} fill="none" stroke="currentColor" strokeWidth="1.3" />
            <path d="M4 13.5l8-8" stroke="currentColor" strokeWidth="1.3" />
          </>
        )
      default:
        return (
          <>
            <path d={paper} fill="none" stroke="currentColor" strokeWidth="1.4" />
            <path d={fold} fill="none" stroke="currentColor" strokeWidth="1.2" />
          </>
        )
    }
  })()
  return (
    <svg viewBox="0 0 16 16" aria-hidden className={cx('size-3.5 shrink-0', reqTone[status] ?? 'text-fg-2', className)}>
      {body}
    </svg>
  )
}

// PlanGlyph：一根风筝线串起几个阶段。
export function PlanGlyph({ status, className }: { status?: string; className?: string }) {
  return (
    <svg viewBox="0 0 16 16" aria-hidden className={cx('size-3.5 shrink-0', planTone[status ?? ''] ?? 'text-fg-2', className)}>
      <path d="M2 12.5C5 12 7 4 14 3.5" fill="none" stroke="currentColor" strokeWidth="1.3" />
      <rect x="1" y="11" width="3.4" height="3.4" rx=".6" fill="currentColor" />
      <rect x="6.3" y="6.6" width="3.4" height="3.4" rx=".6" fill="currentColor" />
      <rect x="11.6" y="1.8" width="3.4" height="3.4" rx=".6" fill="none" stroke="currentColor" strokeWidth="1.2" />
    </svg>
  )
}

// DecGlyph：菱形加感叹号，和“等你回复”的菱形同一语言。等你作答时朱砂，答完变灰。
export function DecGlyph({ open, className }: { open?: boolean; className?: string }) {
  return (
    <svg viewBox="0 0 16 16" aria-hidden className={cx('size-3.5 shrink-0', open ? 'text-reply' : 'text-fg-3', className)}>
      <path d="M8 1.5l6.5 6.5L8 14.5 1.5 8z" fill="currentColor" />
      <path d="M8 5v3.6" stroke="var(--surface)" strokeWidth="1.6" strokeLinecap="round" />
      <circle cx="8" cy="10.9" r=".95" fill="var(--surface)" />
    </svg>
  )
}

// ProjGlyph：页签。项目只做分组，不抢眼。
export function ProjGlyph({ className }: { className?: string }) {
  return (
    <svg viewBox="0 0 16 16" aria-hidden className={cx('size-3.5 shrink-0 text-fg-3', className)}>
      <path d="M1.8 4.2h4.4l1.3 1.6h6.7v8H1.8z" fill="none" stroke="currentColor" strokeWidth="1.4" strokeLinejoin="round" />
    </svg>
  )
}

// TurnSeal：“轮到你”朱印，全站唯一的实心朱砂小章。
export function TurnSeal({ className }: { className?: string }) {
  return <span className={cx('inline-flex shrink-0 items-center rounded-[4px] bg-seal px-1.5 py-px text-[11px] leading-[18px] font-semibold whitespace-nowrap text-seal-fg', className)}>{t.nextActor.user}</span>
}

// ReqStatus：状态词药丸，颜色与纸片一致。
export function ReqStatus({ status, className }: { status: string; className?: string }) {
  return <span className={cx('inline-flex shrink-0 items-center rounded-full px-2 py-0.5 text-xs font-medium whitespace-nowrap', statusCls[status]?.pill, className)}>{t.reqStatus[status] ?? status}</span>
}

// ---------- 编号 → 对象 ----------

export type RefInfo = {
  kind: 'req' | 'plan' | 'dec'
  code: string
  title: string
  status: string
  turn: boolean // 轮到你处理
  reqCode?: string // 决策挂在哪条需求上
  planCode?: string
  stage?: number
  project?: string
}

const RefIndexContext = createContext<Map<string, RefInfo> | null>(null)

// RefIndexProvider：把需求、计划、决策按编号建一次索引（挂在 Shell），所有印记共用。
export function RefIndexProvider({ children }: { children: ReactNode }) {
  const reqs = useRequirements()
  const plans = usePlans()
  const decs = useDecisions()
  const index = useMemo(() => {
    const m = new Map<string, RefInfo>()
    for (const r of reqs.data ?? [])
      m.set(r.code, {
        kind: 'req',
        code: r.code,
        title: r.title,
        status: r.status,
        turn: r.next_actor === 'user' && r.status !== 'done' && r.status !== 'shelved',
        planCode: r.plan_code || undefined,
        stage: r.stage_seq || undefined,
        project: r.project_name || undefined,
      })
    for (const p of plans.data ?? []) m.set(p.code, { kind: 'plan', code: p.code, title: p.title, status: p.status, turn: false, project: p.project_name || undefined })
    for (const d of decs.data ?? [])
      m.set(d.code, {
        kind: 'dec',
        code: d.code,
        title: d.short_title || d.question,
        status: d.status,
        turn: d.status === 'pending',
        reqCode: d.requirement_code || undefined,
        planCode: d.plan_code || undefined,
        project: d.project_name || undefined,
      })
    return m
  }, [reqs.data, plans.data, decs.data])
  return <RefIndexContext.Provider value={index}>{children}</RefIndexContext.Provider>
}

export function useRefInfo(code: string): RefInfo | undefined {
  return useContext(RefIndexContext)?.get(code)
}

export const refKind = (code: string): RefInfo['kind'] | null => (code.startsWith('R-') ? 'req' : code.startsWith('P-') ? 'plan' : code.startsWith('D-') ? 'dec' : null)

// ---------- 悬停联动 ----------

let lit = ''
const litSubs = new Set<() => void>()
function setLit(code: string) {
  if (lit === code) return
  lit = code
  litSubs.forEach((f) => f())
}
function subscribeLit(f: () => void) {
  litSubs.add(f)
  return () => litSubs.delete(f)
}
function useLit(code: string) {
  return useSyncExternalStore(subscribeLit, () => lit === code)
}

// useOpenRef：点哪个印记都落到同一个地方。需求开需求抽屉，计划开计划抽屉；
// 待答的决策开审批抽屉；答过的决策没有自己的面板，落到它挂着的需求（或计划）上，都没有时回工作台。
export function useOpenRef() {
  const openReq = useOpenReq()
  const openPlan = useOpenPlan()
  const nav = useNavigate()
  const openAsk = useOpenAsk()
  const index = useContext(RefIndexContext)
  return (code: string) => {
    const kind = refKind(code)
    if (kind === 'req') return openReq(code)
    if (kind === 'plan') return openPlan(code)
    const d = index?.get(code)
    // 还在等你答的决策点开审批抽屉（R-177）；挂在待验收需求上的，落到那件验收
    if (d?.status === 'pending') return openAsk(d.reqCode && index?.get(d.reqCode)?.status === 'review' ? `r:${d.reqCode}` : `d:${code}`)
    if (d?.reqCode) openReq(d.reqCode)
    else if (d?.planCode) openPlan(d.planCode)
    else nav('/')
  }
}

// RefGlyph：按编号画出对应形状（状态取自索引，可用 status 覆盖）。
export function RefGlyph({ code, status, className }: { code: string; status?: string; className?: string }) {
  const info = useRefInfo(code)
  const kind = refKind(code)
  const st = status ?? info?.status ?? ''
  if (kind === 'plan') return <PlanGlyph status={st} className={className} />
  if (kind === 'dec') return <DecGlyph open={st === 'pending'} className={className} />
  return <ReqGlyph status={st} className={className} />
}

type ChipProps = {
  code: string
  size?: 'inline' | 'row' | 'card'
  status?: string // 调用方手里已有最新状态时传入（例如计划里的需求行）
  title?: string
  suffix?: ReactNode // 编号后面紧跟的补充（如“阶段 2”）
  hint?: string // 悬停提示里补一句
  turn?: boolean // 默认按数据判断
  showStatus?: boolean // row / card 是否带状态词
  link?: boolean // false：只展示，不可点
  onOpen?: (code: string) => void
  className?: string
  label?: 'code' | 'title' // title：图标 + 短标题 + 缩小变淡的编号（引用别的对象时用，R-151）；code：图标 + 编号
}

// RefChip：一个对象，三种尺寸，同一套零件。
// inline：印记 + 编号（或短标题 + 小编号），放在句子、会话卡、通知里；row：再加标题、状态词、朱印；card：一张小卡。
// shortTitle：引用里用的短标题：有「：」时取前半句，否则截到 12 个字。
export function shortTitle(title: string) {
  const head = title.split(/[：:]/)[0].trim()
  const s = head.length >= 2 && head.length <= 14 ? head : title
  return s.length > 12 ? s.slice(0, 12) + '…' : s
}

export function RefChip({ code, size = 'inline', status, title, suffix, hint, turn, showStatus, link = true, onOpen, className, label = 'code' }: ChipProps) {
  const info = useRefInfo(code)
  const open = useOpenRef()
  const on = useLit(code)
  const [count, setCount] = useState(1)
  const st = status ?? info?.status ?? ''
  const name = title ?? info?.title ?? ''
  const isTurn = turn ?? info?.turn ?? false
  const go = (e: React.SyntheticEvent) => {
    e.stopPropagation()
    e.preventDefault()
    ;(onOpen ?? open)(code)
  }
  const byTitle = label === 'title' && !!name
  const tip = [byTitle ? `${code} ${name}` : name, hint, count > 1 ? t.ref.onPage(count) : ''].filter(Boolean).join(t.common.sep)
  const chip = (
    <span
      data-ref={code}
      role={link ? 'link' : undefined}
      tabIndex={link ? 0 : undefined}
      onClick={link ? go : undefined}
      onKeyDown={link ? (e) => e.key === 'Enter' && go(e) : undefined}
      onPointerEnter={(e) => {
        if (e.pointerType === 'touch') return
        setCount(document.querySelectorAll(`.ref-chip[data-ref="${code}"]`).length)
        setLit(code)
      }}
      onPointerLeave={() => setLit('')}
      onFocus={() => {
        setCount(document.querySelectorAll(`.ref-chip[data-ref="${code}"]`).length)
        setLit(code)
      }}
      onBlur={() => setLit('')}
      className={cx(
        'ref-chip inline-flex max-w-full shrink-0 items-center gap-1 rounded-md border border-line bg-surface-2 whitespace-nowrap text-fg-2 transition-[background,border-color] outline-none',
        byTitle ? 'ref-title py-px pr-1.5 pl-1 align-[-0.3em] text-[0.93em] leading-[1.35]' : 'py-0 pr-1.5 pl-1 align-[-3px] font-mono text-[11px] leading-4 font-semibold',
        link && 'cursor-pointer hover:border-accent hover:bg-[var(--lit)] focus-visible:border-accent focus-visible:bg-[var(--lit)]',
        on && 'lit',
        size === 'inline' && className,
      )}
    >
      <RefGlyph code={code} status={st} />
      {byTitle ? (
        <>
          <span className="min-w-0 truncate font-medium text-fg">{shortTitle(name)}</span>
          <span className="shrink-0 font-mono text-[0.8em] text-fg-3">{code}</span>
        </>
      ) : (
        code
      )}
      {suffix && <span className="inline-flex items-center gap-0.5 font-sans font-medium text-fg-3">{suffix}</span>}
    </span>
  )
  const tipped = tip ? <Tip content={tip}>{chip}</Tip> : chip
  if (size === 'inline') return tipped
  const kind = refKind(code)
  const statusEl = showStatus && st && kind === 'req' ? <ReqStatus status={st} /> : null
  if (size === 'row')
    return (
      <span className={cx('flex min-w-0 items-center gap-2', className)}>
        {tipped}
        {name && <span className="min-w-0 flex-1 truncate text-sm text-fg">{name}</span>}
        {statusEl}
        {isTurn && <TurnSeal />}
      </span>
    )
  return (
    <div className={cx('grid gap-2 rounded-xl border border-line bg-surface p-3.5', className)}>
      <div className="flex flex-wrap items-center gap-2">
        {tipped}
        {statusEl}
        {isTurn && <TurnSeal />}
      </div>
      {name && <div className="text-sm font-semibold text-fg">{name}</div>}
      {(info?.planCode || info?.project) && (
        <div className="flex flex-wrap items-center gap-2 text-xs text-fg-3">
          {info?.planCode && <RefChip code={info.planCode} label="title" suffix={info.stage ? t.ref.stage(info.stage) : undefined} />}
          {info?.project && (
            <span className="inline-flex items-center gap-1">
              <ProjGlyph />
              {info.project}
            </span>
          )}
        </div>
      )}
    </div>
  )
}

// ---------- 文本里的编号 ----------

// 编号前后不能紧挨字母、数字或连字符（“R-95-D”这类子编号不识别）。
export const refPattern = /(?<![A-Za-z0-9_-])([RPD]-\d+)(?![A-Za-z0-9_-])/g

// RefText：文本里认出的一个编号。索引里有这个对象才画成印记，否则原样输出（不误伤）。
// 文本里的编号都是在引用别的对象，默认显示短标题（R-151）。
export function RefText({ code, label = 'title' }: { code: string; label?: 'code' | 'title' }) {
  const info = useRefInfo(code)
  return info ? <RefChip code={code} className="mx-0.5" label={label} /> : <>{code}</>
}

// Linkify：把一段纯文本里的 R-/P-/D- 编号换成印记。
export function Linkify({ text, label }: { text: string; label?: 'code' | 'title' }) {
  if (!text) return null
  const parts = text.split(refPattern)
  if (parts.length === 1) return <>{text}</>
  return <>{parts.map((p, i) => (i % 2 ? <RefText key={i} code={p} label={label} /> : p))}</>
}
