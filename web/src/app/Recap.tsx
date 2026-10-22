import { useState, type ReactNode } from 'react'
import { History, RefreshCw } from 'lucide-react'
import { api, errMsg, unwrap } from '../api/client'
import { t } from '../i18n'
import { absTime, cx } from '../lib/util'
import { Button, Empty, ErrorState, Segmented, Skeleton } from '../ui/base'
import { Panel } from '../ui/overlay'

type Range = 'h4' | 'today' | 'night' | 'h24'

function rangeOf(r: Range): [Date, Date] {
  const now = new Date()
  const at = (d: Date, h: number) => {
    const x = new Date(d)
    x.setHours(h, 0, 0, 0)
    return x
  }
  switch (r) {
    case 'h4':
      return [new Date(now.getTime() - 4 * 3600e3), now]
    case 'today':
      return [at(now, 0), now]
    case 'night': {
      // 昨晚：昨天 18:00 到今天 10:00（或现在，若更早）
      const y = new Date(now)
      y.setDate(y.getDate() - 1)
      const end = at(now, 10)
      return [at(y, 18), end < now ? end : now]
    }
    default:
      return [new Date(now.getTime() - 24 * 3600e3), now]
  }
}

// 只支持回顾里会出现的 Markdown：标题、列表、加粗、行内代码。
function inline(text: string): ReactNode[] {
  const out: ReactNode[] = []
  const re = /(\*\*[^*]+\*\*|`[^`]+`)/g
  let last = 0
  let m: RegExpExecArray | null
  while ((m = re.exec(text))) {
    if (m.index > last) out.push(text.slice(last, m.index))
    const tok = m[0]
    out.push(tok.startsWith('**') ? <strong key={m.index} className="font-semibold text-fg">{tok.slice(2, -2)}</strong> : <code key={m.index} className="rounded bg-sunken px-1 font-mono text-[12px]">{tok.slice(1, -1)}</code>)
    last = m.index + tok.length
  }
  if (last < text.length) out.push(text.slice(last))
  return out
}

export function Markdown({ text }: { text: string }) {
  const blocks: ReactNode[] = []
  let list: string[] = []
  const flush = () => {
    if (list.length) {
      blocks.push(
        <ul key={`ul${blocks.length}`} className="my-2 space-y-1.5">
          {list.map((li, i) => (
            <li key={i} className="flex gap-2.5 text-sm leading-relaxed text-fg-2">
              <span className="mt-[9px] size-1 shrink-0 rounded-full bg-fg-3" />
              <span>{inline(li)}</span>
            </li>
          ))}
        </ul>,
      )
      list = []
    }
  }
  for (const raw of text.split('\n')) {
    const line = raw.trimEnd()
    const li = line.match(/^\s*(?:[-*]|\d+\.)\s+(.*)$/)
    if (li) {
      list.push(li[1])
      continue
    }
    flush()
    if (!line.trim()) continue
    const h = line.match(/^(#{1,4})\s+(.*)$/)
    if (h) {
      const lvl = h[1].length
      blocks.push(
        <div key={blocks.length} className={cx(lvl <= 2 ? 'mt-5 mb-1.5 text-[15px] font-semibold text-fg first:mt-0' : 'mt-3.5 mb-1 text-sm font-semibold text-fg-2')}>
          {inline(h[2])}
        </div>,
      )
    } else {
      blocks.push(
        <p key={blocks.length} className="my-1.5 text-sm leading-relaxed text-fg-2">
          {inline(line)}
        </p>,
      )
    }
  }
  flush()
  return <div>{blocks}</div>
}

export function RecapPanel({ open, onClose }: { open: boolean; onClose: () => void }) {
  const [range, setRange] = useState<Range>('today')
  const [state, setState] = useState<{ loading?: boolean; text?: string; error?: string; from?: string; to?: string }>({})

  const run = async (r: Range) => {
    const [from, to] = rangeOf(r)
    setState({ loading: true })
    try {
      const res = await unwrap(api.POST('/api/recap', { body: { from: from.toISOString(), to: to.toISOString() } }))
      setState({ text: res.markdown, from: res.from, to: res.to })
    } catch (e) {
      setState({ error: errMsg(e) })
    }
  }

  return (
    <Panel
      open={open}
      onClose={onClose}
      wide
      title={
        <span className="flex items-center gap-2">
          <History className="size-4 text-fg-3" />
          {t.recap.title}
        </span>
      }
      subtitle={state.from && `${absTime(state.from)} – ${absTime(state.to)}`}
    >
      <div className="mb-5 flex flex-wrap items-center gap-2">
        <Segmented
          size="sm"
          value={range}
          onChange={(r) => {
            setRange(r)
            void run(r)
          }}
          options={(['h4', 'today', 'night', 'h24'] as Range[]).map((r) => ({ value: r, label: t.recap.ranges[r] }))}
        />
        <Button size="sm" variant={state.text ? 'ghost' : 'primary'} icon={<RefreshCw className={cx('size-3.5', state.loading && 'animate-spin')} />} disabled={state.loading} onClick={() => run(range)} className="ml-auto">
          {state.text ? t.recap.regenerate : t.recap.generate}
        </Button>
      </div>
      {state.loading ? (
        <div className="space-y-3">
          <Skeleton className="h-5 w-1/3" />
          <Skeleton className="h-4 w-full" />
          <Skeleton className="h-4 w-5/6" />
          <Skeleton className="mt-6 h-5 w-1/4" />
          <Skeleton className="h-4 w-4/5" />
        </div>
      ) : state.error ? (
        <ErrorState error={new Error(state.error)} onRetry={() => run(range)} />
      ) : state.text ? (
        <Markdown text={state.text} />
      ) : (
        <Empty icon={<History />} title={t.recap.empty} />
      )}
    </Panel>
  )
}
