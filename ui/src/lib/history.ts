// Usage history windows for the namespace charts. The operator keeps the last hour itself;
// longer windows come from VictoriaMetrics and reach back as far as its lookback window
// (retentionDays). The API answers with the window it actually covered in X-History-Range.

export interface HistorySource {
  enabled: boolean
  retentionDays?: number
}

export const rangeHours = (r: string) => (parseInt(r, 10) || 1) * (r.endsWith('d') ? 24 : 1)

// historyRanges lists the windows worth offering, shortest first.
export function historyRanges(vm?: HistorySource | null): string[] {
  if (!vm?.enabled) return ['1h']
  const days = vm.retentionDays || 7
  const out = ['1h', '24h', '7d', '14d', '30d'].filter(r => rangeHours(r) <= days * 24)
  if (days > 1 && !out.includes(`${days}d`)) out.push(`${days}d`)
  return out.sort((a, b) => rangeHours(a) - rangeHours(b))
}

// describeRange reads "1h", "24h" or "7d" as "60 minutes", "24 hours" or "7 days".
export function describeRange(r: string): string {
  const n = parseInt(r, 10) || 1
  if (r.endsWith('d')) return n === 1 ? '24 hours' : `${n} days`
  return n === 1 ? '60 minutes' : `${n} hours`
}

// pointLabel names a point on the time axis: the time of day for a day or less, the date
// and hour beyond that.
export function pointLabel(t: Date, range: string): string {
  const hhmm = `${String(t.getHours()).padStart(2, '0')}:${String(t.getMinutes()).padStart(2, '0')}`
  return rangeHours(range) <= 24 ? hhmm : `${t.toLocaleDateString(undefined, { month: 'short', day: 'numeric' })} ${hhmm}`
}
