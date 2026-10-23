import { useEffect, useState } from 'react'
import { AlertTriangle, Loader2, XCircle } from 'lucide-react'
import { apiError, errorMessage } from '../lib/api'
import { formatMoney } from '../lib/format'
import { dayStrips } from '../lib/preview'
import { when } from '../lib/schedule'
import type { SchedulePreview as Preview, ScalingSpec } from '../lib/types'
import { Badge } from './ui'

const DAYS = 14

// SchedulePreview shows what a schedule would do, saved or not, as the operator works it
// out (POST /api/scaling/preview): what it scales, when, and what that saves.
export default function SchedulePreview({ kind, name, spec }: { kind: 'group' | 'config'; name?: string; spec: ScalingSpec }) {
  const body = JSON.stringify({ kind, name, spec })
  const [result, setResult] = useState<{ body: string; preview?: Preview; error?: string } | null>(null)
  useEffect(() => {
    const t = setTimeout(() => {
      fetch('/api/scaling/preview', { method: 'POST', headers: { 'Content-Type': 'application/json' }, body })
        .then(async res => (res.ok ? setResult({ body, preview: await res.json() }) : setResult({ body, error: await apiError(res) })))
        .catch(e => setResult({ body, error: errorMessage(e) }))
    }, 300)
    return () => clearTimeout(t)
  }, [body])

  const current = result?.body === body ? result : null
  if (!current) return <div className="flex items-center gap-2 text-sm text-slate-400"><Loader2 size={16} className="animate-spin" /> Working it out…</div>
  if (current.error) return <p className="text-sm text-rose-600">Could not preview the schedule: {current.error}</p>
  const p = current.preview!
  const workloads = p.namespaces.reduce((n, ns) => n + (ns.ownedBy && kind === 'group' ? 0 : (ns.workloads || []).filter(w => !w.excluded).length), 0)

  return (
    <div className="space-y-5" data-testid="schedule-preview">
      <section className="rounded-xl border border-brand-200 bg-brand-50 px-4 py-3">
        {p.onDemand
          ? <p className="text-sm text-brand-800"><b>On demand:</b> runs only while a schedule that depends on it needs it.</p>
          : <p className="text-sm text-brand-800">
              Saves about <b>{formatMoney(p.savings.monthly, p.savings.currency)} a month</b>: {workloads} workload{workloads === 1 ? '' : 's'} stopped,
              up {Math.round(p.upShare * 100)}% of the next four weeks{p.nextChange ? `, next change ${when(p.nextChange)}` : ''}.
            </p>}
        <p className="text-xs text-brand-700/80 mt-0.5">{formatMoney(p.savings.hourly, p.savings.currency)} per hour while down · {p.savings.basis}</p>
      </section>

      {p.problems.map(m => <p key={m} className="flex items-start gap-2 text-sm text-rose-700"><XCircle size={15} className="mt-0.5 shrink-0" />{m}</p>)}
      {p.warnings.map(m => <p key={m} className="flex items-start gap-2 text-sm text-amber-800"><AlertTriangle size={15} className="mt-0.5 shrink-0" />{m}</p>)}

      <section>
        <h3 className="text-sm font-semibold text-slate-800 mb-2">Next two weeks</h3>
        <div className="space-y-1">
          {dayStrips(p.intervals, DAYS).map(({ day, segments }) => (
            <div key={day.toISOString()} className="flex items-center gap-2">
              <span className="w-20 shrink-0 text-xs text-slate-500">{day.toLocaleDateString(undefined, { weekday: 'short', month: 'short', day: 'numeric' })}</span>
              <div className="relative h-3 flex-1 rounded bg-slate-100 overflow-hidden">
                {segments.map((s, i) => (
                  <div key={i} title={`${s.active ? 'Up' : 'Down'} · ${s.reason}`}
                    className={`absolute inset-y-0 ${s.active ? 'bg-brand-500' : s.reason === 'schedule' ? 'bg-slate-200' : 'bg-amber-300'}`}
                    style={{ left: `${s.start}%`, width: `${s.end - s.start}%` }} />
                ))}
              </div>
              <span className="w-28 shrink-0 truncate text-[11px] text-amber-700">{[...new Set(segments.filter(s => s.reason !== 'schedule').map(s => s.reason))].join(', ')}</span>
            </div>
          ))}
        </div>
        <p className="mt-1 text-[11px] text-slate-400"><span className="inline-block w-2 h-2 rounded-sm bg-brand-500 mr-1" />up <span className="inline-block w-2 h-2 rounded-sm bg-slate-200 mx-1" />down <span className="inline-block w-2 h-2 rounded-sm bg-amber-300 mx-1" />exception</p>
      </section>

      <section>
        <h3 className="text-sm font-semibold text-slate-800 mb-2">What it scales</h3>
        {p.namespaces.length === 0 && <p className="text-sm text-slate-400 italic">No namespaces yet.</p>}
        <ul className="space-y-1.5">
          {p.namespaces.map(ns => {
            const w = ns.workloads || []
            return (
              <li key={ns.name} className="text-sm text-slate-700">
                <span className="font-medium">{ns.name}</span>{' '}
                {ns.source === 'selected' && <Badge tone="info">by label</Badge>}{' '}
                {ns.missing ? <span className="text-xs text-rose-600">does not exist</span>
                  : ns.ownedBy && kind === 'group' ? <span className="text-xs text-amber-700">scaled by {ns.ownedBy}</span>
                  : <span className="text-xs text-slate-500">
                      {w.filter(x => !x.excluded).map(x => `${x.name} ×${x.replicas}${x.gitOps ? ` (${x.gitOps.kind === 'Application' ? 'Argo CD' : 'Flux'} ${x.gitOps.name})` : ''}`).join(', ') || 'no workloads'}
                      {w.some(x => x.excluded) && ` · kept running: ${w.filter(x => x.excluded).map(x => x.name).join(', ')}`}
                      {ns.cronJobs > 0 && ` · ${ns.cronJobs} CronJob${ns.cronJobs === 1 ? '' : 's'} paused`}
                    </span>}
              </li>
            )
          })}
        </ul>
        {p.cloudTargets.length > 0 && <p className="mt-2 text-xs text-slate-500">Also stops {p.cloudTargets.length} cloud resource{p.cloudTargets.length === 1 ? '' : 's'}.</p>}
      </section>
    </div>
  )
}
