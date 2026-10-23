import { useState } from 'react'
import { Plus, Trash2 } from 'lucide-react'
import { apiError, errorMessage } from '../../lib/api'
import { clustersRequest, type ClusterForm } from '../../lib/clusters'
import { Button } from '../ui'
import { SecretInput, TestButton, inputClass, labelClass } from './fields'

// ClustersSettings lists the other Cost Deck installations the Clusters page reads, each
// with a viewer API token of that installation. The section's Save stores the list.
export default function ClustersSettings({ value, onChange }: { value: ClusterForm[]; onChange: (f: ClusterForm[]) => void }) {
  const [testing, setTesting] = useState<number | null>(null)
  const [result, setResult] = useState<{ i: number; ok: boolean; text: string } | null>(null)
  const set = (i: number, patch: Partial<ClusterForm>) => onChange(value.map((c, j) => (j === i ? { ...c, ...patch } : c)))

  const test = async (i: number) => {
    setTesting(i)
    setResult(null)
    try {
      const res = await fetch('/api/settings/providers/cluster/test', { method: 'POST', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify(clustersRequest([value[i]])[0]) })
      if (!res.ok) throw new Error(await apiError(res))
      const d = await res.json()
      setResult({ i, ok: !!d.connected, text: d.connected ? d.message : d.error || 'Could not reach it' })
    } catch (e) {
      setResult({ i, ok: false, text: errorMessage(e) })
    } finally {
      setTesting(null)
    }
  }

  return (
    <div className="space-y-4">
      <p className="text-sm text-slate-600">
        On each other installation, create a <b>viewer</b> API token (Settings → API tokens) and add it here. The Clusters page then shows that cluster next to this one.
      </p>
      {value.map((c, i) => (
        <div key={i} className="bg-white rounded-xl border border-slate-200 shadow-sm p-5 space-y-3">
          <div className="grid grid-cols-1 sm:grid-cols-[12rem_1fr] gap-3">
            <div>
              <label className={labelClass} htmlFor={`cluster-name-${i}`}>Name</label>
              <input id={`cluster-name-${i}`} className={inputClass} value={c.name} onChange={e => set(i, { name: e.target.value })} placeholder="prod-eu" />
            </div>
            <div>
              <label className={labelClass} htmlFor={`cluster-url-${i}`}>Dashboard URL</label>
              <input id={`cluster-url-${i}`} className={`${inputClass} font-mono`} value={c.url} onChange={e => set(i, { url: e.target.value })} placeholder="https://costdeck.eu.example.com" />
            </div>
          </div>
          <div>
            <label className={labelClass}>Viewer API token {c.hasToken && <span className="ml-2 text-emerald-500 normal-case font-medium text-xs">✓ Configured</span>}</label>
            <SecretInput value={c.token} onChange={v => set(i, { token: v })} label={`API token of ${c.name || 'the cluster'}`}
              placeholder={c.hasToken ? '•••••••• (leave empty to keep it)' : 'cdk_…'} />
          </div>
          <label className="flex items-center gap-2 text-sm text-slate-600">
            <input type="checkbox" className="accent-brand-600" checked={c.skipTlsVerify} onChange={e => set(i, { skipTlsVerify: e.target.checked })} />
            Accept its certificate without checking it
          </label>
          <div className="flex flex-wrap items-center justify-between gap-3">
            <TestButton onTest={() => test(i)} testing={testing === i} result={result?.i === i ? result : null} />
            <Button size="sm" variant="ghost" onClick={() => onChange(value.filter((_, j) => j !== i))} icon={<Trash2 size={14} />}>Remove</Button>
          </div>
        </div>
      ))}
      <Button onClick={() => onChange([...value, { name: '', url: '', token: '', skipTlsVerify: false, hasToken: false }])} icon={<Plus size={16} />}>Add a cluster</Button>
    </div>
  )
}
