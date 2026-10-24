import { useSessions } from '../api/queries'
import { t } from '../i18n'
import { actorOf } from '../lib/proofs'
import { cx } from '../lib/util'
import { Tip } from '../ui/base'
import { RoleTag } from '../ui/marks'
import { useOpenSession } from './nav'
import { sessionTitle } from './SessionCard'

// Who：时间线上一条记录是谁做的（R-195）——「身份胶囊 + 会话标题」，点开就是那个会话。
// 标题用动作发生时记下的快照，悬停看会话现在的标题；快照为空（老数据）时用现在的标题。
// 老数据没记录是哪个会话的写「Agent（没记录是哪个会话）」；你在网页上做的写「你」，纸鸢自动做的写「纸鸢」。
export function Who({ actor, session, role, title, className }: { actor?: string | null; session?: string | null; role?: string | null; title?: string | null; className?: string }) {
  const sessions = useSessions()
  const openSession = useOpenSession()
  const a = actorOf({ actor, session, role, title })
  if (a.kind === 'user') return <b className={cx('shrink-0 font-medium text-fg', className)}>{t.who.user}</b>
  if (a.kind === 'system') return <span className={cx('shrink-0 font-medium text-fg-2', className)}>{t.who.system}</span>
  if (a.kind === 'unknown')
    return (
      <span className={cx('inline-flex min-w-0 items-center gap-1', className)}>
        {a.role && <RoleTag role={a.role} />}
        <span className="truncate text-fg-3">{t.who.unknown}</span>
      </span>
    )
  const s = (sessions.data?.sessions ?? []).find((x) => x.key === a.key)
  const now = s ? sessionTitle(s) : ''
  const shown = a.title || now || t.who.gone
  const r = a.role || s?.role || ''
  const tip = a.title && now && now !== a.title ? t.who.now(now) : s ? [shown, s.device_name].filter(Boolean).join(t.common.sep) : t.who.gone
  return (
    <span className={cx('inline-flex min-w-0 items-center gap-1', className)}>
      {r && <RoleTag role={r} />}
      <Tip content={tip}>
        <button
          type="button"
          disabled={!s}
          onClick={(e) => {
            e.stopPropagation()
            if (s) openSession(a.key)
          }}
          className="min-w-0 truncate border-b border-dashed border-line-strong text-left font-medium text-fg-2 transition hover:border-accent hover:text-accent disabled:cursor-default disabled:border-transparent disabled:text-fg-3"
        >
          {shown}
        </button>
      </Tip>
    </span>
  )
}
