import { describe, expect, it } from 'vitest'
import shared from '../../../internal/scaling/testdata/windows.json'
import type { ScalingSchedule } from './types'
import { defaultPlan, describeOverride, describePlan, describeSpec, isUpAt, pendingChange, planFromSpec, planProblem, specFromPlan, statusLine, upHours, weekGrid, when } from './schedule'

// "Mon 08:00" as a minute of the week, Sunday 00:00 = 0.
const weekMinute = (s: string) => {
  const [day, time] = s.split(' ')
  const [h, m] = time.split(':').map(Number)
  return ['Sun', 'Mon', 'Tue', 'Wed', 'Thu', 'Fri', 'Sat'].indexOf(day) * 1440 + h * 60 + m
}

describe('isUpAt matches the operator (internal/scaling/testdata/windows.json)', () => {
  for (const c of shared.cases) {
    it(c.name, () => {
      const schedules = c.schedules as ScalingSchedule[]
      for (const at of c.up) expect(isUpAt(schedules, weekMinute(at)), `${at} should be up`).toBe(true)
      for (const at of c.down) expect(isUpAt(schedules, weekMinute(at)), `${at} should be down`).toBe(false)
    })
  }
})

describe('weekGrid', () => {
  it('marks half-hour slots, Sunday first', () => {
    const grid = weekGrid([{ days: [1], startTime: '08:00', endTime: '10:00', timezone: 'UTC' }])
    expect(grid).toHaveLength(7 * 48)
    expect(grid[48 + 16]).toBe(true) // Mon 08:00–08:30
    expect(grid[48 + 19]).toBe(true) // Mon 09:30–10:00
    expect(grid[48 + 20]).toBe(false) // Mon 10:00–10:30: judged at 10:15
    expect(upHours(grid)).toBe(2)
  })
  it('is up all week without a schedule, like the operator', () => {
    expect(upHours(weekGrid(undefined))).toBe(168)
  })
})

describe('plans and specs', () => {
  const tz = 'Europe/Berlin'
  it.each([
    ['work hours', { schedules: [{ days: [5, 1, 3], startTime: '07:00', endTime: '19:00', timezone: tz }] }, 'workhours'],
    ['a work week', { schedules: [{ startDay: 1, startTime: '06:00', endDay: 5, endTime: '22:00', timezone: tz }] }, 'workweek'],
    ['on demand', { activation: 'OnDemand' as const }, 'ondemand'],
    ['several windows', { schedules: [{ days: [1], startTime: '08:00', endTime: '12:00', timezone: tz }, { days: [2], startTime: '08:00', endTime: '12:00', timezone: tz }] }, 'custom'],
  ] as [string, { schedules?: ScalingSchedule[]; activation?: 'OnDemand' }, string][])('recognises %s and renders it back unchanged', (_, spec, preset) => {
    const plan = planFromSpec(spec)
    expect(plan.preset).toBe(preset)
    const back = specFromPlan(plan)
    if (preset === 'ondemand') expect(back).toEqual({ schedules: undefined, activation: 'OnDemand' })
    else expect(back.schedules).toEqual(spec.schedules!.map(s => (s.days ? { ...s, days: [...s.days].sort() } : s)))
  })
  it('starts a new schedule as weekday working hours in the given zone', () => {
    const plan = defaultPlan(tz)
    expect(specFromPlan(plan).schedules).toEqual([{ days: [1, 2, 3, 4, 5], startTime: '08:00', endTime: '20:00', timezone: tz }])
  })
  it('falls back to the default plan for an empty spec', () => {
    expect(planFromSpec({}).preset).toBe('workhours')
  })
})

describe('planProblem', () => {
  const plan = defaultPlan('UTC')
  it('accepts the default plan', () => expect(planProblem(plan)).toBeNull())
  it('needs a day', () => expect(planProblem({ ...plan, days: [] })).toMatch(/at least one day/))
  it('rejects an empty window', () => expect(planProblem({ ...plan, start: '09:00', end: '09:00' })).toMatch(/same/))
  it('needs a window in a custom plan', () => expect(planProblem({ ...plan, preset: 'custom', windows: [] })).toMatch(/at least one window/))
  it('needs days in every daily window', () =>
    expect(planProblem({ ...plan, preset: 'custom', windows: [{ days: [], startTime: '08:00', endTime: '09:00' }] })).toMatch(/needs at least one day/))
})

describe('descriptions', () => {
  it.each([
    [{ schedules: [{ days: [1, 2, 3, 4, 5], startTime: '08:00', endTime: '20:00', timezone: 'UTC' }] }, 'Weekdays 08:00–20:00 · UTC'],
    [{ schedules: [{ days: [0, 6], startTime: '22:00', endTime: '06:00', timezone: 'UTC' }] }, 'Weekends 22:00–06:00 (overnight) · UTC'],
    [{ schedules: [{ days: [0, 1, 2, 3, 4, 5, 6], startTime: '00:00', endTime: '12:00', timezone: 'UTC' }] }, 'Every day 00:00–12:00 · UTC'],
    [{ schedules: [{ days: [3, 1], startTime: '08:00', endTime: '09:00', timezone: 'UTC' }] }, 'Mon, Wed 08:00–09:00 · UTC'],
    [{ schedules: [{ startDay: 1, startTime: '00:00', endDay: 5, endTime: '23:59', timezone: 'UTC' }] }, 'Mon 00:00 → Fri 23:59, non-stop · UTC'],
    [{ activation: 'OnDemand' as const }, 'Runs only while a schedule that depends on it is up, or when started manually.'],
    [{}, 'No schedule: always on'],
  ])('describes %j', (spec, text) => expect(describeSpec(spec)).toBe(text))
  it('counts custom windows', () => expect(describePlan({ ...defaultPlan('UTC'), preset: 'custom' })).toBe('1 custom window'))
})

describe('when', () => {
  const now = new Date(2026, 9, 22, 10, 0) // a Thursday, local time
  const at = (d: number, h: number, m = 0) => new Date(2026, 9, d, h, m).toISOString()
  it('counts minutes in the next hour', () => expect(when(at(22, 10, 25), now)).toBe('in 25 min'))
  it('never says "in 0 min"', () => expect(when(at(22, 10, 0), now)).toBe('in 1 min'))
  it('counts hours within half a day', () => expect(when(at(22, 13), now)).toMatch(/^in 3 h, at /))
  it('names tomorrow', () => expect(when(at(23, 8), now)).toMatch(/^tomorrow /))
  it('names a weekday within the week', () => expect(when(at(26, 8), now)).toMatch(/^Mon /))
  it('says today for a past time earlier today', () => expect(when(at(22, 7), now)).toMatch(/^today /))
})

describe('statusLine', () => {
  it('says the operator has not caught up with a new spec', () => {
    const conditions = [{ type: 'Ready', status: 'True', observedGeneration: 1 }]
    expect(pendingChange(2, conditions as never)).toBe(true)
    expect(statusLine({ phase: 'ScaledUp', generation: 2, status: { conditions } as never }).title).toBe('Applying changes…')
  })
  it('shows progress while starting', () => {
    expect(statusLine({ phase: 'ScalingUp', status: { namespacesReady: 1, namespacesTotal: 3 } as never })).toEqual({ tone: 'busy', title: 'Starting…', detail: '1 of 3 ready' })
  })
  it('names the dependencies it waits for', () => {
    expect(statusLine({ phase: 'WaitingForDependencies', dependsOn: ['db', 'cache'] }).title).toBe('Waiting for db, cache to start')
  })
  it('explains a manual override and when it ends', () => {
    const line = statusLine({ phase: 'ScaledUp', status: { mode: 'ManualUp', desiredState: 'Up' } as never })
    expect(line).toEqual({ tone: 'manual', title: 'Kept up manually', detail: 'until you hand it back to the schedule' })
  })
  it('says why a target is up without a schedule', () => {
    expect(statusLine({ phase: 'ScaledUp', status: { mode: 'AlwaysOn', desiredState: 'Up' } as never }).detail).toBe('no schedule set')
  })
  it('says who needs a dependency', () => {
    expect(statusLine({ phase: 'ScaledUp', status: { mode: 'Dependency', requiredBy: ['shop'] } as never }).detail).toBe('needed by shop')
  })
  it('follows the desired state on a schedule', () => {
    expect(statusLine({ phase: 'ScaledDown', status: { mode: 'Schedule', desiredState: 'Down' } as never })).toEqual({ tone: 'down', title: 'Down', detail: undefined })
  })
})

describe('describeOverride', () => {
  it.each([
    [true, 'nextTransition', 'Starting shop-dev until the next scheduled change'],
    [true, '1h', 'Starting shop-dev for 1 hour'],
    [false, '24h', 'Stopping shop-dev for 24 hours'],
    [false, 'forever', 'Stopping shop-dev until you hand it back to the schedule'],
    [true, undefined, 'Starting shop-dev until you hand it back to the schedule'],
    [null, undefined, 'shop-dev follows its schedule again'],
  ] as const)('active=%s until=%s', (active, until, text) => expect(describeOverride('shop-dev', active, until)).toBe(text))
})
