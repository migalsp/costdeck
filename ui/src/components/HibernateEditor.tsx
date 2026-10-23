import { useState } from 'react'
import { Plus, X } from 'lucide-react'
import { parseNames, presets, ruleIssue, type Preset } from '../lib/hibernate'
import type { HibernateRule } from '../lib/types'
import { Button } from './ui'

const input = 'bg-white border border-slate-200 rounded-lg px-2.5 py-1.5 text-sm outline-none focus:border-brand-500'

// NamesInput keeps what is typed, so "pg-*, " does not lose its comma while the next name
// is on its way; the rule gets the parsed names. Names changed from outside (another rule
// removed above this one) replace the text.
function NamesInput({ names, onChange, label }: { names?: string[]; onChange: (names?: string[]) => void; label: string }) {
  const [text, setText] = useState((names || []).join(', '))
  const shown = (parseNames(text) || []).join() === (names || []).join() ? text : (names || []).join(', ')
  return (
    <input value={shown} aria-label={label} placeholder="every one, or names such as pg-*"
      onChange={e => { setText(e.target.value); onChange(parseNames(e.target.value)) }}
      className={`${input} w-56`} />
  )
}

// HibernateEditor edits hibernate rules: the operators CostDeck knows as checkboxes, and
// rules for other operators by apiVersion, kind and replica field.
export default function HibernateEditor({ rules, onChange }: { rules: HibernateRule[]; onChange: (rules: HibernateRule[]) => void }) {
  const update = (i: number, r: HibernateRule) => onChange(rules.map((x, j) => (j === i ? r : x)))
  const presetIndex = (id: Preset) => rules.findIndex(r => r.preset === id)
  const toggle = (id: Preset, on: boolean) => onChange(on ? [...rules, { preset: id }] : rules.filter(r => r.preset !== id))
  const field = (i: number, key: 'apiVersion' | 'kind' | 'replicasField' | 'readyField', value: string) => update(i, { ...rules[i], [key]: value || undefined })

  return (
    <div className="space-y-3" data-testid="hibernate-editor">
      <div className="space-y-2">
        {presets.map(p => {
          const i = presetIndex(p.id)
          return (
            <div key={p.id} className="flex flex-wrap items-center gap-x-3 gap-y-1">
              <label className="flex items-start gap-2 text-sm text-slate-600 min-w-64 flex-1">
                <input type="checkbox" checked={i >= 0} onChange={e => toggle(p.id, e.target.checked)} className="mt-0.5 accent-brand-600" />
                <span><b className="text-slate-800">{p.label}</b>: {p.covers}</span>
              </label>
              {i >= 0 && <NamesInput names={rules[i].names} label={`${p.label} names`} onChange={names => update(i, { ...rules[i], names })} />}
            </div>
          )
        })}
      </div>

      {rules.map((r, i) => {
        if (r.preset) return null
        const issue = ruleIssue(r)
        return (
          <div key={i} className="rounded-lg border border-slate-200 bg-slate-50/60 p-3" data-testid="custom-rule">
            <div className="flex flex-wrap items-center gap-2">
              <input value={r.apiVersion || ''} onChange={e => field(i, 'apiVersion', e.target.value.trim())} placeholder="apiVersion, e.g. redis.example.com/v1" aria-label="apiVersion" className={`${input} w-64`} />
              <input value={r.kind || ''} onChange={e => field(i, 'kind', e.target.value.trim())} placeholder="Kind" aria-label="Kind" className={`${input} w-32`} />
              <button type="button" onClick={() => onChange(rules.filter((_, j) => j !== i))} className="ml-auto p-1 rounded text-slate-400 hover:bg-white hover:text-rose-600" title="Remove rule"><X size={14} /></button>
            </div>
            <div className="flex flex-wrap items-center gap-2 mt-2">
              <input value={r.replicasField || ''} onChange={e => field(i, 'replicasField', e.target.value.trim())} placeholder="replicas field, e.g. spec.replicas" aria-label="Replicas field" className={`${input} w-56`} />
              <input value={r.readyField || ''} onChange={e => field(i, 'readyField', e.target.value.trim())} placeholder="ready field (optional)" aria-label="Ready field" className={`${input} w-48`} />
              <NamesInput names={r.names} label="Names" onChange={names => update(i, { ...r, names })} />
            </div>
            {issue && <p className="mt-1.5 text-xs font-medium text-rose-600">{issue}</p>}
          </div>
        )
      })}
      <Button size="sm" variant="secondary" icon={<Plus size={13} />} onClick={() => onChange([...rules, {}])}>Another operator</Button>
      <p className="text-xs text-slate-400">
        Services stop after the workloads and start before them; only those Cost Deck stopped are started again. The workloads their operators run are left to them.
        Needs <code>hibernation.enabled</code> in the Helm chart, and <code>hibernation.extraRules</code> for other operators.
      </p>
    </div>
  )
}
