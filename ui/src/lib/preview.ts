import type { SchedulePreview } from './types'

export interface DaySegment { start: number; end: number; active: boolean; reason: string }
export interface DayStrip { day: Date; segments: DaySegment[] }

// dayStrips cuts a preview's intervals into one strip per local day, each segment given as
// a share (0-100) of its day, for the two-week timeline.
export function dayStrips(intervals: SchedulePreview['intervals'], days: number, from = new Date()): DayStrip[] {
  const out: DayStrip[] = []
  const first = new Date(from.getFullYear(), from.getMonth(), from.getDate())
  for (let i = 0; i < days; i++) {
    const day = new Date(first.getFullYear(), first.getMonth(), first.getDate() + i)
    const next = new Date(first.getFullYear(), first.getMonth(), first.getDate() + i + 1)
    const span = next.getTime() - day.getTime() // 23 or 25 hours on days the clocks change
    const segments: DaySegment[] = []
    for (const iv of intervals) {
      const a = Math.max(new Date(iv.from).getTime(), day.getTime())
      const b = Math.min(new Date(iv.to).getTime(), next.getTime())
      if (b > a) segments.push({ start: ((a - day.getTime()) / span) * 100, end: ((b - day.getTime()) / span) * 100, active: iv.active, reason: iv.reason })
    }
    out.push({ day, segments })
  }
  return out
}
