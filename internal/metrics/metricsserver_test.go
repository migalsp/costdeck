package metrics

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	"k8s.io/apimachinery/pkg/runtime"
	k8stesting "k8s.io/client-go/testing"
	metricsv1beta1 "k8s.io/metrics/pkg/apis/metrics/v1beta1"
	metricsfake "k8s.io/metrics/pkg/client/clientset/versioned/fake"
)

func TestMetricsServerSharesOneReading(t *testing.T) {
	client := metricsfake.NewSimpleClientset() //nolint:staticcheck // k8s.io/metrics generates no NewClientset
	gvr := metricsv1beta1.SchemeGroupVersion.WithResource("pods")
	for _, p := range []struct{ ns, name, cpu string }{{"a", "a-1", "100m"}, {"a", "a-2", "50m"}, {"b", "b-1", "200m"}} {
		pm := &metricsv1beta1.PodMetrics{
			Name: p.name, Namespace: p.ns,
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

func TestMetricsServerBacksOffAfterAFailure(t *testing.T) {
	src, cs := metricsServerWith(t, podMetric{"shop", "api-1", "api", "100m", "64Mi"})
	if src.Name() != SourceMetricsServer {
		t.Errorf("Name() = %q", src.Name())
	}
	down := true
	cs.PrependReactor("list", "pods", func(k8stesting.Action) (bool, runtime.Object, error) {
		if down {
			return true, nil, errors.New("the server is currently unable to handle the request")
		}
		return false, nil, nil
	})
	ctx := context.Background()

	for range 2 {
		if _, err := src.PodUsage(ctx, "shop"); err == nil || !strings.Contains(err.Error(), "list pod metrics from metrics-server") {
			t.Fatalf("PodUsage() error = %v, want the metrics-server failure", err)
		}
	}
	if n := len(cs.Actions()); n != 1 {
		t.Errorf("a broken metrics-server was asked %d times; within the back-off it must be asked once", n)
	}

	down = false
	src.mu.Lock()
	src.until = time.Now().Add(-time.Millisecond)
	src.mu.Unlock()
	pods, err := src.PodUsage(ctx, "shop")
	if err != nil || len(cs.Actions()) != 2 {
		t.Fatalf("after the back-off PodUsage() = %v, %v with %d requests", pods, err, len(cs.Actions()))
	}
	if api := pods["api-1"]; api.CPU.MilliValue() != 100 {
		t.Errorf("api-1 = %s after recovery", api.CPU.String())
	}
}

func TestMetricsServerWithoutAClient(t *testing.T) {
	src := &MetricsServerSource{}
	if _, err := src.NamespaceUsage(context.Background(), "shop"); err == nil || !strings.Contains(err.Error(), "metrics API client is not configured") {
		t.Errorf("NamespaceUsage() error = %v", err)
	}
	if _, err := src.ClusterPodUsage(context.Background()); err == nil {
		t.Error("ClusterPodUsage() without a client must fail")
	}
}
