package v1

import (
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

const (
	RightsizingApplyAllowed   = "Allowed"
	RightsizingApplyForbidden = "Forbidden"
)

// RightsizingPolicySpec says how right-sizing treats the workloads of its namespace that
// the selector picks.
type RightsizingPolicySpec struct {
	// Selector picks workloads by the labels of their pod template. Empty picks every
	// Deployment, StatefulSet and Argo Rollout of the namespace.
	// +optional
	Selector *metav1.LabelSelector `json:"selector,omitempty"`

	// Apply says whether operators may apply the advice to these workloads or open a pull
	// request with it. The advice is still shown when it is Forbidden.
	// +kubebuilder:validation:Enum=Allowed;Forbidden
	// +optional
	Apply string `json:"apply,omitempty"`

	// Exclude leaves these workloads out of right-sizing advice altogether.
	// +optional
	Exclude bool `json:"exclude,omitempty"`

	// CPU bounds and pads the CPU request advice.
	// +optional
	CPU ResourcePolicy `json:"cpu,omitempty"`

	// Memory bounds and pads the memory request advice.
	// +optional
	Memory ResourcePolicy `json:"memory,omitempty"`
}

// ResourcePolicy bounds and pads the advice for one resource.
type ResourcePolicy struct {
	// Min is the smallest request the advice recommends.
	// +optional
	Min *resource.Quantity `json:"min,omitempty"`

	// Max is the largest request the advice recommends.
	// +optional
	Max *resource.Quantity `json:"max,omitempty"`

	// HeadroomPercent is added on top of the observed demand, in place of the default of
	// 20 with history from VictoriaMetrics and 50 with a single reading.
	// +kubebuilder:validation:Minimum=0
	// +kubebuilder:validation:Maximum=300
	// +optional
	HeadroomPercent *int32 `json:"headroomPercent,omitempty"`
}

// +kubebuilder:object:root=true
// +kubebuilder:resource:shortName=rsp

// RightsizingPolicy sets bounds, headroom and permissions for right-sizing advice on the
// workloads of its namespace. A workload follows the oldest policy that picks it.
type RightsizingPolicy struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec RightsizingPolicySpec `json:"spec"`
}

// +kubebuilder:object:root=true

// RightsizingPolicyList contains a list of RightsizingPolicy.
type RightsizingPolicyList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []RightsizingPolicy `json:"items"`
}

func init() {
	SchemeBuilder.Register(&RightsizingPolicy{}, &RightsizingPolicyList{})
}
