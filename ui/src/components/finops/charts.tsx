import { useState, type ReactNode } from 'react'
import { ArrowDownRight, ArrowUpRight, Minus } from 'lucide-react'
import { Badge, type Tone } from '../ui'
import { formatMoney } from '../../lib/format'
import { funnelColors, otherColor, OTHER, percent, seriesColors, type HistoryDay } from '../../lib/finops'

// Charts for the cost pages. Bars are thin and rounded at their ends, segments are
// separated by a 2px surface gap, every series has a legend with its value, and every
// mark explains itself on hover.

// Values stay in ink; colour only carries meaning: a change that helps or hurts, or a
// rating. Everything else is neutral, so the eye goes where something needs attention.

export function KpiTile({ label, value, sub, tone, delta, status, children }: {
  label: string
  value: ReactNode
  sub?: ReactNode
  // good colours the value as a saving; use it only for amounts above zero.
  tone?: 'good'
  delta?: ReactNode
  status?: { label: string; tone: Tone; title?: string }
  children?: ReactNode
}) {
  return (
    <div className="bg-white rounded-xl border border-slate-200 shadow-sm p-5 min-w-0">
      <div className="flex items-center justify-between gap-2">
        <span className="text-xs font-semibold text-slate-500">{label}</span>
        {status && <Badge tone={status.tone} title={status.title}>{status.label}</Badge>}
      </div>
      <div className={`mt-2 text-2xl font-bold tracking-tight ${tone === 'good' ? 'text-brand-700' : 'text-slate-900'}`}>{value}</div>
      {delta && <div className="mt-1">{delta}</div>}
      {sub && <div className="mt-1 text-xs text-slate-500 leading-relaxed">{sub}</div>}
      {children}
    </div>
  )
}

// Delta shows a relative change with an arrow. goodWhen says which direction helps:
// down for costs, up for savings. Changes below 2% read as steady.
export function Delta({ value, goodWhen = 'down', label, compact }: { value?: number | null; goodWhen?: 'up' | 'down'; label?: string; compact?: boolean }) {
  if (value === undefined || value === null || !isFinite(value)) return compact ? <span className="text-xs text-slate-300">—</span> : null
  const steady = Math.abs(value) < 0.02
  const good = steady ? null : (value < 0) === (goodWhen === 'down')
  const color = steady ? 'text-slate-500' : good ? 'text-brand-700' : 'text-amber-700'
  const Icon = steady ? Minus : value > 0 ? ArrowUpRight : ArrowDownRight
  return (
    <span className={`inline-flex items-center gap-0.5 text-xs font-semibold tabular-nums ${color}`}
      title={steady ? 'About the same as before' : good ? 'A change for the better' : 'A change worth a look'}>
      <Icon size={13} strokeWidth={2.5} />
      {steady ? (compact ? '0%' : 'steady') : `${Math.abs(value * 100).toFixed(0)}%`}
      {label && <span className="ml-1 font-normal text-slate-500">{label}</span>}
    </span>
  )
}

export interface Segment { key: string; label: string; value: number; color: string; hint?: string }

// SegmentBar is one horizontal stacked bar; widths are shares of `total`. Hovering a
// segment dims the others and names it in a tooltip, so a bar never needs a legend to be
// read. Pass onHover to show the details elsewhere instead.
export function SegmentBar({ segments, total, height = 'h-3', onHover }: { segments: Segment[]; total: number; height?: string; onHover?: (s: Segment | null) => void }) {
  const [active, setActive] = useState<string | null>(null)
  const shown = segments.filter(s => s.value > 0)
  const sum = shown.reduce((a, s) => a + s.value, 0)
  const share = (v: number) => (total > 0 ? (v / total) * 100 : 0)
  // Each segment's centre, for placing the tooltip.
  const centres: Record<string, number> = {}
  let at = 0
  for (const s of shown) {
    centres[s.key] = at + share(s.value) / 2
    at += share(s.value)
  }
  const hovered = shown.find(s => s.key === active)
  const leave = () => { setActive(null); onHover?.(null) }
  return (
    <div className="relative w-full" onMouseLeave={leave}>
      <div className={`flex w-full ${height} gap-[2px]`}>
        {shown.map((s, i) => (
          <div key={s.key}
            onMouseEnter={() => { setActive(s.key); onHover?.(s) }}
            className={`${i === 0 ? 'rounded-l' : ''} ${i === shown.length - 1 && sum >= total ? 'rounded-r' : ''} transition-opacity ${active && active !== s.key ? 'opacity-35' : ''}`}
            style={{ width: `${share(s.value)}%`, background: s.color, minWidth: 2 }} />
        ))}
        {total > 0 && sum < total && <div className="flex-1 rounded-r bg-slate-100" onMouseEnter={leave} />}
      </div>
      {hovered && !onHover && (
        <div role="tooltip"
          className={`pointer-events-none absolute bottom-full mb-2 z-30 whitespace-nowrap rounded-md bg-slate-900 px-2 py-1 text-[11px] text-white shadow-lg
            ${centres[hovered.key] < 20 ? '' : centres[hovered.key] > 80 ? '-translate-x-full' : '-translate-x-1/2'}`}
          style={{ left: `${centres[hovered.key] < 20 ? 0 : centres[hovered.key] > 80 ? 100 : centres[hovered.key]}%` }}>
          <span className="inline-block w-2 h-2 rounded-sm mr-1.5 align-middle" style={{ background: hovered.color }} />
          <b className="font-semibold">{hovered.label}</b>
          {hovered.hint && <span className="text-slate-300"> · {hovered.hint}</span>}
          {total > 0 && <span className="text-slate-400"> · {Math.round(share(hovered.value))}%</span>}
        </div>
      )}
    </div>
  )
}

// SegmentLegend names the colours of a set of bars.
export function SegmentLegend({ items }: { items: { label: string; color: string }[] }) {
  return (
    <div className="flex flex-wrap gap-x-4 gap-y-1 text-[11px] text-slate-500">
      {items.map(i => (
        <span key={i.label} className="inline-flex items-center gap-1.5"><span className="w-2.5 h-2.5 rounded-sm" style={{ background: i.color }} />{i.label}</span>
      ))}
    </div>
  )
}

// CostFunnel splits what the nodes cost into used, requested-but-idle and unrequested.
export function CostFunnel({ monthly, currency, cpu, memory }: {
  monthly: { provisioned: number; used: number; requested: number; unallocated: number }
  currency: string
  cpu: { capacity: number; requested: number; used: number }
  memory: { capacity: number; requested: number; used: number }
}) {
  const [hover, setHover] = useState<Segment | null>(null)
  const idle = Math.max(0, monthly.requested - monthly.used)
  const used = Math.min(monthly.used, monthly.requested)
  const segments: Segment[] = [
    { key: 'used', label: 'Used', value: used, color: funnelColors.used, hint: `${formatMoney(used, currency)}/mo — what pods actually use` },
    { key: 'idle', label: 'Requested, not used', value: idle, color: funnelColors.idle, hint: `${formatMoney(idle, currency)}/mo — reserved by requests but idle; right-sizing recovers it` },
    { key: 'unallocated', label: 'Not requested', value: monthly.unallocated, color: funnelColors.unallocated, hint: `${formatMoney(monthly.unallocated, currency)}/mo — node capacity no pod asked for, including the share the kubelet reserves` },
  ]
  const p = monthly.provisioned
  return (
    <div>
      <SegmentBar segments={segments} total={p} height="h-8" onHover={setHover} />
      <div className="mt-2 h-5 text-xs text-slate-600">{hover ? <span><b className="text-slate-800">{hover.label}</b> · {hover.hint}</span> : <span className="text-slate-400">Hover a segment for details</span>}</div>
      <div className="mt-2 grid grid-cols-3 gap-3">
        {segments.map(s => (
          <div key={s.key} className="min-w-0">
            <div className="flex items-center gap-1.5 text-xs text-slate-500"><span className="w-2.5 h-2.5 rounded-sm shrink-0" style={{ background: s.color }} />{s.label}</div>
            <div className="mt-0.5 text-lg font-bold text-slate-900">{formatMoney(s.value, currency)}<span className="text-xs font-medium text-slate-400">/mo</span></div>
            <div className="text-xs text-slate-500">{percent(p > 0 ? s.value / p : undefined)} of the node bill</div>
          </div>
        ))}
      </div>
      <div className="mt-5 space-y-3">
        <ResourceRow label="CPU" unit="cores" {...cpu} />
        <ResourceRow label="Memory" unit="GiB" {...memory} />
      </div>
    </div>
  )
}

function ResourceRow({ label, unit, capacity, requested, used }: { label: string; unit: string; capacity: number; requested: number; used: number }) {
  const fmt = (v: number) => (v >= 100 ? v.toFixed(0) : v >= 10 ? v.toFixed(1) : v.toFixed(2))
  const u = Math.min(used, requested)
  return (
    <div>
      <div className="flex justify-between text-xs mb-1">
        <span className="font-semibold text-slate-700">{label}</span>
        <span className="text-slate-500">
          {fmt(used)} used · {fmt(requested)} requested · {fmt(capacity)} {unit} on the nodes
        </span>
      </div>
      <SegmentBar total={capacity} segments={[
        { key: 'u', label: 'Used', value: u, color: funnelColors.used, hint: `${fmt(used)} ${unit}` },
        { key: 'i', label: 'Requested, not used', value: Math.max(0, requested - used), color: funnelColors.idle, hint: `${fmt(Math.max(0, requested - used))} ${unit}` },
        { key: 'n', label: 'Not requested', value: Math.max(0, capacity - requested), color: funnelColors.unallocated, hint: `${fmt(Math.max(0, capacity - requested))} ${unit}` },
      ]} />
    </div>
  )
}

const shortDate = (date: string) => new Date(`${date}T00:00:00Z`).toLocaleDateString(undefined, { month: 'short', day: 'numeric', timeZone: 'UTC' })

// CostTrend is daily cost as stacked columns: the biggest namespaces, then Other.
export function CostTrend({ days, top, currency, labelOf = n => n }: { days: HistoryDay[]; top: string[]; currency: string; labelOf?: (ns: string) => string }) {
  const [active, setActive] = useState<number | null>(null)
  const series = [...top.map((ns, i) => ({ key: ns, label: labelOf(ns), color: seriesColors[i] })), { key: OTHER, label: 'Other', color: otherColor }]
  const totals = days.map(d => series.reduce((a, s) => a + (d.namespaces[s.key] || 0), 0))
  const peak = Math.max(...totals, 0)
  const scale = niceMax(peak)
  const sums = series.map(s => days.reduce((a, d) => a + (d.namespaces[s.key] || 0), 0))
  const today = new Date().toISOString().slice(0, 10)
  return (
    <div>
      <div className="relative h-52 pl-12">
        {[1, 0.5, 0].map(f => (
          <div key={f} className="absolute left-12 right-0 border-t border-slate-100" style={{ bottom: `${f * 100}%` }}>
            <span className="absolute -left-12 -translate-y-1/2 w-10 text-right text-[10px] text-slate-400">{formatMoney(scale * f, currency)}</span>
          </div>
        ))}
        <div className="absolute inset-0 left-12 flex items-end gap-1" onMouseLeave={() => setActive(null)}>
          {days.map((d, i) => (
            <div key={d.date} className="relative flex-1 h-full flex flex-col justify-end cursor-default" onMouseEnter={() => setActive(i)}>
              <div className={`flex flex-col-reverse gap-[2px] ${active !== null && active !== i ? 'opacity-50' : ''}`}
                style={{ height: `${scale > 0 ? (totals[i] / scale) * 100 : 0}%` }}>
                {series.map((s, si) => {
                  const v = d.namespaces[s.key] || 0
                  if (v <= 0) return null
                  const last = series.slice(si + 1).every(x => !(d.namespaces[x.key] > 0))
                  return <div key={s.key} className={last ? 'rounded-t' : ''} style={{ flexGrow: v, background: s.color, minHeight: 1 }} />
                })}
              </div>
              {d.date === today && <div className="absolute -bottom-4 inset-x-0 text-center text-[9px] text-slate-400">today</div>}
              {active === i && (
                <div className={`absolute z-10 bottom-full mb-2 w-56 p-3 rounded-lg border border-slate-200 bg-white shadow-lg text-xs ${i > days.length / 2 ? 'right-0' : 'left-0'}`}>
                  <div className="font-semibold text-slate-800">{shortDate(d.date)}{d.hours < 23.5 && <span className="font-normal text-slate-400"> · {d.hours.toFixed(1)} h observed</span>}</div>
                  <div className="mt-2 space-y-1">
                    {series.filter(s => d.namespaces[s.key] > 0).map(s => (
                      <div key={s.key} className="flex items-center gap-1.5">
                        <span className="w-2 h-2 rounded-sm shrink-0" style={{ background: s.color }} />
                        <span className="truncate text-slate-600">{s.label}</span>
                        <span className="ml-auto font-medium text-slate-800">{formatMoney(d.namespaces[s.key], currency)}</span>
                      </div>
                    ))}
                  </div>
                  <div className="mt-2 pt-2 border-t border-slate-100 flex justify-between text-slate-600"><span>Requested by pods</span><b className="text-slate-800">{formatMoney(totals[i], currency)}</b></div>
                  <div className="flex justify-between text-slate-600"><span>Node bill</span><b className="text-slate-800">{formatMoney(d.provisioned, currency)}</b></div>
                  {d.saved > 0 && <div className="flex justify-between text-slate-600"><span>Saved by schedules</span><b className="text-brand-700">{formatMoney(d.saved, currency)}</b></div>}
                </div>
              )}
            </div>
          ))}
        </div>
      </div>
      <div className="flex justify-between pl-12 mt-5 text-[10px] text-slate-400">
        <span>{days.length ? shortDate(days[0].date) : ''}</span>
        <span>{days.length > 1 ? shortDate(days[days.length - 1].date) : ''}</span>
      </div>
      <div className="mt-3 flex flex-wrap gap-x-4 gap-y-1.5">
        {series.map((s, i) => sums[i] > 0 && (
          <span key={s.key} className="inline-flex items-center gap-1.5 text-xs text-slate-600">
            <span className="w-2.5 h-2.5 rounded-sm" style={{ background: s.color }} />{s.label}
            <span className="text-slate-400">{formatMoney(sums[i], currency)}</span>
          </span>
        ))}
      </div>
    </div>
  )
}

// niceMax rounds a chart maximum up to a round number close above it, so bars use most of
// the height.
function niceMax(v: number): number {
  if (v <= 0) return 1
  const p = Math.pow(10, Math.floor(Math.log10(v)))
  for (const m of [1, 1.2, 1.5, 2, 2.5, 3, 4, 5, 6, 8, 10]) if (v <= m * p) return m * p
  return 10 * p
}

export interface BarItem { key: string; label: ReactNode; used: number; idle: number; total: number; sub?: ReactNode; onClick?: () => void }

// BarList ranks items by cost. Each bar shows the used and the idle part of what they
// request; whatever else makes up the total is drawn as `rest`, or left blank without it.
export function BarList({ items, currency, max, rest }: { items: BarItem[]; currency: string; max?: number; rest?: { label: string; color: string } }) {
  const top = max ?? Math.max(...items.map(i => i.total), 0)
  return (
    <div className="space-y-2.5">
      {items.map(it => (
        <button key={it.key} type="button" onClick={it.onClick} disabled={!it.onClick}
          className="w-full text-left grid grid-cols-[minmax(0,10rem)_1fr_auto] items-center gap-3 rounded-md px-1 py-0.5 enabled:hover:bg-slate-50">
          <span className="min-w-0 truncate text-sm text-slate-700">{it.label}</span>
          <SegmentBar total={top} height="h-2.5" segments={[
            { key: 'u', label: 'Used', value: Math.min(it.used, it.total), color: funnelColors.used, hint: formatMoney(it.used, currency) + '/mo' },
            { key: 'i', label: 'Requested, not used', value: it.idle, color: funnelColors.idle, hint: formatMoney(it.idle, currency) + '/mo' },
            ...(rest ? [{ key: 'o', label: rest.label, value: Math.max(0, it.total - Math.min(it.used, it.total) - it.idle), color: rest.color,
              hint: formatMoney(Math.max(0, it.total - Math.min(it.used, it.total) - it.idle), currency) + '/mo' }] : []),
          ]} />
          <span className="text-right text-sm font-semibold text-slate-800 tabular-nums">{formatMoney(it.total, currency)}{it.sub && <span className="block text-[11px] font-normal text-slate-400">{it.sub}</span>}</span>
        </button>
      ))}
    </div>
  )
}

// Sparkline draws a small trend line; the last point is marked.
export function Sparkline({ values, width = 72, height = 20 }: { values: number[]; width?: number; height?: number }) {
  if (values.length < 2) return <span className="text-[11px] text-slate-300">—</span>
  const max = Math.max(...values), min = Math.min(...values)
  const span = max - min || 1
  const pts = values.map((v, i) => [(i / (values.length - 1)) * (width - 4) + 2, height - 3 - ((v - min) / span) * (height - 6)])
  const last = pts[pts.length - 1]
  return (
    <svg width={width} height={height} className="overflow-visible" aria-hidden>
      <polyline points={pts.map(p => p.join(',')).join(' ')} fill="none" stroke="#64748b" strokeWidth={1.5} strokeLinejoin="round" strokeLinecap="round" />
      <circle cx={last[0]} cy={last[1]} r={2.5} fill="#334155" stroke="#fff" strokeWidth={1.5} />
    </svg>
  )
}
