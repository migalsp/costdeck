import { useMemo, useState } from 'react'
import { Search, Check, CalendarClock, ListOrdered, SlidersHorizontal } from 'lucide-react'
import { apiError, errorMessage } from '../lib/api'
import { planFromSpec, planProblem, specFromPlan, type SchedulePlan } from '../lib/schedule'
import type { ExternalTarget, ScalingConfig, ScalingGroup, ScalingGroupSpec, ScalingSpec } from '../lib/types'
import ScheduleEditor from './ScheduleEditor'
import StartOrderEditor, { type StartOrder } from './StartOrderEditor'
import { Button, Modal, Tabs } from './ui'

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

export type WizardTab = 'schedule' | 'order' | 'advanced'

type Props = {
  namespaces: string[]
  groups: ScalingGroup[]
  // Cloud resources offered in Start order; undefined where discovery is unavailable.
  discovered?: ExternalTarget[]
  initialTab?: WizardTab
  onClose: () => void
  onSaved: () => void
} & (
  | { kind: 'group'; existing?: ScalingGroup; initialNamespaces?: string[] }
  | { kind: 'config'; existing: ScalingConfig }
)

// ScheduleWizard creates or edits a schedule: which namespaces, when they run and a name
// (Schedule), the order they start in (Start order), and dependencies and timeouts
// (Advanced). One Save stores all of it.
export default function ScheduleWizard(props: Props) {
  const { namespaces, groups, onClose, onSaved, discovered } = props
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
  const [tab, setTab] = useState<WizardTab>(props.initialTab || 'schedule')
  const [order, setOrder] = useState<StartOrder>({ sequence: existingGroup?.spec.sequence, externalTargets: existingGroup?.spec.externalTargets })
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
          // Only keep stage entries that still belong to the schedule.
          sequence: order.sequence?.map(s => s.split(/\s+/).filter(i => selected.includes(i) || i.startsWith('ext:')).join(' ')).filter(Boolean),
          externalTargets: order.externalTargets,
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

  const scheduleTab = (
    <div className="space-y-6">
      <section>
        <h3 className="text-sm font-semibold text-slate-800 mb-2">Which namespaces?</h3>
        {isGroup ? (
          <>
            <div className="relative mb-2">
              <Search size={14} className="absolute left-3 top-1/2 -translate-y-1/2 text-slate-400" />
              <input value={filter} onChange={e => setFilter(e.target.value)} placeholder="Filter namespaces"
                className="w-full pl-8 pr-3 py-2 text-sm bg-white border border-slate-200 rounded-lg outline-none focus:border-brand-500" />
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
                        : on ? 'border-brand-300 bg-brand-50 text-brand-800' : 'border-slate-200 hover:border-slate-300 text-slate-700'}`}>
                    <span className={`w-4 h-4 shrink-0 rounded border flex items-center justify-center ${on ? 'bg-brand-600 border-brand-600' : 'border-slate-300 bg-white'}`}>
                      {on && <Check size={12} className="text-white" />}
                    </span>
                    <span className="truncate font-medium">{ns}</span>
                    {owner && <span className="ml-auto text-[10px] truncate">in {owner}</span>}
                  </button>
                )
              })}
            </div>
            <p className="text-xs text-slate-400 mt-1">{selected.length} selected</p>
          </>
        ) : (
          <p className="text-sm font-medium text-slate-700">{selected[0]}</p>
        )}
      </section>

      <section>
        <h3 className="text-sm font-semibold text-slate-800 mb-2">When should they run?</h3>
        <ScheduleEditor plan={plan} onChange={setPlan} allowOnDemand={isGroup} />
        {editing && overrideActive && scheduleChanged && (
          <p className="mt-2 text-xs font-medium text-amber-700">Saving ends the current manual override, so the new schedule applies straight away.</p>
        )}
      </section>

      {isGroup && (
        <section>
          <h3 className="text-sm font-semibold text-slate-800 mb-2">Name</h3>
          <input value={editing ? existing!.metadata.name : nameTouched ? name : suggestName(selected)} disabled={editing}
            onChange={e => { setName(e.target.value); setNameTouched(true) }} placeholder="for example pps1"
            className="w-full md:w-80 px-3 py-2 text-sm bg-white border border-slate-200 rounded-lg outline-none focus:border-brand-500 disabled:bg-slate-50 disabled:text-slate-500" />
          {!editing && finalName && finalName !== (nameTouched ? name : suggestName(selected)) && (
            <p className="text-xs text-slate-400 mt-1">Saved as <b>{finalName}</b></p>
          )}
        </section>
      )}
    </div>
  )

  const advancedTab = (
    <div className="space-y-6">
      <section>
        <h3 className="text-sm font-semibold text-slate-800">Start only after</h3>
        <p className="text-sm text-slate-500 mb-2">This schedule waits until the selected ones are fully up, and keeps them up while it runs. Use it for a shared platform that environments need.</p>
        <div className="flex flex-wrap gap-1.5">
          {groups.filter(g => g.metadata.name !== existing?.metadata.name).map(g => {
            const on = dependsOn.includes(g.metadata.name)
            return (
              <button key={g.metadata.name} type="button" onClick={() => setDependsOn(on ? dependsOn.filter(d => d !== g.metadata.name) : [...dependsOn, g.metadata.name])}
                className={`px-2.5 py-1 rounded-lg text-xs font-semibold border ${on ? 'bg-brand-600 border-brand-600 text-white' : 'bg-white border-slate-200 text-slate-600 hover:border-slate-300'}`}>
                {g.metadata.name}
              </button>
            )
          })}
          {groups.filter(g => g.metadata.name !== existing?.metadata.name).length === 0 && <span className="text-xs text-slate-400 italic">No other schedules yet.</span>}
        </div>
      </section>
      <section>
        <h3 className="text-sm font-semibold text-slate-800">Don't wait forever</h3>
        <label className="flex items-center gap-2 text-sm text-slate-600 mt-1">
          <input type="checkbox" checked={giveUp} onChange={e => setGiveUp(e.target.checked)} className="accent-brand-600" />
          Move on if a namespace is not ready after
          <input type="number" min={1} max={60} value={giveUpMinutes} disabled={!giveUp}
            onChange={e => setGiveUpMinutes(Math.max(1, Math.min(60, Number(e.target.value) || 10)))}
            className="w-16 px-2 py-1 border border-slate-200 rounded-md text-center disabled:opacity-50" />
          minutes
        </label>
      </section>
      <section>
        <h3 className="text-sm font-semibold text-slate-800">Section</h3>
        <p className="text-sm text-slate-500 mb-1">Groups schedules on the page, for example Platform or Environments.</p>
        <input value={category} onChange={e => setCategory(e.target.value)} placeholder="General" list="schedule-sections"
          className="w-56 px-3 py-1.5 text-sm bg-white border border-slate-200 rounded-lg outline-none focus:border-brand-500" />
        <datalist id="schedule-sections">{categories.map(c => <option key={c} value={c} />)}</datalist>
      </section>
    </div>
  )

  return (
    <Modal
      size="lg"
      tall
      dismissOnBackdrop={false}
      title={editing ? `Edit ${existing!.metadata.name}` : 'New schedule'}
      subtitle="Workloads are scaled to zero outside the hours you choose and restored afterwards."
      onClose={onClose}
      headerExtra={isGroup ? (
        <div className="mt-3">
          <Tabs<WizardTab>
            value={tab}
            onChange={setTab}
            tabs={[
              { id: 'schedule', label: <span className="inline-flex items-center gap-1.5"><CalendarClock size={14} /> Schedule</span> },
              { id: 'order', label: <span className="inline-flex items-center gap-1.5"><ListOrdered size={14} /> Start order{order.sequence?.length ? ` (${order.sequence.length})` : ''}</span> },
              { id: 'advanced', label: <span className="inline-flex items-center gap-1.5"><SlidersHorizontal size={14} /> Advanced{dependsOn.length || giveUp ? ' ·' : ''}</span> },
            ]}
          />
        </div>
      ) : undefined}
      footer={
        <>
          <span className="flex-1 text-xs font-medium text-rose-600">{error || (problem && selected.length > 0 ? problem : '')}</span>
          <Button variant="ghost" onClick={onClose}>Cancel</Button>
          <Button variant="primary" onClick={save} disabled={!!problem || saving}>
            {saving ? 'Saving…' : editing ? 'Save changes' : 'Create schedule'}
          </Button>
        </>
      }
    >
      {(!isGroup || tab === 'schedule') && scheduleTab}
      {isGroup && tab === 'order' && (
        selected.length === 0
          ? <p className="text-sm text-slate-500">Pick namespaces on the Schedule tab first.</p>
          : <StartOrderEditor namespaces={selected} value={order} onChange={setOrder} discovered={discovered} />
      )}
      {isGroup && tab === 'advanced' && advancedTab}
    </Modal>
  )
}
