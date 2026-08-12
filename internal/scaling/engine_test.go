package scaling

import (
	"context"
	"testing"
	"time"

	"k8s.io/apimachinery/pkg/runtime"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	finopsv1 "github.com/migalsp/costdeck-operator/api/v1"
	appsv1 "k8s.io/api/apps/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func TestParseMinutes(t *testing.T) {
	tests := []struct {
		input    string
		expected int
		valid    bool
	}{
		{"00:00", 0, true},
		{"01:30", 90, true},
		{"09:05", 545, true},
		{"12:00", 720, true},
		{"23:59", 1439, true},
		{" 08:00 ", 480, true},
		{"", 0, false},
		{"24:00", 0, false},
		{"12:60", 0, false},
		{"noon", 0, false},
		{"12", 0, false},
	}

	for _, tt := range tests {
		actual, ok := parseMinutes(tt.input)
		if ok != tt.valid {
			t.Errorf("parseMinutes(%q) valid = %v; want %v", tt.input, ok, tt.valid)
			continue
		}
		if ok && actual != tt.expected {
			t.Errorf("parseMinutes(%q) = %d; want %d", tt.input, actual, tt.expected)
		}
	}
}

func TestIsExcluded(t *testing.T) {
	tests := []struct {
		name       string
		exclusions []string
		expected   bool
	}{
		{"frontend", []string{"backend", "redis"}, false},
		{"frontend", []string{"frontend"}, true},
		{"frontend", []string{"front*"}, true},
		{"api-server", []string{"*"}, true},
		{"db-postgres", []string{"db-*"}, true},
		{"db-postgres", []string{"db"}, false},
		{"  spaced  ", []string{"spaced"}, true},
		{"empty-rule", []string{""}, false},
	}

	for _, tt := range tests {
		actual := isExcluded(tt.name, tt.exclusions)
		if actual != tt.expected {
			t.Errorf("isExcluded(%q, %v) = %v; want %v", tt.name, tt.exclusions, actual, tt.expected)
		}
	}
}

func TestGetSequenceIndex(t *testing.T) {
	sequence := []string{"db-*", "backend", "*", "frontend"}

	tests := []struct {
		name     string
		expected int
	}{
		{"db-postgres", 0},
		{"backend", 1},
		{"anything-else", 2},
		{"frontend-app", 2},    // Matches "*" before "frontend" since "*" is at index 2
		{"unknown-no-star", 2}, // Matches "*" at index 2
	}

	for _, tt := range tests {
		obj := &appsv1.Deployment{
			ObjectMeta: metav1.ObjectMeta{Name: tt.name},
		}
		actual := getSequenceIndex(obj, sequence)
		if actual != tt.expected {
			t.Errorf("getSequenceIndex(%q) = %d; want %d", tt.name, actual, tt.expected)
		}
	}

	// Test missing string
	obj2 := &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{Name: "not-in-list"},
	}
	actual := getSequenceIndex(obj2, []string{"only-one"})
	if actual != 999 {
		t.Errorf("getSequenceIndex(not-in-list) = %d; want 999", actual)
	}
}

func TestIsActive(t *testing.T) {
	engine := &Engine{}

	truthy := true
	falsy := false

	tests := []struct {
		name         string
		schedules    []finopsv1.ScalingSchedule
		manualActive *bool
		expected     bool
	}{
		{
			name:         "manual override true",
			schedules:    []finopsv1.ScalingSchedule{{Days: []int{0, 1, 2, 3, 4, 5, 6}, StartTime: "00:00", EndTime: "00:01"}},
			manualActive: &truthy,
			expected:     true,
		},
		{
			name:         "manual override false ignores schedule",
			schedules:    []finopsv1.ScalingSchedule{{Days: []int{0, 1, 2, 3, 4, 5, 6}, StartTime: "00:00", EndTime: "23:59"}},
			manualActive: &falsy,
			expected:     false,
		},
		{
			name:         "no schedules, no override",
			schedules:    nil,
			manualActive: nil,
			expected:     true, // defaults to active
		},
		{
			name:         "empty schedules list, no override",
			schedules:    []finopsv1.ScalingSchedule{},
			manualActive: nil,
			expected:     true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			actual := engine.IsActive(tt.schedules, tt.manualActive)
			if actual != tt.expected {
				t.Errorf("IsActive() = %v; want %v", actual, tt.expected)
			}
		})
	}
}

// at builds a concrete instant in UTC. Weekday is asserted so the fixtures stay honest.
func at(t *testing.T, day time.Weekday, hhmm string) time.Time {
	t.Helper()
	h, m, ok := 0, 0, false
	if v, valid := parseMinutes(hhmm); valid {
		h, m, ok = v/60, v%60, true
	}
	if !ok {
		t.Fatalf("bad fixture time %q", hhmm)
	}
	// 2026-08-02 is a Sunday, so adding the weekday index lands on the wanted day.
	base := time.Date(2026, 8, 2, h, m, 0, 0, time.UTC).AddDate(0, 0, int(day))
	if base.Weekday() != day {
		t.Fatalf("fixture landed on %s, wanted %s", base.Weekday(), day)
	}
	return base
}

func intPtr(i int) *int { return new(i) }

func TestIsActiveAtWeeklyWindow(t *testing.T) {
	engine := &Engine{}

	// "Non-stop from the start of Monday to the end of Friday" as a single window.
	monToFri := []finopsv1.ScalingSchedule{{
		StartDay:  intPtr(int(time.Monday)),
		StartTime: "00:00",
		EndDay:    intPtr(int(time.Friday)),
		EndTime:   "23:59",
		Timezone:  "UTC",
	}}

	// A window that wraps through Sunday midnight: shut down over the weekend only.
	friToMon := []finopsv1.ScalingSchedule{{
		StartDay:  intPtr(int(time.Friday)),
		StartTime: "20:00",
		EndDay:    intPtr(int(time.Monday)),
		EndTime:   "08:00",
		Timezone:  "UTC",
	}}

	tests := []struct {
		name      string
		schedules []finopsv1.ScalingSchedule
		day       time.Weekday
		clock     string
		expected  bool
	}{
		{"mon-fri: monday open", monToFri, time.Monday, "00:00", true},
		{"mon-fri: tuesday midnight stays up", monToFri, time.Tuesday, "00:00", true},
		{"mon-fri: wednesday last minute of day stays up", monToFri, time.Wednesday, "23:59", true},
		{"mon-fri: thursday midday", monToFri, time.Thursday, "12:00", true},
		{"mon-fri: friday close", monToFri, time.Friday, "23:59", true},
		{"mon-fri: saturday down", monToFri, time.Saturday, "00:00", false},
		{"mon-fri: sunday down", monToFri, time.Sunday, "12:00", false},
		{"mon-fri: sunday just before open", monToFri, time.Sunday, "23:59", false},

		{"wrapping: friday before open", friToMon, time.Friday, "19:59", false},
		{"wrapping: friday after open", friToMon, time.Friday, "20:00", true},
		{"wrapping: saturday inside", friToMon, time.Saturday, "03:00", true},
		{"wrapping: sunday inside", friToMon, time.Sunday, "12:00", true},
		{"wrapping: monday before close", friToMon, time.Monday, "08:00", true},
		{"wrapping: monday after close", friToMon, time.Monday, "08:01", false},
		{"wrapping: wednesday outside", friToMon, time.Wednesday, "12:00", false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			actual := engine.IsActiveAt(at(t, tt.day, tt.clock), tt.schedules, nil)
			if actual != tt.expected {
				t.Errorf("IsActiveAt(%s %s) = %v; want %v", tt.day, tt.clock, actual, tt.expected)
			}
		})
	}
}

func TestIsActiveAtOvernightWindow(t *testing.T) {
	engine := &Engine{}

	// Nightly batch window opening Monday-Friday at 22:00 and closing at 06:00 the
	// morning after. Before week-minute evaluation this could never match at all.
	overnight := []finopsv1.ScalingSchedule{{
		Days:      []int{1, 2, 3, 4, 5},
		StartTime: "22:00",
		EndTime:   "06:00",
		Timezone:  "UTC",
	}}

	tests := []struct {
		name     string
		day      time.Weekday
		clock    string
		expected bool
	}{
		{"monday before open", time.Monday, "21:59", false},
		{"monday after open", time.Monday, "22:00", true},
		{"tuesday past midnight still open", time.Tuesday, "02:00", true},
		{"tuesday at close", time.Tuesday, "06:00", true},
		{"tuesday after close", time.Tuesday, "06:01", false},
		{"saturday morning spillover from friday", time.Saturday, "05:00", true},
		{"saturday evening stays down", time.Saturday, "22:00", false},
		{"sunday stays down", time.Sunday, "02:00", false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			actual := engine.IsActiveAt(at(t, tt.day, tt.clock), overnight, nil)
			if actual != tt.expected {
				t.Errorf("IsActiveAt(%s %s) = %v; want %v", tt.day, tt.clock, actual, tt.expected)
			}
		})
	}
}

func TestIsActiveAtDailyWindowUnchanged(t *testing.T) {
	engine := &Engine{}

	// Regression guard: the classic business-hours schedule must behave exactly as before.
	business := []finopsv1.ScalingSchedule{{
		Days:      []int{1, 2, 3, 4, 5},
		StartTime: "09:00",
		EndTime:   "18:00",
		Timezone:  "UTC",
	}}

	tests := []struct {
		day      time.Weekday
		clock    string
		expected bool
	}{
		{time.Monday, "08:59", false},
		{time.Monday, "09:00", true},
		{time.Monday, "18:00", true},
		{time.Monday, "18:01", false},
		{time.Friday, "12:00", true},
		{time.Saturday, "12:00", false},
		{time.Sunday, "12:00", false},
	}

	for _, tt := range tests {
		t.Run(tt.day.String()+" "+tt.clock, func(t *testing.T) {
			actual := engine.IsActiveAt(at(t, tt.day, tt.clock), business, nil)
			if actual != tt.expected {
				t.Errorf("IsActiveAt(%s %s) = %v; want %v", tt.day, tt.clock, actual, tt.expected)
			}
		})
	}
}

func TestIsActiveAtMultipleWindows(t *testing.T) {
	engine := &Engine{}

	// Two windows in a day: the CRD has always allowed a list, now the evaluation is
	// exercised for it.
	split := []finopsv1.ScalingSchedule{
		{Days: []int{1}, StartTime: "08:00", EndTime: "12:00", Timezone: "UTC"},
		{Days: []int{1}, StartTime: "14:00", EndTime: "18:00", Timezone: "UTC"},
	}

	cases := map[string]bool{"07:00": false, "09:00": true, "13:00": false, "15:00": true, "19:00": false}
	for clock, want := range cases {
		t.Run(clock, func(t *testing.T) {
			if got := engine.IsActiveAt(at(t, time.Monday, clock), split, nil); got != want {
				t.Errorf("IsActiveAt(Monday %s) = %v; want %v", clock, got, want)
			}
		})
	}
}

func TestIsActiveAtTimezone(t *testing.T) {
	engine := &Engine{}

	// 09:00-18:00 Moscow time is 06:00-15:00 UTC. Verifies the schedule is evaluated in
	// its own zone and that the embedded tzdata resolves a non-UTC location.
	moscow := []finopsv1.ScalingSchedule{{
		Days:      []int{1, 2, 3, 4, 5},
		StartTime: "09:00",
		EndTime:   "18:00",
		Timezone:  "Europe/Moscow",
	}}

	cases := map[string]bool{"05:00": false, "07:00": true, "14:00": true, "16:00": false}
	for clock, want := range cases {
		t.Run("utc "+clock, func(t *testing.T) {
			if got := engine.IsActiveAt(at(t, time.Monday, clock), moscow, nil); got != want {
				t.Errorf("IsActiveAt(Monday %s UTC) = %v; want %v", clock, got, want)
			}
		})
	}
}

func TestIsActiveAtMalformedScheduleStaysUp(t *testing.T) {
	engine := &Engine{}

	// A schedule that cannot be parsed must never be read as "scale to zero".
	broken := []finopsv1.ScalingSchedule{{Days: []int{1}, StartTime: "not-a-time", EndTime: "18:00"}}
	if !engine.IsActiveAt(at(t, time.Monday, "23:00"), broken, nil) {
		t.Error("IsActiveAt() with an unparseable schedule = false; want true (fail-safe)")
	}

	noDays := []finopsv1.ScalingSchedule{{StartTime: "09:00", EndTime: "18:00"}}
	if !engine.IsActiveAt(at(t, time.Monday, "23:00"), noDays, nil) {
		t.Error("IsActiveAt() with no days and no weekly window = false; want true (fail-safe)")
	}
}

func TestEffectiveOverride(t *testing.T) {
	truthy := true
	now := time.Date(2026, 8, 11, 12, 0, 0, 0, time.UTC)
	past := metav1.NewTime(now.Add(-time.Hour))
	future := metav1.NewTime(now.Add(time.Hour))

	if got := EffectiveOverride(nil, &future, now); got != nil {
		t.Errorf("EffectiveOverride(nil, ...) = %v; want nil", *got)
	}
	if got := EffectiveOverride(&truthy, nil, now); got == nil || !*got {
		t.Error("EffectiveOverride() with no deadline should keep the override")
	}
	if got := EffectiveOverride(&truthy, &future, now); got == nil || !*got {
		t.Error("EffectiveOverride() before the deadline should keep the override")
	}
	if got := EffectiveOverride(&truthy, &past, now); got != nil {
		t.Error("EffectiveOverride() after the deadline should hand control back to the schedule")
	}

	zero := metav1.Time{}
	if got := EffectiveOverride(&truthy, &zero, now); got == nil || !*got {
		t.Error("EffectiveOverride() with a zero deadline should keep the override")
	}
}

func TestResolveDesiredStateExpiredOverrideFollowsSchedule(t *testing.T) {
	engine := &Engine{}
	falsy := false
	expired := metav1.NewTime(time.Now().Add(-time.Minute))

	// Override says "stay down", but it has lapsed and there is no schedule, so the
	// fail-safe default applies again.
	if !engine.ResolveDesiredState(nil, &falsy, &expired) {
		t.Error("ResolveDesiredState() with an expired override = false; want true")
	}

	live := metav1.NewTime(time.Now().Add(time.Hour))
	if engine.ResolveDesiredState(nil, &falsy, &live) {
		t.Error("ResolveDesiredState() with a live override = true; want false")
	}
}

func buildMockEngine() *Engine {
	scheme := runtime.NewScheme()
	clientgoscheme.AddToScheme(scheme)
	finopsv1.AddToScheme(scheme)
	client := fake.NewClientBuilder().WithScheme(scheme).Build()
	return &Engine{Client: client}
}

func TestComputePhase(t *testing.T) {
	e := buildMockEngine()
	ctx := context.Background()

	// Empty namespace -> ScaledUp if active=true, ScaledDown if active=false
	if p := e.ComputePhase(ctx, "test-ns", true); p != "ScaledUp" {
		t.Errorf("Expected ScaledUp for empty ns, got %v", p)
	}

	zero := int32(0)
	one := int32(1)

	// Add a Deployment with replicas=0
	d1 := &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{Name: "d1", Namespace: "test-ns"},
		Spec:       appsv1.DeploymentSpec{Replicas: &zero},
	}
	e.Client.Create(ctx, d1)

	if p := e.ComputePhase(ctx, "test-ns", false); p != "ScaledDown" {
		t.Errorf("Expected ScaledDown, got %v", p)
	}

	// Add a StatefulSet with replicas=1, ready=1
	s1 := &appsv1.StatefulSet{
		ObjectMeta: metav1.ObjectMeta{Name: "s1", Namespace: "test-ns"},
		Spec:       appsv1.StatefulSetSpec{Replicas: &one},
		Status:     appsv1.StatefulSetStatus{ReadyReplicas: 1},
	}
	e.Client.Create(ctx, s1)

	// Mixed state
	if p := e.ComputePhase(ctx, "test-ns", false); p != "ScalingDown" && p != "PartlyScaled" {
		t.Errorf("Expected ScalingDown or PartlyScaled, got %v", p)
	}
}

func TestScaleTarget(t *testing.T) {
	e := buildMockEngine()
	ctx := context.Background()

	one := int32(1)
	d1 := &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{Name: "app1", Namespace: "test-ns"},
		Spec:       appsv1.DeploymentSpec{Replicas: &one},
		Status:     appsv1.DeploymentStatus{ReadyReplicas: 1},
	}
	e.Client.Create(ctx, d1)

	orig := make(map[string]int32)

	// Scale Down
	newOrig, _, err := e.ScaleTarget(ctx, "test-ns", false, nil, nil, orig, false)
	if err != nil {
		t.Fatal(err)
	}

	// Verify original replicas saved
	if newOrig["*v1.Deployment/app1"] != 1 {
		t.Errorf("Expected original replicas to be saved")
	}

	// Verify target scaled to 0
	scaledD := &appsv1.Deployment{}
	e.Client.Get(ctx, client.ObjectKey{Name: "app1", Namespace: "test-ns"}, scaledD)
	if *scaledD.Spec.Replicas != 0 {
		t.Errorf("Expected replicas to be 0, got %d", *scaledD.Spec.Replicas)
	}
}

func TestIsGroupReady(t *testing.T) {
	e := buildMockEngine()
	ctx := context.Background()

	one := int32(1)
	d1 := &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{Name: "app1", Namespace: "test-ns"},
		Spec:       appsv1.DeploymentSpec{Replicas: &one},
		Status:     appsv1.DeploymentStatus{ReadyReplicas: 0}, // Not ready yet
	}
	e.Client.Create(ctx, d1)

	objs := []client.Object{d1}

	// Target active = true, but readyReplicas = 0 < targetReplicas(1) -> False
	if ready := e.isGroupReady(ctx, objs, true); ready {
		t.Errorf("Expected group to NOT be ready")
	}

	// Update to ready
	d1.Status.ReadyReplicas = 1
	e.Client.Status().Update(ctx, d1)
	if ready := e.isGroupReady(ctx, objs, true); !ready {
		t.Errorf("Expected group to be ready")
	}
}
