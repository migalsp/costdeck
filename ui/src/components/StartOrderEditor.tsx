import { useMemo, useState, type DragEvent } from 'react'
import { ArrowDown, ArrowUp, Cloud, GripVertical } from 'lucide-react'
import type { ExternalTarget } from '../lib/types'
import { Button } from './ui'

const EXT = 'ext:'
const UNPLACED = -1

interface Props {
  namespaces: string[]
  sequence?: string[]
  externalTargets?: ExternalTarget[]
  // Cloud resources that can still be added (discovered, not yet in this schedule).
  discovered: ExternalTarget[]
  readOnly: boolean
  onSave: (change: { sequence?: string[]; externalTargets?: ExternalTarget[] }) => Promise<void>
}

const label = (item: string) => (item.startsWith(EXT) ? item.slice(EXT.length) : item)

// StartOrderEditor arranges a schedule's namespaces and cloud resources into stages:
// stages start top to bottom and stop bottom to top. Drag items between stages, or use
// each item's menu.
export default function StartOrderEditor({ namespaces, sequence, externalTargets, discovered, readOnly, onSave }: Props) {
  const initial = useMemo(() => {
    const valid = new Set([...namespaces, ...(externalTargets || []).map(t => EXT + t.identifier)])
    return (sequence || []).map(s => s.split(/\s+/).filter(i => valid.has(i))).filter(s => s.length > 0)
  }, [namespaces, sequence, externalTargets])

  const [stages, setStages] = useState<string[][]>(initial)
  const [targets, setTargets] = useState<ExternalTarget[]>(externalTargets || [])
  const [dragging, setDragging] = useState<string | null>(null)
  const [over, setOver] = useState<number | 'new' | null>(null)
  const [saving, setSaving] = useState(false)
  const [error, setError] = useState<string | null>(null)

  const placed = new Set(stages.flat())
  const unplaced = namespaces.filter(ns => !placed.has(ns))
  const dirty = JSON.stringify(stages) !== JSON.stringify(initial) || JSON.stringify(targets) !== JSON.stringify(externalTargets || [])
  const addable = discovered.filter(d => !targets.some(t => t.identifier === d.identifier))

  // move puts an item into a stage (index), a new last stage ('new') or back to unplaced.
  const move = (item: string, to: number | 'new') => {
    setStages(prev => {
      const without = prev.map(s => s.filter(i => i !== item))
      if (to === 'new') return [...without, [item]].filter(s => s.length > 0)
      if (to === UNPLACED) return without.filter(s => s.length > 0)
      // Keep the target stage index stable while removing empties around it.
      const next = without.map((s, i) => (i === to ? [...s, item] : s))
      return next.filter(s => s.length > 0)
    })
  }

  const swap = (i: number, j: number) => setStages(prev => {
    const next = [...prev]
    ;[next[i], next[j]] = [next[j], next[i]]
    return next
  })

  const removeTarget = (id: string) => {
    setTargets(prev => prev.filter(t => t.identifier !== id))
    setStages(prev => prev.map(s => s.filter(i => i !== EXT + id)).filter(s => s.length > 0))
  }

  const addTarget = (id: string) => {
    const found = discovered.find(d => d.identifier === id)
    if (!found) return
    setTargets(prev => [...prev, { provider: found.provider || 'aws', type: found.type, identifier: found.identifier, region: found.region, name: found.name }])
    // Cloud dependencies usually start first.
    setStages(prev => (prev.length ? [[EXT + id, ...prev[0]], ...prev.slice(1)] : [[EXT + id]]))
  }

  const save = async () => {
    setSaving(true)
    setError(null)
    try {
      await onSave({ sequence: stages.length ? stages.map(s => s.join(' ')) : undefined, externalTargets: targets.length ? targets : undefined })
    } catch (err) {
      setError(err instanceof Error ? err.message : String(err))
    } finally {
      setSaving(false)
    }
  }

  const dropProps = (zone: number | 'new') => readOnly ? {} : {
    onDragOver: (e: DragEvent) => { e.preventDefault(); setOver(zone) },
    onDragLeave: () => setOver(o => (o === zone ? null : o)),
    onDrop: (e: DragEvent) => {
      e.preventDefault()
      const item = e.dataTransfer.getData('text/plain')
      if (item) move(item, zone)
      setOver(null)
      setDragging(null)
    },
  }

  const stageCount = stages.length

  const renderItem = (item: string, stage: number) => {
    const ext = item.startsWith(EXT)
    return (
      <span
        key={item}
        draggable={!readOnly}
        onDragStart={e => { e.dataTransfer.setData('text/plain', item); setDragging(item) }}
        onDragEnd={() => { setDragging(null); setOver(null) }}
        className={`group inline-flex items-center gap-1 pl-1.5 pr-1 py-1 rounded-md border text-xs font-medium bg-white ${
          dragging === item ? 'opacity-40' : ''} ${ext ? 'border-amber-200 text-amber-800' : 'border-slate-200 text-slate-700'} ${readOnly ? '' : 'cursor-grab'}`}
      >
        {!readOnly && <GripVertical size={12} className="text-slate-300" />}
        {ext && <Cloud size={12} className="text-amber-500" />}
        <span className="max-w-[14rem] truncate" title={label(item)}>{label(item)}</span>
        {!readOnly && (
          <span className="relative ml-0.5">
            <span className="px-1 text-slate-400 group-hover:text-slate-600">⋯</span>
            {/* A native select keeps moving keyboard-accessible without a custom menu. */}
            <select
              aria-label={`Move ${label(item)}`}
              className="absolute inset-0 opacity-0 cursor-pointer"
              value=""
              onChange={e => {
                const v = e.target.value
                if (v === 'remove') removeTarget(label(item))
                else move(item, v === 'new' ? 'new' : Number(v))
              }}
            >
              <option value="" disabled>Move to…</option>
              {Array.from({ length: stageCount }, (_, i) => i).filter(i => i !== stage).map(i => (
                <option key={i} value={i}>Stage {i + 1}</option>
              ))}
              <option value="new">New last stage</option>
              {!ext && stage !== UNPLACED && <option value={UNPLACED}>Not placed (starts last)</option>}
              {ext && <option value="remove">Remove from schedule</option>}
            </select>
          </span>
        )}
      </span>
    )
  }

  return (
    <div className="space-y-3">
      <p className="text-sm text-slate-500">
        Stages start from the top and stop from the bottom. Everything in a stage starts together and must be ready before the next stage starts.
      </p>

      {stages.map((stage, i) => (
        <div key={i} {...dropProps(i)}
          className={`rounded-xl border p-3 transition-colors ${over === i ? 'border-brand-400 bg-brand-50/50' : 'border-slate-200 bg-slate-50/60'}`}>
          <div className="flex items-center gap-2 mb-2">
            <span className="w-6 h-6 rounded-full bg-white border border-slate-200 text-xs font-bold text-slate-600 flex items-center justify-center">{i + 1}</span>
            <span className="text-sm font-semibold text-slate-700">Stage {i + 1}</span>
            <span className="text-xs text-slate-400">{i === 0 ? 'starts first, stops last' : i === stageCount - 1 && unplaced.length === 0 ? 'starts last, stops first' : ''}</span>
            {!readOnly && (
              <span className="ml-auto flex items-center gap-0.5">
                <button disabled={i === 0} onClick={() => swap(i, i - 1)} className="p-1 rounded text-slate-400 hover:bg-white hover:text-slate-700 disabled:opacity-30" title="Move stage up"><ArrowUp size={14} /></button>
                <button disabled={i === stageCount - 1} onClick={() => swap(i, i + 1)} className="p-1 rounded text-slate-400 hover:bg-white hover:text-slate-700 disabled:opacity-30" title="Move stage down"><ArrowDown size={14} /></button>
              </span>
            )}
          </div>
          <div className="flex flex-wrap gap-1.5">
            {stage.map(item => renderItem(item, i))}
          </div>
        </div>
      ))}

      {!readOnly && (
        <div {...dropProps('new')}
          className={`rounded-xl border-2 border-dashed px-3 py-3 text-xs text-center transition-colors ${over === 'new' ? 'border-brand-400 bg-brand-50/50 text-brand-700' : 'border-slate-200 text-slate-400'}`}>
          Drop here to start a new last stage
        </div>
      )}

      <div {...dropProps(UNPLACED)}
        className={`rounded-xl border p-3 transition-colors ${over === UNPLACED ? 'border-brand-400 bg-brand-50/50' : 'border-slate-200'}`}>
        <div className="text-sm font-semibold text-slate-700 mb-1">Not placed</div>
        <div className="text-xs text-slate-400 mb-2">Start after every stage and stop first.</div>
        <div className="flex flex-wrap gap-1.5">
          {unplaced.length === 0 ? <span className="text-xs text-slate-400 italic">Every namespace has a stage.</span>
            : unplaced.map(ns => renderItem(ns, UNPLACED))}
        </div>
      </div>

      {!readOnly && (
        <div className="flex flex-wrap items-center gap-2 pt-1">
          <Cloud size={16} className="text-amber-500" />
          <span className="text-sm font-semibold text-slate-700">Cloud resources</span>
          {addable.length > 0 ? (
            <select value="" onChange={e => addTarget(e.target.value)}
              className="ml-auto bg-white border border-slate-200 rounded-lg px-2.5 py-1.5 text-sm text-slate-700 outline-none focus:border-brand-500">
              <option value="" disabled>Add a database or instance…</option>
              {addable.map(d => <option key={d.identifier} value={d.identifier}>{d.name || d.identifier} · {d.type} · {d.region}</option>)}
            </select>
          ) : (
            <span className="ml-auto text-xs text-slate-400">{discovered.length ? 'All discovered resources are added.' : 'Connect AWS under Settings to add Aurora clusters or EC2 instances.'}</span>
          )}
        </div>
      )}

      {!readOnly && (dirty || error) && (
        <div className="sticky bottom-0 flex items-center gap-2 pt-3">
          <span className="flex-1 text-xs font-semibold text-rose-600">{error}</span>
          <Button variant="ghost" onClick={() => { setStages(initial); setTargets(externalTargets || []) }} disabled={saving}>Reset</Button>
          <Button variant="primary" onClick={save} disabled={saving || !dirty}>{saving ? 'Saving…' : 'Save order'}</Button>
        </div>
      )}
      {readOnly && <p className="text-xs text-slate-400">Only admins can change the order.</p>}
    </div>
  )
}
