import { useState, type ReactNode } from 'react'
import { AlertTriangle, ArrowRight, CalendarClock, HardDrive, Info, Loader2, Minimize2, Network, Server, TrendingUp } from 'lucide-react'
import { Badge, Button, Card, PageHeader, SectionTitle, Tabs } from '../components/ui'
import { BarList, CostFunnel, CostTrend, Delta, KpiTile, SegmentLegend, type BarItem } from '../components/finops/charts'
import { change, efficiencyRating, environmentLabel, fetchBudgets, fetchOverview, funnelColors, percent, type BudgetStatus, type HistoryDay, type NamespaceRow, type Opportunity, type Overview as OverviewData } from '../lib/finops'
import { formatMoney } from '../lib/format'
import { errorMessage } from '../lib/api'
import { usePolling } from '../lib/usePolling'

type Tab = 'dashboard' | 'scale' | 'cluster' | 'storage' | 'budgets'

interface Props {
  onSelectNamespace: (name: string) => void
  onNavigate: (tab: Tab) => void
}

const opportunityIcon: Partial<Record<Opportunity['kind'], ReactNode>> = {
  rightsizing: <Minimize2 size={16} />,
  schedule: <CalendarClock size={16} />,
  'idle-capacity': <Server size={16} />,
  'cost-increase': <TrendingUp size={16} />,
  'missing-requests': <AlertTriangle size={16} />,
  'unused-volumes': <HardDrive size={16} />,
  'orphaned-volumes': <HardDrive size={16} />,
  'idle-load-balancers': <Network size={16} />,
}

// weekChange compares the last seven complete days with the seven before, once the
// history holds both.
function weekChange(days: HistoryDay[], field: 'bill' | 'saved'): number | undefined {
  const today = new Date().toISOString().slice(0, 10)
  const full = days.filter(d => d.date < today)
  if (full.length < 14) return undefined
  const value = (d: HistoryDay) => (field === 'bill' ? d.provisioned + d.storage + d.network : d.saved)
  const sum = (ds: HistoryDay[]) => ds.reduce((a, d) => a + value(d), 0)
  return change(sum(full.slice(-7)), sum(full.slice(-14, -7)))
}

const sinceDate = (iso?: string) => (iso ? new Date(iso).toLocaleDateString(undefined, { month: 'short', day: 'numeric' }) : '')

// group adds namespaces up by a key: their cost, what of it is used and what is idle.
function group(rows: NamespaceRow[], keyOf: (r: NamespaceRow) => string): BarItem[] {
  const out = new Map<string, BarItem>()
  for (const r of rows) {
    const k = keyOf(r)
    const g = out.get(k) || { key: k, label: k, used: 0, idle: 0, total: 0, sub: undefined }
    g.used += Math.min(r.monthlyUsedCost, r.monthlyCost)
    g.idle += r.monthlyIdle
    g.total += r.monthlyTotal
    out.set(k, g)
  }
  return [...out.values()].filter(g => g.total > 0).sort((a, b) => b.total - a.total)
}

export default function Overview({ onSelectNamespace, onNavigate }: Props) {
  const [data, setData] = useState<OverviewData | null>(null)
  const [error, setError] = useState<string | null>(null)
  const [breakdown, setBreakdown] = useState<'team' | 'environment'>('team')
  const [budgets, setBudgets] = useState<BudgetStatus[]>([])

  usePolling(() => {
    fetchOverview().then(d => { setData(d); setError(null) }).catch(e => setError(errorMessage(e)))
    fetchBudgets().then(b => setBudgets(b.budgets)).catch(() => setBudgets([]))
  }, 30000)

  if (!data) {
    return (
      <div className="p-8 max-w-[1600px] mx-auto">
        <PageHeader title="Cost overview" subtitle="What the cluster costs, what is wasted and what to do about it" />
        {error ? <p className="text-sm text-rose-600">Could not load the overview: {error}</p>
          : <div className="flex items-center gap-2 text-sm text-slate-400"><Loader2 size={16} className="animate-spin" /> Loading…</div>}
      </div>
    )
  }

  const cur = data.currency
  const m = data.monthly
  const couldSave = data.savings.rightsizingMonthly + data.savings.scheduleCandidatesMonthly
  const mtd = data.monthToDate
  const hasTeams = data.namespaces.some(n => n.team)
  const view = hasTeams ? breakdown : 'environment'
  const nsItems: BarItem[] = [...data.namespaces].filter(n => n.monthlyTotal > 0).sort((a, b) => b.monthlyTotal - a.monthlyTotal).slice(0, 8)
    .map(n => ({
      key: n.name, label: n.name, used: n.monthlyUsedCost, idle: n.monthlyIdle, total: n.monthlyTotal,
      sub: n.efficiency !== undefined ? `${percent(n.efficiency)} used` : undefined, onClick: () => onSelectNamespace(n.name),
    }))
  const groupItems = group(data.namespaces, n => (view === 'team' ? n.team || 'No team label' : environmentLabel[n.environment]))
  const schedules = [...new Map(data.namespaces.filter(n => n.schedule).map(n => [`${n.schedule!.kind}/${n.schedule!.name}`, n.schedule!])).values()]
    .sort((a, b) => b.savedMonthly - a.savedMonthly)

  return (
    <div className="p-8 max-w-[1600px] mx-auto">
      <PageHeader
        title="Cost overview"
        subtitle="What the cluster costs, what is wasted and what to do about it"
        actions={<span title={data.rates.basis}><Badge tone={data.rates.basis.toLowerCase().includes('estimate') ? 'warning' : 'neutral'}><Info size={11} /> {priceSource(data.rates.basis)}</Badge></span>}
      />

      <div className="grid gap-4 sm:grid-cols-2 xl:grid-cols-5">
        <KpiTile label="Monthly run rate" value={<>{formatMoney(m.total, cur)}<span className="text-sm font-medium text-slate-400">/mo</span></>}
          delta={<Delta value={weekChange(data.history.days, 'bill')} goodWhen="down" label="vs last week" />}
          sub={<>Nodes {formatMoney(m.provisioned, cur)}{m.storage > 0 && <> · storage {formatMoney(m.storage, cur)}</>}{m.network > 0 && <> · load balancers {formatMoney(m.network, cur)}</>}
            <span className="block">{data.cluster.nodes} node{data.cluster.nodes === 1 ? '' : 's'}{data.cluster.spotNodes ? ` (${data.cluster.spotNodes} spot)` : ''}</span></>} />
        <KpiTile label={`This month (${new Date(`${mtd.month}-01T00:00:00Z`).toLocaleDateString(undefined, { month: 'long', timeZone: 'UTC' })})`}
          value={mtd.hours > 0 ? formatMoney(mtd.cost, cur) : '—'}
          sub={mtd.hours > 0
            ? <>Forecast {formatMoney(mtd.forecast, cur)}{mtd.partial && <> · tracked since {sinceDate(data.history.since)}</>}</>
            : 'Cost history starts now; the first figures appear within minutes.'} />
        <KpiTile label="Cost efficiency" value={percent(m.provisioned > 0 ? m.used / m.provisioned : undefined)}
          status={efficiencyRating(m.provisioned > 0 ? m.used / m.provisioned : undefined)}
          sub={<>Pods request {percent(m.provisioned > 0 ? m.requested / m.provisioned : undefined)} of the nodes and use {percent(m.requested > 0 ? m.used / m.requested : undefined)} of that</>} />
        <KpiTile label="Saved by schedules" tone={data.savings.schedulesMonthly > 0 ? 'good' : undefined} value={<>{formatMoney(data.savings.schedulesMonthly, cur)}<span className="text-sm font-medium text-slate-400">/mo</span></>}
          delta={<Delta value={weekChange(data.history.days, 'saved')} goodWhen="up" label="vs last week" />}
          sub={mtd.saved > 0 ? <>{formatMoney(mtd.saved, cur)} saved this month</> : 'At the rate of what is kept down right now'} />
        <KpiTile label="Could save" value={<>{formatMoney(couldSave, cur)}<span className="text-sm font-medium text-slate-400">/mo</span></>}
          sub={<>Right-sizing {formatMoney(data.savings.rightsizingMonthly, cur)} · schedules {formatMoney(data.savings.scheduleCandidatesMonthly, cur)}
            {data.savings.rightsizingPending > 0 && <span className="block text-slate-400">Analysing {data.savings.rightsizingPending} namespace{data.savings.rightsizingPending === 1 ? '' : 's'}…</span>}</>} />
      </div>

      <div className="grid gap-4 mt-4 lg:grid-cols-5">
        <Card className="p-5 lg:col-span-2">
          <h2 className="text-base font-semibold text-slate-800">Where the money goes</h2>
          <p className="text-xs text-slate-500 mb-4">The node bill, split into what pods use, what they request but leave idle, and what nobody requests.
            {(m.storage > 0 || m.network > 0) && <> Volumes ({formatMoney(m.storage, cur)}) and load balancers ({formatMoney(m.network, cur)}) are billed on top: <button className="font-semibold text-brand-700 hover:text-brand-800" onClick={() => onNavigate('storage')}>Storage &amp; Network</button>.</>}</p>
          <CostFunnel monthly={m} currency={cur}
            cpu={{ capacity: data.cluster.cpuCores, requested: data.cluster.cpuRequested, used: data.cluster.cpuUsed }}
            memory={{ capacity: data.cluster.memoryGiB, requested: data.cluster.memoryRequestedGiB, used: data.cluster.memoryUsedGiB }} />
        </Card>
        <Card className="p-5 lg:col-span-3">
          <h2 className="text-base font-semibold text-slate-800">Daily cost by namespace</h2>
          <p className="text-xs text-slate-500 mb-4">What pods request each day, at today's rates. UTC days.</p>
          {data.history.days.length > 0
            ? <CostTrend days={data.history.days} top={data.history.top} currency={cur} />
            : <EmptyHistory since={data.history.since} />}
        </Card>
      </div>

      <div className="grid gap-4 mt-4 lg:grid-cols-2">
        <Card className="p-5">
          <div className="flex items-center justify-between mb-4">
            <h2 className="text-base font-semibold text-slate-800">Most expensive namespaces</h2>
            <Button variant="ghost" size="sm" onClick={() => onNavigate('dashboard')}>All namespaces <ArrowRight size={14} /></Button>
          </div>
          {nsItems.length ? <BarList items={nsItems} currency={cur} rest={restSegment} /> : <p className="text-sm text-slate-400">No namespace requests anything yet.</p>}
          <Legend />
        </Card>
        <Card className="p-5">
          <div className="flex items-center justify-between mb-2">
            <h2 className="text-base font-semibold text-slate-800">Cost by {view === 'team' ? 'team' : 'environment'}</h2>
            {hasTeams && <Tabs value={breakdown} onChange={setBreakdown} tabs={[{ id: 'team', label: 'Team' }, { id: 'environment', label: 'Environment' }]} />}
          </div>
          {!hasTeams && <p className="text-xs text-slate-500 mb-3">Add a <code className="px-1 rounded bg-slate-100">team</code> label to namespaces to see cost by team.</p>}
          <div className="mt-3"><BarList items={groupItems} currency={cur} rest={restSegment} /></div>
          <Legend />
        </Card>
      </div>

      <div className="grid gap-4 mt-4 lg:grid-cols-3">
        <div className="lg:col-span-2">
          <SectionTitle aside={<span className="text-xs text-slate-400">ranked by what they save</span>}>What to do next</SectionTitle>
          {data.opportunities.length === 0 ? (
            <Card className="p-6 text-sm text-slate-500">Nothing stands out right now{data.savings.rightsizingPending > 0 ? ' — right-sizing advice is still being calculated' : ''}.</Card>
          ) : (
            <div className="space-y-2">
              {data.opportunities.map((op, i) => (
                <OpportunityRow key={i} op={op} currency={cur}
                  onAct={() => (op.kind === 'schedule' ? onNavigate('scale') : op.kind === 'idle-capacity' ? onNavigate('cluster')
                    : op.kind === 'unused-volumes' || op.kind === 'orphaned-volumes' || op.kind === 'idle-load-balancers' ? onNavigate('storage')
                    : op.namespace && onSelectNamespace(op.namespace))} />
              ))}
            </div>
          )}
        </div>
        <div>
          {budgets.length > 0 && (
            <div className="mb-6">
              <SectionTitle aside={<Button variant="ghost" size="sm" onClick={() => onNavigate('budgets')}>Budgets <ArrowRight size={14} /></Button>}>Budgets this month</SectionTitle>
              <Card className="p-4 space-y-3">
                {budgets.slice(0, 5).map(b => {
                  const share = b.limit ? b.spent / b.limit : 0
                  return (
                    <div key={b.budget.name}>
                      <div className="flex items-baseline gap-2 text-sm">
                        <span className="font-medium text-slate-700 truncate">{b.budget.name}</span>
                        {b.state !== 'ok' && <Badge tone={b.state === 'over' ? 'danger' : 'warning'}>{b.state === 'over' ? 'Over' : 'Heading over'}</Badge>}
                        <span className="ml-auto text-xs text-slate-500 tabular-nums">{formatMoney(b.spent, cur)} / {formatMoney(b.limit, cur)}</span>
                      </div>
                      <div className="mt-1 h-1.5 rounded-full bg-slate-100 overflow-hidden">
                        <div className={`h-full rounded-full ${b.state === 'over' ? 'bg-rose-500' : b.state === 'at-risk' ? 'bg-amber-500' : 'bg-brand-700'}`} style={{ width: `${Math.min(100, share * 100)}%` }} />
                      </div>
                    </div>
                  )
                })}
              </Card>
            </div>
          )}
          <SectionTitle aside={<Button variant="ghost" size="sm" onClick={() => onNavigate('scale')}>Schedules <ArrowRight size={14} /></Button>}>Savings by schedule</SectionTitle>
          <Card className="p-4">
            {schedules.length === 0 ? (
              <p className="text-sm text-slate-500">No schedules yet. Non-production namespaces that run around the clock are listed under “What to do next”.</p>
            ) : (
              <div className="divide-y divide-slate-100">
                {schedules.map(s => (
                  <div key={`${s.kind}/${s.name}`} className="flex items-center gap-2 py-2 text-sm">
                    <span className={`w-2 h-2 rounded-full ${s.desiredState === 'Down' ? 'bg-slate-400' : 'bg-brand-500'}`} />
                    <span className="font-medium text-slate-700 truncate">{s.name}</span>
                    <span className="text-xs text-slate-400">{s.desiredState === 'Down' ? 'down' : 'up'}</span>
                    <span className="ml-auto font-semibold tabular-nums text-brand-700">{s.savedMonthly > 0 ? `${formatMoney(s.savedMonthly, cur)}/mo` : '—'}</span>
                  </div>
                ))}
              </div>
            )}
            <p className="mt-3 text-[11px] text-slate-400">The rate of what each schedule keeps down right now.</p>
          </Card>
        </div>
      </div>
    </div>
  )
}

function priceSource(basis: string): string {
  const b = basis.toLowerCase()
  if (b.startsWith('custom')) return 'Custom rates'
  if (b.includes('aws')) return 'AWS list prices'
  if (b.includes('azure')) return 'Azure list prices'
  return 'Estimated prices'
}

const restSegment = { label: 'Volumes and load balancers', color: '#cbd5e1' }

function Legend() {
  return (
    <div className="mt-4">
      <SegmentLegend items={[
        { label: 'Compute used', color: funnelColors.used },
        { label: 'Requested, not used', color: funnelColors.idle },
        restSegment,
      ]} />
    </div>
  )
}

function EmptyHistory({ since }: { since?: string }) {
  return (
    <div className="h-52 flex flex-col items-center justify-center text-center rounded-lg border border-dashed border-slate-200">
      <Loader2 size={18} className="text-slate-300 mb-2" />
      <p className="text-sm text-slate-600">Collecting cost history{since ? ` since ${new Date(since).toLocaleString(undefined, { dateStyle: 'medium', timeStyle: 'short' })}` : ''}.</p>
      <p className="text-xs text-slate-400 mt-1">Cost Deck books the cluster's cost every five minutes; the first day appears shortly.</p>
    </div>
  )
}

function OpportunityRow({ op, currency, onAct }: { op: Opportunity; currency: string; onAct: () => void }) {
  const action = op.kind === 'schedule' ? 'Create schedule' : op.kind === 'idle-capacity' ? 'Node map'
    : op.kind === 'unused-volumes' || op.kind === 'orphaned-volumes' || op.kind === 'idle-load-balancers' ? 'Storage & Network' : 'Open namespace'
  return (
    <div className="flex items-start gap-3 p-4 rounded-xl border border-slate-200 bg-white">
      <span className={`mt-0.5 w-8 h-8 shrink-0 rounded-lg flex items-center justify-center ${op.kind === 'missing-requests' || op.kind === 'cost-increase' ? 'bg-amber-50 text-amber-600' : 'bg-brand-50 text-brand-700'}`}>
        {opportunityIcon[op.kind]}
      </span>
      <div className="min-w-0 flex-1">
        <div className="font-semibold text-slate-800 text-sm">{op.title}</div>
        <div className="text-xs text-slate-500 mt-0.5 leading-relaxed">{op.detail}</div>
      </div>
      <div className="shrink-0 text-right">
        {op.monthlySavings ? <div className="text-sm font-bold text-brand-700 tabular-nums">−{formatMoney(op.monthlySavings, currency)}<span className="text-xs font-medium text-slate-400">/mo</span></div> : null}
        <button onClick={onAct} className="mt-1 text-xs font-semibold text-slate-500 hover:text-brand-700 inline-flex items-center gap-1">{action} <ArrowRight size={12} /></button>
      </div>
    </div>
  )
}
