import { useState } from 'react'
import { Check, Cloud, Loader2 } from 'lucide-react'
import type { ScalingGroup } from '../lib/types'
import { usePolling } from '../lib/usePolling'

interface KubeEvent {
  metadata: { name: string; creationTimestamp: string }
  type: string
  reason: string
  message: string
  count?: number
  lastTimestamp?: string
  eventTime?: string
}

const eventTime = (e: KubeEvent) => e.lastTimestamp || e.eventTime || e.metadata.creationTimestamp

// stagesOf mirrors the operator: sequence stages first, then every namespace not in any
// stage as one final stage; scale-down runs them in reverse.
function stagesOf(group: ScalingGroup): string[][] {
  const stages = (group.spec.sequence || []).map(s => s.split(/\s+/).filter(Boolean)).filter(s => s.length > 0)
  const placed = new Set(stages.flat())
  const rest = group.spec.namespaces.filter(ns => !placed.has(ns))
  if (rest.length) stages.push(rest)
  const down = group.status?.desiredState
    ? group.status.desiredState === 'Down'
    : group.status?.phase === 'ScalingDown' || group.status?.phase === 'ScaledDown'
  return down ? stages.reverse() : stages
}

// ScheduleActivity shows where a running scale-up or scale-down is, stage by stage, and
// the events the operator recorded for the schedule.
export default function ScheduleActivity({ group: initial }: { group: ScalingGroup }) {
  const [group, setGroup] = useState<ScalingGroup>(initial)
  const [events, setEvents] = useState<KubeEvent[]>([])
  const name = initial.metadata.name

  usePolling(() => {
    fetch(`/api/scaling/groups/${name}`).then(r => (r.ok ? r.json() : null)).then(g => { if (g) setGroup(g) }).catch(() => {})
    fetch(`/api/scaling/groups/${name}/events`).then(r => (r.ok ? r.json() : [])).then((list: KubeEvent[]) =>
      setEvents([...(list || [])].sort((a, b) => new Date(eventTime(b)).getTime() - new Date(eventTime(a)).getTime())),
    ).catch(() => {})
  }, 3000, name)

  const phase = group.status?.phase
  const busy = phase === 'ScalingUp' || phase === 'ScalingDown' || phase === 'WaitingForDependencies'
  const ready = new Set(group.status?.readyNamespaces || [])
  const stages = stagesOf(group)
  const down = group.status?.desiredState === 'Down'

  return (
    <div className="space-y-6">
      <div>
        <div className="flex items-center justify-between mb-3">
          <h3 className="text-sm font-semibold text-slate-800">{down ? 'Scale-down' : 'Scale-up'} progress</h3>
          <span className="text-xs text-slate-500">{group.status?.namespacesReady || 0} of {group.status?.namespacesTotal || group.spec.namespaces.length} ready</span>
        </div>
        <ol className="space-y-2">
          {stages.map((stage, i) => {
            const done = stage.every(item => ready.has(item) || item.startsWith('ext:') && !busy)
            const current = busy && !done && stages.slice(0, i).every(s => s.every(item => ready.has(item)))
            return (
              <li key={i} className={`rounded-xl border p-3 ${current ? 'border-sky-200 bg-sky-50/50' : 'border-slate-200'}`}>
                <div className="flex items-center gap-2 mb-2">
                  <span className={`w-6 h-6 rounded-full flex items-center justify-center text-xs font-bold ${
                    done ? 'bg-emerald-100 text-emerald-700' : current ? 'bg-sky-100 text-sky-700' : 'bg-slate-100 text-slate-500'}`}>
                    {done ? <Check size={13} strokeWidth={3} /> : current ? <Loader2 size={13} className="animate-spin" /> : i + 1}
                  </span>
                  <span className="text-sm font-semibold text-slate-700">Stage {i + 1}</span>
                  <span className="text-xs text-slate-400">{done ? 'done' : current ? 'in progress' : 'waiting'}</span>
                </div>
                <div className="flex flex-wrap gap-1.5">
                  {stage.map(item => {
                    const ext = item.startsWith('ext:')
                    const ok = ready.has(item)
                    return (
                      <span key={item} className={`inline-flex items-center gap-1.5 px-2 py-0.5 rounded-md border text-xs ${
                        ok ? 'border-emerald-200 bg-emerald-50 text-emerald-800' : current ? 'border-sky-200 bg-white text-sky-800' : 'border-slate-200 bg-white text-slate-600'}`}>
                        {ext ? <Cloud size={11} /> : <span className={`w-1.5 h-1.5 rounded-full ${ok ? 'bg-emerald-500' : current ? 'bg-sky-500 animate-pulse' : 'bg-slate-300'}`} />}
                        {ext ? item.slice(4) : item}
                      </span>
                    )
                  })}
                </div>
              </li>
            )
          })}
        </ol>
      </div>

      <div>
        <h3 className="text-sm font-semibold text-slate-800 mb-3">Events</h3>
        {events.length === 0 ? (
          <p className="text-sm text-slate-400">No events yet. They appear as soon as the schedule scales something.</p>
        ) : (
          <ul className="divide-y divide-slate-100 rounded-xl border border-slate-200">
            {events.slice(0, 50).map(e => (
              <li key={e.metadata.name} className="px-3 py-2 flex gap-3 text-sm">
                <span className="w-16 shrink-0 text-xs text-slate-400 tabular-nums pt-0.5">
                  {new Date(eventTime(e)).toLocaleTimeString([], { hour: '2-digit', minute: '2-digit' })}
                </span>
                <span className="min-w-0">
                  <span className={`text-xs font-semibold ${e.type === 'Warning' ? 'text-amber-700' : 'text-slate-500'}`}>{e.reason}</span>
                  <span className="block text-slate-700">{e.message}{(e.count || 0) > 1 && <span className="text-slate-400"> ×{e.count}</span>}</span>
                </span>
              </li>
            ))}
          </ul>
        )}
      </div>
    </div>
  )
}
