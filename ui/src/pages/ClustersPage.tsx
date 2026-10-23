import { useState } from 'react'
import { AlertTriangle, ExternalLink, Loader2 } from 'lucide-react'
import { errorMessage, getJSON } from '../lib/api'
import { usedShare, type ClustersResponse } from '../lib/clusters'
import { percent } from '../lib/finops'
import { formatMoney } from '../lib/format'
import { followLink } from '../lib/router'
import { useAuth } from '../lib/auth'
import { usePolling } from '../lib/usePolling'
import { Badge, Card, PageHeader } from '../components/ui'

// ClustersPage shows this installation next to the other Cost Deck installations it
// reads (Settings → Clusters): what each cluster costs, saves and could save.
export default function ClustersPage() {
  const { can } = useAuth()
  const [data, setData] = useState<ClustersResponse | null>(null)
  const [error, setError] = useState<string | null>(null)
  usePolling(() => {
    getJSON<ClustersResponse>('/api/clusters').then(d => { setData(d); setError(null) }).catch(e => setError(errorMessage(e)))
  }, 60000)

  const alone = data && data.clusters.length === 1
  return (
    <div className="p-8 max-w-[1600px] mx-auto">
      <PageHeader title="Clusters" subtitle="Every Cost Deck installation side by side: what each cluster costs, saves and could still save" />
      {error && <p className="text-sm text-rose-600 mb-4">{error}</p>}
      {!data ? <div className="flex items-center gap-2 text-sm text-slate-400"><Loader2 size={14} className="animate-spin" /> Loading…</div> : (
        <>
          {data.totals.length > 0 && !alone && (
            <div className="grid gap-3 sm:grid-cols-2 xl:grid-cols-4 mb-6">
              {data.totals.map(t => (
                <Card key={t.currency} className="p-4">
                  <div className="text-xs font-semibold uppercase tracking-wider text-slate-400">{t.clusters} clusters, {t.nodes} nodes</div>
                  <div className="text-2xl font-bold text-slate-900 mt-1">{formatMoney(t.monthlyCost, t.currency)}<span className="text-sm font-medium text-slate-400">/mo</span></div>
                  <div className="text-xs text-slate-500 mt-1">Schedules save {formatMoney(t.schedulesSaving, t.currency)}/mo · {formatMoney(t.savingsAvailable, t.currency)}/mo more possible</div>
                </Card>
              ))}
            </div>
          )}
          <Card className="overflow-x-auto">
            <table className="w-full text-sm">
              <thead>
                <tr className="text-left text-[11px] uppercase tracking-wider text-slate-400 border-b border-slate-100">
                  <th className="px-4 py-3">Cluster</th><th className="px-3 text-right">Nodes</th><th className="px-3 text-right">Cost/mo</th><th className="px-3 text-right">Used</th>
                  <th className="px-3 text-right">Saved by schedules</th><th className="px-3 text-right">Could save</th><th className="px-3 text-right">This month</th><th className="px-3 text-right">Schedules</th><th className="px-4" />
                </tr>
              </thead>
              <tbody className="tabular-nums">
                {data.clusters.map(c => {
                  const s = c.summary
                  const money = (v: number) => formatMoney(v, s?.currency || 'USD')
                  return (
                    <tr key={c.name} className="border-t border-slate-100">
                      <td className="px-4 py-3">
                        <span className="font-semibold text-slate-800">{c.name}</span>
                        {c.local && <span className="ml-2"><Badge tone="brand">this one</Badge></span>}
                        {c.error && <div className="mt-1 flex items-start gap-1.5 text-xs text-rose-600"><AlertTriangle size={13} className="mt-0.5 shrink-0" />{c.error}</div>}
                      </td>
                      {s ? (
                        <>
                          <td className="px-3 text-right">{s.nodes}</td>
                          <td className="px-3 text-right font-semibold text-slate-800">{money(s.monthlyCost)}</td>
                          <td className="px-3 text-right">{percent(usedShare(s))}</td>
                          <td className="px-3 text-right text-emerald-700">{money(s.schedulesSaving)}</td>
                          <td className="px-3 text-right text-brand-700">{money(s.savingsAvailable)}</td>
                          <td className="px-3 text-right">{money(s.monthToDate)}<div className="text-[11px] text-slate-400">heading for {money(s.forecast)}</div></td>
                          <td className="px-3 text-right">{s.schedules ? `${s.schedules - s.schedulesDown} up, ${s.schedulesDown} down` : '—'}</td>
                        </>
                      ) : <td className="px-3 text-slate-400" colSpan={7}>—</td>}
                      <td className="px-4 text-right">
                        {c.url && <a href={c.url} target="_blank" rel="noopener noreferrer" className="inline-flex items-center gap-1 text-xs font-semibold text-brand-700 hover:underline">Open <ExternalLink size={12} /></a>}
                      </td>
                    </tr>
                  )
                })}
              </tbody>
            </table>
          </Card>
          {alone && (
            <p className="mt-4 text-sm text-slate-500">
              Only this cluster so far. Add other Cost Deck installations, each with a viewer API token of that installation,
              {can('admin') ? <> under <a href="/settings/clusters" onClick={e => followLink(e, '/settings/clusters')} className="font-semibold underline">Settings → Clusters</a>.</> : ' under Settings → Clusters (admins).'}
            </p>
          )}
        </>
      )}
    </div>
  )
}
