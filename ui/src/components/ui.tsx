// Shared building blocks. One accent colour (brand, the emerald of the logo), slate for
// everything neutral, and status colours only where they carry meaning: emerald up/ok,
// sky in progress, amber manual or warning, rose danger.
import { useEffect, type ButtonHTMLAttributes, type ReactNode } from 'react'
import { ArrowLeft, X } from 'lucide-react'

type ButtonVariant = 'primary' | 'secondary' | 'ghost' | 'danger' | 'success' | 'warning'

const buttonVariants: Record<ButtonVariant, string> = {
  primary: 'bg-brand-600 text-white hover:bg-brand-700 shadow-sm',
  secondary: 'bg-white text-slate-700 border border-slate-200 hover:bg-slate-50 hover:border-slate-300 shadow-sm',
  ghost: 'text-slate-600 hover:bg-slate-100',
  danger: 'bg-white text-rose-600 border border-rose-200 hover:bg-rose-50',
  success: 'bg-brand-50 text-brand-700 border border-brand-200 hover:bg-brand-100',
  warning: 'bg-amber-50 text-amber-800 border border-amber-200 hover:bg-amber-100',
}

interface ButtonProps extends ButtonHTMLAttributes<HTMLButtonElement> {
  variant?: ButtonVariant
  size?: 'sm' | 'md'
  icon?: ReactNode
}

export function Button({ variant = 'secondary', size = 'md', icon, className = '', children, ...rest }: ButtonProps) {
  const sizing = size === 'sm' ? 'px-2.5 py-1.5 text-xs gap-1.5' : 'px-4 py-2 text-sm gap-2'
  return (
    <button
      type="button"
      {...rest}
      className={`inline-flex items-center justify-center rounded-lg font-semibold transition-colors focus:outline-none focus-visible:ring-2 focus-visible:ring-brand-500/40 disabled:opacity-50 disabled:pointer-events-none ${sizing} ${buttonVariants[variant]} ${className}`}
    >
      {icon}
      {children}
    </button>
  )
}

export function Card({ className = '', children, onClick }: { className?: string; children: ReactNode; onClick?: () => void }) {
  return (
    <div
      onClick={onClick}
      className={`bg-white rounded-xl border border-slate-200 shadow-sm ${onClick ? 'cursor-pointer hover:border-slate-300 hover:shadow-md transition-all' : ''} ${className}`}
    >
      {children}
    </div>
  )
}

interface PageHeaderProps {
  title: ReactNode
  subtitle?: ReactNode
  actions?: ReactNode
  onBack?: () => void
  extra?: ReactNode
}

export function PageHeader({ title, subtitle, actions, onBack, extra }: PageHeaderProps) {
  return (
    <div className="flex flex-wrap items-start justify-between gap-4 mb-8">
      <div className="flex items-start gap-3 min-w-0">
        {onBack && (
          <button onClick={onBack} className="mt-1 p-2 rounded-lg border border-slate-200 bg-white text-slate-500 hover:bg-slate-50" title="Back">
            <ArrowLeft size={18} />
          </button>
        )}
        <div className="min-w-0">
          <h1 className="text-2xl font-bold tracking-tight text-slate-900 flex items-center gap-2">{title}</h1>
          {subtitle && <p className="mt-1 text-sm text-slate-500">{subtitle}</p>}
          {extra && <div className="mt-3">{extra}</div>}
        </div>
      </div>
      {actions && <div className="flex flex-wrap items-center gap-2">{actions}</div>}
    </div>
  )
}

export function SectionTitle({ children, aside }: { children: ReactNode; aside?: ReactNode }) {
  return (
    <div className="flex items-center gap-3 mb-4">
      <h2 className="text-base font-semibold text-slate-800">{children}</h2>
      <div className="h-px flex-1 bg-slate-200" />
      {aside}
    </div>
  )
}

export type Tone = 'neutral' | 'brand' | 'success' | 'info' | 'warning' | 'danger'

const badgeTones: Record<Tone, string> = {
  neutral: 'bg-slate-100 text-slate-600 border-slate-200',
  brand: 'bg-brand-50 text-brand-700 border-brand-200',
  success: 'bg-emerald-50 text-emerald-700 border-emerald-200',
  info: 'bg-sky-50 text-sky-700 border-sky-200',
  warning: 'bg-amber-50 text-amber-800 border-amber-200',
  danger: 'bg-rose-50 text-rose-700 border-rose-200',
}

export function Badge({ tone = 'neutral', children, title }: { tone?: Tone; children: ReactNode; title?: string }) {
  return (
    <span title={title} className={`inline-flex items-center gap-1 px-2 py-0.5 rounded-md border text-[11px] font-semibold ${badgeTones[tone]}`}>
      {children}
    </span>
  )
}

export function Tabs<T extends string>({ tabs, value, onChange }: { tabs: { id: T; label: ReactNode }[]; value: T; onChange: (id: T) => void }) {
  return (
    <div className="flex gap-1 border-b border-slate-200">
      {tabs.map(t => (
        <button key={t.id} onClick={() => onChange(t.id)}
          className={`px-3 py-2 -mb-px text-sm font-semibold border-b-2 transition-colors ${value === t.id ? 'border-brand-600 text-brand-700' : 'border-transparent text-slate-500 hover:text-slate-800'}`}>
          {t.label}
        </button>
      ))}
    </div>
  )
}

// useEscape closes overlays with the Escape key.
function useEscape(onClose: () => void) {
  useEffect(() => {
    const onKey = (e: KeyboardEvent) => { if (e.key === 'Escape') onClose() }
    window.addEventListener('keydown', onKey)
    return () => window.removeEventListener('keydown', onKey)
  }, [onClose])
}

interface OverlayProps {
  title: ReactNode
  subtitle?: ReactNode
  onClose: () => void
  children: ReactNode
  footer?: ReactNode
  headerExtra?: ReactNode
}

// Drawer slides in from the right for details that keep the page in view.
export function Drawer({ title, subtitle, onClose, children, footer, headerExtra }: OverlayProps) {
  useEscape(onClose)
  return (
    <div className="fixed inset-0 z-[100] flex justify-end">
      <div className="absolute inset-0 bg-slate-900/40" onClick={onClose} />
      <div className="relative w-full max-w-2xl h-full bg-white shadow-2xl flex flex-col animate-slide-in">
        <div className="px-6 pt-5 pb-3 border-b border-slate-200">
          <div className="flex items-start justify-between gap-4">
            <div className="min-w-0">
              <h2 className="text-lg font-bold text-slate-900 truncate">{title}</h2>
              {subtitle && <div className="text-sm text-slate-500">{subtitle}</div>}
            </div>
            <button onClick={onClose} className="p-1.5 rounded-lg text-slate-400 hover:bg-slate-100" title="Close"><X size={18} /></button>
          </div>
          {headerExtra}
        </div>
        <div className="flex-1 overflow-y-auto px-6 py-5">{children}</div>
        {footer && <div className="px-6 py-3 border-t border-slate-200 bg-slate-50">{footer}</div>}
      </div>
    </div>
  )
}

// Modal centres a focused task, such as a confirmation or a form.
export function Modal({ title, subtitle, onClose, children, footer, size = 'md' }: OverlayProps & { size?: 'sm' | 'md' | 'lg' }) {
  useEscape(onClose)
  const width = size === 'sm' ? 'max-w-md' : size === 'lg' ? 'max-w-3xl' : 'max-w-xl'
  return (
    <div className="fixed inset-0 z-[110] flex items-center justify-center p-4">
      <div className="absolute inset-0 bg-slate-900/40" onClick={onClose} />
      <div className={`relative w-full ${width} max-h-[92vh] bg-white rounded-2xl shadow-2xl flex flex-col animate-pop`}>
        <div className="px-6 py-4 border-b border-slate-200 flex items-start justify-between gap-4">
          <div>
            <h2 className="text-lg font-bold text-slate-900">{title}</h2>
            {subtitle && <p className="text-sm text-slate-500">{subtitle}</p>}
          </div>
          <button onClick={onClose} className="p-1.5 rounded-lg text-slate-400 hover:bg-slate-100" title="Close"><X size={18} /></button>
        </div>
        <div className="px-6 py-5 overflow-y-auto">{children}</div>
        {footer && <div className="px-6 py-3 border-t border-slate-200 bg-slate-50 rounded-b-2xl flex items-center justify-end gap-2">{footer}</div>}
      </div>
    </div>
  )
}
