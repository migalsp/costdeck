package metrics

import (
	"context"
	"fmt"
	"sync"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	metricsapi "k8s.io/metrics/pkg/apis/metrics/v1beta1"
	metricsv "k8s.io/metrics/pkg/client/clientset/versioned"
)

// defaultMaxAge is how long one cluster-wide reading of the Metrics API is reused.
// metrics-server itself refreshes every 15 to 60 seconds.
const defaultMaxAge = 30 * time.Second

// MetricsServerSource reads live usage from the Kubernetes Metrics API. Every caller
// shares one cluster-wide reading, refreshed at most every MaxAge: sampling each namespace
// separately cost one Metrics API request per namespace per minute.
type MetricsServerSource struct {
	Client metricsv.Interface
	// MaxAge bounds the age of the shared reading; zero means 30 seconds.
	MaxAge time.Duration

	mu    sync.Mutex
	at    time.Time
	byNS  map[string][]metricsapi.PodMetrics
	until time.Time // no new attempt before this after a failure
	err   error
}

// Name implements Source.
func (s *MetricsServerSource) Name() string { return SourceMetricsServer }

// pods returns the pod metrics of one namespace, or of all namespaces for "", from the
// shared reading.
func (s *MetricsServerSource) pods(ctx context.Context, namespace string) ([]metricsapi.PodMetrics, error) {
	if s.Client == nil {
		return nil, fmt.Errorf("metrics API client is not configured")
	}
	maxAge := s.MaxAge
	if maxAge <= 0 {
		maxAge = defaultMaxAge
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	now := time.Now()
	if s.byNS == nil || now.Sub(s.at) >= maxAge {
		if now.Before(s.until) {
			return nil, s.err
		}
		// The reading is shared, so one caller giving up must not fail it for the others.
		fetchCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 20*time.Second)
		list, err := s.Client.MetricsV1beta1().PodMetricses("").List(fetchCtx, metav1.ListOptions{})
		cancel()
		if err != nil {
			// Back off briefly so a broken metrics-server is not asked once per namespace.
			s.err, s.until = fmt.Errorf("list pod metrics from metrics-server: %w", err), now.Add(10*time.Second)
			return nil, s.err
		}
		byNS := make(map[string][]metricsapi.PodMetrics)
		for _, pm := range list.Items {
			byNS[pm.Namespace] = append(byNS[pm.Namespace], pm)
		}
		s.byNS, s.at, s.err = byNS, now, nil
	}
	if namespace != "" {
		return s.byNS[namespace], nil
	}
	all := []metricsapi.PodMetrics{}
	for _, items := range s.byNS {
		all = append(all, items...)
	}
	return all, nil
}

// NamespaceUsage implements Source.
func (s *MetricsServerSource) NamespaceUsage(ctx context.Context, namespace string) (Usage, error) {
	pods, err := s.PodUsage(ctx, namespace)
	if err != nil {
		return Usage{}, err
	}
	var total Usage
	for _, u := range pods {
		total.CPU.Add(u.CPU)
		total.Memory.Add(u.Memory)
	}
	return total, nil
}

// PodUsage implements Source.
func (s *MetricsServerSource) PodUsage(ctx context.Context, namespace string) (map[string]Usage, error) {
	items, err := s.pods(ctx, namespace)
	if err != nil {
		return nil, err
	}
	usage := make(map[string]Usage, len(items))
	for _, pm := range items {
		usage[pm.Name] = podTotal(pm)
	}
	return usage, nil
}

// ClusterPodUsage returns the usage of every pod in the cluster, keyed namespace/name.
func (s *MetricsServerSource) ClusterPodUsage(ctx context.Context) (map[string]Usage, error) {
	items, err := s.pods(ctx, "")
	if err != nil {
		return nil, err
	}
	usage := make(map[string]Usage, len(items))
	for _, pm := range items {
		usage[pm.Namespace+"/"+pm.Name] = podTotal(pm)
	}
	return usage, nil
}

func podTotal(pm metricsapi.PodMetrics) Usage {
	var u Usage
	for _, c := range pm.Containers {
		u.CPU.Add(*c.Usage.Cpu())
		u.Memory.Add(*c.Usage.Memory())
	}
	return u
}
