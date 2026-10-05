import { useState, type ReactNode } from 'react'
import { AlertTriangle, ArrowLeft, ArrowRight, Check, Clock, Cloud, Link2, Loader2, Moon } from 'lucide-react'
import { targetLabel } from '../lib/cloud'
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

interface Pod { name: string; status: string; ready: boolean; reason?: string; restarts: number }
interface Workload { name: string; kind: string; replicas: number; readyReplicas: number }

type NodeState = 'done' | 'running' | 'waiting' | 'problem' | 'idle'

const eventTime = (e: KubeEvent) => e.lastTimestamp || e.eventTime || e.metadata.creationTimestamp
const EXT = 'ext:'
const busyPhases = ['ScalingUp', 'ScalingDown', 'WaitingForDependencies']

// startStages mirrors the operator: sequence stages first, then every namespace not in any
// stage as one last stage. Scale-down runs them in reverse.
function startStages(group: ScalingGroup): string[][] {
  const stages = (group.spec.sequence || []).map(s => s.split(/\s+/).filter(Boolean)).filter(s => s.length > 0)
  const placed = new Set(stages.flat())
  const rest = group.spec.namespaces.filter(ns => !placed.has(ns))
  return rest.length ? [...stages, rest] : stages
}

const problemPod = (p: Pod) => !!p.reason || (p.status !== 'Running' && p.status !== 'Succeeded')

const nodeStyle: Record<NodeState, { box: string; icon: ReactNode; text: string }> = {
  done: { box: 'border-emerald-200 bg-emerald-50', icon: <Check size={13} className="text-emerald-600" strokeWidth={3} />, text: 'text-emerald-800' },
  running: { box: 'border-sky-300 bg-sky-50', icon: <Loader2 size={13} className="text-sky-600 animate-spin" />, text: 'text-sky-800' },
  waiting: { box: 'border-slate-200 bg-white', icon: <Clock size={13} className="text-slate-400" />, text: 'text-slate-500' },
  problem: { box: 'border-rose-300 bg-rose-50', icon: <AlertTriangle size={13} className="text-rose-600" />, text: 'text-rose-800' },
  idle: { box: 'border-slate-200 bg-slate-50', icon: <Moon size={13} className="text-slate-400" />, text: 'text-slate-600' },
}

function Node({ state, label, sub, icon, onClick, selected }: { state: NodeState; label: string; sub?: string; icon?: ReactNode; onClick?: () => void; selected?: boolean }) {
  const st = nodeStyle[state]
  return (
    <button type="button" onClick={onClick} disabled={!onClick}
      className={`w-44 text-left rounded-lg border px-2.5 py-2 transition-shadow ${st.box} ${onClick ? 'hover:shadow-sm' : 'cursor-default'} ${selected ? 'ring-2 ring-brand-500/50' : ''}`}>
      <div className="flex items-center gap-1.5">
        {st.icon}
        {icon}
        <span className={`text-xs font-semibold truncate ${st.text}`} title={label}>{label}</span>
      </div>
      {sub && <div className="mt-0.5 text-[11px] text-slate-500 truncate" title={sub}>{sub}</div>}
    </button>
  )
}

function Column({ title, hint, children }: { title: string; hint?: string; children: ReactNode }) {
  return (
    <div className="shrink-0 flex flex-col gap-2">
      <div>
        <div className="text-xs font-semibold text-slate-700">{title}</div>
        {hint && <div className="text-[11px] text-slate-400">{hint}</div>}
      </div>
      {children}
    </div>
  )
}

// ScheduleActivity draws the schedule as a pipeline, from the schedules it depends on,
// through its stages, to the schedules that depend on it, with live state for every node.
// Problems (unmet conditions, failing pods, warning events) are listed above it, and a
// namespace can be opened to see its workloads.
export default function ScheduleActivity({ group: initial }: { group: ScalingGroup }) {
  const name = initial.metadata.name
  const [groups, setGroups] = useState<ScalingGroup[]>([])
  const [events, setEvents] = useState<KubeEvent[]>([])
  const [pods, setPods] = useState<Record<string, Pod[]>>({})
  const [selected, setSelected] = useState<string | null>(null)
  const [workloads, setWorkloads] = useState<Workload[]>([])

  const group = groups.find(g => g.metadata.name === name) || initial
  const phase = group.status?.phase || ''
  const busy = busyPhases.includes(phase)
  const down = group.status?.desiredState ? group.status.desiredState === 'Down' : phase === 'ScalingDown' || phase === 'ScaledDown'
  const stages = startStages(group)
  const runOrder = down ? [...stages].reverse() : stages
  const ready = new Set(group.status?.readyNamespaces || [])
  // The stage being worked on: the first, in execution order, that is not fully there.
  const current = busy && phase !== 'WaitingForDependencies' ? runOrder.find(s => s.some(i => !ready.has(i))) : undefined

  usePolling(() => {
    fetch('/api/scaling/groups').then(r => (r.ok ? r.json() : [])).then(setGroups).catch(() => {})
    fetch(`/api/scaling/groups/${name}/events`).then(r => (r.ok ? r.json() : [])).then((list: KubeEvent[]) =>
      setEvents([...(list || [])].sort((a, b) => new Date(eventTime(b)).getTime() - new Date(eventTime(a)).getTime())),
    ).catch(() => {})
    // Look for failing pods only where work is happening, plus the namespace being inspected.
    const watch = new Set([...(current || []).filter(i => !i.startsWith(EXT) && !ready.has(i)), ...(selected ? [selected] : [])])
    watch.forEach(ns => {
      fetch(`/api/namespaces/${ns}/pods`).then(r => (r.ok ? r.json() : [])).then((list: Pod[]) => setPods(prev => ({ ...prev, [ns]: list || [] }))).catch(() => {})
    })
    if (selected) fetch(`/api/namespaces/${selected}/workloads`).then(r => (r.ok ? r.json() : [])).then(setWorkloads).catch(() => {})
  }, 3000, `${name}/${selected}/${current?.join(',')}`)

  const stateOf = (item: string, inCurrent: boolean): NodeState => {
    if (ready.has(item)) return busy || phase.startsWith('Scaled') ? 'done' : 'idle'
    if (!busy) return phase === 'ScaledDown' ? 'idle' : 'done'
    if (!inCurrent) return 'waiting'
    return (pods[item] || []).some(problemPod) ? 'problem' : 'running'
  }

  const groupState = (g?: ScalingGroup): NodeState => {
    const p = g?.status?.phase
    if (!g) return 'problem'
    if (p === 'ScaledUp') return 'done'
    if (p && busyPhases.includes(p)) return 'running'
    return 'idle'
  }

  // Issues worth acting on, most specific first.
  const issues: string[] = []
  for (const dep of group.spec.dependsOn || []) {
    const g = groups.find(x => x.metadata.name === dep)
    if (!g) issues.push(`Depends on "${dep}", which does not exist.`)
    else if (phase === 'WaitingForDependencies' && g.status?.phase !== 'ScaledUp') issues.push(`Waiting for ${dep} to be fully up (now ${g.status?.phase || 'unknown'}).`)
  }
  for (const [ns, list] of Object.entries(pods)) {
    if (!current?.includes(ns) || ready.has(ns)) continue
    for (const p of list.filter(problemPod).slice(0, 3)) issues.push(`${ns}/${p.name}: ${p.reason || p.status}${p.restarts ? `, ${p.restarts} restarts` : ''}`)
  }
  for (const c of group.status?.conditions || []) {
    if (c.status === 'False' && c.type !== 'ManualOverride' && c.message && !(c.type === 'DependenciesReady' && issues.length)) issues.push(c.message)
  }
  for (const c of group.status?.conflictingNamespaces || []) issues.push(`${c} is skipped: another schedule already owns it.`)
  const recentWarnings = events.filter(e => e.type === 'Warning').slice(0, 3)

  const arrow = down
    ? <ArrowLeft size={18} className="shrink-0 self-center text-slate-300" />
    : <ArrowRight size={18} className="shrink-0 self-center text-slate-300" />
  const total = group.status?.namespacesTotal || group.spec.namespaces.length
  const directionText = phase === 'WaitingForDependencies' ? 'Waiting for dependencies before starting'
    : busy ? (down ? 'Scaling down: right to left, stage by stage' : 'Scaling up: left to right, stage by stage')
      : phase === 'ScaledDown' ? 'Scaled down' : phase === 'ScaledUp' ? 'Up and ready' : phase || 'No activity yet'

  return (
    <div className="space-y-5">
      <div className="flex flex-wrap items-center justify-between gap-2 text-sm">
        <span className="font-semibold text-slate-800">{directionText}</span>
        <span className="text-slate-500">{group.status?.namespacesReady || 0} of {total} namespaces {down ? 'down' : 'ready'}</span>
      </div>

      {(issues.length > 0 || recentWarnings.length > 0) && (
        <div className="rounded-xl border border-amber-200 bg-amber-50 p-3">
          <div className="flex items-center gap-1.5 text-sm font-semibold text-amber-800 mb-1"><AlertTriangle size={14} /> Needs attention</div>
          <ul className="space-y-0.5 text-sm text-amber-900">
            {issues.map((i, n) => <li key={`i${n}`}>{i}</li>)}
            {recentWarnings.map(e => <li key={e.metadata.name}><span className="font-medium">{e.reason}:</span> {e.message}</li>)}
          </ul>
        </div>
      )}

      <div className="overflow-x-auto pb-2">
        <div className="flex items-start gap-3 min-w-max">
          {(group.spec.dependsOn?.length || 0) > 0 && (
            <>
              <Column title="Starts after" hint="must be up first">
                {group.spec.dependsOn!.map(dep => {
                  const g = groups.find(x => x.metadata.name === dep)
                  return <Node key={dep} state={groupState(g)} label={dep} sub={g?.status?.phase || 'not found'} icon={<Link2 size={12} className="text-slate-400" />} />
                })}
              </Column>
              {arrow}
            </>
          )}

          {stages.map((stage, i) => {
            const inCurrent = current === stage
            return (
              <div key={i} className="flex items-start gap-3">
                <Column title={`Stage ${i + 1}`} hint={inCurrent ? 'in progress' : undefined}>
                  {stage.map(item => {
                    const ext = item.startsWith(EXT)
                    const st = stateOf(item, inCurrent)
                    const failing = (pods[item] || []).filter(problemPod)
                    const sub = st === 'problem' ? `${failing.length} pod${failing.length === 1 ? '' : 's'} failing`
                      : st === 'running' ? (down ? 'stopping' : 'starting')
                        : st === 'waiting' ? 'waiting' : st === 'done' ? (down ? 'down' : 'ready') : down ? 'down' : 'up'
                    return (
                      <Node key={item} state={st} label={ext ? targetLabel(item.slice(EXT.length), group.spec.externalTargets) : item}
                        sub={ext ? `${group.spec.externalTargets?.find(t => t.identifier === item.slice(EXT.length))?.type || 'cloud'} · ${sub}` : sub}
                        icon={ext ? <Cloud size={12} className="text-amber-500" /> : undefined}
                        selected={selected === item}
                        onClick={ext ? undefined : () => setSelected(selected === item ? null : item)} />
                    )
                  })}
                </Column>
                {(i < stages.length - 1 || (group.status?.requiredBy?.length || 0) > 0) && arrow}
              </div>
            )
          })}

          {(group.status?.requiredBy?.length || 0) > 0 && (
            <Column title="Kept up for" hint="schedules that need this one">
              {group.status!.requiredBy!.map(dep => {
                const g = groups.find(x => x.metadata.name === dep)
                return <Node key={dep} state={groupState(g)} label={dep} sub={g?.status?.phase} icon={<Link2 size={12} className="text-slate-400" />} />
              })}
            </Column>
          )}
        </div>
      </div>

      {selected && (
        <div className="rounded-xl border border-slate-200">
          <div className="px-3 py-2 border-b border-slate-100 flex items-center justify-between">
            <span className="text-sm font-semibold text-slate-800">{selected}</span>
            <button onClick={() => setSelected(null)} className="text-xs text-slate-400 hover:text-slate-600">Close</button>
          </div>
          <div className="p-3 grid gap-4 md:grid-cols-2">
            <div>
              <div className="text-xs font-semibold text-slate-500 mb-1.5">Workloads</div>
              {workloads.length === 0 ? <p className="text-sm text-slate-400">None</p> : (
                <ul className="space-y-1">
                  {workloads.map(w => (
                    <li key={`${w.kind}/${w.name}`} className="flex items-center justify-between text-sm">
                      <span className="truncate text-slate-700">{w.name} <span className="text-xs text-slate-400">{w.kind}</span></span>
                      <span className={`tabular-nums ${w.readyReplicas === w.replicas ? 'text-emerald-700' : 'text-amber-700'}`}>{w.readyReplicas}/{w.replicas}</span>
                    </li>
                  ))}
                </ul>
              )}
            </div>
            <div>
              <div className="text-xs font-semibold text-slate-500 mb-1.5">Pods with problems</div>
              {(pods[selected] || []).filter(problemPod).length === 0 ? <p className="text-sm text-slate-400">None</p> : (
                <ul className="space-y-1">
                  {(pods[selected] || []).filter(problemPod).map(p => (
                    <li key={p.name} className="text-sm">
                      <span className="text-slate-700">{p.name}</span>
                      <span className="block text-xs text-rose-700">{p.reason || p.status}{p.restarts ? ` · ${p.restarts} restarts` : ''}</span>
                    </li>
                  ))}
                </ul>
              )}
            </div>
          </div>
        </div>
      )}

      <div>
        <h3 className="text-sm font-semibold text-slate-800 mb-2">Events</h3>
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
