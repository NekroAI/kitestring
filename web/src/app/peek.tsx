import { useCallback, useContext, useEffect, useMemo, useState, type ReactNode } from 'react'
import { ChevronLeft, X } from 'lucide-react'
import { useDecisions, usePlans } from '../api/queries'
import { t } from '../i18n'
import { Button, Tip } from '../ui/base'
import { AsideStackedContext } from '../ui/overlay'
import { refKind, RefChip, useGoRef } from '../ui/refmark'
import { DecisionPeek } from './DecisionCard'
import { PeekContext, SideReqContext } from './nav'
import { PlanBody } from './plans'
import { ReqView } from './reqDrawer'

// 引用在左边再展开一张纸（R-196）：抽屉里点需求 / 决策 / 计划编号，不切走当前抽屉，在它左边展开一张只读预览纸。
// 宽屏放得下就并排，放不下（iPad、窄窗）盖在上面、后面那张露一条边，手机上盖满整个抽屉（布局由 ui/overlay.tsx 的 Panel 决定）。
// 纸里再点编号：换掉这张纸的内容，纸顶「‹ R-前一条」返回；只叠一层，不往左无限摞。
// 「在这里打开」：把抽屉整个切过去，新抽屉顶上「‹ 返回 R-x」回来。
// 需求、审批、会话、计划四种抽屉用同一张纸、同一种返回（设计原则第 2 条：同一类动作只有一种手感）。

export type Peek = { stack: string[]; open: (code: string) => void; push: (code: string) => void; back: () => void; close: () => void }

// usePeek：一个抽屉的纸。main 是抽屉自己的编号（点它自己不开纸）；reset 变了（抽屉换了内容）就收起。
export function usePeek(main: string | null | undefined, reset: unknown): Peek {
  const [stack, setStack] = useState<string[]>([])
  useEffect(() => setStack([]), [reset])
  const open = useCallback((code: string) => {
    if (code === main || !refKind(code)) return
    // 再点一次正开着的那条 = 收起（列表里选中的行同理）
    setStack((s) => (s.length === 1 && s[0] === code ? [] : [code]))
  }, [main])
  const push = useCallback((code: string) => {
    if (code === main || !refKind(code)) return
    setStack((s) => (s[s.length - 1] === code ? s : [...s, code]))
  }, [main])
  const back = useCallback(() => setStack((s) => s.slice(0, -1)), [])
  const close = useCallback(() => setStack([]), [])
  return useMemo(() => ({ stack, open, push, back, close }), [stack, open, push, back, close])
}

// PeekScope：抽屉里点引用交给这张纸（包住整个 Panel）。
export function PeekScope({ peek, children }: { peek: Peek; children: ReactNode }) {
  return (
    <PeekContext value={peek.open}>
      <SideReqContext value={peek.open}>{children}</SideReqContext>
    </PeekContext>
  )
}

// RefPeek：纸本身。from 是当前抽屉（「在这里打开」后用来返回），mainLabel 是盖在上面时「‹ 返回 …」写的编号。
export function RefPeek({ peek, from, mainLabel }: { peek: Peek; from: string; mainLabel?: string }) {
  const stacked = useContext(AsideStackedContext)
  const goRef = useGoRef()
  const code = peek.stack[peek.stack.length - 1]
  if (!code) return null
  const prev = peek.stack.length > 1 ? peek.stack[peek.stack.length - 2] : ''
  // 纸里有前一条时回前一条；盖在上面时第一张也给「返回」，回到后面那个抽屉
  const backLabel = prev || (stacked ? (mainLabel ? t.peek.back(mainLabel) : t.peek.backPlain) : '')
  const head = (
    <div className="flex shrink-0 items-center gap-1.5 px-3 pt-3 pb-1 sm:px-4">
      {backLabel && (
        <button onClick={peek.stack.length > 1 ? peek.back : peek.close} className="inline-flex shrink-0 items-center gap-0.5 rounded-lg bg-sunken py-1 pr-2.5 pl-1.5 text-[13px] font-medium text-fg-2 transition hover:bg-hover hover:text-fg active:scale-[0.97] pointer-coarse:py-1.5">
          <ChevronLeft className="size-4" />
          {prev ? <RefChip code={prev} link={false} className="border-0 bg-transparent px-0" /> : backLabel}
        </button>
      )}
      <span className="min-w-0 truncate px-1 text-xs text-fg-3">{t.peek.label}</span>
      <span className="flex-1" />
      <Tip content={t.peek.openHereTip}>
        <Button size="sm" variant="ghost" onClick={() => goRef(code, from)} className="shrink-0">
          {t.peek.openHere}
        </Button>
      </Tip>
      <Button size="icon-sm" variant="ghost" aria-label={t.common.close} onClick={peek.close}>
        <X className="size-4" />
      </Button>
    </div>
  )
  // 纸里再点编号：换掉这张纸的内容（可返回）
  const inner = (
    <PeekContext value={peek.push}>
      <SideReqContext value={peek.push}>
        <PeekBody key={code} code={code} head={head} />
      </SideReqContext>
    </PeekContext>
  )
  return inner
}

function PeekBody({ code, head }: { code: string; head: ReactNode }) {
  const kind = refKind(code)
  if (kind === 'req') return <ReqView refId={code} top={head} peek />
  if (kind === 'plan') return <PlanPeek code={code} head={head} />
  return <DecPeek code={code} head={head} />
}

function PlanPeek({ code, head }: { code: string; head: ReactNode }) {
  const plans = usePlans()
  const x = (plans.data ?? []).find((v) => v.code === code)
  if (x) return <PlanBody x={x} top={head} />
  return <Missing head={head} loading={plans.isPending} />
}

function DecPeek({ code, head }: { code: string; head: ReactNode }) {
  const decisions = useDecisions()
  const d = (decisions.data ?? []).find((v) => v.code === code)
  if (!d) return <Missing head={head} loading={decisions.isPending} />
  return (
    <div className="flex min-h-0 flex-1 flex-col">
      {head}
      <div className="min-h-0 flex-1 overflow-y-auto overscroll-contain px-4 pt-2 pb-5 sm:px-5">
        <DecisionPeek d={d} />
      </div>
    </div>
  )
}

function Missing({ head, loading }: { head: ReactNode; loading: boolean }) {
  return (
    <div className="flex min-h-0 flex-1 flex-col">
      {head}
      {!loading && <p className="px-5 py-6 text-sm text-fg-3">{t.peek.missing}</p>}
    </div>
  )
}
