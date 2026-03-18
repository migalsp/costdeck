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
	l := log.FromContext(ctx).WithValues("namespace", ns, "targetActive", active)

	if originalReplicas == nil {
		originalReplicas = make(map[string]int32)
	}

	// 1. List all scalable resources in the namespace
	deployments := &appsv1.DeploymentList{}
	if err := e.Client.List(ctx, deployments, client.InNamespace(ns)); err != nil {
		return nil, false, err
	}

	statefulSets := &appsv1.StatefulSetList{}
	if err := e.Client.List(ctx, statefulSets, client.InNamespace(ns)); err != nil {
		return nil, false, err
	}

	// 2. Filter exclusions
	scalableResources := []client.Object{}
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

	// 3. Group by priority
	priorityGroups := make(map[int][]client.Object)
	for _, obj := range scalableResources {
		idx := getSequenceIndex(obj, sequence)
		priorityGroups[idx] = append(priorityGroups[idx], obj)
	}

	// 4. Sort priorities
	priorities := []int{}
	for p := range priorityGroups {
		priorities = append(priorities, p)
	}
	sort.Ints(priorities)

	// If scaling UP, reverse priorities
	if active {
		for i, j := 0, len(priorities)-1; i < j; i, j = i+1, j-1 {
			priorities[i], priorities[j] = priorities[j], priorities[i]
		}
	}

	// 5. Execute Scaling by priority groups (NON-BLOCKING)
	for _, p := range priorities {
		objs := priorityGroups[p]

		// First, check if this priority group is ALREADY ready.
		// If so, we move to the next.
		if e.isGroupReady(ctx, objs, active) {
			continue
		}

		// Group is not ready. Act on it.
		l.Info("Scaling priority group", "priority", p, "count", len(objs))
		for _, obj := range objs {
			key := fmt.Sprintf("%T/%s", obj, obj.GetName())

			// Target replicas for this object
			var target int32
			current := getReplicas(obj)

			if !active {
				target = 0
			} else {
				if current > 0 {
					// Respect manual or HPA scaling that occurred during active state.
					target = current
				} else {
					if t, ok := originalReplicas[key]; ok {
						target = t
					} else {
						// Fallback if no record of original replicas
						target = 1
					}
				}
			}
			if current != target {
				// Record original IF scaling down for the first time
				if !active && current > 0 {
					originalReplicas[key] = current
				}

				l.Info("Setting replicas", "resource", key, "from", current, "to", target)
				if err := e.setReplicas(ctx, obj, target); err != nil {
					l.Error(err, "failed to update replicas", "resource", key, "target", target)
				}
			}
		}

		// After acting, check if it reached readiness.
		// If not, we return false and stop here (strict sequencing).
		if !e.isGroupReady(ctx, objs, active) {
			if timeoutPassed {
				l.Info("Priority group not yet ready, but 1-minute timeout passed! Bypassing strict sequence for this group.", "priority", p)
			} else {
				l.Info("Priority group not yet ready, stopping for now", "priority", p)
				return originalReplicas, false, nil
			}
		}

		// If scaling UP, we can now safely remove from originals IF they are ready.
		if active && e.isGroupReady(ctx, objs, active) {
			for _, obj := range objs {
				key := fmt.Sprintf("%T/%s", obj, obj.GetName())
				delete(originalReplicas, key)
			}
		}
	}

	return originalReplicas, true, nil
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
		// Refetch to get latest status
		key := client.ObjectKey{Name: o.GetName(), Namespace: o.GetNamespace()}
		switch v := o.(type) {
		case *appsv1.Deployment:
			e.Client.Get(ctx, key, v)
			if targetActive {
				target := int32(0)
				if v.Spec.Replicas != nil {
					target = *v.Spec.Replicas
				}
				// If target is still 0, the deployment hasn't been scaled up yet → NOT ready
				if target == 0 {
					return false
				}
				if v.Status.ReadyReplicas < target {
					return false
				}
			} else {
				if v.Status.ReadyReplicas > 0 || v.Status.Replicas > 0 {
					return false
				}
				if v.Spec.Selector != nil && e.hasRemainingPods(ctx, v.GetNamespace(), v.Spec.Selector.MatchLabels) {
					return false
				}
			}
		case *appsv1.StatefulSet:
			e.Client.Get(ctx, key, v)
			if targetActive {
				target := int32(0)
				if v.Spec.Replicas != nil {
					target = *v.Spec.Replicas
				}
				if target == 0 {
					return false
				}
				if v.Status.ReadyReplicas < target {
					return false
				}
			} else {
				if v.Status.ReadyReplicas > 0 || v.Status.Replicas > 0 {
					return false
				}
				if v.Spec.Selector != nil && e.hasRemainingPods(ctx, v.GetNamespace(), v.Spec.Selector.MatchLabels) {
					return false
				}
			}
		}
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

	totalResources := 0
	runningCount := 0 // spec.replicas > 0
	zeroCount := 0    // spec.replicas == 0
	readyCount := 0   // all pods ready (readyReplicas == spec.replicas)

	for _, d := range deployments.Items {
		totalResources++
		replicas := int32(1)
		if d.Spec.Replicas != nil {
			replicas = *d.Spec.Replicas
		}
		if replicas == 0 && d.Status.Replicas == 0 {
			if d.Spec.Selector != nil && e.hasRemainingPods(ctx, ns, d.Spec.Selector.MatchLabels) {
				runningCount++
			} else {
				zeroCount++
			}
		} else {
			runningCount++
			if replicas > 0 && d.Status.ReadyReplicas >= replicas {
				readyCount++
			}
		}
	}
	for _, s := range statefulSets.Items {
		totalResources++
		replicas := int32(1)
		if s.Spec.Replicas != nil {
			replicas = *s.Spec.Replicas
		}
		if replicas == 0 && s.Status.Replicas == 0 {
			if s.Spec.Selector != nil && e.hasRemainingPods(ctx, ns, s.Spec.Selector.MatchLabels) {
				runningCount++
			} else {
				zeroCount++
			}
		} else {
			runningCount++
			if replicas > 0 && s.Status.ReadyReplicas >= replicas {
				readyCount++
			}
		}
	}

	if totalResources == 0 {
		if targetActive {
			return "ScaledUp"
		}
		return "ScaledDown"
	}

	if targetActive {
		// We want everything up but some are still at 0 or not ready
		if readyCount == totalResources {
			return "ScaledUp"
		}
		return "ScalingUp"
	}

	// We want everything down
	if zeroCount == totalResources {
		return "ScaledDown"
	}
	return "ScalingDown"
}
