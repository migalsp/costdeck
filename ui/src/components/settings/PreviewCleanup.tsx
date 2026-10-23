import { useEffect, useState } from 'react'
import { AlertTriangle, ShieldCheck, Trash2 } from 'lucide-react'
import { apiError, errorMessage, getJSON } from '../../lib/api'
import { TTL_CHOICES, WARN_CHOICES, deletes, type CleanupForm, type TTLSettings, type TTLStatus } from '../../lib/cleanup'
import { when } from '../../lib/schedule'
import { toast } from '../../lib/toast'
import { Badge, Button } from '../ui'
import { SettingsCard, inputClass, labelClass } from './fields'

// PreviewCleanup edits spec.namespaceTTL and lists what it would delete with the values in
// the form, before they are saved. Turning deleting on shows what it means and asks for a
// confirmation; the section's Save stores the form.
export default function PreviewCleanup({ value, onChange, saved }: { value: CleanupForm; onChange: (f: CleanupForm) => void; saved?: TTLSettings }) {
  const [status, setStatus] = useState<TTLStatus | null>(null)
  const [loadError, setLoadError] = useState<string | null>(null)
  const [busy, setBusy] = useState<string | null>(null)
  const set = (patch: Partial<CleanupForm>) => onChange({ ...value, ...patch })
  const selector = value.selector.trim()

  // The plan follows the form: unsaved selector and time to live.
  useEffect(() => {
    const t = setTimeout(() => {
      getJSON<TTLStatus>(`/api/namespace-ttl?selector=${encodeURIComponent(selector)}&ttl=${encodeURIComponent(value.ttl)}`)
        .then(s => { setStatus(s); setLoadError(null) })
        .catch(e => setLoadError(errorMessage(e)))
    }, 300)
    return () => clearTimeout(t)
  }, [selector, value.ttl])

  const extend = async (ns: string) => {
    setBusy(ns)
    try {
      const res = await fetch(`/api/namespace-ttl/${encodeURIComponent(ns)}/extend`, { method: 'POST', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify({ by: '3d' }) })
      if (!res.ok) throw new Error(await apiError(res))
      const e = await res.json()
      toast.success(`${ns} is kept until ${when(e.expiresAt)}`)
      setStatus(s => s && { ...s, namespaces: s.namespaces.map(n => (n.namespace === ns ? e : n)).sort((a, b) => (a.protected ? 1 : 0) - (b.protected ? 1 : 0) || a.expiresAt.localeCompare(b.expiresAt)) })
    } catch (err) {
      toast.error(`Could not keep ${ns}: ${errorMessage(err)}`)
    } finally {
      setBusy(null)
    }
  }

  const deleting = deletes(value)
  const newlyDeleting = deleting && !(saved && deletes(saved))
  const due = status?.namespaces.filter(n => !n.protected) || []
  const kept = status?.namespaces.filter(n => n.protected) || []

  return (
    <>
      <SettingsCard logo={<div className="w-10 h-10 rounded-xl bg-rose-50 flex items-center justify-center"><Trash2 size={20} className="text-rose-500" /></div>}
        title="Delete preview namespaces" subtitle="Off by default. Deletes whole namespaces, with their volumes and data."
        enabled={value.enabled} onToggle={v => set({ enabled: v, understood: false })}>
        {status && !status.permitted && (
          <p className="text-xs text-amber-800 bg-amber-50 border border-amber-200 rounded-lg px-3 py-2" role="note">
            The operator may not delete namespaces yet: install the chart with <code>--set namespaceTTL.enabled=true</code>. Until then nothing is deleted.
          </p>
        )}
        <div className="grid grid-cols-1 sm:grid-cols-[1fr_9rem_10rem] gap-3">
          <div>
            <label className={labelClass} htmlFor="ttl-selector">Namespaces with the label</label>
            <input id="ttl-selector" className={`${inputClass} font-mono`} value={value.selector} onChange={e => set({ selector: e.target.value })} placeholder="env=preview" />
          </div>
          <div>
            <label className={labelClass} htmlFor="ttl-ttl">Delete after</label>
            <select id="ttl-ttl" className={inputClass} value={value.ttl} onChange={e => set({ ttl: e.target.value })}>
              {(TTL_CHOICES.includes(value.ttl) ? TTL_CHOICES : [value.ttl, ...TTL_CHOICES]).map(d => <option key={d} value={d}>{d.replace(/d$/, d === '1d' ? ' day' : ' days')}</option>)}
            </select>
          </div>
          <div>
            <label className={labelClass} htmlFor="ttl-warn">Warn</label>
            <select id="ttl-warn" className={inputClass} value={value.warnBefore} onChange={e => set({ warnBefore: e.target.value })}>
              {(WARN_CHOICES.some(w => w.value === value.warnBefore) ? WARN_CHOICES : [{ value: value.warnBefore, label: `${value.warnBefore} before` }, ...WARN_CHOICES]).map(w => <option key={w.value} value={w.value}>{w.label}</option>)}
            </select>
          </div>
        </div>
        <p className="text-[11px] text-slate-400">
          Counted from when the namespace was created. CI can set its own time with the label <code>costdeck.io/ttl=3d</code>, or keep a namespace with <code>costdeck.io/ttl=off</code>.
          Warnings go to the notification channels that take alerts.
        </p>
        <label className="flex items-start gap-2 text-sm text-slate-600">
          <input type="checkbox" className="mt-0.5 accent-brand-600" checked={value.dryRun} onChange={e => set({ dryRun: e.target.checked, understood: false })} />
          <span><b className="text-slate-800">Dry run</b>: only show below what would be deleted and when; delete and send nothing.</span>
        </label>
        {value.enabled && (
          <div className={`rounded-xl border px-4 py-3 text-sm ${deleting ? 'border-rose-200 bg-rose-50 text-rose-800' : 'border-amber-200 bg-amber-50 text-amber-800'}`} role="alert">
            <div className="flex gap-2 font-semibold"><AlertTriangle size={16} className="mt-0.5 shrink-0" />{deleting ? 'Namespaces will be deleted' : 'Dry run: nothing is deleted'}</div>
            <ul className="mt-1.5 ml-6 list-disc space-y-0.5 text-[13px]">
              <li>Every namespace with this label is deleted {value.ttl.replace(/d$/, ' days').replace(/^1 days$/, '1 day')} after it was created, with everything in it: workloads, volumes and their data, secrets.</li>
              <li>A deleted namespace cannot be restored.</li>
              <li>Never deleted: <code>kube-*</code>, <code>default</code>, Cost Deck&apos;s own namespace, namespaces a schedule lists by name, and those labelled <code>costdeck.io/ttl=off</code>.</li>
            </ul>
            {newlyDeleting && (
              <label className="mt-2 flex items-start gap-2 font-medium">
                <input type="checkbox" className="mt-0.5 accent-rose-600" checked={value.understood} onChange={e => set({ understood: e.target.checked })} />
                <span>I understand that deleted namespaces, with their data, cannot be restored</span>
              </label>
            )}
          </div>
        )}
      </SettingsCard>

      <div className="bg-white rounded-xl border border-slate-200 shadow-sm p-6">
        <h3 className="font-bold text-slate-800">{value.enabled && !value.dryRun ? 'Deleted next' : 'Would be deleted'}</h3>
        <p className="text-xs text-slate-500 mt-0.5 mb-4">With the label and time above{selector ? '' : ' (enter a label first)'}.</p>
        {loadError && <p className="text-sm text-rose-600">{loadError}</p>}
        {status?.error && <p className="text-sm text-rose-600">{status.error}</p>}
        {selector && status && !status.error && due.length === 0 && <p className="text-sm text-slate-500">No namespace with this label would be deleted.</p>}
        {due.length > 0 && (
          <div className="overflow-x-auto">
            <table className="w-full text-sm">
              <thead><tr className="text-left text-[11px] uppercase tracking-wider text-slate-400"><th className="py-1.5 pr-3">Namespace</th><th className="pr-3">Created</th><th className="pr-3">Deleted</th><th /></tr></thead>
              <tbody>
                {due.map(n => (
                  <tr key={n.namespace} className="border-t border-slate-100">
                    <td className="py-2 pr-3 font-mono">{n.namespace}</td>
                    <td className="pr-3 text-slate-500">{when(n.createdAt)}</td>
                    <td className="pr-3">
                      {new Date(n.expiresAt) <= new Date() ? 'on the next run' : when(n.expiresAt)}
                      {n.source === 'extended' && <span className="ml-2"><Badge tone="info">extended</Badge></span>}
                      {n.source === 'label' && <span className="ml-2"><Badge tone="neutral">its label</Badge></span>}
                      {n.warned && <span className="ml-2"><Badge tone="warning">warned</Badge></span>}
                    </td>
                    <td className="text-right">
                      <Button size="sm" onClick={() => extend(n.namespace)} disabled={busy === n.namespace || !status?.permitted}>Keep 3 more days</Button>
                    </td>
                  </tr>
                ))}
              </tbody>
            </table>
          </div>
        )}
        {kept.length > 0 && (
          <div className="mt-4 text-xs text-slate-500">
            <div className="flex items-center gap-1.5 font-semibold text-slate-600 mb-1"><ShieldCheck size={14} /> Never deleted</div>
            {kept.map(n => <div key={n.namespace}><span className="font-mono">{n.namespace}</span>: {n.protected}</div>)}
          </div>
        )}
        {(status?.deleted.length || 0) > 0 && (
          <div className="mt-4 text-xs text-slate-500">
            <div className="font-semibold text-slate-600 mb-1">Deleted</div>
            {status!.deleted.slice(0, 10).map(d => <div key={d.namespace + d.deletedAt}><span className="font-mono">{d.namespace}</span> on {when(d.deletedAt)}</div>)}
          </div>
        )}
      </div>
    </>
  )
}
