// Preview cleanup deletes namespaces some time after they were created (spec.namespaceTTL).
// Mirrors NamespaceTTLSettings and namespaceTTLResponse in internal/api/namespace_ttl.go.
import { shortDuration } from './schedule'

export interface TTLSettings { enabled: boolean; dryRun: boolean; selector: string; ttl?: string; warnBefore?: string }

export interface TTLEntry {
  namespace: string
  createdAt: string
  expiresAt: string
  source: 'ttl' | 'label' | 'extended'
  warned: boolean
  protected?: string
}

export interface TTLDeletion { namespace: string; createdAt: string; expiresAt: string; deletedAt: string }

export interface TTLStatus extends TTLSettings {
  permitted: boolean
  error?: string
  namespaces: TTLEntry[]
  deleted: TTLDeletion[]
}

export const TTL_CHOICES = ['1d', '3d', '7d', '14d', '30d']
export const WARN_CHOICES: { value: string; label: string }[] = [
  { value: '0s', label: 'no warning' },
  { value: '1h', label: '1 hour before' },
  { value: '24h', label: '1 day before' },
  { value: '3d', label: '3 days before' },
]

// days writes a duration as the form offers it: "168h0m0s" is "7d".
export function days(d?: string): string {
  const s = shortDuration(d)
  const m = /^(\d+)h$/.exec(s)
  if (m && Number(m[1]) % 24 === 0) return `${Number(m[1]) / 24}d`
  return s
}

export interface CleanupForm { enabled: boolean; dryRun: boolean; selector: string; ttl: string; warnBefore: string; understood: boolean }

export function cleanupForm(s?: TTLSettings): CleanupForm {
  const warn = s?.warnBefore ? days(s.warnBefore) : '24h'
  return {
    // Never set up: the first save starts in a dry run.
    enabled: !!s?.enabled, dryRun: s && (s.enabled || s.selector) ? s.dryRun : true, selector: s?.selector || '', ttl: s?.ttl ? days(s.ttl) : '7d',
    warnBefore: /^0[a-z]$/.test(warn) ? '0s' : warn === '1d' ? '24h' : warn, understood: false,
  }
}

export const cleanupRequest = (f: CleanupForm) => ({ enabled: f.enabled, dryRun: f.dryRun, selector: f.selector.trim(), ttl: f.ttl, warnBefore: f.warnBefore })

// deletes reports whether the form, once saved, deletes namespaces.
export const deletes = (f: { enabled: boolean; dryRun: boolean }) => f.enabled && !f.dryRun

// cleanupProblem names what keeps the form from saving. Turning deleting on asks for the
// confirmation every time it was off before.
export function cleanupProblem(f: CleanupForm, saved?: TTLSettings): string | null {
  if (!f.enabled) return null
  if (!f.selector.trim()) return 'Preview cleanup: enter a label selector, such as env=preview'
  if (!/^\d+[dhm]$|^\d+h\d+m$/.test(f.ttl.trim())) return 'Preview cleanup: the time to live is a duration such as 7d or 36h'
  if (deletes(f) && !(saved && deletes(saved)) && !f.understood) return 'Preview cleanup: tick that deleted namespaces cannot be restored'
  return null
}
