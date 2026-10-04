package scaling

import (
	"context"
	"fmt"
	"sync"
	"time"

	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/migalsp/costdeck-operator/internal/config"
)

// ProviderResolver returns the external provider for a name ("aws", ...), configured from
// the live CostDeckConfig.
type ProviderResolver interface {
	Resolve(ctx context.Context, name string) (ExternalProvider, error)
}

// ConfigProviderResolver builds external providers from the CostDeckConfig singleton, so
// that the credentials saved in the settings page are the ones used to start and stop
// cloud resources, not only the ones used for discovery.
type ConfigProviderResolver struct {
	Client client.Reader
	// TTL bounds how long a built provider is reused before its Secret is re-read, so that
	// rotated credentials are picked up.
	TTL time.Duration

	mu    sync.Mutex
	cache map[string]cachedProvider
}

type cachedProvider struct {
	provider ExternalProvider
	builtAt  time.Time
}

// Resolve implements ProviderResolver.
func (r *ConfigProviderResolver) Resolve(ctx context.Context, name string) (ExternalProvider, error) {
	cfg, err := config.Get(ctx, r.Client)
	if err != nil {
		return nil, err
	}

	switch name {
	case "aws":
		aws := cfg.Spec.Providers.AWS
		if aws == nil || !aws.Enabled {
			return nil, fmt.Errorf("the AWS provider is not enabled in CostDeckConfig")
		}
		key := "aws|" + aws.SecretRef + "|" + aws.Region
		if p := r.cached(key); p != nil {
			return p, nil
		}
		var p *AWSProvider
		if aws.SecretRef != "" {
			p, err = NewAWSProviderFromSecret(ctx, r.Client, aws.SecretRef, cfg.Namespace, aws.Region)
		} else {
			// No stored keys: use the pod identity (IRSA, EKS Pod Identity, env vars).
			p, err = NewAWSProvider(ctx)
		}
		if err != nil {
			return nil, err
		}
		r.store(key, p)
		return p, nil
	default:
		return nil, fmt.Errorf("external provider %q is not supported", name)
	}
}

func (r *ConfigProviderResolver) cached(key string) ExternalProvider {
	r.mu.Lock()
	defer r.mu.Unlock()
	ttl := r.TTL
	if ttl == 0 {
		ttl = time.Minute
	}
	if c, ok := r.cache[key]; ok && time.Since(c.builtAt) < ttl {
		return c.provider
	}
	return nil
}

func (r *ConfigProviderResolver) store(key string, p ExternalProvider) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.cache == nil {
		r.cache = map[string]cachedProvider{}
	}
	r.cache[key] = cachedProvider{provider: p, builtAt: time.Now()}
}

// Provider returns the provider registered under name, falling back to the resolver.
func (e *Engine) Provider(ctx context.Context, name string) (ExternalProvider, error) {
	if p, ok := e.Providers[name]; ok {
		return p, nil
	}
	if e.Resolver == nil {
		return nil, fmt.Errorf("provider %s not found", name)
	}
	return e.Resolver.Resolve(ctx, name)
}
