// Shapes of the REST API responses shared by several pages. Field names mirror the Go
// JSON tags in internal/api and api/v1.

export interface CostEstimate {
  hourlyCost: number
  monthlyCost: number
  currency: string
  determinedBy: string
}

export interface ResourceValues {
  cpuRequest?: string
  cpuLimit?: string
  memoryRequest?: string
  memoryLimit?: string
}

export interface WorkloadOptimization {
  name: string
  kind: string
  original: ResourceValues
  optimized: ResourceValues
}

// OptimizationStatus is GET /api/namespaces/{ns}/optimization.
export interface OptimizationStatus {
  active: boolean
  optimizedAt?: string
  workloads?: WorkloadOptimization[]
}

export interface ScalingSchedule {
  days?: number[]
  startDay?: number
  endDay?: number
  startTime: string
  endTime: string
  timezone?: string
}

// ScheduleException keeps a target up or down on given dates, whatever the schedules
// say: "2026-12-24" (a whole day) or "2026-12-24T18:00". A manual override still wins.
export interface ScheduleException {
  name?: string
  start: string
  end?: string
  state: DesiredState
  timezone?: string
}

// HolidayCalendar is a shared list of days (Settings → Holiday calendars) that schedules
// name in spec.calendars.
export interface HolidayCalendar {
  name: string
  timezone?: string
  state?: DesiredState
  days: { date: string; name?: string }[]
}

// ExternalTarget is a cloud resource scaled alongside a group; the discovery endpoints
// return the same shape.
export interface ExternalTarget {
  provider: string
  type: string
  identifier: string
  region: string
  executeAfter?: string
  status?: string
  name?: string
}

export type DesiredState = 'Up' | 'Down'

// ScheduleStatus mirrors what the operator reports about who is in control.
export interface ScheduleStatus {
  mode?: 'Schedule' | 'ManualUp' | 'ManualDown' | 'Exception' | 'AlwaysOn' | 'Dependency' | 'OnDemand'
  desiredState?: DesiredState
  // The exception or calendar day in force while mode is Exception, and when it ends.
  exception?: string
  exceptionEndsAt?: string
  overrideExpiresAt?: string
  nextTransition?: { time: string; desiredState: DesiredState }
  estimatedHourlySavings?: string
  currency?: string
}

// LabelSelector is Kubernetes' label selector, as spec.namespaceSelector stores it.
export interface LabelSelector {
  matchLabels?: Record<string, string>
  matchExpressions?: { key: string; operator: 'In' | 'NotIn' | 'Exists' | 'DoesNotExist'; values?: string[] }[]
}

// HibernateRule stops a service an operator runs, such as a database, with the namespace:
// after its workloads, and starts it before them. A preset, or apiVersion, kind and
// replicasField for another operator.
export interface HibernateRule {
  preset?: 'cnpg' | 'zalando-postgres' | 'strimzi'
  apiVersion?: string
  kind?: string
  replicasField?: string
  readyField?: string
  names?: string[]
}

export interface ScalingGroupSpec {
  category: string
  // Namespaces listed by name; groupNamespaces() adds those the selector matched.
  namespaces?: string[]
  namespaceSelector?: LabelSelector
  active?: boolean
  activeUntil?: string
  schedules?: ScalingSchedule[]
  exceptions?: ScheduleException[]
  calendars?: string[]
  sequence?: string[]
  exclusions?: string[]
  externalTargets?: ExternalTarget[]
  featureFlags?: {
    skipOnTimeout: boolean
    timeoutMinutes: number
  }
  dependsOn?: string[]
  activation?: 'Schedule' | 'OnDemand'
  // dryRun works out what the schedule would do without scaling anything.
  dryRun?: boolean
  // pauseGitOps pauses Flux and Argo CD while the schedule keeps the workloads down.
  pauseGitOps?: boolean
  // hibernate stops operator-run services in every namespace of the group.
  hibernate?: HibernateRule[]
}

// Condition is a metav1.Condition as the API returns it.
export interface Condition {
  type: string
  status: 'True' | 'False' | 'Unknown'
  reason?: string
  message?: string
  observedGeneration?: number
}

export interface ScalingGroup {
  metadata: { name: string; generation?: number }
  spec: ScalingGroupSpec
  status?: ScheduleStatus & {
    phase: string
    lastAction: string
    managedCount: number
    originalReplicas?: Record<string, number>
    namespacesReady?: number
    namespacesTotal?: number
    readyNamespaces?: string[]
    currentStage?: number
    skippedNamespaces?: string[]
    requiredBy?: string[]
    conflictingNamespaces?: string[]
    // Namespaces spec.namespaceSelector matched at the last reconcile.
    selectedNamespaces?: string[]
    conditions?: Condition[]
  }
}

export interface ScalingConfigSpec {
  targetNamespace: string
  active?: boolean
  activeUntil?: string
  schedules?: ScalingSchedule[]
  exceptions?: ScheduleException[]
  calendars?: string[]
  sequence?: string[]
  exclusions?: string[]
  dryRun?: boolean
  pauseGitOps?: boolean
  hibernate?: HibernateRule[]
}

export interface ScalingConfig {
  metadata: { name: string; generation?: number }
  spec: ScalingConfigSpec
  status?: ScheduleStatus & {
    phase: string
    lastAction: string
    originalReplicas?: Record<string, number>
    conditions?: Condition[]
  }
}

// ScalingSpec is what the schedule and sequence editor works on: a group spec (it has
// namespaces) or a config spec (it has targetNamespace).
export type ScalingSpec = Partial<ScalingGroupSpec> & Partial<ScalingConfigSpec>

export type AdviceAction = 'reduce' | 'increase' | 'keep' | 'set' | 'unknown'

export interface ResourceAdvice {
  request?: string
  observed?: string
  recommended?: string
  action: AdviceAction
}

export interface ContainerAdvice {
  name: string
  cpu: ResourceAdvice
  memory: ResourceAdvice
}

export interface WorkloadAdvice {
  kind: 'Deployment' | 'StatefulSet'
  name: string
  replicas: number
  monthlySavings: number
  containers: ContainerAdvice[]
}

// Recommendations is GET /api/namespaces/{ns}/recommendations: read-only right-sizing advice.
export interface Recommendations {
  namespace: string
  source: string
  historical: boolean
  window?: string
  basis: string
  warning?: string
  currency: string
  monthlySavings: number
  workloads: WorkloadAdvice[]
}

// NamespaceFinOps is GET /api/namespaces: the per-minute usage samples of one namespace.
export interface ResourceMetrics {
  usage: string
  requests: string
  limits: string
}

export interface MetricDataPoint {
  timestamp: string
  cpu: ResourceMetrics
  memory: ResourceMetrics
}

export interface NamespaceFinOps {
  metadata: { name: string; creationTimestamp: string }
  spec: { targetNamespace: string }
  status?: {
    lastUpdated?: string
    insights?: string[]
    history?: MetricDataPoint[]
  }
}

// SchedulePreview is POST /api/scaling/preview: what a schedule would do, unchanged.
export interface SchedulePreview {
  namespaces: {
    name: string
    source: 'listed' | 'selected'
    ownedBy?: string
    missing?: boolean
    workloads: { kind: string; name: string; replicas: number; excluded?: boolean; managedBy?: string; cpu: string; memory: string; gitOps?: { kind: string; name: string; namespace?: string } }[] | null
    cronJobs: number
    // services hibernate rules stop after the workloads; running is false when stopped already.
    services?: { kind: string; name: string; running: boolean; stoppedByCostDeck?: boolean }[]
  }[]
  cloudTargets: ExternalTarget[]
  intervals: { from: string; to: string; active: boolean; reason: string }[]
  upShare: number
  nextChange?: string
  onDemand?: boolean
  savings: { hourly: number; monthly: number; currency: string; basis: string }
  problems: string[]
  warnings: string[]
}
