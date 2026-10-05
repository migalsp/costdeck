package scaling

import (
	"context"
	"errors"
	"fmt"

	"sigs.k8s.io/controller-runtime/pkg/client"

	finopsv1 "github.com/migalsp/costdeck-operator/api/v1"
	"github.com/migalsp/costdeck-operator/internal/config"
)

// CloudProvider is an ExternalProvider that can check its own credentials and lists the
// resource types it supports.
type CloudProvider interface {
	ExternalProvider
	ValidateConnectivity(ctx context.Context) error
	ResourceTypes() []string
}

// CloudProviders are the cloud provider names CostDeck supports, in display order.
var CloudProviders = []string{ProviderAWS, ProviderAzure, ProviderGCP}

// CloudSettings is the provider-neutral part of a provider's configuration.
type CloudSettings struct {
	Enabled bool
	// SecretRef names the Secret with stored credentials; empty means pod identity.
	SecretRef string
	// Filter holds the tags (AWS, Azure) or labels (Google Cloud) a resource must carry
	// to be discovered.
	Filter map[string]string
	// ResourceTypes are the configured types, or every supported type when none are set.
	ResourceTypes []string
}

// CloudSettingsFor returns the settings of one provider from the CostDeckConfig.
func CloudSettingsFor(cfg *finopsv1.CostDeckConfig, name string) CloudSettings {
	p := cfg.Spec.Providers
	switch name {
	case ProviderAWS:
		if p.AWS != nil {
			return CloudSettings{Enabled: p.AWS.Enabled, SecretRef: p.AWS.SecretRef, Filter: p.AWS.DiscoveryTags, ResourceTypes: orDefault(p.AWS.ResourceTypes, []string{AWSTypeAurora})}
		}
	case ProviderAzure:
		if p.Azure != nil {
			return CloudSettings{Enabled: p.Azure.Enabled, SecretRef: p.Azure.SecretRef, Filter: p.Azure.DiscoveryTags, ResourceTypes: orDefault(p.Azure.ResourceTypes, AzureResourceTypes)}
		}
	case ProviderGCP:
		if p.GCP != nil {
			return CloudSettings{Enabled: p.GCP.Enabled, SecretRef: p.GCP.SecretRef, Filter: p.GCP.DiscoveryLabels, ResourceTypes: orDefault(p.GCP.ResourceTypes, GCPResourceTypes)}
		}
	}
	return CloudSettings{}
}

func orDefault(v, def []string) []string {
	if len(v) > 0 {
		return v
	}
	return def
}

// cacheKey identifies the configuration a provider was built from.
func cacheKey(cfg *finopsv1.CostDeckConfig, name string) string {
	p := cfg.Spec.Providers
	switch name {
	case ProviderAWS:
		return name + "|" + p.AWS.SecretRef + "|" + p.AWS.Region
	case ProviderAzure:
		return name + "|" + p.Azure.SecretRef + "|" + p.Azure.SubscriptionID + "|" + p.Azure.TenantID
	case ProviderGCP:
		return name + "|" + p.GCP.SecretRef + "|" + p.GCP.ProjectID
	}
	return name
}

// BuildProvider creates a provider from the CostDeckConfig. Credentials come from
// override when given (unsaved settings being tested), else from the provider's Secret,
// else from the pod identity: IRSA or EKS Pod Identity, AKS workload identity, GKE
// workload identity.
func BuildProvider(ctx context.Context, c client.Reader, cfg *finopsv1.CostDeckConfig, name string, override map[string][]byte) (CloudProvider, error) {
	settings := CloudSettingsFor(cfg, name)
	data := override
	if data == nil && settings.SecretRef != "" {
		var err error
		if data, err = config.SecretData(ctx, c, settings.SecretRef); err != nil {
			return nil, err
		}
	}
	p := cfg.Spec.Providers
	switch name {
	case ProviderAWS:
		region := ""
		if p.AWS != nil {
			region = p.AWS.Region
		}
		if access, secret := string(data["AWS_ACCESS_KEY_ID"]), string(data["AWS_SECRET_ACCESS_KEY"]); access != "" && secret != "" {
			return NewAWSProviderFromCredentials(ctx, access, secret, region)
		}
		return NewAWSProvider(ctx)
	case ProviderAzure:
		if p.Azure == nil {
			return nil, errors.New("the Azure provider is not configured")
		}
		tenant := p.Azure.TenantID
		if t := string(data[AzureSecretTenantID]); t != "" {
			tenant = t
		}
		cred, err := NewAzureCredential(tenant, string(data[AzureSecretClientID]), string(data[AzureSecretClientSecret]))
		if err != nil {
			return nil, fmt.Errorf("the Azure credentials are not usable: %w", err)
		}
		return NewAzureProvider(ctx, cred, p.Azure.SubscriptionID), nil
	case ProviderGCP:
		if p.GCP == nil {
			return nil, errors.New("the Google Cloud provider is not configured")
		}
		creds, err := GCPCredentials(ctx, data[GCPSecretKey])
		if err != nil {
			return nil, fmt.Errorf("the Google Cloud credentials are not usable: %w", err)
		}
		project := p.GCP.ProjectID
		if project == "" {
			project = creds.ProjectID
		}
		return NewGCPProvider(ctx, creds.TokenSource, project), nil
	}
	return nil, fmt.Errorf("external provider %q is not supported", name)
}

// DiscoverAll lists the resources of every enabled provider, for the configured types.
// Errors are collected per provider so one misconfigured cloud does not hide the others.
func DiscoverAll(ctx context.Context, c client.Reader, cfg *finopsv1.CostDeckConfig) ([]finopsv1.ExternalTarget, map[string]error) {
	var all []finopsv1.ExternalTarget
	errs := map[string]error{}
	for _, name := range CloudProviders {
		settings := CloudSettingsFor(cfg, name)
		if !settings.Enabled {
			continue
		}
		provider, err := BuildProvider(ctx, c, cfg, name, nil)
		if err != nil {
			errs[name] = err
			continue
		}
		for _, rt := range settings.ResourceTypes {
			targets, err := provider.Discover(ctx, rt, settings.Filter)
			if err != nil {
				errs[name] = err
				continue
			}
			all = append(all, targets...)
		}
	}
	return all, errs
}
