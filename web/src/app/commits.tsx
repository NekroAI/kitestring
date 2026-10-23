import { useQuery } from '@tanstack/react-query'
import { ExternalLink, GitCommitHorizontal, Undo2 } from 'lucide-react'
import { api, unwrap, type Schemas } from '../api/client'
import { t } from '../i18n'
import { cx } from '../lib/util'
import { Badge } from '../ui/base'

// 需求关联的提交、回滚与 CI（R-156）。纸鸢只记录与展示：不 revert / push，也不触发 CI。

export type Commit = Schemas['ChainCommit']

export function useReqCommits(code: string) {
  return useQuery({
    queryKey: ['requirements', 'commits', code],
    queryFn: () => unwrap(api.GET('/api/requirements/{ref}/commits', { params: { path: { ref: code } } })),
    staleTime: 30_000,
  })
}

export const shortSha = (s: string) => (s.length > 12 && /^[0-9a-f]+$/i.test(s) ? s.slice(0, 7) : s)

const ciTone = { pending: 'wait', success: 'run', failure: 'danger' } as const

// CommitTags：一个提交的回滚与 CI 标记。all 是同一需求的全部提交，用来判断「回滚已撤销」
export function CommitTags({ c, all }: { c: Commit; all?: Commit[] }) {
  const undone = !!c.reverted_by && !!all?.some((x) => x.link === c.reverted_by && x.reverted_by)
  return (
    <>
      {c.reverts && <Badge tone="reply">{t.commits.reverts(shortSha(c.reverts))}</Badge>}
      {c.reverted_by && (
        <Badge tone={undone ? 'neutral' : 'danger'} icon={<Undo2 className="size-3" />}>
          {undone ? t.commits.restored : t.commits.revertedBy(shortSha(c.reverted_by))}
        </Badge>
      )}
      {c.ci && (
        <Badge tone={ciTone[c.ci.state]}>
          {c.ci.url ? (
            <a href={c.ci.url} target="_blank" rel="noreferrer" className="inline-flex items-center gap-1 hover:underline">
              {t.commits.ci[c.ci.state]}
              <ExternalLink className="size-3" />
            </a>
          ) : (
            t.commits.ci[c.ci.state]
          )}
        </Badge>
      )}
    </>
  )
}

// CommitLine：一行提交（哈希、标题、标记），用在后果链里
export function CommitLine({ c, all, className }: { c: Commit; all?: Commit[]; className?: string }) {
  return (
    <div className={cx('flex min-w-0 flex-wrap items-center gap-x-1.5 gap-y-1 text-xs text-fg-3', className)}>
      {c.reverts ? <Undo2 className="size-3.5 shrink-0" /> : <GitCommitHorizontal className="size-3.5 shrink-0" />}
      {c.link && <Badge mono>{shortSha(c.link)}</Badge>}
      <span className={cx('min-w-0 truncate', c.reverted_by && 'line-through decoration-fg-3/60')}>{c.summary}</span>
      <CommitTags c={c} all={all} />
    </div>
  )
}
