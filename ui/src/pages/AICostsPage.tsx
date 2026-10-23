import { useState, type ReactNode } from 'react'
import { AlertTriangle, Loader2 } from 'lucide-react'
import { Badge, PageHeader } from '../components/ui'
import { KpiTile } from '../components/finops/charts'
import { fetchAICosts, tokens, type AICosts } from '../lib/finops'
import { formatMoney } from '../lib/format'
import { errorMessage } from '../lib/api'
import { followLink } from '../lib/router'
import { usePolling } from '../lib/usePolling'

const subtitle = 'What applications pay LLM APIs, and what the models you serve yourself cost per million tokens'
const sourceLabel = { litellm: 'LiteLLM', opentelemetry: 'OpenTelemetry' } as const

function Table({ head, children, empty }: { head: string[]; children: ReactNode[]; empty: string }) {
  return (
    <div className="rounded-xl border border-slate-200 bg-white shadow-sm overflow-x-auto">
      <table className="w-full text-sm">
        <thead className="bg-slate-50 text-xs text-slate-500">
          <tr>{head.map((h, i) => <th key={h} className={`px-3 py-2.5 font-semibold whitespace-nowrap ${i === 0 ? 'text-left' : 'text-right'}`}>{h}</th>)}</tr>
        </thead>
        <tbody>
          {children.length === 0 ? <tr><td colSpan={head.length} className="px-3 py-8 text-center text-slate-400">{empty}</td></tr> : children}
        </tbody>
      </table>
    </div>
  )
}

const num = 'px-3 py-2.5 text-right tabular-nums text-slate-700'

// AICostsPage shows LLM API spend by team and model, and the cost per million tokens of
// the models vLLM serves in the cluster (GET /api/finops/ai).
export default function AICostsPage() {
  const [data, setData] = useState<AICosts | null>(null)
  const [error, setError] = useState<string | null>(null)
  usePolling(() => {
    fetchAICosts().then(d => { setData(d); setError(null) }).catch(e => setError(errorMessage(e)))
  }, 120000)

  if (!data) {
    return (
      <div className="p-8 max-w-[1600px] mx-auto">
        <PageHeader title="AI Costs" subtitle={subtitle} />
        {error ? <p className="text-sm text-rose-600">Could not load the AI costs: {error}</p>
          : <div className="flex items-center gap-2 text-sm text-slate-400"><Loader2 size={16} className="animate-spin" /> Loading…</div>}
      </div>
    )
  }
  const cur = data.currency
  const selfHostedMonthly = data.selfHosted.reduce((a, m) => a + m.monthlyCost, 0)
  const nothing = data.available && data.api.teams.length === 0 && data.selfHosted.length === 0

  return (
    <div className="p-8 max-w-[1600px] mx-auto">
      <PageHeader title="AI Costs" subtitle={subtitle} />

      {!data.available && (
        <div className="mb-4 flex gap-2 p-3 rounded-xl border border-amber-200 bg-amber-50 text-sm text-amber-900" data-testid="ai-unavailable">
          <AlertTriangle size={16} className="mt-0.5 shrink-0" />
          <span>
            {data.error ? <>Token counts could not be read from VictoriaMetrics: {data.error}</> : <>Token counts come from VictoriaMetrics, which is not connected. </>}
            {' '}Cost Deck reads LiteLLM's spend, OpenTelemetry GenAI token metrics from your applications, and vLLM's token counters from it.{' '}
            <a href="/settings/metrics" onClick={e => followLink(e, '/settings/metrics')} className="font-semibold underline">Connect VictoriaMetrics</a>
          </span>
        </div>
      )}
      {nothing && (
        <p className="mb-4 text-sm text-slate-500" data-testid="ai-empty">
          No LLM traffic in the last {data.windowDays} days: VictoriaMetrics has no LiteLLM, OpenTelemetry GenAI (<code>gen_ai_client_token_usage</code>) or vLLM metrics.
        </p>
      )}

      <div className="grid gap-4 sm:grid-cols-2 xl:grid-cols-4">
        <KpiTile label="LLM APIs" value={<>{formatMoney(data.api.monthly, cur)}<span className="text-sm font-medium text-slate-400">/mo</span></>}
          sub={data.api.sources.length ? `From ${data.api.sources.map(s => sourceLabel[s]).join(' and ')}, last ${data.windowDays} days` : `Last ${data.windowDays} days`} />
        <KpiTile label="Teams" value={String(new Set(data.api.teams.map(t => t.team)).size)} sub={`${data.api.models.length} model${data.api.models.length === 1 ? '' : 's'} in use`} />
        <KpiTile label="Self-hosted models" value={<>{formatMoney(selfHostedMonthly, cur)}<span className="text-sm font-medium text-slate-400">/mo</span></>}
          sub={`${data.selfHosted.length} model${data.selfHosted.length === 1 ? '' : 's'} served by vLLM`} />
        <KpiTile label="Unpriced models" value={String(data.api.unpriced.length)}
          status={data.api.unpriced.length ? { label: 'set their prices', tone: 'warning' } : undefined}
          sub={data.api.unpriced.length ? data.api.unpriced.join(', ') : 'Every model has a price'} />
      </div>

      {data.assistant && (
        <p className="mt-3 text-sm text-slate-600" data-testid="ai-assistant">
          Cost Deck's assistant ({data.assistant.model}) used {tokens(data.assistant.inputTokens + data.assistant.outputTokens)} tokens in {data.assistant.month}
          {data.assistant.monthlyTokenBudget > 0 && <> of its budget of {tokens(data.assistant.monthlyTokenBudget)}</>}
          {data.assistant.cost !== undefined && <>, about {formatMoney(data.assistant.cost, cur)}</>}.{' '}
          <a href="/settings/ai" onClick={e => followLink(e, '/settings/ai')} className="font-semibold underline">Budget</a>
        </p>
      )}

      {data.api.unpriced.length > 0 && (
        <p className="mt-3 text-sm text-amber-800" data-testid="ai-unpriced">
          {data.api.unpriced.join(', ')} {data.api.unpriced.length === 1 ? 'has' : 'have'} no price, so {data.api.unpriced.length === 1 ? 'its' : 'their'} tokens count for nothing.{' '}
          <a href="/settings/pricing" onClick={e => followLink(e, '/settings/pricing')} className="font-semibold underline">Set model prices</a>
        </p>
      )}

      <h2 className="mt-8 mb-3 text-base font-semibold text-slate-800">By team</h2>
      <Table head={['Team', 'Cost/mo', 'Input tokens/mo', 'Output tokens/mo']} empty="No LLM API traffic.">
        {data.api.teams.map(t => (
          <tr key={t.team + t.source} className="border-t border-slate-100 align-top">
            <td className="px-3 py-2.5">
              <div className="font-medium text-slate-800">{t.team === 'unassigned' ? <span className="text-slate-500">No team</span> : t.team} <Badge>{sourceLabel[t.source]}</Badge></div>
              {t.namespaces && <div className="text-[11px] text-slate-500">{t.namespaces.join(', ')}</div>}
            </td>
            <td className={`${num} font-semibold text-slate-800`}>{formatMoney(t.monthly, cur)}</td>
            <td className={num}>{tokens(t.inputTokens)}</td>
            <td className={num}>{tokens(t.outputTokens)}</td>
          </tr>
        ))}
      </Table>

      <h2 className="mt-8 mb-3 text-base font-semibold text-slate-800">By model</h2>
      <Table head={['Model', 'Cost/mo', 'Input tokens/mo', 'Output tokens/mo']} empty="No model in use.">
        {data.api.models.map(m => (
          <tr key={m.model} className="border-t border-slate-100">
            <td className="px-3 py-2.5 font-medium text-slate-800">{m.model || <span className="text-slate-500">unnamed</span>} {!m.priced && <Badge tone="warning">no price</Badge>}</td>
            <td className={`${num} font-semibold text-slate-800`}>{formatMoney(m.monthly, cur)}</td>
            <td className={num}>{tokens(m.inputTokens)}</td>
            <td className={num}>{tokens(m.outputTokens)}</td>
          </tr>
        ))}
      </Table>

      <h2 className="mt-8 mb-1 text-base font-semibold text-slate-800">Self-hosted models</h2>
      <p className="mb-3 text-xs text-slate-500">The run rate of the namespace serving a model (GPUs included), shared out over its models by tokens, against the tokens they served in the last {data.windowDays} days.</p>
      <Table head={['Model', 'Cost/mo', 'Tokens/hour', 'Per 1M tokens']} empty="No model served by vLLM.">
        {data.selfHosted.map(m => (
          <tr key={m.namespace + m.model} className="border-t border-slate-100 align-top">
            <td className="px-3 py-2.5"><div className="font-medium text-slate-800">{m.model}</div><div className="text-[11px] text-slate-500">{m.namespace}</div></td>
            <td className={`${num} font-semibold text-slate-800`}>{formatMoney(m.monthlyCost, cur)}</td>
            <td className={num}>{tokens(m.tokensPerHour)}</td>
            <td className={`${num} font-semibold text-slate-800`}>{m.costPer1M !== undefined ? formatMoney(m.costPer1M, cur) : '—'}</td>
          </tr>
        ))}
      </Table>
    </div>
  )
}
