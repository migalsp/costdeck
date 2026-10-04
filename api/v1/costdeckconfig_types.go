/*
Copyright 2026 migalsp.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package v1

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// ─── Cloud Providers ─────────────────────────────────────────────────────────

// AWSProviderConfig holds configuration for the AWS cloud provider.
type AWSProviderConfig struct {
	// Enabled toggles the AWS provider on/off
	// +optional
	Enabled bool `json:"enabled,omitempty"`

	// Region is the default AWS region (e.g. "us-east-1")
	// +kubebuilder:validation:Pattern=`^[a-z]{2}-[a-z]+-\d$`
	// +optional
	Region string `json:"region,omitempty"`

	// SecretRef is the name of the K8s Secret holding AWS credentials
	// (keys: AWS_ACCESS_KEY_ID, AWS_SECRET_ACCESS_KEY)
	// +optional
	SecretRef string `json:"secretRef,omitempty"`

	// DiscoveryTags defines tag key-value pairs used to filter AWS resources during discovery.
	// Only resources matching ALL specified tags will be discovered.
	// +optional
	DiscoveryTags map[string]string `json:"discoveryTags,omitempty"`

	// ResourceTypes lists which AWS resource types to discover and manage.
	// Supported values: "aurora", "ec2"
	// +optional
	// +listType=set
	ResourceTypes []string `json:"resourceTypes,omitempty"`
}

// AzureProviderConfig holds configuration for the Azure cloud provider (stub).
type AzureProviderConfig struct {
	// Enabled toggles the Azure provider on/off
	// +optional
	Enabled bool `json:"enabled,omitempty"`

	// SecretRef is the name of the K8s Secret holding Azure credentials
	// +optional
	SecretRef string `json:"secretRef,omitempty"`

	// SubscriptionID is the Azure subscription ID
	// +optional
	SubscriptionID string `json:"subscriptionId,omitempty"`

	// TenantID is the Azure AD tenant ID
	// +optional
	TenantID string `json:"tenantId,omitempty"`
}

// GCPProviderConfig holds configuration for the GCP cloud provider (stub).
type GCPProviderConfig struct {
	// Enabled toggles the GCP provider on/off
	// +optional
	Enabled bool `json:"enabled,omitempty"`

	// SecretRef is the name of the K8s Secret holding GCP service account JSON
	// +optional
	SecretRef string `json:"secretRef,omitempty"`

	// ProjectID is the GCP project ID
	// +optional
	ProjectID string `json:"projectId,omitempty"`
}

// ProvidersConfig groups all cloud provider configurations.
type ProvidersConfig struct {
	// AWS provider configuration
	// +optional
	AWS *AWSProviderConfig `json:"aws,omitempty"`

	// Azure provider configuration (coming soon)
	// +optional
	Azure *AzureProviderConfig `json:"azure,omitempty"`

	// GCP provider configuration (coming soon)
	// +optional
	GCP *GCPProviderConfig `json:"gcp,omitempty"`
}

// ─── Integrations ────────────────────────────────────────────────────────────

// AIIntegrationConfig configures the assistant and AI reports.
type AIIntegrationConfig struct {
	// Enabled toggles the AI integration on/off
	// +optional
	Enabled bool `json:"enabled,omitempty"`

	// Provider is anthropic, openai, gemini, or local (any OpenAI-compatible endpoint such
	// as Ollama, vLLM or an internal gateway).
	// +kubebuilder:validation:Enum=anthropic;openai;gemini;local
	// +optional
	Provider string `json:"provider,omitempty"`

	// Model is the model ID, e.g. "claude-opus-5-5". Defaults per provider when empty;
	// the settings page lists the models the configured key can use.
	// +optional
	Model string `json:"model,omitempty"`

	// BaseURL overrides the provider endpoint (gateways, proxies, self-hosted models).
	// Cloud providers may not point at loopback or link-local addresses.
	// +optional
	BaseURL string `json:"baseUrl,omitempty"`

	// SecretRef is the name of the K8s Secret holding the AI API key
	// +optional
	SecretRef string `json:"secretRef,omitempty"`

	// SkipSSLVerify allows skipping TLS certificate verification for custom endpoints
	// +optional
	SkipSSLVerify bool `json:"skipSslVerify,omitempty"`
}

// WebexConfig configures the Webex bot.
type WebexConfig struct {
	// Enabled toggles the Webex integration on/off
	// +optional
	Enabled bool `json:"enabled,omitempty"`

	// RoomID is the Webex room/space ID for notifications
	// +optional
	RoomID string `json:"roomId,omitempty"`

	// SecretRef is the name of the K8s Secret holding the Webex bot token (key BOT_TOKEN).
	// Adding a WEBHOOK_SECRET key switches delivery from polling to signed webhooks
	// (POST /api/webex/webhook).
	// +optional
	SecretRef string `json:"secretRef,omitempty"`
}

// MessengerIntegrationConfig groups all messenger configurations.
type MessengerIntegrationConfig struct {
	// Webex bot
	// +optional
	Webex *WebexConfig `json:"webex,omitempty"`
}

// VictoriaMetricsConfig holds configuration for VictoriaMetrics integration.
type VictoriaMetricsConfig struct {
	// Enabled toggles VictoriaMetrics as the metrics source (instead of metrics-server)
	// +optional
	Enabled bool `json:"enabled,omitempty"`

	// Endpoint is the PromQL-compatible query URL
	// e.g. "http://vmselect.monitoring.svc:8481/select/0/prometheus"
	// +optional
	Endpoint string `json:"endpoint,omitempty"`

	// SecretRef is the name of the K8s Secret holding VictoriaMetrics credentials
	// (keys: BEARER_TOKEN or USERNAME/PASSWORD, and optionally CA_CERT with a PEM bundle)
	// +optional
	SecretRef string `json:"secretRef,omitempty"`

	// LabelSelector is added to every query as extra PromQL label matchers, e.g.
	// `cluster="prod-eu"`. Required when one VictoriaMetrics instance stores several
	// clusters: without it, namespaces with the same name are summed across clusters.
	// +kubebuilder:validation:MaxLength=512
	// +optional
	LabelSelector string `json:"labelSelector,omitempty"`

	// SkipSSLVerify disables TLS certificate verification for the endpoint.
	// Prefer adding CA_CERT to the credentials Secret instead.
	// +optional
	SkipSSLVerify bool `json:"skipSslVerify,omitempty"`

	// RetentionDays is the lookback window for historical queries.
	// Used by the Optimize feature to compute resource recommendations.
	// +kubebuilder:validation:Minimum=1
	// +kubebuilder:default=7
	// +optional
	RetentionDays int `json:"retentionDays,omitempty"`
}

// MCPConfig holds configuration for the built-in MCP server.
type MCPConfig struct {
	// Enabled toggles the MCP server on/off
	// +optional
	Enabled bool `json:"enabled,omitempty"`

	// Port is the HTTP port the MCP server will listen on for SSE connections
	// +kubebuilder:default=8083
	// +optional
	Port int `json:"port,omitempty"`
}

// IntegrationsConfig groups all integration configurations.
type IntegrationsConfig struct {
	// AI assistant and reports
	// +optional
	AI *AIIntegrationConfig `json:"ai,omitempty"`

	// Messenger integrations
	// +optional
	Messenger *MessengerIntegrationConfig `json:"messenger,omitempty"`

	// VictoriaMetrics metrics integration
	// +optional
	VictoriaMetrics *VictoriaMetricsConfig `json:"victoriaMetrics,omitempty"`

	// MCP Server configuration
	// +optional
	MCP *MCPConfig `json:"mcp,omitempty"`
}

// ─── Authentication ──────────────────────────────────────────────────────────

// EntraConfig configures Microsoft Entra ID (Azure AD) single sign-on using the OpenID
// Connect authorization code flow with PKCE.
// +kubebuilder:validation:XValidation:rule="!self.enabled || (size(self.tenantId) > 0 && size(self.clientId) > 0)",message="tenantId and clientId are required when Entra SSO is enabled"
type EntraConfig struct {
	// Enabled shows the "Sign in with Microsoft" button and accepts Entra ID sign-ins.
	// +optional
	Enabled bool `json:"enabled,omitempty"`

	// TenantID is the directory (tenant) ID or a verified domain. Use "organizations" to
	// accept any work account (multi-tenant app registration).
	// +kubebuilder:validation:MaxLength=128
	// +optional
	TenantID string `json:"tenantId,omitempty"`

	// ClientID is the application (client) ID of the app registration.
	// +kubebuilder:validation:MaxLength=128
	// +optional
	ClientID string `json:"clientId,omitempty"`

	// ClientSecretRef names the Secret holding the client secret under CLIENT_SECRET.
	// +optional
	ClientSecretRef string `json:"clientSecretRef,omitempty"`

	// RedirectURL must match a redirect URI of the app registration exactly. Either
	// https://<host>/api/auth/entra/callback (server-side) or https://<host>/auth/callback
	// (single-page app). Derived from the request host when empty.
	// +kubebuilder:validation:MaxLength=512
	// +optional
	RedirectURL string `json:"redirectUrl,omitempty"`

	// AuthorityHost is the Entra login endpoint; override it for sovereign clouds
	// (https://login.microsoftonline.us, https://login.chinacloudapi.cn).
	// +kubebuilder:default="https://login.microsoftonline.com"
	// +optional
	AuthorityHost string `json:"authorityHost,omitempty"`

	// DefaultRole is granted to users who match no group or app role mapping:
	// viewer, operator or admin.
	// +kubebuilder:validation:Enum=viewer;operator;admin
	// +kubebuilder:default=viewer
	// +optional
	DefaultRole string `json:"defaultRole,omitempty"`

	// AutoProvision lets any user of the tenant sign in with DefaultRole. When false, only
	// users matched by GroupRoleMapping or an app role may sign in.
	// +kubebuilder:default=true
	// +optional
	AutoProvision *bool `json:"autoProvision,omitempty"`

	// GroupRoleMapping maps Entra group object IDs (the "groups" claim) to a role. The most
	// privileged match wins. App roles named admin/operator/viewer are honoured as well.
	// +optional
	GroupRoleMapping map[string]string `json:"groupRoleMapping,omitempty"`

	// SkipSSLVerify disables TLS verification towards Entra. Only for SSL-inspecting
	// corporate proxies.
	// +optional
	SkipSSLVerify bool `json:"skipSslVerify,omitempty"`
}

// AuthConfig configures how users sign in to the dashboard and the API.
type AuthConfig struct {
	// Entra configures Microsoft Entra ID single sign-on.
	// +optional
	Entra *EntraConfig `json:"entra,omitempty"`

	// DisableLocalLogin hides the username/password form once SSO works. The built-in
	// admin account (COSTDECK_AUTH_USER/PASSWORD) then only serves as break-glass access
	// through the API.
	// +optional
	DisableLocalLogin bool `json:"disableLocalLogin,omitempty"`
}

// ─── Features ────────────────────────────────────────────────────────────────

// FeaturesConfig holds configuration for core CostDeck features.
type FeaturesConfig struct {
	// CloudPricingAPI toggles whether to use Public Cloud Pricing API (e.g., AWS Pricing API) instead of heuristic math calculations.
	// +optional
	CloudPricingAPI bool `json:"cloudPricingApi,omitempty"`
}

// ─── CostDeckConfig CRD ─────────────────────────────────────────────────────

// CostDeckConfigSpec defines the desired state of CostDeckConfig.
type CostDeckConfigSpec struct {
	// ClusterName defines an optional identifier for this cluster.
	// Used in multi-cluster environments to distinguish bot commands.
	// +optional
	ClusterName string `json:"clusterName,omitempty"`

	// Providers holds cloud provider configurations
	// +optional
	Providers ProvidersConfig `json:"providers,omitempty"`

	// Integrations holds AI and messenger configurations
	// +optional
	Integrations IntegrationsConfig `json:"integrations,omitempty"`

	// Features holds core CostDeck feature toggles
	// +optional
	Features FeaturesConfig `json:"features,omitempty"`

	// Auth configures single sign-on and local login.
	// +optional
	Auth AuthConfig `json:"auth,omitempty"`
}

// ProviderStatus represents the connection status of a single provider.
type ProviderStatus struct {
	// Connected indicates whether the provider credentials are valid and the provider is reachable
	Connected bool `json:"connected"`

	// LastChecked is when connectivity was last verified
	// +optional
	LastChecked metav1.Time `json:"lastChecked,omitempty"`

	// Error contains the last error message if connection failed
	// +optional
	Error string `json:"error,omitempty"`

	// Message describes a successful check, e.g. which identity the credentials map to.
	// +optional
	Message string `json:"message,omitempty"`

	// DiscoveredResources is the count of resources found during the last discovery
	// +optional
	DiscoveredResources int `json:"discoveredResources,omitempty"`
}

// CostDeckConfigStatus defines the observed state of CostDeckConfig.
type CostDeckConfigStatus struct {
	// AWS provider status
	// +optional
	AWS *ProviderStatus `json:"aws,omitempty"`

	// Azure provider status
	// +optional
	Azure *ProviderStatus `json:"azure,omitempty"`

	// GCP provider status
	// +optional
	GCP *ProviderStatus `json:"gcp,omitempty"`

	// VictoriaMetrics reports whether the configured metrics endpoint is reachable and
	// actually holds the container metrics CostDeck queries.
	// +optional
	VictoriaMetrics *ProviderStatus `json:"victoriaMetrics,omitempty"`

	// Webex reports whether the bot token is valid and the bot can see its space.
	// +optional
	Webex *ProviderStatus `json:"webex,omitempty"`

	// Conditions represent the current state of the CostDeckConfig resource.
	// +optional
	Conditions []metav1.Condition `json:"conditions,omitempty"`
}

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:resource:scope=Namespaced,shortName=cdc

// CostDeckConfig is the Schema for the costdeckconfigs API.
// It is a singleton resource (one per namespace, conventionally named "default")
// that holds all provider and integration settings.
type CostDeckConfig struct {
	metav1.TypeMeta `json:",inline"`

	// metadata is a standard object metadata
	// +optional
	metav1.ObjectMeta `json:"metadata,omitzero"`

	// spec defines the desired configuration
	// +required
	Spec CostDeckConfigSpec `json:"spec"`

	// status defines the observed state
	// +optional
	Status CostDeckConfigStatus `json:"status,omitzero"`
}

// +kubebuilder:object:root=true

// CostDeckConfigList contains a list of CostDeckConfig
type CostDeckConfigList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitzero"`
	Items           []CostDeckConfig `json:"items"`
}

func init() {
	SchemeBuilder.Register(&CostDeckConfig{}, &CostDeckConfigList{})
}
