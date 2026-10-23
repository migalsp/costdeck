import { useRef, useState } from 'react'
import { CalendarDays, Plus, Trash2, Upload } from 'lucide-react'
import { calendarProblem, parseIcs, upcomingDays } from '../../lib/exceptions'
import { timeZoneOptions } from '../../lib/schedule'
import type { HolidayCalendar } from '../../lib/types'
import { Button } from '../ui'

const inputCls = 'px-2 py-1.5 text-sm bg-white border border-slate-200 rounded-lg outline-none focus:border-brand-500'

const byDate = (days: HolidayCalendar['days']) => [...days].sort((a, b) => a.date.localeCompare(b.date))

// CalendarsEditor edits the shared holiday calendars that schedules name to apply their
// days as exceptions. Days can be typed in or imported from an .ics file.
export default function CalendarsEditor({ value, onChange }: { value: HolidayCalendar[]; onChange: (v: HolidayCalendar[]) => void }) {
  const file = useRef<HTMLInputElement>(null)
  const [importInto, setImportInto] = useState<number | null>(null)
  const [imported, setImported] = useState<string | null>(null)
  const set = (i: number, patch: Partial<HolidayCalendar>) => onChange(value.map((c, j) => (j === i ? { ...c, ...patch } : c)))
  const problem = calendarProblem(value)

  const importFile = async (f: File | undefined) => {
    if (!f || importInto === null) return
    const days = parseIcs(await f.text())
    const cal = value[importInto]
    const known = new Set(cal.days.map(d => d.date))
    const added = days.filter(d => !known.has(d.date))
    set(importInto, { days: byDate([...cal.days, ...added]) })
    setImported(added.length ? `Added ${added.length} day${added.length === 1 ? '' : 's'} to ${cal.name}.` : `${f.name} has no days ${cal.name} does not list already.`)
    if (file.current) file.current.value = ''
  }

  return (
    <div className="space-y-4">
      <input ref={file} type="file" accept=".ics,text/calendar" className="hidden" aria-label="iCalendar file" onChange={e => importFile(e.target.files?.[0])} />
      {value.length === 0 && (
        <p className="text-sm text-slate-500">No calendars yet. A calendar holds days such as public holidays; a schedule that names it keeps its namespaces down on those days (or up, for a calendar of release days).</p>
      )}
      {value.map((cal, i) => (
        <div key={i} className="bg-white rounded-xl border border-slate-200 shadow-sm p-5 space-y-3" data-testid="calendar">
          <div className="flex flex-wrap items-center gap-2">
            <CalendarDays size={18} className="text-brand-600" />
            <input aria-label="Calendar name" value={cal.name} placeholder="de-holidays" onChange={e => set(i, { name: e.target.value.toLowerCase() })} className={`${inputCls} w-48 font-mono`} />
            <select aria-label="Timezone" value={cal.timezone || ''} onChange={e => set(i, { timezone: e.target.value || undefined })} className={inputCls}>
              <option value="">Timezone of each schedule</option>
              {timeZoneOptions(cal.timezone).map(z => <option key={z} value={z}>{z}</option>)}
            </select>
            <select aria-label="Keep" value={cal.state || 'Down'} onChange={e => set(i, { state: e.target.value === 'Up' ? 'Up' : undefined })} className={inputCls}>
              <option value="Down">Keep down on these days</option>
              <option value="Up">Keep up on these days</option>
            </select>
            <span className="text-xs text-slate-400 mr-auto">{upcomingDays(cal).length} of {cal.days.length} still to come</span>
            <Button size="sm" icon={<Upload size={14} />} onClick={() => { setImportInto(i); setImported(null); file.current?.click() }}>Import .ics</Button>
            <button type="button" title="Remove calendar" onClick={() => onChange(value.filter((_, j) => j !== i))}
              className="p-1.5 rounded-lg text-slate-400 hover:text-rose-600 hover:bg-rose-50"><Trash2 size={15} /></button>
          </div>
          <div className="grid gap-1.5 sm:grid-cols-2">
            {cal.days.map((d, k) => (
              <div key={k} className="flex items-center gap-2">
                <input aria-label="Date" type="date" value={d.date} onChange={e => set(i, { days: cal.days.map((x, m) => (m === k ? { ...x, date: e.target.value } : x)) })} className={inputCls} />
                <input aria-label="Day name" value={d.name || ''} placeholder="Name" onChange={e => set(i, { days: cal.days.map((x, m) => (m === k ? { ...x, name: e.target.value || undefined } : x)) })} className={`${inputCls} flex-1 min-w-0`} />
                <button type="button" title="Remove day" onClick={() => set(i, { days: cal.days.filter((_, m) => m !== k) })}
                  className="p-1 rounded text-slate-300 hover:text-rose-600"><Trash2 size={13} /></button>
              </div>
            ))}
          </div>
          <Button size="sm" variant="ghost" icon={<Plus size={14} />} onClick={() => set(i, { days: [...cal.days, { date: '' }] })}>Add a day</Button>
        </div>
      ))}
      <div className="flex flex-wrap items-center gap-3">
        <Button icon={<Plus size={15} />} onClick={() => onChange([...value, { name: value.length ? `calendar-${value.length + 1}` : 'holidays', days: [] }])}>New calendar</Button>
        {imported && <span className="text-xs font-medium text-slate-600" role="status">{imported}</span>}
        {problem && <span className="text-xs font-medium text-rose-600">{problem}</span>}
      </div>
    </div>
  )
}
