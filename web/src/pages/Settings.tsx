import { useEffect, useState, type ReactNode } from 'react'
import { Navigate, useNavigate, useSearchParams } from 'react-router'
import { useQuery, useQueryClient } from '@tanstack/react-query'
import { Bell, BellRing, CheckCircle2, ChevronDown, Copy, FileText, FolderGit2, History, Info, KeyRound, LogOut, MessageSquareText, Monitor, MonitorSmartphone, Moon, Palette, Pencil, Plus, Send, Smartphone, Sparkles, Sun, SunMoon, Tag, Trash2, User, XCircle, type LucideIcon } from 'lucide-react'
import { toast } from 'sonner'
import { api, errMsg, unwrap, type Device, type NotifyConfig, type SettingsView, type Workspace } from '../api/client'
import { qk, useDevices, useProjects, usePush, useSettings } from '../api/queries'
import { disablePush, enablePush, pushState, type PushState } from '../lib/push'
import { passkeySupported, registerPasskey } from '../lib/passkey'
import { t } from '../i18n'
import { copyText, cx, relTime, useNow } from '../lib/util'
import { useTheme, type ThemePref } from '../lib/theme'
import { Badge, Button, Card, Empty, ErrorState, Field, Input, PageHeader, Segmented, Skeleton, Switch, Tip } from '../ui/base'
import { Modal, useConfirm } from '../ui/overlay'
import { useActions, useBusy } from '../app/actions'

type Tab = 'devices' | 'notify' | 'projects' | 'ai' | 'appearance' | 'account'
const tabs: { id: Tab; icon: LucideIcon }[] = [
  { id: 'devices', icon: Monitor },
  { id: 'notify', icon: Bell },
  { id: 'projects', icon: FolderGit2 },
  { id: 'ai', icon: Sparkles },
  { id: 'appearance', icon: Palette },
  { id: 'account', icon: User },
]

function Row({ children, className }: { children: ReactNode; className?: string }) {
  return <div className={cx('flex items-center gap-3 px-4 py-3.5', className)}>{children}</div>
}

function Hint({ children }: { children: ReactNode }) {
  return (
    <Tip content={children}>
      <span className="inline-flex text-fg-3 hover:text-fg-2" aria-label={typeof children === 'string' ? children : undefined}>
        <Info className="size-3.5" />
      </span>
    </Tip>
  )
}

function Label({ children, hint }: { children: ReactNode; hint?: ReactNode }) {
  return (
    <span className="flex items-center gap-1.5">
      {children}
      {hint && <Hint>{hint}</Hint>}
    </span>
  )
}

function Group({ title, hint, children, action }: { title: ReactNode; hint?: ReactNode; children: ReactNode; action?: ReactNode }) {
  return (
    <section className="mb-8">
      <div className="mb-2.5 flex items-center gap-2">
        <h3 className="text-[13px] font-semibold text-fg-2">{title}</h3>
        {hint && <Hint>{hint}</Hint>}
        <span className="ml-auto">{action}</span>
      </div>
      {children}
    </section>
  )
}

// 保存设置：服务端接口要求整份提交，这里在当前值上合并修改。
function useSaveSettings() {
  const q = useSettings()
  const qc = useQueryClient()
  return async (patch: Partial<SettingsView> & { summary_api_key?: string }, okMsg: string | false = t.common.saved) => {
    const s = q.data
    if (!s) return false
    try {
      await unwrap(
        api.PUT('/api/settings', {
          body: {
            public_url: patch.public_url ?? s.public_url,
            ai: patch.ai ?? s.ai,
            summary_base_url: patch.summary_base_url ?? s.summary_base_url,
            summary_model: patch.summary_model ?? s.summary_model,
            summary_api_key: patch.summary_api_key || undefined,
            notify_default: patch.notify_default ?? s.notify_default,
            upload_image_mb: patch.upload_image_mb ?? s.upload_image_mb,
            upload_video_mb: patch.upload_video_mb ?? s.upload_video_mb,
          },
        }),
      )
      await qc.invalidateQueries({ queryKey: qk.settings })
      if (okMsg) toast.success(okMsg)
      return true
    } catch (e) {
      toast.error(errMsg(e))
      return false
    }
  }
}

// ---------- 设备 ----------

function CommandLine({ cmd }: { cmd: string }) {
  return (
    <div className="flex items-center gap-2 rounded-lg bg-sunken px-3 py-2">
      <code className="min-w-0 flex-1 overflow-x-auto font-mono text-[12px] whitespace-nowrap text-fg">{cmd}</code>
      <Tip content={t.common.copy}>
        <Button size="icon-sm" variant="ghost" aria-label={t.common.copy} onClick={async () => (await copyText(cmd)) && toast.success(t.common.copied)}>
          <Copy className="size-3.5" />
        </Button>
      </Tip>
    </div>
  )
}

function DeviceTokenModal({ token, name, onClose }: { token: string; name: string; onClose: () => void }) {
  const cmds = [null, `ks login --hub ${window.location.origin} --token ${token}`, 'ks install', null]
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
      <ol className="space-y-3.5 text-sm text-fg-2">
        {t.settings.devices.steps.map((text, i) => (
          <li key={i} className="flex gap-3">
            <span className="flex size-5 shrink-0 items-center justify-center rounded-full bg-accent-soft text-[11px] font-semibold text-accent">{i + 1}</span>
            <div className="min-w-0 flex-1 space-y-1.5">
              <div>{text}</div>
              {cmds[i] && <CommandLine cmd={cmds[i]!} />}
            </div>
          </li>
        ))}
      </ol>
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
    if (await confirm({ title: `${t.settings.devices.revoke}「${d.name}」`, description: t.settings.devices.revokeConfirm(d.name), confirmText: t.settings.devices.revoke, danger: true })) {
      if (await revokeDevice(d.id)) toast(t.settings.devices.revoked)
    }
  }

  return (
    <>
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
                <div className="mt-0.5 truncate text-xs text-fg-3">{[d.online ? t.device.online : d.last_seen ? t.device.lastSeen(relTime(d.last_seen, now)) : t.device.never, d.hostname, d.platform, d.version].filter(Boolean).join(t.common.sep)}</div>
              </div>
              {!d.revoked && (
                <Tip content={t.settings.devices.revoke}>
                  <Button size="icon-sm" variant="ghost" className="text-fg-3 hover:text-danger" aria-label={t.settings.devices.revoke} onClick={() => revoke(d)}>
                    <Trash2 className="size-4" />
                  </Button>
                </Tip>
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

function channels(n: NotifyConfig): string[] {
  const out: string[] = []
  if (n.bark?.key) out.push(t.settings.notify.bark)
  if (n.wecom?.webhook) out.push(t.settings.notify.wecom)
  return out
}

const allEvents = ['decision_raised', 'waiting_approval', 'requirement_review', 'waiting_reply', 'device_lost']

function NotifyForm({ value, onChange }: { value: NotifyConfig; onChange: (v: NotifyConfig) => void }) {
  const [busy, wrap] = useBusy()
  const chosen = value.events?.length ? value.events : allEvents.slice(0, 3)
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
        <Field label={<Label hint={t.settings.notify.barkKeyTip}>{t.settings.notify.barkKey}</Label>}>
          <Input value={value.bark?.key ?? ''} onChange={(e) => onChange({ ...value, bark: { server: value.bark?.server ?? '', key: e.target.value } })} className="font-mono" />
        </Field>
        <Field label={t.settings.notify.barkServer}>
          <Input value={value.bark?.server ?? ''} placeholder="https://api.day.app" onChange={(e) => onChange({ ...value, bark: { key: value.bark?.key ?? '', server: e.target.value } })} className="font-mono" />
        </Field>
      </div>
      <Field label={<Label hint={t.settings.notify.wecomTip}>{t.settings.notify.wecomWebhook}</Label>}>
        <Input value={value.wecom?.webhook ?? ''} placeholder="https://qyapi.weixin.qq.com/cgi-bin/webhook/send?key=…" onChange={(e) => onChange({ ...value, wecom: { webhook: e.target.value } })} className="font-mono" />
      </Field>
      <div>
        <div className="mb-2 text-[13px] font-medium text-fg-2">{t.settings.notify.events}</div>
        <div className="flex flex-wrap gap-2">
          {allEvents.map((ev) => {
            const on = chosen.includes(ev)
            return (
              <Tip key={ev} content={t.settings.notify.evTip[ev]}>
                <button
                  onClick={() => onChange({ ...value, events: on ? chosen.filter((x) => x !== ev) : [...chosen, ev] })}
                  aria-pressed={on}
                  className={cx('rounded-full border px-3 py-1.5 text-[13px] font-medium transition-colors', on ? 'border-accent bg-accent-soft text-accent' : 'border-line text-fg-2 hover:border-line-strong')}
                >
                  {t.settings.notify.ev[ev]}
                </button>
              </Tip>
            )
          })}
        </div>
      </div>
      <Button size="sm" icon={<Send className="size-3.5" />} loading={busy} disabled={!channels(value).length} onClick={test}>
        {t.settings.notify.test}
      </Button>
    </div>
  )
}

function WorkspaceModal({ ws, onClose }: { ws: Workspace | 'new' | null; onClose: () => void }) {
  const { createWorkspace, updateWorkspace } = useActions()
  const [busy, wrap] = useBusy()
  const [name, setName] = useState('')
  const [n, setN] = useState<NotifyConfig>({})
  useEffect(() => {
    setName(ws && ws !== 'new' ? ws.name : '')
    setN(ws && ws !== 'new' ? ws.notify : {})
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
        <NotifyForm value={n} onChange={setN} />
      </div>
    </Modal>
  )
}

// 系统推送（R-49）：这台设备的开关、测试，以及已开启推送的全部设备
function PushGroup() {
  const push = usePush()
  const qc = useQueryClient()
  const now = useNow()
  const [busy, wrap] = useBusy()
  const [st, setSt] = useState<{ state: PushState; endpoint?: string } | null>(null)
  const refresh = () => void pushState().then(setSt)
  useEffect(refresh, [])
  const run = (fn: () => Promise<unknown>, done?: string) =>
    wrap(async () => {
      try {
        await fn()
        if (done) toast.success(done)
      } catch (e) {
        toast.error(errMsg(e))
      }
      refresh()
      void qc.invalidateQueries({ queryKey: ['settings', 'push'] })
    })
  const mine = (push.data?.subs ?? []).find((x) => x.endpoint === st?.endpoint)
  const test = (ref: string) => run(() => unwrap(api.POST('/api/push/test', { body: { ref } })), t.settings.push.testSent)
  return (
    <Group title={t.settings.push.title} hint={t.settings.push.hint}>
      <Card className="divide-y divide-line overflow-hidden">
        <Row>
          <BellRing className="size-5 shrink-0 text-fg-3" />
          <div className="min-w-0 flex-1">
            <div className="text-sm font-medium text-fg">{t.settings.push.thisDevice}</div>
            <div className="mt-0.5 text-xs leading-relaxed text-fg-3">{st ? t.settings.push.state[st.state] : '…'}</div>
          </div>
          {st?.state === 'on' ? (
            <>
              <Button size="sm" disabled={busy} onClick={() => void test(st.endpoint!)}>
                {t.settings.push.test}
              </Button>
              <Button size="sm" variant="ghost" disabled={busy} onClick={() => void run(disablePush, t.settings.push.disabled)}>
                {t.settings.push.disable}
              </Button>
            </>
          ) : (
            st?.state === 'off' && (
              <Button size="sm" variant="primary" loading={busy} onClick={() => void run(enablePush, t.settings.push.enabled)}>
                {t.settings.push.enable}
              </Button>
            )
          )}
        </Row>
        {(push.data?.subs ?? [])
          .filter((x) => x.id !== mine?.id)
          .map((x) => (
            <Row key={x.id}>
              <Smartphone className="size-5 shrink-0 text-fg-3" />
              <div className="min-w-0 flex-1">
                <div className="truncate text-sm text-fg">{x.name || t.settings.push.unnamed}</div>
                <div className="mt-0.5 text-xs text-fg-3">{x.last_ok_at ? t.settings.push.lastOk(relTime(x.last_ok_at, now)) : t.settings.push.since(relTime(x.created_at, now))}</div>
              </div>
              <Button size="sm" variant="ghost" disabled={busy} onClick={() => void test(x.id)}>
                {t.settings.push.test}
              </Button>
              <Button size="sm" variant="ghost" disabled={busy} onClick={() => void run(() => unwrap(api.POST('/api/push/unsubscribe', { body: { ref: x.id } })), t.settings.push.removed)}>
                {t.settings.push.remove}
              </Button>
            </Row>
          ))}
      </Card>
    </Group>
  )
}

function Notify() {
  const settings = useSettings()
  const projects = useProjects()
  const { deleteWorkspace } = useActions()
  const confirm = useConfirm()
  const save = useSaveSettings()
  const [editing, setEditing] = useState<Workspace | 'new' | null>(null)
  const [def, setDef] = useState<NotifyConfig | null>(null)
  const [busy, wrap] = useBusy()
  useEffect(() => {
    if (settings.data && !def) setDef(settings.data.notify_default)
  }, [settings.data, def])

  return (
    <>
      <PushGroup />
      <Group
        title={t.settings.notify.workspaces}
        hint={t.settings.notify.workspacesHint}
        action={
          <Button size="sm" variant="ghost" icon={<Plus className="size-3.5" />} onClick={() => setEditing('new')}>
            {t.settings.notify.addWorkspace}
          </Button>
        }
      >
        {(projects.data?.workspaces ?? []).length > 0 && (
          <Card className="divide-y divide-line overflow-hidden">
            {projects.data!.workspaces.map((w) => {
              const ch = channels(w.notify)
              const n = projects.data!.projects.filter((p) => p.workspace_id === w.id).length
              return (
                <Row key={w.id}>
                  <div className="min-w-0 flex-1">
                    <div className="text-sm font-medium text-fg">{w.name}</div>
                    <div className="mt-1 flex flex-wrap items-center gap-1.5">
                      {ch.length ? ch.map((c) => <Badge key={c}>{c}</Badge>) : <span className="text-xs text-fg-3">{t.settings.notify.noChannel}</span>}
                      <Tip content={t.settings.sections.projects}>
                        <span className="inline-flex items-center gap-1 text-xs text-fg-3">
                          <FolderGit2 className="size-3.5" />
                          {n}
                        </span>
                      </Tip>
                    </div>
                  </div>
                  <Tip content={t.common.edit}>
                    <Button size="icon-sm" variant="ghost" aria-label={t.common.edit} onClick={() => setEditing(w)}>
                      <Pencil className="size-3.5" />
                    </Button>
                  </Tip>
                  <Tip content={t.common.delete}>
                    <Button
                      size="icon-sm"
                      variant="ghost"
                      aria-label={t.common.delete}
                      onClick={async () => (await confirm({ title: t.common.delete, description: t.settings.notify.deleteConfirm(w.name), danger: true, confirmText: t.common.delete })) && deleteWorkspace(w.id)}
                    >
                      <Trash2 className="size-3.5" />
                    </Button>
                  </Tip>
                </Row>
              )
            })}
          </Card>
        )}
      </Group>

      <Group title={t.settings.notify.defaultTitle} hint={t.settings.notify.defaultHint}>
        <Card className="p-4 sm:p-5">
          {def ? (
            <>
              <NotifyForm value={def} onChange={setDef} />
              <div className="mt-5 flex justify-end border-t border-line pt-4">
                <Button variant="primary" loading={busy} onClick={() => wrap(() => save({ notify_default: def }))}>
                  {t.common.save}
                </Button>
              </div>
            </>
          ) : (
            <Skeleton className="h-40" />
          )}
        </Card>
      </Group>
      <WorkspaceModal ws={editing} onClose={() => setEditing(null)} />
    </>
  )
}

// ---------- 智能 ----------

type Feature = 'summary' | 'title' | 'digest' | 'decision_title' | 'recap'
const features: { id: Feature; icon: LucideIcon }[] = [
  { id: 'summary', icon: FileText },
  { id: 'title', icon: Tag },
  { id: 'digest', icon: MessageSquareText },
  { id: 'decision_title', icon: Sparkles },
  { id: 'recap', icon: History },
]

type TestResult = { ok: true; sample_prompt: string; sample_reply: string; summary: string; latency_ms: number } | { ok: false; error: string }

function AI() {
  const q = useSettings()
  const save = useSaveSettings()
  const [base, setBase] = useState('')
  const [model, setModel] = useState('')
  const [key, setKey] = useState('')
  const [busy, wrap] = useBusy()
  const [result, setResult] = useState<TestResult | null>(null)
  const [sample, setSample] = useState(false)
  useEffect(() => {
    if (q.data) {
      setBase(q.data.summary_base_url)
      setModel(q.data.summary_model)
    }
  }, [q.data])

  if (!q.data) return <Skeleton className="h-64" />
  const ai = q.data.ai ?? {}
  const configured = !!q.data.summary_base_url && !!q.data.summary_model
  const dirty = base !== q.data.summary_base_url || model !== q.data.summary_model || !!key

  const test = () =>
    wrap(async () => {
      setResult(null)
      if (dirty && !(await save({ summary_base_url: base.trim(), summary_model: model.trim(), summary_api_key: key }, false))) return
      setKey('')
      try {
        const r = await unwrap(api.POST('/api/settings/test-summary'))
        setResult({ ok: true, ...r })
      } catch (e) {
        setResult({ ok: false, error: errMsg(e) })
      }
    })

  return (
    <>
      <Group title={t.settings.ai.model} hint={t.settings.ai.modelHint}>
        <Card className="space-y-4 p-4 sm:p-5">
          <Field label={t.settings.ai.baseUrl}>
            <Input value={base} onChange={(e) => setBase(e.target.value)} placeholder="https://api.deepseek.com/v1" className="font-mono" />
          </Field>
          <div className="grid gap-4 sm:grid-cols-2">
            <Field label={t.settings.ai.modelName}>
              <Input value={model} onChange={(e) => setModel(e.target.value)} placeholder="deepseek-chat" className="font-mono" />
            </Field>
            <Field label={<Label>{t.settings.ai.apiKey}</Label>}>
              <Input type="password" value={key} onChange={(e) => setKey(e.target.value)} placeholder={q.data.summary_api_key_set ? `•••••••• ${t.settings.ai.apiKeySet}` : 'sk-…'} className="font-mono" autoComplete="off" />
            </Field>
          </div>
          <div className="flex flex-wrap items-center justify-end gap-2 border-t border-line pt-4">
            {result &&
              (result.ok ? (
                <button onClick={() => setSample(!sample)} className="mr-auto flex items-center gap-1.5 text-sm text-run">
                  <CheckCircle2 className="size-4" />
                  {t.settings.ai.testOk(result.latency_ms)}
                  <span className="text-fg-3">· {t.settings.ai.sample}</span>
                  <ChevronDown className={cx('size-3.5 text-fg-3 transition-transform', sample && 'rotate-180')} />
                </button>
              ) : (
                <Tip content={result.error}>
                  <span className="mr-auto flex min-w-0 items-center gap-1.5 text-sm text-danger">
                    <XCircle className="size-4 shrink-0" />
                    <span className="truncate">{t.settings.ai.testFail}：{result.error}</span>
                  </span>
                </Tip>
              ))}
            {dirty && (
              <Button onClick={() => wrap(async () => (await save({ summary_base_url: base.trim(), summary_model: model.trim(), summary_api_key: key })) && setKey(''))}>
                {t.common.save}
              </Button>
            )}
            <Button variant="primary" loading={busy} disabled={!base || !model} onClick={test}>
              {t.settings.ai.test}
            </Button>
          </div>
          {result?.ok && sample && (
            <div className="rise grid gap-2 text-[13px] sm:grid-cols-[auto_1fr]">
              <span className="text-fg-3">{t.settings.ai.sampleIn}</span>
              <span className="text-fg-2">
                {result.sample_prompt} → {result.sample_reply}
              </span>
              <span className="text-fg-3">{t.settings.ai.sampleOut}</span>
              <span className="font-medium text-fg">{result.summary}</span>
            </div>
          )}
        </Card>
      </Group>

      <Group title={t.settings.ai.features} hint={t.settings.ai.featuresHint}>
        <Card className="divide-y divide-line overflow-hidden">
          {features.map((f) => (
            <Row key={f.id} className={cx(!configured && 'opacity-60')}>
              <span className="flex size-8 shrink-0 items-center justify-center rounded-[10px] bg-accent-soft text-accent">
                <f.icon className="size-4" />
              </span>
              <div className="min-w-0 flex-1">
                <div className="text-sm font-medium text-fg">{t.settings.ai.f[f.id].name}</div>
                <div className="mt-0.5 text-xs text-fg-3">{t.settings.ai.f[f.id].desc}</div>
              </div>
              <Tip content={configured ? undefined : t.settings.ai.needModel}>
                <span>
                  <Switch checked={!!ai[f.id]} disabled={!configured} onChange={(v) => void save({ ai: { ...ai, [f.id]: v } }, false)} />
                </span>
              </Tip>
            </Row>
          ))}
        </Card>
        {(ai.summary || ai.title || ai.digest) && <p className="mt-2.5 text-xs text-fg-3">{t.settings.ai.privacy}</p>}
      </Group>
    </>
  )
}

// ---------- 外观 ----------

function Appearance() {
  const [pref, setPref] = useTheme()
  const opts: { v: ThemePref; icon: LucideIcon; label: string }[] = [
    { v: 'system', icon: SunMoon, label: t.theme.system },
    { v: 'light', icon: Sun, label: t.theme.light },
    { v: 'dark', icon: Moon, label: t.theme.dark },
  ]
  return (
    <div className="grid max-w-md grid-cols-3 gap-3">
      {opts.map((o) => (
        <button key={o.v} onClick={() => setPref(o.v)} className={cx('flex flex-col items-center gap-2 rounded-2xl border p-4 transition-colors', pref === o.v ? 'border-accent bg-accent-soft ring-1 ring-accent/40' : 'border-line bg-surface hover:border-line-strong')}>
          <o.icon className={cx('size-6', pref === o.v ? 'text-accent' : 'text-fg-3')} />
          <span className="text-sm font-medium text-fg">{o.label}</span>
        </button>
      ))}
    </div>
  )
}

// ---------- 账户 ----------

// passkey（R-7）：登记、查看、删除。passkey 绑定在当前网址上，换了网址要重新登记。
function Passkeys() {
  const q = useQuery({ queryKey: ['settings', 'passkeys'], queryFn: () => unwrap(api.GET('/api/passkeys')), select: (d) => d.passkeys ?? [] })
  const qc = useQueryClient()
  const confirm = useConfirm()
  const now = useNow()
  const [busy, wrap] = useBusy()
  const here = (q.data ?? []).filter((p) => p.origin === window.location.origin)
  const others = (q.data ?? []).filter((p) => p.origin !== window.location.origin)
  const refresh = () => {
    void qc.invalidateQueries({ queryKey: ['settings', 'passkeys'] })
    void qc.invalidateQueries({ queryKey: qk.auth })
  }
  const add = () =>
    wrap(async () => {
      try {
        await registerPasskey()
        toast.success(t.settings.passkey.added)
      } catch (e) {
        if (!(e instanceof DOMException && e.name === 'NotAllowedError')) toast.error(errMsg(e))
      }
      refresh()
    })
  const remove = async (id: string, name: string) => {
    if (!(await confirm({ title: t.settings.passkey.removeConfirm(name), description: t.settings.passkey.removeBody, confirmText: t.common.delete, danger: true }))) return
    try {
      await unwrap(api.DELETE('/api/passkeys/{id}', { params: { path: { id } } }))
      refresh()
    } catch (e) {
      toast.error(errMsg(e))
    }
  }
  const row = (p: (typeof here)[number]) => (
    <div key={p.id} className="flex items-center gap-3 py-2.5">
      <KeyRound className="size-4 shrink-0 text-fg-3" />
      <div className="min-w-0 flex-1">
        <div className="truncate text-sm text-fg">{p.name}</div>
        <div className="truncate text-xs text-fg-3">
          {p.last_used_at ? t.settings.passkey.used(relTime(p.last_used_at, now)) : t.settings.passkey.created(relTime(p.created_at, now))}
          {p.origin !== window.location.origin && ` · ${p.origin}`}
        </div>
      </div>
      <Button size="icon-sm" variant="ghost" aria-label={t.common.delete} onClick={() => void remove(p.id, p.name)}>
        <Trash2 className="size-3.5" />
      </Button>
    </div>
  )
  return (
    <Card className="p-4 sm:p-5">
      <div className="flex items-center gap-2">
        <div className="text-sm font-semibold text-fg">{t.settings.passkey.title}</div>
        <span className="ml-auto">
          <Button size="sm" loading={busy} disabled={!passkeySupported()} onClick={() => void add()} icon={<Plus className="size-3.5" />}>
            {t.settings.passkey.add}
          </Button>
        </span>
      </div>
      <p className="mt-1 text-xs leading-relaxed text-fg-3">{passkeySupported() ? t.settings.passkey.hint : t.settings.passkey.unsupported}</p>
      {(here.length > 0 || others.length > 0) && <div className="mt-2 divide-y divide-line">{[...here, ...others].map(row)}</div>}
    </Card>
  )
}

// 附件大小上限（R-72）：图片、录屏分开设，网页和 Agent 上传都按这里的值检查。
function UploadLimits() {
  const q = useSettings()
  const save = useSaveSettings()
  const [busy, wrap] = useBusy()
  const [v, setV] = useState({ image: '', video: '' })
  useEffect(() => {
    if (q.data) setV({ image: String(q.data.upload_image_mb), video: String(q.data.upload_video_mb) })
  }, [q.data])
  const num = (x: string) => Math.min(4096, Math.max(1, Math.round(Number(x)) || 1))
  const dirty = q.data && (num(v.image) !== q.data.upload_image_mb || num(v.video) !== q.data.upload_video_mb)
  const L = t.settings.account.upload
  return (
    <Card className="space-y-4 p-4 sm:p-5">
      <div>
        <div className="text-sm font-semibold text-fg">{L.title}</div>
        <p className="mt-1 text-xs text-fg-3">{L.hint}</p>
      </div>
      <div className="grid gap-3 sm:grid-cols-2">
        <Field label={L.image}>
          <Input type="number" inputMode="numeric" min={1} max={4096} value={v.image} onChange={(e) => setV({ ...v, image: e.target.value })} />
        </Field>
        <Field label={L.video}>
          <Input type="number" inputMode="numeric" min={1} max={4096} value={v.video} onChange={(e) => setV({ ...v, video: e.target.value })} />
        </Field>
      </div>
      <div className="flex justify-end">
        <Button variant="primary" loading={busy} disabled={!dirty} onClick={() => wrap(() => save({ upload_image_mb: num(v.image), upload_video_mb: num(v.video) }))}>
          {t.common.save}
        </Button>
      </div>
    </Card>
  )
}

function Account({ version }: { version: string }) {
  const q = useSettings()
  const save = useSaveSettings()
  const { logout } = useActions()
  const [url, setUrl] = useState('')
  const [pw, setPw] = useState({ old: '', next: '' })
  const [busy, wrap] = useBusy()
  const [busy2, wrap2] = useBusy()
  useEffect(() => {
    if (q.data) setUrl(q.data.public_url)
  }, [q.data])
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
        <Field label={<Label hint={t.settings.account.publicUrlTip}>{t.settings.account.publicUrl}</Label>}>
          <div className="flex gap-2">
            <Input value={url} onChange={(e) => setUrl(e.target.value)} placeholder="https://ks.example.com" className="font-mono" />
            <Button loading={busy} disabled={url === q.data?.public_url} onClick={() => wrap(() => save({ public_url: url.trim() }))}>
              {t.common.save}
            </Button>
          </div>
        </Field>
      </Card>
      <UploadLimits />
      <Passkeys />
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
  const navigate = useNavigate()
  // 项目设置只在项目页维护一份（R-51）：这里的“项目”页签直接跳过去
  const go = (id: Tab) =>
    id === 'projects'
      ? navigate('/projects')
      : setParams((p) => {
          const n = new URLSearchParams(p)
          n.set('tab', id)
          return n
        })
  const body: Record<Tab, ReactNode> = {
    devices: <Devices />,
    notify: <Notify />,
    projects: <Navigate to="/projects" replace />,
    ai: <AI />,
    appearance: <Appearance />,
    account: <Account version={version} />,
  }
  return (
    <>
      <PageHeader title={t.settings.title} />
      <div className="-mx-4 mb-5 overflow-x-auto px-4 lg:hidden">
        <Segmented size="sm" value={tab} onChange={go} options={tabs.map((x) => ({ value: x.id, label: t.settings.sections[x.id] }))} />
      </div>
      <div className="grid grid-cols-1 gap-10 lg:grid-cols-[200px_minmax(0,1fr)]">
        <nav className="hidden space-y-0.5 lg:block">
          {tabs.map((x) => (
            <button key={x.id} onClick={() => go(x.id)} className={cx('flex h-9 w-full items-center gap-2.5 rounded-lg px-2.5 text-left text-sm font-medium transition-colors', tab === x.id ? 'bg-surface text-fg shadow-xs' : 'text-fg-2 hover:bg-hover hover:text-fg')}>
              <x.icon className="size-[18px] shrink-0 text-fg-3" />
              {t.settings.sections[x.id]}
            </button>
          ))}
        </nav>
        <div key={tab} className="rise min-w-0 max-w-3xl">
          <h2 className="mb-4 hidden text-lg font-semibold text-fg lg:block">{t.settings.sections[tab]}</h2>
          {body[tab]}
        </div>
      </div>
    </>
  )
}
