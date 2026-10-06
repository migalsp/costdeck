import type { StatusLine, StatusTone } from '../lib/schedule'

const toneStyle: Record<StatusTone, { dot: string; text: string }> = {
  up: { dot: 'bg-emerald-500', text: 'text-emerald-700' },
  down: { dot: 'bg-slate-300', text: 'text-slate-700' },
  busy: { dot: 'bg-sky-500 animate-pulse', text: 'text-sky-700' },
  manual: { dot: 'bg-amber-500', text: 'text-amber-800' },
  blocked: { dot: 'bg-sky-400 animate-pulse', text: 'text-sky-700' },
}

// ScheduleStatusLine renders statusLine(): a coloured dot, the state and what happens next.
export default function ScheduleStatusLine({ line, size = 'md' }: { line: StatusLine; size?: 'md' | 'lg' }) {
  const tone = toneStyle[line.tone]
  return (
    <div className={`flex items-start gap-2 ${size === 'lg' ? 'text-base' : 'text-sm'}`}>
      <span className={`mt-1.5 w-2.5 h-2.5 rounded-full shrink-0 ${tone.dot}`} />
      <div className="min-w-0">
        <span className={`font-semibold ${tone.text}`}>{line.title}</span>
        {line.detail && <span className="text-slate-500"> · {line.detail}</span>}
      </div>
    </div>
  )
}
