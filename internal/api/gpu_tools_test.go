package api

import (
	"context"
	"strings"
	"testing"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
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
		&corev1.Node{ObjectMeta: metav1.ObjectMeta{Name: "gpu", Labels: map[string]string{"nvidia.com/gpu.product": "NVIDIA-A10G"}}, Status: corev1.NodeStatus{Capacity: capacity, Allocatable: capacity}},
		&corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "ml"}},
		&finopsv1.NamespaceFinOps{ObjectMeta: metav1.ObjectMeta{Name: "ml", Namespace: "costdeck"}, Spec: finopsv1.NamespaceFinOpsSpec{TargetNamespace: "ml"}},
		&corev1.Pod{ObjectMeta: metav1.ObjectMeta{Namespace: "ml", Name: "train"}, Spec: corev1.PodSpec{NodeName: "gpu", Containers: []corev1.Container{{Name: "c",
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
