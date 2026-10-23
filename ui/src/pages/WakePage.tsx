import { useState } from 'react'
import { AlarmClock, CheckCircle2, ExternalLink, Loader2 } from 'lucide-react'
import { apiError, errorMessage } from '../lib/api'
import { useAuth } from '../lib/auth'
import { usePolling } from '../lib/usePolling'
import { fetchWakeStatus, isAwake, safeReturn, type WakeStatus } from '../lib/wake'
import { Button, PageHeader } from '../components/ui'

const durations = [['1h', '1 hour'], ['4h', '4 hours'], ['8h', '8 hours']] as const

// WakePage starts a sleeping namespace for a while (a manual override with an end) and
// then leads back to the application: /wake/<namespace>?return=<address it was opened at>.
export default function WakePage({ namespace }: { namespace: string }) {
  const { user, can } = useAuth()
  const [status, setStatus] = useState<WakeStatus | null>(null)
  const [error, setError] = useState<string | null>(null)
  const [waking, setWaking] = useState<string | null>(null)
  // Without a user, authentication is off and everyone may.
  const canWake = !user || can('operator')
  usePolling(() => {
    fetchWakeStatus(namespace).then(s => { setStatus(s); setError(null) }).catch(e => setError(errorMessage(e)))
  }, waking ? 5000 : 30000, namespace)

  const wake = async (until: string) => {
    if (!status?.schedule) return
    setWaking(until)
    try {
      const kind = status.schedule.kind === 'group' ? 'groups' : 'configs'
      const res = await fetch(`/api/scaling/${kind}/${encodeURIComponent(status.schedule.name)}/manual`, {
        method: 'POST', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify({ active: true, until }),
      })
      if (!res.ok) throw new Error(await apiError(res))
    } catch (e) {
      setError(errorMessage(e))
      setWaking(null)
    }
  }

  const back = status ? safeReturn(new URLSearchParams(window.location.search).get('return'), status.hostnames) : null
  const awake = status ? isAwake(status) : false
  return (
    <div className="p-8 max-w-2xl mx-auto" data-testid="wake-page">
      <PageHeader title={`Wake ${namespace}`} subtitle="Start a namespace its schedule keeps down, for a while" />
      {error && <p className="mb-4 text-sm text-rose-600">{error}</p>}
      {!status ? <div className="flex items-center gap-2 text-sm text-slate-400"><Loader2 size={16} className="animate-spin" /> Loading…</div>
        : !status.schedule ? <p className="text-sm text-slate-600">No schedule scales {namespace}, so there is nothing to wake.</p>
        : awake ? (
          <div className="rounded-xl border border-emerald-200 bg-emerald-50 p-5 text-sm text-emerald-800">
            <p className="flex items-center gap-2 font-semibold"><CheckCircle2 size={16} /> {namespace} is up{status.overrideExpiresAt && ` until ${new Date(status.overrideExpiresAt).toLocaleTimeString()}`}.</p>
            {back && <a href={back} className="mt-3 inline-flex items-center gap-1.5 font-semibold underline"><ExternalLink size={14} /> Open {new URL(back).hostname}</a>}
          </div>
        ) : waking || status.desiredState === 'Up' ? (
          <p className="flex items-center gap-2 text-sm text-slate-600"><Loader2 size={16} className="animate-spin" /> Starting {namespace}… this page leads back to the application once it is up.</p>
        ) : (
          <div className="rounded-xl border border-slate-200 bg-white p-5">
            <p className="text-sm text-slate-600 mb-4">{namespace} is asleep: <b>{status.schedule.name}</b> keeps it down. Start it for</p>
            <div className="flex flex-wrap gap-2">
              {durations.map(([until, label]) => (
                <Button key={until} variant="primary" icon={<AlarmClock size={15} />} disabled={!canWake} onClick={() => wake(until)}>{label}</Button>
              ))}
            </div>
            {!canWake && <p className="mt-3 text-xs text-amber-700">Waking needs the operator role.</p>}
          </div>
        )}
    </div>
  )
}
