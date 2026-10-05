import { DAY_SHORT, SLOTS_PER_DAY, WEEK_ORDER, nowSlot, upHours, weekGrid } from '../lib/schedule'
import type { ScalingSchedule } from '../lib/types'

interface WeekTimelineProps {
  schedules?: ScalingSchedule[]
  // onDemand groups have no window of their own: everything renders as down.
  onDemand?: boolean
  timezone?: string
  compact?: boolean
}

// WeekTimeline draws when a schedule keeps workloads up across the week (green) and when
// they are scaled down, with a marker at the current time in the schedule's time zone.
export default function WeekTimeline({ schedules, onDemand, timezone, compact }: WeekTimelineProps) {
  const grid = onDemand ? new Array(7 * SLOTS_PER_DAY).fill(false) : weekGrid(schedules)
  const tz = timezone || schedules?.[0]?.timezone || 'UTC'
  const now = nowSlot(tz)
  const hours = upHours(grid)
  const rowHeight = compact ? 'h-2' : 'h-3'

  return (
    <div className="select-none" title={compact ? `Up ${hours} h a week, down ${168 - hours} h (${tz})` : undefined}>
      <div className={`grid items-center ${compact ? 'gap-x-1.5 gap-y-[3px]' : 'gap-1'}`} style={{ gridTemplateColumns: compact ? '0.6rem 1fr' : '2.25rem 1fr' }}>
        {WEEK_ORDER.map(day => (
          <div key={day} className="contents">
            <span className={`font-bold text-slate-400 ${compact ? 'text-[8px] leading-[8px]' : 'text-[10px] leading-3'}`}>
              {compact ? DAY_SHORT[day][0] : DAY_SHORT[day]}
            </span>
            <div className={`relative flex ${rowHeight} rounded-sm overflow-hidden bg-slate-100`}>
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
          <p className="mt-2 text-xs text-slate-500">
            {onDemand
              ? 'No fixed hours: up only while something that depends on it is up.'
              : <>Up <b className="text-slate-700">{hours} h</b> a week, scaled down <b className="text-emerald-600">{168 - hours} h ({Math.round((168 - hours) / 168 * 100)}%)</b> · times in {tz}</>}
          </p>
        </>
      )}
    </div>
  )
}
