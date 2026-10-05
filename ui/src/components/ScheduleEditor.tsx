import { Briefcase, CalendarRange, Link2, SlidersHorizontal, Plus, X } from 'lucide-react'
import type { ReactNode } from 'react'
import { DAY_SHORT, WEEK_ORDER, describePlan, planProblem, specFromPlan, timeZoneOptions, type PresetId, type SchedulePlan } from '../lib/schedule'
import type { ScalingSchedule } from '../lib/types'
import WeekTimeline from './WeekTimeline'

const input = 'bg-white border border-slate-200 rounded-lg px-2.5 py-1.5 text-sm font-semibold text-slate-700 outline-none focus:border-brand-500 focus:ring-2 focus:ring-brand-500/10'

function DayPicker({ days, onChange }: { days: number[]; onChange: (days: number[]) => void }) {
  return (
    <div className="flex gap-1">
      {WEEK_ORDER.map(d => {
        const on = days.includes(d)
        return (
          <button key={d} type="button" onClick={() => onChange(on ? days.filter(x => x !== d) : [...days, d])}
            className={`w-10 py-1.5 rounded-lg text-xs font-bold border transition-colors ${on ? 'bg-brand-600 border-brand-600 text-white' : 'bg-white border-slate-200 text-slate-400 hover:border-slate-300'}`}>
            {DAY_SHORT[d]}
          </button>
        )
      })}
    </div>
  )
}

function DaySelect({ value, onChange }: { value: number; onChange: (d: number) => void }) {
  return (
    <select className={input} value={value} onChange={e => onChange(Number(e.target.value))}>
      {WEEK_ORDER.map(d => <option key={d} value={d}>{DAY_SHORT[d]}</option>)}
    </select>
  )
}

// CustomWindows edits any number of daily or continuous windows; they are OR-ed.
function CustomWindows({ windows, onChange }: { windows: ScalingSchedule[]; onChange: (w: ScalingSchedule[]) => void }) {
  const patch = (i: number, w: ScalingSchedule) => onChange(windows.map((x, j) => (j === i ? w : x)))
  return (
    <div className="space-y-2">
      {windows.map((w, i) => {
        const weekly = w.startDay !== undefined && w.endDay !== undefined
        return (
          <div key={i} className="flex flex-wrap items-center gap-2 p-3 rounded-xl bg-white border border-slate-200">
            <select className={input} value={weekly ? 'weekly' : 'daily'}
              onChange={e => patch(i, e.target.value === 'weekly'
                ? { startDay: 1, startTime: w.startTime, endDay: 5, endTime: w.endTime }
                : { days: [1, 2, 3, 4, 5], startTime: w.startTime, endTime: w.endTime })}>
              <option value="daily">Every selected day</option>
              <option value="weekly">One continuous stretch</option>
            </select>
            {weekly ? (
              <>
                <DaySelect value={w.startDay!} onChange={d => patch(i, { ...w, startDay: d })} />
                <input type="time" className={input} value={w.startTime} onChange={e => patch(i, { ...w, startTime: e.target.value })} />
                <span className="text-slate-300">→</span>
                <DaySelect value={w.endDay!} onChange={d => patch(i, { ...w, endDay: d })} />
                <input type="time" className={input} value={w.endTime} onChange={e => patch(i, { ...w, endTime: e.target.value })} />
              </>
            ) : (
              <>
                <DayPicker days={w.days || []} onChange={days => patch(i, { ...w, days })} />
                <input type="time" className={input} value={w.startTime} onChange={e => patch(i, { ...w, startTime: e.target.value })} />
                <span className="text-slate-300">→</span>
                <input type="time" className={input} value={w.endTime} onChange={e => patch(i, { ...w, endTime: e.target.value })} />
              </>
            )}
            {windows.length > 1 && (
              <button type="button" onClick={() => onChange(windows.filter((_, j) => j !== i))} className="ml-auto p-1 text-slate-300 hover:text-rose-500" title="Remove window">
                <X size={16} />
              </button>
            )}
          </div>
        )
      })}
      <button type="button" onClick={() => onChange([...windows, { days: [1, 2, 3, 4, 5], startTime: '08:00', endTime: '20:00' }])}
        className="flex items-center gap-1 text-xs font-bold text-brand-600 hover:text-brand-700">
        <Plus size={14} /> Add window
      </button>
      <p className="text-[11px] text-slate-400">Up whenever any window is open. An end time earlier than the start runs overnight.</p>
    </div>
  )
}

interface PresetCardProps {
  id: PresetId
  current: PresetId
  icon: ReactNode
  title: string
  hint: string
  onSelect: (id: PresetId) => void
}

function PresetCard({ id, current, icon, title, hint, onSelect }: PresetCardProps) {
  const on = id === current
  return (
    <button type="button" onClick={() => onSelect(id)}
      className={`text-left p-3 rounded-xl border-2 transition-all ${on ? 'border-brand-500 bg-brand-50/60' : 'border-slate-200 bg-white hover:border-slate-300'}`}>
      <div className={`flex items-center gap-2 font-bold text-sm ${on ? 'text-brand-700' : 'text-slate-700'}`}>{icon}{title}</div>
      <div className="text-[11px] text-slate-500 mt-1 leading-snug">{hint}</div>
    </button>
  )
}

interface ScheduleEditorProps {
  plan: SchedulePlan
  onChange: (plan: SchedulePlan) => void
  // On demand only makes sense for groups other groups can depend on.
  allowOnDemand?: boolean
}

// ScheduleEditor answers "when should this run?" with a few presets and a live preview.
export default function ScheduleEditor({ plan, onChange, allowOnDemand = true }: ScheduleEditorProps) {
  const set = (patch: Partial<SchedulePlan>) => onChange({ ...plan, ...patch })
  const problem = planProblem(plan)
  const preview = specFromPlan(plan)

  return (
    <div className="space-y-4">
      <div className={`grid gap-2 ${allowOnDemand ? 'grid-cols-2 lg:grid-cols-4' : 'grid-cols-3'}`}>
        <PresetCard id="workhours" current={plan.preset} onSelect={id => set({ preset: id })} icon={<Briefcase size={16} />}
          title="Working hours" hint="Up during the hours you choose, down at night." />
        <PresetCard id="workweek" current={plan.preset} onSelect={id => set({ preset: id })} icon={<CalendarRange size={16} />}
          title="Work week" hint="Up non-stop Monday to Friday, down at weekends." />
        {allowOnDemand && (
          <PresetCard id="ondemand" current={plan.preset} onSelect={id => set({ preset: id })} icon={<Link2 size={16} />}
            title="On demand" hint="No hours of its own: up while a schedule that needs it is up." />
        )}
        <PresetCard id="custom" current={plan.preset} onSelect={id => set({ preset: id })} icon={<SlidersHorizontal size={16} />}
          title="Custom" hint="Several windows, overnight shifts, weekends…" />
      </div>

      {plan.preset !== 'ondemand' && (
        <div className="p-4 rounded-xl bg-slate-50 border border-slate-200 space-y-3">
          {plan.preset === 'workhours' && (
            <div className="flex flex-wrap items-center gap-3">
              <DayPicker days={plan.days} onChange={days => set({ days })} />
              <div className="flex items-center gap-2">
                <span className="text-xs font-bold text-slate-500">from</span>
                <input type="time" className={input} value={plan.start} onChange={e => set({ start: e.target.value })} />
                <span className="text-xs font-bold text-slate-500">to</span>
                <input type="time" className={input} value={plan.end} onChange={e => set({ end: e.target.value })} />
              </div>
            </div>
          )}
          {plan.preset === 'workweek' && (
            <div className="flex flex-wrap items-center gap-2">
              <span className="text-xs font-bold text-slate-500">Up from</span>
              <DaySelect value={plan.fromDay} onChange={fromDay => set({ fromDay })} />
              <input type="time" className={input} value={plan.fromTime} onChange={e => set({ fromTime: e.target.value })} />
              <span className="text-xs font-bold text-slate-500">until</span>
              <DaySelect value={plan.toDay} onChange={toDay => set({ toDay })} />
              <input type="time" className={input} value={plan.toTime} onChange={e => set({ toTime: e.target.value })} />
            </div>
          )}
          {plan.preset === 'custom' && <CustomWindows windows={plan.windows} onChange={windows => set({ windows })} />}
          <div className="flex items-center gap-2">
            <span className="text-xs font-bold text-slate-500">Time zone</span>
            <select className={input} value={plan.timezone} onChange={e => set({ timezone: e.target.value })}>
              {timeZoneOptions(plan.timezone).map(z => <option key={z} value={z}>{z}</option>)}
            </select>
          </div>
        </div>
      )}

      <div className="p-4 rounded-xl border border-slate-200 bg-white">
        <div className="flex items-baseline justify-between mb-3">
          <span className="text-xs font-bold uppercase tracking-wider text-slate-400">Preview</span>
          <span className="text-sm font-bold text-slate-700">{describePlan(plan)}</span>
        </div>
        {problem
          ? <p className="text-sm font-bold text-rose-600">{problem}</p>
          : <WeekTimeline schedules={preview.schedules} onDemand={plan.preset === 'ondemand'} timezone={plan.timezone} />}
      </div>
    </div>
  )
}
