package metrics

import (
	"context"
	"encoding/base64"
	"errors"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	"k8s.io/apimachinery/pkg/runtime"
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	metricsv1beta1 "k8s.io/metrics/pkg/apis/metrics/v1beta1"
	metricsfake "k8s.io/metrics/pkg/client/clientset/versioned/fake"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	finopsv1 "github.com/migalsp/costdeck-operator/api/v1"
)

func configClient(t *testing.T, vm *finopsv1.VictoriaMetricsConfig, objs ...client.Object) client.Client {
	t.Helper()
	t.Setenv("POD_NAMESPACE", "costdeck")
	scheme := runtime.NewScheme()
	utilruntime.Must(clientgoscheme.AddToScheme(scheme))
	utilruntime.Must(finopsv1.AddToScheme(scheme))
	cfg := &finopsv1.CostDeckConfig{Name: "default", Namespace: "costdeck"}
	cfg.Spec.Integrations.VictoriaMetrics = vm
	return fake.NewClientBuilder().WithScheme(scheme).WithObjects(append(objs, cfg)...).Build()
}

type fixedSource struct {
	pods map[string]Usage
	err  error
}

func (s fixedSource) Name() string { return SourceMetricsServer }
func (s fixedSource) NamespaceUsage(context.Context, string) (Usage, error) {
	return Usage{}, s.err
}
func (s fixedSource) PodUsage(context.Context, string) (map[string]Usage, error) {
	return s.pods, s.err
}

type podMetric struct{ ns, pod, container, cpu, memory string }

func metricsServerWith(t *testing.T, pods ...podMetric) (*MetricsServerSource, *metricsfake.Clientset) {
	t.Helper()
	cs := metricsfake.NewSimpleClientset() //nolint:staticcheck
	gvr := metricsv1beta1.SchemeGroupVersion.WithResource("pods")
	byPod := map[string]*metricsv1beta1.PodMetrics{}
	var order []string
	for _, p := range pods {
		key := p.ns + "/" + p.pod
		pm, ok := byPod[key]
		if !ok {
			pm = &metricsv1beta1.PodMetrics{Name: p.pod, Namespace: p.ns}
			byPod[key] = pm
			order = append(order, key)
		}
		pm.Containers = append(pm.Containers, metricsv1beta1.ContainerMetrics{Name: p.container, Usage: corev1.ResourceList{
			corev1.ResourceCPU: resource.MustParse(p.cpu), corev1.ResourceMemory: resource.MustParse(p.memory),
		}})
	}
	for _, key := range order {
		if err := cs.Tracker().Create(gvr, byPod[key], byPod[key].Namespace); err != nil {
			t.Fatal(err)
		}
	}
	return &MetricsServerSource{Client: cs, MaxAge: time.Minute}, cs
}

func TestProviderActiveSource(t *testing.T) {
	prom := &fakePromQL{}
	srv := httptest.NewServer(prom)
	defer srv.Close()

	tests := []struct {
		name    string
		vm      *finopsv1.VictoriaMetricsConfig
		want    string
		wantErr string
	}{
		{name: "no VictoriaMetrics section", vm: nil, want: SourceMetricsServer},
		{name: "disabled", vm: &finopsv1.VictoriaMetricsConfig{Endpoint: srv.URL}, want: SourceMetricsServer},
		{name: "enabled", vm: &finopsv1.VictoriaMetricsConfig{Enabled: true, Endpoint: srv.URL}, want: SourceVictoriaMetrics},
		{name: "enabled without an endpoint", vm: &finopsv1.VictoriaMetricsConfig{Enabled: true}, want: SourceMetricsServer, wantErr: "no endpoint is configured"},
		{name: "credentials Secret missing", vm: &finopsv1.VictoriaMetricsConfig{Enabled: true, Endpoint: srv.URL, SecretRef: "absent"}, want: SourceMetricsServer, wantErr: "read secret costdeck/absent"},
		{name: "invalid label selector", vm: &finopsv1.VictoriaMetricsConfig{Enabled: true, Endpoint: srv.URL, LabelSelector: "cluster=prod"}, want: SourceMetricsServer, wantErr: "invalid label selector"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			p := NewProvider(configClient(t, tt.vm), fixedSource{})
			got, err := p.ActiveSource(context.Background())
			if got != tt.want {
				t.Errorf("ActiveSource() = %q, want %q", got, tt.want)
			}
			switch {
			case tt.wantErr == "" && err != nil:
				t.Errorf("ActiveSource() error = %v", err)
			case tt.wantErr != "" && (err == nil || !strings.Contains(err.Error(), tt.wantErr)):
				t.Errorf("ActiveSource() error = %v, want it to mention %q", err, tt.wantErr)
			}
		})
	}
	if len(prom.queries) != 0 {
		t.Errorf("reporting the source must not query VictoriaMetrics, got %v", prom.queries)
	}
}

func TestProviderPodUsagePrefersVictoriaMetricsAndFallsBack(t *testing.T) {
	healthy := &fakePromQL{answer: func(q string) []map[string]any {
		if strings.Contains(q, "cpu") {
			return []map[string]any{sample(map[string]string{"pod": "api-1"}, "0.5")}
		}
		return []map[string]any{sample(map[string]string{"pod": "api-1"}, "1048576")}
	}}
	healthySrv := httptest.NewServer(healthy)
	defer healthySrv.Close()
	broken := httptest.NewServer(&fakePromQL{failOn: func(string) bool { return true }})
	defer broken.Close()

	fromMetricsServer := map[string]Usage{"api-1": {CPU: *resourceMilli(120)}}
	tests := []struct {
		name         string
		vm           *finopsv1.VictoriaMetricsConfig
		wantSource   string
		wantCPUMilli int64
		wantDegraded []string
	}{
		{name: "healthy VictoriaMetrics", vm: &finopsv1.VictoriaMetricsConfig{Enabled: true, Endpoint: healthySrv.URL}, wantSource: SourceVictoriaMetrics, wantCPUMilli: 500},
		{name: "VictoriaMetrics disabled", vm: &finopsv1.VictoriaMetricsConfig{Endpoint: healthySrv.URL}, wantSource: SourceMetricsServer, wantCPUMilli: 120},
		{name: "VictoriaMetrics failing", vm: &finopsv1.VictoriaMetricsConfig{Enabled: true, Endpoint: broken.URL}, wantSource: SourceMetricsServer, wantCPUMilli: 120, wantDegraded: []string{"query pod CPU", "HTTP 500", "query execution failed"}},
		{name: "VictoriaMetrics misconfigured", vm: &finopsv1.VictoriaMetricsConfig{Enabled: true}, wantSource: SourceMetricsServer, wantCPUMilli: 120, wantDegraded: []string{"no endpoint is configured"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			p := NewProvider(configClient(t, tt.vm), fixedSource{pods: fromMetricsServer})
			pods, res, err := p.PodUsage(context.Background(), "shop")
			if err != nil {
				t.Fatal(err)
			}
			if res.Source != tt.wantSource {
				t.Errorf("source = %q, want %q", res.Source, tt.wantSource)
			}
			api := pods["api-1"]
			if got := api.CPU.MilliValue(); got != tt.wantCPUMilli {
				t.Errorf("api-1 CPU = %dm, want %dm", got, tt.wantCPUMilli)
			}
			if len(tt.wantDegraded) == 0 && res.Degraded != nil {
				t.Errorf("degraded = %v, want none", res.Degraded)
			}
			for _, want := range tt.wantDegraded {
				if res.Degraded == nil || !strings.Contains(res.Degraded.Error(), want) {
					t.Errorf("degraded = %v, want it to mention %q", res.Degraded, want)
				}
			}
		})
	}

	t.Run("metrics-server failing too", func(t *testing.T) {
		boom := errors.New("metrics API unavailable")
		p := NewProvider(configClient(t, nil), fixedSource{err: boom})
		if _, res, err := p.PodUsage(context.Background(), "shop"); !errors.Is(err, boom) || res.Source != SourceMetricsServer {
			t.Errorf("PodUsage() = %+v, %v; want the metrics-server error", res, err)
		}
	})
}

func TestProviderClusterPodUsageNeedsMetricsServer(t *testing.T) {
	ms, _ := metricsServerWith(t,
		podMetric{"shop", "api-1", "main", "100m", "64Mi"},
		podMetric{"shop", "api-1", "sidecar", "20m", "16Mi"},
		podMetric{"jobs", "worker", "main", "300m", "128Mi"},
	)
	p := NewProvider(configClient(t, nil), ms)
	got, err := p.ClusterPodUsage(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	api, worker := got["shop/api-1"], got["jobs/worker"]
	if len(got) != 2 || api.CPU.MilliValue() != 120 || api.Memory.Value() != 80<<20 || worker.CPU.MilliValue() != 300 {
		t.Errorf("cluster pod usage = %v; want pods keyed namespace/name with their containers summed", got)
	}

	other := NewProvider(configClient(t, nil), fixedSource{})
	if _, err := other.ClusterPodUsage(context.Background()); err == nil || !strings.Contains(err.Error(), "metrics-server is not configured") {
		t.Errorf("ClusterPodUsage() without a metrics-server source = %v", err)
	}
}

func TestProviderValidate(t *testing.T) {
	prom := &fakePromQL{answer: func(string) []map[string]any { return []map[string]any{sample(nil, "12")} }}
	srv := httptest.NewServer(prom)
	defer srv.Close()
	empty := &fakePromQL{}
	emptySrv := httptest.NewServer(empty)
	defer emptySrv.Close()
	creds := &corev1.Secret{Name: "vm-creds", Namespace: "costdeck", Data: map[string][]byte{SecretKeyUsername: []byte("reader"), SecretKeyPassword: []byte("pw")}}

	t.Run("disabled", func(t *testing.T) {
		enabled, err := NewProvider(configClient(t, &finopsv1.VictoriaMetricsConfig{Endpoint: srv.URL}), fixedSource{}).Validate(context.Background())
		if enabled || err != nil {
			t.Errorf("Validate() = %v, %v; want false, nil", enabled, err)
		}
	})
	t.Run("enabled without an endpoint", func(t *testing.T) {
		enabled, err := NewProvider(configClient(t, &finopsv1.VictoriaMetricsConfig{Enabled: true}), fixedSource{}).Validate(context.Background())
		if !enabled || err == nil || !strings.Contains(err.Error(), "no endpoint") {
			t.Errorf("Validate() = %v, %v; want true and a configuration error", enabled, err)
		}
	})
	t.Run("series present", func(t *testing.T) {
		vm := &finopsv1.VictoriaMetricsConfig{Enabled: true, Endpoint: srv.URL, SecretRef: "vm-creds", LabelSelector: `cluster="eu"`}
		enabled, err := NewProvider(configClient(t, vm, creds), fixedSource{}).Validate(context.Background())
		if !enabled || err != nil {
			t.Fatalf("Validate() = %v, %v", enabled, err)
		}
		if want := `count(container_cpu_usage_seconds_total{container!="",container!="POD",cluster="eu"})`; len(prom.queries) != 1 || prom.queries[0] != want {
			t.Errorf("queries = %v, want [%s]", prom.queries, want)
		}
		if want := "Basic " + base64.StdEncoding.EncodeToString([]byte("reader:pw")); prom.auth[0] != want {
			t.Errorf("Authorization = %q, want the Secret's basic auth %q", prom.auth[0], want)
		}
	})
	t.Run("no series behind the label selector", func(t *testing.T) {
		vm := &finopsv1.VictoriaMetricsConfig{Enabled: true, Endpoint: emptySrv.URL, LabelSelector: `cluster="eu"`}
		enabled, err := NewProvider(configClient(t, vm), fixedSource{}).Validate(context.Background())
		if !enabled || err == nil || !strings.Contains(err.Error(), `does the label selector {cluster="eu"} match your series?`) {
			t.Errorf("Validate() = %v, %v; want a hint about the label selector", enabled, err)
		}
	})
}

func TestProviderRereadsRotatedCredentialsAfterTheTTL(t *testing.T) {
	prom := &fakePromQL{answer: func(string) []map[string]any { return []map[string]any{sample(nil, "1")} }}
	srv := httptest.NewServer(prom)
	defer srv.Close()
	secret := &corev1.Secret{Name: "vm-creds", Namespace: "costdeck", Data: map[string][]byte{SecretKeyBearerToken: []byte("old")}}
	c := configClient(t, &finopsv1.VictoriaMetricsConfig{Enabled: true, Endpoint: srv.URL, SecretRef: "vm-creds"}, secret)
	p := NewProvider(c, fixedSource{})
	p.TTL = time.Hour
	ctx := context.Background()
	usage := func() {
		t.Helper()
		if _, res, err := p.NamespaceUsage(ctx, "shop"); err != nil || res.Source != SourceVictoriaMetrics {
			t.Fatalf("NamespaceUsage() = %+v, %v", res, err)
		}
	}

	usage()
	secret.Data[SecretKeyBearerToken] = []byte("new")
	if err := c.Update(ctx, secret); err != nil {
		t.Fatal(err)
	}
	usage()
	p.TTL = 0
	usage()

	want := []string{"Bearer old", "Bearer old", "Bearer old", "Bearer old", "Bearer new", "Bearer new"}
	if strings.Join(prom.auth, ",") != strings.Join(want, ",") {
		t.Errorf("Authorization headers = %v, want %v: the Secret is re-read only once the TTL has passed", prom.auth, want)
	}
}
