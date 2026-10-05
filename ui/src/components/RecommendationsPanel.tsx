import { useState } from 'react'
import { Lightbulb, ArrowRight, AlertTriangle, Copy, Check, Info } from 'lucide-react'
import { fetchRecommendations } from '../lib/api'
import { formatMoney } from '../lib/format'
import type { AdviceAction, ContainerAdvice, Recommendations, ResourceAdvice, WorkloadAdvice } from '../lib/types'
import { usePolling } from '../lib/usePolling'

const actionable: AdviceAction[] = ['reduce', 'increase', 'set']

const needsAttention = (c: ContainerAdvice) => actionable.includes(c.cpu.action) || actionable.includes(c.memory.action)

// The values to paste: recommended where there is advice, the current request otherwise.
const yamlFor = (w: WorkloadAdvice, c: ContainerAdvice) => {
  const value = (r: ResourceAdvice) => (actionable.includes(r.action) ? r.recommended : r.request) || ''
  const lines = [`# ${w.kind.toLowerCase()}/${w.name}, container ${c.name}`, 'resources:', '  requests:']
  if (value(c.cpu)) lines.push(`    cpu: ${value(c.cpu)}`)
  if (value(c.memory)) lines.push(`    memory: ${value(c.memory)}`)
  return lines.join('\n')
}

function Change({ advice }: { advice: ResourceAdvice }) {
  if (advice.action === 'unknown') return <span className="text-slate-300">no data</span>
  if (advice.action === 'keep') return <span className="text-slate-400">{advice.request} · fits</span>
  const tone = advice.action === 'increase' ? 'text-amber-600' : 'text-emerald-600'
  return (
    <span className="inline-flex items-center gap-1.5 whitespace-nowrap">
      <span className={advice.request ? 'text-slate-400 line-through' : 'text-slate-400 italic'}>{advice.request || 'unset'}</span>
      <ArrowRight size={12} className="text-slate-300" />
      <span className={`font-bold ${tone}`}>{advice.recommended}</span>
      <span className="text-[10px] text-slate-400">uses {advice.observed}</span>
    </span>
  )
}

function CopyButton({ text }: { text: string }) {
  const [copied, setCopied] = useState(false)
  return (
    <button
      onClick={() => {
        navigator.clipboard?.writeText(text).then(() => {
          setCopied(true)
          setTimeout(() => setCopied(false), 1500)
        })
      }}
      className="p-1.5 rounded-lg text-slate-400 hover:text-indigo-600 hover:bg-indigo-50 transition-colors"
      title={`Copy as YAML:\n\n${text}`}
    >
      {copied ? <Check size={14} className="text-emerald-500" /> : <Copy size={14} />}
    </button>
  )
}

// RecommendationsPanel shows what a namespace could give back, from observed demand. It is
// advice only: CostDeck never edits requests, the team applies them through its own pipeline.
export default function RecommendationsPanel({ namespace }: { namespace: string }) {
  const [report, setReport] = useState<Recommendations | null>(null)
  const [unavailable, setUnavailable] = useState(false)

  usePolling(() => {
    fetchRecommendations(namespace)
      .then(r => { setReport(r); setUnavailable(r === null) })
      .catch(() => setUnavailable(true))
  }, 5 * 60 * 1000, namespace)

  if (unavailable) {
    return (
      <div className="bg-white rounded-xl border border-slate-200 p-5 mb-6 flex items-center gap-3 text-sm text-slate-500">
        <Info size={16} className="text-slate-400" />
        Right-sizing advice appears once usage data is available for this namespace (metrics-server or VictoriaMetrics).
      </div>
    )
  }
  if (!report) return <div className="h-24 bg-slate-100 rounded-xl animate-pulse mb-6" />

  const rows = report.workloads.flatMap(w => w.containers.filter(needsAttention).map(c => ({ w, c })))
  const reducible = rows.filter(({ c }) => c.cpu.action === 'reduce' || c.memory.action === 'reduce').length
  const underRequested = rows.filter(({ c }) => c.cpu.action === 'increase' || c.memory.action === 'increase').length

  return (
    <div className="bg-white rounded-xl border border-slate-200 shadow-sm mb-6 overflow-hidden">
      <div className="p-5 flex flex-wrap items-start justify-between gap-4 border-b border-slate-100">
        <div className="flex items-start gap-3">
          <div className="p-2 rounded-lg bg-emerald-50 text-emerald-600"><Lightbulb size={20} /></div>
          <div>
            <h3 className="font-bold text-slate-800">What can be reduced</h3>
            <p className="text-xs text-slate-500 mt-0.5 max-w-3xl">{report.basis} Nothing is changed automatically: apply the values through your deployment pipeline.</p>
            {!report.historical && (
              <p className="text-xs text-amber-600 mt-1">A single reading can miss peaks. Treat these values as a starting point.</p>
            )}
            {report.warning && <p className="text-xs text-rose-600 mt-1">{report.warning}</p>}
          </div>
        </div>
        <div className="text-right">
          <div className="text-2xl font-black text-emerald-600">
            {report.monthlySavings >= 0.01 ? `~${formatMoney(report.monthlySavings, report.currency)}` : formatMoney(0, report.currency)}
            <span className="text-sm font-bold text-slate-400">/mo</span>
          </div>
          <div className="text-[11px] text-slate-400">
            {reducible} container{reducible === 1 ? '' : 's'} over-requested
            {underRequested > 0 && <> · <span className="text-amber-600">{underRequested} under-requested</span></>}
          </div>
        </div>
      </div>

      {report.workloads.length === 0 ? (
        <div className="p-5 text-sm text-slate-500">No Deployments or StatefulSets here, so there is nothing to right-size.</div>
      ) : rows.length === 0 ? (
        <div className="p-5 text-sm text-slate-500">Requests match observed usage. Nothing to reduce right now.</div>
      ) : (
        <div className="overflow-x-auto">
          <table className="w-full text-left text-sm">
            <thead className="bg-slate-50 text-[11px] uppercase tracking-wider text-slate-400">
              <tr>
                <th className="px-5 py-2 font-bold">Workload</th>
                <th className="px-3 py-2 font-bold">CPU request</th>
                <th className="px-3 py-2 font-bold">Memory request</th>
                <th className="px-3 py-2 font-bold text-right">Saving</th>
                <th className="px-3 py-2" />
              </tr>
            </thead>
            <tbody className="divide-y divide-slate-100">
              {rows.map(({ w, c }) => (
                <tr key={`${w.kind}/${w.name}/${c.name}`} className="hover:bg-slate-50/60">
                  <td className="px-5 py-2.5">
                    <div className="font-bold text-slate-700">{w.name}{w.containers.length > 1 && <span className="font-medium text-slate-400"> / {c.name}</span>}</div>
                    <div className="text-[10px] text-slate-400 uppercase tracking-wider">
                      {w.kind} · {w.replicas} replica{w.replicas === 1 ? '' : 's'}
                      {(c.cpu.action === 'increase' || c.memory.action === 'increase') && (
                        <span className="ml-2 inline-flex items-center gap-1 text-amber-600 normal-case tracking-normal" title="It uses more than it requests, so it competes for capacity it never reserved: CPU throttling, or eviction under memory pressure.">
                          <AlertTriangle size={10} /> uses more than requested
                        </span>
                      )}
                    </div>
                  </td>
                  <td className="px-3 py-2.5"><Change advice={c.cpu} /></td>
                  <td className="px-3 py-2.5"><Change advice={c.memory} /></td>
                  <td className="px-3 py-2.5 text-right font-bold text-emerald-600 whitespace-nowrap">
                    {w.monthlySavings >= 0.01 && w.containers.indexOf(c) === w.containers.findIndex(needsAttention)
                      ? `${formatMoney(w.monthlySavings, report.currency)}/mo` : ''}
                  </td>
                  <td className="px-3 py-2.5 text-right"><CopyButton text={yamlFor(w, c)} /></td>
                </tr>
              ))}
            </tbody>
          </table>
        </div>
      )}
    </div>
  )
}
