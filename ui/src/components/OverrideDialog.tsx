import { useState } from 'react'
import { Play, Square, X, CalendarClock } from 'lucide-react'

export type OverrideUntil = 'nextTransition' | '1h' | '4h' | '8h' | '24h' | 'forever'

interface OverrideDialogProps {
  name: string
  kind: 'group' | 'config'
  active: boolean
  hasSchedule: boolean
  onCancel: () => void
  onConfirm: (until: OverrideUntil) => void
}

const OPTIONS: { id: OverrideUntil; label: string; hint: string; needsSchedule?: boolean }[] = [
  { id: 'nextTransition', label: 'Until the next scheduled change', hint: 'The schedule takes over again automatically.', needsSchedule: true },
  { id: '1h', label: 'For 1 hour', hint: '' },
  { id: '4h', label: 'For 4 hours', hint: '' },
  { id: '8h', label: 'For 8 hours', hint: '' },
  { id: '24h', label: 'For 24 hours', hint: '' },
  { id: 'forever', label: 'Until I resume the schedule', hint: 'The schedule stays ignored until you click "Follow schedule".' },
]

// OverrideDialog asks how long a manual scale action should hold. A manual override fully
// disables the schedule, so an open-ended one is offered last rather than by default.
export default function OverrideDialog({ name, kind, active, hasSchedule, onCancel, onConfirm }: OverrideDialogProps) {
  const options = OPTIONS.filter(o => !o.needsSchedule || hasSchedule)
  const [choice, setChoice] = useState<OverrideUntil>(options[0].id)

  return (
    <div className="fixed inset-0 bg-slate-900/60 backdrop-blur-sm z-[200] flex items-center justify-center p-4" onClick={onCancel}>
      <div className="bg-white rounded-2xl shadow-2xl w-full max-w-md" onClick={e => e.stopPropagation()}>
        <div className="p-6 border-b border-slate-100 flex items-start justify-between gap-4">
          <div className="flex items-center gap-3">
            <div className={`w-10 h-10 rounded-xl flex items-center justify-center ${active ? 'bg-emerald-50 text-emerald-500' : 'bg-rose-50 text-rose-500'}`}>
              {active ? <Play size={18} /> : <Square size={18} />}
            </div>
            <div>
              <h3 className="font-black text-slate-800">Force {active ? 'up' : 'down'}</h3>
              <p className="text-xs text-slate-500">{kind === 'group' ? 'Group' : 'Namespace'} <b className="text-slate-700">{name}</b></p>
            </div>
          </div>
          <button onClick={onCancel} className="p-1.5 text-slate-400 hover:bg-slate-100 rounded-lg"><X size={16} /></button>
        </div>
        <div className="p-6 space-y-2">
          <p className="text-[11px] text-slate-500 mb-3 flex items-start gap-1.5">
            <CalendarClock size={13} className="shrink-0 mt-0.5" />
            While a manual override is active the schedule is ignored. Choose when it should hand control back.
          </p>
          {options.map(o => (
            <label key={o.id} className={`flex items-start gap-3 p-3 rounded-xl border cursor-pointer transition-all ${choice === o.id ? 'border-indigo-300 bg-indigo-50/50' : 'border-slate-200 hover:border-slate-300'}`}>
              <input type="radio" name="override-until" checked={choice === o.id} onChange={() => setChoice(o.id)} className="mt-0.5 accent-indigo-600" />
              <span>
                <span className="block text-sm font-bold text-slate-700">{o.label}</span>
                {o.hint && <span className="block text-[11px] text-slate-400">{o.hint}</span>}
              </span>
            </label>
          ))}
        </div>
        <div className="p-6 pt-0 flex gap-3">
          <button onClick={onCancel} className="flex-1 px-4 py-2.5 rounded-xl font-bold text-slate-500 border border-slate-200 hover:bg-slate-50">Cancel</button>
          <button
            onClick={() => onConfirm(choice)}
            className={`flex-1 px-4 py-2.5 rounded-xl font-bold text-white shadow-lg ${active ? 'bg-emerald-500 hover:bg-emerald-600 shadow-emerald-500/20' : 'bg-rose-500 hover:bg-rose-600 shadow-rose-500/20'}`}
          >
            Scale {active ? 'up' : 'down'}
          </button>
        </div>
      </div>
    </div>
  )
}

// relativeTime renders a future timestamp as "in 3h 20m".
export function relativeTime(iso: string): string {
  const ms = new Date(iso).getTime() - Date.now()
  if (ms <= 0) return 'now'
  const minutes = Math.round(ms / 60000)
  if (minutes < 60) return `in ${minutes}m`
  const hours = Math.floor(minutes / 60)
  if (hours < 48) return `in ${hours}h ${minutes % 60}m`
  return `in ${Math.floor(hours / 24)}d ${hours % 24}h`
}
