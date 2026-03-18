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

// ScalingGroupSpec defines the desired state of ScalingGroup
type ScalingGroupSpec struct {
	// Category is the group classification (e.g. Solution, Platform)
	// +kubebuilder:validation:Required
	Category string `json:"category"`

	// Namespaces is the list of namespaces managed by this group
	// +kubebuilder:validation:MinItems=1
	// +listType=set
	Namespaces []string `json:"namespaces"`

	// Active is the manual override for scaling.
	// If null, the schedule is followed.
	// +optional
	Active *bool `json:"active,omitempty"`

	// Schedules define periodic scaling events for the group
	// +optional
	// +listType=atomic
	Schedules []ScalingSchedule `json:"schedules,omitempty"`

	// Sequence defines the order of scaling namespaces.
	// Each element can be a single namespace or multiple namespaces separated by spaces (a "stage").
	// Stages are executed sequentially, waiting for all namespaces in a stage to reach the target state.
	// Example: ["ns1", "ns2 ns3", "ns4"]
	// +optional
	// +listType=atomic
	Sequence []string `json:"sequence,omitempty"`

	// ExternalTargets allows you to manage 3rd party cloud resources alongside Kubernetes resources.
	// +optional
	// +listType=atomic
	ExternalTargets []ExternalTarget `json:"externalTargets,omitempty"`
}

// ExternalTarget represents a 3rd party resource to scale
type ExternalTarget struct {
	// Provider is the cloud provider (e.g., "aws", "gcp", "azure")
	// +kubebuilder:validation:Required
	Provider string `json:"provider"`

	// Type of the resource (e.g., "aurora", "cloudsql")
	// +kubebuilder:validation:Required
	Type string `json:"type"`

	// Identifier is the unique ID or ARN of the resource
	// +kubebuilder:validation:Required
	Identifier string `json:"identifier"`

	// Region where the resource resides
	// +kubebuilder:validation:Required
	Region string `json:"region"`

	// ExecuteAfter specifies which namespace stage this resource should scale after
	// +optional
	ExecuteAfter string `json:"executeAfter,omitempty"`

	// Status tells you if the DB is available or stopped (populated at runtime by UI/Discovery API)
	// +optional
	Status string `json:"status,omitempty"`
}

// ScalingGroupStatus defines the observed state of ScalingGroup.
type ScalingGroupStatus struct {
	// Phase is the current state of the group (ScaledUp, ScalingDown, ScaledDown)
	// +optional
	Phase string `json:"phase,omitempty"`

	// LastAction is the timestamp of the last scaling event
	// +optional
	LastAction metav1.Time `json:"lastAction,omitempty"`

	// OriginalReplicas stores the previous replica counts for restoration
	// Key format: "Namespace/Kind/Name"
	// +optional
	OriginalReplicas map[string]int32 `json:"originalReplicas,omitempty"`

	// ManagedCount is the current number of successfully managed namespaces in the group
	// +optional
	ManagedCount int `json:"managedCount,omitempty"`

	// NamespacesReady is the number of namespaces that have reached their target state
	// +optional
	NamespacesReady int `json:"namespacesReady,omitempty"`

	// NamespacesTotal is the total number of namespaces in this group
	// +optional
	NamespacesTotal int `json:"namespacesTotal,omitempty"`

	// ReadyNamespaces is the list of namespaces that are currently at their target state
	// +optional
	ReadyNamespaces []string `json:"readyNamespaces,omitempty"`

	// Conditions represent the current state of the ScalingGroup resource.
	Conditions []metav1.Condition `json:"conditions,omitempty"`
}

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status

// ScalingGroup is the Schema for the scalinggroups API
type ScalingGroup struct {
	metav1.TypeMeta `json:",inline"`

	// metadata is a standard object metadata
	// +optional
	metav1.ObjectMeta `json:"metadata,omitzero"`

	// spec defines the desired state of ScalingGroup
	// +required
	Spec ScalingGroupSpec `json:"spec"`

	// status defines the observed state of ScalingGroup
	// +optional
	Status ScalingGroupStatus `json:"status,omitzero"`
}

// +kubebuilder:object:root=true

// ScalingGroupList contains a list of ScalingGroup
type ScalingGroupList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitzero"`
	Items           []ScalingGroup `json:"items"`
}

func init() {
	SchemeBuilder.Register(&ScalingGroup{}, &ScalingGroupList{})
}
