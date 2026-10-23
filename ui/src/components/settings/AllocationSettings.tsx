import type { AllocationForm } from '../../lib/allocation'
import { inputClass, labelClass } from './fields'

// AllocationSettings edits spec.allocation: how cost that belongs to no team is charged in
// showback and chargeback (Reports → Cost allocation). The section's Save stores it.
export default function AllocationSettings({ value, onChange }: { value: AllocationForm; onChange: (f: AllocationForm) => void }) {
  const set = (patch: Partial<AllocationForm>) => onChange({ ...value, ...patch })
  const choice = (name: string, current: string, options: { id: string; label: string; hint: string }[], pick: (id: string) => void) => (
    <div className="grid gap-2 sm:grid-cols-2">
      {options.map(o => (
        <label key={o.id} className={`flex items-start gap-2.5 p-3 rounded-xl border cursor-pointer ${current === o.id ? 'border-brand-300 bg-brand-50/60' : 'border-slate-200 bg-white'}`}>
          <input type="radio" name={name} className="mt-0.5 accent-brand-600" checked={current === o.id} onChange={() => pick(o.id)} />
          <span><span className="block text-sm font-semibold text-slate-800">{o.label}</span><span className="block text-xs text-slate-500">{o.hint}</span></span>
        </label>
      ))}
    </div>
  )
  return (
    <div className="bg-white rounded-xl border border-slate-200 shadow-sm p-6 space-y-6">
      <div>
        <label className={labelClass} htmlFor="shared-namespaces">Shared namespaces</label>
        <input id="shared-namespaces" className={`${inputClass} font-mono`} value={value.shared} onChange={e => set({ shared: e.target.value })}
          placeholder="kube-*, monitoring, ingress-nginx" />
        <p className="mt-1 text-[11px] text-slate-400">Namespaces every team relies on. Their cost is charged to the others instead of to a team. Names or patterns such as <code>kube-*</code>, separated by commas.</p>
      </div>
      <fieldset>
        <legend className={labelClass}>Split the shared cost</legend>
        {choice('split', value.sharedSplit, [
          { id: 'proportional', label: 'By what each requests', hint: 'A namespace that requests twice the CPU and memory pays twice the share.' },
          { id: 'even', label: 'Evenly', hint: 'Every namespace pays the same share, whatever its size.' },
        ], id => set({ sharedSplit: id as AllocationForm['sharedSplit'] }))}
      </fieldset>
      <fieldset>
        <legend className={labelClass}>Capacity no pod requests</legend>
        {choice('idle', value.idle, [
          { id: 'separate', label: 'A line of its own', hint: 'Shown as unallocated capacity, for the platform team to shrink.' },
          { id: 'proportional', label: 'Charged to the namespaces', hint: 'Spread by what each requests, so the groups add up to the whole bill.' },
        ], id => set({ idle: id as AllocationForm['idle'] }))}
      </fieldset>
    </div>
  )
}
