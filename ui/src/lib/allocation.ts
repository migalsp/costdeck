// Cost allocation: a period's cost charged to teams, environments, namespaces or the values
// of a namespace label (showback and chargeback). Mirrors AllocationReport in
// internal/finops/allocation.go and AllocationSettings in internal/api/allocation_handlers.go.

export interface AllocationSettings { idle: 'separate' | 'proportional'; sharedNamespaces: string[]; sharedSplit: 'proportional' | 'even' }

export interface AllocationRow {
  key: string
  namespaces: string[]
  compute: number
  storage: number
  network: number
  shared: number
  idle: number
  total: number
}

export interface AllocationReport {
  currency: string
  from: string
  to: string
  by: string
  policy: Partial<AllocationSettings>
  days: number
  rows: AllocationRow[]
  sharedNamespaces: string[]
  unallocated: number
  total: number
}

export type AllocationPeriod = 'this-month' | 'last-month' | 'last-7-days' | 'last-30-days'
export const allocationPeriods: { id: AllocationPeriod; label: string }[] = [
  { id: 'this-month', label: 'This month' },
  { id: 'last-month', label: 'Last month' },
  { id: 'last-7-days', label: 'Last 7 days' },
  { id: 'last-30-days', label: 'Last 30 days' },
]

export type AllocationGroup = 'team' | 'environment' | 'namespace' | 'label'

// allocationQuery is the API query for a period and a group; a label group needs its key.
export function allocationQuery(period: AllocationPeriod, group: AllocationGroup, labelKey: string): string | null {
  const by = group === 'label' ? (labelKey.trim() ? `label:${labelKey.trim()}` : null) : group
  return by ? `by=${encodeURIComponent(by)}&period=${period}` : null
}

// policySummary says in a line how shared and idle costs were charged.
export function policySummary(r: Pick<AllocationReport, 'policy' | 'sharedNamespaces'>): string {
  const shared = r.sharedNamespaces.length
    ? `${r.sharedNamespaces.join(', ')} shared out ${r.policy.sharedSplit === 'even' ? 'evenly' : 'by what each namespace requests'}`
    : 'no shared namespaces'
  const idle = r.policy.idle === 'proportional' ? 'unrequested capacity charged by requests' : 'unrequested capacity on a line of its own'
  return `${shared}; ${idle}`
}

// The settings form keeps the shared namespaces as the text typed.
export interface AllocationForm { idle: AllocationSettings['idle']; shared: string; sharedSplit: AllocationSettings['sharedSplit'] }

export const allocationForm = (s?: AllocationSettings): AllocationForm =>
  ({ idle: s?.idle || 'separate', shared: (s?.sharedNamespaces || []).join(', '), sharedSplit: s?.sharedSplit || 'proportional' })

export const sharedList = (text: string) => [...new Set(text.split(/[\s,]+/).map(s => s.trim()).filter(Boolean))]

export const allocationRequest = (f: AllocationForm) => ({ idle: f.idle, sharedSplit: f.sharedSplit, sharedNamespaces: sharedList(f.shared) })

export function allocationProblem(f: AllocationForm): string | null {
  const bad = sharedList(f.shared).find(p => !/^[a-z0-9*?[\]-]+$/.test(p) || (p.match(/\[/g) || []).length !== (p.match(/]/g) || []).length)
  return bad ? `Cost allocation: "${bad}" is not a namespace name or a pattern such as kube-*` : null
}
