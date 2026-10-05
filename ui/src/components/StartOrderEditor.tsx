import { useState, type DragEvent } from 'react'
import { ArrowDown, ArrowUp, Cloud, GripVertical } from 'lucide-react'
import { cloudLabel, targetLabel } from '../lib/cloud'
import type { ExternalTarget } from '../lib/types'

const EXT = 'ext:'
const UNPLACED = -1

export interface StartOrder {
  sequence?: string[]
  externalTargets?: ExternalTarget[]
}

interface Props {
  namespaces: string[]
  value: StartOrder
  onChange: (value: StartOrder) => void
  // Cloud resources that can be added; leave undefined where discovery is not available.
  discovered?: ExternalTarget[]
}

// idOf strips the ext: prefix of a cloud resource entry.
const idOf = (item: string) => (item.startsWith(EXT) ? item.slice(EXT.length) : item)

// stagesOf parses spec.sequence into stages, keeping only items that still belong to the
// schedule (a namespace deselected elsewhere simply drops out).
function stagesOf(namespaces: string[], value: StartOrder): string[][] {
  const valid = new Set([...namespaces, ...(value.externalTargets || []).map(t => EXT + t.identifier)])
  return (value.sequence || []).map(s => s.split(/\s+/).filter(i => valid.has(i))).filter(s => s.length > 0)
}

// StartOrderEditor arranges a schedule's namespaces and cloud resources into stages:
// stages start top to bottom and stop bottom to top. Drag items between stages, or use
// each item's menu. It is controlled: every change is reported through onChange.
export default function StartOrderEditor({ namespaces, value, onChange, discovered }: Props) {
  const [dragging, setDragging] = useState<string | null>(null)
  const [over, setOver] = useState<number | 'new' | null>(null)

  const stages = stagesOf(namespaces, value)
  const targets = value.externalTargets || []
  const placed = new Set(stages.flat())
  const unplaced = namespaces.filter(ns => !placed.has(ns))
  const addable = (discovered || []).filter(d => !targets.some(t => t.identifier === d.identifier))

  const emit = (next: string[][], nextTargets = targets) => {
    const clean = next.filter(s => s.length > 0)
    onChange({
      sequence: clean.length ? clean.map(s => s.join(' ')) : undefined,
      externalTargets: nextTargets.length ? nextTargets : undefined,
    })
  }

  // move puts an item into a stage (index), a new last stage ('new') or back to unplaced.
  const move = (item: string, to: number | 'new') => {
    const without = stages.map(s => s.filter(i => i !== item))
    if (to === 'new') emit([...without, [item]])
    else if (to === UNPLACED) emit(without)
    else emit(without.map((s, i) => (i === to ? [...s, item] : s)))
  }

  const swap = (i: number, j: number) => {
    const next = [...stages]
    ;[next[i], next[j]] = [next[j], next[i]]
    emit(next)
  }

  const removeTarget = (id: string) =>
    emit(stages.map(s => s.filter(i => i !== EXT + id)), targets.filter(t => t.identifier !== id))

  const addTarget = (id: string) => {
    const found = (discovered || []).find(d => d.identifier === id)
    if (!found) return
    const target: ExternalTarget = { provider: found.provider || 'aws', type: found.type, identifier: found.identifier, region: found.region, name: found.name }
    // Cloud dependencies (databases) usually start first.
    emit(stages.length ? [[EXT + id, ...stages[0]], ...stages.slice(1)] : [[EXT + id]], [...targets, target])
  }

  const dropProps = (zone: number | 'new') => ({
    onDragOver: (e: DragEvent) => { e.preventDefault(); setOver(zone) },
    onDragLeave: () => setOver(o => (o === zone ? null : o)),
    onDrop: (e: DragEvent) => {
      e.preventDefault()
      const item = e.dataTransfer.getData('text/plain')
      if (item) move(item, zone)
      setOver(null)
      setDragging(null)
    },
  })

  const display = (item: string) => (item.startsWith(EXT) ? targetLabel(idOf(item), targets) : item)

  const renderItem = (item: string, stage: number) => {
    const ext = item.startsWith(EXT)
    return (
      <span
        key={item}
        draggable
        onDragStart={e => { e.dataTransfer.setData('text/plain', item); setDragging(item) }}
        onDragEnd={() => { setDragging(null); setOver(null) }}
        className={`group inline-flex items-center gap-1 pl-1.5 pr-1 py-1 rounded-md border text-xs font-medium bg-white cursor-grab ${
          dragging === item ? 'opacity-40' : ''} ${ext ? 'border-amber-200 text-amber-800' : 'border-slate-200 text-slate-700'}`}
      >
        <GripVertical size={12} className="text-slate-300" />
        {ext && <Cloud size={12} className="text-amber-500" />}
        <span className="max-w-[14rem] truncate" title={idOf(item)}>{display(item)}</span>
        <span className="relative ml-0.5">
          <span className="px-1 text-slate-400 group-hover:text-slate-600">⋯</span>
          {/* A native select keeps moving keyboard-accessible without a custom menu. */}
          <select
            aria-label={`Move ${display(item)}`}
            className="absolute inset-0 opacity-0 cursor-pointer"
            value=""
            onChange={e => {
              const v = e.target.value
              if (v === 'remove') removeTarget(idOf(item))
              else move(item, v === 'new' ? 'new' : Number(v))
            }}
          >
            <option value="" disabled>Move to…</option>
            {stages.map((_, i) => i).filter(i => i !== stage).map(i => <option key={i} value={i}>Stage {i + 1}</option>)}
            <option value="new">New last stage</option>
            {!ext && stage !== UNPLACED && <option value={UNPLACED}>Not placed (starts last)</option>}
            {ext && <option value="remove">Remove from schedule</option>}
          </select>
        </span>
      </span>
    )
  }

  return (
    <div className="space-y-3">
      <p className="text-sm text-slate-500">
        Stages start from the top and stop from the bottom. Everything in a stage starts together and must be ready before the next one starts.
      </p>

      {stages.map((stage, i) => (
        <div key={i} {...dropProps(i)}
          className={`rounded-xl border p-3 transition-colors ${over === i ? 'border-brand-400 bg-brand-50/50' : 'border-slate-200 bg-slate-50/60'}`}>
          <div className="flex items-center gap-2 mb-2">
            <span className="w-6 h-6 rounded-full bg-white border border-slate-200 text-xs font-bold text-slate-600 flex items-center justify-center">{i + 1}</span>
            <span className="text-sm font-semibold text-slate-700">Stage {i + 1}</span>
            <span className="text-xs text-slate-400">{i === 0 ? 'starts first, stops last' : ''}</span>
            <span className="ml-auto flex items-center gap-0.5">
              <button type="button" disabled={i === 0} onClick={() => swap(i, i - 1)} className="p-1 rounded text-slate-400 hover:bg-white hover:text-slate-700 disabled:opacity-30" title="Move stage up"><ArrowUp size={14} /></button>
              <button type="button" disabled={i === stages.length - 1} onClick={() => swap(i, i + 1)} className="p-1 rounded text-slate-400 hover:bg-white hover:text-slate-700 disabled:opacity-30" title="Move stage down"><ArrowDown size={14} /></button>
            </span>
          </div>
          <div className="flex flex-wrap gap-1.5">{stage.map(item => renderItem(item, i))}</div>
        </div>
      ))}

      <div {...dropProps('new')}
        className={`rounded-xl border-2 border-dashed px-3 py-3 text-xs text-center transition-colors ${over === 'new' ? 'border-brand-400 bg-brand-50/50 text-brand-700' : 'border-slate-200 text-slate-400'}`}>
        Drop here to start a new last stage
      </div>

      <div {...dropProps(UNPLACED)}
        className={`rounded-xl border p-3 transition-colors ${over === UNPLACED ? 'border-brand-400 bg-brand-50/50' : 'border-slate-200'}`}>
        <div className="text-sm font-semibold text-slate-700">Not placed</div>
        <div className="text-xs text-slate-400 mb-2">Start after every stage and stop first.</div>
        <div className="flex flex-wrap gap-1.5">
          {unplaced.length === 0
            ? <span className="text-xs text-slate-400 italic">Every namespace has a stage.</span>
            : unplaced.map(ns => renderItem(ns, UNPLACED))}
        </div>
      </div>

      {discovered !== undefined && (
        <div className="flex flex-wrap items-center gap-2 pt-1">
          <Cloud size={16} className="text-amber-500" />
          <span className="text-sm font-semibold text-slate-700">Cloud resources</span>
          {addable.length > 0 ? (
            <select value="" onChange={e => addTarget(e.target.value)}
              className="ml-auto bg-white border border-slate-200 rounded-lg px-2.5 py-1.5 text-sm text-slate-700 outline-none focus:border-brand-500">
              <option value="" disabled>Add a database or instance…</option>
              {addable.map(d => <option key={d.identifier} value={d.identifier}>{d.name || d.identifier} · {cloudLabel(d.provider)} {d.type} · {d.region}</option>)}
            </select>
          ) : (
            <span className="ml-auto text-xs text-slate-400">
              {discovered.length ? 'All discovered resources are added.' : 'Connect AWS, Azure or Google Cloud under Settings to add databases and instances.'}
            </span>
          )}
        </div>
      )}
    </div>
  )
}
