import { useMemo, useState } from 'react'
import { X, Search, ChevronDown, ChevronRight, Check } from 'lucide-react'
import { apiError, errorMessage } from '../lib/api'
import { planFromSpec, planProblem, specFromPlan, type SchedulePlan } from '../lib/schedule'
import type { ScalingConfig, ScalingGroup, ScalingGroupSpec, ScalingSpec } from '../lib/types'
import ScheduleEditor from './ScheduleEditor'

const K8S_NAME = /^[a-z0-9]([-a-z0-9]*[a-z0-9])?$/

const sanitize = (s: string) => s.toLowerCase().trim().replace(/[^a-z0-9-]/g, '-').replace(/-+/g, '-').replace(/^-|-$/g, '')

// suggestName proposes a schedule name from its namespaces: the namespace itself, or their
// shared prefix ("pps1-boss", "pps1-genai" -> "pps1").
function suggestName(namespaces: string[]): string {
  if (namespaces.length === 0) return ''
  if (namespaces.length === 1) return namespaces[0]
  let prefix = namespaces[0]
  for (const ns of namespaces) while (!ns.startsWith(prefix)) prefix = prefix.slice(0, -1)
  prefix = prefix.replace(/[-.]+$/, '')
  return prefix.length >= 3 ? prefix : `${namespaces[0]}-group`
}

const sameSchedule = (a: Pick<ScalingSpec, 'schedules' | 'activation'>, b: Pick<ScalingSpec, 'schedules' | 'activation'>) =>
  JSON.stringify(a.schedules || []) === JSON.stringify(b.schedules || []) && (a.activation || '') === (b.activation || '')

type Props = {
  namespaces: string[]
  groups: ScalingGroup[]
  onClose: () => void
  onSaved: () => void
} & (
  | { kind: 'group'; existing?: ScalingGroup; initialNamespaces?: string[] }
  | { kind: 'config'; existing: ScalingConfig }
)

// ScheduleWizard creates or edits a schedule in one screen: which namespaces, when they
// run, and a name. Ordering, dependencies and timeouts stay folded under Advanced.
export default function ScheduleWizard(props: Props) {
  const { namespaces, groups, onClose, onSaved } = props
  const isGroup = props.kind === 'group'
  const existingGroup = props.kind === 'group' ? props.existing : undefined
  const existing = props.existing
  const editing = !!existing

  const [selected, setSelected] = useState<string[]>(
    props.kind === 'config' ? [props.existing.spec.targetNamespace] : props.existing?.spec.namespaces || props.initialNamespaces || [])
  const [plan, setPlan] = useState<SchedulePlan>(() => planFromSpec(existing?.spec || {}))
  const [name, setName] = useState(existing?.metadata.name || '')
  const [nameTouched, setNameTouched] = useState(editing)
  const [filter, setFilter] = useState('')
  const [advanced, setAdvanced] = useState(false)
  const [dependsOn, setDependsOn] = useState<string[]>(existingGroup?.spec.dependsOn || [])
  const [category, setCategory] = useState(existingGroup?.spec.category || '')
  const [giveUp, setGiveUp] = useState(existingGroup?.spec.featureFlags?.skipOnTimeout || false)
  const [giveUpMinutes, setGiveUpMinutes] = useState(existingGroup?.spec.featureFlags?.timeoutMinutes || 10)
  const [saving, setSaving] = useState(false)
  const [error, setError] = useState<string | null>(null)

  const finalName = nameTouched ? sanitize(name) : sanitize(suggestName(selected))
  const ownerOf = useMemo(() => {
    const m = new Map<string, string>()
    for (const g of groups) if (g.metadata.name !== existing?.metadata.name) for (const ns of g.spec.namespaces) m.set(ns, g.metadata.name)
    return m
  }, [groups, existing])
  const categories = Array.from(new Set(groups.map(g => g.spec.category).filter(Boolean)))
  const scheduleChanged = !sameSchedule(specFromPlan(plan), existing?.spec || {})
  const overrideActive = existing?.spec.active !== undefined && existing?.spec.active !== null

  const problem = planProblem(plan)
    || (selected.length === 0 ? 'Pick at least one namespace.' : null)
    || (!K8S_NAME.test(finalName) ? 'Name must use lowercase letters, digits and dashes.' : null)

  const save = async () => {
    if (problem) return
    setSaving(true)
    setError(null)
    try {
      const schedule = specFromPlan(plan)
      let url: string, method: string, spec: ScalingSpec
      if (props.kind === 'group') {
        const base: Partial<ScalingGroupSpec> = props.existing?.spec || {}
        spec = {
          ...base,
          ...schedule,
          category: category.trim() || base.category || 'General',
          namespaces: selected,
          dependsOn: dependsOn.length ? dependsOn : undefined,
          featureFlags: giveUp ? { skipOnTimeout: true, timeoutMinutes: giveUpMinutes } : undefined,
        }
        url = editing ? `/api/scaling/groups/${existing!.metadata.name}` : '/api/scaling/groups'
        method = editing ? 'PUT' : 'POST'
      } else {
        spec = { ...props.existing.spec, schedules: schedule.schedules }
        url = `/api/scaling/configs/${props.existing.metadata.name}`
        method = 'PUT'
      }
      // A new schedule is meant to take effect, so it ends a manual override.
      if (scheduleChanged) {
        delete spec.active
        delete spec.activeUntil
      }
      const res = await fetch(url, {
        method,
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ metadata: { name: editing ? existing!.metadata.name : finalName }, spec }),
      })
      if (!res.ok) throw new Error(await apiError(res))
      onSaved()
    } catch (err) {
      setError(errorMessage(err))
    } finally {
      setSaving(false)
    }
  }

  const shown = namespaces.filter(ns => ns.includes(filter.trim().toLowerCase()))

  return (
    <div className="fixed inset-0 bg-slate-900/60 backdrop-blur-sm z-[100] flex items-center justify-center p-4">
      <div className="bg-white rounded-2xl shadow-2xl w-full max-w-3xl max-h-[92vh] flex flex-col animate-in fade-in zoom-in duration-200">
        <div className="px-6 py-4 border-b border-slate-100 flex items-center justify-between">
          <div>
            <h2 className="text-xl font-black text-slate-800">{editing ? `Edit ${existing!.metadata.name}` : 'New schedule'}</h2>
            <p className="text-xs text-slate-500">Workloads are scaled to zero outside the hours you choose and restored afterwards.</p>
          </div>
          <button onClick={onClose} className="p-2 rounded-full text-slate-400 hover:bg-slate-100"><X size={18} /></button>
        </div>

        <div className="p-6 space-y-6 overflow-y-auto">
          <section>
            <h3 className="text-sm font-bold text-slate-700 mb-2"><span className="text-indigo-500">1.</span> Which namespaces?</h3>
            {isGroup ? (
              <>
                <div className="relative mb-2">
                  <Search size={14} className="absolute left-3 top-1/2 -translate-y-1/2 text-slate-400" />
                  <input value={filter} onChange={e => setFilter(e.target.value)} placeholder="Filter namespaces"
                    className="w-full pl-8 pr-3 py-2 text-sm bg-slate-50 border border-slate-200 rounded-lg outline-none focus:border-indigo-500" />
                </div>
                <div className="max-h-44 overflow-y-auto grid grid-cols-2 md:grid-cols-3 gap-1.5 pr-1">
                  {shown.map(ns => {
                    const owner = ownerOf.get(ns)
                    const on = selected.includes(ns)
                    return (
                      <button key={ns} type="button" disabled={!!owner}
                        onClick={() => setSelected(on ? selected.filter(n => n !== ns) : [...selected, ns])}
                        title={owner ? `Already scheduled by ${owner}` : undefined}
                        className={`flex items-center gap-2 px-2.5 py-2 rounded-lg border text-left text-sm transition-colors ${
                          owner ? 'border-transparent bg-slate-50 text-slate-300 cursor-not-allowed'
                            : on ? 'border-indigo-300 bg-indigo-50 text-indigo-800' : 'border-slate-200 hover:border-slate-300 text-slate-700'}`}>
                        <span className={`w-4 h-4 shrink-0 rounded border flex items-center justify-center ${on ? 'bg-indigo-600 border-indigo-600' : 'border-slate-300 bg-white'}`}>
                          {on && <Check size={12} className="text-white" />}
                        </span>
                        <span className="truncate font-semibold">{ns}</span>
                        {owner && <span className="ml-auto text-[10px] truncate">in {owner}</span>}
                      </button>
                    )
                  })}
                </div>
                <p className="text-[11px] text-slate-400 mt-1">{selected.length} selected</p>
              </>
            ) : (
              <p className="text-sm font-semibold text-slate-700">{selected[0]}</p>
            )}
          </section>

          <section>
            <h3 className="text-sm font-bold text-slate-700 mb-2"><span className="text-indigo-500">2.</span> When should they run?</h3>
            <ScheduleEditor plan={plan} onChange={setPlan} allowOnDemand={isGroup} />
            {editing && overrideActive && scheduleChanged && (
              <p className="mt-2 text-xs font-semibold text-amber-600">Saving ends the current manual override, so the new schedule applies straight away.</p>
            )}
          </section>

          {isGroup && (
            <section>
              <h3 className="text-sm font-bold text-slate-700 mb-2"><span className="text-indigo-500">3.</span> Name</h3>
              <input value={editing ? existing!.metadata.name : nameTouched ? name : suggestName(selected)} disabled={editing}
                onChange={e => { setName(e.target.value); setNameTouched(true) }} placeholder="for example pps1"
                className="w-full md:w-80 px-3 py-2 text-sm font-semibold bg-slate-50 border border-slate-200 rounded-lg outline-none focus:border-indigo-500 disabled:opacity-60" />
              {!editing && finalName && finalName !== (nameTouched ? name : suggestName(selected)) && (
                <p className="text-[11px] text-slate-400 mt-1">Saved as <b>{finalName}</b></p>
              )}
            </section>
          )}

          {isGroup && (
            <section className="border-t border-slate-100 pt-4">
              <button type="button" onClick={() => setAdvanced(!advanced)} className="flex items-center gap-1 text-sm font-bold text-slate-500 hover:text-slate-700">
                {advanced ? <ChevronDown size={16} /> : <ChevronRight size={16} />} Advanced
                {(dependsOn.length > 0 || giveUp) && <span className="ml-1 text-[10px] font-bold text-indigo-500">(in use)</span>}
              </button>
              {advanced && (
                <div className="mt-3 space-y-5 pl-5">
                  <div>
                    <div className="text-sm font-bold text-slate-700">Start only after</div>
                    <p className="text-[11px] text-slate-500 mb-2">This schedule waits until the selected ones are fully up, and keeps them up while it runs. Use it for a shared platform that environments need.</p>
                    <div className="flex flex-wrap gap-1.5">
                      {groups.filter(g => g.metadata.name !== existing?.metadata.name).map(g => {
                        const on = dependsOn.includes(g.metadata.name)
                        return (
                          <button key={g.metadata.name} type="button" onClick={() => setDependsOn(on ? dependsOn.filter(d => d !== g.metadata.name) : [...dependsOn, g.metadata.name])}
                            className={`px-2.5 py-1 rounded-lg text-xs font-bold border ${on ? 'bg-violet-600 border-violet-600 text-white' : 'bg-white border-slate-200 text-slate-500 hover:border-slate-300'}`}>
                            {g.metadata.name}
                          </button>
                        )
                      })}
                      {groups.filter(g => g.metadata.name !== existing?.metadata.name).length === 0 && <span className="text-xs text-slate-400 italic">No other schedules yet.</span>}
                    </div>
                  </div>
                  <div>
                    <div className="text-sm font-bold text-slate-700">Don't wait forever</div>
                    <label className="flex items-center gap-2 text-xs text-slate-600 mt-1">
                      <input type="checkbox" checked={giveUp} onChange={e => setGiveUp(e.target.checked)} className="accent-indigo-600" />
                      Move on if a namespace is not ready after
                      <input type="number" min={1} max={60} value={giveUpMinutes} disabled={!giveUp}
                        onChange={e => setGiveUpMinutes(Math.max(1, Math.min(60, Number(e.target.value) || 10)))}
                        className="w-14 px-2 py-1 border border-slate-200 rounded-md text-center disabled:opacity-50" />
                      minutes
                    </label>
                  </div>
                  <div>
                    <div className="text-sm font-bold text-slate-700">Section</div>
                    <p className="text-[11px] text-slate-500 mb-1">Groups schedules on the page, for example Platform or Environments.</p>
                    <input value={category} onChange={e => setCategory(e.target.value)} placeholder="General" list="schedule-sections"
                      className="w-56 px-3 py-1.5 text-sm bg-slate-50 border border-slate-200 rounded-lg outline-none focus:border-indigo-500" />
                    <datalist id="schedule-sections">{categories.map(c => <option key={c} value={c} />)}</datalist>
                  </div>
                </div>
              )}
            </section>
          )}
        </div>

        <div className="px-6 py-4 border-t border-slate-100 flex items-center gap-3">
          <span className="flex-1 text-xs font-semibold text-rose-600">{error || (problem && selected.length > 0 ? problem : '')}</span>
          <button onClick={onClose} className="px-4 py-2 rounded-lg text-sm font-bold text-slate-500 hover:bg-slate-100">Cancel</button>
          <button onClick={save} disabled={!!problem || saving}
            className="px-5 py-2 rounded-lg text-sm font-bold bg-indigo-600 text-white hover:bg-indigo-700 disabled:opacity-40">
            {saving ? 'Saving…' : editing ? 'Save changes' : 'Create schedule'}
          </button>
        </div>
      </div>
    </div>
  )
}
