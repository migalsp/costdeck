import { Plus, X } from 'lucide-react'
import { llmModelProblem } from '../../lib/llm'
import type { LLMModelPrice } from '../../lib/types'
import { Button } from '../ui'

const input = 'w-full px-3 py-2 bg-white border border-slate-200 rounded-lg text-sm font-mono'

// ModelPricesEditor edits the price per million tokens of the LLM models applications
// report through OpenTelemetry.
export default function ModelPricesEditor({ models, onChange }: { models: LLMModelPrice[]; onChange: (m: LLMModelPrice[]) => void }) {
  const set = (i: number, patch: Partial<LLMModelPrice>) => onChange(models.map((m, j) => (j === i ? { ...m, ...patch } : m)))
  const problem = llmModelProblem(models)
  return (
    <div data-testid="model-prices">
      {models.length > 0 && (
        <div className="grid grid-cols-[1fr_8rem_8rem_2rem] gap-2 mb-1 text-[10px] font-bold uppercase tracking-wider text-slate-500">
          <span>Model or glob</span><span>Input / 1M</span><span>Output / 1M</span><span />
        </div>
      )}
      <div className="space-y-2">
        {models.map((m, i) => (
          <div key={i} className="grid grid-cols-[1fr_8rem_8rem_2rem] gap-2 items-center">
            <input value={m.model} onChange={e => set(i, { model: e.target.value })} placeholder="gpt-4o or claude-sonnet-*" aria-label="Model" className={input} />
            <input value={m.inputPer1M} onChange={e => set(i, { inputPer1M: e.target.value })} placeholder="2.50" aria-label="Input price per million tokens" className={input} />
            <input value={m.outputPer1M} onChange={e => set(i, { outputPer1M: e.target.value })} placeholder="10" aria-label="Output price per million tokens" className={input} />
            <button type="button" onClick={() => onChange(models.filter((_, j) => j !== i))} className="p-1 rounded text-slate-400 hover:text-rose-600" title="Remove price"><X size={14} /></button>
          </div>
        ))}
      </div>
      {problem && models.length > 0 && <p className="mt-2 text-xs font-medium text-rose-600">{problem}</p>}
      <Button size="sm" variant="secondary" icon={<Plus size={13} />} className="mt-3" onClick={() => onChange([...models, { model: '', inputPer1M: '', outputPer1M: '' }])}>Add a model price</Button>
    </div>
  )
}
