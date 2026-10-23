import { useEffect, useState, useSyncExternalStore } from 'react'
import { Undo2 } from 'lucide-react'
import { t } from '../i18n'
import { Button } from '../ui/base'
import { RefChip } from '../ui/refmark'

// 就地通过（R-114）：点了先进入 5 秒撤销期，到时才真正通过；状态放在模块里，离开工作台也照常生效。

const passDelay = 5000
const passing = new Map<string, { until: number; timer: number }>()
const subs = new Set<() => void>()
let ver = 0
const emit = () => {
  ver++
  subs.forEach((f) => f())
}

export function usePassing() {
  useSyncExternalStore(
    (f) => {
      subs.add(f)
      return () => subs.delete(f)
    },
    () => ver,
  )
  return passing
}

export function schedulePass(code: string, commit: () => Promise<unknown>) {
  if (passing.has(code)) return
  const timer = window.setTimeout(() => {
    passing.delete(code)
    emit()
    void commit()
  }, passDelay)
  passing.set(code, { until: Date.now() + passDelay, timer })
  emit()
}

export function undoPass(code: string) {
  const p = passing.get(code)
  if (!p) return
  window.clearTimeout(p.timer)
  passing.delete(code)
  emit()
}

function Countdown({ until }: { until: number }) {
  const [, tick] = useState(0)
  useEffect(() => {
    const id = window.setInterval(() => tick((n) => n + 1), 250)
    return () => window.clearInterval(id)
  }, [])
  return <>{t.desk.passing(Math.max(1, Math.ceil((until - Date.now()) / 1000)))}</>
}

// PassingRow：撤销期里这一行显示倒计时和撤销。
export function PassingRow({ code, until, className }: { code: string; until: number; className?: string }) {
  return (
    <div className={className ?? 'flex items-center gap-3 rounded-xl bg-run-soft/60 px-3 py-2.5'}>
      <RefChip code={code} status="done" link={false} />
      <span className="min-w-0 flex-1 truncate text-sm text-run">
        <Countdown until={until} />
      </span>
      <Button size="sm" variant="ghost" icon={<Undo2 className="size-3.5" />} onClick={() => undoPass(code)}>
        {t.desk.undo}
      </Button>
    </div>
  )
}
