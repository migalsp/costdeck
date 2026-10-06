import { useState, type KeyboardEvent } from 'react'
import { ArrowDown, ArrowUp, Plus, ShieldCheck, ListOrdered, X } from 'lucide-react'
import type { ScalingSpec } from '../lib/types'
import { usePolling } from '../lib/usePolling'
import { Button, Modal } from './ui'

interface Workload { name: string; kind: string }

interface Props {
  namespace: string
  spec: ScalingSpec
  onClose: () => void
  onSave: (spec: ScalingSpec) => Promise<void> | void
}

// PatternPicker adds a workload by choosing it from the namespace or typing a pattern
// such as "api-*".
function PatternPicker({ options, onAdd, placeholder }: { options: string[]; onAdd: (p: string) => void; placeholder: string }) {
  const [text, setText] = useState('')
  const add = () => { const v = text.trim(); if (v) { onAdd(v); setText('') } }
  return (
    <div className="flex flex-wrap items-center gap-2">
      {options.length > 0 && (
        <select value="" onChange={e => onAdd(e.target.value)}
          className="bg-white border border-slate-200 rounded-lg px-2.5 py-1.5 text-sm text-slate-700 outline-none focus:border-brand-500">
          <option value="" disabled>Choose a workload…</option>
          {options.map(o => <option key={o} value={o}>{o}</option>)}
        </select>
      )}
      <input value={text} onChange={e => setText(e.target.value)} placeholder={placeholder}
        onKeyDown={(e: KeyboardEvent) => { if (e.key === 'Enter') { e.preventDefault(); add() } }}
        className="w-44 bg-white border border-slate-200 rounded-lg px-2.5 py-1.5 text-sm outline-none focus:border-brand-500" />
      <Button size="sm" variant="ghost" icon={<Plus size={13} />} onClick={add} disabled={!text.trim()}>Add</Button>
    </div>
  )
}

function Chip({ label, onRemove }: { label: string; onRemove: () => void }) {
  return (
    <span className="inline-flex items-center gap-1 pl-2 pr-1 py-0.5 rounded-md border border-slate-200 bg-white text-xs font-medium text-slate-700">
      {label}
      <button type="button" onClick={onRemove} className="p-0.5 rounded text-slate-400 hover:text-rose-600" title={`Remove ${label}`}><X size={12} /></button>
    </span>
  )
}

// WorkloadRulesDialog edits the per-namespace rules every schedule respects: workloads
// that never scale down, and the order workloads start in (stopping runs in reverse).
export default function WorkloadRulesDialog({ namespace, spec, onClose, onSave }: Props) {
  const [workloads, setWorkloads] = useState<Workload[]>([])
  const [exclusions, setExclusions] = useState<string[]>(spec.exclusions || [])
  const [stages, setStages] = useState<string[][]>((spec.sequence || []).map(s => s.split(/\s+/).filter(Boolean)).filter(s => s.length > 0))
  const [saving, setSaving] = useState(false)
  const [error, setError] = useState<string | null>(null)

  usePolling(() => {
    fetch(`/api/namespaces/${namespace}/workloads`).then(r => (r.ok ? r.json() : [])).then(setWorkloads).catch(() => {})
  }, null, namespace)

  const names = workloads.map(w => w.name).sort()
  const inStages = new Set(stages.flat())

  const setStage = (i: number, items: string[]) => setStages(stages.map((s, j) => (j === i ? items : s)).filter(s => s.length > 0))
  const swap = (i: number, j: number) => { const next = [...stages]; [next[i], next[j]] = [next[j], next[i]]; setStages(next) }

  const save = async () => {
    setSaving(true)
    setError(null)
    try {
      const clean = stages.filter(s => s.length > 0)
      await onSave({ ...spec, exclusions: exclusions.length ? exclusions : undefined, sequence: clean.length ? clean.map(s => s.join(' ')) : undefined })
    } catch (err) {
      setError(err instanceof Error ? err.message : String(err))
      setSaving(false)
    }
  }

  return (
    <Modal
      size="lg"
      dismissOnBackdrop={false}
      title="Workload rules"
      subtitle={<>Namespace <b className="text-slate-700">{namespace}</b> · respected by whichever schedule scales it</>}
      onClose={onClose}
      footer={
        <>
          <span className="flex-1 text-xs font-medium text-rose-600">{error}</span>
          <Button variant="ghost" onClick={onClose}>Cancel</Button>
          <Button variant="primary" onClick={save} disabled={saving}>{saving ? 'Saving…' : 'Save rules'}</Button>
        </>
      }
    >
      <div className="space-y-6">
        <section>
          <h3 className="text-sm font-semibold text-slate-800 flex items-center gap-1.5"><ShieldCheck size={15} className="text-brand-600" /> Keep running</h3>
          <p className="text-sm text-slate-500 mb-2">These workloads stay up while the rest of the namespace scales down. Patterns such as <code>api-*</code> work.</p>
          <div className="flex flex-wrap gap-1.5 mb-2">
            {exclusions.length === 0 ? <span className="text-sm text-slate-400">Nothing: every workload scales down.</span>
              : exclusions.map(e => <Chip key={e} label={e} onRemove={() => setExclusions(exclusions.filter(x => x !== e))} />)}
          </div>
          <PatternPicker options={names.filter(n => !exclusions.includes(n))} placeholder="or a pattern, e.g. api-*"
            onAdd={p => !exclusions.includes(p) && setExclusions([...exclusions, p])} />
        </section>

        <section>
          <h3 className="text-sm font-semibold text-slate-800 flex items-center gap-1.5"><ListOrdered size={15} className="text-brand-600" /> Start order</h3>
          <p className="text-sm text-slate-500 mb-1">Stages start from the top and stop from the bottom. Workloads in no stage start last and stop first.</p>
          <p className="text-xs text-slate-400 mb-3">
            A workload goes to the stage of its most specific pattern: its exact name, then a pattern such as <code>api-*</code>, then <code>*</code>.
            To stop an operator before everything it manages, put <code>*</code> in stage 1 and the operator in stage 2.
          </p>
          <div className="space-y-2">
            {stages.map((stage, i) => (
              <div key={i} className="rounded-lg border border-slate-200 bg-slate-50/60 p-3">
                <div className="flex items-center gap-2 mb-2">
                  <span className="w-6 h-6 rounded-full bg-white border border-slate-200 text-xs font-bold text-slate-600 flex items-center justify-center">{i + 1}</span>
                  <span className="text-sm font-semibold text-slate-700">Stage {i + 1}</span>
                  <span className="ml-auto flex items-center gap-0.5">
                    <button type="button" disabled={i === 0} onClick={() => swap(i, i - 1)} className="p-1 rounded text-slate-400 hover:bg-white disabled:opacity-30" title="Move up"><ArrowUp size={14} /></button>
                    <button type="button" disabled={i === stages.length - 1} onClick={() => swap(i, i + 1)} className="p-1 rounded text-slate-400 hover:bg-white disabled:opacity-30" title="Move down"><ArrowDown size={14} /></button>
                    <button type="button" onClick={() => setStage(i, [])} className="p-1 rounded text-slate-400 hover:bg-white hover:text-rose-600" title="Remove stage"><X size={14} /></button>
                  </span>
                </div>
                <div className="flex flex-wrap gap-1.5 mb-2">
                  {stage.map(item => <Chip key={item} label={item} onRemove={() => setStage(i, stage.filter(x => x !== item))} />)}
                </div>
                <PatternPicker options={names.filter(n => !inStages.has(n))} placeholder="or a pattern"
                  onAdd={p => !stage.includes(p) && setStage(i, [...stage, p])} />
              </div>
            ))}
            <Button size="sm" variant="secondary" icon={<Plus size={13} />} onClick={() => setStages([...stages, []])}>Add stage</Button>
            {stages.some(s => s.length === 0) && <p className="text-xs text-slate-400">Empty stages are dropped when saving.</p>}
          </div>
        </section>
      </div>
    </Modal>
  )
}
