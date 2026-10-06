package scaling

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"regexp"
	"strings"

	"github.com/Azure/azure-sdk-for-go/sdk/azcore"
	"github.com/Azure/azure-sdk-for-go/sdk/azcore/policy"
	"github.com/Azure/azure-sdk-for-go/sdk/azidentity"
	"golang.org/x/oauth2"
	"sigs.k8s.io/controller-runtime/pkg/log"

	finopsv1 "github.com/migalsp/costdeck-operator/api/v1"
)

// ProviderAzure is the name external targets and discovery use for Azure.
const ProviderAzure = "azure"

// Azure resource types CostDeck can discover, start and stop.
const (
	AzureTypeVM       = "vm"
	AzureTypePostgres = "postgres"
	AzureTypeMySQL    = "mysql"
)

// Secret keys of the Azure credentials Secret.
const (
	AzureSecretTenantID     = "AZURE_TENANT_ID"
	AzureSecretClientID     = "AZURE_CLIENT_ID"
	AzureSecretClientSecret = "AZURE_CLIENT_SECRET"
)

const azureManagementScope = "https://management.azure.com/.default"

// azureKind describes how one resource type is listed, started and stopped through Azure
// Resource Manager.
type azureKind struct {
	provider   string
	apiVersion string
	start      string
	stop       string // VMs are deallocated, not just powered off, so compute stops billing
}

var azureKinds = map[string]azureKind{
	AzureTypeVM:       {provider: "Microsoft.Compute/virtualMachines", apiVersion: "2024-07-01", start: "start", stop: "deallocate"},
	AzureTypePostgres: {provider: "Microsoft.DBforPostgreSQL/flexibleServers", apiVersion: "2024-08-01", start: "start", stop: "stop"},
	AzureTypeMySQL:    {provider: "Microsoft.DBforMySQL/flexibleServers", apiVersion: "2023-12-30", start: "start", stop: "stop"},
}

// AzureResourceTypes lists every type the Azure provider supports.
var AzureResourceTypes = []string{AzureTypeVM, AzureTypePostgres, AzureTypeMySQL}

// azureResourceID accepts only ARM IDs of the supported kinds, so a target can never point
// a request anywhere else.
var azureResourceID = regexp.MustCompile(`^/subscriptions/[0-9a-fA-F-]+/resourceGroups/[^/?#]+/providers/[^?#]+$`)

// AzureProvider starts and stops Azure virtual machines and flexible database servers.
type AzureProvider struct {
	subscription string
	rest         *restClient
	base         string
}

// NewAzureProvider builds a provider for a subscription from any Azure token credential.
func NewAzureProvider(ctx context.Context, cred azcore.TokenCredential, subscription string) *AzureProvider {
	return &AzureProvider{
		subscription: subscription,
		rest:         newRESTClient(ctx, &azureTokenSource{ctx: ctx, cred: cred}),
		base:         "https://management.azure.com",
	}
}

// NewAzureCredential returns a client-secret credential when one is given, otherwise the
// default chain (AKS workload identity, managed identity, environment).
func NewAzureCredential(tenantID, clientID, clientSecret string) (azcore.TokenCredential, error) {
	if clientID != "" && clientSecret != "" {
		if tenantID == "" {
			return nil, errors.New("a tenant ID is required with a client secret")
		}
		return azidentity.NewClientSecretCredential(tenantID, clientID, clientSecret, nil)
	}
	return azidentity.NewDefaultAzureCredential(nil)
}

// azureTokenSource adapts an azcore credential to oauth2 so the shared REST client can use it.
type azureTokenSource struct {
	ctx  context.Context
	cred azcore.TokenCredential
}

func (s *azureTokenSource) Token() (*oauth2.Token, error) {
	tok, err := s.cred.GetToken(s.ctx, policy.TokenRequestOptions{Scopes: []string{azureManagementScope}})
	if err != nil {
		return nil, err
	}
	return &oauth2.Token{AccessToken: tok.Token, TokenType: "Bearer", Expiry: tok.ExpiresOn}, nil
}

// Name implements ExternalProvider.
func (p *AzureProvider) Name() string { return ProviderAzure }

// ResourceTypes implements CloudProvider.
func (p *AzureProvider) ResourceTypes() []string { return AzureResourceTypes }

// ValidateConnectivity reads the subscription, which needs only reader access.
func (p *AzureProvider) ValidateConnectivity(ctx context.Context) error {
	if p.subscription == "" {
		return errors.New("no Azure subscription ID is configured")
	}
	var sub struct {
		DisplayName string `json:"displayName"`
		State       string `json:"state"`
	}
	if err := p.rest.do(ctx, http.MethodGet, fmt.Sprintf("%s/subscriptions/%s?api-version=2022-12-01", p.base, url.PathEscape(p.subscription)), nil, &sub); err != nil {
		return fmt.Errorf("could not read subscription %s: %w", p.subscription, err)
	}
	if sub.State != "" && sub.State != "Enabled" {
		return fmt.Errorf("subscription %s is %s", sub.DisplayName, sub.State)
	}
	return nil
}

type azureResource struct {
	ID         string            `json:"id"`
	Name       string            `json:"name"`
	Location   string            `json:"location"`
	Tags       map[string]string `json:"tags"`
	Properties struct {
		State        string `json:"state"` // flexible servers: Ready, Stopped, Starting, Stopping
		InstanceView struct {
			Statuses []struct {
				Code string `json:"code"`
			} `json:"statuses"`
		} `json:"instanceView"`
	} `json:"properties"`
}

// vmPowerState returns "running", "deallocated", "stopped", … from PowerState/<state>.
func vmPowerState(codes []struct {
	Code string `json:"code"`
}) string {
	for _, c := range codes {
		if state, ok := strings.CutPrefix(c.Code, "PowerState/"); ok {
			return state
		}
	}
	return ""
}

// Discover implements ExternalProvider.
func (p *AzureProvider) Discover(ctx context.Context, resourceType string, tags map[string]string) ([]finopsv1.ExternalTarget, error) {
	kind, ok := azureKinds[resourceType]
	if !ok {
		return nil, fmt.Errorf("discovery unsupported for Azure type %q", resourceType)
	}
	next := fmt.Sprintf("%s/subscriptions/%s/providers/%s?api-version=%s", p.base, url.PathEscape(p.subscription), kind.provider, kind.apiVersion)
	if resourceType == AzureTypeVM {
		next += "&statusOnly=true"
	}
	var targets []finopsv1.ExternalTarget
	for next != "" {
		var page struct {
			Value    []azureResource `json:"value"`
			NextLink string          `json:"nextLink"`
		}
		if err := p.rest.do(ctx, http.MethodGet, next, nil, &page); err != nil {
			return nil, fmt.Errorf("list Azure %s: %w", resourceType, err)
		}
		for _, r := range page.Value {
			if !matchLabels(r.Tags, tags) {
				continue
			}
			status := strings.ToLower(r.Properties.State)
			if resourceType == AzureTypeVM {
				status = vmPowerState(r.Properties.InstanceView.Statuses)
			}
			targets = append(targets, finopsv1.ExternalTarget{
				Provider: ProviderAzure, Type: resourceType, Identifier: r.ID, Region: r.Location, Status: status, Name: r.Name,
			})
		}
		var err error
		if next, err = nextLink(p.base, page.NextLink); err != nil {
			return nil, err
		}
	}
	return targets, nil
}

func (p *AzureProvider) kindOf(target finopsv1.ExternalTarget) (azureKind, error) {
	kind, ok := azureKinds[target.Type]
	if !ok {
		return kind, fmt.Errorf("unsupported Azure resource type: %s", target.Type)
	}
	if !azureResourceID.MatchString(target.Identifier) || !strings.Contains(strings.ToLower(target.Identifier), "/providers/"+strings.ToLower(kind.provider)+"/") {
		return kind, fmt.Errorf("%q is not the resource ID of an Azure %s", target.Identifier, target.Type)
	}
	return kind, nil
}

// Scale implements ExternalProvider: start, or stop (deallocate for VMs).
func (p *AzureProvider) Scale(ctx context.Context, target finopsv1.ExternalTarget, active bool) error {
	kind, err := p.kindOf(target)
	if err != nil {
		return err
	}
	action := kind.stop
	if active {
		action = kind.start
	}
	log.FromContext(ctx).Info("Calling Azure action", "action", action, "resource", target.Identifier)
	err = p.rest.do(ctx, http.MethodPost, fmt.Sprintf("%s%s/%s?api-version=%s", p.base, target.Identifier, action, kind.apiVersion), nil, nil)
	var api *apiError
	if errors.As(err, &api) && api.Status == http.StatusConflict {
		// Already in, or on its way to, the requested state.
		return nil
	}
	return err
}

// IsReady implements ExternalProvider.
func (p *AzureProvider) IsReady(ctx context.Context, target finopsv1.ExternalTarget, active bool) (bool, error) {
	kind, err := p.kindOf(target)
	if err != nil {
		return false, err
	}
	if target.Type == AzureTypeVM {
		var view struct {
			Statuses []struct {
				Code string `json:"code"`
			} `json:"statuses"`
		}
		if err := p.rest.do(ctx, http.MethodGet, fmt.Sprintf("%s%s/instanceView?api-version=%s", p.base, target.Identifier, kind.apiVersion), nil, &view); err != nil {
			return false, err
		}
		state := vmPowerState(view.Statuses)
		if active {
			return state == stateRunning, nil
		}
		return state == "deallocated" || state == stateStopped, nil
	}
	var r azureResource
	if err := p.rest.do(ctx, http.MethodGet, fmt.Sprintf("%s%s?api-version=%s", p.base, target.Identifier, kind.apiVersion), nil, &r); err != nil {
		return false, err
	}
	if active {
		return r.Properties.State == "Ready", nil
	}
	return r.Properties.State == "Stopped", nil
}
