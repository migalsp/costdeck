import { apiError } from './api'
import type { Tone } from '../components/ui'

// Mirrors internal/finops (GET /api/finops/overview). Amounts are in `currency`; "monthly"
// means the current run rate over an average month of 730 hours.

export type Environment = 'production' | 'non-production' | 'system' | 'unclassified'

export interface Resource { requested: number; used: number; limit: number }

export interface NamespaceRow {
  name: string
  labels?: Record<string, string>
  environment: Environment
  team?: string
  pods: number
  collected: boolean
  cpu: Resource
  memoryGiB: Resource
  monthlyCost: number
  monthlyUsedCost: number
  monthlyIdle: number
  monthlyStorage: number
  monthlyNetwork: number
  monthlyTotal: number
  efficiency?: number
  insights: string[]
  schedule?: { kind: 'group' | 'config'; name: string; desiredState?: string; mode?: string; savedMonthly: number }
  rightsizingMonthly?: number
  scheduleSavingMonthly?: number
  cost7d?: number
  costPrev7d?: number
  trend?: number[]
}

export interface Opportunity {
  kind: 'rightsizing' | 'schedule' | 'idle-capacity' | 'cost-increase' | 'missing-requests'
    | 'consolidation' | 'node-shape' | 'pending-pods' | 'node-not-ready' | 'node-pressure' | 'no-spot' | 'node-type'
    | 'unused-volumes' | 'orphaned-volumes' | 'idle-load-balancers'
  namespace?: string
  title: string
  detail: string
  monthlySavings?: number
}

export interface HistoryDay {
  date: string
  hours: number
  provisioned: number
  requested: number
  used: number
  saved: number
  storage: number
  network: number
  namespaces: Record<string, number>
}

export interface Overview {
  generatedAt: string
  currency: string
  rates: { cpuCoreHour: number; memoryGiBHour: number; currency: string; basis: string }
  cluster: {
    nodes: number; spotNodes: number; cpuCores: number; memoryGiB: number
    cpuRequested: number; memoryRequestedGiB: number; cpuUsed: number; memoryUsedGiB: number
    provisionedHourly: number; requestedHourly: number; usedHourly: number
  }
  monthly: {
    provisioned: number; requested: number; used: number; unallocated: number; overprovisioned: number
    storage: number; network: number; total: number; unusedStorage: number; idleNetwork: number
  }
  savings: { schedulesMonthly: number; rightsizingMonthly: number; rightsizingPending: number; scheduleCandidatesMonthly: number }
  monthToDate: { month: string; cost: number; saved: number; hours: number; forecast: number; partial: boolean }
  history: { since?: string; top: string[]; days: HistoryDay[] }
  namespaces: NamespaceRow[]
  opportunities: Opportunity[]
  infraError?: string
}

export const OTHER = '__other__'

export async function fetchOverview(): Promise<Overview> {
  const res = await fetch('/api/finops/overview')
  if (!res.ok) throw new Error(await apiError(res))
  return res.json()
}

export const environmentLabel: Record<Environment, string> = {
  production: 'Production',
  'non-production': 'Non-production',
  system: 'System',
  unclassified: 'Unclassified',
}

// couldSave is what acting on a namespace's advice would save per month.
export const couldSave = (ns: NamespaceRow) => (ns.rightsizingMonthly || 0) + (ns.scheduleSavingMonthly || 0)

// weekChange is the change of the last seven days against the seven before, or undefined
// until the history covers both weeks.
export function weekChange(ns: NamespaceRow): number | undefined {
  if (ns.cost7d === undefined || !ns.costPrev7d) return undefined
  return ns.cost7d / ns.costPrev7d - 1
}

export const isDown = (ns: NamespaceRow) => ns.schedule?.desiredState === 'Down'

// Categorical series colours for per-namespace charts, in fixed order (validated for
// colour-vision deficiency on white); everything past them folds into neutral "Other".
export const seriesColors = ['#2a78d6', '#eb6834', '#1baf7a', '#eda100', '#e87ba4']
export const otherColor = '#94a3b8'

// The cost funnel is one ordinal emerald ramp, darkest for what is used.
export const funnelColors = { used: '#064e3b', idle: '#047857', unallocated: '#10b981' }

export const percent = (v: number | undefined, digits = 0) =>
  v === undefined || !isFinite(v) ? '—' : `${(v * 100).toFixed(digits)}%`

// downloadCsv saves rows as a CSV file named after today's date.
export function downloadCsv(name: string, header: string[], rows: (string | number)[][]) {
  const escape = (v: string | number) => {
    const s = String(v)
    return /[",\n]/.test(s) ? `"${s.replace(/"/g, '""')}"` : s
  }
  const csv = [header, ...rows].map(r => r.map(escape).join(',')).join('\n')
  const url = URL.createObjectURL(new Blob([csv], { type: 'text/csv;charset=utf-8' }))
  const a = document.createElement('a')
  a.href = url
  a.download = `${name}-${new Date().toISOString().slice(0, 10)}.csv`
  a.click()
  URL.revokeObjectURL(url)
}

// ─── Nodes (GET /api/finops/nodes) ──────────────────────────────────────────

export interface NodeResource { capacity: number; allocatable: number; requested: number; used: number }

export interface NodeView {
  name: string
  status: 'Ready' | 'NotReady' | 'Unknown'
  unschedulable: boolean
  pressure?: string[]
  pool?: string
  instanceType?: string
  zone?: string
  region?: string
  capacityType: 'spot' | 'on-demand'
  arch?: string
  os?: string
  kernel?: string
  kubelet?: string
  createdAt: string
  pods: number
  maxPods: number
  metricsAvailable: boolean
  cpu: NodeResource
  memoryGiB: NodeResource
  monthlyCost: number
  monthlyRequested: number
  monthlyUsed: number
  namespaces: { namespace: string; pods: number; cpu: number; memoryGiB: number }[]
  consolidationCandidate?: boolean
  daemonSetCpu: number
  daemonSetMemoryGiB: number
}

export interface PoolSummary {
  name: string
  nodes: number
  spot: number
  instanceTypes: string[]
  cpu: NodeResource
  memoryGiB: NodeResource
  monthlyCost: number
}

export interface NodesReport {
  generatedAt: string
  currency: string
  rates: Overview['rates']
  summary: {
    nodes: number; ready: number; spot: number; unschedulable: number; pendingPods: number; pods: number; maxPods: number
    cpu: NodeResource; memoryGiB: NodeResource; monthlyCost: number; monthlyUnrequested: number
  }
  pools: PoolSummary[]
  nodes: NodeView[]
  findings: Opportunity[]
  recommendations: NodeRecommendation[]
  spot?: { spotShare: number; nonProductionMonthly: number; monthlySavings: number }
}

export interface ShapeOption { type: string; nodes: number; monthly: number; monthlySavings: number }

export interface NodeRecommendation {
  pool: string
  currentType: string
  currentNodes: number
  currentMonthly: number
  type: string
  family: string
  nodes: number
  monthly: number
  monthlySavings: number
  requestedCpu: number
  requestedMemoryGiB: number
  reason: string
  arm?: ShapeOption
}

export interface NodePod {
  namespace: string; name: string; phase: string; daemonSet: boolean
  cpuRequest: number; cpuUsed: number; memoryRequestGiB: number; memoryUsedGiB: number; monthlyCost: number
}

export async function fetchNodes(): Promise<{ k8sVersion: string; report: NodesReport }> {
  const res = await fetch('/api/finops/nodes')
  if (!res.ok) throw new Error(await apiError(res))
  return res.json()
}

export async function fetchNodePods(name: string): Promise<NodePod[]> {
  const res = await fetch(`/api/finops/nodes/${encodeURIComponent(name)}/pods`)
  if (!res.ok) throw new Error(await apiError(res))
  return res.json()
}

// ─── Reports (/api/reports) ─────────────────────────────────────────────────

export type Audience = 'executive' | 'engineering' | 'showback'

export interface ReportEntry {
  id: string
  kind: 'ai' | 'digest'
  title: string
  audience?: Audience
  period?: 'week' | 'month'
  generatedAt: string
  by?: string
  delivered?: string
}

export interface DigestSchedule { enabled?: boolean; frequency?: 'weekly' | 'monthly'; time?: string; timezone?: string }

export interface ReportsIndex {
  reports: ReportEntry[]
  schedule: DigestSchedule
  webexReady: boolean
  lastSent?: string
  nextDue?: string
}

export interface DigestRow { name: string; cost: number; prev: number }

export interface Digest {
  period: 'week' | 'month'
  title: string
  from: string; to: string; prevFrom: string; prevTo: string
  generatedAt: string
  currency: string
  hours: number; prevHours: number; complete: boolean
  cost: number; prevCost: number; requested: number; used: number; saved: number; prevSaved: number
  runRate: number
  monthToDate: Overview['monthToDate']
  namespaces: DigestRow[]
  teams: DigestRow[]
  opportunities: Opportunity[]
}

export async function fetchReports(): Promise<ReportsIndex> {
  const res = await fetch('/api/reports')
  if (!res.ok) throw new Error(await apiError(res))
  return res.json()
}

export async function fetchDigest(period: 'week' | 'month'): Promise<{ digest: Digest; markdown: string }> {
  const res = await fetch(`/api/reports/digest?period=${period}`)
  if (!res.ok) throw new Error(await apiError(res))
  return res.json()
}

export async function fetchReport(id: string): Promise<{ report: ReportEntry; markdown: string }> {
  const res = await fetch(`/api/reports/${encodeURIComponent(id)}`)
  if (!res.ok) throw new Error(await apiError(res))
  return res.json()
}

// downloadText saves text as a file.
export function downloadText(filename: string, text: string, type = 'text/markdown;charset=utf-8') {
  const url = URL.createObjectURL(new Blob([text], { type }))
  const a = document.createElement('a')
  a.href = url
  a.download = filename
  a.click()
  URL.revokeObjectURL(url)
}

// change is the relative change from prev to cur, or undefined without a base.
export const change = (cur: number, prev?: number) => (prev ? cur / prev - 1 : undefined)

// efficiencyRating rates used cost over the node bill. Most clusters use well under a
// fifth of what they pay for; half is good.
export function efficiencyRating(v?: number): { label: string; tone: Tone; title: string } | undefined {
  if (v === undefined || !isFinite(v)) return undefined
  const title = 'Used cost over the node bill. Under 20% is common and low; 50% or more is good.'
  if (v >= 0.5) return { label: 'Good', tone: 'success', title }
  if (v >= 0.2) return { label: 'Fair', tone: 'neutral', title }
  return { label: 'Low', tone: 'warning', title }
}

// ─── Storage and network (GET /api/finops/infra) ────────────────────────────

export type VolumeState = 'in-use' | 'unused' | 'scaled-down' | 'orphaned'

export interface Volume {
  namespace?: string
  claim?: string
  volume?: string
  storageClass?: string
  diskType?: string
  phase: string
  sizeGiB: number
  monthlyCost: number
  local?: boolean
  mountedBy: string[]
  state: VolumeState
  basis: string
}

export interface LoadBalancer {
  namespace: string
  name: string
  address?: string
  ports: string[]
  readyEndpoints: number
  monthlyCost: number
  state: 'serving' | 'idle' | 'scaled-down'
  basis: string
}

export interface InfraResponse {
  currency: string
  cloud: string
  error?: string
  infra: {
    volumes: Volume[]
    loadBalancers: LoadBalancer[]
    storageMonthly: number
    networkMonthly: number
    unusedStorageMonthly: number
    idleNetworkMonthly: number
    networkBasis: string
  }
}

export async function fetchInfra(): Promise<InfraResponse> {
  const res = await fetch('/api/finops/infra')
  if (!res.ok) throw new Error(await apiError(res))
  return res.json()
}

// ─── Budgets (GET /api/budgets) ─────────────────────────────────────────────

export type BudgetScope = 'cluster' | 'namespace' | 'team' | 'environment'

export interface Budget {
  name: string
  scope: BudgetScope
  // value names the team or environment; budgets saved before there could be several
  // namespaces keep their one namespace here.
  value?: string
  namespaces?: string[]
  monthlyLimit: string
  thresholds?: number[]
  forecast?: boolean
}

export interface BudgetStatus {
  budget: Budget
  limit: number
  spent: number
  forecast: number
  hourlyNow: number
  state: 'ok' | 'at-risk' | 'over'
  namespaces: string[]
  partial: boolean
}

export interface AlertEvent {
  at: string
  kind: 'budget' | 'forecast' | 'anomaly'
  title: string
  detail: string
  budget?: string
  namespace?: string
  delivered?: string
}

export interface AnomalySettings { enabled?: boolean; percent?: number; minimumDaily?: string }

export interface BudgetsResponse {
  currency: string
  budgets: BudgetStatus[]
  anomalies: AnomalySettings
  events: AlertEvent[]
  webexReady: boolean
  scopes: { namespaces: string[]; teams: string[] }
}

export async function fetchBudgets(): Promise<BudgetsResponse> {
  const res = await fetch('/api/budgets')
  if (!res.ok) throw new Error(await apiError(res))
  return res.json()
}

// saveSettings sends a partial settings update.
export async function saveSettings(body: unknown): Promise<void> {
  const res = await fetch('/api/settings', { method: 'PUT', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify(body) })
  if (!res.ok) throw new Error(await apiError(res))
}
