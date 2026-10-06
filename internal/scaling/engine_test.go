package scaling

import (
	"context"
	"testing"
	"time"

	"k8s.io/apimachinery/pkg/runtime"
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	finopsv1 "github.com/migalsp/costdeck-operator/api/v1"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
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
	utilruntime.Must(clientgoscheme.AddToScheme(scheme))
	utilruntime.Must(finopsv1.AddToScheme(scheme))
	return &Engine{Client: fake.NewClientBuilder().WithScheme(scheme).Build()}
}

func must(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
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
	must(t, e.Client.Create(ctx, d1))

	if p := e.ComputePhase(ctx, "test-ns", false); p != "ScaledDown" {
		t.Errorf("Expected ScaledDown, got %v", p)
	}

	// Add a StatefulSet with replicas=1, ready=1
	s1 := &appsv1.StatefulSet{
		ObjectMeta: metav1.ObjectMeta{Name: "s1", Namespace: "test-ns"},
		Spec:       appsv1.StatefulSetSpec{Replicas: &one},
		Status:     appsv1.StatefulSetStatus{ReadyReplicas: 1},
	}
	must(t, e.Client.Create(ctx, s1))

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
	must(t, e.Client.Create(ctx, d1))

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
	must(t, e.Client.Get(ctx, client.ObjectKey{Name: "app1", Namespace: "test-ns"}, scaledD))
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
	must(t, e.Client.Create(ctx, d1))

	objs := []client.Object{d1}

	// Target active = true, but readyReplicas = 0 < targetReplicas(1) -> False
	if ready := e.isGroupReady(ctx, objs, true); ready {
		t.Errorf("Expected group to NOT be ready")
	}

	// Update to ready
	d1.Status.ReadyReplicas = 1
	must(t, e.Client.Status().Update(ctx, d1))
	if ready := e.isGroupReady(ctx, objs, true); !ready {
		t.Errorf("Expected group to be ready")
	}
}

// A workload name must match a whole sequence pattern. Substring matching used to put
// "api" into the stage of "apps/v1:Deployment/api-gateway", and "d" into "db redis".
func TestGetSequenceIndexMatchesWholePatterns(t *testing.T) {
	sequence := []string{
		"apps/v1:Deployment/api-gateway",
		"db redis",
		"StatefulSet/cache-*",
		"worker-?",
	}
	deploy := func(name string) client.Object {
		return &appsv1.Deployment{ObjectMeta: metav1.ObjectMeta{Name: name}}
	}
	sts := func(name string) client.Object {
		return &appsv1.StatefulSet{ObjectMeta: metav1.ObjectMeta{Name: name}}
	}

	tests := []struct {
		obj  client.Object
		want int
	}{
		{deploy("api-gateway"), 0},
		{deploy("api"), unsequenced},
		{sts("api-gateway"), unsequenced}, // Kind-qualified pattern must not match a StatefulSet.
		{deploy("db"), 1},
		{deploy("redis"), 1},
		{deploy("d"), unsequenced},
		{sts("cache-0"), 2},
		{deploy("cache-0"), unsequenced},
		{deploy("worker-1"), 3},
		{deploy("worker-10"), unsequenced},
	}
	for _, tt := range tests {
		if got := getSequenceIndex(tt.obj, sequence); got != tt.want {
			t.Errorf("getSequenceIndex(%T %s) = %d, want %d", tt.obj, tt.obj.GetName(), got, tt.want)
		}
	}
}

func TestTargetReplicas(t *testing.T) {
	tests := []struct {
		name     string
		active   bool
		current  int32
		original int32
		want     int32
	}{
		{"scale down always means zero", false, 3, 3, 0},
		{"restore the recorded count", true, 0, 3, 3},
		{"restore over a partial manual scale-up", true, 1, 3, 3},
		{"keep a larger manual scale-up", true, 5, 3, 5},
		{"no record keeps running replicas", true, 2, 0, 2},
		{"no record starts one replica", true, 0, 0, 1},
	}
	for _, tt := range tests {
		if got := targetReplicas(tt.active, tt.current, tt.original); got != tt.want {
			t.Errorf("%s: targetReplicas() = %d, want %d", tt.name, got, tt.want)
		}
	}
}

// The original replica count must survive losing status.originalReplicas, for example when
// the status update after a scale-down hits a conflict.
func TestOriginalReplicasSurviveLostStatus(t *testing.T) {
	e := buildMockEngine()
	replicas := int32(5)
	d := &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{Name: "api", Namespace: "shop"},
		Spec:       appsv1.DeploymentSpec{Replicas: &replicas},
	}
	ctx := context.Background()
	if err := e.Client.Create(ctx, d); err != nil {
		t.Fatal(err)
	}
	get := func() *appsv1.Deployment {
		var out appsv1.Deployment
		if err := e.Client.Get(ctx, client.ObjectKeyFromObject(d), &out); err != nil {
			t.Fatal(err)
		}
		return &out
	}

	if err := e.scaleResource(ctx, get(), false, map[string]int32{}); err != nil {
		t.Fatal(err)
	}
	down := get()
	if *down.Spec.Replicas != 0 || down.Annotations[OriginalReplicasAnnotation] != "5" {
		t.Fatalf("after scale-down: replicas %d, annotation %q", *down.Spec.Replicas, down.Annotations[OriginalReplicasAnnotation])
	}

	// A fresh, empty map stands in for the status update that never landed.
	if err := e.scaleResource(ctx, down, true, map[string]int32{}); err != nil {
		t.Fatal(err)
	}
	up := get()
	if *up.Spec.Replicas != 5 {
		t.Errorf("after scale-up: %d replicas, want the original 5", *up.Spec.Replicas)
	}
	if _, ok := up.Annotations[OriginalReplicasAnnotation]; ok {
		t.Error("the annotation must be removed once the workload is restored")
	}
}

func TestGetReplicasDefaultsToOne(t *testing.T) {
	if got := getReplicas(&appsv1.Deployment{}); got != 1 {
		t.Errorf("getReplicas(nil replicas) = %d, want the API default of 1", got)
	}
}

// An evicted (Failed) pod lingers until garbage collection; it must not keep a workload
// in ScalingDown forever.
func TestScaleDownIgnoresEvictedPods(t *testing.T) {
	e := buildMockEngine()
	ctx := context.Background()

	zero := int32(0)
	selector := &metav1.LabelSelector{MatchLabels: map[string]string{"app": "api"}}
	d := &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{Name: "api", Namespace: "test-ns"},
		Spec:       appsv1.DeploymentSpec{Replicas: &zero, Selector: selector},
	}
	evicted := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Name: "api-x", Namespace: "test-ns", Labels: map[string]string{"app": "api"}},
		Status:     corev1.PodStatus{Phase: corev1.PodFailed, Reason: "Evicted"},
	}
	for _, obj := range []client.Object{d, evicted} {
		if err := e.Client.Create(ctx, obj); err != nil {
			t.Fatal(err)
		}
	}

	if !e.isResourceReady(ctx, d, false) {
		t.Fatal("a scaled-down Deployment with only an evicted pod left must count as scaled down")
	}
	if phase := e.ComputePhase(ctx, "test-ns", false); phase != PhaseScaledDown {
		t.Errorf("ComputePhase() = %s, want %s", phase, PhaseScaledDown)
	}

	running := evicted.DeepCopy()
	running.Name, running.ResourceVersion = "api-y", ""
	running.Status.Phase = corev1.PodRunning
	if err := e.Client.Create(ctx, running); err != nil {
		t.Fatal(err)
	}
	if e.isResourceReady(ctx, d, false) {
		t.Error("a running pod must keep the Deployment in ScalingDown")
	}
}

type staticResolver struct{ p ExternalProvider }

func (r staticResolver) Resolve(context.Context, string) (ExternalProvider, error) { return r.p, nil }

func TestEngineProviderFallsBackToResolver(t *testing.T) {
	registered := &AWSProvider{}
	resolved := &AWSProvider{}
	e := &Engine{Providers: map[string]ExternalProvider{"aws": registered}, Resolver: staticResolver{resolved}}

	if p, _ := e.Provider(context.Background(), "aws"); p != registered {
		t.Error("a registered provider must win over the resolver")
	}
	e.Providers = nil
	if p, _ := e.Provider(context.Background(), "aws"); p != resolved {
		t.Error("an unregistered provider must come from the resolver")
	}
	e.Resolver = nil
	if _, err := e.Provider(context.Background(), "aws"); err == nil {
		t.Error("expected an error with neither a registration nor a resolver")
	}
}

func TestKeptDownCountsMissingReplicas(t *testing.T) {
	e := buildMockEngine()
	ctx := context.Background()
	zero, one := int32(0), int32(1)
	podSpec := corev1.PodSpec{
		InitContainers: []corev1.Container{{Name: "migrate", Resources: corev1.ResourceRequirements{Requests: corev1.ResourceList{
			corev1.ResourceMemory: resource.MustParse("2Gi"),
		}}}},
		Containers: []corev1.Container{{Name: "app", Resources: corev1.ResourceRequirements{Requests: corev1.ResourceList{
			corev1.ResourceCPU: resource.MustParse("500m"), corev1.ResourceMemory: resource.MustParse("1Gi"),
		}}}},
	}
	must(t, e.Client.Create(ctx, &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{Name: "api", Namespace: "test-ns"},
		Spec:       appsv1.DeploymentSpec{Replicas: &zero, Template: corev1.PodTemplateSpec{Spec: podSpec}},
	}))
	must(t, e.Client.Create(ctx, &appsv1.StatefulSet{
		ObjectMeta: metav1.ObjectMeta{Name: "db", Namespace: "test-ns"},
		Spec:       appsv1.StatefulSetSpec{Replicas: &one, Template: corev1.PodTemplateSpec{Spec: podSpec}},
	}))

	cpu, mem, err := e.KeptDown(ctx, "test-ns", map[string]int32{"*v1.Deployment/api": 3, "*v1.StatefulSet/db": 1})
	if err != nil {
		t.Fatal(err)
	}
	// api: 3 missing replicas x (500m, max(1Gi, 2Gi init)); db is at its original count.
	if cpu.MilliValue() != 1500 || mem.Value() != 6<<30 {
		t.Errorf("KeptDown() = %s CPU, %s memory; want 1500m and 6Gi", cpu.String(), mem.String())
	}
}
