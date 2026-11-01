package finops

import (
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	policyv1 "k8s.io/api/policy/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/intstr"
)

func TestNodeBlockers(t *testing.T) {
	now := time.Date(2026, 10, 22, 12, 0, 0, 0, time.UTC)
	owned := func(kind string) []metav1.OwnerReference {
		return []metav1.OwnerReference{{Kind: kind, Name: "owner", Controller: new(true)}}
	}
	pod := func(ns, name string, mutate func(*corev1.Pod)) corev1.Pod {
		p := corev1.Pod{Namespace: ns, Name: name, Labels: map[string]string{"app": name}, OwnerReferences: owned("ReplicaSet"),
			Spec: corev1.PodSpec{NodeName: "node-a"}, Status: corev1.PodStatus{Phase: corev1.PodRunning}}
		if mutate != nil {
			mutate(&p)
		}
		return p
	}
	annotate := func(kv ...string) func(*corev1.Pod) {
		return func(p *corev1.Pod) {
			p.Annotations = map[string]string{}
			for i := 0; i < len(kv); i += 2 {
				p.Annotations[kv[i]] = kv[i+1]
			}
		}
	}
	pdb := func(ns, name, app string, allowed int32) policyv1.PodDisruptionBudget {
		return policyv1.PodDisruptionBudget{Namespace: ns, Name: name,
			Spec:   policyv1.PodDisruptionBudgetSpec{MinAvailable: new(intstr.FromInt32(1)), Selector: &metav1.LabelSelector{MatchLabels: map[string]string{"app": app}}},
			Status: policyv1.PodDisruptionBudgetStatus{DisruptionsAllowed: allowed}}
	}
	node := corev1.Node{Name: "node-a"}

	cases := []struct {
		name string
		node corev1.Node
		pods []corev1.Pod
		pdbs []policyv1.PodDisruptionBudget
		want []Blocker
	}{
		{"nothing in the way", node, []corev1.Pod{pod("shop", "api", nil)}, nil, nil},
		{"a PDB that allows no disruption", node, []corev1.Pod{pod("shop", "api", nil)}, []policyv1.PodDisruptionBudget{pdb("shop", "api-pdb", "api", 0), pdb("shop", "other", "web", 0)},
			[]Blocker{{BlockerPDB, "shop/api", "The PodDisruptionBudget shop/api-pdb allows no disruption now."}}},
		{"a PDB that allows one", node, []corev1.Pod{pod("shop", "api", nil)}, []policyv1.PodDisruptionBudget{pdb("shop", "api-pdb", "api", 1)}, nil},
		{"the node opts out", corev1.Node{Name: "node-a", Annotations: map[string]string{caScaleDownDisabled: "true", karpenterNoDisrupt: "true"}}, nil, nil,
			[]Blocker{{BlockerNode, "", "The node is annotated cluster-autoscaler.kubernetes.io/scale-down-disabled."}, {BlockerNode, "", "The node is annotated karpenter.sh/do-not-disrupt."}}},
		{"safe-to-evict false", node, []corev1.Pod{pod("shop", "api", annotate(caSafeToEvict, "false"))}, nil,
			[]Blocker{{BlockerSafeToEvict, "shop/api", `Annotated cluster-autoscaler.kubernetes.io/safe-to-evict: "false".`}}},
		{"do-not-disrupt for good", node, []corev1.Pod{pod("shop", "api", annotate(karpenterNoDisrupt, "true"))}, nil,
			[]Blocker{{BlockerDoNotDisrupt, "shop/api", `Annotated karpenter.sh/do-not-disrupt: "true".`}}},
		{"do-not-disrupt for a while", node, []corev1.Pod{pod("shop", "api", func(p *corev1.Pod) {
			annotate(karpenterNoDisrupt, "2h")(p)
			p.Status.StartTime = &metav1.Time{Time: now.Add(-90 * time.Minute)}
		})}, nil, []Blocker{{BlockerDoNotDisrupt, "shop/api", `Annotated karpenter.sh/do-not-disrupt: "2h", for another 30m0s.`}}},
		{"do-not-disrupt that has run out", node, []corev1.Pod{pod("shop", "api", func(p *corev1.Pod) {
			annotate(karpenterNoDisrupt, "1h")(p)
			p.Status.StartTime = &metav1.Time{Time: now.Add(-2 * time.Hour)}
		})}, nil, nil},
		{"local storage, except in memory or released", node, []corev1.Pod{pod("shop", "api", func(p *corev1.Pod) {
			annotate(caSafeLocalVolumes, "scratch")(p)
			p.Spec.Volumes = []corev1.Volume{
				{Name: "cache", EmptyDir: &corev1.EmptyDirVolumeSource{}},
				{Name: "logs", HostPath: &corev1.HostPathVolumeSource{Path: "/var/log"}},
				{Name: "tmp", EmptyDir: &corev1.EmptyDirVolumeSource{Medium: corev1.StorageMediumMemory}},
				{Name: "scratch", EmptyDir: &corev1.EmptyDirVolumeSource{}},
				{Name: "config", ConfigMap: &corev1.ConfigMapVolumeSource{}},
			}
		})}, nil, []Blocker{{BlockerLocalStorage, "shop/api", "Uses local storage (cache, logs); Cluster Autoscaler keeps such nodes unless cluster-autoscaler.kubernetes.io/safe-to-evict-local-volumes lists the volumes."}}},
		{"a bare pod and an operator's pod", node, []corev1.Pod{
			pod("shop", "debug", func(p *corev1.Pod) { p.OwnerReferences = nil }),
			pod("shop", "db-1", func(p *corev1.Pod) { p.OwnerReferences = owned("Cluster") }),
		}, nil, []Blocker{
			{BlockerNoController, "shop/debug", "No controller would recreate it elsewhere."},
			{BlockerCustomController, "shop/db-1", "Run by Cluster owner; Cluster Autoscaler skips nodes with pods of other controllers by default."},
		}},
		{"kube-system pods need a PDB", node, []corev1.Pod{pod("kube-system", "coredns", nil), pod("kube-system", "metrics", nil)},
			[]policyv1.PodDisruptionBudget{pdb("kube-system", "metrics", "metrics", 1)},
			[]Blocker{{BlockerKubeSystem, "kube-system/coredns", "A kube-system pod without a PodDisruptionBudget."}}},
		{"safe-to-evict true lifts Cluster Autoscaler's own checks, not PDBs", node, []corev1.Pod{pod("kube-system", "debug", func(p *corev1.Pod) {
			annotate(caSafeToEvict, "true")(p)
			p.OwnerReferences = nil
			p.Spec.Volumes = []corev1.Volume{{Name: "cache", EmptyDir: &corev1.EmptyDirVolumeSource{}}}
		})}, []policyv1.PodDisruptionBudget{pdb("kube-system", "all", "debug", 0)},
			[]Blocker{{BlockerPDB, "kube-system/debug", "The PodDisruptionBudget kube-system/all allows no disruption now."}}},
		{"pods that go with the node, or are elsewhere, do not count", node, []corev1.Pod{
			pod("shop", "agent", func(p *corev1.Pod) { p.OwnerReferences = owned("DaemonSet"); annotate(caSafeToEvict, "false")(p) }),
			pod("kube-system", "kube-proxy", func(p *corev1.Pod) { p.OwnerReferences = nil; annotate(mirrorPod, "x")(p) }),
			pod("shop", "done", func(p *corev1.Pod) { p.OwnerReferences = nil; p.Status.Phase = corev1.PodSucceeded }),
			pod("shop", "elsewhere", func(p *corev1.Pod) { p.OwnerReferences = nil; p.Spec.NodeName = "node-b" }),
		}, nil, nil},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := NodeBlockers(c.node, c.pods, c.pdbs, now)
			if len(got) != len(c.want) {
				t.Fatalf("got %+v, want %+v", got, c.want)
			}
			for i := range got {
				if got[i] != c.want[i] {
					t.Errorf("blocker %d = %+v, want %+v", i, got[i], c.want[i])
				}
			}
		})
	}
}
