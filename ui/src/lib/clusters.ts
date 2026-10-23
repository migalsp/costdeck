// Other Cost Deck installations shown next to this one (GET /api/clusters). Mirrors
// clustersResponse and ClusterSettings in internal/api/clusters.go.

export interface ClusterSummary {
  name: string
  generatedAt: string
  currency: string
  nodes: number
  namespaces: number
  monthlyCost: number
  monthlyRequested: number
  monthlyUsed: number
  monthlyUnallocated: number
  schedulesSaving: number
  savingsAvailable: number
  monthToDate: number
  forecast: number
  schedules: number
  schedulesDown: number
}

export interface ClusterRow { name: string; url?: string; local: boolean; error?: string; summary?: ClusterSummary }
export interface ClusterTotals { currency: string; clusters: number; nodes: number; monthlyCost: number; schedulesSaving: number; savingsAvailable: number; monthToDate: number; forecast: number }
export interface ClustersResponse { clusters: ClusterRow[]; totals: ClusterTotals[] }

export interface ClusterSettings { name: string; url: string; skipTlsVerify: boolean; hasToken: boolean }

// The settings form: tokens the person typed, never read back.
export interface ClusterForm { name: string; url: string; token: string; skipTlsVerify: boolean; hasToken: boolean }

export const clustersForm = (s?: ClusterSettings[]): ClusterForm[] => (s || []).map(c => ({ ...c, token: '' }))

export const clustersRequest = (f: ClusterForm[]) =>
  f.map(c => ({ name: c.name.trim(), url: c.url.trim().replace(/\/+$/, ''), skipTlsVerify: c.skipTlsVerify, ...(c.token.trim() ? { token: c.token.trim() } : {}) }))

export function clustersProblem(f: ClusterForm[]): string | null {
  const names = new Set<string>()
  for (const c of f) {
    const name = c.name.trim()
    if (!/^[a-z0-9]([-a-z0-9]*[a-z0-9])?$/.test(name)) return `Clusters: "${name || '(empty)'}" must use lowercase letters, digits and dashes`
    if (names.has(name)) return `Clusters: ${name} is listed twice`
    names.add(name)
    if (!/^https?:\/\/[^\s/]+/.test(c.url.trim())) return `Clusters: the URL of ${name} starts with https://`
    if (!c.hasToken && !c.token.trim()) return `Clusters: ${name} needs a viewer API token of that installation`
  }
  return null
}

// usedShare is the share of the node cost pods use, for the table.
export const usedShare = (s: ClusterSummary) => (s.monthlyRequested + s.monthlyUnallocated > 0 ? s.monthlyUsed / (s.monthlyRequested + s.monthlyUnallocated) : undefined)
