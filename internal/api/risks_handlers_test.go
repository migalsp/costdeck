package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	appsv1 "k8s.io/api/apps/v1"
	autoscalingv2 "k8s.io/api/autoscaling/v2"
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

func riskDeployment(name string) *appsv1.Deployment {
	replicas := int32(1)
	return &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: "shop"},
		Spec: appsv1.DeploymentSpec{Replicas: &replicas, Template: corev1.PodTemplateSpec{Spec: corev1.PodSpec{Containers: []corev1.Container{{
			Name: "main",
			Resources: corev1.ResourceRequirements{
				Requests: corev1.ResourceList{corev1.ResourceCPU: resource.MustParse("500m"), corev1.ResourceMemory: resource.MustParse("1Gi")},
				Limits:   corev1.ResourceList{corev1.ResourceMemory: resource.MustParse("1Gi")},
			},
		}}}}},
	}
}

func riskPod(name string, status corev1.PodStatus) *corev1.Pod {
	return &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: "shop"}, Spec: corev1.PodSpec{Containers: []corev1.Container{{Name: "main"}}}, Status: status}
}

func TestRisksReadPodsAndAutoscalers(t *testing.T) {
	t.Setenv("POD_NAMESPACE", "costdeck")
	server := buildMockServerWithK8s()
	ctx := context.Background()
	oomStatus := corev1.PodStatus{Phase: corev1.PodRunning, ContainerStatuses: []corev1.ContainerStatus{{
		Name: "main", RestartCount: 2,
		LastTerminationState: corev1.ContainerState{Terminated: &corev1.ContainerStateTerminated{Reason: "OOMKilled", ExitCode: 137}},
	}}}
	crashStatus := corev1.PodStatus{Phase: corev1.PodRunning, ContainerStatuses: []corev1.ContainerStatus{{
		Name: "main", RestartCount: 6,
		State:                corev1.ContainerState{Waiting: &corev1.ContainerStateWaiting{Reason: "CrashLoopBackOff"}},
		LastTerminationState: corev1.ContainerState{Terminated: &corev1.ContainerStateTerminated{Reason: "Error", ExitCode: 1}},
	}}}
	evicted := corev1.PodStatus{Phase: corev1.PodFailed, Reason: "Evicted", Message: "The node was low on resource: memory."}
	hpa := &autoscalingv2.HorizontalPodAutoscaler{
		ObjectMeta: metav1.ObjectMeta{Name: "web", Namespace: "shop"},
		Spec: autoscalingv2.HorizontalPodAutoscalerSpec{
			ScaleTargetRef: autoscalingv2.CrossVersionObjectReference{Kind: "Deployment", Name: "web"}, MaxReplicas: 3,
			Metrics: []autoscalingv2.MetricSpec{{Type: autoscalingv2.ResourceMetricSourceType, Resource: &autoscalingv2.ResourceMetricSource{
				Name: corev1.ResourceCPU, Target: autoscalingv2.MetricTarget{Type: autoscalingv2.UtilizationMetricType, AverageUtilization: new(int32(60))},
			}}},
		},
		Status: autoscalingv2.HorizontalPodAutoscalerStatus{DesiredReplicas: 3, CurrentReplicas: 3,
			CurrentMetrics: []autoscalingv2.MetricStatus{{Type: autoscalingv2.ResourceMetricSourceType, Resource: &autoscalingv2.ResourceMetricStatus{
				Name: corev1.ResourceCPU, Current: autoscalingv2.MetricValueStatus{AverageUtilization: new(int32(100))},
			}}},
			Conditions: []autoscalingv2.HorizontalPodAutoscalerCondition{{Type: autoscalingv2.ScalingLimited, Status: corev1.ConditionTrue, Reason: "TooManyReplicas"}},
		},
	}
	for _, obj := range []client.Object{
		riskDeployment("api"), riskDeployment("worker"), riskDeployment("web"),
		riskPod("api-7d9f8c-abcde", oomStatus), riskPod("worker-5c6b7d-fghij", crashStatus), riskPod("web-6f7a8b-klmno", evicted), hpa,
	} {
		if err := server.Client.Create(ctx, obj); err != nil {
			t.Fatalf("creating %s: %v", obj.GetName(), err)
		}
	}
	metricsClient := metricsfake.NewSimpleClientset() //nolint:staticcheck // k8s.io/metrics generates no NewClientset
	for _, pod := range []string{"api-7d9f8c-abcde", "worker-5c6b7d-fghij"} {
		pm := &metricsv1beta1.PodMetrics{
			ObjectMeta: metav1.ObjectMeta{Name: pod, Namespace: "shop"},
			Containers: []metricsv1beta1.ContainerMetrics{{Name: "main", Usage: corev1.ResourceList{corev1.ResourceCPU: resource.MustParse("50m"), corev1.ResourceMemory: resource.MustParse("100Mi")}}},
		}
		if err := metricsClient.Tracker().Create(metricsv1beta1.SchemeGroupVersion.WithResource("pods"), pm, "shop"); err != nil {
			t.Fatal(err)
		}
	}
	server.MetricsClient = metricsClient

	rr := httptest.NewRecorder()
	server.routes().ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/api/namespaces/shop/risks", nil))
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d: %s", rr.Code, rr.Body.String())
	}
	var report risks.Report
	if err := json.Unmarshal(rr.Body.Bytes(), &report); err != nil {
		t.Fatal(err)
	}
	found := map[string]risks.Risk{}
	for _, r := range report.Risks {
		found[r.Workload+"/"+r.Code] = r
	}
	if r, ok := found["api/"+risks.CodeOOMKilled]; !ok || r.Severity != risks.SeverityCritical || r.FixMonthlyCost == nil {
		t.Errorf("api OOM risk = %+v in %+v", r, report.Risks)
	}
	if r, ok := found["worker/"+risks.CodeRestarts]; !ok || r.Severity != risks.SeverityCritical || !strings.Contains(r.Message, "Error (exit code 1)") || !strings.Contains(r.Fix, "kubectl logs -n shop deployment/worker -c main --previous") {
		t.Errorf("worker restart risk = %+v", r)
	}
	if r, ok := found["web/"+risks.CodeEvicted]; !ok || !strings.Contains(r.Message, "low on resource: memory") {
		t.Errorf("web eviction risk = %+v", r)
	}
	if r, ok := found["web/"+risks.CodeHPAAtMax]; !ok || !strings.Contains(r.Fix, "maxReplicas of web to 5") {
		t.Errorf("web HPA risk = %+v", r)
	}
	if report.Risks[len(report.Risks)-1].Severity != risks.SeverityWarning || report.Historical || !strings.Contains(report.Basis, "Connect VictoriaMetrics") {
		t.Errorf("report = %+v: critical risks first, and without VictoriaMetrics only the current state", report)
	}

	rr = httptest.NewRecorder()
	server.routes().ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/api/namespaces/shop/recommendations", nil))
	var advice rightsizing.Report
	if err := json.Unmarshal(rr.Body.Bytes(), &advice); err != nil {
		t.Fatal(err)
	}
	for _, w := range advice.Workloads {
		if w.Name == "api" {
			if m := w.Containers[0].Memory; m.Action != rightsizing.ActionKeep || m.Held != "OOM-killed" {
				t.Errorf("api memory advice = %+v: an OOM-killed container must not be told to use less", m)
			}
		}
	}
}

func TestRisksFlagASlowWakeUp(t *testing.T) {
	t.Setenv("POD_NAMESPACE", "costdeck")
	server := buildMockServerWithK8s()
	ctx := context.Background()
	group := &finopsv1.ScalingGroup{
		ObjectMeta: metav1.ObjectMeta{Name: "dev", Namespace: "costdeck"},
		Spec: finopsv1.ScalingGroupSpec{Category: "e2e", Namespaces: []string{"shop"}, Schedules: []finopsv1.ScalingSchedule{
			{Days: []int{1, 2, 3, 4, 5}, StartTime: "08:00", EndTime: "18:00", Timezone: "UTC"},
		}},
	}
	group.Status.RecentWakeUps = []finopsv1.WakeUp{{StartedAt: metav1.Now(), Seconds: 420}, {StartedAt: metav1.Now(), Seconds: 200}}
	if err := server.Client.Create(ctx, group); err != nil {
		t.Fatal(err)
	}
	if err := server.Client.Create(ctx, riskDeployment("api")); err != nil {
		t.Fatal(err)
	}
	server.MetricsClient = metricsfake.NewSimpleClientset() //nolint:staticcheck // k8s.io/metrics generates no NewClientset

	rr := httptest.NewRecorder()
	server.routes().ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/api/namespaces/shop/risks", nil))
	var report risks.Report
	if err := json.Unmarshal(rr.Body.Bytes(), &report); err != nil {
		t.Fatalf("%v: %s", err, rr.Body.String())
	}
	for _, r := range report.Risks {
		if r.Code == risks.CodeSlowWakeUp {
			if r.Kind != "ScalingGroup" || r.Workload != "dev" || !strings.Contains(r.Message, "took up to 7m") || !strings.Contains(r.Fix, "about 8m earlier") || r.FixMonthlyCost == nil || *r.FixMonthlyCost <= 0 {
				t.Errorf("slow wake-up risk = %+v", r)
			}
			return
		}
	}
	t.Errorf("no slow wake-up risk in %+v", report.Risks)
}
