import { useState, type FormEvent } from 'react'
import { useQueryClient } from '@tanstack/react-query'
import { api, errMsg, unwrap } from '../api/client'
import { qk } from '../api/queries'
import { KiteMark } from '../components/Layout'
import { Field, Spinner } from '../components/ui'
import { t } from '../i18n'

export default function LoginPage({ initialized }: { initialized: boolean }) {
  const qc = useQueryClient()
  const [pw, setPw] = useState('')
  const [pw2, setPw2] = useState('')
  const [err, setErr] = useState('')
  const [busy, setBusy] = useState(false)

  const submit = async (e: FormEvent) => {
    e.preventDefault()
    setErr('')
    if (!initialized) {
      if (pw.length < 8) return setErr(t.auth.tooShort)
      if (pw !== pw2) return setErr(t.auth.mismatch)
    }
    setBusy(true)
    try {
      if (initialized) await unwrap(api.POST('/api/auth/login', { body: { password: pw } }))
      else await unwrap(api.POST('/api/auth/setup', { body: { password: pw } }))
      qc.removeQueries({ predicate: (q) => q.queryKey[0] !== qk.auth[0] })
      qc.setQueryData(qk.auth, await unwrap(api.GET('/api/auth/status')))
    } catch (e) {
      setErr(errMsg(e))
    } finally {
      setBusy(false)
    }
  }

  return (
    <div className="pt-safe flex min-h-dvh items-center justify-center px-4">
      <form onSubmit={submit} className="card w-full max-w-sm space-y-4 p-6">
        <div className="flex items-center gap-3">
          <KiteMark className="size-10" />
          <div>
            <div className="text-lg font-semibold">{t.app.fullName}</div>
            <div className="muted text-xs">{t.app.slogan}</div>
          </div>
        </div>
        <div>
          <h1 className="text-base font-semibold">{initialized ? t.auth.loginTitle : t.auth.setupTitle}</h1>
          <p className="muted mt-1 text-sm">{initialized ? t.auth.loginHint : t.auth.setupHint}</p>
        </div>
        <Field label={t.auth.password}>
          <input className="input" type="password" autoFocus autoComplete={initialized ? 'current-password' : 'new-password'} value={pw} onChange={(e) => setPw(e.target.value)} />
        </Field>
        {!initialized && (
          <Field label={t.auth.passwordConfirm}>
            <input className="input" type="password" autoComplete="new-password" value={pw2} onChange={(e) => setPw2(e.target.value)} />
          </Field>
        )}
        {err && <p className="text-sm text-red-600 dark:text-red-400">{err}</p>}
        <button className="btn btn-primary w-full" disabled={busy || !pw}>
          {busy && <Spinner />}
          {initialized ? t.auth.loginSubmit : t.auth.setupSubmit}
        </button>
      </form>
    </div>
  )
}
