import type { ScalingSchedule, ScalingSpec, ScheduleStatus } from './types'

// Day indexes follow the operator: Sunday = 0.
export const DAY_SHORT = ['Sun', 'Mon', 'Tue', 'Wed', 'Thu', 'Fri', 'Sat']
// Display order: the working week first.
export const WEEK_ORDER = [1, 2, 3, 4, 5, 6, 0]
export const WEEKDAYS = [1, 2, 3, 4, 5]

const MINUTES_PER_DAY = 1440
const MINUTES_PER_WEEK = 7 * MINUTES_PER_DAY
export const SLOTS_PER_DAY = 48 // half hours

export const browserTimeZone = (): string => {
  try {
    return Intl.DateTimeFormat().resolvedOptions().timeZone || 'UTC'
  } catch {
    return 'UTC'
  }
}

const COMMON_ZONES = [
  'UTC', 'Europe/London', 'Europe/Berlin', 'Europe/Paris', 'Europe/Moscow', 'Asia/Dubai', 'Asia/Kolkata',
  'Asia/Singapore', 'Asia/Tokyo', 'Australia/Sydney', 'America/New_York', 'America/Chicago', 'America/Denver',
  'America/Los_Angeles', 'America/Sao_Paulo',
]

// timeZoneOptions lists common zones plus the browser's and any already in use.
export const timeZoneOptions = (...extra: (string | undefined)[]): string[] =>
  Array.from(new Set([browserTimeZone(), ...extra.filter((z): z is string => !!z), ...COMMON_ZONES]))

// Presets cover what almost everyone wants; "custom" keeps the full window editor.
export type PresetId = 'workhours' | 'workweek' | 'ondemand' | 'custom'

export interface SchedulePlan {
  preset: PresetId
  timezone: string
  // workhours
  days: number[]
  start: string
  end: string
  // workweek
  fromDay: number
  fromTime: string
  toDay: number
  toTime: string
  // custom
  windows: ScalingSchedule[]
}

export const defaultPlan = (timezone = browserTimeZone()): SchedulePlan => ({
  preset: 'workhours', timezone,
  days: [...WEEKDAYS], start: '08:00', end: '20:00',
  fromDay: 1, fromTime: '00:00', toDay: 5, toTime: '23:59',
  windows: [{ days: [...WEEKDAYS], startTime: '08:00', endTime: '20:00', timezone }],
})

const isWeekly = (s: ScalingSchedule) => s.startDay !== undefined && s.endDay !== undefined

// planFromSpec recognises a preset in an existing spec, falling back to "custom".
export function planFromSpec(spec: Pick<ScalingSpec, 'schedules' | 'activation'>): SchedulePlan {
  const schedules = spec.schedules || []
  const tz = schedules[0]?.timezone || browserTimeZone()
  const plan = defaultPlan(tz)
  if (spec.activation === 'OnDemand') return { ...plan, preset: 'ondemand' }
  if (schedules.length === 0) return plan
  plan.windows = schedules.map(s => ({ ...s }))
  if (schedules.length === 1) {
    const s = schedules[0]
    if (isWeekly(s)) {
      return { ...plan, preset: 'workweek', fromDay: s.startDay!, fromTime: s.startTime, toDay: s.endDay!, toTime: s.endTime }
    }
    if ((s.days?.length || 0) > 0) {
      return { ...plan, preset: 'workhours', days: [...s.days!].sort(), start: s.startTime, end: s.endTime }
    }
  }
  return { ...plan, preset: 'custom' }
}

// specFromPlan renders a plan into the spec fields it owns.
export function specFromPlan(plan: SchedulePlan): { schedules?: ScalingSchedule[]; activation?: 'OnDemand' } {
  const timezone = plan.timezone
  switch (plan.preset) {
    case 'ondemand':
      return { schedules: undefined, activation: 'OnDemand' }
    case 'workhours':
      return { schedules: [{ days: [...plan.days].sort(), startTime: plan.start, endTime: plan.end, timezone }], activation: undefined }
    case 'workweek':
      return { schedules: [{ startDay: plan.fromDay, startTime: plan.fromTime, endDay: plan.toDay, endTime: plan.toTime, timezone }], activation: undefined }
    case 'custom':
      return { schedules: plan.windows.map(w => ({ ...w, timezone })), activation: undefined }
  }
}

// planProblem explains why a plan cannot be saved, or returns null.
export function planProblem(plan: SchedulePlan): string | null {
  if (plan.preset === 'workhours') {
    if (plan.days.length === 0) return 'Pick at least one day.'
    if (plan.start === plan.end) return 'Start and end time are the same.'
  }
  if (plan.preset === 'custom') {
    if (plan.windows.length === 0) return 'Add at least one window.'
    if (plan.windows.some(w => !isWeekly(w) && (w.days || []).length === 0)) return 'Every daily window needs at least one day.'
  }
  return null
}

const toMinutes = (hhmm: string): number | null => {
  const m = /^(\d{1,2}):(\d{2})$/.exec(hhmm || '')
  if (!m) return null
  const h = Number(m[1]), min = Number(m[2])
  return h < 24 && min < 60 ? h * 60 + min : null
}

// windowsOf converts schedules into [start, end] week-minute ranges; end < start wraps
// through Sunday midnight, exactly as the operator evaluates them.
function windowsOf(schedules: ScalingSchedule[]): [number, number][] {
  const out: [number, number][] = []
  for (const s of schedules) {
    const start = toMinutes(s.startTime), end = toMinutes(s.endTime)
    if (start === null || end === null) continue
    if (isWeekly(s)) {
      out.push([s.startDay! * MINUTES_PER_DAY + start, s.endDay! * MINUTES_PER_DAY + end])
      continue
    }
    for (const d of s.days || []) {
      const a = d * MINUTES_PER_DAY + start
      const b = end >= start ? d * MINUTES_PER_DAY + end : ((d + 1) % 7) * MINUTES_PER_DAY + end
      out.push([a, b])
    }
  }
  return out
}

const inWindow = (minute: number, [a, b]: [number, number]) => (a <= b ? minute >= a && minute <= b : minute >= a || minute <= b)

// weekGrid returns 7 * 48 half-hour slots (Sunday first) that are up. Without a usable
// window the operator keeps the target up (its fail-safe), and so does the grid.
export function weekGrid(schedules: ScalingSchedule[] | undefined): boolean[] {
  const windows = windowsOf(schedules || [])
  return Array.from({ length: 7 * SLOTS_PER_DAY }, (_, slot) =>
    windows.length === 0 || windows.some(w => inWindow((slot * 30 + 15) % MINUTES_PER_WEEK, w)))
}

export const upHours = (grid: boolean[]) => grid.filter(Boolean).length / 2

// nowSlot is the current half-hour slot in a time zone, Sunday first.
export function nowSlot(timezone: string, now = new Date()): number {
  try {
    const parts = new Intl.DateTimeFormat('en-US', { timeZone: timezone, weekday: 'short', hour: '2-digit', minute: '2-digit', hourCycle: 'h23' })
      .formatToParts(now)
    const get = (t: string) => parts.find(p => p.type === t)?.value || ''
    const day = DAY_SHORT.indexOf(get('weekday'))
    return day * SLOTS_PER_DAY + Math.floor((Number(get('hour')) * 60 + Number(get('minute'))) / 30)
  } catch {
    return -1
  }
}

const dayList = (days: number[]): string => {
  const sorted = WEEK_ORDER.filter(d => days.includes(d))
  if (sorted.length === 7) return 'Every day'
  if (sorted.length === 5 && WEEKDAYS.every(d => days.includes(d))) return 'Weekdays'
  if (sorted.length === 2 && days.includes(0) && days.includes(6)) return 'Weekends'
  return sorted.map(d => DAY_SHORT[d]).join(', ')
}

// describePlan renders a plan as one plain sentence.
export function describePlan(plan: SchedulePlan): string {
  switch (plan.preset) {
    case 'ondemand':
      return 'Runs only while a schedule that depends on it is up, or when started manually.'
    case 'workhours':
      return `${dayList(plan.days)} ${plan.start}–${plan.end}${plan.end < plan.start ? ' (overnight)' : ''}`
    case 'workweek':
      return `${DAY_SHORT[plan.fromDay]} ${plan.fromTime} → ${DAY_SHORT[plan.toDay]} ${plan.toTime}, non-stop`
    case 'custom':
      return `${plan.windows.length} custom window${plan.windows.length === 1 ? '' : 's'}`
  }
}

// describeSpec summarises a saved spec for a card.
export function describeSpec(spec: Pick<ScalingSpec, 'schedules' | 'activation'>): string {
  if (spec.activation !== 'OnDemand' && !(spec.schedules?.length)) return 'No schedule: always on'
  const plan = planFromSpec(spec)
  return describePlan(plan) + (plan.preset === 'ondemand' ? '' : ` · ${plan.timezone}`)
}

// when renders a moment relative to today in the viewer's locale: "today 20:00",
// "tomorrow 08:00", "Mon 08:00".
export function when(iso: string, now = new Date()): string {
  const t = new Date(iso)
  const time = t.toLocaleTimeString([], { hour: '2-digit', minute: '2-digit' })
  const minutes = Math.round((t.getTime() - now.getTime()) / 60000)
  if (minutes >= 0 && minutes < 60) return `in ${Math.max(minutes, 1)} min`
  if (minutes > 0 && minutes < 12 * 60) return `in ${Math.round(minutes / 60)} h, at ${time}`
  const days = Math.round((new Date(t.toDateString()).getTime() - new Date(now.toDateString()).getTime()) / 86400000)
  if (days === 0) return `today ${time}`
  if (days === 1) return `tomorrow ${time}`
  if (days > 1 && days < 7) return `${t.toLocaleDateString([], { weekday: 'short' })} ${time}`
  return t.toLocaleString([], { month: 'short', day: 'numeric', hour: '2-digit', minute: '2-digit' })
}

export type Tone = 'up' | 'down' | 'busy' | 'manual' | 'blocked'

export interface StatusLine {
  tone: Tone
  title: string
  detail?: string
}

interface StatusInput {
  phase?: string
  status?: ScheduleStatus & { requiredBy?: string[]; namespacesReady?: number; namespacesTotal?: number }
  dependsOn?: string[]
  activeUntil?: string
}

// statusLine answers "is it up, who decided, and what happens next?" in plain words.
export function statusLine({ phase, status, dependsOn, activeUntil }: StatusInput): StatusLine {
  const next = status?.nextTransition
  const progress = status?.namespacesTotal ? ` · ${status.namespacesReady || 0} of ${status.namespacesTotal} ready` : ''
  switch (phase) {
    case 'ScalingUp':
    case 'Scaling...':
      return { tone: 'busy', title: 'Starting…', detail: progress.slice(3) || undefined }
    case 'ScalingDown':
      return { tone: 'busy', title: 'Scaling down…', detail: progress.slice(3) || undefined }
    case 'WaitingForDependencies':
      return { tone: 'blocked', title: `Waiting for ${(dependsOn || []).join(', ') || 'dependencies'} to start` }
    case 'OverriddenByGroup':
      return { tone: 'down', title: 'Controlled by a group schedule' }
  }
  const up = status?.desiredState ? status.desiredState === 'Up' : phase === 'ScaledUp'
  const expiry = status?.overrideExpiresAt || activeUntil
  switch (status?.mode) {
    case 'ManualUp':
    case 'ManualDown':
      return {
        tone: 'manual',
        title: `Kept ${status.mode === 'ManualUp' ? 'up' : 'down'} manually`,
        detail: expiry ? `until ${when(expiry)}, then the schedule takes over` : 'until you hand it back to the schedule',
      }
    case 'Dependency':
      return { tone: 'up', title: 'Up', detail: `needed by ${(status.requiredBy || []).join(', ')}` }
    case 'OnDemand':
      return { tone: 'down', title: 'Down', detail: 'starts when a schedule that depends on it needs it' }
    case 'AlwaysOn':
      return { tone: 'up', title: 'Up', detail: 'no schedule set' }
  }
  if (up) return { tone: 'up', title: 'Up', detail: next ? `scales down ${when(next.time)}` : undefined }
  return { tone: 'down', title: 'Down', detail: next ? `starts ${when(next.time)}` : undefined }
}
