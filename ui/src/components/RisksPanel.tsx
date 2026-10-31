import { useState } from 'react'
import { Activity, AlertOctagon, AlertTriangle, CheckCircle2 } from 'lucide-react'
import { getJSON } from '../lib/api'
import { formatMoney } from '../lib/format'
import type { Risk, Risks } from '../lib/types'
import { usePolling } from '../lib/usePolling'

const fixCost = (r: Risk, currency: string) => {
  if (r.fixMonthlyCost === undefined) return null
  if (r.fixMonthlyCost < 0.01) return <span className="text-emerald-700">no extra cost</span>
  return <span className="text-slate-600">+{formatMoney(r.fixMonthlyCost, currency)}/mo</span>
}

export default function RisksPanel({ namespace }: { namespace: string }) {
  const [report, setReport] = useState<Risks | null>(null)
  const [failed, setFailed] = useState(false)
  const load = () => getJSON<Risks>(`/api/namespaces/${encodeURIComponent(namespace)}/risks`)
    .then(r => { setReport(r); setFailed(false) })
    .catch(() => setFailed(true))
  usePolling(load, 5 * 60 * 1000, namespace)

  if (failed || !report) return null
  const critical = report.risks.filter(r => r.severity === 'critical').length
  const warnings = report.risks.length - critical

  return (
    <section className="bg-white rounded-xl border border-slate-200 shadow-sm mb-6 overflow-hidden" aria-label="Performance risks">
      <div className="p-5 flex flex-wrap items-start justify-between gap-4 border-b border-slate-100">
        <div className="flex items-start gap-3">
          <div className={`p-2 rounded-lg ${critical > 0 ? 'bg-rose-50 text-rose-600' : 'bg-slate-50 text-slate-500'}`}><Activity size={20} /></div>
          <div>
            <h3 className="font-bold text-slate-800">Performance risks</h3>
            <p className="text-xs text-slate-500 mt-0.5 max-w-3xl">{report.basis}</p>
            {report.warning && <p className="text-xs text-rose-600 mt-1">{report.warning}</p>}
          </div>
        </div>
        {report.risks.length > 0 && (
          <div className="text-right text-[11px]">
            {critical > 0 && <div className="font-bold text-rose-600">{critical} critical</div>}
            {warnings > 0 && <div className="font-bold text-amber-600">{warnings} warning{warnings === 1 ? '' : 's'}</div>}
          </div>
        )}
      </div>
      {report.risks.length === 0 ? (
        <div className="p-5 text-sm text-slate-500 flex items-center gap-2">
          <CheckCircle2 size={16} className="text-emerald-500" />
          No performance risks seen {report.window ? `in the last ${report.window}` : 'right now'}.
        </div>
      ) : (
        <ul className="divide-y divide-slate-100">
          {report.risks.map(r => (
            <li key={`${r.kind}/${r.workload}/${r.container ?? ''}/${r.code}`} className="px-5 py-3 flex gap-3 text-sm">
              {r.severity === 'critical'
                ? <AlertOctagon size={16} className="text-rose-600 shrink-0 mt-0.5" aria-label="Critical" />
                : <AlertTriangle size={16} className="text-amber-600 shrink-0 mt-0.5" aria-label="Warning" />}
              <div className="min-w-0 flex-1 space-y-0.5">
                <div className="font-bold text-slate-700">
                  {r.workload}{r.container && <span className="font-medium text-slate-400"> / {r.container}</span>}
                  <span className="ml-2 text-[10px] uppercase tracking-wider font-semibold text-slate-400">{r.kind}</span>
                </div>
                <p className="text-slate-600">{r.message}</p>
                {r.fix && <p className="text-xs text-slate-500"><span className="font-semibold text-slate-600">Fix:</span> {r.fix}</p>}
              </div>
              <div className="text-xs whitespace-nowrap font-semibold">{fixCost(r, report.currency)}</div>
            </li>
          ))}
        </ul>
      )}
    </section>
  )
}
