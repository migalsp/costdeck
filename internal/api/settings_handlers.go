package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"net/http"
	"regexp"
	"strings"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/util/retry"
	"sigs.k8s.io/controller-runtime/pkg/client"

	finopsv1 "github.com/migalsp/costdeck-operator/api/v1"
	"github.com/migalsp/costdeck-operator/internal/auth"
	"github.com/migalsp/costdeck-operator/internal/config"
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
}

// PricingSettingsResponse shows the configured custom rates and the rates in effect.
type PricingSettingsResponse struct {
	CPUCoreHour  string        `json:"cpuCoreHour,omitempty"`
	MemoryGBHour string        `json:"memoryGiBHour,omitempty"`
	Currency     string        `json:"currency,omitempty"`
	Effective    pricing.Rates `json:"effective"`
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
	HasCredentials bool                     `json:"hasCredentials"`
	Status         *finopsv1.ProviderStatus `json:"status,omitempty"`
}

type GCPSettingsResponse struct {
	Enabled        bool                     `json:"enabled"`
	ProjectID      string                   `json:"projectId,omitempty"`
	HasCredentials bool                     `json:"hasCredentials"`
	Status         *finopsv1.ProviderStatus `json:"status,omitempty"`
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
	HasCredentials bool                     `json:"hasCredentials"`
	Status         *finopsv1.ProviderStatus `json:"status,omitempty"`
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
}

// PricingUpdateRequest sets custom rates; empty strings clear them.
type PricingUpdateRequest struct {
	CPUCoreHour  *string `json:"cpuCoreHour,omitempty"`
	MemoryGBHour *string `json:"memoryGiBHour,omitempty"`
	Currency     *string `json:"currency,omitempty"`
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
	Enabled        *bool  `json:"enabled,omitempty"`
	SubscriptionID string `json:"subscriptionId,omitempty"`
	TenantID       string `json:"tenantId,omitempty"`
	ClientID       string `json:"clientId,omitempty"`
	ClientSecret   string `json:"clientSecret,omitempty"`
}

type GCPUpdateRequest struct {
	Enabled            *bool  `json:"enabled,omitempty"`
	ProjectID          string `json:"projectId,omitempty"`
	ServiceAccountJSON string `json:"serviceAccountJson,omitempty"`
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
	Enabled           *bool `json:"enabled,omitempty"`
	NotifyTransitions *bool `json:"notifyTransitions,omitempty"`
	RoomID        *string `json:"roomId,omitempty"`
	BotToken      string  `json:"botToken,omitempty"`
	WebhookSecret *string `json:"webhookSecret,omitempty"`
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
			HasCredentials: hasCreds,
			Status:         cfg.Status.Azure,
		}
	}

	// GCP (stub)
	if cfg.Spec.Providers.GCP != nil {
		hasCreds := cfg.Spec.Providers.GCP.SecretRef != "" && s.secretExists(ctx, cfg.Spec.Providers.GCP.SecretRef, cfg.Namespace)
		resp.Providers.GCP = &GCPSettingsResponse{
			Enabled:        cfg.Spec.Providers.GCP.Enabled,
			ProjectID:      cfg.Spec.Providers.GCP.ProjectID,
			HasCredentials: hasCreds,
			Status:         cfg.Status.GCP,
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
				RoomID:         cfg.Spec.Integrations.Messenger.Webex.RoomID,
				HasCredentials: cfg.Spec.Integrations.Messenger.Webex.SecretRef != "",
				Status:         cfg.Status.Webex,
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
		CPUCoreHour:  cfg.Spec.Pricing.CPUCoreHour,
		MemoryGBHour: cfg.Spec.Pricing.MemoryGBHour,
		Currency:     cfg.Spec.Pricing.Currency,
		Effective:    s.costRates(ctx),
	}

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
		s.applyWebexSettings, s.applyVictoriaMetricsSettings, applyMCPSettings, applyFeatureSettings, applyPricingSettings, s.applyAuthSettings,
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
	if azureReq.ClientID == "" || azureReq.ClientSecret == "" {
		return nil
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
	if gcpReq.ServiceAccountJSON == "" {
		return nil
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
	}{{p.CPUCoreHour, &cfg.Spec.Pricing.CPUCoreHour}, {p.MemoryGBHour, &cfg.Spec.Pricing.MemoryGBHour}} {
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
	case "aws":
		s.testAWSProvider(w, ctx, cfg, r)
	case "ai":
		s.testAIProvider(w, r)
	case "victoriametrics":
		s.testVictoriaMetrics(w, ctx, cfg, r)
	case "webex":
		s.testWebex(w, ctx, r)
	case "entra":
		s.testEntra(w, ctx, cfg, r)
	case "azure":
		writeJSON(w, http.StatusOK, map[string]any{
			"connected": false,
			"error":     "Azure provider is not yet implemented",
		})
	case "gcp":
		writeJSON(w, http.StatusOK, map[string]any{
			"connected": false,
			"error":     "GCP provider is not yet implemented",
		})
	default:
		writeError(w, http.StatusBadRequest, fmt.Sprintf("Unknown provider: %s", providerName))
	}
}

func (s *Server) testAWSProvider(w http.ResponseWriter, ctx context.Context, cfg *finopsv1.CostDeckConfig, r *http.Request) {
	var provider *scaling.AWSProvider
	var err error

	// Try to parse credentials from the request body first (unsaved UI form data)
	var req AWSUpdateRequest
	if r.Body != nil {
		err := json.NewDecoder(r.Body).Decode(&req)
		if err == nil && req.AccessKeyID != "" && req.SecretAccessKey != "" {
			provider, err = scaling.NewAWSProviderFromCredentials(ctx, req.AccessKeyID, req.SecretAccessKey, req.Region)
			if err != nil {
				writeJSON(w, http.StatusOK, map[string]any{
					"connected": false,
					"error":     fmt.Sprintf("Failed to initialize with provided credentials: %v", err),
				})
				return
			}
		}
	}

	// Fallback to stored credentials if no body or body belongs to another provider
	if provider == nil {
		if cfg.Spec.Providers.AWS == nil || cfg.Spec.Providers.AWS.SecretRef == "" {
			writeJSON(w, http.StatusOK, map[string]any{
				"connected": false,
				"error":     "No AWS credentials configured",
			})
			return
		}

		provider, err = scaling.NewAWSProviderFromSecret(ctx, s.Client,
			cfg.Spec.Providers.AWS.SecretRef,
			cfg.Namespace,
			cfg.Spec.Providers.AWS.Region,
		)
		if err != nil {
			writeJSON(w, http.StatusOK, map[string]any{
				"connected": false,
				"error":     err.Error(),
			})
			return
		}
	}

	if err := provider.ValidateConnectivity(ctx); err != nil {
		writeJSON(w, http.StatusOK, map[string]any{
			"connected": false,
			"error":     err.Error(),
		})
		return
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"connected": true,
	})
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
