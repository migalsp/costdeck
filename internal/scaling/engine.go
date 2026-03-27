package scaling

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"

	finopsv1 "github.com/migalsp/costdeck-operator/api/v1"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/log"
)

type Engine struct {
	Client    client.Client
	Providers map[string]ExternalProvider
}

// ExternalProvider defines the interface for 3rd party cloud service scaling
type ExternalProvider interface {
	// Name returns the provider name (e.g. "aws", "gcp")
	Name() string
	// Scale sets the target state for a specific resource
	Scale(ctx context.Context, target finopsv1.ExternalTarget, active bool) error
	// IsReady checks if the target resource has reached the desired state
	IsReady(ctx context.Context, target finopsv1.ExternalTarget, active bool) (bool, error)
	// Discover returns a list of scalable targets in the environment
	Discover(ctx context.Context, resourceType string) ([]finopsv1.ExternalTarget, error)
}

// IsActive checks if the namespace/group should be active based on schedules and manual override.
func (e *Engine) IsActive(schedules []finopsv1.ScalingSchedule, manualActive *bool) bool {
	// 1. Manual override takes priority if explicitly set (non-nil)
	if manualActive != nil {
		return *manualActive
	}

	// 2. If no manual override, check schedules
	if len(schedules) > 0 {
		hasValidSchedule := false
		for _, s := range schedules {
			if len(s.Days) == 0 {
				continue
			}
			hasValidSchedule = true

			now := time.Now()
			if s.Timezone != "" {
				loc, err := time.LoadLocation(s.Timezone)
				if err == nil {
					now = now.In(loc)
				}
			}

			weekday := int(now.Weekday())
			nowMinutes := now.Hour()*60 + now.Minute()

			matchesDay := false
			for _, d := range s.Days {
				if d == weekday {
					matchesDay = true
					break
				}
			}
			if !matchesDay {
				continue
			}

			startMin := parseMinutes(s.StartTime)
			endMin := parseMinutes(s.EndTime)

			if nowMinutes >= startMin && nowMinutes <= endMin {
				return true
			}
		}

		if hasValidSchedule {
			return false // Valid schedules exist but none are active now
		}
	}

	return true // Default to active if no schedule and no manual override
}

func parseMinutes(hhmm string) int {
	var h, m int
	fmt.Sscanf(hhmm, "%d:%d", &h, &m)
	return h*60 + m
}

// ScaleTarget handles scaling for a specific namespace.
// It returns the updated map of original replicas and a boolean indicating if target state is fully reached.
func (e *Engine) ScaleTarget(ctx context.Context, ns string, active bool, sequence []string, exclusions []string, originalReplicas map[string]int32, timeoutPassed bool) (map[string]int32, bool, error) {
	if originalReplicas == nil {
		originalReplicas = make(map[string]int32)
	}

	// 1 & 2. List and Filter
	scalableResources, err := e.listScalableResources(ctx, ns, exclusions)
	if err != nil {
		return nil, false, err
	}

	// 3, 4. Group and Sort
	priorities, priorityGroups := e.groupAndSortPriorities(scalableResources, sequence, active)

	// 5. Execute Scaling by priority groups (NON-BLOCKING)
	for _, p := range priorities {
		objs := priorityGroups[p]

		ready, err := e.scalePriorityGroup(ctx, ns, objs, p, active, originalReplicas, timeoutPassed)
		if err != nil {
			return originalReplicas, false, err
		}
		if !ready {
			return originalReplicas, false, nil
		}
	}

	return originalReplicas, true, nil
}

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
			e.cleanupOriginals(objs, originalReplicas)
		}
		return true, nil
	}

	l.Info("Scaling priority group", "priority", p, "count", len(objs))
	for _, obj := range objs {
		e.scaleResource(ctx, obj, active, originalReplicas)
	}

	if !e.isGroupReady(ctx, objs, active) {
		if timeoutPassed {
			l.Info("Priority group not ready, bypassing due to timeout", "priority", p)
			return true, nil
		}
		return false, nil
	}

	if active {
		e.cleanupOriginals(objs, originalReplicas)
	}
	return true, nil
}

func (e *Engine) scaleResource(ctx context.Context, obj client.Object, active bool, originalReplicas map[string]int32) {
	l := log.FromContext(ctx)
	key := fmt.Sprintf("%T/%s", obj, obj.GetName())
	current := getReplicas(obj)
	target := e.getTargetReplicas(obj, active, current, originalReplicas)

	if current != target {
		if !active && current > 0 {
			originalReplicas[key] = current
		}
		l.Info("Setting replicas", "resource", key, "from", current, "to", target)
		if err := e.setReplicas(ctx, obj, target); err != nil {
			l.Error(err, "failed to update replicas", "resource", key)
		}
	}
}

func (e *Engine) getTargetReplicas(obj client.Object, active bool, current int32, originals map[string]int32) int32 {
	if !active {
		return 0
	}
	if current > 0 {
		return current
	}
	if t, ok := originals[fmt.Sprintf("%T/%s", obj, obj.GetName())]; ok {
		return t
	}
	return 1
}

func (e *Engine) cleanupOriginals(objs []client.Object, originals map[string]int32) {
	for _, obj := range objs {
		delete(originals, fmt.Sprintf("%T/%s", obj, obj.GetName()))
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
		if strings.HasSuffix(ex, "*") {
			if strings.HasPrefix(name, strings.TrimSuffix(ex, "*")) {
				return true
			}
		}
		if ex == name {
			return true
		}
	}
	return false
}

func getSequenceIndex(obj client.Object, sequence []string) int {
	name := obj.GetName()
	for i, s := range sequence {
		if s == "*" {
			return i
		}
		if strings.HasSuffix(s, "*") {
			if strings.HasPrefix(name, strings.TrimSuffix(s, "*")) {
				return i
			}
		}
		if strings.Contains(s, name) {
			return i
		}
	}
	return 999 // Parallel at the end/start
}

func getReplicas(obj client.Object) int32 {
	switch v := obj.(type) {
	case *appsv1.Deployment:
		return *v.Spec.Replicas
	case *appsv1.StatefulSet:
		return *v.Spec.Replicas
	}
	return 0
}

func (e *Engine) setReplicas(ctx context.Context, obj client.Object, count int32) error {
	switch v := obj.(type) {
	case *appsv1.Deployment:
		v.Spec.Replicas = &count
	case *appsv1.StatefulSet:
		v.Spec.Replicas = &count
	}
	return e.Client.Update(ctx, obj)
}

func (e *Engine) hasRemainingPods(ctx context.Context, ns string, matchLabels map[string]string) bool {
	if len(matchLabels) == 0 {
		return false
	}
	pods := &corev1.PodList{}
	err := e.Client.List(ctx, pods, client.InNamespace(ns), client.MatchingLabels(matchLabels))
	if err != nil {
		return true // assume pods exist if we can't be sure
	}
	return len(pods.Items) > 0
}

func (e *Engine) isGroupReady(ctx context.Context, objs []client.Object, targetActive bool) bool {
	for _, o := range objs {
		if !e.isResourceReady(ctx, o, targetActive) {
			return false
		}
	}
	return true
}

func (e *Engine) isResourceReady(ctx context.Context, o client.Object, targetActive bool) bool {
	key := client.ObjectKey{Name: o.GetName(), Namespace: o.GetNamespace()}
	e.Client.Get(ctx, key, o)

	var replicas, readyReplicas int32
	var matchLabels map[string]string

	switch v := o.(type) {
	case *appsv1.Deployment:
		if v.Spec.Replicas != nil {
			replicas = *v.Spec.Replicas
		}
		if v.Spec.Selector != nil {
			matchLabels = v.Spec.Selector.MatchLabels
		}
		readyReplicas = v.Status.ReadyReplicas
	case *appsv1.StatefulSet:
		if v.Spec.Replicas != nil {
			replicas = *v.Spec.Replicas
		}
		if v.Spec.Selector != nil {
			matchLabels = v.Spec.Selector.MatchLabels
		}
		readyReplicas = v.Status.ReadyReplicas
	default:
		return true
	}

	if targetActive {
		return replicas > 0 && readyReplicas >= replicas
	}

	// Scaling Down
	if readyReplicas > 0 || replicas > 0 || e.hasRemainingPods(ctx, o.GetNamespace(), matchLabels) {
		return false
	}
	return true
}

// ComputePhase checks actual replica states in the namespace and returns one of:
// ScaledUp, ScalingUp, ScaledDown, ScalingDown, PartlyScaled
func (e *Engine) ComputePhase(ctx context.Context, ns string, targetActive bool) string {
	deployments := &appsv1.DeploymentList{}
	_ = e.Client.List(ctx, deployments, client.InNamespace(ns))
	statefulSets := &appsv1.StatefulSetList{}
	_ = e.Client.List(ctx, statefulSets, client.InNamespace(ns))

	totalResources, zeroCount, readyCount := e.getScalingStats(ctx, ns, deployments.Items, statefulSets.Items)

	if totalResources == 0 {
		if targetActive {
			return "ScaledUp"
		}
		return "ScaledDown"
	}

	if targetActive {
		if readyCount == totalResources {
			return "ScaledUp"
		}
		return "ScalingUp"
	}

	if zeroCount == totalResources {
		return "ScaledDown"
	}
	return "ScalingDown"
}

func (e *Engine) getScalingStats(ctx context.Context, ns string, deploys []appsv1.Deployment, stss []appsv1.StatefulSet) (int, int, int) {
	total, zero, ready := 0, 0, 0
	for _, d := range deploys {
		total++
		z, r := e.getResourceStats(ctx, ns, &d)
		if z {
			zero++
		}
		if r {
			ready++
		}
	}
	for _, s := range stss {
		total++
		z, r := e.getResourceStats(ctx, ns, &s)
		if z {
			zero++
		}
		if r {
			ready++
		}
	}
	return total, zero, ready
}

func (e *Engine) getResourceStats(ctx context.Context, ns string, obj client.Object) (bool, bool) {
	var replicas, readyReplicas int32
	var matchLabels map[string]string
	var currentReplicas int32

	switch v := obj.(type) {
	case *appsv1.Deployment:
		if v.Spec.Replicas != nil {
			replicas = *v.Spec.Replicas
		}
		if v.Spec.Selector != nil {
			matchLabels = v.Spec.Selector.MatchLabels
		}
		readyReplicas = v.Status.ReadyReplicas
		currentReplicas = v.Status.Replicas
	case *appsv1.StatefulSet:
		if v.Spec.Replicas != nil {
			replicas = *v.Spec.Replicas
		}
		if v.Spec.Selector != nil {
			matchLabels = v.Spec.Selector.MatchLabels
		}
		readyReplicas = v.Status.ReadyReplicas
		currentReplicas = v.Status.Replicas
	}

	isZero := replicas == 0 && currentReplicas == 0 && !e.hasRemainingPods(ctx, ns, matchLabels)
	isReady := replicas > 0 && readyReplicas >= replicas
	return isZero, isReady
}
