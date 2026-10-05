import { useEffect, useRef, useState } from 'react'
import { Play, Square, RotateCcw, Pencil, MoreHorizontal, ListOrdered, Activity, Trash2, Link2 } from 'lucide-react'
import { formatMoney } from '../lib/format'
import { describeSpec, statusLine, type Tone } from '../lib/schedule'
import type { ScalingSpec, ScheduleStatus } from '../lib/types'
import WeekTimeline from './WeekTimeline'

const toneStyle: Record<Tone, { dot: string; text: string }> = {
  up: { dot: 'bg-emerald-500', text: 'text-emerald-700' },
  down: { dot: 'bg-slate-300', text: 'text-slate-600' },
  busy: { dot: 'bg-sky-500 animate-pulse', text: 'text-sky-700' },
  manual: { dot: 'bg-amber-500', text: 'text-amber-700' },
  blocked: { dot: 'bg-violet-500 animate-pulse', text: 'text-violet-700' },
}

export interface ScheduleCardProps {
  name: string
  namespaces: string[]
  spec: ScalingSpec
  status?: ScheduleStatus & { phase?: string; requiredBy?: string[]; namespacesReady?: number; namespacesTotal?: number; conflictingNamespaces?: string[] }
  busy?: boolean
  canOperate: boolean
  canAdmin: boolean
  onStart: () => void
  onStop: () => void
  onResume: () => void
  onEdit: () => void
  onOrder: () => void
  onActivity?: () => void
  onDelete?: () => void
  onSelectNamespace: (ns: string) => void
}

const MAX_CHIPS = 6

// ScheduleCard says in one line whether a schedule's workloads are up and what happens
// next, shows the week at a glance, and offers the one action that makes sense now.
export default function ScheduleCard(p: ScheduleCardProps) {
  const [menuOpen, setMenuOpen] = useState(false)
  const menuRef = useRef<HTMLDivElement>(null)
  useEffect(() => {
    if (!menuOpen) return
    const close = (e: MouseEvent) => { if (!menuRef.current?.contains(e.target as Node)) setMenuOpen(false) }
    document.addEventListener('mousedown', close)
    return () => document.removeEventListener('mousedown', close)
  }, [menuOpen])

  const line = statusLine({ phase: p.status?.phase, status: p.status, dependsOn: p.spec.dependsOn, activeUntil: p.spec.activeUntil })
  const tone = toneStyle[line.tone]
  const manual = p.status?.mode === 'ManualUp' || p.status?.mode === 'ManualDown'
    || (p.spec.active !== undefined && p.spec.active !== null)
  const up = p.status?.desiredState ? p.status.desiredState === 'Up' : p.status?.phase === 'ScaledUp'
  const saving = parseFloat(p.status?.estimatedHourlySavings || '')
  const onDemand = p.spec.activation === 'OnDemand'

  return (
    <div className="bg-white rounded-2xl border border-slate-200 shadow-sm hover:shadow-md transition-shadow p-5 flex flex-col gap-4">
      <div className="flex items-start justify-between gap-3">
        <div className="min-w-0">
          <h3 className="font-bold text-slate-800 truncate">{p.name}</h3>
          <p className="text-[11px] text-slate-400 truncate" title={describeSpec(p.spec)}>{describeSpec(p.spec)}</p>
        </div>
        {(p.canAdmin || p.onActivity) && (
          <div className="relative shrink-0" ref={menuRef}>
            <button onClick={() => setMenuOpen(!menuOpen)} className="p-1.5 rounded-lg text-slate-400 hover:bg-slate-100" title="More">
              <MoreHorizontal size={16} />
            </button>
            {menuOpen && (
              <div className="absolute right-0 top-full mt-1 w-56 bg-white rounded-xl shadow-xl border border-slate-100 p-1 z-20 text-sm">
                {p.canAdmin && (
                  <button onClick={() => { setMenuOpen(false); p.onOrder() }} className="w-full flex items-center gap-2 px-3 py-2 rounded-lg hover:bg-slate-50 text-slate-700">
                    <ListOrdered size={14} /> Start order & cloud resources
                  </button>
                )}
                {p.onActivity && (
                  <button onClick={() => { setMenuOpen(false); p.onActivity!() }} className="w-full flex items-center gap-2 px-3 py-2 rounded-lg hover:bg-slate-50 text-slate-700">
                    <Activity size={14} /> Activity & progress
                  </button>
                )}
                {p.canAdmin && p.onDelete && (
                  <button onClick={() => { setMenuOpen(false); p.onDelete!() }} className="w-full flex items-center gap-2 px-3 py-2 rounded-lg hover:bg-rose-50 text-rose-600">
                    <Trash2 size={14} /> Delete schedule
                  </button>
                )}
              </div>
            )}
          </div>
        )}
      </div>

      <div className="flex items-start gap-2">
        <span className={`mt-1.5 w-2.5 h-2.5 rounded-full shrink-0 ${tone.dot}`} />
        <div className="min-w-0">
          <span className={`font-bold ${tone.text}`}>{line.title}</span>
          {line.detail && <span className="text-slate-500"> · {line.detail}</span>}
          {saving > 0 && (
            <div className="text-xs font-bold text-emerald-600 mt-0.5" title="Estimated cost of the workloads currently scaled down, at the configured rates">
              saving ~{formatMoney(saving, p.status?.currency)}/h right now
            </div>
          )}
        </div>
      </div>

      <WeekTimeline schedules={p.spec.schedules} onDemand={onDemand} compact />

      {((p.spec.dependsOn?.length || 0) > 0 || (p.status?.conflictingNamespaces?.length || 0) > 0) && (
        <div className="space-y-1 text-[11px]">
          {(p.spec.dependsOn?.length || 0) > 0 && (
            <div className="flex items-center gap-1 text-violet-600"><Link2 size={12} /> Starts after {p.spec.dependsOn!.join(', ')}</div>
          )}
          {(p.status?.conflictingNamespaces?.length || 0) > 0 && (
            <div className="text-rose-600 font-semibold">Skipped, already in another schedule: {p.status!.conflictingNamespaces!.join(', ')}</div>
          )}
        </div>
      )}

      <div className="flex flex-wrap gap-1">
        {p.namespaces.slice(0, MAX_CHIPS).map(ns => (
          <button key={ns} onClick={() => p.onSelectNamespace(ns)} title={`Open ${ns}`}
            className="px-2 py-0.5 rounded-md bg-slate-50 border border-slate-200 text-[11px] font-medium text-slate-600 hover:border-indigo-300 hover:text-indigo-600">
            {ns}
          </button>
        ))}
        {p.namespaces.length > MAX_CHIPS && <span className="px-2 py-0.5 text-[11px] text-slate-400">+{p.namespaces.length - MAX_CHIPS} more</span>}
      </div>

      {(p.canOperate || p.canAdmin) && (
        <div className="mt-auto pt-3 border-t border-slate-100 flex items-center gap-2">
          {p.canOperate && (manual ? (
            <button onClick={p.onResume} disabled={p.busy}
              className="flex items-center gap-1.5 px-3 py-1.5 rounded-lg bg-amber-50 border border-amber-200 text-amber-700 text-xs font-bold hover:bg-amber-100 disabled:opacity-50">
              <RotateCcw size={13} /> Follow schedule
            </button>
          ) : up ? (
            <button onClick={p.onStop} disabled={p.busy}
              className="flex items-center gap-1.5 px-3 py-1.5 rounded-lg bg-slate-50 border border-slate-200 text-slate-700 text-xs font-bold hover:bg-slate-100 disabled:opacity-50">
              <Square size={13} /> Scale down now
            </button>
          ) : (
            <button onClick={p.onStart} disabled={p.busy}
              className="flex items-center gap-1.5 px-3 py-1.5 rounded-lg bg-emerald-50 border border-emerald-200 text-emerald-700 text-xs font-bold hover:bg-emerald-100 disabled:opacity-50">
              <Play size={13} /> Start now
            </button>
          ))}
          {p.busy && <span className="w-3.5 h-3.5 border-2 border-slate-200 border-t-indigo-500 rounded-full animate-spin" />}
          {p.canAdmin && (
            <button onClick={p.onEdit} className="ml-auto flex items-center gap-1.5 px-3 py-1.5 rounded-lg text-slate-500 text-xs font-bold hover:bg-slate-100">
              <Pencil size={13} /> Edit
            </button>
          )}
        </div>
      )}
    </div>
  )
}
