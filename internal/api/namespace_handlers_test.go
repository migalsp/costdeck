package api

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"testing"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	metricsv1beta1 "k8s.io/metrics/pkg/apis/metrics/v1beta1"
	metricsfake "k8s.io/metrics/pkg/client/clientset/versioned/fake"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"

	finopsv1 "github.com/migalsp/costdeck-operator/api/v1"
	"github.com/migalsp/costdeck-operator/internal/metrics"
)

func TestNamespaceOptimizationInfoMatchesTheDashboardShape(t *testing.T) {
	t.Setenv("POD_NAMESPACE", operatorNamespace)
	server := buildMockServerWithK8s()

	rr := send(t, server, http.MethodGet, "/api/namespaces/shop/optimization", "")
	if got := decodeInto[map[string]any](t, rr); rr.Code != http.StatusOK || len(got) != 1 || got["active"] != false {
		t.Errorf("a namespace never right-sized = %d %v, want only active: false", rr.Code, got)
	}

	optimizedAt := metav1.NewTime(time.Date(2026, 10, 30, 12, 0, 0, 0, time.UTC))
	createAll(t, server.Client, &finopsv1.NamespaceOptimization{Name: "shop", Namespace: operatorNamespace,
		Spec: finopsv1.NamespaceOptimizationSpec{TargetNamespace: "shop"},
		Status: finopsv1.NamespaceOptimizationStatus{Active: true, OptimizedAt: optimizedAt, Workloads: []finopsv1.WorkloadOptimization{{
			Name: "api", Kind: "Deployment", AppliedBy: "Anna",
			Original:  finopsv1.ResourceValues{CPURequest: "2", MemoryRequest: "4Gi"},
			Optimized: finopsv1.ResourceValues{CPURequest: "500m", MemoryRequest: "1Gi"},
		}}}})

	rr = send(t, server, http.MethodGet, "/api/namespaces/shop/optimization", "")
	got := decodeInto[struct {
		Active      bool   `json:"active"`
		OptimizedAt string `json:"optimizedAt"`
		Workloads   []struct {
			Name      string                  `json:"name"`
			Kind      string                  `json:"kind"`
			Original  finopsv1.ResourceValues `json:"original"`
			Optimized finopsv1.ResourceValues `json:"optimized"`
		} `json:"workloads"`
	}](t, rr)
	if rr.Code != http.StatusOK || !got.Active || got.OptimizedAt != "2026-10-30T12:00:00Z" || len(got.Workloads) != 1 {
		t.Fatalf("optimization = %d %+v", rr.Code, got)
	}
	if w := got.Workloads[0]; w.Name != apiWorkload || w.Kind != "Deployment" || w.Original.CPURequest != "2" || w.Optimized.MemoryRequest != "1Gi" {
		t.Errorf("workload = %+v", w)
	}
}

func TestNamespaceOptimizationInfoReportsAReadFailure(t *testing.T) {
	t.Setenv("POD_NAMESPACE", operatorNamespace)
	server := interceptedServer(interceptor.Funcs{Get: func(ctx context.Context, c client.WithWatch, key client.ObjectKey, obj client.Object, opts ...client.GetOption) error {
		if _, ok := obj.(*finopsv1.NamespaceOptimization); ok {
			return errors.New("etcd timed out")
		}
		return c.Get(ctx, key, obj, opts...)
	}})
	rr := send(t, server, http.MethodGet, "/api/namespaces/shop/optimization", "")
	if rr.Code != http.StatusInternalServerError || errorMessage(t, rr) != "etcd timed out" {
		t.Errorf("optimization on a failing API server = %d %s, want 500, not a namespace that looks untouched", rr.Code, rr.Body.String())
	}
}

func TestHistoryFindsANamespaceTrackedUnderAnotherName(t *testing.T) {
	t.Setenv("POD_NAMESPACE", operatorNamespace)
	server := buildMockServerWithK8s()
	tracker := namespaceFinOps("handmade-tracker", "1", "250m", "1Gi", "512Mi")
	tracker.Spec.TargetNamespace = shopNamespace
	createAll(t, server.Client, tracker)

	rr := send(t, server, http.MethodGet, "/api/namespaces/shop/history", "")
	points := decodeInto[[]finopsv1.MetricDataPoint](t, rr)
	if rr.Code != http.StatusOK || len(points) != 2 || points[1].CPU.Usage != "250m" {
		t.Errorf("history = %d %+v, want the hand-made tracker's points", rr.Code, points)
	}
	if rr := send(t, server, http.MethodGet, "/api/namespaces/ghost/history", ""); rr.Code != http.StatusNotFound || errorMessage(t, rr) != "Not found" {
		t.Errorf("an untracked namespace = %d %s, want 404", rr.Code, rr.Body.String())
	}
}

func TestNamespaceRevertWithoutAnOptimizationIsNotFound(t *testing.T) {
	t.Setenv("POD_NAMESPACE", operatorNamespace)
	server := buildMockServerWithK8s()
	rr := send(t, server, http.MethodPost, "/api/namespaces/never-optimized/revert", "")
	if rr.Code != http.StatusNotFound || errorMessage(t, rr) != "Optimization info not found" {
		t.Errorf("revert = %d %s, want 404", rr.Code, rr.Body.String())
	}
}

func TestWorkloadActionScalesOnlyValidRequests(t *testing.T) {
	t.Setenv("POD_NAMESPACE", operatorNamespace)
	server := buildMockServerWithK8s()
	createAll(t, server.Client, &appsv1.Deployment{Name: "api", Namespace: "shop", Spec: appsv1.DeploymentSpec{Replicas: new(int32(3))}})
	replicas := func() int32 {
		var d appsv1.Deployment
		if err := server.Client.Get(context.Background(), client.ObjectKey{Namespace: "shop", Name: "api"}, &d); err != nil {
			t.Fatal(err)
		}
		return *d.Spec.Replicas
	}

	for _, tc := range []struct {
		path, body string
		status     int
	}{
		{"/api/namespaces/shop/workloads/api", `{"kind":"Deployment","replicas":-1}`, http.StatusBadRequest},
		{"/api/namespaces/shop/workloads/api", `{"kind":"CronJob","replicas":1}`, http.StatusBadRequest},
		{"/api/namespaces/shop/workloads/api", `{"kind":`, http.StatusBadRequest},
		{"/api/namespaces/shop/workloads/missing", `{"kind":"Deployment","replicas":1}`, http.StatusNotFound},
	} {
		if rr := send(t, server, http.MethodPut, tc.path, tc.body); rr.Code != tc.status {
			t.Errorf("PUT %s %s = %d %s, want %d", tc.path, tc.body, rr.Code, rr.Body.String(), tc.status)
		}
	}
	if got := replicas(); got != 3 {
		t.Fatalf("a refused request changed the replicas to %d", got)
	}
	if rr := send(t, server, http.MethodPut, "/api/namespaces/shop/workloads/api", `{"kind":"Deployment","replicas":0}`); rr.Code != http.StatusOK || replicas() != 0 {
		t.Errorf("scaling to zero = %d, replicas %d", rr.Code, replicas())
	}
}

func TestServePodsMatchesTheDashboardShape(t *testing.T) {
	t.Setenv("POD_NAMESPACE", operatorNamespace)
	server := buildMockServerWithK8s()
	seedConfig(t, server, finopsv1.CostDeckConfigSpec{Pricing: customPricing()})
	web := podWith(shopNamespace, "web", corev1.PodRunning,
		corev1.Container{Name: "app", Resources: corev1.ResourceRequirements{
			Requests: corev1.ResourceList{corev1.ResourceCPU: resource.MustParse("500m"), corev1.ResourceMemory: resource.MustParse("1Gi")},
			Limits:   corev1.ResourceList{corev1.ResourceCPU: resource.MustParse("1"), corev1.ResourceMemory: resource.MustParse("2Gi")}}},
		corev1.Container{Name: "proxy", Resources: requests("500m", "1Gi")})
	web.Status.ContainerStatuses = []corev1.ContainerStatus{{Name: "app", Ready: true}, {Name: "proxy", Ready: false, RestartCount: 4,
		State: corev1.ContainerState{Waiting: &corev1.ContainerStateWaiting{Reason: "CrashLoopBackOff"}}}}
	createAll(t, server.Client, web,
		podWith(shopNamespace, "migrate", corev1.PodSucceeded, corev1.Container{Name: "job", Resources: requests("2", "2Gi")}),
		podWith("other", "noise", corev1.PodRunning, corev1.Container{Name: "app"}))
	metricsClient := metricsfake.NewSimpleClientset() //nolint:staticcheck
	pm := &metricsv1beta1.PodMetrics{Name: "web", Namespace: shopNamespace, Containers: []metricsv1beta1.ContainerMetrics{
		{Name: "app", Usage: corev1.ResourceList{corev1.ResourceCPU: resource.MustParse("200m"), corev1.ResourceMemory: resource.MustParse("300Mi")}},
		{Name: "proxy", Usage: corev1.ResourceList{corev1.ResourceCPU: resource.MustParse("50m"), corev1.ResourceMemory: resource.MustParse("20Mi")}}}}
	if err := metricsClient.Tracker().Create(metricsv1beta1.SchemeGroupVersion.WithResource("pods"), pm, shopNamespace); err != nil {
		t.Fatal(err)
	}
	server.MetricsClient = metricsClient

	rr := send(t, server, http.MethodGet, "/api/namespaces/shop/pods", "")
	pods := decodeInto[[]PodDetail](t, rr)
	if rr.Code != http.StatusOK || rr.Header().Get("X-Metrics-Source") != metrics.SourceMetricsServer || len(pods) != 2 {
		t.Fatalf("pods = %d %v %+v", rr.Code, rr.Header(), pods)
	}
	byName := map[string]PodDetail{}
	for _, p := range pods {
		byName[p.Name] = p
	}
	w := byName["web"]
	if w.Status != "Running" || w.Ready || w.Reason != "CrashLoopBackOff" || w.Restarts != 4 {
		t.Errorf("web health = %+v, want not ready because of the crash-looping proxy", w)
	}
	if !sameQuantity(w.CPU.Requests, "1") || !sameQuantity(w.CPU.Limits, "1") || !sameQuantity(w.Memory.Requests, "2Gi") || !sameQuantity(w.Memory.Limits, "2Gi") ||
		!sameQuantity(w.CPU.Usage, "250m") || !sameQuantity(w.Memory.Usage, "320Mi") {
		t.Errorf("web resources = %+v / %+v", w.CPU, w.Memory)
	}
	if w.Cost == nil || !closeTo(w.Cost.MonthlyCost, monthlyAtCustomRates(1, 2)) || w.Cost.Currency != customCurrency {
		t.Errorf("web cost = %+v, want its requests priced at the custom rates", w.Cost)
	}
	if m := byName["migrate"]; m.Cost != nil || m.Status != "Succeeded" {
		t.Errorf("a finished pod costs nothing: %+v", m)
	}
}

func TestWorkloadsOfProtectedNamespacesCannotBeChanged(t *testing.T) {
	t.Setenv("POD_NAMESPACE", operatorNamespace)
	server := buildMockServerWithK8s()
	ctx := context.Background()
	optedOut := &corev1.Namespace{Name: "legacy", Labels: map[string]string{finopsv1.OptOutLabel: "off"}}
	createAll(t, server.Client, optedOut,
		&finopsv1.CostDeckConfig{Name: "default", Namespace: operatorNamespace, Spec: finopsv1.CostDeckConfigSpec{Rightsizing: finopsv1.RightsizingConfig{Apply: true}}})
	for _, ns := range []string{"kube-system", operatorNamespace, "legacy", "shop"} {
		createAll(t, server.Client, &appsv1.Deployment{Name: "app", Namespace: ns, Spec: appsv1.DeploymentSpec{Replicas: new(int32(2))}})
	}
	replicas := func(ns string) int32 {
		var d appsv1.Deployment
		if err := server.Client.Get(ctx, client.ObjectKey{Namespace: ns, Name: "app"}, &d); err != nil {
			t.Fatal(err)
		}
		return *d.Spec.Replicas
	}
	for ns, why := range map[string]string{"kube-system": "belongs to Kubernetes", operatorNamespace: "runs Cost Deck itself", "legacy": "labelled costdeck.io/schedule=off"} {
		rr := send(t, server, http.MethodPut, "/api/namespaces/"+ns+"/workloads/app", `{"kind":"Deployment","replicas":0}`)
		if rr.Code != http.StatusForbidden || !strings.Contains(rr.Body.String(), why) {
			t.Errorf("scaling app in %s = %d %s, want 403 saying it %s", ns, rr.Code, rr.Body.String(), why)
		}
		if got := replicas(ns); got != 2 {
			t.Errorf("app in %s has %d replicas, want it untouched", ns, got)
		}
		if rr := send(t, server, http.MethodPost, "/api/namespaces/"+ns+"/rightsizing/apply", `{"kind":"Deployment","name":"app"}`); rr.Code != http.StatusForbidden {
			t.Errorf("applying advice in %s = %d %s, want 403", ns, rr.Code, rr.Body.String())
		}
	}
	if rr := send(t, server, http.MethodPut, "/api/namespaces/shop/workloads/app", `{"kind":"Deployment","replicas":0}`); rr.Code != http.StatusOK || replicas("shop") != 0 {
		t.Errorf("scaling app in shop = %d %s, want it scaled", rr.Code, rr.Body.String())
	}
}
