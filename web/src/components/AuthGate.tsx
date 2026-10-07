import { useEffect, useState, type FormEvent, type ReactNode } from 'react'
import { KeyRound, LoaderCircle, ShieldCheck } from 'lucide-react'
import QRCode from 'qrcode'
import { demoMode } from '../api/apiClient'
import { authApi, type LoginChallenge, type Session } from '../api/authApi'

type Props = { children: (logout: () => Promise<void>) => ReactNode }

export function AuthGate({ children }: Props) {
  const [checking, setChecking] = useState(!demoMode)
  const [role, setRole] = useState<Session['role'] | undefined>(demoMode ? 'user' : undefined)
  const [username, setUsername] = useState('')
  const [password, setPassword] = useState('')
  const [code, setCode] = useState('')
  const [challenge, setChallenge] = useState<LoginChallenge>()
  const [qrCode, setQrCode] = useState('')
  const [error, setError] = useState('')
  const [submitting, setSubmitting] = useState(false)

  useEffect(() => {
    if (demoMode) return
    authApi.session().then((session) => setRole(session.role)).catch(() => setRole(undefined)).finally(() => setChecking(false))
  }, [])

  useEffect(() => {
    if (role === 'admin') window.location.replace('/admin/config')
  }, [role])

  useEffect(() => {
    if (!challenge?.provisioningUri) return
    QRCode.toDataURL(challenge.provisioningUri, { width: 220, margin: 1 }).then(setQrCode)
  }, [challenge])

  const submitCredentials = async (event: FormEvent) => {
    event.preventDefault()
    setSubmitting(true)
    setError('')
    setQrCode('')
    try {
      const result = await authApi.login(username, password)
      if (result.authenticated) setRole((await authApi.session()).role)
      else setChallenge(result)
      setPassword('')
    } catch {
      setError('The username or password is incorrect.')
    } finally {
      setSubmitting(false)
    }
  }

  const submitCode = async (event: FormEvent) => {
    event.preventDefault()
    if (!challenge) return
    setSubmitting(true)
    setError('')
    try {
      await authApi.verify(challenge.challengeToken, code)
      setRole((await authApi.session()).role)
    } catch {
      setError('That authentication code is invalid or expired.')
    } finally {
      setSubmitting(false)
    }
  }

  const logout = async () => {
    if (demoMode) return
    await authApi.logout()
    setRole(undefined)
    setChallenge(undefined)
    setCode('')
  }

  if (checking || role === 'admin') return <div className="auth-loading"><LoaderCircle className="spin" size={26} /><span>{role === 'admin' ? 'Opening user configuration' : 'Opening gallery'}</span></div>
  if (role === 'user') return children(logout)

  return (
    <main className="auth-page">
      <section className="auth-panel">
        <img className="auth-mark" src={`${import.meta.env.BASE_URL}icon.png`} alt="" />
        <p className="auth-kicker">Next Gallery</p>
        <h1>{challenge ? (challenge.provisioningUri ? 'Secure your gallery' : 'Verification') : 'Welcome back'}</h1>
        {!challenge ? (
          <form onSubmit={submitCredentials}>
            <label className="field-label">Username<input autoComplete="username" value={username} onChange={(event) => setUsername(event.target.value)} required autoFocus /></label>
            <label className="field-label">Password<input type="password" autoComplete="current-password" value={password} onChange={(event) => setPassword(event.target.value)} required /></label>
            {error && <p className="auth-error" role="alert">{error}</p>}
            <button className="primary-button auth-submit" disabled={submitting}><KeyRound size={17} /> Sign in</button>
          </form>
        ) : (
          <form onSubmit={submitCode}>
            {challenge.provisioningUri && <div className="totp-setup">{qrCode && <img src={qrCode} alt="Authenticator setup QR code" />}<p>Scan with your authenticator app, or enter this key:</p><code>{challenge.secret}</code></div>}
            <label className="field-label">6-digit code<input className="totp-input" inputMode="numeric" autoComplete="one-time-code" pattern="[0-9]{6}" maxLength={6} value={code} onChange={(event) => setCode(event.target.value.replace(/\D/g, ''))} required autoFocus /></label>
            {error && <p className="auth-error" role="alert">{error}</p>}
            <button className="primary-button auth-submit" disabled={submitting || code.length !== 6}><ShieldCheck size={17} /> Verify</button>
            <button className="text-button" type="button" onClick={() => { setChallenge(undefined); setError(''); setCode(''); setQrCode('') }}>Back to sign in</button>
          </form>
        )}
      </section>
    </main>
  )
}
