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

// ScalingSchedule defines when a namespace should be active
type ScalingSchedule struct {
	// Days of week (0-6, 0=Sunday)
	// +kubebuilder:validation:MinItems=1
	// +kubebuilder:validation:MaxItems=7
	Days []int `json:"days"`

	// StartTime in HH:MM format (local operator time)
	// +kubebuilder:validation:Pattern=`^([0-1]?[0-9]|2[0-3]):[0-5][0-9]$`
	StartTime string `json:"startTime"`

	// EndTime in HH:MM format (local operator time)
	// +kubebuilder:validation:Pattern=`^([0-1]?[0-9]|2[0-3]):[0-5][0-9]$`
	EndTime string `json:"endTime"`

	// Timezone for the schedule (e.g. "UTC", "America/New_York")
	// If empty, local operator time is used.
	// +optional
	Timezone string `json:"timezone,omitempty"`
}

// ScalingConfigSpec defines the desired state of ScalingConfig
type ScalingConfigSpec struct {
	// TargetNamespace is the namespace this config applies to
	// +kubebuilder:validation:Required
	TargetNamespace string `json:"targetNamespace"`

	// Active is the manual override for scaling.
	// If null, the schedule is followed.
	// If true, the namespace is forced to Scale Up.
	// If false, the namespace is forced to Scale Down.
	// +optional
	Active *bool `json:"active,omitempty"`

	// Schedules define periodic scaling events
	// +optional
	// +listType=atomic
	Schedules []ScalingSchedule `json:"schedules,omitempty"`

	// Sequence defines the order of scaling resources.
	// Format: "Group/Version:Kind/Name" (e.g. "apps/v1:Deployment/my-app" or "apps/v1:Deployment/*")
	// +optional
	// +listType=atomic
	Sequence []string `json:"sequence,omitempty"`

	// Exclusions lists resources that should never be scaled down
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

	// Conditions represent the current state of the ScalingConfig resource.
	Conditions []metav1.Condition `json:"conditions,omitempty"`
}

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status

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
