package scaling

import (
	"context"
	"testing"

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
	}{
		{"00:00", 0},
		{"01:30", 90},
		{"12:00", 720},
		{"23:59", 1439},
	}

	for _, tt := range tests {
		actual := parseMinutes(tt.input)
		if actual != tt.expected {
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
