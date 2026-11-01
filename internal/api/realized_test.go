package api

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	policyv1 "k8s.io/api/policy/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	finopsv1 "github.com/migalsp/costdeck-operator/api/v1"
	"github.com/migalsp/costdeck-operator/internal/finops"
)

// realizedServer has one node left of two a scale-down recorded, with a pod on it that
// refuses eviction behind a budget, and schedules in each state of the check.
func realizedServer(t *testing.T) *Server {
	t.Helper()
	t.Setenv("POD_NAMESPACE", "costdeck")
	server := buildMockServerWithK8s()
	ctx := context.Background()
	node := &corev1.Node{Name: "node-a", Status: corev1.NodeStatus{
		Allocatable: corev1.ResourceList{corev1.ResourceCPU: resource.MustParse("4"), corev1.ResourceMemory: resource.MustParse("16Gi")}}}
	debug := &corev1.Pod{Namespace: "ops", Name: "debug", Labels: map[string]string{"app": "debug"},
		Annotations:     map[string]string{"cluster-autoscaler.kubernetes.io/safe-to-evict": "false"},
		OwnerReferences: []metav1.OwnerReference{{Kind: "ReplicaSet", Name: "debug-1", Controller: new(true)}},
		Spec:            corev1.PodSpec{NodeName: "node-a"}, Status: corev1.PodStatus{Phase: corev1.PodRunning}}
	pdb := &policyv1.PodDisruptionBudget{Namespace: "ops", Name: "debug",
		Spec: policyv1.PodDisruptionBudgetSpec{Selector: &metav1.LabelSelector{MatchLabels: map[string]string{"app": "debug"}}}}
	if err := server.Client.Create(ctx, node); err != nil {
		t.Fatal(err)
	}
	if err := server.Client.Create(ctx, debug); err != nil {
		t.Fatal(err)
	}
	if err := server.Client.Create(ctx, pdb); err != nil {
		t.Fatal(err)
	}
	status := func(desired string, since time.Duration) finopsv1.ScheduleStatus {
		at := metav1.NewTime(time.Now().Add(-since))
		return finopsv1.ScheduleStatus{DesiredState: desired, EstimatedHourlySavings: "1.0000", Currency: "EUR", ScaleDownAt: &at,
			ScaleDownNodes: []finopsv1.ScaleDownNode{{Name: "gone-node", HourlyCost: "0.3000"}, {Name: "node-a", HourlyCost: "0.2000"}}}
	}
	for name, st := range map[string]finopsv1.ScheduleStatus{"settled": status("Down", 20*time.Minute), "fresh": status("Down", 2*time.Minute), "up": status("Up", time.Hour)} {
		if err := server.Client.Create(ctx, &finopsv1.ScalingGroup{Name: name, Namespace: "costdeck",
			Spec: finopsv1.ScalingGroupSpec{Namespaces: []string{"shop"}}, Status: finopsv1.ScalingGroupStatus{ScheduleStatus: st}}); err != nil {
			t.Fatal(err)
		}
	}
	if err := server.Client.Create(ctx, &finopsv1.ScalingConfig{Name: "billing", Namespace: "costdeck",
		Spec: finopsv1.ScalingConfigSpec{TargetNamespace: "billing"}, Status: finopsv1.ScalingConfigStatus{ScheduleStatus: status("Down", 20*time.Minute)}}); err != nil {
		t.Fatal(err)
	}
	return server
}

func getRealized(t *testing.T, server *Server, path string) realizedResponse {
	t.Helper()
	rr := send(t, server, "GET", path, "")
	var got realizedResponse
	if rr.Code != http.StatusOK || json.Unmarshal(rr.Body.Bytes(), &got) != nil {
		t.Fatalf("GET %s: %d %s", path, rr.Code, rr.Body.String())
	}
	return got
}

func TestRealizedSavings(t *testing.T) {
	server := realizedServer(t)
	got := getRealized(t, server, "/api/scaling/groups/settled/realized")
	if got.State != realizedChecked || got.Gone != 1 || got.RealizedHourly != 0.3 || got.EstimatedHourly != 1 || got.Currency != "EUR" || got.Blocked != 1 || got.SettleMinutes != 15 {
		t.Errorf("settled = %+v", got)
	}
	if len(got.Nodes) != 2 || !got.Nodes[0].Gone || got.Nodes[1].Gone {
		t.Fatalf("nodes = %+v", got.Nodes)
	}
	// The pod both refuses eviction and sits behind a budget with no disruption left.
	reasons := make([]string, 0, len(got.Nodes[1].Blockers))
	for _, b := range got.Nodes[1].Blockers {
		reasons = append(reasons, b.Reason+" "+b.Pod)
	}
	if strings.Join(reasons, ",") != finops.BlockerSafeToEvict+" ops/debug,"+finops.BlockerPDB+" ops/debug" {
		t.Errorf("blockers = %v", reasons)
	}
}

func TestRealizedSavingsStatesAndTool(t *testing.T) {
	server := realizedServer(t)
	get := func(path string) realizedResponse { return getRealized(t, server, path) }
	if got := get("/api/scaling/groups/fresh/realized"); got.State != realizedSettling || got.Gone != 1 {
		t.Errorf("fresh = %+v, want settling", got)
	}
	if got := get("/api/scaling/groups/up/realized"); got.State != realizedNone || len(got.Nodes) != 0 || got.ScaledDownAt != nil {
		t.Errorf("up = %+v, want nothing to check", got)
	}
	if got := get("/api/scaling/configs/billing/realized"); got.State != realizedChecked || got.Gone != 1 {
		t.Errorf("config = %+v", got)
	}
	if rr := send(t, server, "GET", "/api/scaling/groups/missing/realized", ""); rr.Code != http.StatusNotFound {
		t.Errorf("missing group: %d", rr.Code)
	}

	out, err := server.toolRealized(context.Background(), "namespace", "billing")
	if err != nil || !strings.Contains(out, `"realizedHourly":0.3`) || !strings.Contains(out, `"state":"checked"`) {
		t.Errorf("tool = %s (%v)", out, err)
	}
	if _, err := server.toolRealized(context.Background(), "cluster", "x"); err == nil {
		t.Error("an unknown target was accepted")
	}
}
