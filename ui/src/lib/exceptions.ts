import type { HolidayCalendar, ScheduleException } from './types'

// Dated exceptions to the weekly schedules, as the operator reads them (internal/scaling
// exceptions.go): a bound is a date ("2026-12-24", the whole day) or a date and time
// ("2026-12-24T18:00"); an end date includes that day; without an end the exception lasts
// to the end of its start day.

const DATE = /^\d{4}-\d{2}-\d{2}$/
const DATE_TIME = /^\d{4}-\d{2}-\d{2}T([01]\d|2[0-3]):[0-5]\d$/

export const isDate = (s: string) => DATE.test(s) && !isNaN(Date.parse(`${s}T00:00:00Z`)) && new Date(`${s}T00:00:00Z`).toISOString().startsWith(s)
const isBound = (s: string) => isDate(s.slice(0, 10)) && (DATE.test(s) || DATE_TIME.test(s))

// boundKey orders bounds within one timezone: the start of a date, or the end of it.
const startKey = (s: string) => (DATE.test(s) ? `${s}T00:00` : s)
const endKey = (s: string) => (DATE.test(s) ? `${s}T24:00` : s)

// exceptionProblem explains why the operator would not apply an exception, or returns null.
export function exceptionProblem(ex: ScheduleException): string | null {
  if (!ex.start) return 'Pick the first day.'
  if (!isBound(ex.start)) return 'The start must be a date, or a date and a time.'
  if (ex.end && !isBound(ex.end)) return 'The end must be a date, or a date and a time.'
  if (ex.state !== 'Up' && ex.state !== 'Down') return 'Choose up or down.'
  if (ex.end && endKey(ex.end) <= startKey(ex.start)) return 'It ends before it starts.'
  return null
}

const fmtDate = (d: string) => new Date(`${d}T00:00:00Z`).toLocaleDateString(undefined, { month: 'short', day: 'numeric', timeZone: 'UTC' })
const fmtBound = (s: string) => (DATE.test(s) ? fmtDate(s) : `${fmtDate(s.slice(0, 10))} ${s.slice(11)}`)

// describeException renders when an exception applies: "Dec 25", "Dec 24 – Jan 1",
// "Nov 7 06:00 – 22:00".
export function describeException(ex: ScheduleException): string {
  if (!ex.end || ex.end === ex.start) return fmtBound(ex.start) + (DATE.test(ex.start) ? '' : ' – end of day')
  if (ex.end.slice(0, 10) === ex.start.slice(0, 10) && !DATE.test(ex.start) && !DATE.test(ex.end)) return `${fmtBound(ex.start)} – ${ex.end.slice(11)}`
  return `${fmtBound(ex.start)} – ${fmtBound(ex.end)}`
}

// isPast says whether an exception ended before today (in the viewer's calendar, which is
// close enough to decide what to dim).
export function isPast(ex: ScheduleException, today = new Date().toISOString().slice(0, 10)): boolean {
  return endKey(ex.end || ex.start.slice(0, 10)) < `${today}T00:00`
}

// upcomingDays lists a calendar's days from today on.
export const upcomingDays = (cal: HolidayCalendar, today = new Date().toISOString().slice(0, 10)) =>
  cal.days.filter(d => d.date >= today)

// parseIcs reads the all-day events of an iCalendar file (a public holiday calendar, for
// example) as calendar days. Multi-day events become one day each; timed events count for
// the day they start. Duplicates are dropped and the days sorted.
export function parseIcs(text: string): { date: string; name?: string }[] {
  // Lines starting with a space or tab continue the previous one (RFC 5545 folding).
  const lines = text.replace(/\r\n/g, '\n').replace(/\n[ \t]/g, '').split('\n')
  const out = new Map<string, string | undefined>()
  let ev: { start?: string; end?: string; startIsDate?: boolean; name?: string } | null = null
  const dateOf = (v: string) => (/^\d{8}/.test(v) ? `${v.slice(0, 4)}-${v.slice(4, 6)}-${v.slice(6, 8)}` : undefined)
  for (const line of lines) {
    if (line === 'BEGIN:VEVENT') ev = {}
    else if (line === 'END:VEVENT' && ev) {
      if (ev.start) {
        const days = [ev.start]
        // An all-day DTEND is exclusive: the day after the last one. A year at most.
        if (ev.startIsDate && ev.end) for (let d = nextDay(ev.start); d < ev.end && days.length < 366; d = nextDay(d)) days.push(d)
        for (const d of days) if (!out.has(d)) out.set(d, ev.name)
      }
      ev = null
    } else if (ev) {
      const colon = line.indexOf(':')
      if (colon < 0) continue
      const key = line.slice(0, colon).toUpperCase()
      const value = line.slice(colon + 1).trim()
      if (key.startsWith('DTSTART')) {
        ev.start = dateOf(value)
        ev.startIsDate = key.includes('VALUE=DATE') || /^\d{8}$/.test(value)
      } else if (key.startsWith('DTEND')) ev.end = dateOf(value)
      else if (key.startsWith('SUMMARY')) ev.name = value.replace(/\\([,;\\])/g, '$1').replace(/\\n/gi, ' ') || undefined
    }
  }
  return [...out.entries()].sort(([a], [b]) => a.localeCompare(b)).map(([date, name]) => (name ? { date, name } : { date }))
}

function nextDay(d: string): string {
  const t = new Date(`${d}T00:00:00Z`)
  t.setUTCDate(t.getUTCDate() + 1)
  return t.toISOString().slice(0, 10)
}

const CALENDAR_NAME = /^[a-z0-9]([-a-z0-9]*[a-z0-9])?$/

// calendarProblem explains why the server would refuse a set of calendars, or returns null.
export function calendarProblem(calendars: HolidayCalendar[]): string | null {
  const names = new Set<string>()
  for (const c of calendars) {
    if (!CALENDAR_NAME.test(c.name) || c.name.length > 63) return `Calendar name "${c.name}" must be lowercase letters, digits and dashes.`
    if (names.has(c.name)) return `There are two calendars named "${c.name}".`
    names.add(c.name)
    const dates = new Set<string>()
    for (const d of c.days) {
      if (!isDate(d.date)) return `${c.name}: "${d.date || 'an empty date'}" is not a date.`
      if (dates.has(d.date)) return `${c.name} lists ${d.date} twice.`
      dates.add(d.date)
    }
  }
  return null
}
