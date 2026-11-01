// Package optimize applies right-sizing advice to workloads and takes it back: the old
// requests are kept in the namespace's NamespaceOptimization record, and the guard puts
// them back when the workload's new pods are OOMKilled, restart or crash-loop.
package optimize

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"time"

	"k8s.io/client-go/util/retry"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"sigs.k8s.io/controller-runtime/pkg/client"

	finopsv1 "github.com/migalsp/costdeck-operator/api/v1"
	"github.com/migalsp/costdeck-operator/internal/config"
	"github.com/migalsp/costdeck-operator/internal/rightsizing"
	"github.com/migalsp/costdeck-operator/internal/scaling"
)

// States of a change.
const (
	StateApplied  = "applied"
	StateKept     = "kept"
	StateReverted = "reverted"
	StateProposed = "proposed"
)

var (
	// ErrManagedByGitOps refuses a workload a GitOps tool would put back.
	ErrManagedByGitOps = errors.New("managed by GitOps")
	// ErrNothingToApply is advice that changes nothing.
	ErrNothingToApply = errors.New("the advice changes no request of this workload")
	// ErrUnsupported is a workload this cannot change.
	ErrUnsupported       = errors.New("only Deployments, StatefulSets and Argo Rollouts with their own pod template can be changed")
	ErrAmbiguousWorkload = errors.New("more than one workload matches")
)

var rolloutGVK = schema.GroupVersionKind{Group: "argoproj.io", Version: "v1alpha1", Kind: "Rollout"}

// load reads a workload.
func load(ctx context.Context, c client.Reader, ns, kind, name string) (client.Object, error) {
	var obj client.Object
	switch kind {
	case "Deployment":
		obj = &appsv1.Deployment{}
	case "StatefulSet":
		obj = &appsv1.StatefulSet{}
	case "Rollout":
		u := &unstructured.Unstructured{}
		u.SetGroupVersionKind(rolloutGVK)
		obj = u
	default:
		return nil, ErrUnsupported
	}
	if err := c.Get(ctx, client.ObjectKey{Namespace: ns, Name: name}, obj); err != nil {
		return nil, err
	}
	return obj, nil
}

// podSpec returns a workload's containers, a function that writes them back, and its
// selector.
func podSpec(obj client.Object) ([]corev1.Container, func([]corev1.Container) error, *metav1.LabelSelector, error) {
	switch o := obj.(type) {
	case *appsv1.Deployment:
		return o.Spec.Template.Spec.Containers, func(cs []corev1.Container) error { o.Spec.Template.Spec.Containers = cs; return nil }, o.Spec.Selector, nil
	case *appsv1.StatefulSet:
		return o.Spec.Template.Spec.Containers, func(cs []corev1.Container) error { o.Spec.Template.Spec.Containers = cs; return nil }, o.Spec.Selector, nil
	case *unstructured.Unstructured:
		raw, found, _ := unstructured.NestedSlice(o.Object, "spec", "template", "spec", "containers")
		if !found {
			return nil, nil, nil, fmt.Errorf("%w: this Rollout takes its pods from a Deployment (workloadRef); change that Deployment", ErrUnsupported)
		}
		var cs []corev1.Container
		for _, r := range raw {
			var c corev1.Container
			if m, ok := r.(map[string]any); ok && runtime.DefaultUnstructuredConverter.FromUnstructured(m, &c) == nil {
				cs = append(cs, c)
			}
		}
		var sel *metav1.LabelSelector
		if m, found, _ := unstructured.NestedMap(o.Object, "spec", "selector"); found {
			sel = &metav1.LabelSelector{}
			_ = runtime.DefaultUnstructuredConverter.FromUnstructured(m, sel)
		}
		set := func(cs []corev1.Container) error {
			out := make([]any, len(cs))
			for i := range cs {
				m, err := runtime.DefaultUnstructuredConverter.ToUnstructured(&cs[i])
				if err != nil {
					return err
				}
				out[i] = m
			}
			return unstructured.SetNestedSlice(o.Object, out, "spec", "template", "spec", "containers")
		}
		return cs, set, sel, nil
	}
	return nil, nil, nil, ErrUnsupported
}

func values(r corev1.ResourceRequirements) finopsv1.ResourceValues {
	str := func(l corev1.ResourceList, n corev1.ResourceName) string {
		if q, ok := l[n]; ok {
			return q.String()
		}
		return ""
	}
	return finopsv1.ResourceValues{
		CPURequest: str(r.Requests, corev1.ResourceCPU), CPULimit: str(r.Limits, corev1.ResourceCPU),
		MemoryRequest: str(r.Requests, corev1.ResourceMemory), MemoryLimit: str(r.Limits, corev1.ResourceMemory),
	}
}

// resources rebuilds requests or limits from recorded strings; an empty one is unset.
func resources(cpu, mem string) corev1.ResourceList {
	out := corev1.ResourceList{}
	if q, err := resource.ParseQuantity(cpu); err == nil && cpu != "" {
		out[corev1.ResourceCPU] = q
	}
	if q, err := resource.ParseQuantity(mem); err == nil && mem != "" {
		out[corev1.ResourceMemory] = q
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

// recommended is the request advice sets, or false when it leaves the request alone.
func recommended(a rightsizing.ResourceAdvice) (resource.Quantity, bool) {
	switch a.Action {
	case rightsizing.ActionReduce, rightsizing.ActionIncrease, rightsizing.ActionSet:
		q, err := resource.ParseQuantity(a.Recommended)
		return q, err == nil && a.Recommended != ""
	}
	return resource.Quantity{}, false
}

// setRequest sets a request, never above the container's limit.
func setRequest(r *corev1.ResourceRequirements, name corev1.ResourceName, q resource.Quantity) {
	if limit, ok := r.Limits[name]; ok && q.Cmp(limit) > 0 {
		q = limit.DeepCopy()
	}
	if r.Requests == nil {
		r.Requests = corev1.ResourceList{}
	}
	r.Requests[name] = q
}

// Apply sets a workload's requests to the advice and records what they were, by whom.
func Apply(ctx context.Context, c client.Client, ns string, advice rightsizing.WorkloadAdvice, by string, now time.Time) (finopsv1.WorkloadOptimization, error) {
	rec := finopsv1.WorkloadOptimization{Kind: advice.Kind, Name: advice.Name, AppliedBy: by, State: StateApplied}
	obj, err := load(ctx, c, ns, advice.Kind, advice.Name)
	if err != nil {
		return rec, err
	}
	if owner := scaling.GitOpsOwnerOf(obj); owner != nil {
		return rec, fmt.Errorf("%w: %s would put the old requests back; open a pull request instead", ErrManagedByGitOps, owner)
	}
	containers, set, _, err := podSpec(obj)
	if err != nil {
		return rec, err
	}
	patch := client.MergeFrom(obj.DeepCopyObject().(client.Object))
	for i := range containers {
		ca := adviceFor(advice, containers[i].Name)
		if ca == nil {
			continue
		}
		before := values(containers[i].Resources)
		if q, ok := recommended(ca.CPU); ok {
			setRequest(&containers[i].Resources, corev1.ResourceCPU, q)
		}
		if q, ok := recommended(ca.Memory); ok {
			setRequest(&containers[i].Resources, corev1.ResourceMemory, q)
		}
		if after := values(containers[i].Resources); after != before {
			rec.Containers = append(rec.Containers, finopsv1.ContainerOptimization{Name: containers[i].Name, Original: before, Optimized: after})
		}
	}
	if len(rec.Containers) == 0 {
		return rec, ErrNothingToApply
	}
	if err := set(containers); err != nil {
		return rec, err
	}
	if err := c.Patch(ctx, obj, patch, client.FieldOwner(scaling.FieldManager)); err != nil {
		return rec, err
	}
	at := metav1.NewTime(now)
	rec.AppliedAt = &at
	rec.Original, rec.Optimized = rec.Containers[0].Original, rec.Containers[0].Optimized
	if err := Record(ctx, c, ns, rec); err != nil {
		if undoErr := restore(ctx, c, ns, rec); undoErr != nil {
			return rec, fmt.Errorf("could not record the change (%w), and putting the old requests back failed too: %w", err, undoErr)
		}
		return rec, fmt.Errorf("could not record the change, so it was undone: %w", err)
	}
	return rec, nil
}

func adviceFor(a rightsizing.WorkloadAdvice, container string) *rightsizing.ContainerAdvice {
	for i := range a.Containers {
		if a.Containers[i].Name == container {
			return &a.Containers[i]
		}
	}
	return nil
}

// Record adds a change to the namespace's NamespaceOptimization, replacing an earlier
// one of the workload. A concurrent change of the record is read again and retried.
func Record(ctx context.Context, c client.Client, ns string, rec finopsv1.WorkloadOptimization) error {
	changedMeanwhile := func(err error) bool { return apierrors.IsConflict(err) || apierrors.IsAlreadyExists(err) }
	return retry.OnError(retry.DefaultBackoff, changedMeanwhile, func() error { return recordOnce(ctx, c, ns, rec) })
}

func recordOnce(ctx context.Context, c client.Client, ns string, rec finopsv1.WorkloadOptimization) error {
	opt := &finopsv1.NamespaceOptimization{}
	key := client.ObjectKey{Namespace: config.OperatorNamespace(), Name: ns}
	err := c.Get(ctx, key, opt)
	if apierrors.IsNotFound(err) {
		opt = &finopsv1.NamespaceOptimization{Namespace: key.Namespace, Name: ns, Spec: finopsv1.NamespaceOptimizationSpec{TargetNamespace: ns}}
		if err := c.Create(ctx, opt); err != nil {
			return err
		}
	} else if err != nil {
		return err
	}
	opt.Status.Workloads = slices.DeleteFunc(opt.Status.Workloads, func(w finopsv1.WorkloadOptimization) bool { return w.Kind == rec.Kind && w.Name == rec.Name })
	opt.Status.Workloads = append(opt.Status.Workloads, rec)
	if rec.AppliedAt != nil {
		opt.Status.OptimizedAt = *rec.AppliedAt
	}
	opt.Status.Active = slices.ContainsFunc(opt.Status.Workloads, func(w finopsv1.WorkloadOptimization) bool {
		return w.State == StateApplied || w.State == StateKept || (w.State == "" && opt.Status.Active)
	})
	return c.Status().Update(ctx, opt)
}

// Revert puts back what a change replaced and marks it reverted, with why. An empty kind
// matches any kind, as long as only one workload of that name has a change.
func Revert(ctx context.Context, c client.Client, ns, kind, name, reason string) (finopsv1.WorkloadOptimization, error) {
	opt := &finopsv1.NamespaceOptimization{}
	if err := c.Get(ctx, client.ObjectKey{Namespace: config.OperatorNamespace(), Name: ns}, opt); err != nil {
		return finopsv1.WorkloadOptimization{}, err
	}
	var matches []finopsv1.WorkloadOptimization
	for _, w := range opt.Status.Workloads {
		if w.Name == name && (kind == "" || w.Kind == kind) {
			matches = append(matches, w)
		}
	}
	switch {
	case len(matches) == 0:
		return finopsv1.WorkloadOptimization{}, apierrors.NewNotFound(schema.GroupResource{Group: "finops.costdeck.io", Resource: "workloadoptimizations"}, name)
	case len(matches) > 1:
		return finopsv1.WorkloadOptimization{}, fmt.Errorf("%w: %d workloads named %s have a change; say which kind", ErrAmbiguousWorkload, len(matches), name)
	}
	rec := matches[0]
	if rec.State != StateProposed && rec.State != StateReverted {
		if err := restore(ctx, c, ns, rec); err != nil && !apierrors.IsNotFound(err) {
			return rec, err
		}
	}
	rec.State, rec.Reason = StateReverted, reason
	return rec, Record(ctx, c, ns, rec)
}

// restore writes a change's original requests and limits back into the workload.
func restore(ctx context.Context, c client.Client, ns string, rec finopsv1.WorkloadOptimization) error {
	obj, err := load(ctx, c, ns, rec.Kind, rec.Name)
	if err != nil {
		return err
	}
	containers, set, _, err := podSpec(obj)
	if err != nil || len(containers) == 0 {
		return err
	}
	patch := client.MergeFrom(obj.DeepCopyObject().(client.Object))
	originals := rec.Containers
	if len(originals) == 0 { // a record made before containers were recorded
		originals = []finopsv1.ContainerOptimization{{Name: containers[0].Name, Original: rec.Original}}
	}
	for _, o := range originals {
		for i := range containers {
			if containers[i].Name == o.Name {
				containers[i].Resources.Requests = resources(o.Original.CPURequest, o.Original.MemoryRequest)
				containers[i].Resources.Limits = resources(o.Original.CPULimit, o.Original.MemoryLimit)
			}
		}
	}
	if err := set(containers); err != nil {
		return err
	}
	return c.Patch(ctx, obj, patch, client.FieldOwner(scaling.FieldManager))
}

// RevertAll puts back every change in a namespace.
func RevertAll(ctx context.Context, c client.Client, ns, reason string) error {
	opt := &finopsv1.NamespaceOptimization{}
	if err := c.Get(ctx, client.ObjectKey{Namespace: config.OperatorNamespace(), Name: ns}, opt); err != nil {
		return err
	}
	for _, w := range opt.Status.Workloads {
		if w.State == StateReverted || w.State == StateProposed {
			continue
		}
		if _, err := Revert(ctx, c, ns, w.Kind, w.Name, reason); err != nil {
			return fmt.Errorf("could not restore %s %s: %w", w.Kind, w.Name, err)
		}
	}
	return nil
}
