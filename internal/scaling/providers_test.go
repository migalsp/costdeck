package scaling

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"

	finopsv1 "github.com/migalsp/costdeck-operator/api/v1"
)

func resolvedProject(t *testing.T, r *ConfigProviderResolver) (*GCPProvider, string) {
	t.Helper()
	p, err := r.Resolve(context.Background(), ProviderGCP)
	if err != nil {
		t.Fatal(err)
	}
	gp, ok := p.(*GCPProvider)
	if !ok {
		t.Fatalf("provider = %T", p)
	}
	return gp, gp.project
}

func TestConfigProviderResolverCachesUntilTheConfigOrTTLChanges(t *testing.T) {
	ctx := context.Background()
	key := operatorSecret("gcp-key", map[string][]byte{GCPSecretKey: serviceAccountKey(t, "alpha", "https://oauth2.googleapis.com/token", placeholderPrivateKey)})
	cfg := costDeckConfig(finopsv1.ProvidersConfig{GCP: &finopsv1.GCPProviderConfig{Enabled: true, SecretRef: "gcp-key"}})
	c := cloudClient(t, key, cfg)
	r := &ConfigProviderResolver{Client: c}

	first, project := resolvedProject(t, r)
	if project != "alpha" {
		t.Fatalf("project = %s, want the key's", project)
	}

	key.Data[GCPSecretKey] = serviceAccountKey(t, "beta", "https://oauth2.googleapis.com/token", placeholderPrivateKey)
	must(t, c.Update(ctx, key))
	if again, project := resolvedProject(t, r); again != first || project != "alpha" {
		t.Errorf("within the TTL got a new provider for %s, want the cached one", project)
	}

	for k, cached := range r.cache {
		cached.builtAt = time.Now().Add(-61 * time.Second)
		r.cache[k] = cached
	}
	rotated, project := resolvedProject(t, r)
	if rotated == first || project != "beta" {
		t.Errorf("after the default one-minute TTL got project %s, want the rotated key's beta", project)
	}

	must(t, c.Get(ctx, client.ObjectKeyFromObject(cfg), cfg))
	cfg.Spec.Providers.GCP.ProjectID = "gamma"
	must(t, c.Update(ctx, cfg))
	if changed, project := resolvedProject(t, r); changed == rotated || project != "gamma" {
		t.Errorf("after the project changed in the settings got %s, want a new provider for gamma at once", project)
	}

	short := &ConfigProviderResolver{Client: c, TTL: time.Nanosecond}
	built, _ := resolvedProject(t, short)
	time.Sleep(time.Millisecond)
	if rebuilt, _ := resolvedProject(t, short); rebuilt == built {
		t.Error("a provider older than a custom TTL was reused")
	}
}

func TestConfigProviderResolverRefusesWhatItCannotBuild(t *testing.T) {
	disabled := costDeckConfig(finopsv1.ProvidersConfig{GCP: &finopsv1.GCPProviderConfig{Enabled: false, ProjectID: "demo"}})
	r := &ConfigProviderResolver{Client: cloudClient(t, disabled)}
	if p, err := r.Resolve(context.Background(), ProviderGCP); err == nil || p != nil || err.Error() != "the gcp provider is not enabled in CostDeckConfig" {
		t.Errorf("disabled provider = %v, %v", p, err)
	}

	missing := &ConfigProviderResolver{Client: cloudClient(t)}
	if p, err := missing.Resolve(context.Background(), ProviderAWS); err == nil || p != nil || err.Error() != "the aws provider is not enabled in CostDeckConfig" {
		t.Errorf("no CostDeckConfig = %v, %v", p, err)
	}

	broken := costDeckConfig(finopsv1.ProvidersConfig{GCP: &finopsv1.GCPProviderConfig{Enabled: true, SecretRef: "absent"}})
	unbuildable := &ConfigProviderResolver{Client: cloudClient(t, broken)}
	for range 2 {
		if p, err := unbuildable.Resolve(context.Background(), ProviderGCP); err == nil || p != nil || !strings.HasPrefix(err.Error(), "read secret costdeck/absent: ") {
			t.Errorf("missing Secret = %v, %v", p, err)
		}
	}
	if len(unbuildable.cache) != 0 {
		t.Errorf("a failed build was cached: %v", unbuildable.cache)
	}

	failing := &ConfigProviderResolver{Client: interceptor.NewClient(cloudClient(t).(client.WithWatch), interceptor.Funcs{
		Get: func(context.Context, client.WithWatch, client.ObjectKey, client.Object, ...client.GetOption) error {
			return errors.New("apiserver unavailable")
		},
	})}
	if p, err := failing.Resolve(context.Background(), ProviderGCP); err == nil || p != nil || !strings.Contains(err.Error(), "apiserver unavailable") {
		t.Errorf("unreadable CostDeckConfig = %v, %v", p, err)
	}
}
