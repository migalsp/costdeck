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

export interface ScalingGroup {
  metadata: { name: string }
  spec: ScalingGroupSpec
  status?: ScheduleStatus & {
    phase: string
    lastAction: string
    managedCount: number
    originalReplicas?: Record<string, number>
    namespacesReady?: number
    namespacesTotal?: number
    readyNamespaces?: string[]
    requiredBy?: string[]
    conflictingNamespaces?: string[]
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
  metadata: { name: string }
  spec: ScalingConfigSpec
  status?: ScheduleStatus & {
    phase: string
    lastAction: string
    originalReplicas?: Record<string, number>
  }
}

// ScalingSpec is what the schedule and sequence editor works on: a group spec (it has
// namespaces) or a config spec (it has targetNamespace).
export type ScalingSpec = Partial<ScalingGroupSpec> & Partial<ScalingConfigSpec>
