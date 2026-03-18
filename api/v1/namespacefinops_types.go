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

// Metric values for a single point in time

// Metric values for a single point in time
type ResourceMetrics struct {
	// Total Usage
	Usage string `json:"usage"`
	// Total Requests
	Requests string `json:"requests"`
	// Total Limits
	Limits string `json:"limits"`
}

// Data point for a specific minute
type MetricDataPoint struct {
	Timestamp metav1.Time     `json:"timestamp"`
	CPU       ResourceMetrics `json:"cpu"`
	Memory    ResourceMetrics `json:"memory"`
}

// NamespaceFinOpsSpec defines the desired state of NamespaceFinOps
type NamespaceFinOpsSpec struct {
	// TargetNamespace is the namespace this CR is tracking metrics for
	// +kubebuilder:validation:Required
	TargetNamespace string `json:"targetNamespace"`
}

// NamespaceFinOpsStatus defines the observed state of NamespaceFinOps.
type NamespaceFinOpsStatus struct {
	// History contains the last 60 minutes of metrics (1 data point per minute)
	// +optional
	// +listType=atomic
	History []MetricDataPoint `json:"history,omitempty"`

	// LastUpdated marks when the metrics were last successfully polled
	// +optional
	LastUpdated metav1.Time `json:"lastUpdated,omitempty"`

	// Insights contains informative labels about the namespace (e.g. "Missing Requests")
	// +optional
	// +listType=atomic
	Insights []string `json:"insights,omitempty"`

	// conditions represent the current state of the NamespaceFinOps resource.
	// +listType=map
	// +listMapKey=type
	// +optional
	Conditions []metav1.Condition `json:"conditions,omitempty"`
}

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status

// NamespaceFinOps is the Schema for the namespacefinops API
type NamespaceFinOps struct {
	metav1.TypeMeta `json:",inline"`

	// metadata is a standard object metadata
	// +optional
	metav1.ObjectMeta `json:"metadata,omitempty"`

	// spec defines the desired state of NamespaceFinOps
	// +required
	Spec NamespaceFinOpsSpec `json:"spec"`

	// status defines the observed state of NamespaceFinOps
	// +optional
	Status NamespaceFinOpsStatus `json:"status,omitempty"`
}

// +kubebuilder:object:root=true

// NamespaceFinOpsList contains a list of NamespaceFinOps
type NamespaceFinOpsList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []NamespaceFinOps `json:"items"`
}

func init() {
	SchemeBuilder.Register(&NamespaceFinOps{}, &NamespaceFinOpsList{})
}
