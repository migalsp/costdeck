package scaling

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"path"
	"regexp"
	"strings"

	"golang.org/x/oauth2"
	"golang.org/x/oauth2/google"
	"sigs.k8s.io/controller-runtime/pkg/log"

	finopsv1 "github.com/migalsp/costdeck-operator/api/v1"
)

// ProviderGCP is the name external targets and discovery use for Google Cloud.
const ProviderGCP = "gcp"

// Google Cloud resource types CostDeck can discover, start and stop.
const (
	GCPTypeGCE      = "gce"
	GCPTypeCloudSQL = "cloudsql"
)

// GCPResourceTypes lists every type the Google Cloud provider supports.
var GCPResourceTypes = []string{GCPTypeGCE, GCPTypeCloudSQL}

// GCPSecretKey is the key of the service account key in the GCP credentials Secret.
const GCPSecretKey = "credentials.json"

const gcpScope = "https://www.googleapis.com/auth/cloud-platform"

// Cloud SQL activation policies: an instance runs while ALWAYS and stops when NEVER.
const (
	sqlPolicyAlways = "ALWAYS"
	sqlPolicyNever  = "NEVER"
)

// Compute instances are addressed as "<zone>/<name>", Cloud SQL instances by name.
var (
	gcpInstanceID = regexp.MustCompile(`^[a-z0-9-]+/[a-z]([-a-z0-9]*[a-z0-9])?$`)
	gcpSQLName    = regexp.MustCompile(`^[a-z]([-a-z0-9]*[a-z0-9])?$`)
)

// GCPProvider starts and stops Compute Engine instances and Cloud SQL instances.
type GCPProvider struct {
	project     string
	rest        *restClient
	computeBase string
	sqlBase     string
}

// NewGCPProvider builds a provider for a project from an OAuth2 token source.
func NewGCPProvider(ctx context.Context, ts oauth2.TokenSource, project string) *GCPProvider {
	return &GCPProvider{
		project:     project,
		rest:        newRESTClient(ctx, ts),
		computeBase: "https://compute.googleapis.com/compute/v1",
		sqlBase:     "https://sqladmin.googleapis.com/v1",
	}
}

// GCPCredentials returns credentials from a service account key, or the default chain
// (GKE workload identity, the metadata server, GOOGLE_APPLICATION_CREDENTIALS) without
// one. Only service account keys are accepted: other credential types in a key file can
// make the library fetch from URLs the file chooses.
func GCPCredentials(ctx context.Context, serviceAccountJSON []byte) (*google.Credentials, error) {
	if len(serviceAccountJSON) > 0 {
		return google.CredentialsFromJSONWithType(ctx, serviceAccountJSON, google.ServiceAccount, gcpScope)
	}
	return google.FindDefaultCredentials(ctx, gcpScope)
}

// Name implements ExternalProvider.
func (p *GCPProvider) Name() string { return ProviderGCP }

// ResourceTypes implements CloudProvider.
func (p *GCPProvider) ResourceTypes() []string { return GCPResourceTypes }

// ValidateConnectivity reads the project through the Compute API.
func (p *GCPProvider) ValidateConnectivity(ctx context.Context) error {
	if p.project == "" {
		return errors.New("no Google Cloud project ID is configured")
	}
	if err := p.rest.do(ctx, http.MethodGet, fmt.Sprintf("%s/projects/%s", p.computeBase, url.PathEscape(p.project)), nil, nil); err != nil {
		return fmt.Errorf("could not read project %s: %w", p.project, err)
	}
	return nil
}

type gceInstance struct {
	Name   string            `json:"name"`
	Zone   string            `json:"zone"` // URL ending in /zones/<zone>
	Status string            `json:"status"`
	Labels map[string]string `json:"labels"`
}

type sqlInstance struct {
	Name     string `json:"name"`
	Region   string `json:"region"`
	State    string `json:"state"`
	Settings struct {
		ActivationPolicy string            `json:"activationPolicy"`
		UserLabels       map[string]string `json:"userLabels"`
	} `json:"settings"`
}

// Discover implements ExternalProvider.
func (p *GCPProvider) Discover(ctx context.Context, resourceType string, labels map[string]string) ([]finopsv1.ExternalTarget, error) {
	switch resourceType {
	case GCPTypeGCE:
		return p.discoverGCE(ctx, labels)
	case GCPTypeCloudSQL:
		return p.discoverCloudSQL(ctx, labels)
	}
	return nil, fmt.Errorf("discovery unsupported for Google Cloud type %q", resourceType)
}

func (p *GCPProvider) discoverGCE(ctx context.Context, labels map[string]string) ([]finopsv1.ExternalTarget, error) {
	var targets []finopsv1.ExternalTarget
	token := ""
	for {
		u := fmt.Sprintf("%s/projects/%s/aggregated/instances?maxResults=500", p.computeBase, url.PathEscape(p.project))
		if token != "" {
			u += "&pageToken=" + url.QueryEscape(token)
		}
		var page struct {
			Items map[string]struct {
				Instances []gceInstance `json:"instances"`
			} `json:"items"`
			NextPageToken string `json:"nextPageToken"`
		}
		if err := p.rest.do(ctx, http.MethodGet, u, nil, &page); err != nil {
			return nil, fmt.Errorf("list Compute Engine instances: %w", err)
		}
		for _, scope := range page.Items {
			for _, in := range scope.Instances {
				if !matchLabels(in.Labels, labels) {
					continue
				}
				zone := path.Base(in.Zone)
				targets = append(targets, finopsv1.ExternalTarget{
					Provider: ProviderGCP, Type: GCPTypeGCE, Identifier: zone + "/" + in.Name, Region: zone,
					Status: strings.ToLower(in.Status), Name: in.Name,
				})
			}
		}
		if token = page.NextPageToken; token == "" {
			return targets, nil
		}
	}
}

func (p *GCPProvider) discoverCloudSQL(ctx context.Context, labels map[string]string) ([]finopsv1.ExternalTarget, error) {
	var targets []finopsv1.ExternalTarget
	token := ""
	for {
		u := fmt.Sprintf("%s/projects/%s/instances", p.sqlBase, url.PathEscape(p.project))
		if token != "" {
			u += "?pageToken=" + url.QueryEscape(token)
		}
		var page struct {
			Items         []sqlInstance `json:"items"`
			NextPageToken string        `json:"nextPageToken"`
		}
		if err := p.rest.do(ctx, http.MethodGet, u, nil, &page); err != nil {
			return nil, fmt.Errorf("list Cloud SQL instances: %w", err)
		}
		for _, in := range page.Items {
			if !matchLabels(in.Settings.UserLabels, labels) {
				continue
			}
			status := strings.ToLower(in.State)
			if in.Settings.ActivationPolicy == sqlPolicyNever {
				status = stateStopped
			}
			targets = append(targets, finopsv1.ExternalTarget{
				Provider: ProviderGCP, Type: GCPTypeCloudSQL, Identifier: in.Name, Region: in.Region, Status: status, Name: in.Name,
			})
		}
		if token = page.NextPageToken; token == "" {
			return targets, nil
		}
	}
}

// instanceURL returns the Compute API URL of a "<zone>/<name>" target.
func (p *GCPProvider) instanceURL(id string) (string, error) {
	if !gcpInstanceID.MatchString(id) {
		return "", fmt.Errorf("%q is not a Compute Engine instance (expected <zone>/<name>)", id)
	}
	zone, name, _ := strings.Cut(id, "/")
	return fmt.Sprintf("%s/projects/%s/zones/%s/instances/%s", p.computeBase, url.PathEscape(p.project), zone, name), nil
}

func (p *GCPProvider) sqlURL(name string) (string, error) {
	if !gcpSQLName.MatchString(name) {
		return "", fmt.Errorf("%q is not a Cloud SQL instance name", name)
	}
	return fmt.Sprintf("%s/projects/%s/instances/%s", p.sqlBase, url.PathEscape(p.project), name), nil
}

// Scale implements ExternalProvider.
func (p *GCPProvider) Scale(ctx context.Context, target finopsv1.ExternalTarget, active bool) error {
	l := log.FromContext(ctx).WithValues("provider", ProviderGCP, "target", target.Identifier, "active", active)
	switch target.Type {
	case GCPTypeGCE:
		u, err := p.instanceURL(target.Identifier)
		if err != nil {
			return err
		}
		action := "stop"
		if active {
			action = "start"
		}
		l.Info("Calling Compute Engine action", "action", action)
		return p.rest.do(ctx, http.MethodPost, u+"/"+action, nil, nil)
	case GCPTypeCloudSQL:
		u, err := p.sqlURL(target.Identifier)
		if err != nil {
			return err
		}
		// Cloud SQL has no start/stop calls: an instance runs while its activation policy
		// is ALWAYS and stops when it is NEVER.
		policy := sqlPolicyNever
		if active {
			policy = sqlPolicyAlways
		}
		l.Info("Setting Cloud SQL activation policy", "policy", policy)
		err = p.rest.do(ctx, http.MethodPatch, u, map[string]any{"settings": map[string]string{"activationPolicy": policy}}, nil)
		var api *apiError
		if errors.As(err, &api) && api.Status == http.StatusConflict {
			// Another operation is running on the instance; the next reconcile retries.
			return nil
		}
		return err
	}
	return fmt.Errorf("unsupported Google Cloud resource type: %s", target.Type)
}

// IsReady implements ExternalProvider.
func (p *GCPProvider) IsReady(ctx context.Context, target finopsv1.ExternalTarget, active bool) (bool, error) {
	switch target.Type {
	case GCPTypeGCE:
		u, err := p.instanceURL(target.Identifier)
		if err != nil {
			return false, err
		}
		var in gceInstance
		if err := p.rest.do(ctx, http.MethodGet, u, nil, &in); err != nil {
			return false, err
		}
		if active {
			return in.Status == "RUNNING", nil
		}
		return in.Status == "TERMINATED" || in.Status == "STOPPED", nil
	case GCPTypeCloudSQL:
		u, err := p.sqlURL(target.Identifier)
		if err != nil {
			return false, err
		}
		var in sqlInstance
		if err := p.rest.do(ctx, http.MethodGet, u, nil, &in); err != nil {
			return false, err
		}
		if active {
			return in.Settings.ActivationPolicy == sqlPolicyAlways && in.State == "RUNNABLE", nil
		}
		return in.Settings.ActivationPolicy == sqlPolicyNever, nil
	}
	return false, fmt.Errorf("unsupported Google Cloud resource type: %s", target.Type)
}
