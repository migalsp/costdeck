package metrics

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"sigs.k8s.io/controller-runtime/pkg/client"

	finopsv1 "github.com/migalsp/costdeck-operator/api/v1"
	"github.com/migalsp/costdeck-operator/internal/config"
)

// Keys read from the VictoriaMetrics credentials Secret.
const (
	SecretKeyBearerToken = "BEARER_TOKEN"
	SecretKeyUsername    = "USERNAME"
	SecretKeyPassword    = "PASSWORD"
	SecretKeyCACert      = "CA_CERT"
)

// defaultLookback is used for averages when the config does not set retentionDays.
const defaultLookback = 7 * 24 * time.Hour

// Result tells the caller which source answered and, when VictoriaMetrics is configured but
// metrics-server had to step in, why.
type Result struct {
	Source   string
	Degraded error
}

// Provider resolves the metrics source from the live CostDeckConfig on every call, so that
// enabling, disabling or re-pointing VictoriaMetrics in the settings takes effect without
// restarting the operator. The resolved client is cached until the config changes, and the
// referenced Secret is re-read at most once per TTL so rotated credentials are picked up.
type Provider struct {
	Client        client.Reader
	MetricsServer Source
	TTL           time.Duration

	mu         sync.Mutex
	key        string
	resolvedAt time.Time
	vm         *VMClient
	vmErr      error
	lookback   time.Duration
}

// NewProvider builds a Provider that falls back to the given metrics-server source.
func NewProvider(c client.Reader, metricsServer Source) *Provider {
	return &Provider{Client: c, MetricsServer: metricsServer, TTL: 30 * time.Second}
}

// resolve returns the VictoriaMetrics client when it is enabled, a configuration error
// when it is enabled but unusable, and (nil, nil) when it is disabled.
func (p *Provider) resolve(ctx context.Context) (*VMClient, time.Duration, error) {
	cfg, err := config.Get(ctx, p.Client)
	if err != nil {
		return nil, defaultLookback, err
	}
	vm := cfg.Spec.Integrations.VictoriaMetrics
	if vm == nil || !vm.Enabled {
		return nil, defaultLookback, nil
	}

	key := fmt.Sprintf("%s|%s|%s|%t", vm.Endpoint, vm.SecretRef, vm.LabelSelector, vm.SkipSSLVerify)
	p.mu.Lock()
	defer p.mu.Unlock()
	if key == p.key && time.Since(p.resolvedAt) < p.TTL {
		return p.vm, p.lookback, p.vmErr
	}

	p.key, p.resolvedAt = key, time.Now()
	p.lookback = defaultLookback
	if vm.RetentionDays > 0 {
		p.lookback = time.Duration(vm.RetentionDays) * 24 * time.Hour
	}
	p.vm, p.vmErr = BuildVMClient(ctx, p.Client, vm, nil)
	return p.vm, p.lookback, p.vmErr
}

// BuildVMClient creates a client from a VictoriaMetrics config. Credentials come from the
// referenced Secret unless override supplies them (used to test unsaved settings).
func BuildVMClient(ctx context.Context, c client.Reader, vm *finopsv1.VictoriaMetricsConfig, override map[string][]byte) (*VMClient, error) {
	if vm.Endpoint == "" {
		return nil, errors.New("VictoriaMetrics is enabled but no endpoint is configured")
	}
	opts := VMOptions{
		Endpoint:           vm.Endpoint,
		LabelSelector:      vm.LabelSelector,
		InsecureSkipVerify: vm.SkipSSLVerify,
	}
	data := override
	if data == nil && vm.SecretRef != "" {
		var err error
		if data, err = config.SecretData(ctx, c, vm.SecretRef); err != nil {
			return nil, err
		}
	}
	opts.BearerToken = string(data[SecretKeyBearerToken])
	opts.Username = string(data[SecretKeyUsername])
	opts.Password = string(data[SecretKeyPassword])
	opts.CACert = data[SecretKeyCACert]
	return NewVMClient(opts)
}

// ActiveSource reports which source a query would use right now.
func (p *Provider) ActiveSource(ctx context.Context) (string, error) {
	vm, _, err := p.resolve(ctx)
	if vm != nil {
		return SourceVictoriaMetrics, nil
	}
	return SourceMetricsServer, err
}

// NamespaceUsage returns current namespace usage, preferring VictoriaMetrics.
func (p *Provider) NamespaceUsage(ctx context.Context, namespace string) (Usage, Result, error) {
	vm, _, vmErr := p.resolve(ctx)
	if vm != nil {
		u, err := vm.NamespaceUsage(ctx, namespace)
		if err == nil {
			return u, Result{Source: SourceVictoriaMetrics}, nil
		}
		vmErr = err
	}
	u, err := p.MetricsServer.NamespaceUsage(ctx, namespace)
	return u, Result{Source: SourceMetricsServer, Degraded: vmErr}, err
}

// PodUsage returns current per-pod usage, preferring VictoriaMetrics.
func (p *Provider) PodUsage(ctx context.Context, namespace string) (map[string]Usage, Result, error) {
	vm, _, vmErr := p.resolve(ctx)
	if vm != nil {
		u, err := vm.PodUsage(ctx, namespace)
		if err == nil {
			return u, Result{Source: SourceVictoriaMetrics}, nil
		}
		vmErr = err
	}
	u, err := p.MetricsServer.PodUsage(ctx, namespace)
	return u, Result{Source: SourceMetricsServer, Degraded: vmErr}, err
}

// Validate checks the currently configured VictoriaMetrics endpoint. It returns
// (false, nil) when the integration is disabled.
func (p *Provider) Validate(ctx context.Context) (bool, error) {
	cfg, err := config.Get(ctx, p.Client)
	if err != nil {
		return false, err
	}
	vm := cfg.Spec.Integrations.VictoriaMetrics
	if vm == nil || !vm.Enabled {
		return false, nil
	}
	c, err := BuildVMClient(ctx, p.Client, vm, nil)
	if err != nil {
		return true, err
	}
	return true, c.Validate(ctx)
}

// Interface guards.
var (
	_ Source = (*VMClient)(nil)
	_ Source = (*MetricsServerSource)(nil)
)
