// Package metrics provides CPU and memory usage for namespaces and pods from either the
// Kubernetes Metrics API (metrics-server) or a PromQL backend such as VictoriaMetrics.
package metrics

import (
	"context"
	"errors"

	"k8s.io/apimachinery/pkg/api/resource"
)

// ErrUnsupported is returned by a Source that cannot answer a query, for example
// metrics-server, which keeps no history.
var ErrUnsupported = errors.New("not supported by this metrics source")

// Usage is the CPU and memory consumption of a namespace or a pod.
type Usage struct {
	CPU    resource.Quantity
	Memory resource.Quantity
}

// Source reports resource usage.
type Source interface {
	// Name identifies the source in status conditions and the UI.
	Name() string
	// NamespaceUsage returns the current total usage of all containers in a namespace.
	NamespaceUsage(ctx context.Context, namespace string) (Usage, error)
	// PodUsage returns the current usage of every pod in a namespace, keyed by pod name.
	PodUsage(ctx context.Context, namespace string) (map[string]Usage, error)
}

// Source names used in conditions and API responses.
const (
	SourceMetricsServer   = "metrics-server"
	SourceVictoriaMetrics = "victoriametrics"
)
