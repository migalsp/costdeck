import { Fragment, useMemo, useState, type ReactNode } from 'react'
import { ArrowDown, ArrowUp, Download, LayoutGrid, Loader2, Rows3, Search } from 'lucide-react'
import NamespaceCard from '../components/NamespaceCard'
import { Badge, Button, PageHeader } from '../components/ui'
import { Delta, SegmentBar, SegmentLegend, Sparkline } from '../components/finops/charts'
import {
  couldSave, downloadCsv, environmentLabel, fetchOverview, funnelColors, isDown, percent, weekChange,
  type Environment, type NamespaceRow, type Overview,
} from '../lib/finops'
import { formatMoney } from '../lib/format'
import { errorMessage } from '../lib/api'
import { usePolling } from '../lib/usePolling'
import { describeRange, historyRanges, type HistorySource } from '../lib/history'

type View = 'table' | 'cards'
type SortKey = 'cost' | 'idle' | 'efficiency' | 'save' | 'change' | 'name'
type GroupBy = 'none' | 'team' | 'environment' | 'schedule'

interface Filters {
  query: string
  env: 'all' | Environment
  schedule: 'all' | 'scheduled' | 'unscheduled' | 'down'
  issue: 'all' | 'overprovisioned' | 'missing' | 'uncapped' | 'savings' | 'healthy'
  team: string // 'all', '__none__' or a team
}

const NO_TEAM = '__none__'
const VIEW_KEY = 'costdeck.insights.view'
const RANGE_KEY = 'costdeck.insights.range'

function storedView(): View {
  try {
    return localStorage.getItem(VIEW_KEY) === 'cards' ? 'cards' : 'table'
  } catch {
    return 'table'
  }
}

function storedRange(): string {
  try {
    return localStorage.getItem(RANGE_KEY) || ''
  } catch {
    return ''
  }
}

const matches = (n: NamespaceRow, f: Filters) => {
  const q = f.query.trim().toLowerCase()
  if (q && !n.name.toLowerCase().includes(q) && !(n.team || '').toLowerCase().includes(q) && !(n.schedule?.name || '').toLowerCase().includes(q)) return false
  if (f.env !== 'all' && n.environment !== f.env) return false
  if (f.schedule === 'scheduled' && !n.schedule) return false
  if (f.schedule === 'unscheduled' && n.schedule) return false
  if (f.schedule === 'down' && !isDown(n)) return false
  if (f.team !== 'all' && (f.team === NO_TEAM ? !!n.team : n.team !== f.team)) return false
  switch (f.issue) {
    case 'overprovisioned': return n.insights.some(i => i.startsWith('Overprovisioned'))
    case 'missing': return n.insights.includes('Missing Requests')
    case 'uncapped': return n.insights.includes('Uncapped')
    case 'savings': return couldSave(n) > 0
    case 'healthy': return n.insights.includes('Optimized')
  }
  return true
}

interface Row { ns: NamespaceRow; cost: number; share: number }

const sorters: Record<SortKey, (a: Row, b: Row) => number> = {
  cost: (a, b) => a.cost - b.cost,
  idle: (a, b) => a.ns.monthlyIdle - b.ns.monthlyIdle,
  efficiency: (a, b) => (a.ns.efficiency ?? -1) - (b.ns.efficiency ?? -1),
  save: (a, b) => couldSave(a.ns) - couldSave(b.ns),
  change: (a, b) => (weekChange(a.ns) ?? -Infinity) - (weekChange(b.ns) ?? -Infinity),
  name: (a, b) => b.ns.name.localeCompare(a.ns.name),
}

const groupKey: Record<Exclude<GroupBy, 'none'>, (n: NamespaceRow) => string> = {
  team: n => n.team || 'No team label',
  environment: n => environmentLabel[n.environment],
  schedule: n => (n.schedule ? n.schedule.name : n.scheduleSavingMonthly ? 'Not scheduled — candidates' : 'Not scheduled'),
}

export default function NamespaceInsights({ onSelectNamespace }: { onSelectNamespace: (name: string) => void }) {
  const [data, setData] = useState<Overview | null>(null)
  const [error, setError] = useState<string | null>(null)
  const [view, setView] = useState<View>(storedView)
  const [filters, setFilters] = useState<Filters>({ query: '', env: 'all', schedule: 'all', issue: 'all', team: 'all' })
  const [groupBy, setGroupBy] = useState<GroupBy>('none')
  const [sort, setSort] = useState<{ key: SortKey; desc: boolean }>({ key: 'cost', desc: true })
  const [idleShare, setIdleShare] = useState(false)
  const [historySource, setHistorySource] = useState<HistorySource | null>(null)
  const [chosenRange, setChosenRange] = useState(storedRange)

  usePolling(() => {
    fetchOverview().then(d => { setData(d); setError(null) }).catch(e => setError(errorMessage(e)))
  }, 30000)
  // Whether VictoriaMetrics is on, and how far back it reaches, decides the chart windows.
  usePolling(() => {
    fetch('/api/settings').then(r => (r.ok ? r.json() : null))
      .then(d => setHistorySource(d?.integrations?.victoriaMetrics ?? { enabled: false }))
      .catch(() => setHistorySource({ enabled: false }))
  }, 300000)
  const ranges = historyRanges(historySource)
  // Unless the viewer picked one, the charts show the whole configured lookback window.
  const range = ranges.includes(chosenRange) ? chosenRange : ranges[ranges.length - 1]
  const changeRange = (r: string) => {
    setChosenRange(r)
    try { localStorage.setItem(RANGE_KEY, r) } catch { /* per-browser convenience only */ }
  }

  const changeView = (v: View) => {
    setView(v)
    try { localStorage.setItem(VIEW_KEY, v) } catch { /* per-browser convenience only */ }
  }
  const set = <K extends keyof Filters>(k: K, v: Filters[K]) => setFilters(f => ({ ...f, [k]: v }))

  const teams = useMemo(() => [...new Set((data?.namespaces || []).map(n => n.team).filter((t): t is string => !!t))].sort(), [data])

  const rows = useMemo<Row[]>(() => {
    if (!data) return []
    // Showback with 100% allocation: capacity nobody requested is shared out in proportion
    // to what each namespace requests.
    const requested = data.namespaces.reduce((a, n) => a + n.monthlyCost, 0)
    const out = data.namespaces.filter(n => matches(n, filters)).map(n => {
      const share = idleShare && requested > 0 ? data.monthly.unallocated * (n.monthlyCost / requested) : 0
      return { ns: n, share, cost: n.monthlyTotal + share }
    })
    const cmp = sorters[sort.key]
    return out.sort((a, b) => (sort.desc ? -cmp(a, b) : cmp(a, b)) || a.ns.name.localeCompare(b.ns.name))
  }, [data, filters, idleShare, sort])

  const groups = useMemo(() => {
    if (groupBy === 'none') return [{ key: '', rows }]
    const m = new Map<string, Row[]>()
    for (const r of rows) {
      const k = groupKey[groupBy](r.ns)
      m.set(k, [...(m.get(k) || []), r])
    }
    return [...m.entries()].map(([key, rs]) => ({ key, rows: rs })).sort((a, b) => total(b.rows) - total(a.rows))
  }, [rows, groupBy])

  const cur = data?.currency || 'USD'
  const sum = (f: (r: Row) => number) => rows.reduce((a, r) => a + f(r), 0)
  const costSum = sum(r => r.cost)
  const requestedSum = sum(r => r.ns.monthlyCost)
  const usedSum = sum(r => Math.min(r.ns.monthlyUsedCost, r.ns.monthlyCost))

  const exportCsv = () => downloadCsv('namespace-costs',
    ['namespace', 'environment', 'team', 'pods', 'cpu_requested_cores', 'cpu_used_cores', 'memory_requested_gib', 'memory_used_gib',
      `monthly_total_${cur}`, `monthly_compute_${cur}`, `monthly_storage_${cur}`, `monthly_load_balancers_${cur}`, `idle_share_${cur}`, `monthly_used_${cur}`, `monthly_idle_${cur}`, 'efficiency', `rightsizing_${cur}`,
      `schedule_saving_${cur}`, 'schedule', `cost_last_7d_${cur}`, `cost_prev_7d_${cur}`, 'insights'],
    rows.map(({ ns, share }) => [ns.name, ns.environment, ns.team || '', ns.pods, ns.cpu.requested.toFixed(3), ns.cpu.used.toFixed(3),
      ns.memoryGiB.requested.toFixed(3), ns.memoryGiB.used.toFixed(3), ns.monthlyTotal.toFixed(2), ns.monthlyCost.toFixed(2), ns.monthlyStorage.toFixed(2), ns.monthlyNetwork.toFixed(2), share.toFixed(2), ns.monthlyUsedCost.toFixed(2),
      ns.monthlyIdle.toFixed(2), ns.efficiency !== undefined ? ns.efficiency.toFixed(3) : '', (ns.rightsizingMonthly ?? 0).toFixed(2),
      (ns.scheduleSavingMonthly ?? 0).toFixed(2), ns.schedule?.name || '', ns.cost7d?.toFixed(2) ?? '', ns.costPrev7d?.toFixed(2) ?? '', ns.insights.join('; ')]))

  const sortHeader = (key: SortKey, label: string, align: 'left' | 'right' = 'right') => (
    <th className={`px-3 py-2.5 font-semibold whitespace-nowrap ${align === 'right' ? 'text-right' : 'text-left'}`}>
      <button onClick={() => setSort(s => ({ key, desc: s.key === key ? !s.desc : key !== 'name' }))}
        className={`inline-flex items-center gap-1 hover:text-slate-800 ${sort.key === key ? 'text-slate-800' : ''}`}>
        {label}{sort.key === key && (sort.desc ? <ArrowDown size={12} /> : <ArrowUp size={12} />)}
      </button>
    </th>
  )

  return (
    <div className="p-8 max-w-[1600px] mx-auto">
      <PageHeader
        title="Namespace Insights"
        subtitle="What every namespace costs, how much of it is used, and what could be saved"
        actions={<>
          {view === 'cards' && (
            <div className="inline-flex rounded-lg border border-slate-200 bg-white p-0.5"
              title={ranges.length > 1 ? 'Usage history shown in the charts' : 'Longer usage history needs VictoriaMetrics (Settings → Integrations)'}>
              {ranges.map(r => (
                <button key={r} onClick={() => changeRange(r)} aria-label={`Last ${describeRange(r)}`}
                  className={`px-2.5 py-1.5 rounded-md text-sm font-semibold ${range === r ? 'bg-slate-900 text-white' : 'text-slate-500 hover:text-slate-800'}`}>
                  {r}
                </button>
              ))}
            </div>
          )}
          <div className="inline-flex rounded-lg border border-slate-200 bg-white p-0.5">
            <ViewButton active={view === 'table'} onClick={() => changeView('table')} icon={<Rows3 size={15} />} label="Table" />
            <ViewButton active={view === 'cards'} onClick={() => changeView('cards')} icon={<LayoutGrid size={15} />} label="Cards" />
          </div>
          <Button onClick={exportCsv} disabled={!rows.length} icon={<Download size={15} />}>Export CSV</Button>
        </>}
      />

      <div className="flex flex-wrap items-center gap-2 mb-4">
        <div className="relative">
          <Search size={15} className="absolute left-3 top-1/2 -translate-y-1/2 text-slate-400" />
          <input value={filters.query} onChange={e => set('query', e.target.value)} placeholder="Namespace, team or schedule"
            className="pl-9 pr-3 py-2 w-64 text-sm bg-white border border-slate-200 rounded-lg outline-none focus:border-brand-500" />
        </div>
        <Select label="Environment" value={filters.env} onChange={v => set('env', v as Filters['env'])}
          options={[['all', 'All environments'], ['production', 'Production'], ['non-production', 'Non-production'], ['system', 'System'], ['unclassified', 'Unclassified']]} />
        <Select label="Schedule" value={filters.schedule} onChange={v => set('schedule', v as Filters['schedule'])}
          options={[['all', 'Any schedule'], ['scheduled', 'Scheduled'], ['unscheduled', 'Not scheduled'], ['down', 'Down right now']]} />
        <Select label="Finding" value={filters.issue} onChange={v => set('issue', v as Filters['issue'])}
          options={[['all', 'Any finding'], ['savings', 'Could save'], ['overprovisioned', 'Overprovisioned'], ['missing', 'Missing requests'], ['uncapped', 'No limits'], ['healthy', 'Healthy']]} />
        {teams.length > 0 && (
          <Select label="Team" value={filters.team} onChange={v => set('team', v)}
            options={[['all', 'All teams'], ...teams.map(t => [t, t] as [string, string]), [NO_TEAM, 'No team label']]} />
        )}
        <Select label="Group by" value={groupBy} onChange={v => setGroupBy(v as GroupBy)}
          options={[['none', 'No grouping'], ['team', 'Group by team'], ['environment', 'Group by environment'], ['schedule', 'Group by schedule']]} />
        <label className="ml-auto inline-flex items-center gap-2 text-sm text-slate-600 cursor-pointer" title="Share the cost of node capacity nobody requested out to namespaces in proportion to what they request, so the namespaces add up to the whole node bill.">
          <input type="checkbox" checked={idleShare} onChange={e => setIdleShare(e.target.checked)} className="accent-brand-600" />
          Include unrequested capacity
        </label>
      </div>

      {!data ? (
        error ? <p className="text-sm text-rose-600">Could not load namespaces: {error}</p>
          : <div className="flex items-center gap-2 text-sm text-slate-400"><Loader2 size={16} className="animate-spin" /> Loading…</div>
      ) : (
        <>
          <div className="grid grid-cols-2 md:grid-cols-5 gap-px rounded-xl overflow-hidden border border-slate-200 bg-slate-200 mb-4">
            <Stat label="Namespaces" value={`${rows.length}${rows.length !== data.namespaces.length ? ` of ${data.namespaces.length}` : ''}`} />
            <Stat label={idleShare ? 'Monthly cost, fully allocated' : 'Monthly cost'} value={formatMoney(costSum, cur)} />
            <Stat label="Requested, not used" value={formatMoney(sum(r => r.ns.monthlyIdle), cur)} />
            <Stat label="Efficiency" value={percent(requestedSum > 0 ? usedSum / requestedSum : undefined)} />
            <Stat label="Could save" value={formatMoney(sum(r => couldSave(r.ns)), cur)} good />
          </div>

          {rows.length === 0 ? (
            <div className="p-12 text-center rounded-xl border border-dashed border-slate-300 bg-white text-slate-500">No namespace matches these filters.</div>
          ) : view === 'cards' ? (
            groups.map(g => (
              <Fragment key={g.key}>
                {g.key && <GroupTitle name={g.key} rows={g.rows} currency={cur} />}
                <div className="grid grid-cols-1 xl:grid-cols-2 gap-6 mb-6">
                  {g.rows.map(r => <NamespaceCard key={r.ns.name} namespace={r.ns.name} insights={r.ns.insights} range={historySource ? range : null} onClick={() => onSelectNamespace(r.ns.name)} />)}
                </div>
              </Fragment>
            ))
          ) : (
            <div className="rounded-xl border border-slate-200 bg-white shadow-sm overflow-x-auto">
              <table className="w-full text-sm">
                <thead className="bg-slate-50 text-xs text-slate-500">
                  <tr>
                    {sortHeader('name', 'Namespace', 'left')}
                    {sortHeader('cost', idleShare ? 'Cost/mo + share' : 'Cost/mo')}
                    {sortHeader('change', 'Trend · 7d')}
                    <th className="px-3 py-2.5 font-semibold text-left whitespace-nowrap">CPU used / req.</th>
                    <th className="px-3 py-2.5 font-semibold text-left whitespace-nowrap">Memory used / req.</th>
                    {sortHeader('efficiency', 'Efficiency')}
                    {sortHeader('idle', 'Idle/mo')}
                    {sortHeader('save', 'Could save')}
                    <th className="px-3 py-2.5 font-semibold text-left">Schedule</th>
                  </tr>
                </thead>
                <tbody>
                  {groups.map(g => (
                    <Fragment key={g.key}>
                      {g.key && (
                        <tr className="bg-slate-50/70 border-t border-slate-200">
                          <td colSpan={9} className="px-3 py-2"><GroupTitle name={g.key} rows={g.rows} currency={cur} inline /></td>
                        </tr>
                      )}
                      {g.rows.map(r => <NamespaceTableRow key={r.ns.name} row={r} currency={cur} onOpen={() => onSelectNamespace(r.ns.name)} />)}
                    </Fragment>
                  ))}
                </tbody>
              </table>
            </div>
          )}
          {view === 'table' && rows.length > 0 && (
            <div className="mt-3"><SegmentLegend items={[{ label: 'Used', color: funnelColors.used }, { label: 'Requested, not used', color: funnelColors.idle }]} /></div>
          )}
          <p className="mt-3 text-xs text-slate-400">
            Costs are the namespace's requests at {rate(data.rates.cpuCoreHour, cur)} per core-hour and {rate(data.rates.memoryGiBHour, cur)} per GiB-hour ({data.rates.basis}), plus its volumes and load balancers.
            “Could save” adds right-sizing advice and, for unscheduled non-production namespaces, what working hours would save.
          </p>
        </>
      )}
    </div>
  )
}

// rate shows a price per unit-hour with enough digits to be meaningful.
const rate = (v: number, currency: string) => {
  try {
    return new Intl.NumberFormat(undefined, { style: 'currency', currency, maximumSignificantDigits: 3 }).format(v)
  } catch {
    return `${v.toPrecision(3)} ${currency}`
  }
}

const total = (rows: Row[]) => rows.reduce((a, r) => a + r.cost, 0)

function GroupTitle({ name, rows, currency, inline }: { name: string; rows: Row[]; currency: string; inline?: boolean }) {
  return (
    <div className={`flex items-center gap-3 ${inline ? '' : 'mb-3'}`}>
      <span className="font-semibold text-slate-800">{name}</span>
      <span className="text-xs text-slate-500">{rows.length} namespace{rows.length === 1 ? '' : 's'} · {formatMoney(total(rows), currency)}/mo · could save {formatMoney(rows.reduce((a, r) => a + couldSave(r.ns), 0), currency)}</span>
    </div>
  )
}

function NamespaceTableRow({ row, currency, onOpen }: { row: Row; currency: string; onOpen: () => void }) {
  const { ns } = row
  const change = weekChange(ns)
  const save = couldSave(ns)
  const fmt = (v: number) => (v >= 10 ? v.toFixed(1) : v.toFixed(2))
  return (
    <tr onClick={onOpen} className="border-t border-slate-100 hover:bg-slate-50 cursor-pointer align-top">
      <td className="px-3 py-3 min-w-[14rem]">
        <div className="font-semibold text-slate-800">{ns.name}</div>
        <div className="mt-1 flex flex-wrap gap-1">
          <Badge tone={ns.environment === 'production' ? 'info' : 'neutral'}>{environmentLabel[ns.environment]}</Badge>
          {ns.team && <Badge>{ns.team}</Badge>}
          <span className="text-[11px] text-slate-400 self-center">{ns.pods} pod{ns.pods === 1 ? '' : 's'}</span>
        </div>
        {ns.insights.some(i => i !== 'Optimized') && (
          <div className="mt-1 flex flex-wrap gap-1 [&>span]:whitespace-nowrap">
            {ns.insights.filter(i => i !== 'Optimized').map(i => (
              <Badge key={i} tone={i === 'Uncapped' ? 'neutral' : 'warning'}>{i === 'Uncapped' ? 'No limits' : i}</Badge>
            ))}
          </div>
        )}
      </td>
      <td className="px-3 py-3 text-right tabular-nums">
        <div className="font-semibold text-slate-800" title={`Compute ${formatMoney(ns.monthlyCost, currency)}, storage ${formatMoney(ns.monthlyStorage, currency)}, load balancers ${formatMoney(ns.monthlyNetwork, currency)}`}>{formatMoney(row.cost, currency)}</div>
        {(ns.monthlyStorage > 0 || ns.monthlyNetwork > 0) && <div className="text-[11px] text-slate-400">{ns.monthlyStorage > 0 && `${formatMoney(ns.monthlyStorage, currency)} storage`}{ns.monthlyStorage > 0 && ns.monthlyNetwork > 0 && ' · '}{ns.monthlyNetwork > 0 && `${formatMoney(ns.monthlyNetwork, currency)} LB`}</div>}
        {row.share > 0 && <div className="text-[11px] text-slate-400">incl. {formatMoney(row.share, currency)} share</div>}
      </td>
      <td className="px-3 py-3 text-right">
        <div className="flex items-center justify-end gap-2">
          <Sparkline values={ns.trend || []} />
          <span className="w-14 inline-flex justify-end"><Delta value={change} goodWhen="down" compact /></span>
        </div>
      </td>
      <td className="px-3 py-3 min-w-[9rem]"><Usage used={ns.cpu.used} requested={ns.cpu.requested} unit="cores" fmt={fmt} /></td>
      <td className="px-3 py-3 min-w-[9rem]"><Usage used={ns.memoryGiB.used} requested={ns.memoryGiB.requested} unit="GiB" fmt={fmt} /></td>
      <td className="px-3 py-3 text-right tabular-nums text-slate-700">{percent(ns.efficiency)}</td>
      <td className="px-3 py-3 text-right tabular-nums text-slate-700">{ns.monthlyIdle > 0 ? formatMoney(ns.monthlyIdle, currency) : '—'}</td>
      <td className="px-3 py-3 text-right tabular-nums" title={save > 0 ? `Right-sizing ${formatMoney(ns.rightsizingMonthly || 0, currency)}, schedule ${formatMoney(ns.scheduleSavingMonthly || 0, currency)}` : undefined}>
        {save > 0 ? <span className="font-semibold text-brand-700">{formatMoney(save, currency)}</span>
          : ns.rightsizingMonthly === undefined && ns.monthlyCost > 0 ? <span className="text-[11px] text-slate-400">analysing…</span> : <span className="text-slate-400">—</span>}
      </td>
      <td className="px-3 py-3">
        {ns.schedule ? (
          <span className="inline-flex items-center gap-1.5 text-xs text-slate-700">
            <span className={`w-2 h-2 rounded-full ${isDown(ns) ? 'bg-slate-400' : 'bg-brand-500'}`} />{ns.schedule.name}
            <span className="text-slate-400">{isDown(ns) ? 'down' : 'up'}</span>
          </span>
        ) : ns.scheduleSavingMonthly ? <Badge tone="brand">Candidate</Badge> : <span className="text-xs text-slate-400">—</span>}
      </td>
    </tr>
  )
}

function Usage({ used, requested, unit, fmt }: { used: number; requested: number; unit: string; fmt: (v: number) => string }) {
  return (
    <div>
      <div className="text-xs text-slate-600 tabular-nums mb-1">{fmt(used)} / {fmt(requested)} <span className="text-slate-400">{unit}</span></div>
      {requested > 0 ? (
        <SegmentBar total={Math.max(requested, used)} height="h-1.5" segments={[
          { key: 'u', label: 'Used', value: Math.min(used, requested), color: funnelColors.used, hint: `${fmt(used)} ${unit}` },
          { key: 'i', label: 'Requested, not used', value: Math.max(0, requested - used), color: funnelColors.idle, hint: `${fmt(Math.max(0, requested - used))} ${unit}` },
        ]} />
      ) : <div className="text-[11px] text-slate-400">no requests</div>}
    </div>
  )
}

function Stat({ label, value, good }: { label: string; value: ReactNode; good?: boolean }) {
  return (
    <div className="bg-white px-4 py-3">
      <div className="text-[11px] font-semibold text-slate-500">{label}</div>
      <div className={`mt-0.5 text-lg font-bold tabular-nums ${good ? 'text-brand-700' : 'text-slate-900'}`}>{value}</div>
    </div>
  )
}

function ViewButton({ active, onClick, icon, label }: { active: boolean; onClick: () => void; icon: ReactNode; label: string }) {
  return (
    <button onClick={onClick} className={`inline-flex items-center gap-1.5 px-2.5 py-1.5 rounded-md text-sm font-semibold ${active ? 'bg-slate-900 text-white' : 'text-slate-500 hover:text-slate-800'}`}>
      {icon}{label}
    </button>
  )
}

function Select({ label, value, onChange, options }: { label: string; value: string; onChange: (v: string) => void; options: [string, string][] }) {
  return (
    <select aria-label={label} value={value} onChange={e => onChange(e.target.value)}
      className={`py-2 pl-3 pr-8 text-sm border rounded-lg outline-none focus:border-brand-500 ${value !== 'all' && value !== 'none' ? 'bg-brand-50 border-brand-200 text-brand-800' : 'bg-white border-slate-200 text-slate-700'}`}>
      {options.map(([v, l]) => <option key={v} value={v}>{l}</option>)}
    </select>
  )
}
