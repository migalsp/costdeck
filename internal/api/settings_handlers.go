package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"maps"
	"net/http"
	"regexp"
	"strings"
	"time"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/util/retry"
	"sigs.k8s.io/controller-runtime/pkg/client"

	finopsv1 "github.com/migalsp/costdeck-operator/api/v1"
	"github.com/migalsp/costdeck-operator/internal/auth"
	"github.com/migalsp/costdeck-operator/internal/config"
	"github.com/migalsp/costdeck-operator/internal/finops"
	"github.com/migalsp/costdeck-operator/internal/metrics"
	"github.com/migalsp/costdeck-operator/internal/pricing"
	"github.com/migalsp/costdeck-operator/internal/scaling"
	"github.com/migalsp/costdeck-operator/internal/webex"
)

// ─── Settings API Data Types ─────────────────────────────────────────────────

// SettingsResponse is the full settings response returned to the UI.
// Credentials are always masked.
type SettingsResponse struct {
	Providers    ProvidersSettingsResponse    `json:"providers"`
	Integrations IntegrationsSettingsResponse `json:"integrations"`
	Features     *FeaturesSettingsResponse    `json:"features,omitempty"`
	Auth         AuthSettingsResponse         `json:"auth"`
	Pricing      PricingSettingsResponse      `json:"pricing"`
	Reports      finopsv1.ReportsConfig       `json:"reports"`
	Budgets      []finopsv1.Budget            `json:"budgets"`
	Alerts       finopsv1.AlertsConfig        `json:"alerts"`
	Billing      BillingSettingsResponse      `json:"billing"`
}

// PricingSettingsResponse shows the configured custom rates and the rates in effect.
type PricingSettingsResponse struct {
	CPUCoreHour       string        `json:"cpuCoreHour,omitempty"`
	MemoryGBHour      string        `json:"memoryGiBHour,omitempty"`
	StorageGiBMonth   string        `json:"storageGiBMonth,omitempty"`
	LoadBalancerMonth string        `json:"loadBalancerMonth,omitempty"`
	Currency          string        `json:"currency,omitempty"`
	Effective         pricing.Rates `json:"effective"`
}

// AuthSettingsResponse describes sign-in settings. The client secret is never returned.
type AuthSettingsResponse struct {
	DisableLocalLogin bool                   `json:"disableLocalLogin"`
	Entra             *EntraSettingsResponse `json:"entra,omitempty"`
}

// EntraSettingsResponse is the masked Entra ID configuration.
type EntraSettingsResponse struct {
	Enabled          bool              `json:"enabled"`
	TenantID         string            `json:"tenantId,omitempty"`
	ClientID         string            `json:"clientId,omitempty"`
	RedirectURL      string            `json:"redirectUrl,omitempty"`
	AuthorityHost    string            `json:"authorityHost,omitempty"`
	DefaultRole      string            `json:"defaultRole,omitempty"`
	AutoProvision    bool              `json:"autoProvision"`
	GroupRoleMapping map[string]string `json:"groupRoleMapping,omitempty"`
	SkipSSLVerify    bool              `json:"skipSslVerify"`
	HasClientSecret  bool              `json:"hasClientSecret"`
}

type FeaturesSettingsResponse struct {
	CloudPricingAPI bool `json:"cloudPricingApi"`
}

type ProvidersSettingsResponse struct {
	AWS   *AWSSettingsResponse   `json:"aws,omitempty"`
	Azure *AzureSettingsResponse `json:"azure,omitempty"`
	GCP   *GCPSettingsResponse   `json:"gcp,omitempty"`
}

type AWSSettingsResponse struct {
	Enabled        bool                     `json:"enabled"`
	Region         string                   `json:"region"`
	HasCredentials bool                     `json:"hasCredentials"`
	DiscoveryTags  map[string]string        `json:"discoveryTags,omitempty"`
	ResourceTypes  []string                 `json:"resourceTypes,omitempty"`
	Status         *finopsv1.ProviderStatus `json:"status,omitempty"`
}

type AzureSettingsResponse struct {
	Enabled        bool                     `json:"enabled"`
	SubscriptionID string                   `json:"subscriptionId,omitempty"`
	TenantID       string                   `json:"tenantId,omitempty"`
	DiscoveryTags  map[string]string        `json:"discoveryTags,omitempty"`
	ResourceTypes  []string                 `json:"resourceTypes,omitempty"`
	HasCredentials bool                     `json:"hasCredentials"`
	Status         *finopsv1.ProviderStatus `json:"status,omitempty"`
}

type GCPSettingsResponse struct {
	Enabled         bool                     `json:"enabled"`
	ProjectID       string                   `json:"projectId,omitempty"`
	DiscoveryLabels map[string]string        `json:"discoveryLabels,omitempty"`
	ResourceTypes   []string                 `json:"resourceTypes,omitempty"`
	HasCredentials  bool                     `json:"hasCredentials"`
	Status          *finopsv1.ProviderStatus `json:"status,omitempty"`
}

type IntegrationsSettingsResponse struct {
	AI              *AISettingsResponse              `json:"ai,omitempty"`
	Messenger       *MessengerSettingsResponse       `json:"messenger,omitempty"`
	VictoriaMetrics *VictoriaMetricsSettingsResponse `json:"victoriaMetrics,omitempty"`
	MCP             MCPSettingsResponse              `json:"mcp"`
}

// MCPSettingsResponse describes the MCP endpoint.
type MCPSettingsResponse struct {
	Enabled bool `json:"enabled"`
	// Path is where MCP clients connect, on the dashboard's own host and port.
	Path string `json:"path"`
}

type AISettingsResponse struct {
	Enabled        bool   `json:"enabled"`
	Provider       string `json:"provider,omitempty"`
	Model          string `json:"model,omitempty"`
	BaseURL        string `json:"baseUrl,omitempty"`
	SkipSSLVerify  bool   `json:"skipSslVerify"`
	HasCredentials bool   `json:"hasCredentials"`
}

type MessengerSettingsResponse struct {
	Webex *WebexSettingsResponse `json:"webex,omitempty"`
}

type WebexSettingsResponse struct {
	Enabled           bool                     `json:"enabled"`
	NotifyTransitions bool                     `json:"notifyTransitions"`
	RoomID            string                   `json:"roomId,omitempty"`
	HasCredentials    bool                     `json:"hasCredentials"`
	Status            *finopsv1.ProviderStatus `json:"status,omitempty"`
}

type VictoriaMetricsSettingsResponse struct {
	Enabled        bool                     `json:"enabled"`
	Endpoint       string                   `json:"endpoint,omitempty"`
	LabelSelector  string                   `json:"labelSelector,omitempty"`
	SkipSSLVerify  bool                     `json:"skipSslVerify"`
	RetentionDays  int                      `json:"retentionDays"`
	HasCredentials bool                     `json:"hasCredentials"`
	Status         *finopsv1.ProviderStatus `json:"status,omitempty"`
}

// SettingsUpdateRequest is the payload for updating settings.
// Credentials are provided here in plaintext and then stored in K8s Secrets.
type SettingsUpdateRequest struct {
	Providers    *ProvidersUpdateRequest    `json:"providers,omitempty"`
	Integrations *IntegrationsUpdateRequest `json:"integrations,omitempty"`
	Features     *FeaturesUpdateRequest     `json:"features,omitempty"`
	Auth         *AuthUpdateRequest         `json:"auth,omitempty"`
	Pricing      *PricingUpdateRequest      `json:"pricing,omitempty"`
	Reports      *ReportsUpdateRequest      `json:"reports,omitempty"`
	// Budgets replaces the whole list when present.
	Budgets *[]finopsv1.Budget     `json:"budgets,omitempty"`
	Alerts  *finopsv1.AlertsConfig `json:"alerts,omitempty"`
	// Billing replaces the reconciliation setup when present.
	Billing *finopsv1.BillingConfig `json:"billing,omitempty"`
}

// BillingSettingsResponse is the reconciliation setup and its last result.
type BillingSettingsResponse struct {
	finopsv1.BillingConfig `json:",inline"`
	Status                 *finopsv1.BillingStatus `json:"status,omitempty"`
}

// ReportsUpdateRequest replaces the digest schedule.
type ReportsUpdateRequest struct {
	Digest *finopsv1.DigestSchedule `json:"digest,omitempty"`
}

// PricingUpdateRequest sets custom rates; empty strings clear them.
type PricingUpdateRequest struct {
	CPUCoreHour       *string `json:"cpuCoreHour,omitempty"`
	MemoryGBHour      *string `json:"memoryGiBHour,omitempty"`
	StorageGiBMonth   *string `json:"storageGiBMonth,omitempty"`
	LoadBalancerMonth *string `json:"loadBalancerMonth,omitempty"`
	Currency          *string `json:"currency,omitempty"`
}

// AuthUpdateRequest changes sign-in settings. Nil fields stay unchanged.
type AuthUpdateRequest struct {
	DisableLocalLogin *bool               `json:"disableLocalLogin,omitempty"`
	Entra             *EntraUpdateRequest `json:"entra,omitempty"`
}

// EntraUpdateRequest changes Microsoft Entra ID SSO settings. GroupRoleMapping replaces the
// whole mapping when present; an empty object clears it.
type EntraUpdateRequest struct {
	Enabled          *bool             `json:"enabled,omitempty"`
	TenantID         *string           `json:"tenantId,omitempty"`
	ClientID         *string           `json:"clientId,omitempty"`
	ClientSecret     string            `json:"clientSecret,omitempty"`
	RedirectURL      *string           `json:"redirectUrl,omitempty"`
	AuthorityHost    *string           `json:"authorityHost,omitempty"`
	DefaultRole      *string           `json:"defaultRole,omitempty"`
	AutoProvision    *bool             `json:"autoProvision,omitempty"`
	GroupRoleMapping map[string]string `json:"groupRoleMapping,omitempty"`
	SkipSSLVerify    *bool             `json:"skipSslVerify,omitempty"`
}

type FeaturesUpdateRequest struct {
	CloudPricingAPI *bool `json:"cloudPricingApi,omitempty"`
}

type ProvidersUpdateRequest struct {
	AWS   *AWSUpdateRequest   `json:"aws,omitempty"`
	Azure *AzureUpdateRequest `json:"azure,omitempty"`
	GCP   *GCPUpdateRequest   `json:"gcp,omitempty"`
}

type AWSUpdateRequest struct {
	Enabled         *bool             `json:"enabled,omitempty"`
	Region          string            `json:"region,omitempty"`
	AccessKeyID     string            `json:"accessKeyId,omitempty"`
	SecretAccessKey string            `json:"secretAccessKey,omitempty"`
	DiscoveryTags   map[string]string `json:"discoveryTags,omitempty"`
	ResourceTypes   []string          `json:"resourceTypes,omitempty"`
}

type AzureUpdateRequest struct {
	Enabled        *bool             `json:"enabled,omitempty"`
	SubscriptionID string            `json:"subscriptionId,omitempty"`
	TenantID       string            `json:"tenantId,omitempty"`
	ClientID       string            `json:"clientId,omitempty"`
	ClientSecret   string            `json:"clientSecret,omitempty"`
	DiscoveryTags  map[string]string `json:"discoveryTags,omitempty"`
	ResourceTypes  []string          `json:"resourceTypes,omitempty"`
}

type GCPUpdateRequest struct {
	Enabled            *bool             `json:"enabled,omitempty"`
	ProjectID          string            `json:"projectId,omitempty"`
	ServiceAccountJSON string            `json:"serviceAccountJson,omitempty"`
	DiscoveryLabels    map[string]string `json:"discoveryLabels,omitempty"`
	ResourceTypes      []string          `json:"resourceTypes,omitempty"`
}

type IntegrationsUpdateRequest struct {
	AI              *AIUpdateRequest              `json:"ai,omitempty"`
	Messenger       *MessengerUpdateRequest       `json:"messenger,omitempty"`
	VictoriaMetrics *VictoriaMetricsUpdateRequest `json:"victoriaMetrics,omitempty"`
	MCP             *MCPUpdateRequest             `json:"mcp,omitempty"`
}

// MCPUpdateRequest toggles the MCP endpoint.
type MCPUpdateRequest struct {
	Enabled *bool `json:"enabled,omitempty"`
}

type AIUpdateRequest struct {
	Enabled       *bool  `json:"enabled,omitempty"`
	Provider      string `json:"provider,omitempty"`
	Model         string `json:"model,omitempty"`
	BaseURL       string `json:"baseUrl,omitempty"`
	APIKey        string `json:"apiKey,omitempty"`
	SkipSSLVerify *bool  `json:"skipSslVerify,omitempty"`
}

type MessengerUpdateRequest struct {
	Webex *WebexUpdateRequest `json:"webex,omitempty"`
}

type WebexUpdateRequest struct {
	Enabled           *bool   `json:"enabled,omitempty"`
	NotifyTransitions *bool   `json:"notifyTransitions,omitempty"`
	RoomID            *string `json:"roomId,omitempty"`
	BotToken          string  `json:"botToken,omitempty"`
	WebhookSecret     *string `json:"webhookSecret,omitempty"`
}

type VictoriaMetricsUpdateRequest struct {
	Enabled       *bool   `json:"enabled,omitempty"`
	Endpoint      string  `json:"endpoint,omitempty"`
	LabelSelector *string `json:"labelSelector,omitempty"`
	SkipSSLVerify *bool   `json:"skipSslVerify,omitempty"`
	RetentionDays *int    `json:"retentionDays,omitempty"`
	BearerToken   string  `json:"bearerToken,omitempty"`
	Username      string  `json:"username,omitempty"`
	Password      string  `json:"password,omitempty"`
	CACert        string  `json:"caCert,omitempty"`
}

// credentials returns the Secret data carried by the request, or nil when it carries none.
func (r *VictoriaMetricsUpdateRequest) credentials() map[string][]byte {
	data := map[string][]byte{}
	if r.BearerToken != "" {
		data[metrics.SecretKeyBearerToken] = []byte(r.BearerToken)
	}
	if r.Username != "" {
		data[metrics.SecretKeyUsername] = []byte(r.Username)
		data[metrics.SecretKeyPassword] = []byte(r.Password)
	}
	if r.CACert != "" {
		data[metrics.SecretKeyCACert] = []byte(r.CACert)
	}
	if len(data) == 0 {
		return nil
	}
	return data
}

// ─── Handlers ────────────────────────────────────────────────────────────────

// ─── GET /api/settings ──────────────────────────────────────────────────────

func (s *Server) handleGetSettings(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	cfg, err := config.Get(ctx, s.Client)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, s.buildSettingsResponse(ctx, cfg))
}

func (s *Server) buildSettingsResponse(ctx context.Context, cfg *finopsv1.CostDeckConfig) SettingsResponse {
	resp := SettingsResponse{}

	// AWS
	if cfg.Spec.Providers.AWS != nil {
		hasCreds := cfg.Spec.Providers.AWS.SecretRef != "" && s.secretExists(ctx, cfg.Spec.Providers.AWS.SecretRef, cfg.Namespace)
		resp.Providers.AWS = &AWSSettingsResponse{
			Enabled:        cfg.Spec.Providers.AWS.Enabled,
			Region:         cfg.Spec.Providers.AWS.Region,
			HasCredentials: hasCreds,
			DiscoveryTags:  cfg.Spec.Providers.AWS.DiscoveryTags,
			ResourceTypes:  cfg.Spec.Providers.AWS.ResourceTypes,
			Status:         cfg.Status.AWS,
		}
	}

	// Azure (stub)
	if cfg.Spec.Providers.Azure != nil {
		hasCreds := cfg.Spec.Providers.Azure.SecretRef != "" && s.secretExists(ctx, cfg.Spec.Providers.Azure.SecretRef, cfg.Namespace)
		resp.Providers.Azure = &AzureSettingsResponse{
			Enabled:        cfg.Spec.Providers.Azure.Enabled,
			SubscriptionID: cfg.Spec.Providers.Azure.SubscriptionID,
			TenantID:       cfg.Spec.Providers.Azure.TenantID,
			DiscoveryTags:  cfg.Spec.Providers.Azure.DiscoveryTags,
			ResourceTypes:  cfg.Spec.Providers.Azure.ResourceTypes,
			HasCredentials: hasCreds,
			Status:         cfg.Status.Azure,
		}
	}

	// GCP (stub)
	if cfg.Spec.Providers.GCP != nil {
		hasCreds := cfg.Spec.Providers.GCP.SecretRef != "" && s.secretExists(ctx, cfg.Spec.Providers.GCP.SecretRef, cfg.Namespace)
		resp.Providers.GCP = &GCPSettingsResponse{
			Enabled:         cfg.Spec.Providers.GCP.Enabled,
			ProjectID:       cfg.Spec.Providers.GCP.ProjectID,
			DiscoveryLabels: cfg.Spec.Providers.GCP.DiscoveryLabels,
			ResourceTypes:   cfg.Spec.Providers.GCP.ResourceTypes,
			HasCredentials:  hasCreds,
			Status:          cfg.Status.GCP,
		}
	}

	// AI (stub)
	if cfg.Spec.Integrations.AI != nil {
		resp.Integrations.AI = &AISettingsResponse{
			Enabled:        cfg.Spec.Integrations.AI.Enabled,
			Provider:       cfg.Spec.Integrations.AI.Provider,
			Model:          cfg.Spec.Integrations.AI.Model,
			BaseURL:        cfg.Spec.Integrations.AI.BaseURL,
			SkipSSLVerify:  cfg.Spec.Integrations.AI.SkipSSLVerify,
			HasCredentials: cfg.Spec.Integrations.AI.SecretRef != "",
		}
	}

	// Messenger / Webex
	if cfg.Spec.Integrations.Messenger != nil && cfg.Spec.Integrations.Messenger.Webex != nil {
		resp.Integrations.Messenger = &MessengerSettingsResponse{
			Webex: &WebexSettingsResponse{
				Enabled:           cfg.Spec.Integrations.Messenger.Webex.Enabled,
				NotifyTransitions: cfg.Spec.Integrations.Messenger.Webex.NotifyTransitions,
				RoomID:            cfg.Spec.Integrations.Messenger.Webex.RoomID,
				HasCredentials:    cfg.Spec.Integrations.Messenger.Webex.SecretRef != "",
				Status:            cfg.Status.Webex,
			},
		}
	}

	// VictoriaMetrics
	if cfg.Spec.Integrations.VictoriaMetrics != nil {
		hasCreds := cfg.Spec.Integrations.VictoriaMetrics.SecretRef != "" && s.secretExists(ctx, cfg.Spec.Integrations.VictoriaMetrics.SecretRef, cfg.Namespace)
		retentionDays := cfg.Spec.Integrations.VictoriaMetrics.RetentionDays
		if retentionDays == 0 {
			retentionDays = 7
		}
		resp.Integrations.VictoriaMetrics = &VictoriaMetricsSettingsResponse{
			Enabled:        cfg.Spec.Integrations.VictoriaMetrics.Enabled,
			Endpoint:       cfg.Spec.Integrations.VictoriaMetrics.Endpoint,
			LabelSelector:  cfg.Spec.Integrations.VictoriaMetrics.LabelSelector,
			SkipSSLVerify:  cfg.Spec.Integrations.VictoriaMetrics.SkipSSLVerify,
			RetentionDays:  retentionDays,
			HasCredentials: hasCreds,
			Status:         cfg.Status.VictoriaMetrics,
		}
	}

	// MCP
	resp.Integrations.MCP = MCPSettingsResponse{Path: "/mcp"}
	if m := cfg.Spec.Integrations.MCP; m != nil {
		resp.Integrations.MCP.Enabled = m.Enabled
	}

	// Features
	resp.Features = &FeaturesSettingsResponse{
		CloudPricingAPI: cfg.Spec.Features.CloudPricingAPI,
	}

	// Pricing
	resp.Pricing = PricingSettingsResponse{
		CPUCoreHour:       cfg.Spec.Pricing.CPUCoreHour,
		MemoryGBHour:      cfg.Spec.Pricing.MemoryGBHour,
		StorageGiBMonth:   cfg.Spec.Pricing.StorageGiBMonth,
		LoadBalancerMonth: cfg.Spec.Pricing.LoadBalancerMonth,
		Currency:          cfg.Spec.Pricing.Currency,
		Effective:         s.costRates(ctx),
	}

	resp.Reports = cfg.Spec.Reports
	resp.Budgets = cfg.Spec.Budgets
	if resp.Budgets == nil {
		resp.Budgets = []finopsv1.Budget{}
	}
	resp.Alerts = cfg.Spec.Alerts
	resp.Billing = BillingSettingsResponse{BillingConfig: cfg.Spec.Billing, Status: cfg.Status.Billing}

	// Sign-in
	resp.Auth.DisableLocalLogin = cfg.Spec.Auth.DisableLocalLogin
	if e := cfg.Spec.Auth.Entra; e != nil {
		resp.Auth.Entra = &EntraSettingsResponse{
			Enabled:          e.Enabled,
			TenantID:         e.TenantID,
			ClientID:         e.ClientID,
			RedirectURL:      e.RedirectURL,
			AuthorityHost:    e.AuthorityHost,
			DefaultRole:      e.DefaultRole,
			AutoProvision:    e.AutoProvision == nil || *e.AutoProvision,
			GroupRoleMapping: e.GroupRoleMapping,
			SkipSSLVerify:    e.SkipSSLVerify,
			HasClientSecret:  e.ClientSecretRef != "" && s.secretExists(ctx, e.ClientSecretRef, cfg.Namespace),
		}
	}

	return resp
}

// ─── PUT /api/settings ──────────────────────────────────────────────────────

func (s *Server) handleUpdateSettings(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()

	var req SettingsUpdateRequest
	if err := decodeJSON(w, r, &req); err != nil {
		writeErrorf(w, http.StatusBadRequest, "Invalid request body: %v", err)
		return
	}

	appliers := []func(context.Context, *finopsv1.CostDeckConfig, *SettingsUpdateRequest) error{
		s.applyAWSSettings, s.applyAzureSettings, s.applyGCPSettings, s.applyAISettings,
		s.applyWebexSettings, s.applyVictoriaMetricsSettings, applyMCPSettings, applyFeatureSettings, applyPricingSettings, applyReportSettings, applyBudgetSettings, applyBillingSettings, s.applyAuthSettings,
	}

	var cfg *finopsv1.CostDeckConfig
	// The controller writes the status every few minutes; retrying on a conflict keeps a
	// save from failing because of that.
	err := retry.RetryOnConflict(retry.DefaultRetry, func() error {
		var err error
		if cfg, err = s.getOrCreateDefaultConfig(ctx); err != nil {
			return err
		}
		for _, apply := range appliers {
			if err := apply(ctx, cfg, &req); err != nil {
				return err
			}
		}
		return s.Client.Update(ctx, cfg)
	})
	var bad badRequest
	switch {
	case errors.As(err, &bad):
		writeError(w, http.StatusBadRequest, bad.Error())
		return
	case err != nil:
		writeK8sError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, s.buildSettingsResponse(ctx, cfg))
}

// badRequest marks a validation failure of a settings section.
type badRequest struct{ error }

func badRequestf(format string, args ...any) error { return badRequest{fmt.Errorf(format, args...)} }

func (s *Server) applyAWSSettings(ctx context.Context, cfg *finopsv1.CostDeckConfig, req *SettingsUpdateRequest) error {
	if req.Providers == nil || req.Providers.AWS == nil {
		return nil
	}
	awsReq := req.Providers.AWS
	if cfg.Spec.Providers.AWS == nil {
		cfg.Spec.Providers.AWS = &finopsv1.AWSProviderConfig{}
	}
	aws := cfg.Spec.Providers.AWS
	if awsReq.Enabled != nil {
		aws.Enabled = *awsReq.Enabled
	}
	if awsReq.Region != "" {
		aws.Region = awsReq.Region
	}
	if awsReq.DiscoveryTags != nil {
		aws.DiscoveryTags = awsReq.DiscoveryTags
	}
	if awsReq.ResourceTypes != nil {
		aws.ResourceTypes = awsReq.ResourceTypes
	}
	if awsReq.AccessKeyID == "" || awsReq.SecretAccessKey == "" {
		return nil
	}
	data := map[string][]byte{
		"AWS_ACCESS_KEY_ID":     []byte(awsReq.AccessKeyID),
		"AWS_SECRET_ACCESS_KEY": []byte(awsReq.SecretAccessKey),
	}
	if aws.Region != "" {
		data["AWS_REGION"] = []byte(aws.Region)
	}
	return s.storeCredentials(ctx, "costdeck-aws-credentials", data, &aws.SecretRef)
}

func (s *Server) applyAzureSettings(ctx context.Context, cfg *finopsv1.CostDeckConfig, req *SettingsUpdateRequest) error {
	if req.Providers == nil || req.Providers.Azure == nil {
		return nil
	}
	azureReq := req.Providers.Azure
	if cfg.Spec.Providers.Azure == nil {
		cfg.Spec.Providers.Azure = &finopsv1.AzureProviderConfig{}
	}
	azure := cfg.Spec.Providers.Azure
	if azureReq.Enabled != nil {
		azure.Enabled = *azureReq.Enabled
	}
	if azureReq.SubscriptionID != "" {
		azure.SubscriptionID = azureReq.SubscriptionID
	}
	if azureReq.TenantID != "" {
		azure.TenantID = azureReq.TenantID
	}
	if azureReq.DiscoveryTags != nil {
		azure.DiscoveryTags = azureReq.DiscoveryTags
	}
	if azureReq.ResourceTypes != nil {
		azure.ResourceTypes = azureReq.ResourceTypes
	}
	if azure.SubscriptionID != "" && !subscriptionID.MatchString(azure.SubscriptionID) {
		return badRequestf("the Azure subscription ID must be a GUID")
	}
	if azureReq.ClientID == "" || azureReq.ClientSecret == "" {
		return nil
	}
	if azure.TenantID == "" {
		return badRequestf("a tenant ID is required with a client secret")
	}
	return s.storeCredentials(ctx, "costdeck-azure-credentials", map[string][]byte{
		"AZURE_CLIENT_ID":     []byte(azureReq.ClientID),
		"AZURE_CLIENT_SECRET": []byte(azureReq.ClientSecret),
		"AZURE_TENANT_ID":     []byte(azureReq.TenantID),
	}, &azure.SecretRef)
}

func (s *Server) applyGCPSettings(ctx context.Context, cfg *finopsv1.CostDeckConfig, req *SettingsUpdateRequest) error {
	if req.Providers == nil || req.Providers.GCP == nil {
		return nil
	}
	gcpReq := req.Providers.GCP
	if cfg.Spec.Providers.GCP == nil {
		cfg.Spec.Providers.GCP = &finopsv1.GCPProviderConfig{}
	}
	gcp := cfg.Spec.Providers.GCP
	if gcpReq.Enabled != nil {
		gcp.Enabled = *gcpReq.Enabled
	}
	if gcpReq.ProjectID != "" {
		gcp.ProjectID = gcpReq.ProjectID
	}
	if gcpReq.DiscoveryLabels != nil {
		gcp.DiscoveryLabels = gcpReq.DiscoveryLabels
	}
	if gcpReq.ResourceTypes != nil {
		gcp.ResourceTypes = gcpReq.ResourceTypes
	}
	if gcpReq.ServiceAccountJSON == "" {
		return nil
	}
	if err := validServiceAccountKey(gcpReq.ServiceAccountJSON); err != nil {
		return badRequest{err}
	}
	return s.storeCredentials(ctx, "costdeck-gcp-credentials", map[string][]byte{
		"credentials.json": []byte(gcpReq.ServiceAccountJSON),
	}, &gcp.SecretRef)
}

func (s *Server) applyAISettings(ctx context.Context, cfg *finopsv1.CostDeckConfig, req *SettingsUpdateRequest) error {
	if req.Integrations == nil || req.Integrations.AI == nil {
		return nil
	}
	aiReq := req.Integrations.AI
	if cfg.Spec.Integrations.AI == nil {
		cfg.Spec.Integrations.AI = &finopsv1.AIIntegrationConfig{}
	}
	ai := cfg.Spec.Integrations.AI
	if aiReq.Enabled != nil {
		ai.Enabled = *aiReq.Enabled
	}
	if aiReq.Provider != "" {
		ai.Provider = aiReq.Provider
	}
	if aiReq.Model != "" {
		ai.Model = aiReq.Model
	}
	ai.BaseURL = aiReq.BaseURL // May be cleared explicitly.
	if aiReq.SkipSSLVerify != nil {
		ai.SkipSSLVerify = *aiReq.SkipSSLVerify
	}
	if aiReq.APIKey == "" {
		return nil
	}
	return s.storeCredentials(ctx, "costdeck-ai-credentials", map[string][]byte{"API_KEY": []byte(aiReq.APIKey)}, &ai.SecretRef)
}

func (s *Server) applyWebexSettings(ctx context.Context, cfg *finopsv1.CostDeckConfig, req *SettingsUpdateRequest) error {
	if req.Integrations == nil || req.Integrations.Messenger == nil || req.Integrations.Messenger.Webex == nil {
		return nil
	}
	wxReq := req.Integrations.Messenger.Webex
	if cfg.Spec.Integrations.Messenger == nil {
		cfg.Spec.Integrations.Messenger = &finopsv1.MessengerIntegrationConfig{}
	}
	if cfg.Spec.Integrations.Messenger.Webex == nil {
		cfg.Spec.Integrations.Messenger.Webex = &finopsv1.WebexConfig{}
	}
	wx := cfg.Spec.Integrations.Messenger.Webex
	if wxReq.Enabled != nil {
		wx.Enabled = *wxReq.Enabled
	}
	if wxReq.RoomID != nil {
		wx.RoomID = strings.TrimSpace(*wxReq.RoomID)
	}
	if wxReq.NotifyTransitions != nil {
		wx.NotifyTransitions = *wxReq.NotifyTransitions
	}
	if wx.NotifyTransitions && wx.RoomID == "" {
		return badRequestf("transition notifications need a Webex space ID to post to")
	}
	if wxReq.BotToken == "" && wxReq.WebhookSecret == nil {
		return nil
	}
	const secretName = "costdeck-webex-credentials"
	data, err := s.secretDataOrEmpty(ctx, secretName)
	if err != nil {
		return err
	}
	if wxReq.BotToken != "" {
		data[webex.SecretKeyBotToken] = []byte(wxReq.BotToken)
	}
	if wxReq.WebhookSecret != nil {
		if *wxReq.WebhookSecret == "" {
			delete(data, webex.SecretKeyWebhookSecret)
		} else {
			data[webex.SecretKeyWebhookSecret] = []byte(*wxReq.WebhookSecret)
		}
	}
	return s.storeCredentials(ctx, secretName, data, &wx.SecretRef)
}

func (s *Server) applyVictoriaMetricsSettings(ctx context.Context, cfg *finopsv1.CostDeckConfig, req *SettingsUpdateRequest) error {
	if req.Integrations == nil || req.Integrations.VictoriaMetrics == nil {
		return nil
	}
	vmReq := req.Integrations.VictoriaMetrics
	if cfg.Spec.Integrations.VictoriaMetrics == nil {
		cfg.Spec.Integrations.VictoriaMetrics = &finopsv1.VictoriaMetricsConfig{}
	}
	vm := cfg.Spec.Integrations.VictoriaMetrics
	if vmReq.Enabled != nil {
		vm.Enabled = *vmReq.Enabled
	}
	if vmReq.Endpoint != "" {
		if _, err := metrics.NormalizeEndpoint(vmReq.Endpoint); err != nil {
			return badRequest{err}
		}
		vm.Endpoint = strings.TrimSpace(vmReq.Endpoint)
	}
	if vmReq.LabelSelector != nil {
		vm.LabelSelector = strings.TrimSpace(*vmReq.LabelSelector)
	}
	if vmReq.SkipSSLVerify != nil {
		vm.SkipSSLVerify = *vmReq.SkipSSLVerify
	}
	if vmReq.RetentionDays != nil {
		vm.RetentionDays = *vmReq.RetentionDays
	}
	data := vmReq.credentials()
	if data == nil {
		return nil
	}
	return s.storeCredentials(ctx, "costdeck-vm-credentials", data, &vm.SecretRef)
}

func applyMCPSettings(_ context.Context, cfg *finopsv1.CostDeckConfig, req *SettingsUpdateRequest) error {
	if req.Integrations == nil || req.Integrations.MCP == nil || req.Integrations.MCP.Enabled == nil {
		return nil
	}
	if cfg.Spec.Integrations.MCP == nil {
		cfg.Spec.Integrations.MCP = &finopsv1.MCPConfig{}
	}
	cfg.Spec.Integrations.MCP.Enabled = *req.Integrations.MCP.Enabled
	return nil
}

var decimalRate = regexp.MustCompile(`^[0-9]+(\.[0-9]+)?$`)

func applyPricingSettings(_ context.Context, cfg *finopsv1.CostDeckConfig, req *SettingsUpdateRequest) error {
	p := req.Pricing
	if p == nil {
		return nil
	}
	for _, f := range []struct {
		src *string
		dst *string
	}{
		{p.CPUCoreHour, &cfg.Spec.Pricing.CPUCoreHour}, {p.MemoryGBHour, &cfg.Spec.Pricing.MemoryGBHour},
		{p.StorageGiBMonth, &cfg.Spec.Pricing.StorageGiBMonth}, {p.LoadBalancerMonth, &cfg.Spec.Pricing.LoadBalancerMonth},
	} {
		if f.src == nil {
			continue
		}
		v := strings.TrimSpace(*f.src)
		if v != "" && !decimalRate.MatchString(v) {
			return badRequestf("rates must be plain decimal numbers such as 0.031, got %q", v)
		}
		*f.dst = v
	}
	if p.Currency != nil {
		c := strings.ToUpper(strings.TrimSpace(*p.Currency))
		if c != "" && len(c) != 3 {
			return badRequestf("currency must be a three-letter code such as USD or EUR")
		}
		cfg.Spec.Pricing.Currency = c
	}
	return nil
}

var clockTime = regexp.MustCompile(`^([01][0-9]|2[0-3]):[0-5][0-9]$`)

func applyReportSettings(_ context.Context, cfg *finopsv1.CostDeckConfig, req *SettingsUpdateRequest) error {
	if req.Reports == nil || req.Reports.Digest == nil {
		return nil
	}
	d := *req.Reports.Digest
	if d.Frequency != "" && d.Frequency != "weekly" && d.Frequency != "monthly" {
		return badRequestf("frequency must be weekly or monthly")
	}
	if d.Time != "" && !clockTime.MatchString(d.Time) {
		return badRequestf("time must be HH:MM, got %q", d.Time)
	}
	if d.Timezone != "" {
		if _, err := time.LoadLocation(d.Timezone); err != nil {
			return badRequestf("unknown time zone %q", d.Timezone)
		}
	}
	cfg.Spec.Reports.Digest = d
	return nil
}

var budgetName = regexp.MustCompile(`^[a-z0-9]([-a-z0-9]*[a-z0-9])?$`)

func applyBudgetSettings(_ context.Context, cfg *finopsv1.CostDeckConfig, req *SettingsUpdateRequest) error {
	if req.Alerts != nil {
		a := req.Alerts.Anomalies
		if a.Percent != 0 && (a.Percent < 5 || a.Percent > 1000) {
			return badRequestf("the anomaly threshold must be between 5 and 1000 percent")
		}
		if a.MinimumDaily != "" && !decimalRate.MatchString(a.MinimumDaily) {
			return badRequestf("the minimum daily increase must be a plain number such as 5")
		}
		cfg.Spec.Alerts = *req.Alerts
	}
	if req.Budgets == nil {
		return nil
	}
	seen := map[string]bool{}
	for _, b := range *req.Budgets {
		switch {
		case !budgetName.MatchString(b.Name) || len(b.Name) > 63:
			return badRequestf("budget name %q must be lowercase letters, digits and dashes", b.Name)
		case seen[b.Name]:
			return badRequestf("there are two budgets named %q", b.Name)
		case b.Scope != finops.ScopeCluster && b.Scope != finops.ScopeNamespace && b.Scope != finops.ScopeTeam && b.Scope != finops.ScopeEnvironment:
			return badRequestf("budget %q: scope must be cluster, namespace, team or environment", b.Name)
		case b.Scope != finops.ScopeCluster && b.Value == "":
			return badRequestf("budget %q: say which %s it covers", b.Name, b.Scope)
		case len(b.Value) > 253:
			return badRequestf("budget %q: the %s name is too long", b.Name, b.Scope)
		case !decimalRate.MatchString(b.MonthlyLimit):
			return badRequestf("budget %q: the monthly limit must be a plain number such as 1200", b.Name)
		case len(b.Thresholds) > 5:
			return badRequestf("budget %q: at most five thresholds", b.Name)
		}
		for _, t := range b.Thresholds {
			if t < 1 || t > 1000 {
				return badRequestf("budget %q: thresholds are percentages between 1 and 1000", b.Name)
			}
		}
		seen[b.Name] = true
	}
	cfg.Spec.Budgets = *req.Budgets
	return nil
}

var (
	resourceGroupName = regexp.MustCompile(`^[-\w._()]{1,90}$`)
	bigQueryTableName = regexp.MustCompile(`^[a-z][-a-z0-9]{4,28}[a-z0-9]\.[A-Za-z0-9_]+\.[A-Za-z0-9_]+$`)
)

func applyBillingSettings(_ context.Context, cfg *finopsv1.CostDeckConfig, req *SettingsUpdateRequest) error {
	if req.Billing == nil {
		return nil
	}
	b := *req.Billing
	clouds := 0
	if b.AWS != nil {
		clouds++
		if b.AWS.TagValue == "" {
			return badRequestf("AWS billing needs the tag value that marks the cluster's instances, usually the cluster name")
		}
	}
	if b.Azure != nil {
		clouds++
		if !resourceGroupName.MatchString(b.Azure.ResourceGroup) {
			return badRequestf("Azure billing needs the node resource group, e.g. MC_rg_cluster_westeurope")
		}
	}
	if b.GCP != nil {
		clouds++
		if !bigQueryTableName.MatchString(b.GCP.Table) || b.GCP.ClusterName == "" {
			return badRequestf("Google Cloud billing needs the export table as project.dataset.table and the cluster name")
		}
	}
	if clouds > 1 {
		return badRequestf("reconcile with one cloud: the one this cluster runs on")
	}
	if b.Enabled && clouds == 0 {
		return badRequestf("say which cloud's bill to read")
	}
	cfg.Spec.Billing = b
	return nil
}

func applyFeatureSettings(_ context.Context, cfg *finopsv1.CostDeckConfig, req *SettingsUpdateRequest) error {
	if req.Features != nil && req.Features.CloudPricingAPI != nil {
		cfg.Spec.Features.CloudPricingAPI = *req.Features.CloudPricingAPI
	}
	return nil
}

func (s *Server) applyAuthSettings(ctx context.Context, cfg *finopsv1.CostDeckConfig, req *SettingsUpdateRequest) error {
	if req.Auth == nil {
		return nil
	}
	if req.Auth.DisableLocalLogin != nil {
		cfg.Spec.Auth.DisableLocalLogin = *req.Auth.DisableLocalLogin
	}
	er := req.Auth.Entra
	if er == nil {
		return nil
	}
	if cfg.Spec.Auth.Entra == nil {
		cfg.Spec.Auth.Entra = &finopsv1.EntraConfig{}
	}
	e := cfg.Spec.Auth.Entra
	setString := func(dst *string, src *string) {
		if src != nil {
			*dst = strings.TrimSpace(*src)
		}
	}
	setString(&e.TenantID, er.TenantID)
	setString(&e.ClientID, er.ClientID)
	setString(&e.RedirectURL, er.RedirectURL)
	setString(&e.AuthorityHost, er.AuthorityHost)
	if er.DefaultRole != nil {
		role, ok := auth.ParseRole(*er.DefaultRole)
		if !ok {
			return badRequestf("unknown default role %q (use viewer, operator or admin)", *er.DefaultRole)
		}
		e.DefaultRole = string(role)
	}
	if er.GroupRoleMapping != nil {
		mapping := make(map[string]string, len(er.GroupRoleMapping))
		for group, roleName := range er.GroupRoleMapping {
			role, ok := auth.ParseRole(roleName)
			if !ok || strings.TrimSpace(group) == "" {
				return badRequestf("invalid group mapping %q -> %q", group, roleName)
			}
			mapping[strings.TrimSpace(group)] = string(role)
		}
		e.GroupRoleMapping = mapping
	}
	if er.AutoProvision != nil {
		v := *er.AutoProvision
		e.AutoProvision = &v
	}
	if er.SkipSSLVerify != nil {
		e.SkipSSLVerify = *er.SkipSSLVerify
	}
	if er.Enabled != nil {
		e.Enabled = *er.Enabled
	}
	if e.Enabled && (e.TenantID == "" || e.ClientID == "") {
		return badRequestf("tenant ID and client ID are required to enable Microsoft sign-in")
	}
	if er.ClientSecret == "" {
		return nil
	}
	return s.storeCredentials(ctx, "costdeck-entra-credentials",
		map[string][]byte{auth.SecretKeyClientSecret: []byte(er.ClientSecret)}, &e.ClientSecretRef)
}

// storeCredentials writes a credentials Secret and points the config field at it.
func (s *Server) storeCredentials(ctx context.Context, name string, data map[string][]byte, ref *string) error {
	if err := s.upsertSecret(ctx, config.OperatorNamespace(), name, data); err != nil {
		return fmt.Errorf("store credentials in %s: %w", name, err)
	}
	*ref = name
	return nil
}

// ─── Test Provider Connectivity ─────────────────────────────────────────────

func (s *Server) handleTestProvider(w http.ResponseWriter, r *http.Request) {
	providerName := r.PathValue("provider")
	ctx := r.Context()
	cfg, err := config.Get(ctx, s.Client)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}

	switch providerName {
	case scaling.ProviderAWS, scaling.ProviderAzure, scaling.ProviderGCP:
		s.testCloudProvider(w, r, cfg, providerName)
	case "ai":
		s.testAIProvider(w, r)
	case "victoriametrics":
		s.testVictoriaMetrics(w, ctx, cfg, r)
	case "webex":
		s.testWebex(w, ctx, r)
	case "entra":
		s.testEntra(w, ctx, cfg, r)
	default:
		writeError(w, http.StatusBadRequest, fmt.Sprintf("Unknown provider: %s", providerName))
	}
}

// testCloudProvider checks a cloud provider with the values typed into the settings form,
// before they are saved: unsaved credentials override the stored Secret, and unsaved
// region, subscription or project override the saved ones. It reports how many resources
// discovery finds, so "connected but sees nothing" is visible straight away.
func (s *Server) testCloudProvider(w http.ResponseWriter, r *http.Request, saved *finopsv1.CostDeckConfig, name string) {
	ctx := r.Context()
	cfg := saved.DeepCopy()
	var override map[string][]byte
	body, _ := io.ReadAll(http.MaxBytesReader(w, r.Body, 1<<20))
	switch name {
	case scaling.ProviderAWS:
		var req AWSUpdateRequest
		_ = json.Unmarshal(body, &req)
		if cfg.Spec.Providers.AWS == nil {
			cfg.Spec.Providers.AWS = &finopsv1.AWSProviderConfig{}
		}
		if req.Region != "" {
			cfg.Spec.Providers.AWS.Region = req.Region
		}
		if req.AccessKeyID != "" && req.SecretAccessKey != "" {
			override = map[string][]byte{"AWS_ACCESS_KEY_ID": []byte(req.AccessKeyID), "AWS_SECRET_ACCESS_KEY": []byte(req.SecretAccessKey)}
		}
	case scaling.ProviderAzure:
		var req AzureUpdateRequest
		_ = json.Unmarshal(body, &req)
		if cfg.Spec.Providers.Azure == nil {
			cfg.Spec.Providers.Azure = &finopsv1.AzureProviderConfig{}
		}
		if req.SubscriptionID != "" {
			cfg.Spec.Providers.Azure.SubscriptionID = req.SubscriptionID
		}
		if req.TenantID != "" {
			cfg.Spec.Providers.Azure.TenantID = req.TenantID
		}
		if req.ClientID != "" && req.ClientSecret != "" {
			override = map[string][]byte{scaling.AzureSecretClientID: []byte(req.ClientID), scaling.AzureSecretClientSecret: []byte(req.ClientSecret)}
		}
	case scaling.ProviderGCP:
		var req GCPUpdateRequest
		_ = json.Unmarshal(body, &req)
		if cfg.Spec.Providers.GCP == nil {
			cfg.Spec.Providers.GCP = &finopsv1.GCPProviderConfig{}
		}
		if req.ProjectID != "" {
			cfg.Spec.Providers.GCP.ProjectID = req.ProjectID
		}
		if req.ServiceAccountJSON != "" {
			if err := validServiceAccountKey(req.ServiceAccountJSON); err != nil {
				writeJSON(w, http.StatusOK, map[string]any{"connected": false, "error": err.Error()})
				return
			}
			override = map[string][]byte{scaling.GCPSecretKey: []byte(req.ServiceAccountJSON)}
		}
	}

	fail := func(err error) { writeJSON(w, http.StatusOK, map[string]any{"connected": false, "error": err.Error()}) }
	provider, err := scaling.BuildProvider(ctx, s.Client, cfg, name, override)
	if err != nil {
		fail(err)
		return
	}
	if err := provider.ValidateConnectivity(ctx); err != nil {
		fail(err)
		return
	}
	settings := scaling.CloudSettingsFor(cfg, name)
	types := settings.ResourceTypes
	if len(types) == 0 {
		types = provider.ResourceTypes()
	}
	total := 0
	for _, rt := range types {
		targets, err := provider.Discover(ctx, rt, settings.Filter)
		if err != nil {
			fail(fmt.Errorf("connected, but listing %s failed: %w", rt, err))
			return
		}
		total += len(targets)
	}
	writeJSON(w, http.StatusOK, map[string]any{"connected": true, "message": fmt.Sprintf("Connected. %d resources found.", total)})
}

// subscriptionID matches an Azure subscription GUID.
var subscriptionID = regexp.MustCompile(`^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}$`)

// validServiceAccountKey accepts only Google service account keys. Other credential file
// types (external accounts, for example) can make the client library fetch tokens from
// URLs chosen by the file.
func validServiceAccountKey(raw string) error {
	var key struct {
		Type        string `json:"type"`
		ClientEmail string `json:"client_email"`
		PrivateKey  string `json:"private_key"`
	}
	if err := json.Unmarshal([]byte(raw), &key); err != nil {
		return fmt.Errorf("the service account key is not valid JSON: %w", err)
	}
	if key.Type != "service_account" || key.ClientEmail == "" || key.PrivateKey == "" {
		return errors.New("paste a service account key (a JSON file with \"type\": \"service_account\")")
	}
	return nil
}

// testEntra validates Entra SSO settings (unsaved form values first, stored ones otherwise):
// the tenant must be discoverable and the client credentials must be accepted.
func (s *Server) testEntra(w http.ResponseWriter, ctx context.Context, cfg *finopsv1.CostDeckConfig, r *http.Request) {
	var req EntraUpdateRequest
	if err := decodeJSON(w, r, &req); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	ec := finopsv1.EntraConfig{}
	if stored := cfg.Spec.Auth.Entra; stored != nil {
		ec = *stored
	}
	for dst, src := range map[*string]*string{&ec.TenantID: req.TenantID, &ec.ClientID: req.ClientID, &ec.AuthorityHost: req.AuthorityHost} {
		if src != nil && *src != "" {
			*dst = strings.TrimSpace(*src)
		}
	}
	if req.SkipSSLVerify != nil {
		ec.SkipSSLVerify = *req.SkipSSLVerify
	}
	secret := req.ClientSecret
	if secret == "" && ec.ClientSecretRef != "" {
		if data, err := config.SecretData(ctx, s.Client, ec.ClientSecretRef); err == nil {
			secret = string(data[auth.SecretKeyClientSecret])
		}
	}
	entra := &auth.Entra{Client: s.Client}
	if s.Auth != nil {
		entra = s.Auth.Entra
	}
	msg, err := entra.TestEntra(ctx, ec, secret)
	if err != nil {
		writeJSON(w, http.StatusOK, map[string]any{"connected": false, "error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"connected": true, "message": msg})
}

// testWebex validates the bot token (unsaved form value first, stored Secret otherwise)
// and that the bot can see the configured space.
func (s *Server) testWebex(w http.ResponseWriter, ctx context.Context, r *http.Request) {
	var req WebexUpdateRequest
	if err := decodeJSON(w, r, &req); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	settings, err := webex.LoadSettings(ctx, s.Client)
	if err != nil || settings == nil {
		settings = &webex.Settings{}
	}
	if req.BotToken != "" {
		settings.Token = req.BotToken
	}
	if req.RoomID != nil {
		settings.RoomID = strings.TrimSpace(*req.RoomID)
	}
	msg, err := webex.Check(ctx, webex.NewClient(settings.Token), settings)
	if err != nil {
		writeJSON(w, http.StatusOK, map[string]any{"connected": false, "error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"connected": true, "message": msg})
}

// testVictoriaMetrics validates the VictoriaMetrics settings in the request body (so unsaved
// form values can be checked before saving), falling back to the stored configuration.
func (s *Server) testVictoriaMetrics(w http.ResponseWriter, ctx context.Context, cfg *finopsv1.CostDeckConfig, r *http.Request) {
	var req VictoriaMetricsUpdateRequest
	if err := decodeJSON(w, r, &req); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}

	vm := finopsv1.VictoriaMetricsConfig{}
	if stored := cfg.Spec.Integrations.VictoriaMetrics; stored != nil {
		vm = *stored
	}
	if req.Endpoint != "" {
		vm.Endpoint = req.Endpoint
	}
	if req.LabelSelector != nil {
		vm.LabelSelector = *req.LabelSelector
	}
	if req.SkipSSLVerify != nil {
		vm.SkipSSLVerify = *req.SkipSSLVerify
	}

	vmClient, err := metrics.BuildVMClient(ctx, s.Client, &vm, req.credentials())
	if err == nil {
		err = vmClient.Validate(ctx)
	}
	if err != nil {
		writeJSON(w, http.StatusOK, map[string]any{"connected": false, "error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"connected": true, "endpoint": vmClient.Endpoint()})
}

// ─── Provider Status ────────────────────────────────────────────────────────

func (s *Server) handleProviderStatus(w http.ResponseWriter, r *http.Request) {
	providerName := r.PathValue("provider")
	cfg, err := config.Get(r.Context(), s.Client)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}

	var status *finopsv1.ProviderStatus
	switch providerName {
	case "aws":
		status = cfg.Status.AWS
	case "azure":
		status = cfg.Status.Azure
	case "gcp":
		status = cfg.Status.GCP
	case "victoriametrics":
		status = cfg.Status.VictoriaMetrics
	case "webex":
		status = cfg.Status.Webex
	default:
		writeError(w, http.StatusBadRequest, "Unknown provider")
		return
	}

	if status == nil {
		status = &finopsv1.ProviderStatus{Connected: false, Error: "Provider not configured"}
	}

	writeJSON(w, http.StatusOK, status)
}

// ─── Helpers ────────────────────────────────────────────────────────────────

// getOrCreateDefaultConfig returns the CostDeckConfig singleton, creating it on first use
// so that settings can be saved on a fresh installation.
func (s *Server) getOrCreateDefaultConfig(ctx context.Context) (*finopsv1.CostDeckConfig, error) {
	cfg := &finopsv1.CostDeckConfig{}
	err := s.Client.Get(ctx, config.Key(), cfg)
	if apierrors.IsNotFound(err) {
		cfg = config.Empty()
		if err := s.Client.Create(ctx, cfg); err != nil && !apierrors.IsAlreadyExists(err) {
			return nil, fmt.Errorf("create CostDeckConfig: %w", err)
		}
		return cfg, nil
	}
	if err != nil {
		return nil, err
	}
	return cfg, nil
}

// secretDataOrEmpty returns a copy of a Secret's data, or an empty map if it does not exist,
// so that updating one key keeps the others.
func (s *Server) secretDataOrEmpty(ctx context.Context, name string) (map[string][]byte, error) {
	data, err := config.SecretData(ctx, s.Client, name)
	if apierrors.IsNotFound(err) {
		return map[string][]byte{}, nil
	}
	if err != nil {
		return nil, err
	}
	out := make(map[string][]byte, len(data))
	maps.Copy(out, data)
	return out, nil
}

func (s *Server) secretExists(ctx context.Context, name, namespace string) bool {
	secret := &corev1.Secret{}
	err := s.Client.Get(ctx, client.ObjectKey{Name: name, Namespace: namespace}, secret)
	return err == nil
}

func (s *Server) upsertSecret(ctx context.Context, namespace, name string, data map[string][]byte) error {
	secret := &corev1.Secret{}
	err := s.Client.Get(ctx, client.ObjectKey{Name: name, Namespace: namespace}, secret)

	if apierrors.IsNotFound(err) {
		// Create new secret
		secret = &corev1.Secret{
			ObjectMeta: metav1.ObjectMeta{
				Name:      name,
				Namespace: namespace,
				Labels: map[string]string{
					"app.kubernetes.io/managed-by": "costdeck-operator",
					"costdeck.io/secret-type":      "provider-credentials",
				},
			},
			Type: corev1.SecretTypeOpaque,
			Data: data,
		}
		return s.Client.Create(ctx, secret)
	}

	if err != nil {
		return err
	}

	// Update existing secret
	secret.Data = data
	return s.Client.Update(ctx, secret)
}
