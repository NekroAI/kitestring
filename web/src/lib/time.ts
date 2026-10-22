import { t } from '../i18n'

// relTime 把时间格式化为“x 分钟前”。
export function relTime(iso?: string | null, now = Date.now()): string {
  if (!iso) return t.time.never
  const ts = new Date(iso).getTime()
  if (Number.isNaN(ts) || ts <= 0) return t.time.never
  const s = Math.max(0, Math.round((now - ts) / 1000))
  if (s < 10) return t.time.justNow
  if (s < 60) return t.time.secondsAgo(s)
  const m = Math.floor(s / 60)
  if (m < 60) return t.time.minutesAgo(m)
  const h = Math.floor(m / 60)
  if (h < 48) return t.time.hoursAgo(h)
  return t.time.daysAgo(Math.floor(h / 24))
}

// absTime 把时间格式化为本地日期时间。
export function absTime(iso?: string | null): string {
  if (!iso) return t.time.never
  const d = new Date(iso)
  if (Number.isNaN(d.getTime())) return t.time.never
  return d.toLocaleString(undefined, { month: '2-digit', day: '2-digit', hour: '2-digit', minute: '2-digit', hour12: false })
}
