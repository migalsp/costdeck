package metrics

import (
	"context"
	"fmt"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	metricsv "k8s.io/metrics/pkg/client/clientset/versioned"
)

// MetricsServerSource reads live usage from the Kubernetes Metrics API.
type MetricsServerSource struct {
	Client metricsv.Interface
}

// Name implements Source.
func (s *MetricsServerSource) Name() string { return SourceMetricsServer }

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
	if s.Client == nil {
		return nil, fmt.Errorf("metrics API client is not configured")
	}
	list, err := s.Client.MetricsV1beta1().PodMetricses(namespace).List(ctx, metav1.ListOptions{})
	if err != nil {
		return nil, fmt.Errorf("list pod metrics from metrics-server: %w", err)
	}
	usage := make(map[string]Usage, len(list.Items))
	for _, pm := range list.Items {
		var u Usage
		for _, c := range pm.Containers {
			u.CPU.Add(*c.Usage.Cpu())
			u.Memory.Add(*c.Usage.Memory())
		}
		usage[pm.Name] = u
	}
	return usage, nil
}
