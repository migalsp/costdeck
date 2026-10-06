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

// ScheduleStatus explains what drives the desired state of a ScalingGroup or ScalingConfig,
// so that `kubectl get` answers "why is this up right now, and until when?".
type ScheduleStatus struct {
	// Mode is what currently decides the desired state:
	// Schedule (spec.schedules), ManualUp / ManualDown (spec.active is set and the
	// schedule is ignored), AlwaysOn (no usable schedule, kept up as a fail-safe) or
	// Dependency (a ScalingGroup kept up for a group that depends on it).
	// +optional
	Mode string `json:"mode,omitempty"`

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
	// TargetNamespace is the namespace this config applies to
	// +kubebuilder:validation:Required
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
	// ("redis-*"). They also apply to the CronJobs CostDeck suspends and the KEDA
	// ScaledObjects it pauses while the namespace is down.
	// +optional
	// +listType=atomic
	Exclusions []string `json:"exclusions,omitempty"`
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
