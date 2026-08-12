package scaling

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"

	finopsv1 "github.com/migalsp/costdeck-operator/api/v1"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
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
	// Discover returns a list of scalable targets in the environment, optionally filtered by tags
	Discover(ctx context.Context, resourceType string, tags map[string]string) ([]finopsv1.ExternalTarget, error)
}

const (
	minutesPerDay  = 24 * 60
	minutesPerWeek = 7 * minutesPerDay
)

// ResolveDesiredState decides whether a group/namespace should be scaled up right now,
// combining the manual override (bounded by activeUntil, if any) with the schedules.
func (e *Engine) ResolveDesiredState(schedules []finopsv1.ScalingSchedule, active *bool, activeUntil *metav1.Time) bool {
	return e.IsActiveAt(time.Now(), schedules, EffectiveOverride(active, activeUntil, time.Now()))
}

// EffectiveOverride returns the manual override that applies at the given time.
// An override with an activeUntil deadline in the past is reported as absent, which hands
// control back to the schedule without anyone having to clear spec.active by hand.
func EffectiveOverride(active *bool, activeUntil *metav1.Time, now time.Time) *bool {
	if active == nil {
		return nil
	}
	if activeUntil != nil && !activeUntil.IsZero() && !now.Before(activeUntil.Time) {
		return nil
	}
	return active
}

// IsActive checks if the namespace/group should be active based on schedules and manual override.
func (e *Engine) IsActive(schedules []finopsv1.ScalingSchedule, manualActive *bool) bool {
	return e.IsActiveAt(time.Now(), schedules, manualActive)
}

// IsActiveAt is the time-injectable core of IsActive.
func (e *Engine) IsActiveAt(now time.Time, schedules []finopsv1.ScalingSchedule, manualActive *bool) bool {
	// 1. Manual override takes priority if explicitly set (non-nil)
	if manualActive != nil {
		return *manualActive
	}

	// 2. If no manual override, check schedules. Any matching window activates.
	hasValidSchedule := false
	for _, s := range schedules {
		window, ok := parseWindow(s)
		if !ok {
			continue
		}
		hasValidSchedule = true

		if window.contains(weekMinute(now.In(loadLocation(s.Timezone)))) {
			return true
		}
	}

	if hasValidSchedule {
		return false // Valid schedules exist but none are active now
	}

	// Default to active if there is no usable schedule and no manual override. Staying up
	// is the fail-safe direction: a malformed schedule must never scale a workload to zero.
	return true
}

// weeklyWindow is a half-open-free interval over minutes of the week (0..10079, Sunday
// 00:00 being 0). Expressing every schedule form in this single space is what makes
// overnight and multi-day windows fall out for free: a window that wraps past Sunday
// midnight simply has end < start.
type weeklyWindow struct {
	start int
	end   int
}

func (w weeklyWindow) contains(m int) bool {
	if w.start <= w.end {
		return m >= w.start && m <= w.end
	}
	return m >= w.start || m <= w.end
}

func weekMinute(t time.Time) int {
	return int(t.Weekday())*minutesPerDay + t.Hour()*60 + t.Minute()
}

// parseWindow converts a ScalingSchedule into a set of week-minute windows.
// It reports false when the schedule carries no usable window at all.
func parseWindow(s finopsv1.ScalingSchedule) (multiWindow, bool) {
	startMin, okStart := parseMinutes(s.StartTime)
	endMin, okEnd := parseMinutes(s.EndTime)
	if !okStart || !okEnd {
		return nil, false
	}

	// Continuous weekly window: Monday 00:00 -> Friday 23:59 is one uninterrupted
	// interval, so there is no daily boundary left for workloads to flap on.
	if s.StartDay != nil && s.EndDay != nil {
		if !isWeekday(*s.StartDay) || !isWeekday(*s.EndDay) {
			return nil, false
		}
		return multiWindow{{
			start: *s.StartDay*minutesPerDay + startMin,
			end:   *s.EndDay*minutesPerDay + endMin,
		}}, true
	}

	if len(s.Days) == 0 {
		return nil, false
	}

	// Daily window, repeated on every listed weekday. When EndTime is earlier than
	// StartTime the window is overnight and runs into the following day.
	windows := make(multiWindow, 0, len(s.Days))
	for _, d := range s.Days {
		if !isWeekday(d) {
			continue
		}
		start := d*minutesPerDay + startMin
		end := d*minutesPerDay + endMin
		if endMin < startMin {
			end += minutesPerDay
		}
		windows = append(windows, weeklyWindow{start: start, end: end % minutesPerWeek})
	}
	if len(windows) == 0 {
		return nil, false
	}
	return windows, true
}

type multiWindow []weeklyWindow

func (m multiWindow) contains(minute int) bool {
	for _, w := range m {
		if w.contains(minute) {
			return true
		}
	}
	return false
}

func isWeekday(d int) bool { return d >= 0 && d <= 6 }

// parseMinutes converts "HH:MM" to minutes since midnight. It reports false for anything
// it cannot parse, so a malformed schedule is skipped rather than silently treated as
// midnight.
func parseMinutes(hhmm string) (int, bool) {
	h, m := 0, 0
	if n, err := fmt.Sscanf(strings.TrimSpace(hhmm), "%d:%d", &h, &m); err != nil || n != 2 {
		return 0, false
	}
	if h < 0 || h > 23 || m < 0 || m > 59 {
		return 0, false
	}
	return h*60 + m, true
}

// locationCache memoises time.LoadLocation. It is called on every reconcile of every
// group, and LoadLocation re-reads the embedded tzdata on each call.
var locationCache sync.Map // string -> *time.Location

// loadLocation resolves a schedule timezone, falling back to the operator's local time.
// The IANA database is embedded via the time/tzdata import in cmd/main.go, so a failure
// here means a genuinely unknown timezone name and is logged rather than swallowed.
func loadLocation(name string) *time.Location {
	if name == "" {
		return time.Local
	}
	if cached, ok := locationCache.Load(name); ok {
		return cached.(*time.Location)
	}

	loc, err := time.LoadLocation(name)
	if err != nil {
		log.Log.Error(err, "Could not load schedule timezone, falling back to operator local time", "timezone", name)
		loc = time.Local
	}
	locationCache.Store(name, loc)
	return loc
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

func getSequenceIndex(obj client.Object, sequence []string) int {
	name := obj.GetName()
	for i, s := range sequence {
		if s == "*" {
			return i
		}
		if before, ok := strings.CutSuffix(s, "*"); ok {
			if strings.HasPrefix(name, before) {
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

	for _, p := range pods.Items {
		if p.Status.Phase != corev1.PodSucceeded {
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
