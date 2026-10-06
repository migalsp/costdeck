import { useState } from 'react'
import { Activity, ArrowRight, LayoutList, ListOrdered, Link2, Pencil, Play, RotateCcw, PowerOff, Trash2 } from 'lucide-react'
import { formatMoney } from '../lib/format'
import { describeSpec, statusLine } from '../lib/schedule'
import { targetLabel } from '../lib/cloud'
import type { ScalingConfig, ScalingGroup, ScalingSpec } from '../lib/types'
import ScheduleActivity from './ScheduleActivity'
import ScheduleStatusLine from './ScheduleStatus'
import WeekTimeline from './WeekTimeline'
import type { WizardTab } from './ScheduleWizard'
import { Badge, Button, Modal, Tabs } from './ui'

export type DetailsTab = 'overview' | 'activity'

interface Props {
  target: { kind: 'group'; group: ScalingGroup } | { kind: 'config'; config: ScalingConfig }
  groups: ScalingGroup[]
  tab: DetailsTab
  onTab: (tab: DetailsTab) => void
  busy?: boolean
  canOperate: boolean
  canAdmin: boolean
  onClose: () => void
  onStart: () => void
  onStop: () => void
  onResume: () => void
  onEdit: (tab?: WizardTab) => void
  onDelete?: () => void
  onRules?: () => void
  onSelectNamespace: (ns: string) => void
}

// ScheduleDetails is the detail view of one schedule: what it does now and when, and the
// live pipeline of a running transition. Configuration, start order included, is edited
// in ScheduleWizard.
export default function ScheduleDetails(p: Props) {
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

  const actions = (
    <div className="flex flex-wrap items-center gap-2 mt-3">
      {p.canOperate && (manual
        ? <Button size="sm" variant="warning" icon={<RotateCcw size={14} />} onClick={p.onResume} disabled={p.busy}>Follow schedule</Button>
        : up
          ? <Button size="sm" icon={<PowerOff size={14} />} onClick={p.onStop} disabled={p.busy}>Scale down now</Button>
          : <Button size="sm" variant="success" icon={<Play size={14} />} onClick={p.onStart} disabled={p.busy}>Start now</Button>)}
      {p.canAdmin && <Button size="sm" variant="ghost" icon={<Pencil size={14} />} onClick={() => p.onEdit()}>Edit</Button>}
      {!isGroup && p.canAdmin && p.onRules && <Button size="sm" variant="ghost" icon={<ListOrdered size={14} />} onClick={p.onRules}>Workload rules</Button>}
    </div>
  )

  return (
    <Modal
      size="xl"
      tall
      title={name}
      subtitle={describeSpec(spec)}
      onClose={p.onClose}
      headerExtra={
        <>
          <div className="mt-3"><ScheduleStatusLine line={line} size="lg" /></div>
          {actions}
          {isGroup && (
            <div className="mt-4">
              <Tabs<DetailsTab>
                value={p.tab}
                onChange={p.onTab}
                tabs={[
                  { id: 'overview', label: <span className="inline-flex items-center gap-1.5"><LayoutList size={14} /> Overview</span> },
                  { id: 'activity', label: <span className="inline-flex items-center gap-1.5"><Activity size={14} /> Activity</span> },
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

          {isGroup && (
            <section>
              <div className="flex items-center justify-between mb-2">
                <h3 className="text-sm font-semibold text-slate-800 flex items-center gap-1.5"><ListOrdered size={14} /> Start order</h3>
                {p.canAdmin && <Button size="sm" variant="ghost" onClick={() => p.onEdit('order')}>Change</Button>}
              </div>
              {(spec.sequence?.length || 0) === 0 ? (
                <p className="text-sm text-slate-500">All namespaces start together.</p>
              ) : (
                <div className="flex flex-wrap items-center gap-1.5 text-sm">
                  {spec.sequence!.map((stage, i) => (
                    <span key={i} className="inline-flex items-center gap-1.5">
                      {i > 0 && <ArrowRight size={13} className="text-slate-300" />}
                      <span className="px-2 py-0.5 rounded-md bg-slate-50 border border-slate-200 text-slate-700">
                        {stage.split(/\s+/).map(item => item.startsWith('ext:') ? `☁ ${targetLabel(item.slice(4), spec.externalTargets)}` : item).join(', ')}
                      </span>
                    </span>
                  ))}
                </div>
              )}
            </section>
          )}

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

          {p.canAdmin && p.onDelete && (
            <section className="rounded-xl border border-rose-200 p-4">
              <h3 className="text-sm font-semibold text-rose-700">Delete schedule</h3>
              <p className="text-sm text-slate-500 mt-1">
                {isGroup
                  ? 'Its namespaces keep their current size and are no longer scaled on a schedule.'
                  : 'The namespace keeps its current size and is no longer scaled on a schedule. Its workload rules (start order, exclusions) are removed too.'}
              </p>
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

    </Modal>
  )
}
