import { useMemo, useState, type ReactNode } from 'react'
import { useNavigate, useParams } from 'react-router'
import { ChevronLeft, ChevronRight, FileCheck2, FolderGit2, HardDrive, Merge, Plus, Settings2 } from 'lucide-react'
import { toast } from 'sonner'
import type { Requirement, Session } from '../api/client'
import { useDecisions, useProjects, useRequirements, useSessions } from '../api/queries'
import { t } from '../i18n'
import { cx, relTime, savePref, shortPath, useMedia, useNow } from '../lib/util'
import { Button, Card, CardSkeleton, Checkbox, Empty, Field, Input } from '../ui/base'
import { Menu, Modal, useConfirm } from '../ui/overlay'
import { displayState } from '../ui/marks'
import { Item, List } from '../ui/motion'
import { useActions } from '../app/actions'
import { useOpenReq } from '../app/nav'
import { NewReqModal, statusCls } from '../app/requirements'
import { SessionRow } from '../app/thread'

// 项目：左侧项目列表常驻，右侧回答“这个项目现在怎样”（D-20 选定 B 主从双栏；窄屏退化为列表 → 详情）。

type Stat = {
  id: string
  name: string
  remote: string
  docRoot: string
  workspaceId?: string
  hidden?: boolean
  sessions: Session[]
  live: Session[]
  reqs: Requirement[]
  open: Requirement[]
  review: number
  last: string
}

const order = ['in_progress', 'review', 'ready', 'blocked', 'draft'] as const

// 列表里每个项目的状态点：有人在等你 > 待命 > 运行中 > 其它。
function headline(p: Stat): string {
  const states = p.live.map(displayState)
  for (const s of ['waiting_approval', 'waiting_answer', 'waiting_reply', 'parked', 'running', 'background']) if (states.includes(s)) return s
  return p.live.length ? 'idle' : 'ended'
}

function useStats(): { list: Stat[]; loading: boolean } {
  const projects = useProjects()
  const sessions = useSessions()
  const reqs = useRequirements()
  return useMemo(() => {
    const ss = sessions.data?.sessions ?? []
    const rs = reqs.data ?? []
    const list = (projects.data?.projects ?? []).map((p) => {
      const mine = ss.filter((s) => s.project_id === p.id)
      const rq = rs.filter((r) => r.project_id === p.id)
      const open = rq.filter((r) => r.status !== 'done' && r.status !== 'shelved')
      const last = [...mine.map((s) => s.last_active_at), ...rq.map((r) => r.updated_at)].sort().at(-1) ?? p.created_at
      return {
        id: p.id,
        name: p.name,
        remote: p.remote,
        docRoot: p.doc_root,
        workspaceId: p.workspace_id,
        hidden: p.hidden,
        sessions: mine,
        live: mine.filter((s) => s.state !== 'ended' && s.state !== 'lost'),
        reqs: rq,
        open,
        review: open.filter((r) => r.status === 'review').length,
        last,
      }
    })
    list.sort((a, b) => Number(!!(b.sessions.length || b.reqs.length)) - Number(!!(a.sessions.length || a.reqs.length)) || b.last.localeCompare(a.last))
    return { list, loading: projects.isPending || sessions.isPending || reqs.isPending }
  }, [projects.data, sessions.data, reqs.data, projects.isPending, sessions.isPending, reqs.isPending])
}

function ProjectList({ list: all, current, now }: { list: Stat[]; current?: string; now: number }) {
  const navigate = useNavigate()
  const [showHidden, setShowHidden] = useState(false)
  // 隐藏的项目（误识别的目录等）默认不列，底部可以展开（R-51）
  const hidden = all.filter((p) => p.hidden)
  const list = all.filter((p) => !p.hidden || p.id === current)
  const quiet = list.filter((p) => !p.sessions.length && !p.reqs.length)
  const busy = list.filter((p) => p.sessions.length || p.reqs.length)
  const row = (p: Stat) => {
    const on = p.id === current
    const st = headline(p)
    return (
      <button
        key={p.id}
        onClick={() => navigate(`/projects/${p.id}`)}
        className={cx('flex w-full items-center gap-3 rounded-xl px-3 py-2.5 text-left transition-colors', on ? 'bg-surface shadow-xs' : 'hover:bg-hover/70')}
      >
        <span className={cx('inline-block size-[7px] shrink-0 rotate-45 rounded-[1.5px]', st === 'ended' ? 'bg-line-strong' : statusDot[st] ?? 'bg-idle')} />
        <span className="min-w-0 flex-1">
          <span className="block truncate font-display text-[15px] text-fg">{p.name}</span>
          <span className="block truncate text-xs text-fg-3">
            {t.projects.openCount(p.open.length)}
            {t.common.sep}
            {relTime(p.last, now)}
          </span>
        </span>
        {p.review > 0 && <span className="rounded-full bg-reply-soft px-2 py-0.5 text-xs font-medium text-reply tabular">{p.review}</span>}
        <ChevronRight className="size-4 shrink-0 text-fg-3 min-[900px]:hidden" />
      </button>
    )
  }
  return (
    <div className="space-y-0.5">
      {busy.map(row)}
      {quiet.length > 0 && <div className="px-3 pt-4 pb-1 text-xs text-fg-3">{t.projects.quiet}</div>}
      {quiet.map(row)}
      {hidden.length > 0 && (
        <>
          <button onClick={() => setShowHidden(!showHidden)} className="flex items-center gap-1 px-3 pt-4 pb-1 text-xs text-fg-3 hover:text-fg-2">
            <ChevronRight className={cx('size-3.5 transition-transform', showHidden && 'rotate-90')} />
            {t.projects.hiddenCount(hidden.length)}
          </button>
          {showHidden && <div className="opacity-70">{hidden.filter((p) => p.id !== current).map(row)}</div>}
        </>
      )}
      <p className="px-3 pt-4 text-xs leading-relaxed text-fg-3">{t.projects.autoHint}</p>
    </div>
  )
}

const statusDot: Record<string, string> = {
  waiting_approval: 'bg-wait',
  waiting_answer: 'bg-reply',
  waiting_reply: 'bg-reply',
  parked: 'bg-accent',
  running: 'bg-run',
  background: 'bg-accent',
  idle: 'bg-idle',
}

function Section({ title, right, children }: { title: string; right?: ReactNode; children: ReactNode }) {
  return (
    <section className="mt-7">
      <div className="mb-2 flex items-center gap-2">
        <h2 className="font-display text-base text-fg-2">{title}</h2>
        <span aria-hidden className="h-0 flex-1 border-t border-dashed border-line-strong/70" />
        {right}
      </div>
      {children}
    </section>
  )
}

function Detail({ p, now, back }: { p: Stat; now: number; back?: () => void }) {
  const openReq = useOpenReq()
  const navigate = useNavigate()
  const decisions = useDecisions()
  const [creating, setCreating] = useState(false)
  const [editing, setEditing] = useState(false)
  const counts = order.map((s) => [s, p.open.filter((r) => r.status === s).length] as const).filter(([, n]) => n)
  const done = p.reqs.filter((r) => r.status === 'done').length
  const firstReview = p.open.filter((r) => r.status === 'review').sort((a, b) => a.updated_at.localeCompare(b.updated_at))[0]
  const live = [...p.live].sort((a, b) => Number(displayState(b) === 'parked') - Number(displayState(a) === 'parked') || b.last_active_at.localeCompare(a.last_active_at))

  // 最近进展：跨会话汇总这个项目的提交验收、退回、留言、交出与决策答复。
  const recent = useMemo(() => {
    const out: { at: string; code: string; text: string; dot: string }[] = []
    for (const r of p.reqs) {
      for (const e of r.evidence ?? []) if (!e.voided) out.push({ at: e.created_at, code: r.code, text: t.projects.evEvidence(r.code, r.title), dot: 'bg-run' })
      for (const f of r.feedback ?? []) {
        const at = f.answered_at ?? f.created_at
        if (f.kind === 'assign') out.push({ at, code: r.code, text: t.projects.evAssign(r.code, r.title), dot: 'bg-accent' })
        else if (f.kind === 'note') out.push({ at, code: r.code, text: t.projects.evNote(r.code, f.answer ?? ''), dot: 'bg-accent' })
        else out.push({ at, code: r.code, text: t.projects.evRevise(r.code, f.answer ?? ''), dot: 'bg-reply' })
      }
      if (r.status === 'done') out.push({ at: r.updated_at, code: r.code, text: t.projects.evDone(r.code, r.title), dot: 'bg-run' })
    }
    for (const d of decisions.data ?? [])
      if (d.project_id === p.id && d.kind === 'decision' && d.answered_at) out.push({ at: d.answered_at, code: d.requirement_code ?? '', text: t.projects.evAnswer(d.code, d.short_title || d.question), dot: 'bg-wait' })
    return out.sort((a, b) => b.at.localeCompare(a.at)).slice(0, 8)
  }, [p.reqs, p.id, decisions.data])

  // 本机路径：按设备汇总会话所在目录。
  const paths = useMemo(() => {
    const m = new Map<string, Set<string>>()
    for (const s of p.sessions) {
      if (!s.cwd) continue
      const set = m.get(s.device_name) ?? new Set()
      set.add(s.cwd)
      m.set(s.device_name, set)
    }
    return [...m.entries()]
  }, [p.sessions])

  return (
    <div className="min-w-0">
      {back && (
        <button onClick={back} className="-ml-1 mb-3 flex items-center gap-1 text-sm text-fg-3 hover:text-fg">
          <ChevronLeft className="size-4" />
          {t.projects.title}
        </button>
      )}
      <div className="flex flex-wrap items-start gap-3">
        <div className="min-w-0 flex-1">
          <h1 className="truncate font-display text-[26px] text-fg lg:text-[30px]">{p.name}</h1>
          <div className="mt-1 truncate font-mono text-xs text-fg-3">
            {p.remote || t.settings.projects.local}
            {p.docRoot && `${t.common.sep}${t.projects.docRoot} ${p.docRoot}`}
          </div>
        </div>
        <div className="flex gap-2">
          <Button icon={<Settings2 className="size-4" />} onClick={() => setEditing(true)}>
            <span className="max-sm:hidden">{t.projects.settings}</span>
          </Button>
          <Button variant="primary" icon={<Plus className="size-4" />} onClick={() => setCreating(true)}>
            {t.projects.newReq}
          </Button>
        </div>
      </div>

      <Section title={t.projects.now}>
        {live.length ? (
          <Card className="@container p-1.5">
            <List>
              {live.map((s) => (
                <Item key={s.key}>
                  <SessionRow s={s} now={now} />
                </Item>
              ))}
            </List>
          </Card>
        ) : (
          <p className="text-sm text-fg-3">{t.projects.noLive}</p>
        )}
      </Section>

      <Section
        title={t.projects.reqs}
        right={
          <button
            onClick={() => {
              savePref('ks.reqs.project', p.id)
              navigate('/requirements')
            }}
            className="text-xs text-fg-3 hover:text-accent"
          >
            {t.projects.allReqs}
          </button>
        }
      >
        <Card className="p-4">
          {p.open.length ? (
            <>
              <div className="flex h-2 overflow-hidden rounded-full bg-sunken">
                {counts.map(([s, n]) => (
                  <span key={s} className={cx('h-full transition-[width] duration-500', statusCls[s].dot)} style={{ width: `${(n / p.open.length) * 100}%` }} />
                ))}
              </div>
              <div className="mt-3 flex flex-wrap items-center gap-x-4 gap-y-2 text-sm">
                {counts.map(([s, n]) => (
                  <span key={s} className="flex items-center gap-1.5 text-fg-2">
                    <span className={cx('size-2 rounded-full', statusCls[s].dot)} />
                    {t.reqStatus[s]} <span className="tabular">{n}</span>
                  </span>
                ))}
                <span className="ml-auto text-xs text-fg-3">{t.projects.doneCount(done)}</span>
              </div>
              {firstReview && (
                <Button className="mt-3" variant="soft" icon={<FileCheck2 className="size-4" />} onClick={() => openReq(firstReview.code)}>
                  {t.projects.reviewAll(p.review)}
                </Button>
              )}
            </>
          ) : (
            <p className="text-sm text-fg-3">{t.projects.noOpen(done)}</p>
          )}
        </Card>
      </Section>

      <Section title={t.projects.recent}>
        {recent.length ? (
          <Card className="divide-y divide-dashed divide-line px-4 py-1">
            {recent.map((e, i) => (
              <button key={i} onClick={() => e.code && openReq(e.code)} className="flex w-full items-center gap-3 py-2.5 text-left text-sm text-fg-2 hover:text-fg">
                <span className={cx('inline-block size-[7px] shrink-0 rotate-45 rounded-[1.5px]', e.dot)} />
                <span className="min-w-0 flex-1 truncate">{e.text}</span>
                <span className="shrink-0 text-xs text-fg-3 tabular">{relTime(e.at, now)}</span>
              </button>
            ))}
          </Card>
        ) : (
          <p className="text-sm text-fg-3">{t.projects.noRecent}</p>
        )}
      </Section>

      {paths.length > 0 && (
        <Section title={t.projects.paths}>
          <Card className="divide-y divide-line">
            {paths.map(([dev, set]) => (
              <div key={dev} className="flex items-start gap-3 px-4 py-2.5">
                <HardDrive className="mt-0.5 size-4 shrink-0 text-fg-3" />
                <span className="w-28 shrink-0 truncate text-sm text-fg-2">{dev}</span>
                <span className="min-w-0 flex-1 space-y-0.5 font-mono text-xs text-fg-3">
                  {[...set].map((c) => (
                    <span key={c} className="block truncate" title={c}>
                      {shortPath(c, 72)}
                    </span>
                  ))}
                </span>
              </div>
            ))}
          </Card>
        </Section>
      )}

      <NewReqModal open={creating} onClose={() => setCreating(false)} defaultProject={p.id} />
      <ProjectSettings p={p} open={editing} onClose={() => setEditing(false)} />
    </div>
  )
}

function ProjectSettings({ p, open, onClose }: { p: Stat; open: boolean; onClose: () => void }) {
  const projects = useProjects()
  const { updateProject } = useActions()
  const [name, setName] = useState(p.name)
  const [doc, setDoc] = useState(p.docRoot)
  const [ws, setWs] = useState(p.workspaceId ?? '')
  const workspaces = projects.data?.workspaces ?? []
  const [auto, setAuto] = useState(projects.data?.projects.find((x) => x.id === p.id)?.auto_dispatch ?? true)
  const [hidden, setHidden] = useState(!!p.hidden)
  const { mergeProject } = useActions()
  const confirm = useConfirm()
  const navigate = useNavigate()
  const others = (projects.data?.projects ?? []).filter((x) => x.id !== p.id)
  const merge = async (into: { id: string; name: string }) => {
    if (!(await confirm({ title: t.projects.mergeConfirm(p.name, into.name), description: t.projects.mergeBody, confirmText: t.projects.merge, danger: true }))) return
    if (await mergeProject(p.id, into.id)) {
      toast.success(t.projects.merged(p.name, into.name))
      onClose()
      navigate(`/projects/${into.id}`)
    }
  }
  return (
    <Modal
      open={open}
      onClose={onClose}
      title={t.projects.settings}
      footer={
        <>
          <Button onClick={onClose}>{t.common.cancel}</Button>
          <Button variant="primary" disabled={!name.trim()} onClick={async () => (await updateProject(p.id, { name: name.trim(), doc_root: doc, workspace_id: ws || undefined, auto_dispatch: auto, hidden })) && onClose()}>
            {t.common.save}
          </Button>
        </>
      }
    >
      <div className="space-y-4">
        <Field label={t.settings.projects.name}>
          <Input value={name} onChange={(e) => setName(e.target.value)} />
        </Field>
        <Field label={t.settings.projects.docRoot}>
          <Input value={doc} placeholder={t.settings.projects.docRootPlaceholder} onChange={(e) => setDoc(e.target.value)} className="font-mono" />
        </Field>
        <Field label={t.settings.projects.workspace}>
          <Menu
            align="start"
            trigger={<Button className="w-full justify-start">{workspaces.find((w) => w.id === ws)?.name ?? t.settings.projects.noWorkspace}</Button>}
            items={[{ label: t.settings.projects.noWorkspace, checked: !ws, onSelect: () => setWs('') }, ...workspaces.map((w) => ({ label: w.name, checked: w.id === ws, onSelect: () => setWs(w.id) }))]}
          />
        </Field>
        <Checkbox checked={auto} onChange={setAuto} hint={t.projects.autoDispatchHint}>
          {t.projects.autoDispatch}
        </Checkbox>
        <Checkbox checked={hidden} onChange={setHidden} hint={t.projects.hideHint}>
          {t.projects.hide}
        </Checkbox>
        {others.length > 0 && (
          <Field label={t.projects.mergeLabel}>
            <Menu
              align="start"
              width="min-w-[260px]"
              trigger={
                <Button className="w-full justify-start" icon={<Merge className="size-4" />}>
                  {t.projects.mergePick}
                </Button>
              }
              items={others.map((x) => ({ label: x.name, hint: x.remote, onSelect: () => void merge(x) }))}
            />
            <p className="mt-1.5 text-xs leading-relaxed text-fg-3">{t.projects.mergeHint}</p>
          </Field>
        )}
      </div>
    </Modal>
  )
}

export default function ProjectsPage() {
  const { id } = useParams()
  const navigate = useNavigate()
  const now = useNow()
  const wide = useMedia('(min-width: 900px)')
  const { list, loading } = useStats()
  const cur = list.find((p) => p.id === id) ?? (wide ? list[0] : undefined)

  if (loading) return <CardSkeleton rows={3} />
  if (!list.length)
    return (
      <Card>
        <Empty icon={<FolderGit2 />} title={t.settings.projects.empty}>
          {t.settings.projects.emptySub}
        </Empty>
      </Card>
    )

  if (!wide)
    return cur && id ? (
      <Detail key={cur.id} p={cur} now={now} back={() => navigate('/projects')} />
    ) : (
      <>
        <h1 className="mb-4 font-display text-[24px] text-fg">{t.projects.title}</h1>
        <ProjectList list={list} now={now} />
      </>
    )

  return (
    <div className="flex gap-6 lg:-my-2">
      <aside className="sticky top-6 w-64 shrink-0 self-start">
        <h1 className="mb-3 px-3 font-display text-[24px] text-fg">{t.projects.title}</h1>
        <ProjectList list={list} current={cur?.id} now={now} />
      </aside>
      <div className="min-w-0 flex-1 border-l border-line pl-6">{cur && <Detail key={cur.id} p={cur} now={now} />}</div>
    </div>
  )
}
