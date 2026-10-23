import { useState } from 'react'
import { Sparkles } from 'lucide-react'
import { formatMoney } from '../lib/format'
import { fetchSuggestion, suggestionNote, type Suggestion } from '../lib/suggest'
import type { ScalingSchedule } from '../lib/types'
import { Button } from './ui'

// ScheduleSuggestion fills the schedule editor with the hours a namespace was busy in the
// last week.
export default function ScheduleSuggestion({ namespace, timezone, onApply }: { namespace: string; timezone: string; onApply: (schedules: ScalingSchedule[]) => void }) {
  const [result, setResult] = useState<{ suggestion?: Suggestion; error?: string } | null>(null)
  const [busy, setBusy] = useState(false)
  const suggest = async () => {
    setBusy(true)
    try {
      const s = await fetchSuggestion(namespace, timezone)
      setResult({ suggestion: s })
      if (s.reason === 'pattern') onApply(s.schedules)
    } catch (e) {
      setResult({ error: e instanceof Error ? e.message : String(e) })
    } finally {
      setBusy(false)
    }
  }
  const s = result?.suggestion
  return (
    <div className="mb-3 flex flex-wrap items-center gap-2" data-testid="schedule-suggestion">
      <Button size="sm" variant="secondary" icon={<Sparkles size={13} />} onClick={suggest} disabled={busy}>{busy ? 'Reading usage…' : 'Suggest from usage'}</Button>
      {result?.error && <span className="text-xs text-rose-600">Could not suggest a schedule: {result.error}</span>}
      {s && <span className={`text-xs ${s.reason === 'pattern' ? 'text-slate-600' : 'text-amber-700'}`}>{suggestionNote(s, n => formatMoney(n, s.currency))}</span>}
    </div>
  )
}
