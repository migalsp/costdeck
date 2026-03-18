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

// ResourceValues stores CPU and Memory requests and limits
type ResourceValues struct {
	CPURequest    string `json:"cpuRequest,omitempty"`
	CPULimit      string `json:"cpuLimit,omitempty"`
	MemoryRequest string `json:"memoryRequest,omitempty"`
	MemoryLimit   string `json:"memoryLimit,omitempty"`
}

// WorkloadOptimization stores optimization details for a specific workload
type WorkloadOptimization struct {
	// Name of the workload (Deployment or StatefulSet)
	Name string `json:"name"`
	// Kind of the workload
	Kind string `json:"kind"`
	// Original values before optimization
	Original ResourceValues `json:"original"`
	// Optimized values applied
	Optimized ResourceValues `json:"optimized"`
}

// NamespaceOptimizationSpec defines the desired state of NamespaceOptimization
type NamespaceOptimizationSpec struct {
	// TargetNamespace is the namespace this optimization applies to
	// +kubebuilder:validation:Required
	TargetNamespace string `json:"targetNamespace"`
}

// NamespaceOptimizationStatus defines the observed state of NamespaceOptimization
type NamespaceOptimizationStatus struct {
	// Active indicates if the optimization is currently applied
	Active bool `json:"active"`
	// OptimizedAt is when the optimization was last applied
	// +optional
	OptimizedAt metav1.Time `json:"optimizedAt,omitempty"`
	// Workloads contains the list of optimized workloads and their original values
	// +optional
	// +listType=map
	// +listMapKey=name
	Workloads []WorkloadOptimization `json:"workloads,omitempty"`
}

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status

// NamespaceOptimization is the Schema for the namespaceoptimizations API
type NamespaceOptimization struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   NamespaceOptimizationSpec   `json:"spec"`
	Status NamespaceOptimizationStatus `json:"status,omitempty"`
}

// +kubebuilder:object:root=true

// NamespaceOptimizationList contains a list of NamespaceOptimization
type NamespaceOptimizationList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []NamespaceOptimization `json:"items"`
}

func init() {
	SchemeBuilder.Register(&NamespaceOptimization{}, &NamespaceOptimizationList{})
}
