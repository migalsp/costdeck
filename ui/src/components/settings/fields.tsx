import { useState, type ReactNode } from 'react'
import { CheckCircle2, ExternalLink, Eye, EyeOff, RefreshCw, XCircle } from 'lucide-react'

// Shared pieces of the settings forms, so every section looks and behaves the same.

export const inputClass = 'w-full px-3 py-2 bg-white border border-slate-200 rounded-lg text-sm focus:outline-none focus:ring-2 focus:ring-brand-500/30 focus:border-brand-400'
export const labelClass = 'text-[10px] font-bold uppercase tracking-wider text-slate-500 mb-1 block'

// SecretInput hides a credential, which the server never sends back, behind dots.
export function SecretInput({ value, onChange, placeholder, label }: { value: string; onChange: (v: string) => void; placeholder: string; label?: string }) {
  const [visible, setVisible] = useState(false)
  return (
    <div className="relative">
      <input
        type={visible ? 'text' : 'password'}
        value={value}
        onChange={e => onChange(e.target.value)}
        placeholder={placeholder}
        aria-label={label}
        autoComplete="off"
        className={`${inputClass} pr-10 font-mono`}
      />
      <button type="button" onClick={() => setVisible(!visible)} aria-label={visible ? 'Hide' : 'Show'}
        className="absolute right-3 top-1/2 -translate-y-1/2 text-slate-400 hover:text-slate-600 transition-colors">
        {visible ? <EyeOff size={16} /> : <Eye size={16} />}
      </button>
    </div>
  )
}

// Toggle is the on/off switch of a settings card.
export function Toggle({ checked, onChange, label }: { checked: boolean; onChange: (v: boolean) => void; label: string }) {
  return (
    <label className="relative inline-flex items-center cursor-pointer shrink-0">
      <input type="checkbox" role="switch" aria-label={label} checked={checked} onChange={e => onChange(e.target.checked)} className="sr-only peer" />
      <span className={`w-11 h-6 rounded-full transition-colors peer-focus-visible:ring-2 peer-focus-visible:ring-brand-500/40 ${checked ? 'bg-brand-500' : 'bg-slate-300'}`}>
        <span className={`absolute top-0.5 left-0.5 w-5 h-5 bg-white rounded-full shadow-md transition-transform ${checked ? 'translate-x-5' : 'translate-x-0'}`} />
      </span>
    </label>
  )
}

// SettingsCard is one integration: logo, name, switch, and its fields, greyed out while off.
export function SettingsCard({ logo, title, subtitle, enabled, onToggle, children }: {
  logo: ReactNode; title: string; subtitle?: ReactNode; enabled: boolean; onToggle: (v: boolean) => void; children: ReactNode
}) {
  return (
    <div className="bg-white rounded-xl border border-slate-200 shadow-sm p-6">
      <div className="flex items-center justify-between gap-3 mb-5">
        <div className="flex items-center gap-3 min-w-0">
          {logo}
          <div className="min-w-0">
            <span className="font-bold text-slate-800">{title}</span>
            {subtitle && <p className="text-[11px] text-slate-400 mt-0.5">{subtitle}</p>}
          </div>
        </div>
        <Toggle checked={enabled} onChange={onToggle} label={`Enable ${title}`} />
      </div>
      <div className={`space-y-4 ${enabled ? '' : 'opacity-50 pointer-events-none'}`} aria-disabled={!enabled}>{children}</div>
    </div>
  )
}

// TestButton runs a connection test and shows its outcome next to it.
export function TestButton({ onTest, testing, result, label = 'Test connection' }: {
  onTest: () => void; testing: boolean; result: { ok: boolean; text: string } | null; label?: string
}) {
  return (
    <div className="flex flex-wrap items-center gap-3">
      <button type="button" onClick={onTest} disabled={testing}
        className="flex items-center gap-2 px-4 py-2 bg-slate-100 hover:bg-slate-200 text-slate-700 rounded-lg text-xs font-bold transition-all disabled:opacity-50">
        {testing ? <RefreshCw size={14} className="animate-spin" /> : <ExternalLink size={14} />}
        {label}
      </button>
      {result && (
        <span role="status" className={`text-xs font-bold flex items-center gap-1 ${result.ok ? 'text-emerald-600' : 'text-rose-500'}`}>
          {result.ok ? <CheckCircle2 size={14} /> : <XCircle size={14} />}
          {result.text}
        </span>
      )}
    </div>
  )
}
