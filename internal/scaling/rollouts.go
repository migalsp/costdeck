package scaling

import (
	"context"
	"errors"
	"fmt"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// Argo Rollouts replaces Deployments for canary and blue-green releases: a Rollout has the
// same pod template and spec.replicas, and its controller runs the ReplicaSets. CostDeck
// scales a Rollout like a Deployment, through spec.replicas and the original-replicas
// annotation. A Rollout can instead take its pod template from a Deployment
// (spec.workloadRef); that Deployment is kept at zero by its owner, so CostDeck leaves it
// alone and scales the Rollout.

var rolloutGK = schema.GroupKind{Group: "argoproj.io", Kind: "Rollout"}

// Kinds as workload rules and previews name them.
const (
	kindRollout    = "Rollout"
	kindDeployment = "Deployment"
)

// listRollouts returns the Rollouts of a namespace, none when Argo Rollouts is not
// installed, and the names of the Deployments they take their pod template from.
func (e *Engine) listRollouts(ctx context.Context, ns string) ([]*unstructured.Unstructured, map[string]bool, error) {
	gvk, ok, err := e.mapping(rolloutGK, "v1alpha1")
	if err != nil || !ok {
		return nil, nil, err
	}
	list := &unstructured.UnstructuredList{}
	list.SetGroupVersionKind(gvk.GroupVersion().WithKind("RolloutList"))
	if err := e.Client.List(ctx, list, client.InNamespace(ns)); err != nil {
		return nil, nil, err
	}
	out := make([]*unstructured.Unstructured, 0, len(list.Items))
	refs := map[string]bool{}
	for i := range list.Items {
		r := &list.Items[i]
		r.SetGroupVersionKind(gvk)
		if kind, _, _ := unstructured.NestedString(r.Object, "spec", "workloadRef", "kind"); kind == kindDeployment {
			if name, _, _ := unstructured.NestedString(r.Object, "spec", "workloadRef", "name"); name != "" {
				refs[name] = true
			}
		}
		out = append(out, r)
	}
	return out, refs, nil
}

// withReferencedTemplate gives a Rollout that takes its pod template from a Deployment
// that template, in memory only, so requests and savings can be worked out. A merge patch
// of the Rollout compares it with a copy that has the same template, so it is never sent.
func withReferencedTemplate(r *unstructured.Unstructured, deployments map[string]*appsv1.Deployment) {
	if _, found, _ := unstructured.NestedMap(r.Object, "spec", "template"); found {
		return
	}
	name, _, _ := unstructured.NestedString(r.Object, "spec", "workloadRef", "name")
	d := deployments[name]
	if d == nil {
		return
	}
	if tmpl, err := runtime.DefaultUnstructuredConverter.ToUnstructured(&d.Spec.Template); err == nil {
		_ = unstructured.SetNestedField(r.Object, tmpl, "spec", "template")
	}
	if _, found, _ := unstructured.NestedMap(r.Object, "spec", "selector"); !found && d.Spec.Selector != nil {
		if sel, err := runtime.DefaultUnstructuredConverter.ToUnstructured(d.Spec.Selector); err == nil {
			_ = unstructured.SetNestedField(r.Object, sel, "spec", "selector")
		}
	}
}

func isRollout(obj client.Object) (*unstructured.Unstructured, bool) {
	u, ok := obj.(*unstructured.Unstructured)
	return u, ok && u.GroupVersionKind().GroupKind() == rolloutGK
}

// rolloutReplicas is spec.replicas, the API default of one when unset.
func rolloutReplicas(u *unstructured.Unstructured) int32 {
	n, found, err := unstructured.NestedInt64(u.Object, "spec", "replicas")
	if err != nil || !found {
		return 1
	}
	return int32(n)
}

// rolloutState reads readiness from a Rollout's status: readyReplicas and replicas, and
// its selector (spec.selector, or status.selector as the scale subresource reports it).
func rolloutState(u *unstructured.Unstructured) workloadState {
	ready, _, _ := unstructured.NestedInt64(u.Object, "status", "readyReplicas")
	current, _, _ := unstructured.NestedInt64(u.Object, "status", "replicas")
	st := workloadState{desired: rolloutReplicas(u), ready: int32(ready), current: int32(current)}
	if raw, found, _ := unstructured.NestedMap(u.Object, "spec", "selector"); found {
		var sel metav1.LabelSelector
		if runtime.DefaultUnstructuredConverter.FromUnstructured(raw, &sel) == nil {
			st.selector = &sel
		}
	} else if s, _, _ := unstructured.NestedString(u.Object, "status", "selector"); s != "" {
		if sel, err := metav1.ParseToLabelSelector(s); err == nil {
			st.selector = sel
		}
	}
	return st
}

// rolloutTemplate is a Rollout's pod template, empty when it has none.
func rolloutTemplate(u *unstructured.Unstructured) corev1.PodTemplateSpec {
	var tmpl corev1.PodTemplateSpec
	if raw, found, _ := unstructured.NestedMap(u.Object, "spec", "template"); found {
		_ = runtime.DefaultUnstructuredConverter.FromUnstructured(raw, &tmpl)
	}
	return tmpl
}

// WorkloadInfo is a scalable workload as the dashboard and right-sizing advice see it.
type WorkloadInfo struct {
	Kind        string
	Name        string
	Replicas    int32
	Ready       int32
	Annotations map[string]string
	Template    corev1.PodTemplateSpec
}

// Workloads lists the Deployments, StatefulSets and Argo Rollouts of a namespace as
// schedules see them: a Deployment that a Rollout takes its pods from is left out.
func (e *Engine) Workloads(ctx context.Context, ns string) ([]WorkloadInfo, error) {
	objs, err := e.listScalableResources(ctx, ns, nil)
	if err != nil {
		return nil, err
	}
	out := make([]WorkloadInfo, 0, len(objs))
	for _, obj := range objs {
		st, _ := stateOf(obj)
		out = append(out, WorkloadInfo{Kind: workloadKind(obj), Name: obj.GetName(), Replicas: getReplicas(obj), Ready: st.ready, Annotations: obj.GetAnnotations(), Template: podTemplate(obj)})
	}
	return out, nil
}

// ErrUnknownKind is returned for a workload kind CostDeck does not scale.
var ErrUnknownKind = errors.New("unknown workload kind: use Deployment, StatefulSet or Rollout")

// SetWorkloadReplicas scales one workload by hand.
func (e *Engine) SetWorkloadReplicas(ctx context.Context, ns, kind, name string, replicas int32) error {
	key := client.ObjectKey{Namespace: ns, Name: name}
	var obj client.Object
	switch kind {
	case kindDeployment:
		obj = &appsv1.Deployment{}
	case "StatefulSet":
		obj = &appsv1.StatefulSet{}
	case kindRollout:
		gvk, ok, err := e.mapping(rolloutGK, "v1alpha1")
		if err != nil {
			return err
		}
		if !ok {
			return fmt.Errorf("%w: Argo Rollouts is not installed", ErrUnknownKind)
		}
		u := &unstructured.Unstructured{}
		u.SetGroupVersionKind(gvk)
		obj = u
	default:
		return ErrUnknownKind
	}
	if err := e.Client.Get(ctx, key, obj); err != nil {
		return err
	}
	patch := client.MergeFrom(obj.DeepCopyObject().(client.Object))
	switch o := obj.(type) {
	case *appsv1.Deployment:
		o.Spec.Replicas = &replicas
	case *appsv1.StatefulSet:
		o.Spec.Replicas = &replicas
	case *unstructured.Unstructured:
		if err := unstructured.SetNestedField(o.Object, int64(replicas), "spec", "replicas"); err != nil {
			return err
		}
	}
	return e.Client.Patch(ctx, obj, patch, client.FieldOwner(FieldManager))
}
