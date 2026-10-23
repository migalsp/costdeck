import { describe, expect, it } from 'vitest'
import { describeException, exceptionProblem, isDate, isPast, parseIcs, upcomingDays } from './exceptions'

describe('exceptionProblem', () => {
  it.each([
    [{ start: '2026-12-25', state: 'Down' }, null],
    [{ start: '2026-12-24', end: '2027-01-01', state: 'Down' }, null],
    [{ start: '2026-11-07T06:00', end: '2026-11-07T22:00', state: 'Up' }, null],
    [{ start: '2026-12-25', end: '2026-12-25', state: 'Down' }, null],
    [{ start: '', state: 'Down' }, 'Pick the first day.'],
    [{ start: '25.12.2026', state: 'Down' }, 'The start must be a date, or a date and a time.'],
    [{ start: '2026-02-30', state: 'Down' }, 'The start must be a date, or a date and a time.'],
    [{ start: '2026-12-25T25:00', state: 'Down' }, 'The start must be a date, or a date and a time.'],
    [{ start: '2026-12-25', end: 'soon', state: 'Down' }, 'The end must be a date, or a date and a time.'],
    [{ start: '2026-12-25', end: '2026-12-24', state: 'Down' }, 'It ends before it starts.'],
    [{ start: '2026-11-07T22:00', end: '2026-11-07T06:00', state: 'Up' }, 'It ends before it starts.'],
    [{ start: '2026-11-07T06:00', end: '2026-11-07T06:00', state: 'Up' }, 'It ends before it starts.'],
    [{ start: '2026-11-07', state: 'Maybe' }, 'Choose up or down.'],
  ] as const)('%j → %s', (ex, want) => expect(exceptionProblem(ex as never)).toBe(want))
})

describe('describeException', () => {
  it('names one day, a range of days, and hours within a day', () => {
    expect(describeException({ start: '2026-12-25', state: 'Down' })).toMatch(/Dec 25$/)
    expect(describeException({ start: '2026-12-24', end: '2027-01-01', state: 'Down' })).toMatch(/Dec 24 – Jan 1/)
    expect(describeException({ start: '2026-11-07T06:00', end: '2026-11-07T22:00', state: 'Up' })).toMatch(/Nov 7 06:00 – 22:00/)
    expect(describeException({ start: '2026-11-07T14:00', state: 'Up' })).toMatch(/Nov 7 14:00 – end of day/)
  })
})

describe('past and upcoming', () => {
  it('knows an exception that is over', () => {
    expect(isPast({ start: '2026-10-01', state: 'Down' }, '2026-10-22')).toBe(true)
    expect(isPast({ start: '2026-10-20', end: '2026-10-22', state: 'Down' }, '2026-10-22')).toBe(false)
    expect(isPast({ start: '2026-10-22', state: 'Down' }, '2026-10-22')).toBe(false)
  })
  it('lists the days still to come', () => {
    const cal = { name: 'x', days: [{ date: '2026-01-01' }, { date: '2026-12-25' }] }
    expect(upcomingDays(cal, '2026-10-22')).toEqual([{ date: '2026-12-25' }])
  })
  it('accepts only real dates', () => {
    expect(isDate('2026-12-25')).toBe(true)
    expect(isDate('2026-13-01')).toBe(false)
    expect(isDate('2026-02-29')).toBe(false)
  })
})

describe('parseIcs', () => {
  const ics = [
    'BEGIN:VCALENDAR', 'VERSION:2.0',
    'BEGIN:VEVENT', 'DTSTART;VALUE=DATE:20261225', 'DTEND;VALUE=DATE:20261227', 'SUMMARY:Christmas\\, both days', 'END:VEVENT',
    'BEGIN:VEVENT', 'DTSTART;VALUE=DATE:20260101', 'SUMMARY:New Year', ' \'s Day', 'END:VEVENT',
    'BEGIN:VEVENT', 'DTSTART:20261031T180000Z', 'DTEND:20261031T230000Z', 'SUMMARY:Party', 'END:VEVENT',
    'BEGIN:VEVENT', 'DTSTART;VALUE=DATE:20261225', 'SUMMARY:Duplicate', 'END:VEVENT',
    'BEGIN:VEVENT', 'SUMMARY:No start', 'END:VEVENT',
    'END:VCALENDAR',
  ].join('\r\n')
  it('reads all-day events, unfolds lines, splits multi-day events and drops duplicates', () => {
    expect(parseIcs(ics)).toEqual([
      { date: '2026-01-01', name: "New Year's Day" },
      { date: '2026-10-31', name: 'Party' },
      { date: '2026-12-25', name: 'Christmas, both days' },
      { date: '2026-12-26', name: 'Christmas, both days' },
    ])
  })
  it('caps a runaway multi-day event at a year', () => {
    const long = 'BEGIN:VEVENT\nDTSTART;VALUE=DATE:20260101\nDTEND;VALUE=DATE:20300101\nEND:VEVENT'
    expect(parseIcs(long)).toHaveLength(366)
  })
  it('returns nothing for a file without events', () => {
    expect(parseIcs('not a calendar')).toEqual([])
  })
})
