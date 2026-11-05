import { useMemo } from 'react'
import { useParams, useSearchParams } from 'react-router'
import { usePlans, useProjects, useRequirements, useShare, useShares } from '../api/queries'
import { editForm, newForm, ShareDetail, ShareEditor, SharesList } from '../app/shares'
import { loadPref } from '../lib/util'
import { CardSkeleton, ErrorState } from '../ui/base'
import type { ShareItem } from '../lib/share'

// 分享（R-281）：/shares 列表，/shares/new 新建（?with=R-x / P-x 预先勾好），/shares/:id 详情，/shares/:id/edit 编辑。

function NewShare() {
  const [params] = useSearchParams()
  // ?with=R-x / P-x：从需求、计划的「分享」进入（不用 r / plan，它们会打开抽屉）
  const code = params.get('with') ?? ''
  const kind = code.startsWith('P-') ? 'plan' : 'req'
  const reqs = useRequirements()
  const plans = usePlans()
  const projects = useProjects()
  const ready = !reqs.isPending && !plans.isPending && !projects.isPending
  const init = useMemo(() => {
    if (!ready) return null
    const from = code ? (kind === 'req' ? reqs.data?.find((r) => r.code === code)?.project_id : plans.data?.find((p) => p.code === code)?.project_id) : undefined
    const list = (projects.data?.projects ?? []).filter((p) => !p.hidden)
    const pref = loadPref<string>('ks.reqs.project', '')
    const project = from ?? (list.some((p) => p.id === pref) ? pref : (list[0]?.id ?? ''))
    // 预选写编号，勾选列表到了以后换成 ID（后端也认编号）
    const items: ShareItem[] = code ? [{ kind, ref: code, auto: true, excluded: false }] : []
    return newForm(project, items)
  }, [ready, code, kind, reqs.data, plans.data, projects.data])
  if (!init) return <CardSkeleton rows={2} />
  return <ShareEditor init={init} draftKey={`share:new:${code}`} preset={code || undefined} />
}

function EditShare({ id }: { id: string }) {
  const q = useShare(id)
  // 只在第一次拿到时定初始值：编辑期间分享在别处变了（比如访客链接），不重置表单
  const init = useMemo(() => (q.data ? editForm(q.data) : null), [q.data?.id])
  if (q.isError) return <ErrorState error={q.error} onRetry={() => q.refetch()} />
  if (!q.data || !init) return <CardSkeleton rows={2} />
  return <ShareEditor share={q.data} init={init} draftKey={`share:edit:${id}`} />
}

function DetailShare({ id }: { id: string }) {
  const q = useShare(id)
  if (q.isError) return <ErrorState error={q.error} onRetry={() => q.refetch()} />
  if (!q.data) return <CardSkeleton rows={2} />
  return <ShareDetail x={q.data} />
}

export default function SharesPage({ mode }: { mode: 'list' | 'new' | 'detail' | 'edit' }) {
  const { id = '' } = useParams()
  const list = useShares()
  if (mode === 'new') return <NewShare />
  if (mode === 'edit') return <EditShare id={id} />
  if (mode === 'detail') return <DetailShare id={id} />
  if (list.isError) return <ErrorState error={list.error} onRetry={() => list.refetch()} />
  return <SharesList shares={list.data ?? []} loading={list.isPending} />
}
