import { useState, type FormEvent } from 'react'
import { motion } from 'motion/react'
import { useQueryClient } from '@tanstack/react-query'
import { api, errMsg, unwrap } from '../api/client'
import { qk } from '../api/queries'
import { t } from '../i18n'
import { Logo } from '../app/Shell'
import { Button, Field, Input } from '../ui/base'

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
      if (pw.length < 8) return setErr(t.login.tooShort)
      if (pw !== pw2) return setErr(t.login.mismatch)
    }
    setBusy(true)
    try {
      await unwrap(initialized ? api.POST('/api/auth/login', { body: { password: pw } }) : api.POST('/api/auth/setup', { body: { password: pw } }))
      await qc.invalidateQueries({ queryKey: qk.auth })
    } catch (e) {
      setErr(errMsg(e))
      setBusy(false)
    }
  }

  return (
    <div className="relative flex min-h-dvh items-center justify-center overflow-hidden px-4">
      <div className="pointer-events-none absolute -top-40 left-1/2 size-[560px] -translate-x-1/2 rounded-full bg-seal/8 blur-3xl" />
      <motion.div initial={{ opacity: 0, y: 12 }} animate={{ opacity: 1, y: 0 }} transition={{ duration: 0.4, ease: [0.22, 1, 0.36, 1] }} className="relative w-full max-w-sm">
        <div className="mb-8 text-center">
          <Logo className="kite-float mx-auto mb-4 size-16" />
          <h1 className="font-display text-[26px] text-fg">{initialized ? t.login.welcome : t.login.setupTitle}</h1>
          {!initialized && <p className="mt-1.5 text-sm text-fg-3">{t.login.setupSub}</p>}
        </div>
        <form onSubmit={submit} className="space-y-4 rounded-2xl border border-line bg-surface p-6 shadow-pop">
          <Field label={t.login.password}>
            <Input type="password" autoFocus autoComplete={initialized ? 'current-password' : 'new-password'} value={pw} onChange={(e) => setPw(e.target.value)} className="h-11" />
          </Field>
          {!initialized && (
            <Field label={t.login.passwordAgain}>
              <Input type="password" autoComplete="new-password" value={pw2} onChange={(e) => setPw2(e.target.value)} className="h-11" />
            </Field>
          )}
          {err && (
            <motion.p initial={{ opacity: 0, x: -4 }} animate={{ opacity: 1, x: [0, -4, 4, -2, 0] }} className="text-sm text-danger">
              {err}
            </motion.p>
          )}
          <Button type="submit" variant="primary" size="lg" className="w-full" loading={busy} disabled={!pw}>
            {initialized ? t.login.login : t.login.setup}
          </Button>
        </form>
        <p className="mt-6 text-center text-xs text-fg-3">{t.app.slogan}</p>
      </motion.div>
    </div>
  )
}
