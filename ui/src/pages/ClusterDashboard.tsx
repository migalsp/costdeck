import { Fragment, useMemo, useState, type ReactNode } from 'react'
import { AlertTriangle, ArrowRight, Boxes, Cpu, LayoutGrid, Loader2, Minimize2, Rows3, Search, Server, Shapes, Zap } from 'lucide-react'
import { Badge, Card, Modal, PageHeader, SectionTitle, type Tone } from '../components/ui'
import { BarList, KpiTile, SegmentBar, SegmentLegend } from '../components/finops/charts'
import { fetchNodePods, fetchNodes, funnelColors, percent, type NodePod, type NodeView, type NodesReport, type Opportunity } from '../lib/finops'
import { formatMoney } from '../lib/format'
import { errorMessage } from '../lib/api'
import { usePolling } from '../lib/usePolling'

type GroupBy = 'pool' | 'instanceType' | 'zone' | 'capacityType' | 'none'
type View = 'map' | 'table'

const groupLabel: Record<GroupBy, string> = { pool: 'Node pool', instanceType: 'Instance type', zone: 'Zone', capacityType: 'Capacity type', none: 'No grouping' }

const groupOf = (n: NodeView, g: GroupBy) => {
  switch (g) {
    case 'pool': return n.pool || 'No pool label'
    case 'instanceType': return n.instanceType || 'Unknown type'
    case 'zone': return n.zone || 'No zone'
    case 'capacityType': return n.capacityType === 'spot' ? 'Spot' : 'On-demand'
  }
  return ''
}

const findingIcon: Record<string, ReactNode> = {
  consolidation: <Minimize2 size={16} />,
  'node-shape': <Shapes size={16} />,
  'pending-pods': <Boxes size={16} />,
  'node-not-ready': <AlertTriangle size={16} />,
  'node-pressure': <AlertTriangle size={16} />,
  'no-spot': <Zap size={16} />,
  'node-type': <Cpu size={16} />,
}

// packingRating rates bin-packing by the fuller of the two resources: nodes are added for
// whichever runs out first.
function packingRating(cpu: number, mem: number): { label: string; tone: Tone; title: string } {
  const v = Math.max(cpu, mem)
  const title = 'How full the nodes are by requests, by the fuller of CPU and memory. 70% or more is well packed.'
  if (v >= 0.7) return { label: 'Well packed', tone: 'success', title }
  if (v >= 0.4) return { label: 'Fair', tone: 'neutral', title }
  return { label: 'Loose', tone: 'warning', title }
}

function age(iso: string): string {
  const h = (Date.now() - new Date(iso).getTime()) / 3.6e6
  if (h < 1) return `${Math.max(1, Math.round(h * 60))}m`
  if (h < 48) return `${Math.round(h)}h`
  return `${Math.round(h / 24)}d`
}

const fmt = (v: number) => (v >= 100 ? v.toFixed(0) : v >= 10 ? v.toFixed(1) : v.toFixed(2))

const resourceLegend = [
  { label: 'Used', color: funnelColors.used },
  { label: 'Requested, not used', color: funnelColors.idle },
  { label: 'Not requested', color: funnelColors.unallocated },
]

// ResourceBar shows used, requested-but-idle and unrequested on a node's allocatable.
function ResourceBar({ label, unit, r, metrics = true }: { label: string; unit: string; r: NodeView['cpu']; metrics?: boolean }) {
  const used = metrics ? Math.min(r.used, r.requested) : 0
  return (
    <div>
      <div className="flex justify-between text-[11px] mb-1">
        <span className="font-semibold text-slate-600">{label}</span>
        <span className="text-slate-500 tabular-nums">{percent(r.allocatable ? r.requested / r.allocatable : undefined)} requested · {fmt(r.requested)}/{fmt(r.allocatable)} {unit}</span>
      </div>
      <SegmentBar total={r.allocatable} height="h-2" segments={[
        { key: 'u', label: 'Used', value: used, color: funnelColors.used, hint: metrics ? `${fmt(r.used)} ${unit}` : 'no reading' },
        { key: 'i', label: 'Requested, not used', value: Math.max(0, r.requested - used), color: funnelColors.idle, hint: `${fmt(Math.max(0, r.requested - used))} ${unit}` },
        { key: 'n', label: 'Not requested', value: Math.max(0, r.allocatable - r.requested), color: funnelColors.unallocated, hint: `${fmt(Math.max(0, r.allocatable - r.requested))} ${unit}` },
      ]} />
    </div>
  )
}

function StatusBadges({ n }: { n: NodeView }) {
  return (
    <>
      {n.status !== 'Ready' && <Badge tone="danger">{n.status === 'NotReady' ? 'Not ready' : 'Unknown'}</Badge>}
      {n.unschedulable && <Badge tone="warning">Cordoned</Badge>}
      {(n.pressure || []).map(p => <Badge key={p} tone="warning">{p.replace('Pressure', ' pressure')}</Badge>)}
      {n.consolidationCandidate && <Badge tone="brand">Could be emptied</Badge>}
      {n.capacityType === 'spot' && <Badge tone="info">Spot</Badge>}
    </>
  )
}

function NodeTile({ n, currency, onOpen }: { n: NodeView; currency: string; onOpen: () => void }) {
  const border = n.status !== 'Ready' ? 'border-rose-300' : n.consolidationCandidate ? 'border-dashed border-brand-400' : 'border-slate-200'
  return (
    <button onClick={onOpen} className={`text-left bg-white rounded-xl border-2 ${border} p-4 hover:shadow-md hover:border-slate-300 transition-all`}>
      <div className="flex items-start gap-2">
        <span className={`mt-1.5 w-2 h-2 rounded-full shrink-0 ${n.status === 'Ready' ? (n.unschedulable ? 'bg-amber-500' : 'bg-brand-500') : 'bg-rose-500'}`} />
        <div className="min-w-0 flex-1">
          <div className="font-semibold text-sm text-slate-800 truncate" title={n.name}>{n.name}</div>
          <div className="text-[11px] text-slate-500 truncate">{[n.instanceType, n.zone].filter(Boolean).join(' · ') || n.arch}</div>
        </div>
        <div className="text-right shrink-0">
          <div className="text-sm font-bold text-slate-800 tabular-nums">{formatMoney(n.monthlyCost, currency)}</div>
          <div className="text-[10px] text-slate-400">/mo</div>
        </div>
      </div>
      <div className="mt-3 space-y-2">
        <ResourceBar label="CPU" unit="cores" r={n.cpu} metrics={n.metricsAvailable} />
        <ResourceBar label="Memory" unit="GiB" r={n.memoryGiB} metrics={n.metricsAvailable} />
      </div>
      <div className="mt-3 flex flex-wrap items-center gap-1">
        <span className="text-[11px] text-slate-500 mr-1">{n.pods}/{n.maxPods} pods · {age(n.createdAt)}</span>
        <StatusBadges n={n} />
      </div>
    </button>
  )
}

function FindingRow({ f, currency }: { f: Opportunity; currency: string }) {
  const warn = f.kind === 'node-not-ready' || f.kind === 'node-pressure' || f.kind === 'pending-pods'
  return (
    <div className="flex items-start gap-3 p-4 rounded-xl border border-slate-200 bg-white">
      <span className={`mt-0.5 w-8 h-8 shrink-0 rounded-lg flex items-center justify-center ${warn ? 'bg-amber-50 text-amber-600' : 'bg-brand-50 text-brand-700'}`}>
        {findingIcon[f.kind] || <Server size={16} />}
      </span>
      <div className="min-w-0 flex-1">
        <div className="font-semibold text-slate-800 text-sm">{f.title}</div>
        <div className="text-xs text-slate-500 mt-0.5 leading-relaxed">{f.detail}</div>
      </div>
      {f.monthlySavings ? <div className="shrink-0 text-sm font-bold text-brand-700 tabular-nums">−{formatMoney(f.monthlySavings, currency)}<span className="text-xs font-medium text-slate-400">/mo</span></div> : null}
    </div>
  )
}

function NodeDetails({ n, currency, onClose }: { n: NodeView; currency: string; onClose: () => void }) {
  const [pods, setPods] = useState<NodePod[] | null>(null)
  const [error, setError] = useState<string | null>(null)
  usePolling(() => { fetchNodePods(n.name).then(setPods).catch(e => setError(errorMessage(e))) }, 30000, n.name)
  const rate = n.cpu.capacity > 0 ? n.monthlyCost : 0
  return (
    <Modal title={n.name} size="xl" tall onClose={onClose}
      subtitle={[n.pool && `pool ${n.pool}`, n.instanceType, n.zone, n.capacityType, `${age(n.createdAt)} old`].filter(Boolean).join(' · ')}
      headerExtra={<div className="flex flex-wrap gap-1"><StatusBadges n={n} /></div>}>
      <div className="grid gap-4 md:grid-cols-3">
        <KpiTile label="Monthly cost" value={formatMoney(rate, currency)} sub={`${formatMoney(n.monthlyRequested, currency)} requested by pods`} />
        <KpiTile label="Not requested" value={formatMoney(Math.max(0, n.monthlyCost - n.monthlyRequested), currency)} sub="Capacity on this node no pod asked for" />
        <KpiTile label="Pods" value={`${n.pods} / ${n.maxPods}`} sub={n.metricsAvailable ? `${fmt(n.cpu.used)} cores and ${fmt(n.memoryGiB.used)} GiB in use` : 'No metrics-server reading'} />
      </div>
      <div className="mt-5 space-y-3">
        <ResourceBar label="CPU" unit="cores" r={n.cpu} metrics={n.metricsAvailable} />
        <ResourceBar label="Memory" unit="GiB" r={n.memoryGiB} metrics={n.metricsAvailable} />
        <SegmentLegend items={resourceLegend} />
        <p className="text-[11px] text-slate-400">
          Allocatable is what pods can use: {fmt(n.cpu.allocatable)} of {fmt(n.cpu.capacity)} cores and {fmt(n.memoryGiB.allocatable)} of {fmt(n.memoryGiB.capacity)} GiB; the kubelet keeps the rest for the system.
        </p>
      </div>
      {n.namespaces.length > 0 && (
        <div className="mt-6">
          <h3 className="text-sm font-semibold text-slate-800 mb-3">Namespaces on this node</h3>
          <div className="space-y-1.5">
            {n.namespaces.map(t => (
              <div key={t.namespace} className="grid grid-cols-[minmax(0,12rem)_1fr_auto] items-center gap-3 text-sm">
                <span className="truncate text-slate-700">{t.namespace}</span>
                <SegmentBar total={n.cpu.allocatable} height="h-2" segments={[{ key: 'r', label: 'CPU requested', value: t.cpu, color: funnelColors.idle, hint: `${fmt(t.cpu)} cores` }]} />
                <span className="text-xs text-slate-500 tabular-nums">{t.pods} pod{t.pods === 1 ? '' : 's'} · {fmt(t.cpu)} cores · {fmt(t.memoryGiB)} GiB</span>
              </div>
            ))}
          </div>
        </div>
      )}
      <div className="mt-6">
        <h3 className="text-sm font-semibold text-slate-800 mb-3">Pods</h3>
        {error ? <p className="text-sm text-rose-600">{error}</p> : !pods ? (
          <div className="flex items-center gap-2 text-sm text-slate-400"><Loader2 size={14} className="animate-spin" /> Loading…</div>
        ) : (
          <div className="overflow-x-auto rounded-xl border border-slate-200">
            <table className="w-full text-sm">
              <thead className="bg-slate-50 text-xs text-slate-500">
                <tr>
                  <th className="px-3 py-2 text-left font-semibold">Pod</th>
                  <th className="px-3 py-2 text-right font-semibold whitespace-nowrap">CPU used / req.</th>
                  <th className="px-3 py-2 text-right font-semibold whitespace-nowrap">Memory used / req.</th>
                  <th className="px-3 py-2 text-right font-semibold">Cost/mo</th>
                </tr>
              </thead>
              <tbody>
                {pods.map(p => (
                  <tr key={`${p.namespace}/${p.name}`} className="border-t border-slate-100">
                    <td className="px-3 py-2">
                      <div className="font-medium text-slate-800 truncate max-w-[24rem]" title={p.name}>{p.name}</div>
                      <div className="text-[11px] text-slate-500">{p.namespace}{p.daemonSet && ' · DaemonSet'}{p.phase !== 'Running' && ` · ${p.phase}`}</div>
                    </td>
                    <td className="px-3 py-2 text-right tabular-nums text-slate-600">{fmt(p.cpuUsed)} / {fmt(p.cpuRequest)}</td>
                    <td className="px-3 py-2 text-right tabular-nums text-slate-600">{fmt(p.memoryUsedGiB)} / {fmt(p.memoryRequestGiB)} GiB</td>
                    <td className="px-3 py-2 text-right tabular-nums font-medium text-slate-800">{formatMoney(p.monthlyCost, currency)}</td>
                  </tr>
                ))}
              </tbody>
            </table>
          </div>
        )}
      </div>
      <div className="mt-6 grid grid-cols-2 md:grid-cols-4 gap-3 text-xs">
        {[['Architecture', n.arch], ['OS', n.os], ['Kernel', n.kernel], ['Kubelet', n.kubelet]].map(([k, v]) => (
          <div key={k} className="rounded-lg bg-slate-50 border border-slate-100 p-2.5">
            <div className="text-slate-400">{k}</div>
            <div className="mt-0.5 font-medium text-slate-700 break-all">{v || '—'}</div>
          </div>
        ))}
      </div>
    </Modal>
  )
}

export default function ClusterDashboard() {
  const [data, setData] = useState<{ k8sVersion: string; report: NodesReport } | null>(null)
  const [error, setError] = useState<string | null>(null)
  const [groupBy, setGroupBy] = useState<GroupBy>('pool')
  const [view, setView] = useState<View>('map')
  const [query, setQuery] = useState('')
  const [open, setOpen] = useState<string | null>(null)

  usePolling(() => {
    fetchNodes().then(d => { setData(d); setError(null) }).catch(e => setError(errorMessage(e)))
  }, 30000)

  const groups = useMemo(() => {
    if (!data) return []
    const q = query.trim().toLowerCase()
    const nodes = data.report.nodes.filter(n => !q || [n.name, n.pool, n.instanceType, n.zone].some(v => (v || '').toLowerCase().includes(q)))
    const m = new Map<string, NodeView[]>()
    for (const n of nodes) m.set(groupOf(n, groupBy), [...(m.get(groupOf(n, groupBy)) || []), n])
    return [...m.entries()].map(([key, items]) => ({ key, items, cost: items.reduce((a, n) => a + n.monthlyCost, 0) })).sort((a, b) => b.cost - a.cost)
  }, [data, groupBy, query])

  if (!data) {
    return (
      <div className="p-8 max-w-[1600px] mx-auto">
        <PageHeader title="Cluster Node Map" subtitle="What the nodes cost, how well pods fill them, and where capacity sits idle" />
        {error ? <p className="text-sm text-rose-600">Could not load the nodes: {error}</p>
          : <div className="flex items-center gap-2 text-sm text-slate-400"><Loader2 size={16} className="animate-spin" /> Loading…</div>}
      </div>
    )
  }

  const { report } = data
  const s = report.summary
  const cur = report.currency
  const openNode = report.nodes.find(n => n.name === open)

  return (
    <div className="p-8 max-w-[1600px] mx-auto">
      <PageHeader
        title="Cluster Node Map"
        subtitle="What the nodes cost, how well pods fill them, and where capacity sits idle"
        actions={<>
          <Badge>Kubernetes {data.k8sVersion}</Badge>
          <div className="inline-flex rounded-lg border border-slate-200 bg-white p-0.5">
            {([['map', 'Map', <LayoutGrid key="m" size={15} />], ['table', 'Table', <Rows3 key="t" size={15} />]] as const).map(([v, label, icon]) => (
              <button key={v} onClick={() => setView(v)}
                className={`inline-flex items-center gap-1.5 px-2.5 py-1.5 rounded-md text-sm font-semibold ${view === v ? 'bg-slate-900 text-white' : 'text-slate-500 hover:text-slate-800'}`}>
                {icon}{label}
              </button>
            ))}
          </div>
        </>}
      />

      <div className="grid gap-4 sm:grid-cols-2 xl:grid-cols-5">
        <KpiTile label="Nodes" value={s.nodes}
          sub={<>{s.ready} ready · {s.spot} spot{report.spot ? ` (${percent(report.spot.spotShare)} of the bill)` : ''}{s.unschedulable ? ` · ${s.unschedulable} cordoned` : ''}</>} />
        <KpiTile label="Node bill" value={<>{formatMoney(s.monthlyCost, cur)}<span className="text-sm font-medium text-slate-400">/mo</span></>}
          sub={<span title={report.rates.basis}>{formatMoney(s.monthlyCost / 730 * 24, cur)} a day · {report.rates.basis.split(' — ')[0]}</span>} />
        <KpiTile label="Requests fill the nodes"
          status={packingRating((s.cpu.allocatable ? s.cpu.requested / s.cpu.allocatable : 0), (s.memoryGiB.allocatable ? s.memoryGiB.requested / s.memoryGiB.allocatable : 0))}
          value={<span className="whitespace-nowrap">{percent(s.cpu.allocatable ? s.cpu.requested / s.cpu.allocatable : undefined)}<span className="text-slate-300 font-normal"> / </span>{percent(s.memoryGiB.allocatable ? s.memoryGiB.requested / s.memoryGiB.allocatable : undefined)}</span>}
          sub="CPU / memory: share of allocatable capacity pods request (bin-packing)" />
        <KpiTile label="Not requested" value={<>{formatMoney(s.monthlyUnrequested, cur)}<span className="text-sm font-medium text-slate-400">/mo</span></>}
          sub={`${percent(s.monthlyCost ? s.monthlyUnrequested / s.monthlyCost : undefined)} of the node bill`} />
        <KpiTile label="Pods" value={<>{s.pods}<span className="text-sm font-medium text-slate-400"> / {s.maxPods}</span></>}
          sub={s.pendingPods ? <span className="text-amber-700 font-semibold">{s.pendingPods} cannot be scheduled</span> : 'All pods placed'} />
      </div>

      {report.recommendations.length > 0 && (
        <div className="mt-6">
          <SectionTitle aside={<span className="text-xs text-slate-400">sized to what pods request, at 85% fill, list prices</span>}>Node shape recommendations</SectionTitle>
          <div className="rounded-xl border border-slate-200 bg-white shadow-sm overflow-x-auto">
            <table className="w-full text-sm">
              <thead className="bg-slate-50 text-xs text-slate-500">
                <tr>
                  <th className="px-3 py-2.5 text-left font-semibold">Pool</th>
                  <th className="px-3 py-2.5 text-left font-semibold">Now</th>
                  <th className="px-3 py-2.5 text-left font-semibold">Suggested</th>
                  <th className="px-3 py-2.5 text-right font-semibold">Saves</th>
                  <th className="px-3 py-2.5 text-left font-semibold">Why</th>
                </tr>
              </thead>
              <tbody>
                {report.recommendations.map(r => (
                  <tr key={r.pool} className="border-t border-slate-100 align-top">
                    <td className="px-3 py-3 font-medium text-slate-800">{r.pool}</td>
                    <td className="px-3 py-3 text-slate-600 whitespace-nowrap">{r.currentNodes} × {r.currentType}<div className="text-[11px] text-slate-400">{formatMoney(r.currentMonthly, cur)}/mo</div></td>
                    <td className="px-3 py-3 text-slate-800 whitespace-nowrap">{r.nodes} × {r.type}<div className="text-[11px] text-slate-400">{formatMoney(r.monthly, cur)}/mo · {r.family}</div></td>
                    <td className="px-3 py-3 text-right font-bold text-brand-700 tabular-nums whitespace-nowrap">−{formatMoney(r.monthlySavings, cur)}<span className="text-xs font-medium text-slate-400">/mo</span></td>
                    <td className="px-3 py-3 text-xs text-slate-600 leading-relaxed max-w-[28rem]">
                      {r.reason}
                      {r.arm && <div className="mt-1 text-slate-500">On arm64: {r.arm.nodes} × {r.arm.type}, −{formatMoney(r.arm.monthlySavings, cur)}/mo, if your images are built for it.</div>}
                    </td>
                  </tr>
                ))}
              </tbody>
            </table>
          </div>
        </div>
      )}

      {report.findings.filter(f => f.kind !== 'node-type').length > 0 && (
        <div className="mt-6">
          <SectionTitle aside={<span className="text-xs text-slate-400">from requests, not from guesses about your workloads</span>}>Findings</SectionTitle>
          <div className="grid gap-2 lg:grid-cols-2">
            {report.findings.filter(f => f.kind !== 'node-type').map((f, i) => <FindingRow key={i} f={f} currency={cur} />)}
          </div>
        </div>
      )}

      {report.pools.length > 1 && (
        <Card className="p-5 mt-6">
          <h2 className="text-base font-semibold text-slate-800 mb-1">Node pools</h2>
          <p className="text-xs text-slate-500 mb-4">What each pool costs, and how much of it pods request and use.</p>
          <BarList currency={cur} items={report.pools.map(p => {
            const share = (r: typeof p.cpu) => r.allocatable ? r.requested / r.allocatable : 0
            const requested = p.monthlyCost * (share(p.cpu) + share(p.memoryGiB)) / 2
            const used = p.monthlyCost * ((p.cpu.allocatable ? Math.min(p.cpu.used, p.cpu.requested) / p.cpu.allocatable : 0) + (p.memoryGiB.allocatable ? Math.min(p.memoryGiB.used, p.memoryGiB.requested) / p.memoryGiB.allocatable : 0)) / 2
            return {
              key: p.name, label: <span>{p.name} <span className="text-xs text-slate-400">{p.nodes} node{p.nodes === 1 ? '' : 's'}{p.spot ? `, ${p.spot} spot` : ''}</span></span>,
              used, idle: Math.max(0, requested - used), total: p.monthlyCost, sub: p.instanceTypes.slice(0, 2).join(', '),
              onClick: () => { setGroupBy('pool'); setQuery(p.name === 'Unpooled' ? '' : p.name) },
            }
          })} rest={{ label: 'Not requested', color: funnelColors.unallocated }} />
          <div className="mt-4"><SegmentLegend items={resourceLegend} /></div>
        </Card>
      )}

      <div className="flex flex-wrap items-center gap-2 mt-8 mb-4">
        <div className="relative">
          <Search size={15} className="absolute left-3 top-1/2 -translate-y-1/2 text-slate-400" />
          <input value={query} onChange={e => setQuery(e.target.value)} placeholder="Node, pool, type or zone"
            className="pl-9 pr-3 py-2 w-64 text-sm bg-white border border-slate-200 rounded-lg outline-none focus:border-brand-500" />
        </div>
        <select aria-label="Group by" value={groupBy} onChange={e => setGroupBy(e.target.value as GroupBy)}
          className="py-2 pl-3 pr-8 text-sm bg-white border border-slate-200 rounded-lg outline-none focus:border-brand-500 text-slate-700">
          {(Object.keys(groupLabel) as GroupBy[]).map(g => <option key={g} value={g}>{g === 'none' ? 'No grouping' : `Group by ${groupLabel[g].toLowerCase()}`}</option>)}
        </select>
        <div className="ml-auto"><SegmentLegend items={resourceLegend} /></div>
      </div>

      {view === 'map' ? groups.map(g => (
        <section key={g.key || 'all'} className="mb-6">
          {groupBy !== 'none' && (
            <div className="flex items-baseline gap-3 mb-3">
              <h2 className="font-semibold text-slate-800">{g.key}</h2>
              <span className="text-xs text-slate-500">{g.items.length} node{g.items.length === 1 ? '' : 's'} · {formatMoney(g.cost, cur)}/mo</span>
            </div>
          )}
          <div className="grid gap-3 grid-cols-[repeat(auto-fill,minmax(17rem,1fr))]">
            {g.items.map(n => <NodeTile key={n.name} n={n} currency={cur} onOpen={() => setOpen(n.name)} />)}
          </div>
        </section>
      )) : (
        <div className="rounded-xl border border-slate-200 bg-white shadow-sm overflow-x-auto">
          <table className="w-full text-sm">
            <thead className="bg-slate-50 text-xs text-slate-500">
              <tr>
                {['Node', 'Type · zone', 'Age', 'Pods', 'CPU requested', 'Memory requested', 'Cost/mo', 'Not requested', ''].map(h => (
                  <th key={h} className={`px-3 py-2.5 font-semibold whitespace-nowrap ${['Cost/mo', 'Not requested', 'Pods', 'Age'].includes(h) ? 'text-right' : 'text-left'}`}>{h}</th>
                ))}
              </tr>
            </thead>
            <tbody>
              {groups.map(g => (
                <Fragment key={g.key || 'all'}>
                  {groupBy !== 'none' && (
                    <tr className="bg-slate-50/70 border-t border-slate-200"><td colSpan={9} className="px-3 py-2 font-semibold text-slate-800">{g.key} <span className="ml-2 text-xs font-normal text-slate-500">{g.items.length} nodes · {formatMoney(g.cost, cur)}/mo</span></td></tr>
                  )}
                  {g.items.map(n => (
                    <tr key={n.name} onClick={() => setOpen(n.name)} className="border-t border-slate-100 hover:bg-slate-50 cursor-pointer">
                      <td className="px-3 py-2.5 font-medium text-slate-800">{n.name}<div className="flex flex-wrap gap-1 mt-1"><StatusBadges n={n} /></div></td>
                      <td className="px-3 py-2.5 text-slate-600">{[n.instanceType, n.zone].filter(Boolean).join(' · ') || '—'}</td>
                      <td className="px-3 py-2.5 text-right text-slate-600 tabular-nums">{age(n.createdAt)}</td>
                      <td className="px-3 py-2.5 text-right text-slate-600 tabular-nums">{n.pods}/{n.maxPods}</td>
                      <td className="px-3 py-2.5 min-w-[10rem]"><ResourceBar label="" unit="cores" r={n.cpu} metrics={n.metricsAvailable} /></td>
                      <td className="px-3 py-2.5 min-w-[10rem]"><ResourceBar label="" unit="GiB" r={n.memoryGiB} metrics={n.metricsAvailable} /></td>
                      <td className="px-3 py-2.5 text-right font-semibold text-slate-800 tabular-nums">{formatMoney(n.monthlyCost, cur)}</td>
                      <td className="px-3 py-2.5 text-right text-slate-600 tabular-nums">{formatMoney(Math.max(0, n.monthlyCost - n.monthlyRequested), cur)}</td>
                      <td className="px-3 py-2.5 text-slate-400"><ArrowRight size={14} /></td>
                    </tr>
                  ))}
                </Fragment>
              ))}
            </tbody>
          </table>
        </div>
      )}
      <p className="mt-3 text-xs text-slate-400 flex items-center gap-1.5">
        <Cpu size={12} /> Node cost is its capacity at {report.rates.basis.toLowerCase().startsWith('custom') ? 'your custom rates' : 'the rates in effect'}; used is the live metrics-server reading.
      </p>

      {openNode && <NodeDetails n={openNode} currency={cur} onClose={() => setOpen(null)} />}
    </div>
  )
}
