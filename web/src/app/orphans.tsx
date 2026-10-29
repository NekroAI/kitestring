import { useState } from 'react'
import { useQuery, useQueryClient } from '@tanstack/react-query'
import { toast } from 'sonner'
import { api, errMsg, unwrap, type Schemas } from '../api/client'
import { t } from '../i18n'
import { cx, duration, relTime } from '../lib/util'
import { Badge, Button, Tip } from '../ui/base'
import { useConfirm } from '../ui/overlay'
import { Collapse, Item, List, useSettled } from '../ui/motion'
import { useOpenSession } from './nav'
import { PendingCover, schedule, usePendingMap } from './verdict'

// 孤儿进程（R-209）：会话结束后它开的后台进程还在跑（或已脱离 Agent）。工作台会话区末尾单独一块，没有时不显示。
// 「停止」经 Hub 交给进程所在设备的 daemon，daemon 核对 pid + 启动时间后才发信号；先 SIGTERM，几秒不退再问要不要强制。

type Orphan = Schemas['Orphan']
type Stop = Schemas['ProcStop']

const RECENT_MS = 60 * 60 * 1000 // 停止后这么久内，块里还留着结果

export function useOrphans() {
  return useQuery({
    queryKey: ['orphans'],
    queryFn: () => unwrap(api.GET('/api/orphans')),
    refetchInterval: 60_000, // CPU / 内存、已运行时长；列表变化走实时推送
    select: (d) => ({
      orphans: d.orphans ?? [],
      ignored: d.ignored ?? [],
      stops: d.stops ?? [],
    }),
  })
}

// shortCmd：去掉解释器和可执行文件的绝对路径（node /x/bin/pnpm dev → pnpm dev），完整命令在悬停提示里。
const base = (p: string) => p.slice(p.lastIndexOf('/') + 1)
export function shortCmd(cmd: string) {
  const f = cmd.split(' ')
  if (f.length > 1 && /^(node|python3?|bun|deno|ruby)$/.test(base(f[0])) && f[1].startsWith('/')) return [base(f[1]), ...f.slice(2)].join(' ')
  if (f[0].startsWith('/')) return [base(f[0]), ...f.slice(1)].join(' ')
  return cmd
}

function stopText(s: Stop) {
  const head = t.orphans.result[s.result] ?? s.result
  return s.detail ? `${head}：${s.detail}` : head
}

// OrphanProcs：整块的出现 / 消失走同一套弹簧（R-216）；首次加载不播。块里的数据按 props 传，消失动画里内容保持原样。
export function OrphanProcs({ now }: { now: number }) {
  const q = useOrphans()
  const still = !useSettled(!!q.data)
  const d = q.data
  const show = !!d && (d.orphans.length > 0 || d.ignored.length > 0 || d.stops.some((x) => now - new Date(x.at).getTime() < RECENT_MS))
  return <Collapse show={show} fluid still={still}>{d && <OrphanBlock now={now} data={d} />}</Collapse>
}

function OrphanBlock({ now, data }: { now: number; data: NonNullable<ReturnType<typeof useOrphans>['data']> }) {
  const qc = useQueryClient()
  const confirm = useConfirm()
  const [busy, setBusy] = useState<string | null>(null)
  const [showIgnored, setShowIgnored] = useState(false)
  const pend = usePendingMap()
  // 撤销期一结束（已提交或在提交）这个进程就在本地离开提醒块，不等重新取数（R-230）；提交失败时放回
  const gone = (o: Orphan) => {
    const e = pend.get(ignoreKey(o))
    return !!e && e.phase !== 'wait'
  }
  const orphans = data.orphans.filter((o) => !gone(o))
  const ignored = data.ignored
  const recent = data.stops.filter((s) => now - new Date(s.at).getTime() < RECENT_MS).slice(0, 3)
  if (!orphans.length && !recent.length && !ignored.length) return null
  const nOrphan = orphans.filter((o) => o.state === 'orphan').length

  // 忽略（R-209 退回意见）：只对孤儿，不发信号，只是不再提醒；走统一的 3 秒撤销期（R-201），到点才记到 Hub
  const ignoreBody = (o: Orphan, undo = false) => ({
    device_id: o.device_id,
    id: o.id,
    undo,
  })
  const ignore = (o: Orphan) =>
    schedule(ignoreKey(o), {
      act: 'move',
      label: t.orphans.ignoreAct,
      subject: shortCmd(o.cmd),
      beacon: {
        method: 'POST',
        url: '/api/orphans/ignore',
        body: ignoreBody(o),
      },
      commit: async () => {
        try {
          await unwrap(api.POST('/api/orphans/ignore', { body: ignoreBody(o) }))
          await qc.invalidateQueries({ queryKey: ['orphans'] })
          return true
        } catch (e) {
          toast.error(errMsg(e, t.fail.ignore))
          return false
        }
      },
    })
  const unignore = async (o: Orphan) => {
    try {
      await unwrap(api.POST('/api/orphans/ignore', { body: ignoreBody(o, true) }))
      void qc.invalidateQueries({ queryKey: ['orphans'] })
    } catch (e) {
      toast.error(errMsg(e, t.fail.ignore))
    }
  }
  const ignoredFooter = ignored.length > 0 && (
    <div className="px-2.5 py-1">
      <button onClick={() => setShowIgnored(!showIgnored)} aria-expanded={showIgnored} className="text-[11.5px] text-fg-3 hover:text-fg-2">
        {t.orphans.ignored(ignored.length)} · {showIgnored ? t.orphans.hide : t.orphans.view}
      </button>
      {showIgnored &&
        ignored.map((o) => (
          <div key={o.device_id + o.id} className="flex h-7 min-w-0 items-center gap-2 text-[11.5px] whitespace-nowrap text-fg-3 tabular">
            <Tip content={o.cmd} truncated={shortCmd(o.cmd) === o.cmd}>
              <code className="min-w-0 truncate font-mono text-fg-2">{shortCmd(o.cmd)}</code>
            </Tip>
            <span className="shrink-0">
              · {[o.project_name, o.ports?.length ? t.orphans.port(o.ports) : '', t.orphans.ran(duration(o.started_at, now))].filter(Boolean).join(' · ')} ·{' '}
              {t.orphans.ignoredAt(relTime(o.ignored_at, now))}
            </span>
            <span className="flex-1" />
            <button onClick={() => void unignore(o)} className="shrink-0 text-fg-2 hover:text-accent">
              {t.orphans.unignore}
            </button>
          </div>
        ))}
    </div>
  )
  // 只剩被忽略的：不出提醒块，只留一行不显眼的「已忽略 n 个 · 查看」
  if (!orphans.length && !recent.length)
    return (
      <section aria-label={t.orphans.title} className="shrink-0 pt-1">
        {ignoredFooter}
      </section>
    )

  const send = async (o: Orphan, force: boolean) => {
    const r = await unwrap(
      api.POST('/api/orphans/stop', {
        body: { device_id: o.device_id, id: o.id, force },
      }),
    )
    void qc.invalidateQueries({ queryKey: ['orphans'] })
    return r
  }
  const stop = async (o: Orphan) => {
    const ok = await confirm({
      title: t.orphans.confirmTitle,
      description: (
        <>
          <code className="block font-mono text-[12px] break-all text-fg-2">{o.cmd}</code>
          {o.state === 'detached' && <span className="mt-2 block font-medium text-wait">{t.orphans.confirmDetached}</span>}
          <span className="mt-2 block">{t.orphans.confirmBody}</span>
        </>
      ),
      confirmText: t.orphans.confirmOk,
      danger: true,
    })
    if (!ok) return
    setBusy(o.id)
    try {
      let r = await send(o, false)
      if (r.result === 'running') {
        const force = await confirm({
          title: t.orphans.forceTitle,
          description: t.orphans.forceBody(5),
          confirmText: t.orphans.forceOk,
          danger: true,
        })
        if (force) r = await send(o, true)
      }
      const msg = stopText(r)
      if (r.result === 'exited' || r.result === 'killed' || r.result === 'gone') toast.success(msg)
      else if (r.result === 'running') toast(msg)
      else toast.error(msg)
    } catch (e) {
      toast.error(errMsg(e, t.fail.stop))
    } finally {
      setBusy(null)
    }
  }

  return (
    <section aria-label={t.orphans.title} className="shrink-0">
      <div className="flex items-center gap-2 px-1 pt-2 pb-1 text-[12px] text-fg-3">
        <span className="font-medium text-fg-2">{t.orphans.title}</span>
        {orphans.length > 0 && <span className="tabular">{t.orphans.sub(nOrphan, orphans.length - nOrphan)}</span>}
        <span aria-hidden className="h-0 min-w-4 flex-1 border-t border-dashed border-line" />
      </div>
      <div className="rounded-[14px] border border-dashed border-wait/45 bg-surface px-1 py-0.5">
        <List>
          {orphans.map((o, i) => (
            <Item key={o.device_id + o.id} layout fluid>
              <OrphanRow o={o} first={i === 0} now={now} busy={busy === o.id} ignoring={pend.has(ignoreKey(o))} onStop={() => void stop(o)} onIgnore={() => ignore(o)} />
            </Item>
          ))}
        </List>
        {recent.length > 0 && (
          <div className={cx('px-2.5 py-1.5', orphans.length > 0 && 'border-t border-line')}>
            <div className="text-[11.5px] text-fg-3">{t.orphans.stopsTitle}</div>
            {recent.map((s) => (
              <StopRow key={s.id} s={s} now={now} />
            ))}
          </div>
        )}
        {ignoredFooter && <div className={cx((orphans.length > 0 || recent.length > 0) && 'border-t border-line')}>{ignoredFooter}</div>}
      </div>
    </section>
  )
}

const ignoreKey = (o: Orphan) => `ignore:${o.device_id}:${o.id}`

function OrphanRow({ o, first, now, busy, ignoring, onStop, onIgnore }: { o: Orphan; first: boolean; now: number; busy: boolean; ignoring: boolean; onStop: () => void; onIgnore: () => void }) {
  const openSession = useOpenSession()
  const detached = o.state === 'detached'
  const short = shortCmd(o.cmd)
  const meta = [o.project_name, o.ports?.length ? t.orphans.port(o.ports) : '', t.orphans.ran(duration(o.started_at, now))].filter(Boolean)
  const load = [t.orphans.cpu(o.cpu), t.orphans.mem(o.rss_kb), o.procs > 1 ? t.orphans.procs(o.procs) : ''].filter(Boolean)
  const metaText = [...meta, ...load, o.local ? '' : o.device_name].filter(Boolean).join(' · ')
  return (
    <div className={cx('relative grid h-[52px] grid-cols-[auto_minmax(0,1fr)_auto] items-center gap-3 px-2.5', !first && 'border-t border-line')}>
      <Tip content={detached ? t.orphans.detachedTip : t.orphans.orphanTip}>
        <span>
          <Badge tone={detached ? 'neutral' : 'wait'} className="w-[4.2em] justify-center">
            {detached ? t.orphans.detached : t.orphans.orphan}
          </Badge>
        </span>
      </Tip>
      <div className="min-w-0">
        <Tip content={o.cmd} truncated={short === o.cmd}>
          <code className="block truncate font-mono text-[12px] text-fg">{short}</code>
        </Tip>
        <div className="flex min-w-0 items-center gap-1.5 overflow-hidden text-[11.5px] whitespace-nowrap text-fg-3 tabular">
          <Tip content={metaText} truncated>
            <span className="min-w-0 truncate">{metaText}</span>
          </Tip>
          {o.session_key && o.session_title && (
            <>
              <span className="shrink-0">·</span>
              <Tip content={o.session_title} truncated>
                <button onClick={() => openSession(o.session_key!)} className="max-w-[8em] min-w-0 shrink-0 truncate hover:text-accent">
                  {t.orphans.from}「{o.session_title}」
                </button>
              </Tip>
            </>
          )}
        </div>
      </div>
      <div className="flex items-center gap-1">
        {!detached && (
          <Tip content={t.orphans.ignoreTip}>
            <Button size="sm" variant="ghost" disabled={ignoring || busy} onClick={onIgnore}>
              {t.orphans.ignore}
            </Button>
          </Tip>
        )}
        {o.device_online ? (
          <Button size="sm" variant="danger" loading={busy} disabled={ignoring} onClick={onStop}>
            {busy ? t.orphans.stopping : t.orphans.stop}
          </Button>
        ) : (
          <Tip content={t.orphans.offlineTip}>
            <span>
              <Button size="sm" disabled>
                {t.orphans.offline}
              </Button>
            </span>
          </Tip>
        )}
      </div>
      <PendingCover k={ignoreKey(o)} />
    </div>
  )
}

function StopRow({ s, now }: { s: Stop; now: number }) {
  const ok = s.result === 'exited' || s.result === 'killed' || s.result === 'gone'
  return (
    <div className="flex h-6 min-w-0 items-center gap-1.5 text-[11.5px] whitespace-nowrap text-fg-3 tabular">
      <span className={cx('size-1.5 shrink-0 rounded-full', ok ? 'bg-run' : s.result === 'running' ? 'bg-wait' : 'bg-danger')} />
      <Tip content={s.cmd} truncated={shortCmd(s.cmd) === s.cmd}>
        <code className="min-w-0 truncate font-mono text-fg-2">{shortCmd(s.cmd)}</code>
      </Tip>
      <span className="shrink-0">· {stopText(s)}</span>
      <span className="shrink-0">
        · {s.by === 'user' ? t.orphans.byYou : s.by} · {s.device_name} · {relTime(s.at, now)}
      </span>
    </div>
  )
}
