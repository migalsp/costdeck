import { useState } from 'react'
import { CalendarClock, Play, PowerOff } from 'lucide-react'
import { Button, Modal } from './ui'

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
  { id: 'forever', label: 'Until I hand it back to the schedule', hint: 'The schedule stays ignored until you click "Follow schedule".' },
]

// OverrideDialog asks how long a manual scale action should hold. A manual override fully
// disables the schedule, so an open-ended one is offered last rather than by default.
export default function OverrideDialog({ name, kind, active, hasSchedule, onCancel, onConfirm }: OverrideDialogProps) {
  const options = OPTIONS.filter(o => !o.needsSchedule || hasSchedule)
  const [choice, setChoice] = useState<OverrideUntil>(options[0].id)

  return (
    <Modal
      size="sm"
      title={active ? `Start ${name} now` : `Scale ${name} down now`}
      subtitle={kind === 'group' ? 'Schedule' : 'Namespace'}
      onClose={onCancel}
      footer={
        <>
          <Button variant="ghost" onClick={onCancel}>Cancel</Button>
          <Button variant={active ? 'primary' : 'danger'} icon={active ? <Play size={14} /> : <PowerOff size={14} />} onClick={() => onConfirm(choice)}>
            {active ? 'Start now' : 'Scale down now'}
          </Button>
        </>
      }
    >
      <p className="text-sm text-slate-500 mb-3 flex items-start gap-1.5">
        <CalendarClock size={15} className="shrink-0 mt-0.5" />
        The schedule is ignored while this holds. When should it take over again?
      </p>
      <div className="space-y-2">
        {options.map(o => (
          <label key={o.id} className={`flex items-start gap-3 p-3 rounded-lg border cursor-pointer transition-colors ${choice === o.id ? 'border-brand-300 bg-brand-50/60' : 'border-slate-200 hover:border-slate-300'}`}>
            <input type="radio" name="override-until" checked={choice === o.id} onChange={() => setChoice(o.id)} className="mt-0.5 accent-brand-600" />
            <span>
              <span className="block text-sm font-medium text-slate-800">{o.label}</span>
              {o.hint && <span className="block text-xs text-slate-500">{o.hint}</span>}
            </span>
          </label>
        ))}
      </div>
    </Modal>
  )
}
