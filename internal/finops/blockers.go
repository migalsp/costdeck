package finops

import (
	"fmt"
	"slices"
	"sort"
	"strings"
	"time"

	corev1 "k8s.io/api/core/v1"
	policyv1 "k8s.io/api/policy/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
)

// A scaled-down namespace saves money only when its nodes go away. Cluster Autoscaler and
// Karpenter remove a node once its pods fit elsewhere, unless something on it says no;
// NodeBlockers names those things, as their documentation lists them.

// Reasons a node stays.
const (
	BlockerNode             = "node"              // the node itself opts out
	BlockerPDB              = "pdb"               // a PodDisruptionBudget allows no disruption
	BlockerDoNotDisrupt     = "do-not-disrupt"    // karpenter.sh/do-not-disrupt on a pod
	BlockerSafeToEvict      = "safe-to-evict"     // cluster-autoscaler.kubernetes.io/safe-to-evict: "false"
	BlockerLocalStorage     = "local-storage"     // hostPath or disk-backed emptyDir (Cluster Autoscaler)
	BlockerNoController     = "no-controller"     // a bare pod nothing would recreate
	BlockerCustomController = "custom-controller" // owned by an operator (Cluster Autoscaler)
	BlockerKubeSystem       = "kube-system"       // kube-system pod without a PDB (Cluster Autoscaler)
)

const (
	caSafeToEvict       = "cluster-autoscaler.kubernetes.io/safe-to-evict"
	caSafeLocalVolumes  = "cluster-autoscaler.kubernetes.io/safe-to-evict-local-volumes"
	caScaleDownDisabled = "cluster-autoscaler.kubernetes.io/scale-down-disabled"
	karpenterNoDisrupt  = "karpenter.sh/do-not-disrupt"
	mirrorPod           = "kubernetes.io/config.mirror"
)

// Blocker is something that keeps an autoscaler from removing a node.
type Blocker struct {
	Reason string `json:"reason"`
	// Pod is namespace/name, empty for the node itself.
	Pod    string `json:"pod,omitempty"`
	Detail string `json:"detail"`
}

// builtInControllers are the owners Cluster Autoscaler knows how to move pods for.
var builtInControllers = []string{"ReplicaSet", "StatefulSet", "Job", "DaemonSet", "ReplicationController"}

// NodeBlockers lists what keeps autoscalers from removing a node: annotations on the node,
// and the pods on it that cannot be evicted. DaemonSet and static pods do not count; they
// go with the node.
func NodeBlockers(node corev1.Node, pods []corev1.Pod, pdbs []policyv1.PodDisruptionBudget, now time.Time) []Blocker {
	var out []Blocker
	if node.Annotations[caScaleDownDisabled] == annotationTrue {
		out = append(out, Blocker{Reason: BlockerNode, Detail: "The node is annotated " + caScaleDownDisabled + "."})
	}
	if node.Annotations[karpenterNoDisrupt] == annotationTrue {
		out = append(out, Blocker{Reason: BlockerNode, Detail: "The node is annotated " + karpenterNoDisrupt + "."})
	}
	for i := range pods {
		p := &pods[i]
		if p.Spec.NodeName != node.Name || !active(p) || isDaemonSetPod(p) || p.Annotations[mirrorPod] != "" {
			continue
		}
		out = append(out, podBlockers(p, pdbs, now)...)
	}
	return out
}

func podBlockers(p *corev1.Pod, pdbs []policyv1.PodDisruptionBudget, now time.Time) []Blocker {
	ref := p.Namespace + "/" + p.Name
	block := func(reason, format string, args ...any) Blocker {
		return Blocker{Reason: reason, Pod: ref, Detail: fmt.Sprintf(format, args...)}
	}
	var out []Blocker
	safe := p.Annotations[caSafeToEvict]
	if safe == "false" {
		out = append(out, block(BlockerSafeToEvict, "Annotated %s: \"false\".", caSafeToEvict))
	}
	if v := p.Annotations[karpenterNoDisrupt]; v == annotationTrue {
		out = append(out, block(BlockerDoNotDisrupt, "Annotated %s: \"true\".", karpenterNoDisrupt))
	} else if d, err := time.ParseDuration(v); err == nil && p.Status.StartTime != nil {
		if left := p.Status.StartTime.Add(d).Sub(now); left > 0 {
			out = append(out, block(BlockerDoNotDisrupt, "Annotated %s: %q, for another %s.", karpenterNoDisrupt, v, left.Round(time.Minute)))
		}
	}
	covered := false
	for _, pdb := range pdbs {
		if pdb.Namespace != p.Namespace || pdb.Spec.Selector == nil {
			continue
		}
		sel, err := metav1.LabelSelectorAsSelector(pdb.Spec.Selector)
		if err != nil || !sel.Matches(labels.Set(p.Labels)) {
			continue
		}
		covered = true
		if pdb.Status.DisruptionsAllowed == 0 {
			out = append(out, block(BlockerPDB, "The PodDisruptionBudget %s/%s allows no disruption now.", pdb.Namespace, pdb.Name))
		}
	}
	// safe-to-evict: "true" lifts the remaining checks, which only Cluster Autoscaler makes.
	if safe == annotationTrue {
		return out
	}
	if vols := localVolumes(p); len(vols) > 0 {
		out = append(out, block(BlockerLocalStorage, "Uses local storage (%s); Cluster Autoscaler keeps such nodes unless %s lists the volumes.",
			strings.Join(vols, ", "), caSafeLocalVolumes))
	}
	owner := metav1.GetControllerOf(p)
	switch {
	case owner == nil:
		out = append(out, block(BlockerNoController, "No controller would recreate it elsewhere."))
	case !slices.Contains(builtInControllers, owner.Kind):
		out = append(out, block(BlockerCustomController, "Run by %s %s; Cluster Autoscaler skips nodes with pods of other controllers by default.", owner.Kind, owner.Name))
	}
	if p.Namespace == "kube-system" && !covered {
		out = append(out, block(BlockerKubeSystem, "A kube-system pod without a PodDisruptionBudget."))
	}
	return out
}

// localVolumes are a pod's volumes on the node's disk that the safe-to-evict-local-volumes
// annotation does not release.
func localVolumes(p *corev1.Pod) []string {
	released := strings.Split(p.Annotations[caSafeLocalVolumes], ",")
	var out []string
	for _, v := range p.Spec.Volumes {
		local := v.HostPath != nil || (v.EmptyDir != nil && v.EmptyDir.Medium != corev1.StorageMediumMemory)
		if local && !slices.Contains(released, v.Name) {
			out = append(out, v.Name)
		}
	}
	sort.Strings(out)
	return out
}
