package api

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
	logf "sigs.k8s.io/controller-runtime/pkg/log"

	finopsv1 "github.com/migalsp/costdeck-operator/api/v1"
	"github.com/migalsp/costdeck-operator/internal/scaling"
)

// ─── Settings API Data Types ─────────────────────────────────────────────────

// SettingsResponse is the full settings response returned to the UI.
// Credentials are always masked.
type SettingsResponse struct {
	Providers    ProvidersSettingsResponse    `json:"providers"`
	Integrations IntegrationsSettingsResponse `json:"integrations"`
	Features     *FeaturesSettingsResponse    `json:"features,omitempty"`
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
	Enabled        bool   `json:"enabled"`
	RoomID         string `json:"roomId,omitempty"`
	HasCredentials bool   `json:"hasCredentials"`
}

type VictoriaMetricsSettingsResponse struct {
	Enabled        bool   `json:"enabled"`
	Endpoint       string `json:"endpoint,omitempty"`
	RetentionDays  int    `json:"retentionDays"`
	HasCredentials bool   `json:"hasCredentials"`
}

// SettingsUpdateRequest is the payload for updating settings.
// Credentials are provided here in plaintext and then stored in K8s Secrets.
type SettingsUpdateRequest struct {
	Providers    *ProvidersUpdateRequest    `json:"providers,omitempty"`
	Integrations *IntegrationsUpdateRequest `json:"integrations,omitempty"`
	Features     *FeaturesUpdateRequest     `json:"features,omitempty"`
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
	Enabled  *bool  `json:"enabled,omitempty"`
	RoomID   string `json:"roomId,omitempty"`
	BotToken string `json:"botToken,omitempty"`
}

type VictoriaMetricsUpdateRequest struct {
	Enabled       *bool  `json:"enabled,omitempty"`
	Endpoint      string `json:"endpoint,omitempty"`
	RetentionDays *int   `json:"retentionDays,omitempty"`
	BearerToken   string `json:"bearerToken,omitempty"`
	Username      string `json:"username,omitempty"`
	Password      string `json:"password,omitempty"`
}

// ─── Handlers ────────────────────────────────────────────────────────────────

func (s *Server) handleSettings(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		s.handleGetSettings(w, r)
	case http.MethodPut:
		s.handleUpdateSettings(w, r)
	default:
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
	}
}

func (s *Server) handleSettingsProviderActions(w http.ResponseWriter, r *http.Request) {
	parts := strings.Split(r.URL.Path, "/")
	// /api/settings/providers/{name}/test → parts: ["", "api", "settings", "providers", name, "test"]
	if len(parts) < 6 {
		http.Error(w, "Invalid path", http.StatusBadRequest)
		return
	}

	providerName := parts[4]
	action := parts[5]

	switch action {
	case "test":
		s.handleTestProvider(w, r, providerName)
	case "status":
		s.handleProviderStatus(w, r, providerName)
	default:
		http.Error(w, "Unknown action", http.StatusBadRequest)
	}
}

// ─── GET /api/settings ──────────────────────────────────────────────────────

func (s *Server) handleGetSettings(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	config := s.getOrCreateDefaultConfig(ctx)

	resp := s.buildSettingsResponse(ctx, config)

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(resp)
}

func (s *Server) buildSettingsResponse(ctx context.Context, config *finopsv1.CostDeckConfig) SettingsResponse {
	resp := SettingsResponse{}

	// AWS
	if config.Spec.Providers.AWS != nil {
		hasCreds := config.Spec.Providers.AWS.SecretRef != "" && s.secretExists(ctx, config.Spec.Providers.AWS.SecretRef, config.Namespace)
		resp.Providers.AWS = &AWSSettingsResponse{
			Enabled:        config.Spec.Providers.AWS.Enabled,
			Region:         config.Spec.Providers.AWS.Region,
			HasCredentials: hasCreds,
			DiscoveryTags:  config.Spec.Providers.AWS.DiscoveryTags,
			ResourceTypes:  config.Spec.Providers.AWS.ResourceTypes,
			Status:         config.Status.AWS,
		}
	}

	// Azure (stub)
	if config.Spec.Providers.Azure != nil {
		hasCreds := config.Spec.Providers.Azure.SecretRef != "" && s.secretExists(ctx, config.Spec.Providers.Azure.SecretRef, config.Namespace)
		resp.Providers.Azure = &AzureSettingsResponse{
			Enabled:        config.Spec.Providers.Azure.Enabled,
			SubscriptionID: config.Spec.Providers.Azure.SubscriptionID,
			TenantID:       config.Spec.Providers.Azure.TenantID,
			HasCredentials: hasCreds,
			Status:         config.Status.Azure,
		}
	}

	// GCP (stub)
	if config.Spec.Providers.GCP != nil {
		hasCreds := config.Spec.Providers.GCP.SecretRef != "" && s.secretExists(ctx, config.Spec.Providers.GCP.SecretRef, config.Namespace)
		resp.Providers.GCP = &GCPSettingsResponse{
			Enabled:        config.Spec.Providers.GCP.Enabled,
			ProjectID:      config.Spec.Providers.GCP.ProjectID,
			HasCredentials: hasCreds,
			Status:         config.Status.GCP,
		}
	}

	// AI (stub)
	if config.Spec.Integrations.AI != nil {
		resp.Integrations.AI = &AISettingsResponse{
			Enabled:        config.Spec.Integrations.AI.Enabled,
			Provider:       config.Spec.Integrations.AI.Provider,
			Model:          config.Spec.Integrations.AI.Model,
			BaseURL:        config.Spec.Integrations.AI.BaseURL,
			SkipSSLVerify:  config.Spec.Integrations.AI.SkipSSLVerify,
			HasCredentials: config.Spec.Integrations.AI.SecretRef != "",
		}
	}

	// Messenger / Webex
	if config.Spec.Integrations.Messenger != nil && config.Spec.Integrations.Messenger.Webex != nil {
		resp.Integrations.Messenger = &MessengerSettingsResponse{
			Webex: &WebexSettingsResponse{
				Enabled:        config.Spec.Integrations.Messenger.Webex.Enabled,
				RoomID:         config.Spec.Integrations.Messenger.Webex.RoomID,
				HasCredentials: config.Spec.Integrations.Messenger.Webex.SecretRef != "",
			},
		}
	}

	// VictoriaMetrics
	if config.Spec.Integrations.VictoriaMetrics != nil {
		hasCreds := config.Spec.Integrations.VictoriaMetrics.SecretRef != "" && s.secretExists(ctx, config.Spec.Integrations.VictoriaMetrics.SecretRef, config.Namespace)
		retentionDays := config.Spec.Integrations.VictoriaMetrics.RetentionDays
		if retentionDays == 0 {
			retentionDays = 7
		}
		resp.Integrations.VictoriaMetrics = &VictoriaMetricsSettingsResponse{
			Enabled:        config.Spec.Integrations.VictoriaMetrics.Enabled,
			Endpoint:       config.Spec.Integrations.VictoriaMetrics.Endpoint,
			RetentionDays:  retentionDays,
			HasCredentials: hasCreds,
		}
	}

	// Features
	resp.Features = &FeaturesSettingsResponse{
		CloudPricingAPI: config.Spec.Features.CloudPricingAPI,
	}

	return resp
}

// ─── PUT /api/settings ──────────────────────────────────────────────────────

func (s *Server) handleUpdateSettings(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()

	var req SettingsUpdateRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "Invalid request body: "+err.Error(), http.StatusBadRequest)
		return
	}

	config := s.getOrCreateDefaultConfig(ctx)
	operatorNs := config.Namespace

	// ─── Process AWS ─────────────────────────────────────────────────────────
	if req.Providers != nil && req.Providers.AWS != nil {
		awsReq := req.Providers.AWS
		if config.Spec.Providers.AWS == nil {
			config.Spec.Providers.AWS = &finopsv1.AWSProviderConfig{}
		}

		if awsReq.Enabled != nil {
			config.Spec.Providers.AWS.Enabled = *awsReq.Enabled
		}
		if awsReq.Region != "" {
			config.Spec.Providers.AWS.Region = awsReq.Region
		}
		if awsReq.DiscoveryTags != nil {
			config.Spec.Providers.AWS.DiscoveryTags = awsReq.DiscoveryTags
		}
		if awsReq.ResourceTypes != nil {
			config.Spec.Providers.AWS.ResourceTypes = awsReq.ResourceTypes
		}

		// If credentials are provided, create/update the K8s Secret
		if awsReq.AccessKeyID != "" && awsReq.SecretAccessKey != "" {
			secretName := "costdeck-aws-credentials"
			if err := s.upsertSecret(ctx, operatorNs, secretName, map[string][]byte{
				"AWS_ACCESS_KEY_ID":     []byte(awsReq.AccessKeyID),
				"AWS_SECRET_ACCESS_KEY": []byte(awsReq.SecretAccessKey),
				"AWS_REGION":            []byte(awsReq.Region),
			}); err != nil {
				http.Error(w, "Failed to store credentials: "+err.Error(), http.StatusInternalServerError)
				return
			}
			config.Spec.Providers.AWS.SecretRef = secretName
		}
	}

	// ─── Process Azure (stub) ────────────────────────────────────────────────
	if req.Providers != nil && req.Providers.Azure != nil {
		azureReq := req.Providers.Azure
		if config.Spec.Providers.Azure == nil {
			config.Spec.Providers.Azure = &finopsv1.AzureProviderConfig{}
		}
		if azureReq.Enabled != nil {
			config.Spec.Providers.Azure.Enabled = *azureReq.Enabled
		}
		if azureReq.SubscriptionID != "" {
			config.Spec.Providers.Azure.SubscriptionID = azureReq.SubscriptionID
		}
		if azureReq.TenantID != "" {
			config.Spec.Providers.Azure.TenantID = azureReq.TenantID
		}
		if azureReq.ClientID != "" && azureReq.ClientSecret != "" {
			secretName := "costdeck-azure-credentials"
			if err := s.upsertSecret(ctx, operatorNs, secretName, map[string][]byte{
				"AZURE_CLIENT_ID":     []byte(azureReq.ClientID),
				"AZURE_CLIENT_SECRET": []byte(azureReq.ClientSecret),
				"AZURE_TENANT_ID":     []byte(azureReq.TenantID),
			}); err != nil {
				http.Error(w, "Failed to store credentials: "+err.Error(), http.StatusInternalServerError)
				return
			}
			config.Spec.Providers.Azure.SecretRef = secretName
		}
	}

	// ─── Process GCP (stub) ──────────────────────────────────────────────────
	if req.Providers != nil && req.Providers.GCP != nil {
		gcpReq := req.Providers.GCP
		if config.Spec.Providers.GCP == nil {
			config.Spec.Providers.GCP = &finopsv1.GCPProviderConfig{}
		}
		if gcpReq.Enabled != nil {
			config.Spec.Providers.GCP.Enabled = *gcpReq.Enabled
		}
		if gcpReq.ProjectID != "" {
			config.Spec.Providers.GCP.ProjectID = gcpReq.ProjectID
		}
		if gcpReq.ServiceAccountJSON != "" {
			secretName := "costdeck-gcp-credentials"
			if err := s.upsertSecret(ctx, operatorNs, secretName, map[string][]byte{
				"credentials.json": []byte(gcpReq.ServiceAccountJSON),
			}); err != nil {
				http.Error(w, "Failed to store credentials: "+err.Error(), http.StatusInternalServerError)
				return
			}
			config.Spec.Providers.GCP.SecretRef = secretName
		}
	}

	// ─── Process AI (stub) ───────────────────────────────────────────────────
	if req.Integrations != nil && req.Integrations.AI != nil {
		aiReq := req.Integrations.AI
		if config.Spec.Integrations.AI == nil {
			config.Spec.Integrations.AI = &finopsv1.AIIntegrationConfig{}
		}
		if aiReq.Enabled != nil {
			config.Spec.Integrations.AI.Enabled = *aiReq.Enabled
		}
		if aiReq.Provider != "" {
			config.Spec.Integrations.AI.Provider = aiReq.Provider
		}
		if aiReq.Model != "" {
			config.Spec.Integrations.AI.Model = aiReq.Model
		}
		// BaseURL can be explicitly empty.
		if aiReq.BaseURL != "" {
			config.Spec.Integrations.AI.BaseURL = aiReq.BaseURL
		} else {
			config.Spec.Integrations.AI.BaseURL = ""
		}
		if aiReq.SkipSSLVerify != nil {
			config.Spec.Integrations.AI.SkipSSLVerify = *aiReq.SkipSSLVerify
		}
		if aiReq.APIKey != "" {
			secretName := "costdeck-ai-credentials"
			if err := s.upsertSecret(ctx, operatorNs, secretName, map[string][]byte{
				"API_KEY": []byte(aiReq.APIKey),
			}); err != nil {
				http.Error(w, "Failed to store credentials: "+err.Error(), http.StatusInternalServerError)
				return
			}
			config.Spec.Integrations.AI.SecretRef = secretName
		}
	}

	// ─── Process Webex ────────────────────────────────────────────────────
	if req.Integrations != nil && req.Integrations.Messenger != nil && req.Integrations.Messenger.Webex != nil {
		wxReq := req.Integrations.Messenger.Webex
		if config.Spec.Integrations.Messenger == nil {
			config.Spec.Integrations.Messenger = &finopsv1.MessengerIntegrationConfig{}
		}
		if config.Spec.Integrations.Messenger.Webex == nil {
			config.Spec.Integrations.Messenger.Webex = &finopsv1.WebexConfig{}
		}
		if wxReq.Enabled != nil {
			config.Spec.Integrations.Messenger.Webex.Enabled = *wxReq.Enabled
		}
		if wxReq.RoomID != "" {
			config.Spec.Integrations.Messenger.Webex.RoomID = wxReq.RoomID
		}
		if wxReq.BotToken != "" {
			secretName := "costdeck-webex-credentials"
			if err := s.upsertSecret(ctx, operatorNs, secretName, map[string][]byte{
				"BOT_TOKEN": []byte(wxReq.BotToken),
			}); err != nil {
				http.Error(w, "Failed to store credentials: "+err.Error(), http.StatusInternalServerError)
				return
			}
			config.Spec.Integrations.Messenger.Webex.SecretRef = secretName
		}
	}

	// ─── Process VictoriaMetrics ────────────────────────────────────────────
	if req.Integrations != nil && req.Integrations.VictoriaMetrics != nil {
		vmReq := req.Integrations.VictoriaMetrics
		if config.Spec.Integrations.VictoriaMetrics == nil {
			config.Spec.Integrations.VictoriaMetrics = &finopsv1.VictoriaMetricsConfig{}
		}
		if vmReq.Enabled != nil {
			config.Spec.Integrations.VictoriaMetrics.Enabled = *vmReq.Enabled
		}
		if vmReq.Endpoint != "" {
			config.Spec.Integrations.VictoriaMetrics.Endpoint = vmReq.Endpoint
		}
		if vmReq.RetentionDays != nil {
			config.Spec.Integrations.VictoriaMetrics.RetentionDays = *vmReq.RetentionDays
		}
		// Store credentials if provided
		if vmReq.BearerToken != "" || (vmReq.Username != "" && vmReq.Password != "") {
			secretName := "costdeck-vm-credentials"
			data := map[string][]byte{}
			if vmReq.BearerToken != "" {
				data["BEARER_TOKEN"] = []byte(vmReq.BearerToken)
			}
			if vmReq.Username != "" {
				data["USERNAME"] = []byte(vmReq.Username)
			}
			if vmReq.Password != "" {
				data["PASSWORD"] = []byte(vmReq.Password)
			}
			if err := s.upsertSecret(ctx, operatorNs, secretName, data); err != nil {
				http.Error(w, "Failed to store VM credentials: "+err.Error(), http.StatusInternalServerError)
				return
			}
			config.Spec.Integrations.VictoriaMetrics.SecretRef = secretName
		}
	}

	// ─── Process Features ────────────────────────────────────────────────────
	if req.Features != nil {
		if req.Features.CloudPricingAPI != nil {
			config.Spec.Features.CloudPricingAPI = *req.Features.CloudPricingAPI
		}
	}

	// Save the config
	if config.Spec.Integrations.AI != nil {
		logf.Log.Info("Saving AI config",
			"enabled", config.Spec.Integrations.AI.Enabled,
			"provider", config.Spec.Integrations.AI.Provider,
			"model", config.Spec.Integrations.AI.Model,
			"baseUrl", config.Spec.Integrations.AI.BaseURL,
			"secretRef", config.Spec.Integrations.AI.SecretRef,
			"skipSslVerify", config.Spec.Integrations.AI.SkipSSLVerify,
		)
	}
	if err := s.Client.Update(ctx, config); err != nil {
		http.Error(w, "Failed to update config: "+err.Error(), http.StatusInternalServerError)
		return
	}

	// Re-fetch to confirm persistence
	updated := &finopsv1.CostDeckConfig{}
	if err := s.Client.Get(ctx, client.ObjectKey{Name: config.Name, Namespace: config.Namespace}, updated); err == nil {
		if updated.Spec.Integrations.AI != nil {
			logf.Log.Info("Verified AI config after save",
				"baseUrl", updated.Spec.Integrations.AI.BaseURL,
				"provider", updated.Spec.Integrations.AI.Provider,
			)
		}
	}

	resp := s.buildSettingsResponse(ctx, config)
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(resp)
}

// ─── Test Provider Connectivity ─────────────────────────────────────────────

func (s *Server) handleTestProvider(w http.ResponseWriter, r *http.Request, providerName string) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	ctx := r.Context()
	config := s.getOrCreateDefaultConfig(ctx)

	switch providerName {
	case "aws":
		s.testAWSProvider(w, ctx, config, r)
	case "ai":
		s.testAIProvider(w, ctx, config, r)
	case "azure":
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]interface{}{
			"connected": false,
			"error":     "Azure provider is not yet implemented",
		})
	case "gcp":
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]interface{}{
			"connected": false,
			"error":     "GCP provider is not yet implemented",
		})
	default:
		http.Error(w, fmt.Sprintf("Unknown provider: %s", providerName), http.StatusBadRequest)
	}
}

func (s *Server) testAWSProvider(w http.ResponseWriter, ctx context.Context, config *finopsv1.CostDeckConfig, r *http.Request) {
	var provider *scaling.AWSProvider
	var err error

	// Try to parse credentials from the request body first (unsaved UI form data)
	var req AWSUpdateRequest
	if r.Body != nil {
		err := json.NewDecoder(r.Body).Decode(&req)
		if err == nil && req.AccessKeyID != "" && req.SecretAccessKey != "" {
			provider, err = scaling.NewAWSProviderFromCredentials(ctx, req.AccessKeyID, req.SecretAccessKey, req.Region)
			if err != nil {
				w.Header().Set("Content-Type", "application/json")
				json.NewEncoder(w).Encode(map[string]interface{}{
					"connected": false,
					"error":     fmt.Sprintf("Failed to initialize with provided credentials: %v", err),
				})
				return
			}
		}
	}

	// Fallback to stored credentials if no body or body belongs to another provider
	if provider == nil {
		if config.Spec.Providers.AWS == nil || config.Spec.Providers.AWS.SecretRef == "" {
			w.Header().Set("Content-Type", "application/json")
			json.NewEncoder(w).Encode(map[string]interface{}{
				"connected": false,
				"error":     "No AWS credentials configured",
			})
			return
		}

		provider, err = scaling.NewAWSProviderFromSecret(ctx, s.Client,
			config.Spec.Providers.AWS.SecretRef,
			config.Namespace,
			config.Spec.Providers.AWS.Region,
		)
		if err != nil {
			w.Header().Set("Content-Type", "application/json")
			json.NewEncoder(w).Encode(map[string]interface{}{
				"connected": false,
				"error":     err.Error(),
			})
			return
		}
	}

	if err := provider.ValidateConnectivity(ctx); err != nil {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]interface{}{
			"connected": false,
			"error":     err.Error(),
		})
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]interface{}{
		"connected": true,
	})
}

func (s *Server) testAIProvider(w http.ResponseWriter, ctx context.Context, config *finopsv1.CostDeckConfig, r *http.Request) {
	var req AIUpdateRequest

	providerType := ""
	baseUrl := ""
	apiKey := ""
	skipSslVerify := false

	// Try load from request body
	if r.Body != nil {
		json.NewDecoder(r.Body).Decode(&req)
		providerType = req.Provider
		baseUrl = req.BaseURL
		apiKey = req.APIKey
		if req.SkipSSLVerify != nil {
			skipSslVerify = *req.SkipSSLVerify
		}
	}

	// Fallback to config if not provided in UI body
	if providerType == "" && config.Spec.Integrations.AI != nil {
		providerType = config.Spec.Integrations.AI.Provider
		baseUrl = config.Spec.Integrations.AI.BaseURL
		skipSslVerify = config.Spec.Integrations.AI.SkipSSLVerify

		if apiKey == "" && config.Spec.Integrations.AI.SecretRef != "" {
			secret := &corev1.Secret{}
			err := s.Client.Get(ctx, client.ObjectKey{Name: config.Spec.Integrations.AI.SecretRef, Namespace: config.Namespace}, secret)
			if err == nil {
				apiKey = string(secret.Data["API_KEY"])
			}
		}
	}

	if providerType == "" {
		providerType = "openai"
	}

	// Mock ping for test connectivity logic.

	var apiUrl string
	if providerType == "openai" {
		if baseUrl == "" {
			baseUrl = "https://api.openai.com/v1"
		}
		apiUrl = strings.TrimSuffix(baseUrl, "/") + "/models"
	} else if providerType == "anthropic" {
		if baseUrl == "" {
			baseUrl = "https://api.anthropic.com/v1"
		}
		apiUrl = strings.TrimSuffix(baseUrl, "/") + "/messages"
	} else if providerType == "gemini" {
		if baseUrl == "" {
			baseUrl = "https://generativelanguage.googleapis.com/v1beta"
		}
		apiUrl = strings.TrimSuffix(baseUrl, "/") + "/models"
	} else {
		if baseUrl == "" {
			baseUrl = "http://localhost:11434/v1"
		}
		apiUrl = strings.TrimSuffix(baseUrl, "/") + "/models"
	}

	parsedUrl, err := url.ParseRequestURI(apiUrl)
	if err != nil || (parsedUrl.Scheme != "http" && parsedUrl.Scheme != "https") {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]interface{}{
			"connected": false,
			"error":     "Invalid URL format or scheme (must be http/https)",
		})
		return
	}

	host := parsedUrl.Hostname()
	if providerType != "local" {
		if host == "localhost" || host == "127.0.0.1" || host == "::1" || strings.HasPrefix(host, "169.254.") {
			w.Header().Set("Content-Type", "application/json")
			json.NewEncoder(w).Encode(map[string]interface{}{
				"connected": false,
				"error":     "Invalid host for cloud provider (local/internal IPs are forbidden)",
			})
			return
		}
	}

	safeUrl := &url.URL{
		Scheme:   parsedUrl.Scheme,
		Host:     parsedUrl.Host,
		Path:     parsedUrl.Path,
		RawQuery: parsedUrl.RawQuery,
	}

	httpReq, _ := http.NewRequest("GET", safeUrl.String(), nil)
	if apiKey != "" {
		if providerType == "gemini" {
			httpReq.Header.Set("x-goog-api-key", apiKey)
		} else if providerType == "anthropic" {
			httpReq.Header.Set("x-api-key", apiKey)
			httpReq.Header.Set("anthropic-version", "2023-06-01")
		} else {
			httpReq.Header.Set("Authorization", "Bearer "+apiKey)
		}
	}

	client := &http.Client{Timeout: 10 * time.Second}

	if skipSslVerify {
		client.Transport = &http.Transport{
			TLSClientConfig: &tls.Config{InsecureSkipVerify: true},
		}
	}

	// lgtm [go/request-forgery]
	// codeql[go/request-forgery]
	resp, err := client.Do(httpReq)
	if err != nil {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]interface{}{
			"connected": false,
			"error":     "Failed to reach AI endpoint: " + err.Error(),
		})
		return
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 400 && resp.StatusCode != 404 && resp.StatusCode != 405 {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]interface{}{
			"connected": false,
			"error":     fmt.Sprintf("AI provider returned error status: %d", resp.StatusCode),
		})
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]interface{}{
		"connected": true,
	})
}

// ─── Provider Status ────────────────────────────────────────────────────────

func (s *Server) handleProviderStatus(w http.ResponseWriter, r *http.Request, providerName string) {
	if r.Method != http.MethodGet {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	ctx := r.Context()
	config := s.getOrCreateDefaultConfig(ctx)

	var status *finopsv1.ProviderStatus
	switch providerName {
	case "aws":
		status = config.Status.AWS
	case "azure":
		status = config.Status.Azure
	case "gcp":
		status = config.Status.GCP
	default:
		http.Error(w, "Unknown provider", http.StatusBadRequest)
		return
	}

	if status == nil {
		status = &finopsv1.ProviderStatus{Connected: false, Error: "Provider not configured"}
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(status)
}

// ─── Helpers ────────────────────────────────────────────────────────────────

func (s *Server) getOrCreateDefaultConfig(ctx context.Context) *finopsv1.CostDeckConfig {
	operatorNs := getOperatorNamespace()

	config := &finopsv1.CostDeckConfig{}
	err := s.Client.Get(ctx, client.ObjectKey{Name: "default", Namespace: operatorNs}, config)
	if err != nil {
		if errors.IsNotFound(err) {
			// Create the default singleton
			config = &finopsv1.CostDeckConfig{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "default",
					Namespace: operatorNs,
				},
				Spec: finopsv1.CostDeckConfigSpec{},
			}
			if createErr := s.Client.Create(ctx, config); createErr != nil {
				logf.Log.Error(createErr, "Failed to create default CostDeckConfig")
			}
		} else {
			logf.Log.Error(err, "Failed to get CostDeckConfig")
		}
	}
	return config
}

func (s *Server) secretExists(ctx context.Context, name, namespace string) bool {
	secret := &corev1.Secret{}
	err := s.Client.Get(ctx, client.ObjectKey{Name: name, Namespace: namespace}, secret)
	return err == nil
}

func (s *Server) upsertSecret(ctx context.Context, namespace, name string, data map[string][]byte) error {
	secret := &corev1.Secret{}
	err := s.Client.Get(ctx, client.ObjectKey{Name: name, Namespace: namespace}, secret)

	if errors.IsNotFound(err) {
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
