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
  mode?: 'Schedule' | 'ManualUp' | 'ManualDown' | 'AlwaysOn' | 'Dependency' | 'OnDemand'
  desiredState?: DesiredState
  overrideExpiresAt?: string
  nextTransition?: { time: string; desiredState: DesiredState }
  estimatedHourlySavings?: string
  currency?: string
}

export interface ScalingGroupSpec {
  category: string
  namespaces: string[]
  active?: boolean
  activeUntil?: string
  schedules?: ScalingSchedule[]
  sequence?: string[]
  exclusions?: string[]
  externalTargets?: ExternalTarget[]
  featureFlags?: {
    skipOnTimeout: boolean
    timeoutMinutes: number
  }
  dependsOn?: string[]
  activation?: 'Schedule' | 'OnDemand'
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
    conditions?: Condition[]
  }
}

export interface ScalingConfigSpec {
  targetNamespace: string
  active?: boolean
  activeUntil?: string
  schedules?: ScalingSchedule[]
  sequence?: string[]
  exclusions?: string[]
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
