package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	metricsv1beta1 "k8s.io/metrics/pkg/apis/metrics/v1beta1"
	metricsfake "k8s.io/metrics/pkg/client/clientset/versioned/fake"
	"sigs.k8s.io/controller-runtime/pkg/client"

	finopsv1 "github.com/migalsp/costdeck-operator/api/v1"
	"github.com/migalsp/costdeck-operator/internal/rightsizing"
	"github.com/migalsp/costdeck-operator/internal/risks"
)

func labelledDeployment(name string) *appsv1.Deployment {
	d := riskDeployment(name)
	d.Spec.Template.Labels = map[string]string{"app": name}
	d.Spec.Template.Spec.Containers[0].Resources.Requests[corev1.ResourceCPU] = resource.MustParse("2")
	return d
}

func TestRightsizingPolicies(t *testing.T) {
	t.Setenv("POD_NAMESPACE", "costdeck")
	server := buildMockServerWithK8s()
	ctx := context.Background()
	cpuMin := resource.MustParse("400m")
	oomKilled := corev1.PodStatus{Phase: corev1.PodRunning, ContainerStatuses: []corev1.ContainerStatus{{
		Name: "main", RestartCount: 1,
		LastTerminationState: corev1.ContainerState{Terminated: &corev1.ContainerStateTerminated{Reason: "OOMKilled", ExitCode: 137}},
	}}}
	objects := []client.Object{
		riskPod("batch-5c6b7d-fghjk", oomKilled),
		&finopsv1.CostDeckConfig{Name: "default", Namespace: "costdeck", Spec: finopsv1.CostDeckConfigSpec{Rightsizing: finopsv1.RightsizingConfig{Apply: true}}},
		labelledDeployment("api"), labelledDeployment("batch"), labelledDeployment("web"),
		&finopsv1.RightsizingPolicy{Name: "payments", Namespace: "shop", Spec: finopsv1.RightsizingPolicySpec{
			Selector: &metav1.LabelSelector{MatchLabels: map[string]string{"app": "api"}}, Apply: finopsv1.RightsizingApplyForbidden,
			CPU: finopsv1.ResourcePolicy{Min: &cpuMin},
		}},
		&finopsv1.RightsizingPolicy{Name: "jobs", Namespace: "shop", Spec: finopsv1.RightsizingPolicySpec{
			Selector: &metav1.LabelSelector{MatchLabels: map[string]string{"app": "batch"}}, Exclude: true,
		}},
	}
	for _, obj := range objects {
		if err := server.Client.Create(ctx, obj); err != nil {
			t.Fatal(err)
		}
	}
	metricsClient := metricsfake.NewSimpleClientset() //nolint:staticcheck // k8s.io/metrics generates no NewClientset
	for _, pod := range []string{"api-7d9f8c-bcdfg", "batch-5c6b7d-fghjk", "web-6f7c8b-klmnp"} {
		pm := &metricsv1beta1.PodMetrics{
			Name: pod, Namespace: "shop",
			Containers: []metricsv1beta1.ContainerMetrics{{Name: "main", Usage: corev1.ResourceList{corev1.ResourceCPU: resource.MustParse("50m"), corev1.ResourceMemory: resource.MustParse("100Mi")}}},
		}
		if err := metricsClient.Tracker().Create(metricsv1beta1.SchemeGroupVersion.WithResource("pods"), pm, "shop"); err != nil {
			t.Fatal(err)
		}
	}
	server.MetricsClient = metricsClient

	rr := httptest.NewRecorder()
	server.routes().ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/api/namespaces/shop/recommendations", nil))
	var report rightsizing.Report
	if err := json.Unmarshal(rr.Body.Bytes(), &report); err != nil {
		t.Fatal(err)
	}
	advice := map[string]rightsizing.WorkloadAdvice{}
	for _, w := range report.Workloads {
		advice[w.Name] = w
	}
	if _, ok := advice["batch"]; ok {
		t.Error("a workload the policy excludes still gets advice")
	}
	rr = httptest.NewRecorder()
	server.routes().ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/api/namespaces/shop/risks", nil))
	var riskReport risks.Report
	if err := json.Unmarshal(rr.Body.Bytes(), &riskReport); err != nil {
		t.Fatal(err)
	}
	if !slices.ContainsFunc(riskReport.Risks, func(r risks.Risk) bool { return r.Workload == "batch" && r.Code == risks.CodeOOMKilled }) {
		t.Errorf("risks = %+v; excluding a workload from right-sizing advice hid its OOM kill", riskReport.Risks)
	}
	api := advice["api"]
	if api.Policy != "payments" || !api.ApplyForbidden || api.Containers[0].CPU.Recommended != "400m" {
		t.Errorf("api advice = %+v, want the payments policy, apply forbidden and its 400m floor", api)
	}
	if web := advice["web"]; web.Policy != "" || web.ApplyForbidden || web.Containers[0].CPU.Recommended != "75m" {
		t.Errorf("web advice = %+v, want no policy", web)
	}

	rr = httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/namespaces/shop/rightsizing/apply", strings.NewReader(`{"kind":"Deployment","name":"api"}`))
	req.Header.Set("Content-Type", "application/json")
	server.routes().ServeHTTP(rr, req)
	if rr.Code != http.StatusForbidden || !strings.Contains(rr.Body.String(), "RightsizingPolicy payments forbids") {
		t.Errorf("apply = %d %s, want 403 naming the policy", rr.Code, rr.Body.String())
	}
	var after appsv1.Deployment
	if err := server.Client.Get(ctx, client.ObjectKey{Namespace: "shop", Name: "api"}, &after); err != nil || after.Spec.Template.Spec.Containers[0].Resources.Requests.Cpu().String() != "2" {
		t.Errorf("api was changed despite its policy: %v", after.Spec.Template.Spec.Containers[0].Resources.Requests)
	}
}

func TestAdviceCannotBeAppliedWhilePoliciesCannotBeRead(t *testing.T) {
	t.Setenv("POD_NAMESPACE", "costdeck")
	ctx := context.Background()
	policyLists := 0
	server := interceptedServer(interceptor.Funcs{List: func(ctx context.Context, c client.WithWatch, list client.ObjectList, opts ...client.ListOption) error {
		if _, ok := list.(*finopsv1.RightsizingPolicyList); ok {
			policyLists++
			return apierrors.NewTooManyRequests("the API server is throttling", 1)
		}
		return c.List(ctx, list, opts...)
	}})
	createAll(t, server.Client,
		&finopsv1.CostDeckConfig{Name: "default", Namespace: "costdeck", Spec: finopsv1.CostDeckConfigSpec{Rightsizing: finopsv1.RightsizingConfig{Apply: true}}},
		labelledDeployment("api"))
	metricsClient := metricsfake.NewSimpleClientset() //nolint:staticcheck // k8s.io/metrics generates no NewClientset
	pm := &metricsv1beta1.PodMetrics{Name: "api-7d9f8c-bcdfg", Namespace: "shop",
		Containers: []metricsv1beta1.ContainerMetrics{{Name: "main", Usage: corev1.ResourceList{corev1.ResourceCPU: resource.MustParse("50m"), corev1.ResourceMemory: resource.MustParse("100Mi")}}}}
	if err := metricsClient.Tracker().Create(metricsv1beta1.SchemeGroupVersion.WithResource("pods"), pm, "shop"); err != nil {
		t.Fatal(err)
	}
	server.MetricsClient = metricsClient

	rr := httptest.NewRecorder()
	server.routes().ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/api/namespaces/shop/recommendations", nil))
	var report rightsizing.Report
	if err := json.Unmarshal(rr.Body.Bytes(), &report); err != nil || len(report.Workloads) != 1 {
		t.Fatalf("recommendations = %d %s", rr.Code, rr.Body.String())
	}
	if w := report.Workloads[0]; !w.ApplyForbidden || w.Policy != policiesUnreadable {
		t.Errorf("advice = %+v; without the policies nobody knows whether one forbids applying it", w)
	}

	rr = httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/namespaces/shop/rightsizing/apply", strings.NewReader(`{"kind":"Deployment","name":"api"}`))
	req.Header.Set("Content-Type", "application/json")
	server.routes().ServeHTTP(rr, req)
	if rr.Code != http.StatusServiceUnavailable || !strings.Contains(rr.Body.String(), "could not be read") {
		t.Errorf("apply = %d %s, want 503", rr.Code, rr.Body.String())
	}
	var after appsv1.Deployment
	if err := server.Client.Get(ctx, client.ObjectKey{Namespace: "shop", Name: "api"}, &after); err != nil || after.Spec.Template.Spec.Containers[0].Resources.Requests.Cpu().String() != "2" {
		t.Errorf("api was changed while its policies were unknown: %v", after.Spec.Template.Spec.Containers[0].Resources.Requests)
	}
	if policyLists < 2 {
		t.Errorf("the policies were read %d time(s); a report made without them must not be cached", policyLists)
	}
}
