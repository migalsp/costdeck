import { useState } from 'react'
import { CalendarClock, ListChecks, Play, PowerOff, RotateCcw, Plus } from 'lucide-react'
import { apiError, errorMessage } from '../lib/api'
import { useAuth } from '../lib/auth'
import { describeSpec, statusLine } from '../lib/schedule'
import type { ScalingConfig, ScalingGroup, ScalingSpec } from '../lib/types'
import { usePolling } from '../lib/usePolling'
import type { NamespaceFinOps } from '../lib/types'
import OverrideDialog, { type OverrideUntil } from './OverrideDialog'
import WorkloadRulesDialog from './WorkloadRulesDialog'
import ScheduleWizard from './ScheduleWizard'
import WeekTimeline from './WeekTimeline'

type Controller =
  | { type: 'group'; name: string; spec: ScalingSpec; status: ScalingGroup['status']; size: number }
  | { type: 'config'; name: string; spec: ScalingSpec; status: ScalingConfig['status']; size: 1 }

// NamespaceScalingPanel shows which schedule controls a namespace and what it does next,
// and keeps the per-namespace workload rules one click away.
export default function NamespaceScalingPanel({ namespace }: { namespace: string }) {
  const { can } = useAuth()
  const [groups, setGroups] = useState<ScalingGroup[]>([])
  const [configs, setConfigs] = useState<ScalingConfig[]>([])
  const [namespaces, setNamespaces] = useState<string[]>([])
  const [loaded, setLoaded] = useState(false)
  const [wizard, setWizard] = useState<null | { kind: 'group'; existing?: ScalingGroup; initialNamespaces?: string[] } | { kind: 'config'; existing: ScalingConfig }>(null)
  const [rulesFor, setRulesFor] = useState<ScalingConfig | null>(null)
  const [prompt, setPrompt] = useState<boolean | null>(null)
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState<string | null>(null)

  const load = () => {
    Promise.all([
      fetch('/api/scaling/groups').then(r => r.json()),
      fetch('/api/scaling/configs').then(r => r.json()),
      fetch('/api/namespaces').then(r => r.json()),
    ]).then(([g, c, n]) => {
      setGroups(g || [])
      setConfigs(c || [])
      setNamespaces(((n || []) as NamespaceFinOps[]).map(x => x.spec.targetNamespace || x.metadata.name).sort())
      setLoaded(true)
    }).catch(err => setError(errorMessage(err)))
  }
  usePolling(load, 10000, namespace)

  const group = groups.find(g => g.spec.namespaces.includes(namespace))
  const config = configs.find(c => c.spec.targetNamespace === namespace)
  const controller: Controller | null = group
    ? { type: 'group', name: group.metadata.name, spec: group.spec, status: group.status, size: group.spec.namespaces.length }
    : config && ((config.spec.schedules?.length || 0) > 0 || config.spec.active !== undefined)
      ? { type: 'config', name: config.metadata.name, spec: config.spec, status: config.status, size: 1 }
      : null

  const override = async (active: boolean | null, until?: OverrideUntil) => {
    if (!controller) return
    setBusy(true)
    setError(null)
    try {
      const base = controller.type === 'group' ? '/api/scaling/groups' : '/api/scaling/configs'
      const res = await fetch(`${base}/${controller.name}/manual`, {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ active, ...(active !== null && until && until !== 'forever' ? { until } : {}) }),
      })
      if (!res.ok) throw new Error(await apiError(res))
      load()
    } catch (err) {
      setError(errorMessage(err))
    } finally {
      setBusy(false)
    }
  }

  // Workload rules live in the namespace's ScalingConfig, created on first use. They apply
  // whichever schedule runs the namespace.
  const openRules = async () => {
    if (config) return setRulesFor(config)
    try {
      const res = await fetch('/api/scaling/configs', {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ metadata: { name: `config-${namespace}` }, spec: { targetNamespace: namespace } }),
      })
      if (!res.ok) throw new Error(await apiError(res))
      setRulesFor(await res.json())
      load()
    } catch (err) {
      setError(errorMessage(err))
    }
  }

  const saveRules = async (spec: ScalingSpec) => {
    if (!rulesFor) return
    try {
      const res = await fetch(`/api/scaling/configs/${rulesFor.metadata.name}`, {
        method: 'PUT',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ metadata: { name: rulesFor.metadata.name }, spec }),
      })
      if (!res.ok) throw new Error(await apiError(res))
      setRulesFor(null)
      load()
    } catch (err) {
      setError(errorMessage(err))
    }
  }

  if (!loaded) return <div className="h-28 bg-slate-100 rounded-xl animate-pulse mb-8" />

  const line = controller && statusLine({ phase: controller.status?.phase, status: controller.status, dependsOn: controller.spec.dependsOn, activeUntil: controller.spec.activeUntil })
  const manual = controller && (controller.status?.mode === 'ManualUp' || controller.status?.mode === 'ManualDown' || (controller.spec.active !== undefined && controller.spec.active !== null))
  const up = controller?.status?.desiredState ? controller.status.desiredState === 'Up' : controller?.status?.phase === 'ScaledUp'
  const rules = (config?.spec.exclusions?.length || 0) + (config?.spec.sequence?.length || 0)

  return (
    <div className="bg-white rounded-xl border border-slate-200 shadow-sm p-5 mb-8">
      <div className="flex flex-wrap items-start gap-6">
        <div className="flex items-start gap-3 min-w-0 flex-1">
          <div className="p-2.5 rounded-xl bg-brand-50 text-brand-500"><CalendarClock size={22} /></div>
          <div className="min-w-0">
            {controller ? (
              <>
                <div className="text-xs font-bold uppercase tracking-wider text-slate-400">
                  {controller.type === 'group' ? <>Scheduled by <span className="text-brand-600">{controller.name}</span>{controller.size > 1 && ` with ${controller.size - 1} other namespace${controller.size > 2 ? 's' : ''}`}</> : 'Own schedule'}
                </div>
                <div className="mt-1"><span className="font-bold text-slate-800">{line!.title}</span>{line!.detail && <span className="text-slate-500"> · {line!.detail}</span>}</div>
                <div className="text-xs text-slate-400 mt-0.5">{describeSpec(controller.spec)}</div>
              </>
            ) : (
              <>
                <div className="text-xs font-bold uppercase tracking-wider text-slate-400">Not scheduled</div>
                <div className="mt-1 font-bold text-slate-800">Always on</div>
                <div className="text-xs text-slate-400 mt-0.5">Give it a schedule to scale it to zero outside working hours.</div>
              </>
            )}
          </div>
        </div>
        {controller && (
          <div className="w-64 hidden md:block">
            <WeekTimeline schedules={controller.spec.schedules} onDemand={controller.spec.activation === 'OnDemand'} compact />
          </div>
        )}
      </div>

      <div className="mt-4 pt-4 border-t border-slate-100 flex flex-wrap items-center gap-2">
        {controller && can('operator') && (manual ? (
          <button onClick={() => override(null)} disabled={busy} className="flex items-center gap-1.5 px-3 py-1.5 rounded-lg bg-amber-50 border border-amber-200 text-amber-700 text-xs font-bold hover:bg-amber-100 disabled:opacity-50">
            <RotateCcw size={13} /> Follow schedule
          </button>
        ) : up ? (
          <button onClick={() => setPrompt(false)} disabled={busy} className="flex items-center gap-1.5 px-3 py-1.5 rounded-lg bg-slate-50 border border-slate-200 text-slate-700 text-xs font-bold hover:bg-slate-100 disabled:opacity-50">
            <PowerOff size={13} /> Scale down now
          </button>
        ) : (
          <button onClick={() => setPrompt(true)} disabled={busy} className="flex items-center gap-1.5 px-3 py-1.5 rounded-lg bg-emerald-50 border border-emerald-200 text-emerald-700 text-xs font-bold hover:bg-emerald-100 disabled:opacity-50">
            <Play size={13} /> Start now
          </button>
        ))}
        {controller?.type === 'group' && controller.size > 1 && can('operator') && (
          <span className="text-[11px] text-slate-400">applies to all of {controller.name}</span>
        )}
        {!controller && can('admin') && (
          <button onClick={() => setWizard({ kind: 'group', initialNamespaces: [namespace] })} className="flex items-center gap-1.5 px-3 py-1.5 rounded-lg bg-brand-600 text-white text-xs font-bold hover:bg-brand-700">
            <Plus size={13} /> Schedule this namespace
          </button>
        )}
        {controller && can('admin') && (
          <button onClick={() => setWizard(controller.type === 'group' ? { kind: 'group', existing: group } : { kind: 'config', existing: config! })}
            className="px-3 py-1.5 rounded-lg text-slate-500 text-xs font-bold hover:bg-slate-100">
            Edit schedule
          </button>
        )}
        {/* Rules only matter under a schedule. A rules-only config on an unscheduled namespace
            would count as "always on" and start every workload that sits at zero. */}
        {controller && can('admin') && (
          <button onClick={openRules} className="ml-auto flex items-center gap-1.5 px-3 py-1.5 rounded-lg text-slate-500 text-xs font-bold hover:bg-slate-100"
            title="Keep some workloads running while the rest scale down, or set the order workloads start in">
            <ListChecks size={13} /> Workload rules{rules > 0 && <span className="text-brand-500">({rules})</span>}
          </button>
        )}
        {error && <span className="w-full text-xs font-semibold text-rose-600">{error}</span>}
      </div>

      {prompt !== null && controller && (
        <OverrideDialog
          name={controller.name}
          kind={controller.type}
          active={prompt}
          hasSchedule={(controller.spec.schedules?.length || 0) > 0 || controller.spec.activation === 'OnDemand'}
          onCancel={() => setPrompt(null)}
          onConfirm={until => { const active = prompt; setPrompt(null); override(active, until) }}
        />
      )}
      {wizard && (
        <ScheduleWizard {...wizard} namespaces={namespaces} groups={groups} onClose={() => setWizard(null)} onSaved={() => { setWizard(null); load() }} />
      )}
      {rulesFor && (
        <WorkloadRulesDialog namespace={namespace} spec={rulesFor.spec} onClose={() => setRulesFor(null)} onSave={saveRules} />
      )}
    </div>
  )
}
