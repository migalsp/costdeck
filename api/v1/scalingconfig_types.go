/*
Copyright 2026 migalsp.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package v1

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// ScalingSchedule defines when a namespace should be active.
//
// It supports two mutually exclusive forms:
//
//   - Daily window (legacy, default): "days" lists the weekdays the window applies to and
//     the window runs from StartTime to EndTime. If EndTime is earlier than StartTime the
//     window is treated as overnight and spills into the following day.
//   - Continuous weekly window: set StartDay and EndDay to define a single uninterrupted
//     window such as Monday 00:00 -> Friday 23:59. "days" is ignored in this form. The
//     window may wrap through Sunday midnight (e.g. Friday 20:00 -> Monday 08:00).
//
// +kubebuilder:validation:XValidation:rule="has(self.startDay) == has(self.endDay)",message="startDay and endDay must be set together to define a continuous weekly window"
// +kubebuilder:validation:XValidation:rule="has(self.startDay) || (has(self.days) && size(self.days) > 0)",message="a schedule needs either days (daily window) or startDay and endDay (continuous weekly window)"
type ScalingSchedule struct {
	// Days of week the daily window applies to (0-6, 0=Sunday).
	// Required unless StartDay/EndDay are used.
	// +kubebuilder:validation:MaxItems=7
	// +optional
	// +listType=atomic
	Days []int `json:"days,omitempty"`

	// StartDay is the weekday a continuous weekly window opens on (0-6, 0=Sunday).
	// Must be set together with EndDay. When set, Days is ignored.
	// +kubebuilder:validation:Minimum=0
	// +kubebuilder:validation:Maximum=6
	// +optional
	StartDay *int `json:"startDay,omitempty"`

	// EndDay is the weekday a continuous weekly window closes on (0-6, 0=Sunday).
	// Must be set together with StartDay. When set, Days is ignored.
	// +kubebuilder:validation:Minimum=0
	// +kubebuilder:validation:Maximum=6
	// +optional
	EndDay *int `json:"endDay,omitempty"`

	// StartTime in HH:MM format
	// +kubebuilder:validation:Pattern=`^([0-1]?[0-9]|2[0-3]):[0-5][0-9]$`
	StartTime string `json:"startTime"`

	// EndTime in HH:MM format
	// +kubebuilder:validation:Pattern=`^([0-1]?[0-9]|2[0-3]):[0-5][0-9]$`
	EndTime string `json:"endTime"`

	// Timezone for the schedule (e.g. "UTC", "America/New_York").
	// If empty, local operator time is used.
	// +optional
	Timezone string `json:"timezone,omitempty"`
}

// ScheduleException overrides the weekly schedules on given dates: a public holiday the
// environment stays down, or a release weekend it stays up. A manual override (spec.active)
// still beats it. When exceptions overlap, the one that began last wins, so a release
// weekend inside a holiday break keeps the environment up.
type ScheduleException struct {
	// Name says what the exception is for, such as "New Year" or "Release 2.4".
	// +kubebuilder:validation:MaxLength=100
	// +optional
	Name string `json:"name,omitempty"`

	// Start is the first day ("2026-12-24") or the moment ("2026-12-24T18:00") it applies.
	// +kubebuilder:validation:Pattern=`^[0-9]{4}-[0-9]{2}-[0-9]{2}(T([01][0-9]|2[0-3]):[0-5][0-9])?$`
	Start string `json:"start"`

	// End is the last day it applies (the whole day) or the moment it stops applying.
	// Defaults to the end of the Start day.
	// +kubebuilder:validation:Pattern=`^[0-9]{4}-[0-9]{2}-[0-9]{2}(T([01][0-9]|2[0-3]):[0-5][0-9])?$`
	// +optional
	End string `json:"end,omitempty"`

	// State the target is kept in while the exception applies.
	// +kubebuilder:validation:Enum=Up;Down
	State string `json:"state"`

	// Timezone of Start and End (e.g. "Europe/Berlin"). Defaults to the timezone of the
	// first schedule, then to UTC.
	// +optional
	Timezone string `json:"timezone,omitempty"`
}

// HibernateRule stops services an operator runs in a namespace, such as a database, together
// with the namespace's workloads: they stop after the workloads and start before them. A
// preset covers a known operator; otherwise apiVersion, kind and replicasField describe the
// resources. Only what CostDeck stopped is started again.
// +kubebuilder:validation:XValidation:rule="has(self.preset) != (has(self.apiVersion) && has(self.kind) && has(self.replicasField))",message="set a preset, or apiVersion, kind and replicasField"
type HibernateRule struct {
	// Preset is a built-in rule: cnpg (CloudNativePG clusters, by declarative hibernation),
	// zalando-postgres (Zalando postgres-operator clusters, numberOfInstances to zero) or
	// strimzi (Strimzi KafkaConnect, KafkaMirrorMaker2 and KafkaBridge; Kafka clusters
	// themselves cannot be stopped).
	// +kubebuilder:validation:Enum=cnpg;zalando-postgres;strimzi
	// +optional
	Preset string `json:"preset,omitempty"`

	// APIVersion and Kind of the resources a rule without a preset stops, such as
	// redis.example.com/v1 Redis.
	// +optional
	APIVersion string `json:"apiVersion,omitempty"`
	// +optional
	Kind string `json:"kind,omitempty"`

	// ReplicasField is the dotted path of the replica count, such as spec.replicas: zero to
	// stop, the earlier count to start.
	// +optional
	ReplicasField string `json:"replicasField,omitempty"`

	// ReadyField is the dotted path of the ready replica count in the status, such as
	// status.readyReplicas. Without it a resource counts as stopped or started once patched.
	// +optional
	ReadyField string `json:"readyField,omitempty"`

	// Names limits the rule to resources with these names; globs such as "pg-*" work.
	// Empty means every matching resource in the namespace.
	// +optional
	// +listType=atomic
	Names []string `json:"names,omitempty"`
}

// ScheduleStatus explains what drives the desired state of a ScalingGroup or ScalingConfig,
// so that `kubectl get` answers "why is this up right now, and until when?".
type ScheduleStatus struct {
	// Mode is what currently decides the desired state:
	// Schedule (spec.schedules), ManualUp / ManualDown (spec.active is set and the
	// schedule is ignored), Exception (a dated exception or calendar day), AlwaysOn (no
	// usable schedule, kept up as a fail-safe), Dependency (a ScalingGroup kept up for a
	// group that depends on it) or Idle (the schedule's hours, put to sleep for being
	// unused, see spec.idleAfter).
	// +optional
	Mode string `json:"mode,omitempty"`

	// Exception names the exception or calendar day in force while Mode is Exception.
	// +optional
	Exception string `json:"exception,omitempty"`

	// ExceptionEndsAt is when the exception in force stops applying.
	// +optional
	ExceptionEndsAt *metav1.Time `json:"exceptionEndsAt,omitempty"`

	// DesiredState is Up or Down.
	// +optional
	DesiredState string `json:"desiredState,omitempty"`

	// OverrideExpiresAt is when the manual override hands control back to the schedule.
	// Unset while an override without spec.activeUntil is in force.
	// +optional
	OverrideExpiresAt *metav1.Time `json:"overrideExpiresAt,omitempty"`

	// NextTransition is when the desired state next changes on its own, either at a
	// schedule boundary or when the override expires. Unset when it never does.
	// +optional
	NextTransition *ScheduledTransition `json:"nextTransition,omitempty"`

	// EstimatedHourlySavings is what the workloads CostDeck keeps scaled down would cost
	// per hour at the current rates (requests x missing replicas), e.g. "1.2400".
	// +optional
	EstimatedHourlySavings string `json:"estimatedHourlySavings,omitempty"`

	// Currency of EstimatedHourlySavings.
	// +optional
	Currency string `json:"currency,omitempty"`

	// ScaleDownNodes are the nodes the scaled workloads ran on when the last scale-down
	// began, with what they cost then, so the realized-savings check can tell later whether
	// they went away. Cleared once the target is up again.
	// +kubebuilder:validation:MaxItems=200
	// +listType=atomic
	// +optional
	ScaleDownNodes []ScaleDownNode `json:"scaleDownNodes,omitempty"`

	// ScaleDownAt is when that scale-down began.
	// +optional
	ScaleDownAt *metav1.Time `json:"scaleDownAt,omitempty"`

	// IdleUntil is when idle auto-sleep hands the namespaces back to the schedule. Set
	// while Mode is Idle; a manual start clears it.
	// +optional
	IdleUntil *metav1.Time `json:"idleUntil,omitempty"`

	// WakeUpStartedAt is when the running scale-up from zero began. It is cleared once
	// every target is ready, or when the targets go down again first.
	// +optional
	WakeUpStartedAt *metav1.Time `json:"wakeUpStartedAt,omitempty"`

	// RecentWakeUps are the last scale-ups from zero, newest first, with how long each
	// took until every target was ready.
	// +kubebuilder:validation:MaxItems=5
	// +listType=atomic
	// +optional
	RecentWakeUps []WakeUp `json:"recentWakeUps,omitempty"`

	// PrewarmMinutes is how far ahead of each scheduled start spec.prewarm starts the
	// workloads now.
	// +optional
	PrewarmMinutes int32 `json:"prewarmMinutes,omitempty"`
}

// Prewarm starts the workloads ahead of each scheduled start.
type Prewarm struct {
	// Auto takes the lead from the slowest of the last five wake-ups (status.recentWakeUps)
	// plus a minute, at most 30 minutes. Minutes applies until there is a wake-up to measure.
	// +optional
	Auto bool `json:"auto,omitempty"`

	// Minutes is the lead when Auto is off, or before the first measured wake-up.
	// +kubebuilder:validation:Minimum=0
	// +kubebuilder:validation:Maximum=120
	// +optional
	Minutes int32 `json:"minutes,omitempty"`
}

// WakeUp is one scale-up from zero and how long it took.
type WakeUp struct {
	// StartedAt is when the scale-up began.
	StartedAt metav1.Time `json:"startedAt"`

	// Seconds is how long it took until every target was ready.
	// +kubebuilder:validation:Minimum=0
	Seconds int32 `json:"seconds"`
}

// ScaleDownNode is a node a scale-down could free.
type ScaleDownNode struct {
	Name string `json:"name"`
	// HourlyCost is what the node cost per hour when the scale-down began, e.g. "0.1920".
	// +optional
	HourlyCost string `json:"hourlyCost,omitempty"`
}

// ScheduledTransition is a future change of the desired state.
type ScheduledTransition struct {
	// Time of the change.
	Time metav1.Time `json:"time"`
	// DesiredState after the change: Up or Down.
	DesiredState string `json:"desiredState"`
}

// ScalingConfigSpec defines the desired state of ScalingConfig
type ScalingConfigSpec struct {
	// TargetNamespace is the namespace this config applies to. It cannot change: create a
	// ScalingConfig for the other namespace, so this one restores what it scaled down.
	// +kubebuilder:validation:Required
	// +kubebuilder:validation:XValidation:rule="self == oldSelf",message="targetNamespace cannot change; create a ScalingConfig for the other namespace"
	TargetNamespace string `json:"targetNamespace"`

	// Active is a manual override. While it is set the schedule is ignored completely:
	// true forces the namespace up, false forces it down. Remove the field (null) to
	// return to the schedule, or set ActiveUntil so that happens automatically.
	// status.mode shows which of the two is in control.
	// +optional
	Active *bool `json:"active,omitempty"`

	// ActiveUntil bounds the manual override in time. When set, Active is honoured only
	// until this timestamp; afterwards the schedule takes over again without anyone having
	// to clear the override by hand. Ignored when Active is null.
	// +optional
	ActiveUntil *metav1.Time `json:"activeUntil,omitempty"`

	// Schedules define periodic scaling events
	// +optional
	// +listType=atomic
	Schedules []ScalingSchedule `json:"schedules,omitempty"`

	// Exceptions override the schedules on given dates (holidays, release weekends).
	// +kubebuilder:validation:MaxItems=100
	// +optional
	// +listType=atomic
	Exceptions []ScheduleException `json:"exceptions,omitempty"`

	// Calendars names shared holiday calendars (CostDeckConfig spec.calendars) whose days
	// apply as exceptions too.
	// +kubebuilder:validation:MaxItems=10
	// +optional
	// +listType=set
	Calendars []string `json:"calendars,omitempty"`

	// Sequence orders the namespace's workloads in stages that start first to last and stop
	// last to first; workloads in no stage start last and stop first. Each entry is a stage:
	// a space separated list of workload name globs ("db", "api-*"), optionally qualified
	// as "Kind/name" or "group/version:Kind/name"; "*" matches every workload. A workload
	// goes to the stage of its most specific pattern (exact name, then glob, then "*"), so
	// ["*", "my-operator"] stops the operator before everything else.
	// +optional
	// +listType=atomic
	Sequence []string `json:"sequence,omitempty"`

	// Exclusions lists workloads that are never scaled down, by name or prefix glob
	// ("redis-*"), or by the kind of their controller ("owner:Elasticsearch") for workloads
	// an operator manages. They also apply to the CronJobs CostDeck suspends and the KEDA
	// ScaledObjects it pauses while the namespace is down.
	// +optional
	// +listType=atomic
	Exclusions []string `json:"exclusions,omitempty"`

	// Hibernate stops services that operators run in the namespace, such as databases,
	// after its workloads and starts them before. Their operators' StatefulSets and
	// Deployments are left to the operators. Needs hibernation.enabled in the Helm chart.
	// +kubebuilder:validation:MaxItems=20
	// +optional
	// +listType=atomic
	Hibernate []HibernateRule `json:"hibernate,omitempty"`

	// DryRun works out what the schedule would do and reports it (phase DryRun and the
	// DryRun condition) without scaling anything, to try a schedule before it acts. To the
	// schedules that depend on it, a group in dry run counts as up.
	// +optional
	DryRun bool `json:"dryRun,omitempty"`

	// PauseGitOps pauses the GitOps tools that would bring the workloads back while the
	// schedule keeps them down: Flux Kustomizations and HelmReleases are suspended and Argo CD
	// Applications lose self-heal, and they are resumed once everything is up again. Only
	// what CostDeck paused itself is resumed. Needs gitops.pause in the Helm chart.
	// +optional
	PauseGitOps bool `json:"pauseGitOps,omitempty"`

	// SleepPage points the namespaces' Gateway API HTTPRoutes at Cost Deck's sleep page while
	// the schedule keeps them down, and back once they are up: whoever opens an application
	// sees that it is asleep and a link to wake it, not a gateway error. Needs
	// sleepPage.enabled in the Helm chart.
	// +optional
	SleepPage bool `json:"sleepPage,omitempty"`

	// IdleAfter puts the namespaces to sleep inside the schedule's hours once nobody has
	// used them for this long: no requests through Envoy Gateway, Istio or ingress-nginx and
	// no GPU busier than 5% (DCGM) in VictoriaMetrics, counted once they have been up that
	// long. They stay down until the schedule next starts them, at most a week, or until
	// someone starts them by hand. Namespaces without such metrics are never treated as
	// unused.
	// +optional
	IdleAfter *metav1.Duration `json:"idleAfter,omitempty"`

	// Prewarm starts the workloads ahead of each scheduled start, so they are ready when it
	// comes: by a fixed number of minutes, or by how long recent wake-ups took.
	// +optional
	Prewarm *Prewarm `json:"prewarm,omitempty"`
}

// ScalingConfigStatus defines the observed state of ScalingConfig.
type ScalingConfigStatus struct {
	// Phase is the current state of the config (ScaledUp, ScalingDown, ScaledDown)
	// +optional
	Phase string `json:"phase,omitempty"`

	// LastAction is the timestamp of the last scaling event
	// +optional
	LastAction metav1.Time `json:"lastAction,omitempty"`

	// OriginalReplicas stores the previous replica counts for restoration
	// Key format: "Kind/Name"
	// +optional
	OriginalReplicas map[string]int32 `json:"originalReplicas,omitempty"`

	ScheduleStatus `json:",inline"`

	// Conditions represent the current state of the ScalingConfig resource.
	// +listType=map
	// +listMapKey=type
	// +optional
	Conditions []metav1.Condition `json:"conditions,omitempty"`
}

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:printcolumn:name="Namespace",type=string,JSONPath=".spec.targetNamespace"
// +kubebuilder:printcolumn:name="Mode",type=string,JSONPath=".status.mode"
// +kubebuilder:printcolumn:name="Desired",type=string,JSONPath=".status.desiredState"
// +kubebuilder:printcolumn:name="Phase",type=string,JSONPath=".status.phase"
// +kubebuilder:printcolumn:name="Next change",type=string,JSONPath=".status.nextTransition.time"
// +kubebuilder:printcolumn:name="Saving/h",type=string,JSONPath=".status.estimatedHourlySavings",priority=1
// +kubebuilder:printcolumn:name="Age",type=date,JSONPath=".metadata.creationTimestamp"

// ScalingConfig is the Schema for the scalingconfigs API
type ScalingConfig struct {
	metav1.TypeMeta `json:",inline"`

	// metadata is a standard object metadata
	// +optional
	metav1.ObjectMeta `json:"metadata,omitzero"`

	// spec defines the desired state of ScalingConfig
	// +required
	Spec ScalingConfigSpec `json:"spec"`

	// status defines the observed state of ScalingConfig
	// +optional
	Status ScalingConfigStatus `json:"status,omitzero"`
}

// +kubebuilder:object:root=true

// ScalingConfigList contains a list of ScalingConfig
type ScalingConfigList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitzero"`
	Items           []ScalingConfig `json:"items"`
}

func init() {
	SchemeBuilder.Register(&ScalingConfig{}, &ScalingConfigList{})
}
