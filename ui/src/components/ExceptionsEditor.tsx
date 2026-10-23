import { CalendarOff, CalendarPlus, Trash2 } from 'lucide-react'
import { describeException, exceptionProblem, isPast, upcomingDays } from '../lib/exceptions'
import type { HolidayCalendar, ScheduleException } from '../lib/types'
import { Button } from './ui'

interface Props {
  exceptions: ScheduleException[]
  calendars: string[]
  // The shared calendars defined under Settings; undefined while loading.
  available?: HolidayCalendar[]
  timezone: string
  onChange: (exceptions: ScheduleException[], calendars: string[]) => void
}

const today = () => new Date().toISOString().slice(0, 10)
const inputCls = 'px-2 py-1.5 text-sm bg-white border border-slate-200 rounded-lg outline-none focus:border-brand-500'

// ExceptionsEditor edits the dates on which a schedule does not apply: shared holiday
// calendars, and the schedule's own days or hours kept down or up.
export default function ExceptionsEditor({ exceptions, calendars, available, timezone, onChange }: Props) {
  const set = (i: number, patch: Partial<ScheduleException>) =>
    onChange(exceptions.map((e, j) => (j === i ? { ...e, ...patch } : e)), calendars)
  const add = (state: 'Up' | 'Down') =>
    onChange([...exceptions, { name: '', start: today(), state }], calendars)
  const past = exceptions.filter(e => isPast(e))

  return (
    <div className="space-y-6">
      <section>
        <h3 className="text-sm font-semibold text-slate-800">Holiday calendars</h3>
        <p className="text-sm text-slate-500 mb-2">Every day of a calendar you pick is an exception too. Calendars are kept under Settings → Holiday calendars.</p>
        <div className="flex flex-wrap gap-1.5">
          {available === undefined && <span className="text-xs text-slate-400">Loading…</span>}
          {available?.length === 0 && <span className="text-xs text-slate-400 italic">No calendars yet.</span>}
          {available?.map(c => {
            const on = calendars.includes(c.name)
            const next = upcomingDays(c)[0]
            return (
              <button key={c.name} type="button" aria-pressed={on}
                onClick={() => onChange(exceptions, on ? calendars.filter(n => n !== c.name) : [...calendars, c.name])}
                title={next ? `Next: ${next.date}${next.name ? ` (${next.name})` : ''}` : 'No days to come'}
                className={`px-2.5 py-1 rounded-lg text-xs font-semibold border ${on ? 'bg-brand-600 border-brand-600 text-white' : 'bg-white border-slate-200 text-slate-600 hover:border-slate-300'}`}>
                {c.name} <span className="font-normal opacity-75">· {upcomingDays(c).length} to come{c.state === 'Up' ? ' · up' : ''}</span>
              </button>
            )
          })}
          {calendars.filter(n => available && !available.some(c => c.name === n)).map(n => (
            <span key={n} className="px-2.5 py-1 rounded-lg text-xs font-semibold border border-rose-200 bg-rose-50 text-rose-700">{n} does not exist</span>
          ))}
        </div>
      </section>

      <section>
        <div className="flex flex-wrap items-center gap-2 mb-2">
          <h3 className="text-sm font-semibold text-slate-800 mr-auto">Dates</h3>
          <Button size="sm" icon={<CalendarOff size={14} />} onClick={() => add('Down')}>Keep down</Button>
          <Button size="sm" icon={<CalendarPlus size={14} />} onClick={() => add('Up')}>Keep up</Button>
        </div>
        <p className="text-sm text-slate-500 mb-3">
          Whole days, or a time on the first and last day. Times are in {timezone}. A manual start or stop still wins; where dates overlap, the one that starts last applies.
        </p>
        {exceptions.length === 0 && <p className="text-sm text-slate-400 italic">No dates yet.</p>}
        <div className="space-y-2">
          {exceptions.map((ex, i) => {
            const problem = exceptionProblem(ex)
            const [startDate, startTime = ''] = ex.start.split('T')
            const [endDate, endTime = ''] = (ex.end || '').split('T')
            const bound = (date: string, time: string) => (date && time ? `${date}T${time}` : date)
            return (
              <div key={i} className={`rounded-lg border p-3 ${isPast(ex) ? 'border-slate-100 opacity-60' : 'border-slate-200'}`} data-testid="exception">
                <div className="flex flex-wrap items-center gap-2">
                  <select aria-label="State" value={ex.state} onChange={e => set(i, { state: e.target.value as 'Up' | 'Down' })} className={inputCls}>
                    <option value="Down">Keep down</option>
                    <option value="Up">Keep up</option>
                  </select>
                  <input aria-label="Name" value={ex.name || ''} placeholder="What for, e.g. Christmas break" onChange={e => set(i, { name: e.target.value })}
                    className={`${inputCls} flex-1 min-w-40`} />
                  <button type="button" onClick={() => onChange(exceptions.filter((_, j) => j !== i), calendars)} title="Remove"
                    className="p-1.5 rounded-lg text-slate-400 hover:text-rose-600 hover:bg-rose-50"><Trash2 size={15} /></button>
                </div>
                <div className="flex flex-wrap items-center gap-2 mt-2 text-sm text-slate-600">
                  <span>from</span>
                  <input aria-label="First day" type="date" value={startDate} onChange={e => set(i, { start: bound(e.target.value, startTime) })} className={inputCls} />
                  <input aria-label="Start time" type="time" value={startTime} onChange={e => set(i, { start: bound(startDate, e.target.value) })} className={inputCls} />
                  <span>to</span>
                  <input aria-label="Last day" type="date" value={endDate} onChange={e => set(i, { end: bound(e.target.value, endTime) || undefined })} className={inputCls} />
                  <input aria-label="End time" type="time" value={endTime} disabled={!endDate} onChange={e => set(i, { end: bound(endDate, e.target.value) })} className={`${inputCls} disabled:opacity-50`} />
                  {!problem && <span className="text-xs text-slate-400">{describeException(ex)}{isPast(ex) ? ' · over' : ''}</span>}
                </div>
                {problem && <p className="mt-1 text-xs font-medium text-rose-600">{problem}</p>}
              </div>
            )
          })}
        </div>
        {past.length > 0 && (
          <button type="button" onClick={() => onChange(exceptions.filter(e => !isPast(e)), calendars)}
            className="mt-2 text-xs font-semibold text-brand-700 hover:text-brand-800">Remove {past.length} past date{past.length === 1 ? '' : 's'}</button>
        )}
      </section>
    </div>
  )
}
