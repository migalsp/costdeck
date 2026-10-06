package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	metricsv1beta1 "k8s.io/metrics/pkg/apis/metrics/v1beta1"
	metricsfake "k8s.io/metrics/pkg/client/clientset/versioned/fake"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/migalsp/costdeck-operator/internal/rightsizing"
)

func TestRecommendationsAdviseWithoutChangingWorkloads(t *testing.T) {
	t.Setenv("POD_NAMESPACE", "costdeck")
	server := buildMockServerWithK8s()
	replicas := int32(2)
	deploy := &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{Name: "api", Namespace: "shop"},
		Spec: appsv1.DeploymentSpec{
			Replicas: &replicas,
			Template: corev1.PodTemplateSpec{Spec: corev1.PodSpec{Containers: []corev1.Container{{
				Name: "api",
				Resources: corev1.ResourceRequirements{Requests: corev1.ResourceList{
					corev1.ResourceCPU: resource.MustParse("1"), corev1.ResourceMemory: resource.MustParse("1Gi"),
				}},
			}}}},
		},
	}
	if err := server.Client.Create(context.Background(), deploy); err != nil {
		t.Fatal(err)
	}
	// The fake metrics clientset lists PodMetrics from the "pods" resource but would file
	// an object passed to NewSimpleClientset under "podmetricses", so add it explicitly.
	metricsClient := metricsfake.NewSimpleClientset() //nolint:staticcheck // k8s.io/metrics generates no NewClientset
	podMetrics := &metricsv1beta1.PodMetrics{
		ObjectMeta: metav1.ObjectMeta{Name: "api-7d9f8c-abcde", Namespace: "shop"},
		Containers: []metricsv1beta1.ContainerMetrics{{Name: "api", Usage: corev1.ResourceList{
			corev1.ResourceCPU: resource.MustParse("50m"), corev1.ResourceMemory: resource.MustParse("100Mi"),
		}}},
	}
	gvr := metricsv1beta1.SchemeGroupVersion.WithResource("pods")
	if err := metricsClient.Tracker().Create(gvr, podMetrics, "shop"); err != nil {
		t.Fatal(err)
	}
	server.MetricsClient = metricsClient

	rr := httptest.NewRecorder()
	server.routes().ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/api/namespaces/shop/recommendations", nil))
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d: %s", rr.Code, rr.Body.String())
	}
	var report rightsizing.Report
	if err := json.Unmarshal(rr.Body.Bytes(), &report); err != nil {
		t.Fatal(err)
	}
	if report.Historical || !strings.Contains(report.Basis, "metrics-server") {
		t.Errorf("a metrics-server reading must be labelled as a snapshot, got %+v", report)
	}
	cpu := report.Workloads[0].Containers[0].CPU
	if cpu.Action != rightsizing.ActionReduce || cpu.Recommended != "75m" || report.MonthlySavings <= 0 {
		t.Errorf("CPU advice = %+v, savings %.2f; want reduce 1 -> 75m (50m + 50%%)", cpu, report.MonthlySavings)
	}

	var after appsv1.Deployment
	if err := server.Client.Get(context.Background(), client.ObjectKey{Namespace: "shop", Name: "api"}, &after); err != nil {
		t.Fatal(err)
	}
	if got := after.Spec.Template.Spec.Containers[0].Resources.Requests.Cpu().String(); got != "1" {
		t.Errorf("CPU request = %s after asking for advice; recommendations must never change a workload", got)
	}
}
