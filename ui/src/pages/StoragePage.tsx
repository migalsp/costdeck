import { useMemo, useState } from 'react'
import { AlertTriangle, HardDrive, Loader2, Network } from 'lucide-react'
import { Badge, PageHeader, Tabs, type Tone } from '../components/ui'
import { KpiTile } from '../components/finops/charts'
import { fetchInfra, type InfraResponse, type LoadBalancer, type Volume, type VolumeState } from '../lib/finops'
import { formatMoney } from '../lib/format'
import { errorMessage } from '../lib/api'
import { usePolling } from '../lib/usePolling'

const volumeState: Record<VolumeState, { label: string; tone: Tone; hint: string }> = {
  'in-use': { label: 'In use', tone: 'neutral', hint: 'Mounted by a running pod.' },
  unused: { label: 'Unused', tone: 'warning', hint: 'Bound, but no pod mounts it. Still billed.' },
  'scaled-down': { label: 'Kept while down', tone: 'info', hint: 'Its namespace is scaled down by a schedule; the volume waits for it, as expected.' },
  orphaned: { label: 'Orphaned', tone: 'warning', hint: 'Released by its claim, but the disk is kept and billed.' },
}

const lbState: Record<LoadBalancer['state'], { label: string; tone: Tone; hint: string }> = {
  serving: { label: 'Serving', tone: 'neutral', hint: 'Has ready endpoints.' },
  idle: { label: 'Nothing behind it', tone: 'warning', hint: 'No ready endpoint, still billed.' },
  'scaled-down': { label: 'Billed while down', tone: 'info', hint: 'Its namespace is scaled down by a schedule, but the load balancer keeps costing.' },
}

type Filter = 'all' | VolumeState

const fmtSize = (g: number) => (g >= 1024 ? `${(g / 1024).toFixed(1)} TiB` : `${g >= 10 ? g.toFixed(0) : g.toFixed(1)} GiB`)

export default function StoragePage() {
  const [data, setData] = useState<InfraResponse | null>(null)
  const [error, setError] = useState<string | null>(null)
  const [tab, setTab] = useState<'volumes' | 'lbs'>('volumes')
  const [filter, setFilter] = useState<Filter>('all')

  usePolling(() => {
    fetchInfra().then(d => { setData(d); setError(null) }).catch(e => setError(errorMessage(e)))
  }, 60000)

  const volumes = useMemo(() => (data?.infra.volumes || []).filter(v => filter === 'all' || v.state === filter), [data, filter])

  if (!data) {
    return (
      <div className="p-8 max-w-[1600px] mx-auto">
        <PageHeader title="Storage & Network" subtitle="Persistent volumes and load balancers, billed apart from the nodes" />
        {error ? <p className="text-sm text-rose-600">Could not load storage and network: {error}</p>
          : <div className="flex items-center gap-2 text-sm text-slate-400"><Loader2 size={16} className="animate-spin" /> Loading…</div>}
      </div>
    )
  }

  const inf = data.infra
  const cur = data.currency
  const count = (s: VolumeState) => inf.volumes.filter(v => v.state === s).length
  const unusedCount = count('unused') + count('orphaned')
  const totalSize = inf.volumes.reduce((a, v) => a + v.sizeGiB, 0)
  const idleLBs = inf.loadBalancers.filter(l => l.state !== 'serving').length

  return (
    <div className="p-8 max-w-[1600px] mx-auto">
      <PageHeader title="Storage & Network" subtitle="Persistent volumes and load balancers, billed apart from the nodes" />

      {data.error && (
        <div className="mb-4 flex gap-2 p-3 rounded-xl border border-amber-200 bg-amber-50 text-sm text-amber-900">
          <AlertTriangle size={16} className="mt-0.5 shrink-0" />
          <span>Volumes and load balancers could not be read: {data.error}. Upgrade the chart so the operator may list them.</span>
        </div>
      )}

      <div className="grid gap-4 sm:grid-cols-2 xl:grid-cols-4">
        <KpiTile label="Storage" value={<>{formatMoney(inf.storageMonthly, cur)}<span className="text-sm font-medium text-slate-400">/mo</span></>}
          sub={`${inf.volumes.length} volume${inf.volumes.length === 1 ? '' : 's'}, ${fmtSize(totalSize)}`} />
        <KpiTile label="Unused volumes" value={<>{formatMoney(inf.unusedStorageMonthly, cur)}<span className="text-sm font-medium text-slate-400">/mo</span></>}
          status={unusedCount ? { label: `${unusedCount} to review`, tone: 'warning' } : undefined}
          sub="Mounted by no pod, or released but kept" />
        <KpiTile label="Load balancers" value={<>{formatMoney(inf.networkMonthly, cur)}<span className="text-sm font-medium text-slate-400">/mo</span></>}
          sub={`${inf.loadBalancers.length} LoadBalancer Service${inf.loadBalancers.length === 1 ? '' : 's'}`} />
        <KpiTile label="Idle load balancers" value={<>{formatMoney(inf.idleNetworkMonthly, cur)}<span className="text-sm font-medium text-slate-400">/mo</span></>}
          status={idleLBs ? { label: `${idleLBs} to review`, tone: 'warning' } : undefined}
          sub="Nothing behind them, or their namespace is scaled down" />
      </div>

      <div className="flex flex-wrap items-center gap-3 mt-8 mb-4">
        <Tabs value={tab} onChange={setTab} tabs={[
          { id: 'volumes', label: <span className="inline-flex items-center gap-1.5"><HardDrive size={14} /> Volumes</span> },
          { id: 'lbs', label: <span className="inline-flex items-center gap-1.5"><Network size={14} /> Load balancers</span> },
        ]} />
        {tab === 'volumes' && (
          <div className="ml-auto flex flex-wrap gap-1">
            {(['all', 'unused', 'orphaned', 'scaled-down', 'in-use'] as Filter[]).map(f => (
              <button key={f} onClick={() => setFilter(f)}
                className={`px-2.5 py-1 rounded-lg text-xs font-semibold border ${filter === f ? 'bg-slate-900 text-white border-slate-900' : 'bg-white text-slate-600 border-slate-200 hover:border-slate-300'}`}>
                {f === 'all' ? `All ${inf.volumes.length}` : `${volumeState[f].label} ${count(f)}`}
              </button>
            ))}
          </div>
        )}
      </div>

      {tab === 'volumes' ? (
        <div className="rounded-xl border border-slate-200 bg-white shadow-sm overflow-x-auto">
          <table className="w-full text-sm">
            <thead className="bg-slate-50 text-xs text-slate-500">
              <tr>
                {['Volume', 'Class · disk', 'Size', 'Cost/mo', 'Mounted by', 'State'].map(h => (
                  <th key={h} className={`px-3 py-2.5 font-semibold whitespace-nowrap ${h === 'Size' || h === 'Cost/mo' ? 'text-right' : 'text-left'}`}>{h}</th>
                ))}
              </tr>
            </thead>
            <tbody>
              {volumes.length === 0 ? (
                <tr><td colSpan={6} className="px-3 py-8 text-center text-slate-400">No volume in this state.</td></tr>
              ) : volumes.map((v: Volume) => (
                <tr key={(v.namespace || '') + (v.claim || v.volume)} className="border-t border-slate-100 align-top">
                  <td className="px-3 py-2.5">
                    <div className="font-medium text-slate-800">{v.claim || v.volume}</div>
                    <div className="text-[11px] text-slate-500">{v.namespace || 'no claim'}{v.claim && v.volume ? ` · ${v.volume}` : ''}</div>
                  </td>
                  <td className="px-3 py-2.5 text-slate-600" title={v.basis}>{v.storageClass || '—'}<div className="text-[11px] text-slate-400">{v.local ? 'local, part of the node' : v.diskType}</div></td>
                  <td className="px-3 py-2.5 text-right tabular-nums text-slate-700">{fmtSize(v.sizeGiB)}</td>
                  <td className="px-3 py-2.5 text-right tabular-nums font-semibold text-slate-800" title={v.basis}>{v.monthlyCost > 0 ? formatMoney(v.monthlyCost, cur) : '—'}</td>
                  <td className="px-3 py-2.5 text-xs text-slate-600 max-w-[16rem]">{v.mountedBy.length ? v.mountedBy.slice(0, 3).join(', ') + (v.mountedBy.length > 3 ? ` +${v.mountedBy.length - 3}` : '') : <span className="text-slate-400">—</span>}</td>
                  <td className="px-3 py-2.5"><span title={volumeState[v.state].hint}><Badge tone={volumeState[v.state].tone}>{volumeState[v.state].label}</Badge></span></td>
                </tr>
              ))}
            </tbody>
          </table>
        </div>
      ) : (
        <div className="rounded-xl border border-slate-200 bg-white shadow-sm overflow-x-auto">
          <table className="w-full text-sm">
            <thead className="bg-slate-50 text-xs text-slate-500">
              <tr>
                {['Service', 'Address', 'Ports', 'Ready endpoints', 'Cost/mo', 'State'].map(h => (
                  <th key={h} className={`px-3 py-2.5 font-semibold whitespace-nowrap ${h === 'Ready endpoints' || h === 'Cost/mo' ? 'text-right' : 'text-left'}`}>{h}</th>
                ))}
              </tr>
            </thead>
            <tbody>
              {inf.loadBalancers.length === 0 ? (
                <tr><td colSpan={6} className="px-3 py-8 text-center text-slate-400">No Service of type LoadBalancer.</td></tr>
              ) : inf.loadBalancers.map(lb => (
                <tr key={`${lb.namespace}/${lb.name}`} className="border-t border-slate-100">
                  <td className="px-3 py-2.5"><div className="font-medium text-slate-800">{lb.name}</div><div className="text-[11px] text-slate-500">{lb.namespace}</div></td>
                  <td className="px-3 py-2.5 text-xs font-mono text-slate-600 break-all">{lb.address || 'pending'}</td>
                  <td className="px-3 py-2.5 text-xs text-slate-600">{lb.ports.join(', ') || '—'}</td>
                  <td className="px-3 py-2.5 text-right tabular-nums text-slate-700">{lb.readyEndpoints}</td>
                  <td className="px-3 py-2.5 text-right tabular-nums font-semibold text-slate-800" title={lb.basis}>{lb.monthlyCost > 0 ? formatMoney(lb.monthlyCost, cur) : '—'}</td>
                  <td className="px-3 py-2.5"><span title={lbState[lb.state].hint}><Badge tone={lbState[lb.state].tone}>{lbState[lb.state].label}</Badge></span></td>
                </tr>
              ))}
            </tbody>
          </table>
        </div>
      )}
      <p className="mt-3 text-xs text-slate-400">
        Volumes are priced per storage class at the cloud's list price for their disk type; without a cloud price list, network volumes are estimated like a general-purpose cloud SSD. Your own storage rate under Settings → Prices replaces both.
        Load balancers: {inf.networkBasis}. Traffic and data transfer are not included.
      </p>
    </div>
  )
}
