package scaling

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"time"

	finopsv1 "github.com/migalsp/costdeck-operator/api/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/log"
)

// Engine evaluates schedules and drives workloads and external targets to a state.
type Engine struct {
	Client client.Client
	// Providers holds statically registered external providers (used by tests).
	Providers map[string]ExternalProvider
	// Resolver builds providers from the live CostDeckConfig when one is not registered.
	Resolver ProviderResolver

	keda kedaProbe
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

	// Before scaling down, stop the things that would bring pods straight back.
	if !active {
		if err := e.holdCompanions(ctx, ns, exclusions); err != nil {
			return originalReplicas, false, err
		}
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

	// Hand CronJobs and autoscalers back only once every workload is up again.
	if active {
		if err := e.releaseCompanions(ctx, ns, exclusions); err != nil {
			return originalReplicas, false, err
		}
	}
	return originalReplicas, true, nil
}
