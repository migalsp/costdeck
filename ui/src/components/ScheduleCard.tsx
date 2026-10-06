import type { MouseEvent } from 'react'
import { ChevronRight, Link2, Pencil, Play, RotateCcw, PowerOff } from 'lucide-react'
import { formatMoney } from '../lib/format'
import { describeSpec, statusLine } from '../lib/schedule'
import type { Condition, ScalingSpec, ScheduleStatus } from '../lib/types'
import type { DetailsTab } from './ScheduleDetails'
import ScheduleStatusLine from './ScheduleStatus'
import WeekTimeline from './WeekTimeline'
import { Button, Card } from './ui'

export interface ScheduleCardProps {
  name: string
  namespaces: string[]
  spec: ScalingSpec
  generation?: number
  status?: ScheduleStatus & {
    phase?: string
    requiredBy?: string[]
    namespacesReady?: number
    namespacesTotal?: number
    conflictingNamespaces?: string[]
    conditions?: Condition[]
  }
  busy?: boolean
  canOperate: boolean
  canAdmin: boolean
  onOpen: (tab: DetailsTab) => void
  onStart: () => void
  onStop: () => void
  onResume: () => void
  onEdit: () => void
}

const MAX_CHIPS = 6
const stop = (fn: () => void) => (e: MouseEvent) => { e.stopPropagation(); fn() }

// ScheduleCard says in one line whether a schedule's workloads are up and what happens
// next, shows the week at a glance and offers the one action that fits. Clicking it
// opens the schedule's details.
export default function ScheduleCard(p: ScheduleCardProps) {
  const line = statusLine({ phase: p.status?.phase, status: p.status, dependsOn: p.spec.dependsOn, activeUntil: p.spec.activeUntil, generation: p.generation })
  const manual = p.status?.mode === 'ManualUp' || p.status?.mode === 'ManualDown'
    || (p.spec.active !== undefined && p.spec.active !== null)
  const up = p.status?.desiredState ? p.status.desiredState === 'Up' : p.status?.phase === 'ScaledUp'
  const saving = parseFloat(p.status?.estimatedHourlySavings || '')
  const transitioning = ['ScalingUp', 'ScalingDown', 'WaitingForDependencies'].includes(p.status?.phase || '')
  const total = p.status?.namespacesTotal || p.namespaces.length
  const ready = p.status?.namespacesReady || 0

  return (
    <Card onClick={() => p.onOpen('overview')} className="p-5 flex flex-col gap-4 group">
      <div className="flex items-start justify-between gap-3">
        <div className="min-w-0">
          <h3 className="font-semibold text-slate-900 truncate">{p.name}</h3>
          <p className="text-xs text-slate-500 truncate" title={describeSpec(p.spec)}>{describeSpec(p.spec)}</p>
        </div>
        <ChevronRight size={18} className="shrink-0 text-slate-300 group-hover:text-slate-500 transition-colors" />
      </div>

      <div>
        <ScheduleStatusLine line={line} />
        {saving > 0 && (
          <div className="ml-4.5 mt-0.5 text-xs font-medium text-emerald-700" title="Estimated cost of the workloads currently scaled down, at the configured rates">
            saving ~{formatMoney(saving, p.status?.currency)}/h
          </div>
        )}
      </div>

      {transitioning ? (
        <button onClick={stop(() => p.onOpen('activity'))} className="text-left rounded-lg border border-sky-200 bg-sky-50 px-3 py-2 hover:bg-sky-100 transition-colors">
          <div className="flex items-center justify-between text-xs font-medium text-sky-800 mb-1.5">
            <span>{ready} of {total} namespaces ready</span>
            <span className="inline-flex items-center gap-0.5">View progress <ChevronRight size={12} /></span>
          </div>
          <div className="h-1.5 rounded-full bg-sky-100 overflow-hidden">
            <div className="h-full bg-sky-500 rounded-full transition-all duration-500" style={{ width: `${Math.min(100, (ready / Math.max(total, 1)) * 100)}%` }} />
          </div>
        </button>
      ) : (
        <WeekTimeline schedules={p.spec.schedules} onDemand={p.spec.activation === 'OnDemand'} compact />
      )}

      {((p.spec.dependsOn?.length || 0) > 0 || (p.status?.conflictingNamespaces?.length || 0) > 0) && (
        <div className="space-y-1 text-xs">
          {(p.spec.dependsOn?.length || 0) > 0 && (
            <div className="flex items-center gap-1 text-slate-500"><Link2 size={12} /> Starts after {p.spec.dependsOn!.join(', ')}</div>
          )}
          {(p.status?.conflictingNamespaces?.length || 0) > 0 && (
            <div className="text-rose-600 font-medium">Skipped, already in another schedule: {p.status!.conflictingNamespaces!.join(', ')}</div>
          )}
        </div>
      )}

      <div className="flex flex-wrap gap-1">
        {p.namespaces.slice(0, MAX_CHIPS).map(ns => (
          <span key={ns} className="px-2 py-0.5 rounded-md bg-slate-50 border border-slate-200 text-[11px] font-medium text-slate-600">{ns}</span>
        ))}
        {p.namespaces.length > MAX_CHIPS && <span className="px-2 py-0.5 text-[11px] text-slate-400">+{p.namespaces.length - MAX_CHIPS} more</span>}
      </div>

      {(p.canOperate || p.canAdmin) && (
        <div className="mt-auto pt-3 border-t border-slate-100 flex items-center gap-2">
          {p.canOperate && (manual
            ? <Button size="sm" variant="warning" icon={<RotateCcw size={13} />} onClick={stop(p.onResume)} disabled={p.busy}>Follow schedule</Button>
            : up
              ? <Button size="sm" icon={<PowerOff size={13} />} onClick={stop(p.onStop)} disabled={p.busy}>Scale down now</Button>
              : <Button size="sm" variant="success" icon={<Play size={13} />} onClick={stop(p.onStart)} disabled={p.busy}>Start now</Button>)}
          {p.busy && <span className="w-3.5 h-3.5 border-2 border-slate-200 border-t-brand-500 rounded-full animate-spin" />}
          {p.canAdmin && (
            <Button size="sm" variant="ghost" className="ml-auto" icon={<Pencil size={13} />} onClick={stop(p.onEdit)}>Edit</Button>
          )}
        </div>
      )}
    </Card>
  )
}
