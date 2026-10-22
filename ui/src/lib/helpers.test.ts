import { afterEach, describe, expect, it, vi } from 'vitest'
import { quantity } from './quantity'
import { formatMoney } from './format'
import { describeRange, historyRanges, pointLabel, rangeHours } from './history'
import { ago, relativeTime } from './time'
import { cloudLabel, targetLabel } from './cloud'
import { apiError, errorMessage } from './api'
import { change, couldSave, efficiencyRating, isDown, percent, weekChange, type NamespaceRow } from './finops'

describe('quantity', () => {
  it.each([
    ['250m', 0.25], ['2', 2], ['1.5Gi', 1.5 * 2 ** 30], ['500M', 5e8], ['1e3', 1000], ['512Ki', 512 * 1024],
    [' 100Mi ', 100 * 2 ** 20], ['.5', 0.5], ['-1', -1],
  ])('reads %s', (v, want) => expect(quantity(v)).toBeCloseTo(want))
  it.each([[undefined], [''], ['abc'], ['10X'], ['1.2.3']])('counts %s as zero', v => expect(quantity(v)).toBe(0))
})

describe('formatMoney', () => {
  it('shows cents only for small amounts', () => {
    expect(formatMoney(3.456, 'USD')).toMatch(/3\.46/)
    expect(formatMoney(1234.56, 'USD')).not.toMatch(/\.56/)
  })
  it('falls back to a plain number for an unknown currency', () => {
    expect(formatMoney(5, 'NOT-A-CURRENCY')).toBe('5.00 NOT-A-CURRENCY')
  })
})

describe('history ranges', () => {
  it('reads ranges in hours', () => {
    expect(rangeHours('1h')).toBe(1)
    expect(rangeHours('24h')).toBe(24)
    expect(rangeHours('7d')).toBe(168)
    expect(rangeHours('junk')).toBe(1)
  })
  it('offers only the last hour without VictoriaMetrics', () => {
    expect(historyRanges(null)).toEqual(['1h'])
    expect(historyRanges({ enabled: false, retentionDays: 30 })).toEqual(['1h'])
  })
  it('offers windows up to the lookback, including the lookback itself', () => {
    expect(historyRanges({ enabled: true, retentionDays: 10 })).toEqual(['1h', '24h', '7d', '10d'])
    expect(historyRanges({ enabled: true, retentionDays: 30 })).toEqual(['1h', '24h', '7d', '14d', '30d'])
    expect(historyRanges({ enabled: true })).toEqual(['1h', '24h', '7d'])
  })
  it('describes ranges in words', () => {
    expect(describeRange('1h')).toBe('60 minutes')
    expect(describeRange('6h')).toBe('6 hours')
    expect(describeRange('1d')).toBe('24 hours')
    expect(describeRange('14d')).toBe('14 days')
  })
  it('labels points with the time for a day, and the date beyond', () => {
    const t = new Date(2026, 9, 22, 9, 5)
    expect(pointLabel(t, '24h')).toBe('09:05')
    expect(pointLabel(t, '7d')).toMatch(/22.* 09:05$/)
  })
})

describe('time', () => {
  afterEach(() => vi.useRealTimers())
  it('says how long until a future moment', () => {
    vi.useFakeTimers({ now: new Date('2026-10-22T10:00:00Z') })
    expect(relativeTime('2026-10-22T09:00:00Z')).toBe('now')
    expect(relativeTime('2026-10-22T10:20:00Z')).toBe('in 20m')
    expect(relativeTime('2026-10-22T13:30:00Z')).toBe('in 3h 30m')
    expect(relativeTime('2026-10-25T12:00:00Z')).toBe('in 3d 2h')
  })
  it('says how long ago a past moment was', () => {
    const now = new Date(2026, 9, 22, 15, 0)
    expect(ago(new Date(2026, 9, 22, 14, 59, 50).toISOString(), now)).toBe('just now')
    expect(ago(new Date(2026, 9, 22, 14, 48).toISOString(), now)).toBe('12 min ago')
    expect(ago(new Date(2026, 9, 22, 12, 0).toISOString(), now)).toBe('3 h ago')
    expect(ago(new Date(2026, 9, 21, 12, 0).toISOString(), now)).toBe('yesterday')
    expect(ago(new Date(2026, 9, 18, 12, 0).toISOString(), now)).toBe('4 days ago')
  })
})

describe('cloud labels', () => {
  it('names the providers', () => {
    expect(cloudLabel('aws')).toBe('AWS')
    expect(cloudLabel('azure')).toBe('Azure')
    expect(cloudLabel('gcp')).toBe('Google Cloud')
    expect(cloudLabel()).toBe('Cloud')
  })
  it('shows a known name, or the last part of a long identifier', () => {
    expect(targetLabel('db-1', [{ identifier: 'db-1', name: 'Orders DB' }])).toBe('Orders DB')
    expect(targetLabel('/subscriptions/x/resourceGroups/rg/providers/Microsoft.DBforPostgreSQL/flexibleServers/pg1')).toBe('pg1')
  })
})

describe('api helpers', () => {
  it('reads the server error message from JSON', async () => {
    expect(await apiError(new Response('{"error":"name taken"}', { status: 409 }))).toBe('name taken')
  })
  it('falls back to the body, then the status text', async () => {
    expect(await apiError(new Response('plain failure', { status: 500 }))).toBe('plain failure')
    expect(await apiError(new Response('', { status: 503, statusText: 'Service Unavailable' }))).toBe('Service Unavailable')
  })
  it('turns anything thrown into text', () => {
    expect(errorMessage(new Error('boom'))).toBe('boom')
    expect(errorMessage('plain')).toBe('plain')
  })
})

describe('finops helpers', () => {
  const ns = (patch: Partial<NamespaceRow>) => patch as NamespaceRow
  it('adds up what acting on the advice would save', () => {
    expect(couldSave(ns({ rightsizingMonthly: 10, scheduleSavingMonthly: 5 }))).toBe(15)
    expect(couldSave(ns({}))).toBe(0)
  })
  it('compares the last week with the one before once both exist', () => {
    expect(weekChange(ns({ cost7d: 120, costPrev7d: 100 }))).toBeCloseTo(0.2)
    expect(weekChange(ns({ cost7d: 120 }))).toBeUndefined()
    expect(weekChange(ns({ cost7d: 120, costPrev7d: 0 }))).toBeUndefined()
  })
  it('knows a namespace a schedule keeps down', () => {
    expect(isDown(ns({ schedule: { desiredState: 'Down' } as NamespaceRow['schedule'] }))).toBe(true)
    expect(isDown(ns({}))).toBe(false)
  })
  it('formats shares', () => {
    expect(percent(0.256)).toBe('26%')
    expect(percent(0.256, 1)).toBe('25.6%')
    expect(percent(undefined)).toBe('—')
    expect(percent(Infinity)).toBe('—')
  })
  it('computes a relative change only against something', () => {
    expect(change(150, 100)).toBeCloseTo(0.5)
    expect(change(150, 0)).toBeUndefined()
    expect(change(150)).toBeUndefined()
  })
  it('rates efficiency', () => {
    expect(efficiencyRating(0.6)?.label).toBe('Good')
    expect(efficiencyRating(0.3)?.label).toBe('Fair')
    expect(efficiencyRating(0.1)?.label).toBe('Low')
    expect(efficiencyRating(undefined)).toBeUndefined()
  })
})
