import { useState } from 'react'
import { Download, Loader2 } from 'lucide-react'
import { apiError, errorMessage, getJSON } from '../../lib/api'
import { allocationPeriods, allocationQuery, policySummary, type AllocationGroup, type AllocationPeriod, type AllocationReport } from '../../lib/allocation'
import { downloadText } from '../../lib/finops'
import { formatMoney } from '../../lib/format'
import { followLink } from '../../lib/router'
import { toast } from '../../lib/toast'
import { usePolling } from '../../lib/usePolling'
import { Button, Card, Tabs } from '../ui'

const groups: { id: AllocationGroup; label: string }[] = [
  { id: 'team', label: 'Team' },
  { id: 'environment', label: 'Environment' },
  { id: 'namespace', label: 'Namespace' },
  { id: 'label', label: 'Label' },
]

// AllocationCard is showback and chargeback: a period's cost charged to groups, from the
// cost history, as a table and a CSV file for finance.
export default function AllocationCard({ canAdmin }: { canAdmin: boolean }) {
  const [period, setPeriod] = useState<AllocationPeriod>('this-month')
  const [group, setGroup] = useState<AllocationGroup>('team')
  const [labelKey, setLabelKey] = useState('cost-center')
  const [report, setReport] = useState<AllocationReport | null>(null)
  const [error, setError] = useState<string | null>(null)
  const query = allocationQuery(period, group, labelKey)

  usePolling(() => {
    if (!query) return
    getJSON<AllocationReport>(`/api/finops/allocation?${query}`).then(r => { setReport(r); setError(null) }).catch(e => setError(errorMessage(e)))
  }, 300000, query)

  const download = async () => {
    if (!query || !report) return
    try {
      const res = await fetch(`/api/finops/allocation.csv?${query}`)
      if (!res.ok) throw new Error(await apiError(res))
      downloadText(`cost-allocation-${report.by.replace(':', '-')}-${report.from}-${report.to}.csv`, await res.text(), 'text/csv;charset=utf-8')
    } catch (e) {
      toast.error(`Could not download the CSV: ${errorMessage(e)}`)
    }
  }

  const cur = report?.currency || 'USD'
  const money = (v: number) => formatMoney(v, cur)
  return (
    <Card className="p-5">
      <div className="flex flex-wrap items-center gap-3 mb-4">
        <div className="min-w-0">
          <h2 className="text-base font-semibold text-slate-800">Cost allocation</h2>
          <p className="text-xs text-slate-500">What each group cost from the cost history, with shared and idle cost charged by your policy. The rows add up to the bill.</p>
        </div>
        <div className="ml-auto flex flex-wrap items-center gap-2">
          <Tabs value={period} onChange={setPeriod} tabs={allocationPeriods} />
        </div>
      </div>
      <div className="flex flex-wrap items-center gap-2 mb-4">
        <span className="text-xs font-semibold text-slate-500">By</span>
        <Tabs value={group} onChange={setGroup} tabs={groups} />
        {group === 'label' && (
          <input aria-label="Label key" value={labelKey} onChange={e => setLabelKey(e.target.value)} placeholder="cost-center"
            className="px-2.5 py-1.5 border border-slate-200 rounded-lg text-sm font-mono w-44" />
        )}
        <Button size="sm" className="ml-auto" onClick={download} disabled={!report || !query} icon={<Download size={14} />}>Download CSV</Button>
      </div>

      {error ? <p className="text-sm text-rose-600">{error}</p>
        : !report ? <div className="flex items-center gap-2 text-sm text-slate-400"><Loader2 size={14} className="animate-spin" /> Loading…</div>
        : report.days === 0 ? <p className="text-sm text-slate-500">The cost history has no day in this period yet.</p>
        : (
          <>
            <div className="overflow-x-auto">
              <table className="w-full text-sm">
                <thead>
                  <tr className="text-left text-[11px] uppercase tracking-wider text-slate-400">
                    <th className="py-1.5 pr-3">{group === 'label' ? labelKey : groups.find(g => g.id === group)?.label}</th>
                    <th className="pr-3 text-right">Compute</th><th className="pr-3 text-right">Storage</th><th className="pr-3 text-right">Load balancers</th>
                    <th className="pr-3 text-right">Shared</th><th className="pr-3 text-right">Idle</th><th className="text-right">Total</th>
                  </tr>
                </thead>
                <tbody className="tabular-nums">
                  {report.rows.map(r => (
                    <tr key={r.key} className="border-t border-slate-100">
                      <td className="py-2 pr-3"><div className="font-medium text-slate-800">{r.key}</div>{group !== 'namespace' && <div className="text-[11px] text-slate-400 truncate max-w-[22rem]">{r.namespaces.join(', ')}</div>}</td>
                      <td className="pr-3 text-right">{money(r.compute)}</td><td className="pr-3 text-right">{money(r.storage)}</td><td className="pr-3 text-right">{money(r.network)}</td>
                      <td className="pr-3 text-right">{money(r.shared)}</td><td className="pr-3 text-right">{money(r.idle)}</td>
                      <td className="text-right font-semibold text-slate-800">{money(r.total)}</td>
                    </tr>
                  ))}
                  {report.unallocated > 0 && (
                    <tr className="border-t border-slate-100 text-slate-500">
                      <td className="py-2 pr-3 italic" colSpan={6}>Unallocated capacity (requested by no pod)</td>
                      <td className="text-right font-semibold">{money(report.unallocated)}</td>
                    </tr>
                  )}
                  <tr className="border-t-2 border-slate-200">
                    <td className="py-2 pr-3 font-semibold" colSpan={6}>Total, {report.days} day{report.days === 1 ? '' : 's'} from {report.from} to {report.to}</td>
                    <td className="text-right font-bold text-slate-900">{money(report.total)}</td>
                  </tr>
                </tbody>
              </table>
            </div>
            <p className="mt-3 text-[11px] text-slate-500">
              {policySummary(report)}.{' '}
              {canAdmin && <a href="/settings/allocation" onClick={e => followLink(e, '/settings/allocation')} className="font-semibold underline">Change the policy</a>}
            </p>
          </>
        )}
    </Card>
  )
}
