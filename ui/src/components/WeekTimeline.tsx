import { useState } from 'react'
import { DAY_SHORT, SLOTS_PER_DAY, WEEK_ORDER, nowSlot, upHours, weekGrid } from '../lib/schedule'
import type { ScalingSchedule } from '../lib/types'

interface WeekTimelineProps {
  schedules?: ScalingSchedule[]
  // onDemand groups have no window of their own: everything renders as down.
  onDemand?: boolean
  timezone?: string
  compact?: boolean
}

const slotTime = (slot: number) => `${String(Math.floor(slot / 2)).padStart(2, '0')}:${slot % 2 ? '30' : '00'}`

// upWindows lists the runs of up slots in one day as "08:00–20:00".
function upWindows(day: boolean[]): string[] {
  const out: string[] = []
  for (let i = 0; i < day.length; i++) {
    if (!day[i] || (i > 0 && day[i - 1])) continue
    let end = i
    while (end < day.length && day[end]) end++
    out.push(`${slotTime(i)}–${slotTime(end)}`)
  }
  return out
}

// WeekTimeline draws when a schedule keeps workloads up across the week (green) and when
// they are scaled down, with a marker at the current time in the schedule's time zone.
// Hovering a day names its up hours.
export default function WeekTimeline({ schedules, onDemand, timezone, compact }: WeekTimelineProps) {
  const [hover, setHover] = useState<number | null>(null)
  const grid = onDemand ? new Array(7 * SLOTS_PER_DAY).fill(false) : weekGrid(schedules)
  const tz = timezone || schedules?.[0]?.timezone || 'UTC'
  const now = nowSlot(tz)
  const hours = upHours(grid)
  const rowHeight = compact ? 'h-2' : 'h-3'

  const describe = (day: number) => {
    const slots = grid.slice(day * SLOTS_PER_DAY, (day + 1) * SLOTS_PER_DAY)
    const windows = upWindows(slots)
    const what = onDemand ? 'up only on demand'
      : windows.length === 0 ? 'scaled down all day'
      : windows.length === 1 && windows[0] === '00:00–24:00' ? 'up all day'
      : `up ${windows.join(', ')}`
    const isToday = Math.floor(now / SLOTS_PER_DAY) === day
    return `${DAY_SHORT[day]}: ${what}${isToday ? ` · now ${slotTime(now % SLOTS_PER_DAY)}` : ''}`
  }

  return (
    <div className="select-none" onMouseLeave={() => setHover(null)}>
      <div className={`grid items-center ${compact ? 'gap-x-1.5 gap-y-[3px]' : 'gap-1'}`} style={{ gridTemplateColumns: compact ? '0.6rem 1fr' : '2.25rem 1fr' }}>
        {WEEK_ORDER.map(day => (
          <div key={day} className="contents">
            <span className={`font-bold ${hover === day ? 'text-slate-700' : 'text-slate-400'} ${compact ? 'text-[8px] leading-[8px]' : 'text-[10px] leading-3'}`}>
              {compact ? DAY_SHORT[day][0] : DAY_SHORT[day]}
            </span>
            <div className="relative" onMouseEnter={() => setHover(day)}>
              <div className={`relative flex ${rowHeight} rounded-sm overflow-hidden bg-slate-100 ${hover === day ? 'ring-1 ring-slate-400' : ''}`}>
                {grid.slice(day * SLOTS_PER_DAY, (day + 1) * SLOTS_PER_DAY).map((up, i) => (
                  <div key={i} className={`flex-1 ${up ? 'bg-emerald-400' : ''}`} />
                ))}
                {Math.floor(now / SLOTS_PER_DAY) === day && (
                  <div
                    className="absolute top-[-2px] bottom-[-2px] w-0.5 bg-slate-800 rounded"
                    style={{ left: `${((now % SLOTS_PER_DAY) + 0.5) / SLOTS_PER_DAY * 100}%` }}
                  />
                )}
              </div>
              {hover === day && (
                <div role="tooltip" className="pointer-events-none absolute left-1/2 -translate-x-1/2 bottom-full mb-1.5 z-30 whitespace-nowrap rounded-md bg-slate-900 px-2 py-1 text-[11px] text-white shadow-lg">
                  {describe(day)}
                  {compact && <div className="text-slate-400">{hours} h up a week · {tz}</div>}
                </div>
              )}
            </div>
          </div>
        ))}
      </div>
      {!compact && (
        <>
          <div className="grid mt-1" style={{ gridTemplateColumns: '2.25rem 1fr' }}>
            <span />
            <div className="flex justify-between text-[9px] font-bold text-slate-300">
              <span>00</span><span>06</span><span>12</span><span>18</span><span>24</span>
            </div>
          </div>
          <div className="mt-2 flex flex-wrap gap-x-4 gap-y-1 text-[11px] text-slate-500">
            <span className="inline-flex items-center gap-1.5"><span className="w-2.5 h-2.5 rounded-sm bg-emerald-400" />Up</span>
            <span className="inline-flex items-center gap-1.5"><span className="w-2.5 h-2.5 rounded-sm bg-slate-100 ring-1 ring-inset ring-slate-200" />Scaled down</span>
            <span className="inline-flex items-center gap-1.5"><span className="w-0.5 h-3 rounded bg-slate-800" />Now</span>
          </div>
          <p className="mt-1.5 text-xs text-slate-500">
            {onDemand
              ? 'No fixed hours: up only while something that depends on it is up.'
              : <>Up <b className="text-slate-700">{hours} h</b> a week, scaled down <b className="text-emerald-600">{168 - hours} h ({Math.round((168 - hours) / 168 * 100)}%)</b> · times in {tz}</>}
          </p>
        </>
      )}
    </div>
  )
}
