import { AlertTriangle, CheckCircle2, X } from 'lucide-react'
import { dismissToast, useToasts } from '../lib/toast'

// Toaster shows the toasts at the bottom of the screen, above dialogs. Screen readers
// announce confirmations when they are idle and errors at once.
export default function Toaster() {
  const toasts = useToasts()
  return (
    <div aria-live="polite" className="fixed bottom-6 left-1/2 -translate-x-1/2 z-[110] flex flex-col items-center gap-2 w-[min(28rem,calc(100vw-2rem))] pointer-events-none">
      {toasts.map(t => (
        <div key={t.id} role={t.tone === 'error' ? 'alert' : 'status'}
          className="pointer-events-auto animate-rise flex items-start gap-2.5 w-full rounded-xl bg-slate-900 text-white shadow-xl pl-3.5 pr-2 py-2.5 text-sm">
          {t.tone === 'error'
            ? <AlertTriangle size={16} className="mt-0.5 shrink-0 text-rose-400" />
            : <CheckCircle2 size={16} className="mt-0.5 shrink-0 text-emerald-400" />}
          <span className="flex-1 leading-snug break-words">{t.text}</span>
          <button type="button" onClick={() => dismissToast(t.id)} aria-label="Dismiss" className="shrink-0 p-0.5 rounded-md text-slate-400 hover:text-white">
            <X size={14} />
          </button>
        </div>
      ))}
    </div>
  )
}
