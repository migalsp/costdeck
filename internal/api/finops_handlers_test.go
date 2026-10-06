package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	finopsv1 "github.com/migalsp/costdeck-operator/api/v1"
	"github.com/migalsp/costdeck-operator/internal/finops"
)

func TestFinOpsOverview(t *testing.T) {
	t.Setenv("POD_NAMESPACE", "costdeck")
	server := buildMockServerWithK8s()
	ctx := context.Background()
	node := &corev1.Node{ObjectMeta: metav1.ObjectMeta{Name: "n1"}}
	node.Status.Capacity = corev1.ResourceList{corev1.ResourceCPU: resource.MustParse("4"), corev1.ResourceMemory: resource.MustParse("16Gi")}
	for _, o := range []client.Object{
		node,
		&corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "dev-api"}},
		&finopsv1.NamespaceFinOps{
			ObjectMeta: metav1.ObjectMeta{Name: "dev-api", Namespace: "costdeck"},
			Spec:       finopsv1.NamespaceFinOpsSpec{TargetNamespace: "dev-api"},
			Status: finopsv1.NamespaceFinOpsStatus{History: []finopsv1.MetricDataPoint{{
				CPU:    finopsv1.ResourceMetrics{Requests: "1", Usage: "100m"},
				Memory: finopsv1.ResourceMetrics{Requests: "2Gi", Usage: "1Gi"},
			}}},
		},
	} {
		if err := server.Client.Create(ctx, o); err != nil {
			t.Fatal(err)
		}
	}

	rr := httptest.NewRecorder()
	server.routes().ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/api/finops/overview", nil))
	if rr.Code != http.StatusOK {
		t.Fatalf("status %d: %s", rr.Code, rr.Body.String())
	}
	var o finops.Overview
	if err := json.Unmarshal(rr.Body.Bytes(), &o); err != nil {
		t.Fatal(err)
	}
	if len(o.Namespaces) != 1 || o.Namespaces[0].Environment != finops.EnvNonProduction || o.Monthly.Provisioned <= o.Monthly.Requested {
		t.Errorf("unexpected overview: %+v", o)
	}
	// dev-api is unscheduled non-production, so it is a schedule candidate.
	if o.Savings.ScheduleCandidatesMonthly <= 0 || o.Namespaces[0].ScheduleSavingMonthly <= 0 {
		t.Errorf("dev-api should be a schedule candidate: %+v", o.Savings)
	}
	if o.Savings.RightsizingPending != 1 {
		t.Errorf("advice for dev-api is computed in the background, pending %d", o.Savings.RightsizingPending)
	}
}

func TestCostOverviewTool(t *testing.T) {
	t.Setenv("POD_NAMESPACE", "costdeck")
	server := buildMockServerWithK8s()
	node := &corev1.Node{ObjectMeta: metav1.ObjectMeta{Name: "n1"}}
	node.Status.Capacity = corev1.ResourceList{corev1.ResourceCPU: resource.MustParse("2"), corev1.ResourceMemory: resource.MustParse("8Gi")}
	if err := server.Client.Create(context.Background(), node); err != nil {
		t.Fatal(err)
	}
	out, err := server.toolCostOverview(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	var got map[string]any
	if err := json.Unmarshal([]byte(out), &got); err != nil {
		t.Fatalf("not JSON: %s", out)
	}
	for _, key := range []string{"monthlyRunRate", "costEfficiency", "monthToDate", "savings", "topNamespaces", "opportunities"} {
		if _, ok := got[key]; !ok {
			t.Errorf("missing %s in %s", key, out)
		}
	}
}
