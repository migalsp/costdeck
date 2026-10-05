import { useState } from 'react'
import { Activity, LayoutList, ListOrdered, Link2, Pencil, Play, RotateCcw, Square, Trash2 } from 'lucide-react'
import { apiError } from '../lib/api'
import { formatMoney } from '../lib/format'
import { describeSpec, statusLine } from '../lib/schedule'
import type { ExternalTarget, ScalingConfig, ScalingGroup, ScalingSpec } from '../lib/types'
import ScheduleActivity from './ScheduleActivity'
import ScheduleStatusLine from './ScheduleStatus'
import StartOrderEditor from './StartOrderEditor'
import WeekTimeline from './WeekTimeline'
import { Badge, Button, Drawer, Tabs } from './ui'

export type DrawerTab = 'overview' | 'activity' | 'order'

interface Props {
  target: { kind: 'group'; group: ScalingGroup } | { kind: 'config'; config: ScalingConfig }
  groups: ScalingGroup[]
  discovered: ExternalTarget[]
  tab: DrawerTab
  onTab: (tab: DrawerTab) => void
  busy?: boolean
  canOperate: boolean
  canAdmin: boolean
  onClose: () => void
  onStart: () => void
  onStop: () => void
  onResume: () => void
  onEdit: () => void
  onDelete?: () => void
  onRules?: () => void
  onSaved: () => void
  onSelectNamespace: (ns: string) => void
}

// ScheduleDrawer is the detail view of one schedule: what it does now and when, how far a
// running transition got, and the order its namespaces and cloud resources start in.
export default function ScheduleDrawer(p: Props) {
  const isGroup = p.target.kind === 'group'
  const obj = p.target.kind === 'group' ? p.target.group : p.target.config
  const spec: ScalingSpec = obj.spec
  const status = obj.status
  const namespaces = p.target.kind === 'group' ? p.target.group.spec.namespaces : [p.target.config.spec.targetNamespace]
  const name = p.target.kind === 'group' ? obj.metadata.name : p.target.config.spec.targetNamespace
  const [confirmDelete, setConfirmDelete] = useState(false)

  const line = statusLine({ phase: status?.phase, status, dependsOn: spec.dependsOn, activeUntil: spec.activeUntil, generation: obj.metadata.generation })
  const manual = status?.mode === 'ManualUp' || status?.mode === 'ManualDown' || (spec.active !== undefined && spec.active !== null)
  const up = status?.desiredState ? status.desiredState === 'Up' : status?.phase === 'ScaledUp'
  const saving = parseFloat(status?.estimatedHourlySavings || '')
  const requiredBy = p.target.kind === 'group' ? p.target.group.status?.requiredBy || [] : []
  const conflicts = p.target.kind === 'group' ? p.target.group.status?.conflictingNamespaces || [] : []

  const saveOrder = async (change: { sequence?: string[]; externalTargets?: ExternalTarget[] }) => {
    if (p.target.kind !== 'group') return
    const g = p.target.group
    const res = await fetch(`/api/scaling/groups/${g.metadata.name}`, {
      method: 'PUT',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({ metadata: { name: g.metadata.name }, spec: { ...g.spec, ...change } }),
    })
    if (!res.ok) throw new Error(await apiError(res))
    p.onSaved()
  }

  const actions = (
    <div className="flex flex-wrap items-center gap-2 mt-3">
      {p.canOperate && (manual
        ? <Button size="sm" variant="warning" icon={<RotateCcw size={14} />} onClick={p.onResume} disabled={p.busy}>Follow schedule</Button>
        : up
          ? <Button size="sm" icon={<Square size={14} />} onClick={p.onStop} disabled={p.busy}>Scale down now</Button>
          : <Button size="sm" variant="success" icon={<Play size={14} />} onClick={p.onStart} disabled={p.busy}>Start now</Button>)}
      {p.canAdmin && <Button size="sm" variant="ghost" icon={<Pencil size={14} />} onClick={p.onEdit}>Edit schedule</Button>}
      {!isGroup && p.canAdmin && p.onRules && <Button size="sm" variant="ghost" icon={<ListOrdered size={14} />} onClick={p.onRules}>Workload rules</Button>}
    </div>
  )

  return (
    <Drawer
      title={name}
      subtitle={describeSpec(spec)}
      onClose={p.onClose}
      headerExtra={
        <>
          <div className="mt-3"><ScheduleStatusLine line={line} size="lg" /></div>
          {actions}
          {isGroup && (
            <div className="mt-4">
              <Tabs<DrawerTab>
                value={p.tab}
                onChange={p.onTab}
                tabs={[
                  { id: 'overview', label: <span className="inline-flex items-center gap-1.5"><LayoutList size={14} /> Overview</span> },
                  { id: 'activity', label: <span className="inline-flex items-center gap-1.5"><Activity size={14} /> Activity</span> },
                  { id: 'order', label: <span className="inline-flex items-center gap-1.5"><ListOrdered size={14} /> Start order</span> },
                ]}
              />
            </div>
          )}
        </>
      }
    >
      {(!isGroup || p.tab === 'overview') && (
        <div className="space-y-6">
          {saving > 0 && (
            <div className="rounded-xl border border-emerald-200 bg-emerald-50 px-4 py-3 text-sm text-emerald-800">
              Saving about <b>{formatMoney(saving, status?.currency)}/h</b> right now while workloads are scaled down.
            </div>
          )}

          <section>
            <h3 className="text-sm font-semibold text-slate-800 mb-3">Week</h3>
            <WeekTimeline schedules={spec.schedules} onDemand={spec.activation === 'OnDemand'} />
          </section>

          <section>
            <h3 className="text-sm font-semibold text-slate-800 mb-2">Namespaces</h3>
            <div className="flex flex-wrap gap-1.5">
              {namespaces.map(ns => (
                <button key={ns} onClick={() => p.onSelectNamespace(ns)}
                  className="px-2.5 py-1 rounded-md border border-slate-200 bg-white text-sm text-slate-700 hover:border-brand-300 hover:text-brand-700">
                  {ns}
                </button>
              ))}
            </div>
            {conflicts.length > 0 && (
              <p className="mt-2 text-sm text-rose-600">Skipped, already in another schedule: {conflicts.join(', ')}</p>
            )}
          </section>

          {isGroup && ((spec.dependsOn?.length || 0) > 0 || requiredBy.length > 0) && (
            <section>
              <h3 className="text-sm font-semibold text-slate-800 mb-2 flex items-center gap-1.5"><Link2 size={14} /> Dependencies</h3>
              {(spec.dependsOn?.length || 0) > 0 && (
                <div className="flex flex-wrap items-center gap-1.5 text-sm text-slate-600 mb-1.5">
                  Starts after
                  {spec.dependsOn!.map(dep => {
                    const phase = p.groups.find(g => g.metadata.name === dep)?.status?.phase
                    return <Badge key={dep} tone={phase === 'ScaledUp' ? 'success' : 'neutral'} title={phase || 'unknown'}>{dep}</Badge>
                  })}
                </div>
              )}
              {requiredBy.length > 0 && (
                <div className="flex flex-wrap items-center gap-1.5 text-sm text-slate-600">
                  Kept up for {requiredBy.map(r => <Badge key={r} tone="brand">{r}</Badge>)}
                </div>
              )}
            </section>
          )}

          {isGroup && p.canAdmin && p.onDelete && (
            <section className="rounded-xl border border-rose-200 p-4">
              <h3 className="text-sm font-semibold text-rose-700">Delete schedule</h3>
              <p className="text-sm text-slate-500 mt-1">Its namespaces keep their current size and are no longer scaled on a schedule.</p>
              <div className="mt-3 flex gap-2">
                {confirmDelete ? (
                  <>
                    <Button size="sm" variant="danger" icon={<Trash2 size={14} />} onClick={p.onDelete}>Delete {name}</Button>
                    <Button size="sm" variant="ghost" onClick={() => setConfirmDelete(false)}>Cancel</Button>
                  </>
                ) : (
                  <Button size="sm" variant="danger" icon={<Trash2 size={14} />} onClick={() => setConfirmDelete(true)}>Delete…</Button>
                )}
              </div>
            </section>
          )}
        </div>
      )}

      {p.target.kind === 'group' && p.tab === 'activity' && <ScheduleActivity group={p.target.group} />}

      {p.target.kind === 'group' && p.tab === 'order' && (
        <StartOrderEditor
          key={p.target.group.metadata.name}
          namespaces={p.target.group.spec.namespaces}
          sequence={p.target.group.spec.sequence}
          externalTargets={p.target.group.spec.externalTargets}
          discovered={p.discovered}
          readOnly={!p.canAdmin}
          onSave={saveOrder}
        />
      )}
    </Drawer>
  )
}
