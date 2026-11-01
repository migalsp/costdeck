package api

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	"sigs.k8s.io/controller-runtime/pkg/client"

	finopsv1 "github.com/migalsp/costdeck-operator/api/v1"
	"github.com/migalsp/costdeck-operator/internal/pricing"
)

func TestGPUUsageTool(t *testing.T) {
	t.Setenv("POD_NAMESPACE", "costdeck")
	server := buildMockServerWithK8s()
	ctx := context.Background()
	capacity := corev1.ResourceList{corev1.ResourceCPU: resource.MustParse("8"), corev1.ResourceMemory: resource.MustParse("32Gi"), pricing.ResourceNvidiaGPU: resource.MustParse("4")}
	objs := []client.Object{
		&corev1.Node{Name: "gpu", Labels: map[string]string{"nvidia.com/gpu.product": "NVIDIA-A10G"}, Status: corev1.NodeStatus{Capacity: capacity, Allocatable: capacity}},
		&corev1.Namespace{Name: "ml"},
		&finopsv1.NamespaceFinOps{Name: "ml", Namespace: "costdeck", Spec: finopsv1.NamespaceFinOpsSpec{TargetNamespace: "ml"}},
		&corev1.Pod{Namespace: "ml", Name: "train", Spec: corev1.PodSpec{NodeName: "gpu", Containers: []corev1.Container{{Name: "c",
			Resources: corev1.ResourceRequirements{Limits: corev1.ResourceList{pricing.ResourceNvidiaGPU: resource.MustParse("2")}}}}}, Status: corev1.PodStatus{Phase: corev1.PodRunning}},
	}
	for _, o := range objs {
		if err := server.Client.Create(ctx, o); err != nil {
			t.Fatal(err)
		}
	}
	out, err := server.toolGPUUsage(ctx)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{`"gpus":4`, `"gpusRequested":2`, `"namespace":"ml"`, `"models":["NVIDIA-A10G"]`, `"averageGpuHour":0.81`, "connect VictoriaMetrics with NVIDIA's DCGM exporter"} {
		if !strings.Contains(out, want) {
			t.Errorf("tool output lacks %s:\n%s", want, out)
		}
	}
}

func TestGPUUsageToolListsTheCostliestGPUNamespaceFirst(t *testing.T) {
	t.Setenv("POD_NAMESPACE", "costdeck")
	server := buildMockServerWithK8s()
	ctx := context.Background()
	capacity := corev1.ResourceList{corev1.ResourceCPU: resource.MustParse("16"), corev1.ResourceMemory: resource.MustParse("64Gi"), pricing.ResourceNvidiaGPU: resource.MustParse("4")}
	gpuPod := func(ns, name, gpus string) *corev1.Pod {
		return &corev1.Pod{Namespace: ns, Name: name, Spec: corev1.PodSpec{NodeName: "gpu", Containers: []corev1.Container{{Name: "c",
			Resources: corev1.ResourceRequirements{Limits: corev1.ResourceList{pricing.ResourceNvidiaGPU: resource.MustParse(gpus)}}}}}, Status: corev1.PodStatus{Phase: corev1.PodRunning}}
	}
	objs := []client.Object{
		&corev1.Node{Name: "gpu", Labels: map[string]string{"nvidia.com/gpu.product": "NVIDIA-A10G"}, Status: corev1.NodeStatus{Capacity: capacity, Allocatable: capacity}},
		&corev1.Namespace{Name: "ml-small"}, &corev1.Namespace{Name: "ml-big"}, &corev1.Namespace{Name: "web"},
		&finopsv1.NamespaceFinOps{Name: "ml-small", Namespace: "costdeck", Spec: finopsv1.NamespaceFinOpsSpec{TargetNamespace: "ml-small"}},
		&finopsv1.NamespaceFinOps{Name: "ml-big", Namespace: "costdeck", Spec: finopsv1.NamespaceFinOpsSpec{TargetNamespace: "ml-big"}},
		&finopsv1.NamespaceFinOps{Name: "web", Namespace: "costdeck", Spec: finopsv1.NamespaceFinOpsSpec{TargetNamespace: "web"}},
		gpuPod("ml-small", "notebook", "1"), gpuPod("ml-big", "train", "3"),
		&corev1.Pod{Namespace: "web", Name: "frontend", Spec: corev1.PodSpec{NodeName: "gpu", Containers: []corev1.Container{{Name: "c"}}}, Status: corev1.PodStatus{Phase: corev1.PodRunning}},
	}
	for _, o := range objs {
		if err := server.Client.Create(ctx, o); err != nil {
			t.Fatal(err)
		}
	}
	out, err := server.toolGPUUsage(ctx)
	if err != nil {
		t.Fatal(err)
	}
	var got struct {
		Namespaces []struct {
			Namespace string  `json:"namespace"`
			GPUs      float64 `json:"gpus"`
			Cost      float64 `json:"monthlyGpuCost"`
		} `json:"namespaces"`
	}
	if err := json.Unmarshal([]byte(out), &got); err != nil {
		t.Fatal(err)
	}
	if len(got.Namespaces) != 2 || got.Namespaces[0].Namespace != "ml-big" || got.Namespaces[1].Namespace != "ml-small" ||
		got.Namespaces[0].GPUs != 3 || got.Namespaces[0].Cost <= got.Namespaces[1].Cost {
		t.Errorf("namespaces = %+v, want ml-big before ml-small and no row for web, which holds no GPU", got.Namespaces)
	}
}
