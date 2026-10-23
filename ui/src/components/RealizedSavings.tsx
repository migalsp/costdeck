import { useState } from 'react'
import { getJSON } from '../lib/api'
import { formatMoney } from '../lib/format'
import { nodeVerdict } from '../lib/realized'
import type { RealizedSavings as Check } from '../lib/types'
import { usePolling } from '../lib/usePolling'
import { Badge } from './ui'

// RealizedSavings shows whether the nodes a schedule's workloads ran on went away after it
// scaled them down, and what keeps the others: the estimate counts requests, the bill
// counts nodes.
export default function RealizedSavings({ kind, name }: { kind: 'group' | 'config'; name: string }) {
  const [check, setCheck] = useState<Check | null>(null)
  const [error, setError] = useState<string | null>(null)
  usePolling(() => {
    getJSON<Check>(`/api/scaling/${kind === 'group' ? 'groups' : 'configs'}/${name}/realized`)
      .then(c => { setCheck(c); setError(null) })
      .catch(e => setError(e instanceof Error ? e.message : String(e)))
  }, 60_000, `${kind}/${name}`)

  if (error) return <p className="text-sm text-rose-600">Could not check the nodes: {error}</p>
  if (!check || check.state === 'none') return null
  const settled = check.state === 'checked'
  const kept = check.nodes.filter(n => !n.gone)

  return (
    <section data-testid="realized-savings">
      <h3 className="text-sm font-semibold text-slate-800 mb-1">Realized savings</h3>
      {check.nodes.length === 0
        ? <p className="text-sm text-slate-500">The workloads ran on no node when the scale-down began, so there are no nodes to free.</p>
        : <p className="text-sm text-slate-600">
            {settled ? '' : `Autoscalers get ${check.settleMinutes} minutes to act. So far: `}
            {check.gone} of {check.nodes.length} node{check.nodes.length === 1 ? '' : 's'} the workloads ran on {check.gone === 1 ? 'was' : 'were'} removed,
            saving <b>{formatMoney(check.realizedHourly, check.currency)}/h</b> of the estimated {formatMoney(check.estimatedHourly, check.currency)}/h.
            {settled && check.estimatedHourly > 0 && check.realizedHourly === 0 && ' The freed requests do not lower the bill until nodes go.'}
          </p>}
      {kept.length > 0 && (
        <ul className="mt-2 space-y-1.5">
          {kept.map(n => {
            const v = nodeVerdict(n, settled)
            return (
              <li key={n.name} className="text-sm text-slate-600">
                <span className="font-medium text-slate-800">{n.name}</span> <span className="text-xs text-slate-400">{formatMoney(n.hourlyCost, check.currency)}/h</span>{' '}
                <Badge tone={v.tone}>{v.text}</Badge>
                {n.blockers.length > 0 && (
                  <ul className="mt-0.5 ml-4 list-disc text-xs text-slate-500">
                    {n.blockers.map((b, i) => <li key={i}>{b.pod && <b className="font-medium text-slate-700">{b.pod}: </b>}{b.detail}</li>)}
                  </ul>
                )}
              </li>
            )
          })}
        </ul>
      )}
    </section>
  )
}
