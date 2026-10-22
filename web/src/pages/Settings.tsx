import { useEffect, useState, type ReactNode } from 'react'
import { useQueryClient } from '@tanstack/react-query'
import { api, errMsg, unwrap, type Device, type NotifyConfig, type Project, type SettingsView, type Workspace } from '../api/client'
import { qk, useDevices, useProjects, useSettings } from '../api/queries'
import { ConfirmButton, CopyButton, Empty, ErrorBox, Field, Loading, Modal, Spinner, cx, useNow, useToast } from '../components/ui'
import { t } from '../i18n'
import { relTime } from '../lib/time'

const DEFAULT_EVENTS = ['decision_raised', 'waiting_approval', 'requirement_review']
const ALL_EVENTS = ['decision_raised', 'waiting_approval', 'requirement_review', 'waiting_reply', 'device_lost']

function Section({ title, hint, children, action }: { title: string; hint?: string; children: ReactNode; action?: ReactNode }) {
  return (
    <section className="space-y-3">
      <div className="flex items-end gap-3">
        <div className="min-w-0 flex-1">
          <h2 className="text-base font-semibold">{title}</h2>
          {hint && <p className="muted mt-0.5 text-xs leading-relaxed">{hint}</p>}
        </div>
        {action}
      </div>
      {children}
    </section>
  )
}

// useAsync 包装一次异步操作的忙碌状态与错误提示。
function useAsync() {
  const toast = useToast()
  const [busy, setBusy] = useState(false)
  const run = async <T,>(fn: () => Promise<T>, okText?: string): Promise<T | undefined> => {
    setBusy(true)
    try {
      const out = await fn()
      if (okText) toast(okText)
      return out
    } catch (e) {
      toast(errMsg(e), 'err')
      return undefined
    } finally {
      setBusy(false)
    }
  }
  return { busy, run }
}

export default function SettingsPage({ version }: { version: string }) {
  const settings = useSettings()
  return (
    <div className="mx-auto max-w-3xl space-y-8">
      <h1 className="text-lg font-semibold">{t.settings.title}</h1>
      <DevicesSection />
      <WorkspacesSection events={settings.data?.notify_events ?? ALL_EVENTS} />
      <ProjectsSection />
      {settings.isPending ? (
        <Loading />
      ) : settings.isError ? (
        <ErrorBox error={settings.error} onRetry={() => settings.refetch()} />
      ) : (
        <>
          <DefaultNotifySection s={settings.data} />
          <SummarySection s={settings.data} />
          <PublicUrlSection s={settings.data} />
        </>
      )}
      <AccountSection version={version} />
    </div>
  )
}

// ---------- 设备 ----------

function DevicesSection() {
  const q = useDevices()
  const qc = useQueryClient()
  const now = useNow()
  const { busy, run } = useAsync()
  const [adding, setAdding] = useState(false)
  const [name, setName] = useState('')
  const [created, setCreated] = useState<{ device: Device; token: string } | null>(null)

  const create = async () => {
    const out = await run(() => unwrap(api.POST('/api/devices', { body: { name: name.trim() } })))
    if (out) {
      setAdding(false)
      setName('')
      setCreated(out)
      void qc.invalidateQueries({ queryKey: qk.devices })
    }
  }

  const cmd = created ? `ks login --hub ${window.location.origin} --token ${created.token}` : ''

  return (
    <Section
      title={t.settings.devices.title}
      hint={t.settings.devices.hint}
      action={
        <button className="btn btn-primary btn-sm" onClick={() => setAdding(true)}>
          + {t.settings.devices.add}
        </button>
      }
    >
      {q.isPending ? (
        <Loading />
      ) : q.isError ? (
        <ErrorBox error={q.error} onRetry={() => q.refetch()} />
      ) : q.data.length === 0 ? (
        <Empty>{t.settings.devices.empty}</Empty>
      ) : (
        <ul className="card divide-y divide-slate-100 dark:divide-slate-800">
          {q.data.map((d) => (
            <li key={d.id} className={cx('flex flex-wrap items-center gap-x-3 gap-y-1 px-3 py-2.5', d.revoked && 'opacity-60')}>
              <span className={cx('size-2 shrink-0 rounded-full', d.revoked ? 'bg-slate-300' : d.online ? 'bg-emerald-500' : 'bg-slate-400')} />
              <div className="min-w-0 flex-1">
                <div className="flex flex-wrap items-center gap-2 text-sm font-medium">
                  <span className={cx(d.revoked && 'line-through')}>{d.name}</span>
                  {d.revoked ? (
                    <span className="chip chip-gray">{t.settings.devices.revoked}</span>
                  ) : (
                    <span className={cx('text-xs font-normal', d.online ? 'text-emerald-700 dark:text-emerald-400' : 'muted')}>
                      {d.online ? t.wall.online : d.last_seen ? t.wall.lastSeen(relTime(d.last_seen, now)) : t.settings.devices.never}
                    </span>
                  )}
                </div>
                <div className="muted mt-0.5 flex flex-wrap gap-x-3 text-xs">
                  {d.hostname && (
                    <span>
                      {t.settings.devices.hostname} <span className="font-mono">{d.hostname}</span>
                    </span>
                  )}
                  {d.platform && (
                    <span>
                      {t.settings.devices.platform} <span className="font-mono">{d.platform}</span>
                    </span>
                  )}
                  {d.version && (
                    <span>
                      {t.settings.devices.version} <span className="font-mono">{d.version}</span>
                    </span>
                  )}
                </div>
              </div>
              {!d.revoked && (
                <ConfirmButton
                  className="btn btn-danger btn-sm"
                  message={t.settings.devices.revokeConfirm}
                  confirmLabel={t.settings.devices.revoke}
                  onConfirm={() => run(() => unwrap(api.POST('/api/devices/{id}/revoke', { params: { path: { id: d.id } } })), t.settings.devices.revoked).then(() => qc.invalidateQueries({ queryKey: qk.devices }))}
                >
                  {t.settings.devices.revoke}
                </ConfirmButton>
              )}
            </li>
          ))}
        </ul>
      )}

      {adding && (
        <Modal
          title={t.settings.devices.add}
          onClose={() => setAdding(false)}
          footer={
            <>
              <button className="btn" onClick={() => setAdding(false)}>
                {t.common.cancel}
              </button>
              <button className="btn btn-primary" disabled={!name.trim() || busy} onClick={create}>
                {busy && <Spinner />}
                {t.common.create}
              </button>
            </>
          }
        >
          <form
            onSubmit={(e) => {
              e.preventDefault()
              if (name.trim()) void create()
            }}
          >
            <Field label={t.settings.devices.name}>
              <input className="input" autoFocus maxLength={64} placeholder={t.settings.devices.namePlaceholder} value={name} onChange={(e) => setName(e.target.value)} />
            </Field>
          </form>
        </Modal>
      )}

      {created && (
        <Modal title={`${t.settings.devices.created} · ${created.device.name}`} onClose={() => setCreated(null)} footer={<button className="btn btn-primary" onClick={() => setCreated(null)}>{t.common.close}</button>}>
          <div className="space-y-3">
            <p className="text-sm text-amber-800 dark:text-amber-300">{t.settings.devices.tokenOnce}</p>
            <pre className="rounded-lg bg-slate-900 p-3 font-mono text-xs break-all whitespace-pre-wrap text-slate-100 dark:bg-black">{cmd}</pre>
            <div className="flex gap-2">
              <CopyButton text={cmd} className="btn btn-primary btn-sm" />
            </div>
            <p className="muted text-xs">{t.settings.devices.token}</p>
            <div className="flex items-center gap-2">
              <code className="min-w-0 flex-1 rounded bg-slate-100 px-2 py-1 font-mono text-xs break-all dark:bg-slate-800">{created.token}</code>
              <CopyButton text={created.token} />
            </div>
          </div>
        </Modal>
      )}
    </Section>
  )
}

// ---------- 通知配置表单 ----------

function NotifyForm({ value, onChange, events }: { value: NotifyConfig; onChange: (v: NotifyConfig) => void; events: string[] }) {
  const chosen = value.events && value.events.length > 0 ? value.events : DEFAULT_EVENTS
  const toggle = (ev: string) => {
    const next = chosen.includes(ev) ? chosen.filter((x) => x !== ev) : [...chosen, ev]
    onChange({ ...value, events: events.filter((x) => next.includes(x)) })
  }
  return (
    <div className="space-y-3">
      <fieldset className="space-y-2">
        <legend className="label">{t.settings.notify.bark}</legend>
        <div className="grid gap-2 sm:grid-cols-[1fr_1fr]">
          <input
            className="input"
            placeholder={t.common.labeled(t.settings.notify.barkServer, 'https://api.day.app')}
            value={value.bark?.server ?? ''}
            onChange={(e) => onChange({ ...value, bark: { server: e.target.value, key: value.bark?.key ?? '' } })}
          />
          <input
            className="input font-mono"
            placeholder={t.settings.notify.barkKey}
            value={value.bark?.key ?? ''}
            onChange={(e) => onChange({ ...value, bark: { server: value.bark?.server ?? '', key: e.target.value } })}
          />
        </div>
      </fieldset>
      <Field label={t.settings.notify.wecom}>
        <input
          className="input font-mono"
          placeholder={t.settings.notify.wecomWebhook}
          value={value.wecom?.webhook ?? ''}
          onChange={(e) => onChange({ ...value, wecom: { webhook: e.target.value } })}
        />
      </Field>
      <fieldset>
        <legend className="label">{t.settings.notify.events}</legend>
        <div className="flex flex-wrap gap-2">
          {events.map((ev) => {
            const on = chosen.includes(ev)
            return (
              <button
                key={ev}
                type="button"
                onClick={() => toggle(ev)}
                className={cx(
                  'inline-flex min-h-9 items-center gap-1.5 rounded-full border px-3 text-sm',
                  on ? 'border-slate-900 bg-slate-900 text-white dark:border-slate-100 dark:bg-slate-100 dark:text-slate-900' : 'border-slate-300 dark:border-slate-700',
                )}
                aria-pressed={on}
              >
                {on ? '✓' : '+'} {t.settings.notify.eventNames[ev] ?? ev}
              </button>
            )
          })}
        </div>
        <p className="muted mt-1 text-xs">{t.settings.notify.eventsDefaultHint}</p>
      </fieldset>
    </div>
  )
}

// cleanNotify 去掉空渠道；Bark 未填服务器时使用默认服务器。
function cleanNotify(n: NotifyConfig): NotifyConfig {
  const out: NotifyConfig = {}
  if (n.bark?.key?.trim()) out.bark = { server: n.bark.server?.trim() || 'https://api.day.app', key: n.bark.key.trim() }
  if (n.wecom?.webhook?.trim()) out.wecom = { webhook: n.wecom.webhook.trim() }
  if (n.events && n.events.length) out.events = n.events
  return out
}

function TestNotifyButton({ value }: { value: NotifyConfig }) {
  const { busy, run } = useAsync()
  const toast = useToast()
  return (
    <button
      type="button"
      className="btn"
      disabled={busy}
      onClick={() => {
        const cfg = cleanNotify(value)
        if (!cfg.bark && !cfg.wecom) return toast(t.settings.notify.noChannel, 'err')
        void run(() => unwrap(api.POST('/api/notify/test', { body: cfg })), t.settings.notify.testOk)
      }}
    >
      {busy && <Spinner />}
      {t.settings.notify.test}
    </button>
  )
}

// ---------- 工作空间 ----------

function WorkspacesSection({ events }: { events: string[] }) {
  const q = useProjects()
  const qc = useQueryClient()
  const { busy, run } = useAsync()
  const ws = q.data?.workspaces ?? []

  return (
    <Section
      title={t.settings.workspaces.title}
      hint={t.settings.workspaces.hint}
      action={
        <button
          className="btn btn-primary btn-sm"
          disabled={busy}
          onClick={() => run(() => unwrap(api.POST('/api/workspaces', { body: { name: t.settings.workspaces.newName, notify: {} } }))).then(() => qc.invalidateQueries({ queryKey: qk.projects }))}
        >
          + {t.settings.workspaces.add}
        </button>
      }
    >
      {q.isPending ? (
        <Loading />
      ) : q.isError ? (
        <ErrorBox error={q.error} onRetry={() => q.refetch()} />
      ) : ws.length === 0 ? (
        <Empty>{t.settings.workspaces.empty}</Empty>
      ) : (
        <div className="space-y-3">
          {ws.map((w) => (
            <WorkspaceCard key={w.id} w={w} events={events} />
          ))}
        </div>
      )}
    </Section>
  )
}

function WorkspaceCard({ w, events }: { w: Workspace; events: string[] }) {
  const qc = useQueryClient()
  const { busy, run } = useAsync()
  const [name, setName] = useState(w.name)
  const [notify, setNotify] = useState<NotifyConfig>(w.notify ?? {})
  useEffect(() => {
    setName(w.name)
    setNotify(w.notify ?? {})
  }, [w])
  const dirty = name !== w.name || JSON.stringify(cleanNotify(notify)) !== JSON.stringify(cleanNotify(w.notify ?? {}))

  return (
    <div className="card space-y-3 p-3">
      <Field label={t.settings.workspaces.name}>
        <input className="input font-medium" value={name} onChange={(e) => setName(e.target.value)} />
      </Field>
      <NotifyForm value={notify} onChange={setNotify} events={events} />
      <div className="flex flex-wrap items-center gap-2">
        <ConfirmButton
          className="btn btn-danger btn-sm"
          message={t.settings.workspaces.deleteConfirm}
          confirmLabel={t.common.delete}
          onConfirm={() => run(() => unwrap(api.DELETE('/api/workspaces/{id}', { params: { path: { id: w.id } } }))).then(() => qc.invalidateQueries({ queryKey: qk.projects }))}
        >
          {t.common.delete}
        </ConfirmButton>
        <span className="ml-auto" />
        <TestNotifyButton value={notify} />
        <button
          className="btn btn-primary"
          disabled={!dirty || !name.trim() || busy}
          onClick={() =>
            run(() => unwrap(api.PUT('/api/workspaces/{id}', { params: { path: { id: w.id } }, body: { name: name.trim(), notify: cleanNotify(notify) } })), t.common.saved).then(() =>
              qc.invalidateQueries({ queryKey: qk.projects }),
            )
          }
        >
          {busy && <Spinner />}
          {t.common.save}
        </button>
      </div>
    </div>
  )
}

// ---------- 项目 ----------

function ProjectsSection() {
  const q = useProjects()
  return (
    <Section title={t.settings.projects.title} hint={t.settings.projects.hint}>
      {q.isPending ? (
        <Loading />
      ) : q.isError ? (
        <ErrorBox error={q.error} onRetry={() => q.refetch()} />
      ) : q.data.projects.length === 0 ? (
        <Empty>{t.settings.projects.empty}</Empty>
      ) : (
        <ul className="card divide-y divide-slate-100 dark:divide-slate-800">
          {q.data.projects.map((p) => (
            <ProjectRow key={p.id} p={p} workspaces={q.data.workspaces} />
          ))}
        </ul>
      )}
    </Section>
  )
}

function ProjectRow({ p, workspaces }: { p: Project; workspaces: Workspace[] }) {
  const qc = useQueryClient()
  const { busy, run } = useAsync()
  const [name, setName] = useState(p.name)
  const [ws, setWs] = useState(p.workspace_id ?? '')
  const [doc, setDoc] = useState(p.doc_root)
  useEffect(() => {
    setName(p.name)
    setWs(p.workspace_id ?? '')
    setDoc(p.doc_root)
  }, [p])
  const dirty = name !== p.name || ws !== (p.workspace_id ?? '') || doc !== p.doc_root

  return (
    <li className="space-y-2 p-3">
      {p.remote && <div className="muted font-mono text-xs break-all">{p.remote}</div>}
      <div className="grid gap-2 sm:grid-cols-[1fr_10rem]">
        <input className="input" aria-label={t.settings.projects.name} value={name} onChange={(e) => setName(e.target.value)} />
        <select className="input" aria-label={t.settings.projects.workspace} value={ws} onChange={(e) => setWs(e.target.value)}>
          <option value="">{t.settings.projects.noWorkspace}</option>
          {workspaces.map((w) => (
            <option key={w.id} value={w.id}>
              {w.name}
            </option>
          ))}
        </select>
      </div>
      <div className="flex gap-2">
        <input className="input flex-1" aria-label={t.settings.projects.docRoot} placeholder={t.common.labeled(t.settings.projects.docRoot, t.settings.projects.docRootPlaceholder)} value={doc} onChange={(e) => setDoc(e.target.value)} />
        <button
          className="btn btn-primary"
          disabled={!dirty || !name.trim() || busy}
          onClick={() =>
            run(() => unwrap(api.PUT('/api/projects/{id}', { params: { path: { id: p.id } }, body: { name: name.trim(), workspace_id: ws || undefined, doc_root: doc.trim() } })), t.common.saved).then(() =>
              qc.invalidateQueries({ queryKey: qk.projects }),
            )
          }
        >
          {t.common.save}
        </button>
      </div>
    </li>
  )
}

// ---------- 全局设置（默认通知、摘要、公网地址共用一次 PUT） ----------

type SettingsPatch = { public_url?: string; summary_mode?: SettingsView['summary_mode']; summary_base_url?: string; summary_model?: string; summary_api_key?: string; notify_default?: NotifyConfig }

function useSaveSettings() {
  const qc = useQueryClient()
  return async (s: SettingsView, patch: SettingsPatch) => {
    const body = {
      public_url: s.public_url,
      summary_mode: s.summary_mode,
      summary_base_url: s.summary_base_url,
      summary_model: s.summary_model,
      notify_default: cleanNotify(s.notify_default ?? {}),
      ...patch,
    }
    const out = await unwrap(api.PUT('/api/settings', { body }))
    qc.setQueryData(qk.settings, out)
    return out
  }
}

function DefaultNotifySection({ s }: { s: SettingsView }) {
  const save = useSaveSettings()
  const { busy, run } = useAsync()
  const [notify, setNotify] = useState<NotifyConfig>(s.notify_default ?? {})
  useEffect(() => setNotify(s.notify_default ?? {}), [s.notify_default])
  const dirty = JSON.stringify(cleanNotify(notify)) !== JSON.stringify(cleanNotify(s.notify_default ?? {}))
  return (
    <Section title={t.settings.defaultNotify.title} hint={t.settings.defaultNotify.hint}>
      <div className="card space-y-3 p-3">
        <NotifyForm value={notify} onChange={setNotify} events={s.notify_events ?? ALL_EVENTS} />
        <div className="flex justify-end gap-2">
          <TestNotifyButton value={notify} />
          <button className="btn btn-primary" disabled={!dirty || busy} onClick={() => run(() => save(s, { notify_default: cleanNotify(notify) }), t.common.saved)}>
            {busy && <Spinner />}
            {t.common.save}
          </button>
        </div>
      </div>
    </Section>
  )
}

function SummarySection({ s }: { s: SettingsView }) {
  const save = useSaveSettings()
  const { busy, run } = useAsync()
  const [mode, setMode] = useState(s.summary_mode || 'off')
  const [base, setBase] = useState(s.summary_base_url)
  const [model, setModel] = useState(s.summary_model)
  const [key, setKey] = useState('')
  const [result, setResult] = useState('')
  useEffect(() => {
    setMode(s.summary_mode || 'off')
    setBase(s.summary_base_url)
    setModel(s.summary_model)
  }, [s.summary_mode, s.summary_base_url, s.summary_model])

  const patch = (): SettingsPatch => ({ summary_mode: mode, summary_base_url: base.trim(), summary_model: model.trim(), ...(key ? { summary_api_key: key } : {}) })
  const doSave = () =>
    run(async () => {
      await save(s, patch())
      setKey('')
    }, t.common.saved)
  const doTest = () =>
    run(async () => {
      setResult('')
      await save(s, patch())
      setKey('')
      const out = await unwrap(api.POST('/api/settings/test-summary'))
      setResult(out.summary)
    })

  return (
    <Section title={t.settings.summary.title} hint={t.settings.summary.hint}>
      <div className="card space-y-3 p-3">
        <div>
          <span className="label">{t.settings.summary.mode}</span>
          <div className="inline-flex w-full rounded-lg bg-slate-100 p-1 sm:w-auto dark:bg-slate-800">
            {(['off', 'hub', 'device'] as const).map((m) => (
              <button
                key={m}
                type="button"
                onClick={() => setMode(m)}
                className={cx('h-9 flex-1 rounded-md px-3 text-sm font-medium whitespace-nowrap sm:flex-none', mode === m ? 'bg-white shadow-sm dark:bg-slate-950' : 'muted')}
              >
                {t.settings.summary.modes[m]}
              </button>
            ))}
          </div>
        </div>
        <div className="grid gap-3 sm:grid-cols-2">
          <Field label={t.settings.summary.baseUrl}>
            <input className="input font-mono" placeholder="https://api.openai.com/v1" value={base} onChange={(e) => setBase(e.target.value)} />
          </Field>
          <Field label={t.settings.summary.model}>
            <input className="input font-mono" value={model} onChange={(e) => setModel(e.target.value)} />
          </Field>
        </div>
        <Field label={t.settings.summary.apiKey} hint={s.summary_api_key_set ? t.settings.summary.apiKeySet : t.settings.summary.apiKeyUnset}>
          <input className="input font-mono" type="password" autoComplete="off" placeholder={s.summary_api_key_set ? '••••••••' : ''} value={key} onChange={(e) => setKey(e.target.value)} />
        </Field>
        {result && (
          <div className="rounded-lg bg-emerald-50 px-3 py-2 text-sm dark:bg-emerald-950/40">
            <span className="mr-1.5 text-xs font-semibold text-emerald-800 dark:text-emerald-300">{t.settings.summary.testResult}</span>
            {result}
          </div>
        )}
        <div className="flex justify-end gap-2">
          <button className="btn" disabled={busy} onClick={doTest}>
            {t.settings.summary.test}
          </button>
          <button className="btn btn-primary" disabled={busy} onClick={doSave}>
            {busy && <Spinner />}
            {t.common.save}
          </button>
        </div>
      </div>
    </Section>
  )
}

function PublicUrlSection({ s }: { s: SettingsView }) {
  const save = useSaveSettings()
  const { busy, run } = useAsync()
  const [url, setUrl] = useState(s.public_url)
  useEffect(() => setUrl(s.public_url), [s.public_url])
  return (
    <Section title={t.settings.publicUrl.title} hint={t.settings.publicUrl.hint}>
      <div className="card flex gap-2 p-3">
        <input className="input flex-1 font-mono" aria-label={t.settings.publicUrl.label} placeholder={window.location.origin} value={url} onChange={(e) => setUrl(e.target.value)} />
        <button className="btn btn-primary" disabled={busy || url === s.public_url} onClick={() => run(() => save(s, { public_url: url.trim() }), t.common.saved)}>
          {t.common.save}
        </button>
      </div>
    </Section>
  )
}

// ---------- 账户 ----------

function AccountSection({ version }: { version: string }) {
  const qc = useQueryClient()
  const toast = useToast()
  const { busy, run } = useAsync()
  const [oldPw, setOld] = useState('')
  const [pw, setPw] = useState('')
  const [pw2, setPw2] = useState('')

  const change = () => {
    if (pw.length < 8) return toast(t.auth.tooShort, 'err')
    if (pw !== pw2) return toast(t.auth.mismatch, 'err')
    void run(() => unwrap(api.POST('/api/password', { body: { old: oldPw, new: pw } })), t.settings.password.changed).then((ok) => {
      if (ok) {
        setOld('')
        setPw('')
        setPw2('')
      }
    })
  }

  const logout = async () => {
    try {
      await api.POST('/api/auth/logout')
    } finally {
      qc.removeQueries({ predicate: (q) => q.queryKey[0] !== qk.auth[0] })
      qc.setQueryData(qk.auth, { initialized: true, logged_in: false, version })
    }
  }

  return (
    <Section title={t.settings.password.title}>
      <form
        className="card space-y-3 p-3"
        onSubmit={(e) => {
          e.preventDefault()
          change()
        }}
      >
        <div className="grid gap-3 sm:grid-cols-3">
          <Field label={t.settings.password.old}>
            <input className="input" type="password" autoComplete="current-password" value={oldPw} onChange={(e) => setOld(e.target.value)} />
          </Field>
          <Field label={t.settings.password.new}>
            <input className="input" type="password" autoComplete="new-password" value={pw} onChange={(e) => setPw(e.target.value)} />
          </Field>
          <Field label={t.settings.password.confirm}>
            <input className="input" type="password" autoComplete="new-password" value={pw2} onChange={(e) => setPw2(e.target.value)} />
          </Field>
        </div>
        <div className="flex justify-end">
          <button className="btn btn-primary" disabled={busy || !oldPw || !pw}>
            {busy && <Spinner />}
            {t.settings.password.title}
          </button>
        </div>
      </form>
      <div className="flex items-center justify-between pt-2">
        <span className="muted text-xs">{t.settings.version(version)}</span>
        <button className="btn btn-danger" onClick={logout}>
          {t.settings.logout}
        </button>
      </div>
    </Section>
  )
}
