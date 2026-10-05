package scaling

import (
	"context"
	"fmt"
	"path"
	"sort"
	"strconv"
	"strings"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/log"
)

// unsequenced is the priority of workloads that match no sequence entry. They scale last
// on the way up and first on the way down.
const unsequenced = 999

// OriginalReplicasAnnotation records on the workload itself how many replicas it had
// before CostDeck scaled it to zero. It is written in the same patch that scales the
// workload down, so the count survives a failed status update, a restart or a deleted
// group; status.originalReplicas is only the fallback for workloads scaled before it
// existed.
const OriginalReplicasAnnotation = "costdeck.io/original-replicas"

// Phase values reported by ComputePhase and stored in status.phase.
const (
	PhaseScaledUp    = "ScaledUp"
	PhaseScalingUp   = "ScalingUp"
	PhaseScaledDown  = "ScaledDown"
	PhaseScalingDown = "ScalingDown"
)

func (e *Engine) listScalableResources(ctx context.Context, ns string, exclusions []string) ([]client.Object, error) {
	deployments := &appsv1.DeploymentList{}
	if err := e.Client.List(ctx, deployments, client.InNamespace(ns)); err != nil {
		return nil, err
	}
	statefulSets := &appsv1.StatefulSetList{}
	if err := e.Client.List(ctx, statefulSets, client.InNamespace(ns)); err != nil {
		return nil, err
	}

	var scalableResources []client.Object
	for i := range deployments.Items {
		if !isExcluded(deployments.Items[i].Name, exclusions) {
			scalableResources = append(scalableResources, &deployments.Items[i])
		}
	}
	for i := range statefulSets.Items {
		if !isExcluded(statefulSets.Items[i].Name, exclusions) {
			scalableResources = append(scalableResources, &statefulSets.Items[i])
		}
	}
	return scalableResources, nil
}

func (e *Engine) groupAndSortPriorities(resources []client.Object, sequence []string, active bool) ([]int, map[int][]client.Object) {
	priorityGroups := make(map[int][]client.Object)
	for _, obj := range resources {
		idx := getSequenceIndex(obj, sequence)
		priorityGroups[idx] = append(priorityGroups[idx], obj)
	}

	priorities := make([]int, 0, len(priorityGroups))
	for p := range priorityGroups {
		priorities = append(priorities, p)
	}
	sort.Ints(priorities)

	if active {
		for i, j := 0, len(priorities)-1; i < j; i, j = i+1, j-1 {
			priorities[i], priorities[j] = priorities[j], priorities[i]
		}
	}
	return priorities, priorityGroups
}

func (e *Engine) scalePriorityGroup(ctx context.Context, ns string, objs []client.Object, p int, active bool, originalReplicas map[string]int32, timeoutPassed bool) (bool, error) {
	l := log.FromContext(ctx).WithValues("namespace", ns, "targetActive", active)

	if e.isGroupReady(ctx, objs, active) {
		if active {
			cleanupOriginals(objs, originalReplicas)
		}
		return true, nil
	}

	l.Info("Scaling priority group", "priority", p, "count", len(objs))
	for _, obj := range objs {
		if err := e.scaleResource(ctx, obj, active, originalReplicas); err != nil {
			return false, err
		}
	}

	if !e.isGroupReady(ctx, objs, active) {
		if timeoutPassed {
			l.Info("Priority group not ready, bypassing due to timeout", "priority", p)
			return true, nil
		}
		return false, nil
	}

	if active {
		cleanupOriginals(objs, originalReplicas)
	}
	return true, nil
}

// originalKey identifies a workload in status.originalReplicas. The "%T/%s" form
// ("*v1.Deployment/name") is what existing objects already store, so it is kept as is.
func originalKey(obj client.Object) string {
	return fmt.Sprintf("%T/%s", obj, obj.GetName())
}

func (e *Engine) scaleResource(ctx context.Context, obj client.Object, active bool, originalReplicas map[string]int32) error {
	key := originalKey(obj)
	current := getReplicas(obj)
	target := targetReplicas(active, current, recordedReplicas(obj, originalReplicas))
	// Nothing to do when the count is right, unless a restored workload still carries the
	// annotation, which a later manual scale-to-zero must not inherit.
	_, annotated := obj.GetAnnotations()[OriginalReplicasAnnotation]
	if current == target && (!active || !annotated) {
		return nil
	}

	var record int32
	if !active && current > 0 {
		record = current
		originalReplicas[key] = current
	}
	log.FromContext(ctx).Info("Setting replicas", "resource", key, "namespace", obj.GetNamespace(), "from", current, "to", target)
	if err := e.setReplicas(ctx, obj, target, record); err != nil {
		return fmt.Errorf("scale %s in %s to %d replicas: %w", key, obj.GetNamespace(), target, err)
	}
	return nil
}

// recordedReplicas returns the replica count a workload had before it was scaled down:
// the annotation on the workload, else the group's status, else zero.
func recordedReplicas(obj client.Object, originals map[string]int32) int32 {
	if v, err := strconv.ParseInt(obj.GetAnnotations()[OriginalReplicasAnnotation], 10, 32); err == nil && v > 0 {
		return int32(v)
	}
	return max(originals[originalKey(obj)], 0)
}

// targetReplicas decides the replica count for a workload. Scaling down always means zero.
// Scaling up restores the count recorded at scale-down time (original, zero if none),
// unless the workload already runs more replicas than that; with no record it gets one.
func targetReplicas(active bool, current, original int32) int32 {
	if !active {
		return 0
	}
	if original > current {
		return original
	}
	if current > 0 {
		return current
	}
	return 1
}

func cleanupOriginals(objs []client.Object, originals map[string]int32) {
	for _, obj := range objs {
		delete(originals, originalKey(obj))
	}
}

func isExcluded(name string, exclusions []string) bool {
	name = strings.TrimSpace(name)
	for _, ex := range exclusions {
		ex = strings.TrimSpace(ex)
		if ex == "" {
			continue
		}
		if ex == "*" {
			return true
		}
		if before, ok := strings.CutSuffix(ex, "*"); ok {
			if strings.HasPrefix(name, before) {
				return true
			}
		}
		if ex == name {
			return true
		}
	}
	return false
}

// getSequenceIndex returns the index of the first sequence stage that matches the
// workload. A stage is a space separated list of patterns; each pattern is a workload name
// glob ("api", "api-*"), optionally qualified as "Kind/name" or "group/version:Kind/name".
// A bare "*" matches everything at its position.
func getSequenceIndex(obj client.Object, sequence []string) int {
	kind := workloadKind(obj)
	name := obj.GetName()
	for i, stage := range sequence {
		for pattern := range strings.FieldsSeq(stage) {
			if matchWorkloadPattern(pattern, kind, name) {
				return i
			}
		}
	}
	return unsequenced
}

func matchWorkloadPattern(pattern, kind, name string) bool {
	if pattern == "*" {
		return true
	}
	// Drop an optional "group/version:" prefix.
	if _, rest, ok := strings.Cut(pattern, ":"); ok {
		pattern = rest
	}
	if k, n, ok := strings.Cut(pattern, "/"); ok {
		if !strings.EqualFold(k, kind) {
			return false
		}
		pattern = n
	}
	matched, err := path.Match(pattern, name)
	return err == nil && matched
}

func workloadKind(obj client.Object) string {
	switch obj.(type) {
	case *appsv1.Deployment:
		return "Deployment"
	case *appsv1.StatefulSet:
		return "StatefulSet"
	}
	return ""
}

// getReplicas returns the desired replica count, treating an unset field as the API
// server default of one.
func getReplicas(obj client.Object) int32 {
	var replicas *int32
	switch v := obj.(type) {
	case *appsv1.Deployment:
		replicas = v.Spec.Replicas
	case *appsv1.StatefulSet:
		replicas = v.Spec.Replicas
	}
	if replicas == nil {
		return 1
	}
	return *replicas
}

// setReplicas changes spec.replicas, and the original-replicas annotation, with one merge
// patch, so it neither conflicts with nor overwrites concurrent edits to the rest of the
// object. A non-zero record is stored in the annotation; scaling up removes it.
func (e *Engine) setReplicas(ctx context.Context, obj client.Object, count, record int32) error {
	patch := client.MergeFrom(obj.DeepCopyObject().(client.Object))
	switch v := obj.(type) {
	case *appsv1.Deployment:
		v.Spec.Replicas = &count
	case *appsv1.StatefulSet:
		v.Spec.Replicas = &count
	default:
		return fmt.Errorf("unsupported workload type %T", obj)
	}
	annotations := obj.GetAnnotations()
	switch {
	case record > 0:
		if annotations == nil {
			annotations = map[string]string{}
		}
		annotations[OriginalReplicasAnnotation] = strconv.Itoa(int(record))
	case count > 0:
		delete(annotations, OriginalReplicasAnnotation)
	}
	obj.SetAnnotations(annotations)
	return e.Client.Patch(ctx, obj, patch)
}

// hasRemainingPods reports whether any pod selected by the workload is still running or
// terminating. Completed and failed pods (for example evicted ones, which linger until
// garbage collection) do not count.
func (e *Engine) hasRemainingPods(ctx context.Context, ns string, selector *metav1.LabelSelector) bool {
	if selector == nil {
		return false
	}
	sel, err := metav1.LabelSelectorAsSelector(selector)
	if err != nil || sel.Empty() {
		return false
	}
	pods := &corev1.PodList{}
	if err := e.Client.List(ctx, pods, client.InNamespace(ns), client.MatchingLabelsSelector{Selector: sel}); err != nil {
		return true // Assume pods exist if we can't be sure.
	}
	for _, p := range pods.Items {
		if p.Status.Phase != corev1.PodSucceeded && p.Status.Phase != corev1.PodFailed {
			return true
		}
	}
	return false
}

func (e *Engine) isGroupReady(ctx context.Context, objs []client.Object, targetActive bool) bool {
	for _, o := range objs {
		if !e.isResourceReady(ctx, o, targetActive) {
			return false
		}
	}
	return true
}

// workloadState extracts what readiness checks need from a Deployment or StatefulSet.
type workloadState struct {
	desired, ready, current int32
	selector                *metav1.LabelSelector
}

func stateOf(obj client.Object) (workloadState, bool) {
	switch v := obj.(type) {
	case *appsv1.Deployment:
		return workloadState{getReplicas(v), v.Status.ReadyReplicas, v.Status.Replicas, v.Spec.Selector}, true
	case *appsv1.StatefulSet:
		return workloadState{getReplicas(v), v.Status.ReadyReplicas, v.Status.Replicas, v.Spec.Selector}, true
	}
	return workloadState{}, false
}

func (e *Engine) isResourceReady(ctx context.Context, o client.Object, targetActive bool) bool {
	if err := e.Client.Get(ctx, client.ObjectKeyFromObject(o), o); err != nil {
		// A workload deleted mid-way has nothing left to wait for; any other error means
		// we cannot tell, so keep waiting.
		return apierrors.IsNotFound(err)
	}
	st, ok := stateOf(o)
	if !ok {
		return true
	}
	if targetActive {
		return st.desired > 0 && st.ready >= st.desired
	}
	return st.desired == 0 && st.ready == 0 && !e.hasRemainingPods(ctx, o.GetNamespace(), st.selector)
}

// ComputePhase checks actual replica states in the namespace and returns one of
// ScaledUp, ScalingUp, ScaledDown or ScalingDown.
func (e *Engine) ComputePhase(ctx context.Context, ns string, targetActive bool) string {
	objs, err := e.listScalableResources(ctx, ns, nil)
	if err != nil {
		if targetActive {
			return PhaseScalingUp
		}
		return PhaseScalingDown
	}

	total, zero, ready := 0, 0, 0
	for _, obj := range objs {
		st, _ := stateOf(obj)
		total++
		if st.desired == 0 && st.current == 0 && !e.hasRemainingPods(ctx, ns, st.selector) {
			zero++
		}
		if st.desired > 0 && st.ready >= st.desired {
			ready++
		}
	}

	switch {
	case targetActive && ready == total:
		return PhaseScaledUp
	case targetActive:
		return PhaseScalingUp
	case zero == total:
		return PhaseScaledDown
	default:
		return PhaseScalingDown
	}
}

// KeptDown returns the CPU and memory requests CostDeck currently keeps off in a
// namespace: for every workload with a recorded original replica count, the per-pod
// requests times the replicas it is short of that count.
func (e *Engine) KeptDown(ctx context.Context, ns string, originals map[string]int32) (resource.Quantity, resource.Quantity, error) {
	var cpu, mem resource.Quantity
	if len(originals) == 0 {
		return cpu, mem, nil
	}
	objs, err := e.listScalableResources(ctx, ns, nil)
	if err != nil {
		return cpu, mem, err
	}
	for _, obj := range objs {
		missing := int64(recordedReplicas(obj, originals) - getReplicas(obj))
		if missing <= 0 {
			continue
		}
		var tmpl corev1.PodTemplateSpec
		switch v := obj.(type) {
		case *appsv1.Deployment:
			tmpl = v.Spec.Template
		case *appsv1.StatefulSet:
			tmpl = v.Spec.Template
		}
		podCPU, podMem := PodRequests(tmpl.Spec)
		podCPU.Mul(missing)
		podMem.Mul(missing)
		cpu.Add(podCPU)
		mem.Add(podMem)
	}
	return cpu, mem, nil
}

// PodRequests returns the effective requests of a pod spec: the sum of its containers,
// or its largest init container when that is bigger, as the scheduler counts them.
func PodRequests(spec corev1.PodSpec) (resource.Quantity, resource.Quantity) {
	var cpu, mem resource.Quantity
	for _, c := range spec.Containers {
		cpu.Add(*c.Resources.Requests.Cpu())
		mem.Add(*c.Resources.Requests.Memory())
	}
	for _, c := range spec.InitContainers {
		if q := c.Resources.Requests.Cpu(); q.Cmp(cpu) > 0 {
			cpu = q.DeepCopy()
		}
		if q := c.Resources.Requests.Memory(); q.Cmp(mem) > 0 {
			mem = q.DeepCopy()
		}
	}
	return cpu, mem
}
