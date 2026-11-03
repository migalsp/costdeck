package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metricsv1beta1 "k8s.io/metrics/pkg/apis/metrics/v1beta1"
	metricsfake "k8s.io/metrics/pkg/client/clientset/versioned/fake"

	"github.com/migalsp/costdeck-operator/internal/jvm"
	"github.com/migalsp/costdeck-operator/internal/rightsizing"
)

func javaDeployment(name string, annotations map[string]string) *appsv1.Deployment {
	replicas := int32(1)
	return &appsv1.Deployment{
		Name: name, Namespace: "shop", Annotations: annotations,
		Spec: appsv1.DeploymentSpec{
			Replicas: &replicas,
			Template: corev1.PodTemplateSpec{Spec: corev1.PodSpec{Containers: []corev1.Container{{
				Name:  "app",
				Image: "registry.example.com/shop/" + name + ":1.0",
				Env:   []corev1.EnvVar{{Name: "JAVA_TOOL_OPTIONS", Value: "-Xmx2g -XX:+UseG1GC"}},
				Resources: corev1.ResourceRequirements{
					Requests: corev1.ResourceList{corev1.ResourceCPU: resource.MustParse("1"), corev1.ResourceMemory: resource.MustParse("1Gi")},
					Limits:   corev1.ResourceList{corev1.ResourceCPU: resource.MustParse("2"), corev1.ResourceMemory: resource.MustParse("4Gi")},
				},
			}}}},
		},
	}
}

func TestRecommendationsReadTheJVMFromThePodSpec(t *testing.T) {
	t.Setenv("POD_NAMESPACE", "costdeck")
	server := buildMockServerWithK8s()
	for _, d := range []*appsv1.Deployment{javaDeployment("orders", nil), javaDeployment("legacy", map[string]string{jvm.RuntimeAnnotation: jvm.RuntimeNone})} {
		if err := server.Client.Create(context.Background(), d); err != nil {
			t.Fatal(err)
		}
	}
	metricsClient := metricsfake.NewSimpleClientset() //nolint:staticcheck // k8s.io/metrics generates no NewClientset
	for _, pod := range []string{"orders-7d9f8c-bcdfg", "legacy-5c6b7d-fghjk"} {
		pm := &metricsv1beta1.PodMetrics{
			Name: pod, Namespace: "shop",
			Containers: []metricsv1beta1.ContainerMetrics{{Name: "app", Usage: corev1.ResourceList{
				corev1.ResourceCPU: resource.MustParse("100m"), corev1.ResourceMemory: resource.MustParse("900Mi"),
			}}},
		}
		if err := metricsClient.Tracker().Create(metricsv1beta1.SchemeGroupVersion.WithResource("pods"), pm, "shop"); err != nil {
			t.Fatal(err)
		}
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
	advice := map[string]rightsizing.ContainerAdvice{}
	for _, w := range report.Workloads {
		advice[w.Name] = w.Containers[0]
	}
	orders := advice["orders"]
	if orders.JVM == nil || orders.JVM.DetectedBy != jvm.DetectedByOptions || orders.JVM.Heap != "2Gi" {
		t.Fatalf("orders JVM advice = %+v", orders.JVM)
	}
	if orders.Memory.Action != rightsizing.ActionIncrease || orders.Memory.Recommended != "2560Mi" {
		t.Errorf("orders memory advice = %+v, want 2560Mi: the 1Gi request does not cover the 2Gi heap", orders.Memory)
	}
	if !strings.Contains(rr.Body.String(), `"code":"request-below-heap"`) {
		t.Errorf("the request below the heap is not reported: %s", rr.Body.String())
	}
	if legacy := advice["legacy"]; legacy.JVM != nil || legacy.Memory.Recommended == "2560Mi" {
		t.Errorf("legacy opted out with %s=%s, got %+v", jvm.RuntimeAnnotation, jvm.RuntimeNone, legacy)
	}
	if !strings.Contains(report.Basis, "Java containers") {
		t.Errorf("basis = %q, want it to say how Java advice is worked out", report.Basis)
	}
}

func TestDroppingAdviceRecountsTheJavaSaving(t *testing.T) {
	var c recommendationCache
	javaContainer := func(saving float64) []rightsizing.ContainerAdvice {
		return []rightsizing.ContainerAdvice{{Name: "app", JVM: &rightsizing.JVMAdvice{MonthlySavings: saving}}}
	}
	c.put("shop", rightsizing.Report{JavaMonthlySavings: 30, Workloads: []rightsizing.WorkloadAdvice{
		{Kind: "Deployment", Name: "orders", Containers: javaContainer(20)},
		{Kind: "Deployment", Name: "billing", Containers: javaContainer(10)},
	}})
	c.drop("shop", "Deployment", "orders")
	if r, _ := c.get("shop"); r.JavaMonthlySavings != 10 {
		t.Errorf("Java saving = %v after dropping orders, want the 10 billing still offers", r.JavaMonthlySavings)
	}
}

func TestTheJavaAnnotationMarksOnlyThePodsMainContainer(t *testing.T) {
	app := corev1.Container{Name: "app", Image: "registry.example.com/shop/app:1.0"}
	proxy := corev1.Container{Name: "istio-proxy", Image: "docker.io/istio/proxyv2:1.24.0"}
	javaContainers := func(template corev1.PodTemplateSpec, annotation string) []string {
		t.Helper()
		var names []string
		for _, c := range toWorkload("Deployment", "orders", nil, template, annotation).Containers {
			if c.Java != nil {
				names = append(names, c.Name+":"+c.Java.DetectedBy)
			}
		}
		return names
	}
	pod := func(annotations map[string]string, containers ...corev1.Container) corev1.PodTemplateSpec {
		return corev1.PodTemplateSpec{Annotations: annotations, Spec: corev1.PodSpec{Containers: containers}}
	}
	for _, tc := range []struct {
		name     string
		template corev1.PodTemplateSpec
		want     []string
	}{
		{"the first container when nothing names one", pod(nil, app, proxy), []string{"app:" + jvm.DetectedByAnnotation}},
		{"the container kubectl defaults to", pod(map[string]string{defaultContainerAnnotation: "app"}, proxy, app), []string{"app:" + jvm.DetectedByAnnotation}},
		{"the first container when the default names none of them", pod(map[string]string{defaultContainerAnnotation: "gone"}, app, proxy), []string{"app:" + jvm.DetectedByAnnotation}},
	} {
		if got := javaContainers(tc.template, jvm.RuntimeJava); !slices.Equal(got, tc.want) {
			t.Errorf("%s: Java containers = %v, want %v", tc.name, got, tc.want)
		}
	}
	javaSidecar := corev1.Container{Name: "agent", Image: "registry.example.com/agent:1.0", Env: []corev1.EnvVar{{Name: "JAVA_TOOL_OPTIONS", Value: "-Xmx64m"}}}
	if got := javaContainers(pod(nil, app, javaSidecar), jvm.RuntimeJava); !slices.Equal(got, []string{"app:" + jvm.DetectedByAnnotation, "agent:" + jvm.DetectedByOptions}) {
		t.Errorf("a sidecar with its own Java hints = %v, want it still found by them", got)
	}
	if got := javaContainers(pod(nil, app, javaSidecar), jvm.RuntimeNone); len(got) != 0 {
		t.Errorf("none = %v, want no Java container at all", got)
	}
}

func TestAPodWideJavaAnnotationGivesTheSidecarNoJVMAdvice(t *testing.T) {
	t.Setenv("POD_NAMESPACE", "costdeck")
	server := buildMockServerWithK8s()
	d := javaDeployment("orders", map[string]string{jvm.RuntimeAnnotation: jvm.RuntimeJava})
	d.Spec.Template.Spec.Containers[0].Env = nil
	d.Spec.Template.Spec.Containers = append(d.Spec.Template.Spec.Containers, corev1.Container{
		Name: "istio-proxy", Image: "docker.io/istio/proxyv2:1.24.0",
		Resources: corev1.ResourceRequirements{
			Requests: corev1.ResourceList{corev1.ResourceCPU: resource.MustParse("100m"), corev1.ResourceMemory: resource.MustParse("128Mi")},
			Limits:   corev1.ResourceList{corev1.ResourceMemory: resource.MustParse("1Gi")},
		},
	})
	if err := server.Client.Create(context.Background(), d); err != nil {
		t.Fatal(err)
	}
	metricsClient := metricsfake.NewSimpleClientset() //nolint:staticcheck // k8s.io/metrics generates no NewClientset
	usage := corev1.ResourceList{corev1.ResourceCPU: resource.MustParse("50m"), corev1.ResourceMemory: resource.MustParse("100Mi")}
	pm := &metricsv1beta1.PodMetrics{Name: "orders-7d9f8c-bcdfg", Namespace: "shop",
		Containers: []metricsv1beta1.ContainerMetrics{{Name: "app", Usage: usage}, {Name: "istio-proxy", Usage: usage}}}
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
	for _, c := range report.Workloads[0].Containers {
		switch c.Name {
		case "app":
			if c.JVM == nil || c.JVM.DetectedBy != jvm.DetectedByAnnotation {
				t.Errorf("app JVM = %+v, want it marked Java by the annotation", c.JVM)
			}
		case "istio-proxy":
			if c.JVM != nil || c.Memory.Action == rightsizing.ActionIncrease {
				t.Errorf("istio-proxy got JVM advice from the pod-wide annotation: JVM %+v, memory %+v", c.JVM, c.Memory)
			}
		}
	}
}
