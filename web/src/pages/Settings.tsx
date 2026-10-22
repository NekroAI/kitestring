import { useEffect, useState, type ReactNode } from 'react'
import { useSearchParams } from 'react-router'
import { motion } from 'motion/react'
import { Bell, Check, CheckCircle2, ChevronDown, Copy, FolderGit2, KeyRound, LogOut, Monitor, MonitorSmartphone, Moon, Palette, Pencil, Plus, Send, Sparkles, Sun, SunMoon, Trash2, User, XCircle } from 'lucide-react'
import { toast } from 'sonner'
import { api, errMsg, unwrap, type Device, type NotifyConfig, type Workspace } from '../api/client'
import { useDevices, useProjects, useSettings } from '../api/queries'
import { t } from '../i18n'
import { copyText, cx, relTime, useNow } from '../lib/util'
import { useTheme, type ThemePref } from '../lib/theme'
import { Badge, Button, Card, Checkbox, Empty, ErrorState, Field, Input, PageHeader, Segmented, Skeleton } from '../ui/base'
import { Menu, Modal, useConfirm } from '../ui/overlay'
import { useActions, useBusy } from '../app/actions'
import { useQueryClient } from '@tanstack/react-query'
import { qk } from '../api/queries'

type Tab = 'devices' | 'notify' | 'projects' | 'summary' | 'appearance' | 'account'
const tabs: { id: Tab; icon: typeof Monitor }[] = [
  { id: 'devices', icon: Monitor },
  { id: 'notify', icon: Bell },
  { id: 'projects', icon: FolderGit2 },
  { id: 'summary', icon: Sparkles },
  { id: 'appearance', icon: Palette },
  { id: 'account', icon: User },
]

function Intro({ children }: { children: ReactNode }) {
  return <p className="mb-5 max-w-2xl text-sm leading-relaxed text-fg-2">{children}</p>
}

function Row({ children, className }: { children: ReactNode; className?: string }) {
  return <div className={cx('flex items-center gap-3 px-4 py-3.5', className)}>{children}</div>
}

// ---------- 设备 ----------

function CommandLine({ cmd }: { cmd: string }) {
  return (
    <div className="flex items-center gap-2 rounded-lg bg-sunken px-3 py-2">
      <code className="min-w-0 flex-1 overflow-x-auto font-mono text-[12px] whitespace-nowrap text-fg">{cmd}</code>
      <Button size="icon-sm" variant="ghost" aria-label={t.common.copy} onClick={async () => (await copyText(cmd)) && toast.success(t.common.copied)}>
        <Copy className="size-3.5" />
      </Button>
    </div>
  )
}

function DeviceTokenModal({ token, name, onClose }: { token: string; name: string; onClose: () => void }) {
  const origin = window.location.origin
  return (
    <Modal
      open={!!token}
      onClose={onClose}
      title={`${t.settings.devices.created} · ${name}`}
      description={t.settings.devices.tokenOnce}
      footer={
        <Button variant="primary" onClick={onClose}>
          {t.common.close}
        </Button>
      }
    >
      <div className="space-y-4">
        <CommandLine cmd={token} />
        <div className="text-sm font-medium text-fg">{t.settings.devices.stepsTitle}</div>
        <ol className="space-y-3 text-sm text-fg-2">
          {[
            [t.settings.devices.step1, null],
            [t.settings.devices.step2, `ks login --hub ${origin} --token ${token}`],
            [t.settings.devices.step3, 'ks install'],
            [t.settings.devices.step4, null],
          ].map(([text, cmd], i) => (
            <li key={i} className="flex gap-3">
              <span className="flex size-5 shrink-0 items-center justify-center rounded-full bg-accent-soft text-[11px] font-semibold text-accent">{i + 1}</span>
              <div className="min-w-0 flex-1 space-y-1.5">
                <div>{text}</div>
                {cmd && <CommandLine cmd={cmd} />}
              </div>
            </li>
          ))}
        </ol>
      </div>
    </Modal>
  )
}

function Devices() {
  const now = useNow()
  const q = useDevices()
  const { createDevice, revokeDevice } = useActions()
  const confirm = useConfirm()
  const [adding, setAdding] = useState(false)
  const [name, setName] = useState('')
  const [busy, wrap] = useBusy()
  const [created, setCreated] = useState<{ token: string; name: string } | null>(null)

  const add = () =>
    wrap(async () => {
      const r = await createDevice(name.trim())
      if (r) {
        setAdding(false)
        setName('')
        setCreated({ token: r.token, name: r.device.name })
      }
    })
  const revoke = async (d: Device) => {
    if (await confirm({ title: t.settings.devices.revoke, description: t.settings.devices.revokeConfirm(d.name), confirmText: t.settings.devices.revoke, danger: true })) {
      if (await revokeDevice(d.id)) toast(t.settings.devices.revoked)
    }
  }

  return (
    <>
      <Intro>{t.settings.devices.intro}</Intro>
      <Card className="divide-y divide-line overflow-hidden">
        {q.isPending ? (
          <Row>
            <Skeleton className="h-10 w-full" />
          </Row>
        ) : q.isError ? (
          <div className="p-4">
            <ErrorState error={q.error} />
          </div>
        ) : !q.data.length ? (
          <Empty icon={<MonitorSmartphone />} title={t.settings.devices.empty} />
        ) : (
          q.data.map((d) => (
            <Row key={d.id} className={cx(d.revoked && 'opacity-55')}>
              <span className="relative flex size-9 shrink-0 items-center justify-center rounded-xl bg-sunken text-fg-2">
                <Monitor className="size-[18px]" />
                {!d.revoked && <span className={cx('absolute -right-0.5 -bottom-0.5 size-2.5 rounded-full border-2 border-surface', d.online ? 'bg-run' : 'bg-fg-3/50')} />}
              </span>
              <div className="min-w-0 flex-1">
                <div className="flex items-center gap-2 text-sm font-medium text-fg">
                  {d.name}
                  {d.revoked && <Badge tone="danger">{t.settings.devices.revokedTag}</Badge>}
                </div>
                <div className="mt-0.5 truncate text-xs text-fg-3">
                  {[d.online ? t.sessions.deviceOnline : d.last_seen ? t.sessions.lastSeen(relTime(d.last_seen, now)) : t.sessions.neverConnected, d.hostname, d.platform, d.version && `v${d.version}`].filter(Boolean).join(t.common.sep)}
                </div>
              </div>
              {!d.revoked && (
                <Button size="sm" variant="ghost" className="text-fg-3 hover:text-danger" onClick={() => revoke(d)}>
                  {t.settings.devices.revoke}
                </Button>
              )}
            </Row>
          ))
        )}
        <Row className="bg-surface-2/50">
          {adding ? (
            <div className="flex w-full flex-wrap gap-2">
              <Input autoFocus value={name} onChange={(e) => setName(e.target.value)} placeholder={t.settings.devices.namePlaceholder} onKeyDown={(e) => e.key === 'Enter' && name.trim() && add()} className="min-w-[200px] flex-1" />
              <Button onClick={() => setAdding(false)}>{t.common.cancel}</Button>
              <Button variant="primary" loading={busy} disabled={!name.trim()} onClick={add}>
                {t.common.create}
              </Button>
            </div>
          ) : (
            <Button variant="ghost" icon={<Plus className="size-4" />} onClick={() => setAdding(true)}>
              {t.settings.devices.add}
            </Button>
          )}
        </Row>
      </Card>
      {created && <DeviceTokenModal token={created.token} name={created.name} onClose={() => setCreated(null)} />}
    </>
  )
}

// ---------- 通知 ----------

function channelsOf(n: NotifyConfig): string[] {
  const out: string[] = []
  if (n.bark?.key) out.push(t.settings.notify.bark)
  if (n.wecom?.webhook) out.push(t.settings.notify.wecom)
  return out
}

function NotifyForm({ value, onChange, events }: { value: NotifyConfig; onChange: (v: NotifyConfig) => void; events: string[] }) {
  const [busy, wrap] = useBusy()
  const chosen = value.events ?? []
  const test = () =>
    wrap(async () => {
      try {
        await unwrap(api.POST('/api/notify/test', { body: value }))
        toast.success(t.settings.notify.testOk)
      } catch (e) {
        toast.error(errMsg(e))
      }
    })
  return (
    <div className="space-y-5">
      <div className="grid gap-4 sm:grid-cols-2">
        <Field label={t.settings.notify.barkKey} hint={t.settings.notify.barkKeyHint}>
          <Input value={value.bark?.key ?? ''} onChange={(e) => onChange({ ...value, bark: { server: value.bark?.server ?? '', key: e.target.value } })} className="font-mono" />
        </Field>
        <Field label={t.settings.notify.barkServer}>
          <Input value={value.bark?.server ?? ''} placeholder="https://api.day.app" onChange={(e) => onChange({ ...value, bark: { key: value.bark?.key ?? '', server: e.target.value } })} className="font-mono" />
        </Field>
      </div>
      <Field label={t.settings.notify.wecomWebhook} hint={t.settings.notify.wecomHint}>
        <Input value={value.wecom?.webhook ?? ''} placeholder="https://qyapi.weixin.qq.com/cgi-bin/webhook/send?key=…" onChange={(e) => onChange({ ...value, wecom: { webhook: e.target.value } })} className="font-mono" />
      </Field>
      <div>
        <div className="mb-1 text-[13px] font-medium text-fg-2">{t.settings.notify.events}</div>
        <div className="grid gap-x-4 sm:grid-cols-2">
          {events.map((ev, i) => (
            <Checkbox
              key={ev}
              checked={chosen.length ? chosen.includes(ev) : i < 3}
              hint={t.settings.notify.evHint[ev]}
              onChange={(on) => {
                const base = chosen.length ? chosen : events.slice(0, 3)
                onChange({ ...value, events: on ? [...base, ev] : base.filter((x) => x !== ev) })
              }}
            >
              {t.settings.notify.ev[ev] ?? ev}
            </Checkbox>
          ))}
        </div>
      </div>
      <Button size="sm" icon={<Send className="size-3.5" />} loading={busy} disabled={!channelsOf(value).length} onClick={test}>
        {t.settings.notify.test}
      </Button>
    </div>
  )
}

function WorkspaceModal({ ws, onClose, events }: { ws: Workspace | 'new' | null; onClose: () => void; events: string[] }) {
  const { createWorkspace, updateWorkspace } = useActions()
  const [busy, wrap] = useBusy()
  const [name, setName] = useState('')
  const [n, setN] = useState<NotifyConfig>({})
  useEffect(() => {
    if (ws && ws !== 'new') {
      setName(ws.name)
      setN(ws.notify)
    } else {
      setName('')
      setN({})
    }
  }, [ws])
  const save = () =>
    wrap(async () => {
      const ok = ws === 'new' ? await createWorkspace(name.trim(), n) : ws && (await updateWorkspace(ws.id, name.trim(), n))
      if (ok) {
        toast.success(t.common.saved)
        onClose()
      }
    })
  return (
    <Modal
      open={!!ws}
      onClose={onClose}
      title={ws === 'new' ? t.settings.notify.addWorkspace : name}
      footer={
        <>
          <Button onClick={onClose}>{t.common.cancel}</Button>
          <Button variant="primary" loading={busy} disabled={!name.trim()} onClick={save}>
            {t.common.save}
          </Button>
        </>
      }
    >
      <div className="space-y-5">
        <Field label={t.settings.notify.workspaceName}>
          <Input autoFocus value={name} placeholder={t.settings.notify.workspaceNamePlaceholder} onChange={(e) => setName(e.target.value)} />
        </Field>
        <NotifyForm value={n} onChange={setN} events={events} />
      </div>
    </Modal>
  )
}

function Notify() {
  const settings = useSettings()
  const projects = useProjects()
  const { deleteWorkspace } = useActions()
  const confirm = useConfirm()
  const qc = useQueryClient()
  const [editing, setEditing] = useState<Workspace | 'new' | null>(null)
  const [def, setDef] = useState<NotifyConfig | null>(null)
  const [busy, wrap] = useBusy()
  useEffect(() => {
    if (settings.data && !def) setDef(settings.data.notify_default)
  }, [settings.data, def])
  const events = settings.data?.notify_events ?? []

  const saveDefault = () =>
    wrap(async () => {
      const s = settings.data!
      try {
        await unwrap(api.PUT('/api/settings', { body: { public_url: s.public_url, summary_mode: s.summary_mode, summary_base_url: s.summary_base_url, summary_model: s.summary_model, notify_default: def ?? {} } }))
        void qc.invalidateQueries({ queryKey: qk.settings })
        toast.success(t.common.saved)
      } catch (e) {
        toast.error(errMsg(e))
      }
    })

  return (
    <>
      <Intro>{t.settings.notify.intro}</Intro>
      <h3 className="mb-1 text-[15px] font-semibold text-fg">{t.settings.notify.workspacesTitle}</h3>
      <p className="mb-3 text-[13px] text-fg-3">{t.settings.notify.workspacesHint}</p>
      <Card className="mb-8 divide-y divide-line overflow-hidden">
        {(projects.data?.workspaces ?? []).map((w) => {
          const ch = channelsOf(w.notify)
          const n = projects.data?.projects.filter((p) => p.workspace_id === w.id).length ?? 0
          return (
            <Row key={w.id}>
              <div className="min-w-0 flex-1">
                <div className="text-sm font-medium text-fg">{w.name}</div>
                <div className="mt-0.5 text-xs text-fg-3">
                  {n} · {ch.length ? ch.join('、') : t.settings.notify.noChannel}
                </div>
              </div>
              <Button size="icon-sm" variant="ghost" aria-label={t.common.edit} onClick={() => setEditing(w)}>
                <Pencil className="size-3.5" />
              </Button>
              <Button
                size="icon-sm"
                variant="ghost"
                aria-label={t.common.delete}
                onClick={async () => (await confirm({ title: t.common.delete, description: t.settings.notify.deleteConfirm(w.name), danger: true, confirmText: t.common.delete })) && deleteWorkspace(w.id)}
              >
                <Trash2 className="size-3.5" />
              </Button>
            </Row>
          )
        })}
        <Row className="bg-surface-2/50">
          <Button variant="ghost" icon={<Plus className="size-4" />} onClick={() => setEditing('new')}>
            {t.settings.notify.addWorkspace}
          </Button>
        </Row>
      </Card>

      <h3 className="mb-1 text-[15px] font-semibold text-fg">{t.settings.notify.defaultTitle}</h3>
      <p className="mb-3 text-[13px] text-fg-3">{t.settings.notify.defaultHint}</p>
      <Card className="p-4 sm:p-5">
        {def ? (
          <>
            <NotifyForm value={def} onChange={setDef} events={events} />
            <div className="mt-5 flex justify-end border-t border-line pt-4">
              <Button variant="primary" loading={busy} onClick={saveDefault}>
                {t.common.save}
              </Button>
            </div>
          </>
        ) : (
          <Skeleton className="h-40" />
        )}
      </Card>
      <WorkspaceModal ws={editing} onClose={() => setEditing(null)} events={events} />
    </>
  )
}

// ---------- 项目 ----------

function Projects() {
  const q = useProjects()
  const { updateProject } = useActions()
  const [edit, setEdit] = useState<{ id: string; name: string; doc_root: string } | null>(null)
  const workspaces = q.data?.workspaces ?? []
  return (
    <>
      <Intro>{t.settings.projects.intro}</Intro>
      <Card className="divide-y divide-line overflow-hidden">
        {q.isPending ? (
          <Row>
            <Skeleton className="h-10 w-full" />
          </Row>
        ) : !q.data?.projects.length ? (
          <Empty icon={<FolderGit2 />} title={t.settings.projects.empty} />
        ) : (
          q.data.projects.map((p) => {
            const ws = workspaces.find((w) => w.id === p.workspace_id)
            return (
              <Row key={p.id} className="flex-wrap">
                <FolderGit2 className="size-[18px] shrink-0 text-fg-3" />
                <div className="min-w-0 flex-1">
                  <div className="text-sm font-medium text-fg">{p.name}</div>
                  <div className="truncate font-mono text-xs text-fg-3">{p.remote || t.settings.projects.noRemote}</div>
                </div>
                <Menu
                  trigger={
                    <Button size="sm">
                      {ws?.name ?? t.settings.projects.noWorkspace}
                      <ChevronDown className="size-3 opacity-60" />
                    </Button>
                  }
                  items={[
                    { type: 'label', label: t.settings.projects.workspace },
                    { label: t.settings.projects.noWorkspace, checked: !p.workspace_id, onSelect: () => void updateProject(p.id, { name: p.name, doc_root: p.doc_root }) },
                    ...workspaces.map((w) => ({ label: w.name, checked: w.id === p.workspace_id, onSelect: () => void updateProject(p.id, { name: p.name, doc_root: p.doc_root, workspace_id: w.id }) })),
                  ]}
                />
                <Button size="icon-sm" variant="ghost" aria-label={t.settings.projects.rename} onClick={() => setEdit({ id: p.id, name: p.name, doc_root: p.doc_root })}>
                  <Pencil className="size-3.5" />
                </Button>
              </Row>
            )
          })
        )}
      </Card>
      <Modal
        open={!!edit}
        onClose={() => setEdit(null)}
        title={t.settings.projects.rename}
        footer={
          <>
            <Button onClick={() => setEdit(null)}>{t.common.cancel}</Button>
            <Button
              variant="primary"
              disabled={!edit?.name.trim()}
              onClick={async () => {
                const p = q.data?.projects.find((x) => x.id === edit!.id)
                if (await updateProject(edit!.id, { name: edit!.name.trim(), doc_root: edit!.doc_root, workspace_id: p?.workspace_id || undefined })) setEdit(null)
              }}
            >
              {t.common.save}
            </Button>
          </>
        }
      >
        {edit && (
          <div className="space-y-4">
            <Field label={t.settings.notify.workspaceName}>
              <Input autoFocus value={edit.name} onChange={(e) => setEdit({ ...edit, name: e.target.value })} />
            </Field>
            <Field label={t.settings.projects.docRoot}>
              <Input value={edit.doc_root} placeholder={t.settings.projects.docRootPlaceholder} onChange={(e) => setEdit({ ...edit, doc_root: e.target.value })} className="font-mono" />
            </Field>
          </div>
        )}
      </Modal>
    </>
  )
}

// ---------- AI 摘要 ----------

type TestResult = { ok: true; sample_prompt: string; sample_reply: string; summary: string; latency_ms: number } | { ok: false; error: string }

function Summary() {
  const q = useSettings()
  const qc = useQueryClient()
  const [mode, setMode] = useState('off')
  const [base, setBase] = useState('')
  const [model, setModel] = useState('')
  const [key, setKey] = useState('')
  const [busy, wrap] = useBusy()
  const [result, setResult] = useState<TestResult | null>(null)
  useEffect(() => {
    if (q.data) {
      setMode(q.data.summary_mode)
      setBase(q.data.summary_base_url)
      setModel(q.data.summary_model)
    }
  }, [q.data])

  const save = (test: boolean) =>
    wrap(async () => {
      const s = q.data!
      setResult(null)
      try {
        await unwrap(api.PUT('/api/settings', { body: { public_url: s.public_url, notify_default: s.notify_default, summary_mode: mode as 'off', summary_base_url: base, summary_model: model, summary_api_key: key || undefined } }))
        setKey('')
        void qc.invalidateQueries({ queryKey: qk.settings })
        if (!test) {
          toast.success(t.common.saved)
          return
        }
        const r = await unwrap(api.POST('/api/settings/test-summary'))
        setResult({ ok: true, ...r })
      } catch (e) {
        if (test) setResult({ ok: false, error: errMsg(e) })
        else toast.error(errMsg(e))
      }
    })

  if (!q.data) return <Skeleton className="h-64" />
  return (
    <>
      <Intro>{t.settings.summary.intro}</Intro>
      <div className="mb-5 flex items-start gap-2.5 rounded-xl bg-accent-soft/60 px-3.5 py-3 text-[13px] text-fg-2">
        <KeyRound className="mt-0.5 size-4 shrink-0 text-accent" />
        {t.settings.summary.privacy}
      </div>

      <div className="mb-2 text-[13px] font-medium text-fg-2">{t.settings.summary.mode}</div>
      <div className="mb-6 grid gap-2 sm:grid-cols-3">
        {(['off', 'hub', 'device'] as const).map((m) => (
          <button key={m} onClick={() => setMode(m)} className={cx('rounded-xl border p-3 text-left transition', mode === m ? 'border-accent bg-accent-soft ring-1 ring-accent/40' : 'border-line bg-surface hover:border-line-strong')}>
            <div className="flex items-center justify-between text-sm font-medium text-fg">
              {t.settings.summary.modes[m]}
              {mode === m && <Check className="size-4 text-accent" />}
            </div>
            <div className="mt-1 text-xs leading-relaxed text-fg-3">{t.settings.summary.modeHints[m]}</div>
          </button>
        ))}
      </div>

      {mode === 'hub' && (
        <motion.div initial={{ opacity: 0, y: 4 }} animate={{ opacity: 1, y: 0 }}>
          <Card className="space-y-4 p-4 sm:p-5">
            <Field label={t.settings.summary.baseUrl} hint={t.settings.summary.baseUrlHint}>
              <Input value={base} onChange={(e) => setBase(e.target.value)} placeholder="https://api.deepseek.com/v1" className="font-mono" />
            </Field>
            <div className="grid gap-4 sm:grid-cols-2">
              <Field label={t.settings.summary.model}>
                <Input value={model} onChange={(e) => setModel(e.target.value)} placeholder={t.settings.summary.modelPlaceholder} className="font-mono" />
              </Field>
              <Field label={t.settings.summary.apiKey} hint={q.data.summary_api_key_set ? t.settings.summary.apiKeySet : undefined}>
                <Input type="password" value={key} onChange={(e) => setKey(e.target.value)} placeholder={q.data.summary_api_key_set ? '••••••••' : 'sk-…'} className="font-mono" autoComplete="off" />
              </Field>
            </div>
          </Card>
        </motion.div>
      )}

      <div className="mt-5 flex flex-wrap justify-end gap-2">
        <Button loading={busy && !result} onClick={() => save(false)}>
          {t.common.save}
        </Button>
        {mode === 'hub' && (
          <Button variant="primary" icon={<Sparkles className="size-4" />} loading={busy} disabled={!base || !model} onClick={() => save(true)}>
            {busy ? t.settings.summary.testing : t.settings.summary.test}
          </Button>
        )}
      </div>

      {result && (
        <motion.div initial={{ opacity: 0, y: 6 }} animate={{ opacity: 1, y: 0 }} className="mt-5">
          {result.ok ? (
            <Card className="overflow-hidden">
              <div className="flex items-center gap-2 border-b border-line bg-run-soft px-4 py-2.5 text-sm font-medium text-run">
                <CheckCircle2 className="size-4" />
                {t.settings.summary.testOk(result.latency_ms)}
              </div>
              <div className="space-y-3 p-4 text-sm">
                <p className="text-fg-3">{t.settings.summary.testExplain}</p>
                <div className="grid gap-3 sm:grid-cols-2">
                  <div className="rounded-xl bg-sunken p-3">
                    <div className="text-xs text-fg-3">{t.settings.summary.samplePrompt}</div>
                    <div className="mt-1 text-fg-2">{result.sample_prompt}</div>
                  </div>
                  <div className="rounded-xl bg-sunken p-3">
                    <div className="text-xs text-fg-3">{t.settings.summary.sampleReply}</div>
                    <div className="mt-1 text-fg-2">{result.sample_reply}</div>
                  </div>
                </div>
                <div className="rounded-xl border border-accent/40 bg-accent-soft/50 p-3">
                  <div className="flex items-center gap-1.5 text-xs font-medium text-accent">
                    <Sparkles className="size-3.5" />
                    {t.settings.summary.result}
                  </div>
                  <div className="mt-1 text-[15px] font-medium text-fg">{result.summary}</div>
                </div>
              </div>
            </Card>
          ) : (
            <Card className="flex items-start gap-3 border-danger/40 p-4">
              <XCircle className="mt-0.5 size-5 shrink-0 text-danger" />
              <div className="min-w-0">
                <div className="text-sm font-medium text-fg">{t.settings.summary.testFail}</div>
                <div className="mt-1 text-[13px] break-words text-fg-2">{result.error}</div>
              </div>
            </Card>
          )}
        </motion.div>
      )}
    </>
  )
}

// ---------- 外观 ----------

function Appearance() {
  const [pref, setPref] = useTheme()
  const opts: { v: ThemePref; icon: typeof Sun; label: string }[] = [
    { v: 'system', icon: SunMoon, label: t.theme.system },
    { v: 'light', icon: Sun, label: t.theme.light },
    { v: 'dark', icon: Moon, label: t.theme.dark },
  ]
  return (
    <div className="grid max-w-xl grid-cols-3 gap-3">
      {opts.map((o) => (
        <button key={o.v} onClick={() => setPref(o.v)} className={cx('flex flex-col items-center gap-2 rounded-2xl border p-4 transition', pref === o.v ? 'border-accent bg-accent-soft ring-1 ring-accent/40' : 'border-line bg-surface hover:border-line-strong')}>
          <o.icon className={cx('size-6', pref === o.v ? 'text-accent' : 'text-fg-3')} />
          <span className="text-sm font-medium text-fg">{o.label}</span>
        </button>
      ))}
    </div>
  )
}

// ---------- 账户 ----------

function Account({ version }: { version: string }) {
  const q = useSettings()
  const qc = useQueryClient()
  const { logout } = useActions()
  const [url, setUrl] = useState('')
  const [pw, setPw] = useState({ old: '', next: '' })
  const [busy, wrap] = useBusy()
  const [busy2, wrap2] = useBusy()
  useEffect(() => {
    if (q.data) setUrl(q.data.public_url)
  }, [q.data])
  const saveUrl = () =>
    wrap(async () => {
      const s = q.data!
      try {
        await unwrap(api.PUT('/api/settings', { body: { public_url: url, summary_mode: s.summary_mode, summary_base_url: s.summary_base_url, summary_model: s.summary_model, notify_default: s.notify_default } }))
        void qc.invalidateQueries({ queryKey: qk.settings })
        toast.success(t.common.saved)
      } catch (e) {
        toast.error(errMsg(e))
      }
    })
  const changePw = () =>
    wrap2(async () => {
      try {
        await unwrap(api.POST('/api/password', { body: { old: pw.old, new: pw.next } }))
        setPw({ old: '', next: '' })
        toast.success(t.settings.account.passwordChanged)
      } catch (e) {
        toast.error(errMsg(e))
      }
    })
  return (
    <div className="max-w-xl space-y-6">
      <Card className="p-4 sm:p-5">
        <Field label={t.settings.account.publicUrl} hint={t.settings.account.publicUrlHint}>
          <div className="flex gap-2">
            <Input value={url} onChange={(e) => setUrl(e.target.value)} placeholder="https://ks.example.com" className="font-mono" />
            <Button loading={busy} onClick={saveUrl}>
              {t.common.save}
            </Button>
          </div>
        </Field>
      </Card>
      <Card className="space-y-4 p-4 sm:p-5">
        <div className="text-sm font-semibold text-fg">{t.settings.account.password}</div>
        <div className="grid gap-3 sm:grid-cols-2">
          <Field label={t.settings.account.oldPassword}>
            <Input type="password" value={pw.old} onChange={(e) => setPw({ ...pw, old: e.target.value })} autoComplete="current-password" />
          </Field>
          <Field label={t.settings.account.newPassword}>
            <Input type="password" value={pw.next} onChange={(e) => setPw({ ...pw, next: e.target.value })} autoComplete="new-password" />
          </Field>
        </div>
        <div className="flex justify-end">
          <Button variant="primary" loading={busy2} disabled={!pw.old || pw.next.length < 8} onClick={changePw}>
            {t.common.save}
          </Button>
        </div>
      </Card>
      <div className="flex items-center justify-between">
        <span className="text-xs text-fg-3">{t.settings.account.version(version)}</span>
        <Button variant="danger" icon={<LogOut className="size-4" />} onClick={() => void logout()}>
          {t.settings.account.logout}
        </Button>
      </div>
    </div>
  )
}

export default function SettingsPage({ version }: { version: string }) {
  const [params, setParams] = useSearchParams()
  const tab = (tabs.find((x) => x.id === params.get('tab'))?.id ?? 'devices') as Tab
  const go = (id: Tab) =>
    setParams((p) => {
      const n = new URLSearchParams(p)
      n.set('tab', id)
      return n
    })

  const body: Record<Tab, ReactNode> = {
    devices: <Devices />,
    notify: <Notify />,
    projects: <Projects />,
    summary: <Summary />,
    appearance: <Appearance />,
    account: <Account version={version} />,
  }

  return (
    <>
      <PageHeader title={t.settings.title} />
      <div className="-mx-4 mb-5 overflow-x-auto px-4 lg:hidden">
        <Segmented id="settings-m" size="sm" value={tab} onChange={go} options={tabs.map((x) => ({ value: x.id, label: t.settings.sections[x.id] }))} />
      </div>
      <div className="grid grid-cols-1 gap-10 lg:grid-cols-[220px_minmax(0,1fr)]">
        <nav className="hidden space-y-0.5 lg:block">
          {tabs.map((x) => (
            <button key={x.id} onClick={() => go(x.id)} className={cx('relative flex w-full items-center gap-3 rounded-xl px-3 py-2.5 text-left transition-colors', tab === x.id ? 'text-fg' : 'text-fg-2 hover:bg-hover')}>
              {tab === x.id && <motion.span layoutId="settings-active" className="absolute inset-0 rounded-xl bg-surface shadow-xs" transition={{ type: 'spring', bounce: 0.15, duration: 0.35 }} />}
              <x.icon className="relative size-[18px] shrink-0 text-fg-3" />
              <span className="relative min-w-0">
                <span className="block text-sm font-medium">{t.settings.sections[x.id]}</span>
                <span className="block truncate text-xs text-fg-3">{t.settings.sectionHints[x.id]}</span>
              </span>
            </button>
          ))}
        </nav>
        <motion.div key={tab} initial={{ opacity: 0, x: 6 }} animate={{ opacity: 1, x: 0 }} transition={{ duration: 0.18 }} className="min-w-0">
          <h2 className="mb-2 hidden text-lg font-semibold text-fg lg:block">{t.settings.sections[tab]}</h2>
          {body[tab]}
        </motion.div>
      </div>
    </>
  )
}
