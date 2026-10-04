import { useEffect, useState } from 'react'
import { LogIn } from 'lucide-react'

interface LoginPageProps {
  onLogin: () => void;
}

interface AuthConfig {
  localLogin: boolean
  entra: { enabled: boolean }
}

const MicrosoftLogo = () => (
  <svg width="18" height="18" viewBox="0 0 21 21" aria-hidden="true">
    <rect x="1" y="1" width="9" height="9" fill="#f25022" />
    <rect x="11" y="1" width="9" height="9" fill="#7fba00" />
    <rect x="1" y="11" width="9" height="9" fill="#00a4ef" />
    <rect x="11" y="11" width="9" height="9" fill="#ffb900" />
  </svg>
)

// Messages for the sso_error codes the backend appends to the URL after a failed sign-in.
const SSO_ERRORS: Record<string, string> = {
  user_not_provisioned: 'Your Microsoft account is not allowed to use CostDeck. Ask an administrator to map one of your groups to a role.',
  sign_in_expired: 'The sign-in took too long or was started in another browser. Please try again.',
  access_denied: 'The Microsoft sign-in was cancelled.',
}

export default function LoginPage({ onLogin }: LoginPageProps) {
  const [username, setUsername] = useState('')
  const [password, setPassword] = useState('')
  const [error, setError] = useState('')
  const [loading, setLoading] = useState(false)
  const [config, setConfig] = useState<AuthConfig>({ localLogin: true, entra: { enabled: false } })

  useEffect(() => {
    fetch('/api/auth/config')
      .then(r => (r.ok ? r.json() : null))
      .then(d => { if (d) setConfig(d) })
      .catch(() => {})

    // A failed Microsoft sign-in comes back with ?sso_error=<code>&sso_detail=<text>.
    const params = new URLSearchParams(window.location.search)
    const code = params.get('sso_error')
    if (code) {
      setError(SSO_ERRORS[code] || params.get('sso_detail') || 'Microsoft sign-in failed.')
      params.delete('sso_error')
      params.delete('sso_detail')
      const rest = params.toString()
      window.history.replaceState(null, '', window.location.pathname + (rest ? `?${rest}` : ''))
    }
  }, [])

  const handleSubmit = async (e: React.FormEvent) => {
    e.preventDefault()
    setError('')
    setLoading(true)

    try {
      const res = await fetch('/api/login', {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ username, password })
      })

      if (!res.ok) {
        const data = await res.json().catch(() => ({}))
        setError(data.error || 'Authentication failed')
        return
      }

      onLogin()
    } catch {
      setError('Connection failed. Is the operator running?')
    } finally {
      setLoading(false)
    }
  }

  const signInWithMicrosoft = () => {
    const back = window.location.pathname + window.location.search
    window.location.href = `/api/auth/entra/login?return=${encodeURIComponent(back)}`
  }

  return (
    <div className="min-h-screen bg-gradient-to-br from-slate-900 via-slate-800 to-slate-900 flex items-center justify-center p-4">
      <div className="w-full max-w-md">
        {/* Logo Header */}
        <div className="text-center mb-8">
          <div className="inline-flex items-center justify-center w-16 h-16 bg-emerald-500/10 rounded-2xl mb-4">
            <svg xmlns="http://www.w3.org/2000/svg" width="36" height="36" viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="2" strokeLinecap="round" strokeLinejoin="round" className="text-emerald-400">
              <path d="M21 16V8a2 2 0 0 0-1-1.73l-7-4a2 2 0 0 0-2 0l-7 4A2 2 0 0 0 3 8v8a2 2 0 0 0 1 1.73l7 4a2 2 0 0 0 2 0l7-4A2 2 0 0 0 21 16z" />
              <polyline points="7 14 10 11 13 14 17 9" />
              <line x1="17" y1="9" x2="17" y2="13" />
              <line x1="17" y1="9" x2="13" y2="9" />
            </svg>
          </div>
          <h1 className="text-3xl font-bold text-white tracking-tight">Cost Deck</h1>
          <p className="text-slate-400 mt-2 text-sm">FinOps Platform UI</p>
        </div>

        <div className="bg-white/5 backdrop-blur-sm border border-white/10 rounded-2xl p-8 shadow-2xl">
          {error && (
            <div className="mb-6 p-3 bg-red-500/10 border border-red-500/20 rounded-lg text-red-400 text-sm text-center font-medium">
              {error}
            </div>
          )}

          {config.entra.enabled && (
            <button
              type="button"
              onClick={signInWithMicrosoft}
              className="w-full flex items-center justify-center gap-3 px-6 py-3 bg-white hover:bg-slate-100 text-slate-800 font-bold rounded-xl transition-all shadow-lg"
            >
              <MicrosoftLogo />
              Sign in with Microsoft
            </button>
          )}

          {config.entra.enabled && config.localLogin && (
            <div className="flex items-center gap-3 my-6 text-[10px] uppercase tracking-widest text-slate-500 font-bold">
              <div className="h-px flex-1 bg-white/10" /> or <div className="h-px flex-1 bg-white/10" />
            </div>
          )}

          {config.localLogin && (
            <form onSubmit={handleSubmit}>
              <div className="space-y-5">
                <div>
                  <label className="block text-xs font-medium text-slate-400 uppercase tracking-wider mb-2">
                    Username
                  </label>
                  <input
                    type="text"
                    value={username}
                    onChange={e => setUsername(e.target.value)}
                    className="w-full px-4 py-3 bg-slate-800/50 border border-slate-700 rounded-xl text-white placeholder-slate-500 focus:outline-none focus:ring-2 focus:ring-emerald-500/50 focus:border-emerald-500/50 transition-all"
                    placeholder="costdeck-admin"
                    autoFocus={!config.entra.enabled}
                    autoComplete="username"
                    required
                  />
                </div>

                <div>
                  <label className="block text-xs font-medium text-slate-400 uppercase tracking-wider mb-2">
                    Password
                  </label>
                  <input
                    type="password"
                    value={password}
                    onChange={e => setPassword(e.target.value)}
                    className="w-full px-4 py-3 bg-slate-800/50 border border-slate-700 rounded-xl text-white placeholder-slate-500 focus:outline-none focus:ring-2 focus:ring-emerald-500/50 focus:border-emerald-500/50 transition-all"
                    placeholder="••••••••••••"
                    autoComplete="current-password"
                    required
                  />
                </div>
              </div>

              <button
                type="submit"
                disabled={loading}
                className="w-full mt-8 flex items-center justify-center gap-2 px-6 py-3 bg-emerald-500 hover:bg-emerald-400 disabled:bg-emerald-500/50 text-white font-bold rounded-xl transition-all shadow-lg shadow-emerald-500/20"
              >
                <LogIn size={18} />
                {loading ? 'Signing in...' : 'Sign In'}
              </button>
            </form>
          )}

          <p className="text-center text-xs text-slate-500 mt-6">
            Every pod <code className="text-slate-400">counts.</code> Every dollar <code className="text-slate-400">matters.</code>
          </p>
        </div>
      </div>
    </div>
  )
}
