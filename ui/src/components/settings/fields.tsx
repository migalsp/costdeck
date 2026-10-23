import { useState, type ReactNode } from 'react'
import { CheckCircle2, ExternalLink, Eye, EyeOff, RefreshCw, XCircle } from 'lucide-react'
import { Button } from '../ui'

// Shared pieces of the settings forms, so every section looks and behaves the same.

// Values such as IDs and URLs are set in monospace (add font-mono); placeholders stay in
// the normal face, so an empty field reads as a hint rather than as a value.
export const inputClass = 'w-full px-3 py-2 bg-white border border-slate-200 rounded-lg text-sm placeholder:font-sans placeholder:text-slate-400 focus:outline-none focus:ring-2 focus:ring-brand-500/30 focus:border-brand-400'
export const labelClass = 'text-[11px] font-bold uppercase tracking-wider text-slate-500 mb-1.5 block'
export const hintClass = 'mt-1.5 text-xs text-slate-400'
export const checkClass = 'flex items-center gap-2 text-sm text-slate-600 cursor-pointer'

// Configured marks a credential the server holds; the field stays empty to keep it.
export const Configured = () => <span className="ml-2 text-emerald-600 normal-case font-medium tracking-normal">✓ Configured</span>

// Optional is the grey remark after a label, such as "(optional)".
export const Optional = ({ children }: { children: ReactNode }) => <span className="normal-case font-normal tracking-normal text-slate-400">{children}</span>

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
export function Toggle({ checked, onChange, label, disabled }: { checked: boolean; onChange: (v: boolean) => void; label: string; disabled?: boolean }) {
  return (
    <label className={`relative inline-flex items-center shrink-0 ${disabled ? 'opacity-50 cursor-not-allowed' : 'cursor-pointer'}`}>
      <input type="checkbox" role="switch" aria-label={label} checked={checked} disabled={disabled} onChange={e => onChange(e.target.checked)} className="sr-only peer" />
      <span className={`w-11 h-6 rounded-full transition-colors peer-focus-visible:ring-2 peer-focus-visible:ring-brand-500/40 ${checked ? 'bg-brand-500' : 'bg-slate-300'}`}>
        <span className={`absolute top-0.5 left-0.5 w-5 h-5 bg-white rounded-full shadow-md transition-transform ${checked ? 'translate-x-5' : 'translate-x-0'}`} />
      </span>
    </label>
  )
}

// CardIcon is the square icon of a settings card that has no product logo.
export const CardIcon = ({ children }: { children: ReactNode }) => (
  <div className="w-10 h-10 rounded-xl bg-brand-50 text-brand-600 flex items-center justify-center shrink-0">{children}</div>
)

// SettingsCard is one integration: logo, name, switch, and its fields, greyed out while off.
// `status` sits next to the switch (a connection badge); `footer` shows under the fields
// while the card is on.
export function SettingsCard({ logo, title, subtitle, status, enabled, onToggle, footer, children }: {
  logo: ReactNode; title: string; subtitle?: ReactNode; status?: ReactNode; enabled: boolean; onToggle: (v: boolean) => void; footer?: ReactNode; children: ReactNode
}) {
  return (
    <div className="bg-white rounded-xl border border-slate-200 shadow-sm p-6">
      <div className="flex items-center justify-between gap-3 mb-5">
        <div className="flex items-center gap-3 min-w-0">
          {logo}
          <div className="min-w-0">
            <span className="font-bold text-slate-800">{title}</span>
            {subtitle && <p className="text-xs text-slate-400 mt-0.5">{subtitle}</p>}
          </div>
        </div>
        <div className="flex items-center gap-3 shrink-0">
          {status}
          <Toggle checked={enabled} onChange={onToggle} label={`Enable ${title}`} />
        </div>
      </div>
      <div className={`space-y-4 ${enabled ? '' : 'opacity-50 pointer-events-none'}`} aria-disabled={!enabled}>{children}</div>
      {enabled && footer && <div className="mt-5 pt-4 border-t border-slate-100 text-xs text-slate-500">{footer}</div>}
    </div>
  )
}

// TestButton runs a connection test and shows its outcome next to it.
export function TestButton({ onTest, testing, result, label = 'Test connection', disabled }: {
  onTest: () => void; testing: boolean; result: { ok: boolean; text: string } | null; label?: string; disabled?: boolean
}) {
  return (
    <div className="flex flex-wrap items-center gap-3">
      <Button size="sm" onClick={onTest} disabled={testing || disabled}
        icon={testing ? <RefreshCw size={14} className="animate-spin" /> : <ExternalLink size={14} />}>
        {label}
      </Button>
      {result && (
        <span role="status" className={`text-xs font-medium flex items-center gap-1 ${result.ok ? 'text-emerald-700' : 'text-rose-600'}`}>
          {result.ok ? <CheckCircle2 size={14} /> : <XCircle size={14} />}
          {result.text}
        </span>
      )}
    </div>
  )
}

// Segmented picks one of a few options, such as an authentication method.
export function Segmented<T extends string>({ value, onChange, options, label }: {
  value: T; onChange: (v: T) => void; options: readonly (readonly [T, string])[]; label: string
}) {
  return (
    <div role="radiogroup" aria-label={label} className="inline-flex flex-wrap gap-0.5 rounded-lg bg-slate-100 p-0.5">
      {options.map(([id, text]) => (
        <button key={id} type="button" role="radio" aria-checked={value === id} onClick={() => onChange(id)}
          className={`px-3 py-1.5 rounded-md text-xs font-semibold transition-colors ${value === id ? 'bg-white text-slate-900 shadow-sm' : 'text-slate-500 hover:text-slate-800'}`}>
          {text}
        </button>
      ))}
    </div>
  )
}
