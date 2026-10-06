package metrics

import (
	"context"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	metricsv1beta1 "k8s.io/metrics/pkg/apis/metrics/v1beta1"
	metricsfake "k8s.io/metrics/pkg/client/clientset/versioned/fake"
)

func TestMetricsServerSharesOneReading(t *testing.T) {
	client := metricsfake.NewSimpleClientset() //nolint:staticcheck // k8s.io/metrics generates no NewClientset
	gvr := metricsv1beta1.SchemeGroupVersion.WithResource("pods")
	for _, p := range []struct{ ns, name, cpu string }{{"a", "a-1", "100m"}, {"a", "a-2", "50m"}, {"b", "b-1", "200m"}} {
		pm := &metricsv1beta1.PodMetrics{
			ObjectMeta: metav1.ObjectMeta{Name: p.name, Namespace: p.ns},
			Containers: []metricsv1beta1.ContainerMetrics{{Name: "c", Usage: corev1.ResourceList{
				corev1.ResourceCPU: resource.MustParse(p.cpu), corev1.ResourceMemory: resource.MustParse("64Mi"),
			}}},
		}
		if err := client.Tracker().Create(gvr, pm, p.ns); err != nil {
			t.Fatal(err)
		}
	}
	src := &MetricsServerSource{Client: client, MaxAge: time.Minute}
	ctx := context.Background()

	a, err := src.NamespaceUsage(ctx, "a")
	if err != nil || a.CPU.MilliValue() != 150 {
		t.Fatalf("namespace a = %v, %v", a.CPU.String(), err)
	}
	b, _ := src.NamespaceUsage(ctx, "b")
	containers, _ := src.ContainerUsage(ctx, "b")
	all, _ := src.ClusterPodUsage(ctx)
	a2 := all["a/a-2"]
	if b.CPU.MilliValue() != 200 || len(containers) != 1 || len(all) != 3 || a2.CPU.MilliValue() != 50 {
		t.Errorf("b = %v, containers %v, cluster %v", b.CPU.String(), containers, all)
	}
	if n := len(client.Actions()); n != 1 {
		t.Errorf("every caller should share one cluster-wide list, got %d requests", n)
	}

	src.MaxAge = time.Nanosecond
	time.Sleep(time.Millisecond)
	if _, err := src.NamespaceUsage(ctx, "a"); err != nil || len(client.Actions()) != 2 {
		t.Errorf("an expired reading is refreshed: %v, %d requests", err, len(client.Actions()))
	}
}
