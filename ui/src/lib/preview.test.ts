import { describe, expect, it } from 'vitest'
import { dayStrips } from './preview'

describe('dayStrips', () => {
  const local = (d: number, h: number) => new Date(2026, 9, d, h).toISOString()
  it('cuts intervals into local days as shares of each day', () => {
    const strips = dayStrips([
      { from: local(20, 6), to: local(20, 18), active: true, reason: 'schedule' },
      { from: local(20, 18), to: local(22, 0), active: false, reason: 'Holiday' },
    ], 3, new Date(2026, 9, 20, 10))
    expect(strips).toHaveLength(3)
    expect(strips[0].segments.map(s => [Math.round(s.start), Math.round(s.end), s.active])).toEqual([[25, 75, true], [75, 100, false]])
    expect(strips[1].segments).toEqual([{ start: 0, end: 100, active: false, reason: 'Holiday' }])
    expect(strips[2].segments).toEqual([])
  })
  it('counts the day the clocks change by its real length', () => {
    // Europe/Berlin-style 25-hour day is not assumed: the share is of the actual day span.
    const strips = dayStrips([{ from: local(25, 0), to: local(26, 0), active: true, reason: 'schedule' }], 1, new Date(2026, 9, 25, 12))
    expect(strips[0].segments).toHaveLength(1)
    expect(strips[0].segments[0].start).toBe(0)
    expect(strips[0].segments[0].end).toBe(100)
  })
})
