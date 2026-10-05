import { useEffect, useRef, useState } from 'react'
import { Loader2, ShieldAlert } from 'lucide-react'

// AuthCallback completes a Microsoft sign-in when the app registration uses the
// single-page-app redirect URI (https://<host>/auth/callback): it hands the code and
// state to the backend, which verifies them and sets the session cookie.
export default function AuthCallback({ onSignedIn }: { onSignedIn: (returnTo: string) => void }) {
  const [error, setError] = useState<string | null>(null)
  const started = useRef(false)

  useEffect(() => {
    if (started.current) return
    started.current = true
    const params = new URLSearchParams(window.location.search)
    fetch('/api/auth/entra/callback-spa', {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({
        code: params.get('code') || '',
        state: params.get('state') || '',
        error: params.get('error') || '',
        error_description: params.get('error_description') || '',
      }),
    })
      .then(async res => {
        const data = await res.json().catch(() => ({}))
        if (!res.ok) throw new Error(data.detail || data.error || 'Sign-in failed')
        onSignedIn(data.returnTo || '/')
      })
      .catch(err => setError(err.message))
  }, [onSignedIn])

  return (
    <div className="min-h-screen bg-slate-900 flex items-center justify-center p-4">
      <div className="bg-white/5 border border-white/10 rounded-xl p-8 max-w-md w-full text-center">
        {error ? (
          <>
            <ShieldAlert className="mx-auto text-rose-400 mb-4" size={36} />
            <h1 className="text-white font-bold text-lg mb-2">Microsoft sign-in failed</h1>
            <p className="text-slate-400 text-sm mb-6">{error}</p>
            <a href="/" className="inline-block px-5 py-2.5 bg-emerald-500 hover:bg-emerald-400 text-white font-bold rounded-xl text-sm">Back to sign-in</a>
          </>
        ) : (
          <>
            <Loader2 className="mx-auto text-emerald-400 animate-spin mb-4" size={36} />
            <p className="text-slate-300 text-sm">Completing Microsoft sign-in…</p>
          </>
        )}
      </div>
    </div>
  )
}
