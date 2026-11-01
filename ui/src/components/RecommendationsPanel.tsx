import { Fragment, useState, type ReactNode } from 'react'
import { Lightbulb, ArrowRight, AlertTriangle, Copy, Check, Info, GitPullRequest, ExternalLink, Undo2, Wand2, Coffee } from 'lucide-react'
import { apiError, errorMessage, fetchRecommendations, getJSON } from '../lib/api'
import { useAuth } from '../lib/auth'
import { describeState, describeValues, latest, type RightsizingChanges } from '../lib/rightsizing'
import { toast } from '../lib/toast'
import { Button } from './ui'
import { formatMoney } from '../lib/format'
import type { AdviceAction, ContainerAdvice, JVMAdvice, JVMFinding, Recommendations, ResourceAdvice, WorkloadAdvice } from '../lib/types'
import { usePolling } from '../lib/usePolling'

const actionable: AdviceAction[] = ['reduce', 'increase', 'set']

const hasRequestAdvice = (c: ContainerAdvice) => actionable.includes(c.cpu.action) || actionable.includes(c.memory.action)

const jvmNeedsAttention = (c: ContainerAdvice) => !!c.jvm && c.jvm.findings.some(f => f.severity !== 'note')

const needsAttention = (c: ContainerAdvice) => hasRequestAdvice(c) || jvmNeedsAttention(c) || !!c.cpu.held || !!c.memory.held

// The values to paste: recommended where there is advice, the current request otherwise.
const yamlFor = (w: WorkloadAdvice, c: ContainerAdvice) => {
  const value = (r: ResourceAdvice) => (actionable.includes(r.action) ? r.recommended : r.request) || ''
  const lines = [`# ${w.kind.toLowerCase()}/${w.name}, container ${c.name}`, 'resources:', '  requests:']
  if (value(c.cpu)) lines.push(`    cpu: ${value(c.cpu)}`)
  if (value(c.memory)) lines.push(`    memory: ${value(c.memory)}`)
  return lines.join('\n')
}

function Change({ advice }: { advice: ResourceAdvice }) {
  if (advice.action === 'unknown') return <span className="text-slate-300">no data</span>
  if (advice.action === 'keep' && advice.held) {
    return <span className="text-amber-700" title={`Not reduced: ${advice.held}`}>{advice.request} · held: {advice.held}</span>
  }
  if (advice.action === 'keep') return <span className="text-slate-400">{advice.request} · fits</span>
  const tone = advice.action === 'increase' ? 'text-amber-600' : 'text-emerald-600'
  return (
    <span className="inline-flex items-center gap-1.5 whitespace-nowrap">
      <span className={advice.request ? 'text-slate-400 line-through' : 'text-slate-400 italic'}>{advice.request || 'unset'}</span>
      <ArrowRight size={12} className="text-slate-300" />
      <span className={`font-bold ${tone}`}>{advice.recommended}</span>
      <span className="text-[10px] text-slate-400">uses {advice.observed}</span>
    </span>
  )
}

function CopyButton({ text, label = 'Copy as YAML' }: { text: string; label?: string }) {
  const [copied, setCopied] = useState(false)
  return (
    <button
      onClick={() => {
        navigator.clipboard?.writeText(text).then(() => {
          setCopied(true)
          setTimeout(() => setCopied(false), 1500)
        })
      }}
      className="p-1.5 rounded-lg text-slate-400 hover:text-brand-600 hover:bg-brand-50 transition-colors"
      title={`${label}:\n\n${text}`}
      aria-label={label}
    >
      {copied ? <Check size={14} className="text-emerald-500" /> : <Copy size={14} />}
    </button>
  )
}

const findingTone: Record<JVMFinding['severity'], { icon: ReactNode; label: string }> = {
  risk: { icon: <AlertTriangle size={12} className="text-rose-600 shrink-0 mt-0.5" />, label: 'Risk' },
  waste: { icon: <Lightbulb size={12} className="text-emerald-600 shrink-0 mt-0.5" />, label: 'Saving' },
  note: { icon: <Info size={12} className="text-slate-400 shrink-0 mt-0.5" />, label: 'Note' },
}

function JvmDetails({ jvm, currency }: { jvm: JVMAdvice; currency: string }) {
  return (
    <div className="rounded-lg border border-orange-100 bg-orange-50/50 px-3 py-2 text-xs text-slate-600 space-y-1.5" aria-label="JVM advice">
      <div className="flex flex-wrap items-center gap-x-3 gap-y-1 text-slate-500">
        <span>Heap {jvm.heap ?? 'unknown'}: {jvm.heapSource}</span>
        {(jvm.options?.length ?? 0) > 0 && <code className="rounded bg-slate-100 px-1 font-mono text-slate-700">{jvm.options!.join(' ')}</code>}
        {jvm.heapAfterGC && <span>after garbage collection up to {jvm.heapAfterGC}{jvm.heapUsed ? `, in use up to ${jvm.heapUsed}` : ''}</span>}
      </div>
      {jvm.findings.length > 0 && (
        <ul className="space-y-1">
          {jvm.findings.map(f => (
            <li key={f.code} className="flex items-start gap-1.5">
              {findingTone[f.severity].icon}
              <span><span className="sr-only">{findingTone[f.severity].label}: </span>{f.message}</span>
            </li>
          ))}
        </ul>
      )}
      {jvm.recommendedOptions && (
        <div className="flex flex-wrap items-center gap-2">
          <span className="font-semibold text-slate-700">Recommended heap</span>
          <code className="rounded bg-slate-100 px-1 font-mono text-slate-700">{jvm.recommendedOptions}</code>
          <span>with {jvm.recommendedMemory} of memory as request and limit</span>
          {(jvm.monthlySavings ?? 0) >= 0.01 && <span className="font-bold text-emerald-600">~{formatMoney(jvm.monthlySavings!, currency)}/mo</span>}
          <CopyButton text={jvm.recommendedOptions} label="Copy the JVM options" />
        </div>
      )}
    </div>
  )
}

// RecommendationsPanel shows what a namespace could give back, from observed demand. By
// default it is advice only. When an admin allows it, operators apply the advice to a
// workload (watched, and put back if its new pods fail) or open a pull request with it.
export default function RecommendationsPanel({ namespace }: { namespace: string }) {
  const { can } = useAuth()
  const [report, setReport] = useState<Recommendations | null>(null)
  const [unavailable, setUnavailable] = useState(false)
  const [changes, setChanges] = useState<RightsizingChanges | null>(null)
  const [confirming, setConfirming] = useState<string | null>(null)
  const [busy, setBusy] = useState<string | null>(null)

  const loadAdvice = () => fetchRecommendations(namespace)
    .then(r => { setReport(r); setUnavailable(r === null) })
    .catch(() => setUnavailable(true))
  const loadChanges = () => getJSON<RightsizingChanges>(`/api/rightsizing/changes?namespace=${encodeURIComponent(namespace)}`).then(setChanges).catch(() => setChanges(null))
  usePolling(loadAdvice, 5 * 60 * 1000, namespace)
  usePolling(loadChanges, 30 * 1000, namespace)

  // act runs one of the actions on a workload and reports how it went.
  const act = async (w: WorkloadAdvice, action: 'apply' | 'pull-request' | 'revert') => {
    const key = `${w.kind}/${w.name}`
    setBusy(key)
    setConfirming(null)
    try {
      const res = await fetch(`/api/namespaces/${encodeURIComponent(namespace)}/rightsizing/${action}`, {
        method: 'POST', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify({ kind: w.kind, name: w.name }),
      })
      if (!res.ok) throw new Error(await apiError(res))
      const c = await res.json()
      toast.success(action === 'apply' ? `Applied the advice to ${w.name}; its new pods are watched`
        : action === 'revert' ? `Put the old requests of ${w.name} back` : `Opened a pull request for ${w.name}`)
      if (action === 'pull-request' && c.pullRequest) window.open(c.pullRequest, '_blank', 'noopener')
      await Promise.all([loadChanges(), loadAdvice()])
    } catch (e) {
      toast.error(`${w.name}: ${errorMessage(e)}`)
    } finally {
      setBusy(null)
    }
  }
  const canAct = can('operator') && !!changes && (changes.applyEnabled || !!changes.repository)

  if (unavailable) {
    return (
      <div className="bg-white rounded-xl border border-slate-200 p-5 mb-6 flex items-center gap-3 text-sm text-slate-500">
        <Info size={16} className="text-slate-400" />
        Right-sizing advice appears once usage data is available for this namespace (metrics-server or VictoriaMetrics).
      </div>
    )
  }
  if (!report) return <div className="h-24 bg-slate-100 rounded-xl animate-pulse mb-6" />

  const rows = report.workloads.flatMap(w => w.containers.filter(needsAttention).map(c => ({ w, c })))
  const reducible = rows.filter(({ c }) => c.cpu.action === 'reduce' || c.memory.action === 'reduce').length
  const underRequested = rows.filter(({ c }) => c.cpu.action === 'increase' || c.memory.action === 'increase').length

  return (
    <div className="bg-white rounded-xl border border-slate-200 shadow-sm mb-6 overflow-hidden">
      <div className="p-5 flex flex-wrap items-start justify-between gap-4 border-b border-slate-100">
        <div className="flex items-start gap-3">
          <div className="p-2 rounded-lg bg-emerald-50 text-emerald-600"><Lightbulb size={20} /></div>
          <div>
            <h3 className="font-bold text-slate-800">What can be reduced</h3>
            <p className="text-xs text-slate-500 mt-0.5 max-w-3xl">
              {report.basis}{' '}
              {changes?.applyEnabled
                ? `Apply sets the values on a workload and watches its new pods for ${changes.guardMinutes} minutes, putting the old requests back if one fails.`
                : changes?.repository ? `Pull request proposes the values in ${changes.repository}.` : 'Nothing is changed automatically: apply the values through your deployment pipeline.'}
            </p>
            {!report.historical && (
              <p className="text-xs text-amber-600 mt-1">A single reading can miss peaks. Treat these values as a starting point.</p>
            )}
            {report.warning && <p className="text-xs text-rose-600 mt-1">{report.warning}</p>}
          </div>
        </div>
        <div className="text-right">
          <div className="text-2xl font-bold text-emerald-600">
            {report.monthlySavings >= 0.01 ? `~${formatMoney(report.monthlySavings, report.currency)}` : formatMoney(0, report.currency)}
            <span className="text-sm font-bold text-slate-400">/mo</span>
          </div>
          {(report.javaMonthlySavings ?? 0) >= 0.01 && (
            <div className="text-[11px] font-semibold text-emerald-700">+ ~{formatMoney(report.javaMonthlySavings!, report.currency)}/mo by lowering Java heaps</div>
          )}
          <div className="text-[11px] text-slate-400">
            {reducible} container{reducible === 1 ? '' : 's'} over-requested
            {underRequested > 0 && <> · <span className="text-amber-600">{underRequested} under-requested</span></>}
          </div>
        </div>
      </div>

      {report.workloads.length === 0 ? (
        <div className="p-5 text-sm text-slate-500">No Deployments, StatefulSets or Argo Rollouts here, so there is nothing to right-size.</div>
      ) : rows.length === 0 ? (
        <div className="p-5 text-sm text-slate-500">Requests match observed usage. Nothing to reduce right now.</div>
      ) : (
        <div className="overflow-x-auto">
          <table className="w-full text-left text-sm">
            <thead className="bg-slate-50 text-[11px] uppercase tracking-wider text-slate-400">
              <tr>
                <th className="px-5 py-2 font-bold">Workload</th>
                <th className="px-3 py-2 font-bold">CPU request</th>
                <th className="px-3 py-2 font-bold">Memory request</th>
                <th className="px-3 py-2 font-bold text-right">Saving</th>
                <th className="px-3 py-2" />
              </tr>
            </thead>
            <tbody className="divide-y divide-slate-100">
              {rows.map(({ w, c }) => (
                <Fragment key={`${w.kind}/${w.name}/${c.name}`}>
                <tr className="hover:bg-slate-50/60">
                  <td className="px-5 py-2.5">
                    <div className="font-bold text-slate-700">{w.name}{w.containers.length > 1 && <span className="font-medium text-slate-400"> / {c.name}</span>}</div>
                    <div className="text-[10px] text-slate-400 uppercase tracking-wider">
                      {w.kind} · {w.replicas} replica{w.replicas === 1 ? '' : 's'}
                      {w.policy && (
                        <span className="ml-2 normal-case tracking-normal text-slate-500" title={w.applyForbidden ? 'Its RightsizingPolicy forbids applying the advice or proposing it' : 'Its RightsizingPolicy bounds this advice'}>
                          policy {w.policy}{w.applyForbidden ? ' · apply forbidden' : ''}
                        </span>
                      )}
                      {c.jvm && (
                        <span className="ml-2 inline-flex items-center gap-1 text-orange-700 normal-case tracking-normal" title={c.jvm.heapSource}>
                          <Coffee size={10} /> Java{c.jvm.heap ? ` · heap ${c.jvm.heap}` : ''}
                        </span>
                      )}
                      {(c.cpu.action === 'increase' || c.memory.action === 'increase') && (
                        <span className="ml-2 inline-flex items-center gap-1 text-amber-600 normal-case tracking-normal" title="It uses more than it requests, so it competes for capacity it never reserved: CPU throttling, or eviction under memory pressure.">
                          <AlertTriangle size={10} /> uses more than requested
                        </span>
                      )}
                    </div>
                  </td>
                  <td className="px-3 py-2.5"><Change advice={c.cpu} /></td>
                  <td className="px-3 py-2.5"><Change advice={c.memory} /></td>
                  <td className="px-3 py-2.5 text-right font-bold text-emerald-600 whitespace-nowrap">
                    {w.monthlySavings >= 0.01 && w.containers.indexOf(c) === w.containers.findIndex(hasRequestAdvice)
                      ? `${formatMoney(w.monthlySavings, report.currency)}/mo` : ''}
                  </td>
                  <td className="px-3 py-2.5 text-right whitespace-nowrap">
                    {canAct && !w.applyForbidden && w.containers.indexOf(c) === w.containers.findIndex(hasRequestAdvice) && (() => {
                      const key = `${w.kind}/${w.name}`
                      const ch = latest(changes, w.kind, w.name)
                      const live = ch && (ch.state === 'applied' || ch.state === 'kept')
                      if (confirming === key) {
                        return (
                          <span className="inline-flex items-center gap-1.5 text-xs text-slate-600">
                            Pods of {w.name} restart.
                            <Button size="sm" variant="primary" onClick={() => act(w, 'apply')}>Apply</Button>
                            <Button size="sm" variant="ghost" onClick={() => setConfirming(null)}>Cancel</Button>
                          </span>
                        )
                      }
                      return (
                        <span className="inline-flex items-center gap-1.5 mr-1">
                          {changes!.applyEnabled && !live && <Button size="sm" onClick={() => setConfirming(key)} disabled={busy === key} icon={<Wand2 size={13} />}>Apply</Button>}
                          {changes!.repository && <Button size="sm" onClick={() => act(w, 'pull-request')} disabled={busy === key} icon={<GitPullRequest size={13} />}>Pull request</Button>}
                        </span>
                      )
                    })()}
                    {hasRequestAdvice(c) && <CopyButton text={yamlFor(w, c)} />}
                  </td>
                </tr>
                {c.jvm && (c.jvm.findings.length > 0 || c.jvm.recommendedOptions) && (
                  <tr>
                    <td colSpan={5} className="px-5 pb-3 pt-0"><JvmDetails jvm={c.jvm} currency={report.currency} /></td>
                  </tr>
                )}
                </Fragment>
              ))}
            </tbody>
          </table>
        </div>
      )}

      {(changes?.changes.length || 0) > 0 && (
        <div className="border-t border-slate-100 px-5 py-4">
          <h4 className="text-xs font-bold uppercase tracking-wider text-slate-400 mb-2">Changes</h4>
          <ul className="space-y-2" aria-label="Right-sizing changes">
            {changes!.changes.map(c => (
              <li key={`${c.kind}/${c.name}/${c.appliedAt}`} className="flex flex-wrap items-baseline gap-x-3 gap-y-1 text-sm">
                <span className="font-semibold text-slate-700">{c.kind} {c.name}</span>
                <span className="text-slate-500">{describeValues(c)}</span>
                <span className={`text-xs ${c.state === 'reverted' ? 'text-amber-700' : 'text-slate-400'}`}>{describeState(c, changes!.guardMinutes)}</span>
                <span className="ml-auto inline-flex gap-2">
                  {c.pullRequest && <a href={c.pullRequest} target="_blank" rel="noopener noreferrer" className="inline-flex items-center gap-1 text-xs font-semibold text-brand-700 hover:underline">Pull request <ExternalLink size={11} /></a>}
                  {can('operator') && (c.state === 'applied' || c.state === 'kept') && (
                    <Button size="sm" variant="ghost" icon={<Undo2 size={13} />} disabled={busy === `${c.kind}/${c.name}`}
                      onClick={() => act({ kind: c.kind, name: c.name } as WorkloadAdvice, 'revert')}>Put back</Button>
                  )}
                </span>
              </li>
            ))}
          </ul>
        </div>
      )}
    </div>
  )
}
