import { useState } from 'react'
import { useQuery, useQueryClient } from '@tanstack/react-query'
import { toast } from 'sonner'
import { api, errMsg, unwrap, type Schemas } from '../api/client'
import { t } from '../i18n'
import { cx, duration, relTime } from '../lib/util'
import { Badge, Button, Tip } from '../ui/base'
import { useConfirm } from '../ui/overlay'
import { useOpenSession } from './nav'

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
    select: (d) => ({ orphans: d.orphans ?? [], stops: d.stops ?? [] }),
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

export function OrphanProcs({ now }: { now: number }) {
  const q = useOrphans()
  const qc = useQueryClient()
  const confirm = useConfirm()
  const [busy, setBusy] = useState<string | null>(null)
  const orphans = q.data?.orphans ?? []
  const recent = (q.data?.stops ?? []).filter((s) => now - new Date(s.at).getTime() < RECENT_MS).slice(0, 3)
  if (!orphans.length && !recent.length) return null
  const nOrphan = orphans.filter((o) => o.state === 'orphan').length

  const send = async (o: Orphan, force: boolean) => {
    const r = await unwrap(api.POST('/api/orphans/stop', { body: { device_id: o.device_id, id: o.id, force } }))
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
        const force = await confirm({ title: t.orphans.forceTitle, description: t.orphans.forceBody(5), confirmText: t.orphans.forceOk, danger: true })
        if (force) r = await send(o, true)
      }
      const msg = stopText(r)
      if (r.result === 'exited' || r.result === 'killed' || r.result === 'gone') toast.success(msg)
      else if (r.result === 'running') toast(msg)
      else toast.error(msg)
    } catch (e) {
      toast.error(errMsg(e))
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
        {orphans.map((o) => (
          <OrphanRow key={o.device_id + o.id} o={o} now={now} busy={busy === o.id} onStop={() => void stop(o)} />
        ))}
        {recent.length > 0 && (
          <div className={cx('px-2.5 py-1.5', orphans.length > 0 && 'border-t border-line')}>
            <div className="text-[11.5px] text-fg-3">{t.orphans.stopsTitle}</div>
            {recent.map((s) => (
              <StopRow key={s.id} s={s} now={now} />
            ))}
          </div>
        )}
      </div>
    </section>
  )
}

function OrphanRow({ o, now, busy, onStop }: { o: Orphan; now: number; busy: boolean; onStop: () => void }) {
  const openSession = useOpenSession()
  const detached = o.state === 'detached'
  const short = shortCmd(o.cmd)
  const meta = [
    o.project_name,
    o.ports?.length ? t.orphans.port(o.ports) : '',
    t.orphans.ran(duration(o.started_at, now)),
  ].filter(Boolean)
  const load = [t.orphans.cpu(o.cpu), t.orphans.mem(o.rss_kb), o.procs > 1 ? t.orphans.procs(o.procs) : ''].filter(Boolean)
  const metaText = [...meta, ...load, o.local ? '' : o.device_name].filter(Boolean).join(' · ')
  return (
    <div className="grid h-[52px] grid-cols-[auto_minmax(0,1fr)_auto] items-center gap-3 border-t border-line px-2.5 first:border-t-0">
      <Tip content={detached ? t.orphans.detachedTip : t.orphans.orphanTip}>
        <span>
          <Badge tone={detached ? 'neutral' : 'wait'} className="w-[4.2em] justify-center">{detached ? t.orphans.detached : t.orphans.orphan}</Badge>
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
      {o.device_online ? (
        <Button size="sm" variant="danger" loading={busy} onClick={onStop}>
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
