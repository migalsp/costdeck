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

// AzureProviderConfig configures Azure: discovery, and start/stop of virtual machines
// (deallocated, so compute stops billing) and PostgreSQL/MySQL flexible servers.
type AzureProviderConfig struct {
	// Enabled toggles the Azure provider on/off
	// +optional
	Enabled bool `json:"enabled,omitempty"`

	// SecretRef names the Secret holding a service principal (AZURE_TENANT_ID,
	// AZURE_CLIENT_ID, AZURE_CLIENT_SECRET). Without it the pod identity is used (AKS
	// workload identity or a managed identity).
	// +optional
	SecretRef string `json:"secretRef,omitempty"`

	// SubscriptionID is the subscription whose resources are discovered and scaled.
	// +kubebuilder:validation:Pattern=`^$|^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}$`
	// +optional
	SubscriptionID string `json:"subscriptionId,omitempty"`

	// TenantID is the Entra ID tenant of the service principal.
	// +optional
	TenantID string `json:"tenantId,omitempty"`

	// DiscoveryTags limits discovery to resources carrying all of these tags.
	// +optional
	DiscoveryTags map[string]string `json:"discoveryTags,omitempty"`

	// ResourceTypes lists the types to discover and manage; all when empty.
	// +optional
	// +listType=set
	// +kubebuilder:validation:items:Enum=vm;postgres;mysql
	ResourceTypes []string `json:"resourceTypes,omitempty"`
}

// GCPProviderConfig configures Google Cloud: discovery, and start/stop of Compute Engine
// instances and Cloud SQL instances.
type GCPProviderConfig struct {
	// Enabled toggles the GCP provider on/off
	// +optional
	Enabled bool `json:"enabled,omitempty"`

	// SecretRef names the Secret holding a service account key (credentials.json).
	// Without it the pod identity is used (GKE workload identity).
	// +optional
	SecretRef string `json:"secretRef,omitempty"`

	// ProjectID is the project whose resources are discovered and scaled; defaults to the
	// project of the service account key.
	// +optional
	ProjectID string `json:"projectId,omitempty"`

	// DiscoveryLabels limits discovery to resources carrying all of these labels.
	// +optional
	DiscoveryLabels map[string]string `json:"discoveryLabels,omitempty"`

	// ResourceTypes lists the types to discover and manage; all when empty.
	// +optional
	// +listType=set
	// +kubebuilder:validation:items:Enum=gce;cloudsql
	ResourceTypes []string `json:"resourceTypes,omitempty"`
}

// ProvidersConfig groups all cloud provider configurations.
type ProvidersConfig struct {
	// AWS provider configuration
	// +optional
	AWS *AWSProviderConfig `json:"aws,omitempty"`

	// Azure provider configuration
	// +optional
	Azure *AzureProviderConfig `json:"azure,omitempty"`

	// GCP provider configuration
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

	// RoomID is the Webex space the bot answers in and posts notifications to. When empty
	// the bot answers in every space it is a member of.
	// +optional
	RoomID string `json:"roomId,omitempty"`

	// NotifyTransitions posts to RoomID whenever a ScalingGroup or ScalingConfig finishes
	// scaling up or down, with who triggered it and the estimated savings.
	// +optional
	NotifyTransitions bool `json:"notifyTransitions,omitempty"`

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

	// RetentionDays is the lookback window for historical queries. Right-sizing advice
	// uses it, capped at 14 days.
	// +kubebuilder:validation:Minimum=1
	// +kubebuilder:default=7
	// +optional
	RetentionDays int `json:"retentionDays,omitempty"`
}

// MCPConfig configures the built-in Model Context Protocol endpoint.
type MCPConfig struct {
	// Enabled serves MCP (Streamable HTTP) at /mcp on the dashboard's host and port,
	// behind the same authentication as the API: a session or an API token.
	// +optional
	Enabled bool `json:"enabled,omitempty"`

	// Deprecated: MCP is served on the API port at /mcp; this field is ignored.
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

	// TenantID is the directory (tenant) ID or a verified domain. "organizations" accepts
	// work accounts from any tenant (multi-tenant app registration); then only
	// GroupRoleMapping grants access, because app roles and auto-provisioning could be
	// abused from a foreign tenant.
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
	// (https://login.microsoftonline.us, https://login.chinacloudapi.cn). It must be
	// HTTPS: the client secret is sent to it.
	// +kubebuilder:validation:Pattern=`^https://[^/\s]+/?$`
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
	// CloudPricingAPI prices the cluster's nodes with the AWS Price List API (on-demand,
	// Linux) and derives the per-core and per-GiB rates from them. Needs the
	// pricing:GetProducts permission. Ignored when custom rates are set in spec.pricing.
	// +optional
	CloudPricingAPI bool `json:"cloudPricingApi,omitempty"`
}

// PricingConfig sets the rates used for every cost estimate. Set both rates to use them
// (on-premises clusters, negotiated discounts); leave them empty to use cloud or
// heuristic list prices.
type PricingConfig struct {
	// CPUCoreHour is the price of one CPU core for one hour, e.g. "0.031".
	// +kubebuilder:validation:Pattern=`^[0-9]+(\.[0-9]+)?$`
	// +optional
	CPUCoreHour string `json:"cpuCoreHour,omitempty"`

	// MemoryGBHour is the price of one GiB of memory for one hour, e.g. "0.0042".
	// +kubebuilder:validation:Pattern=`^[0-9]+(\.[0-9]+)?$`
	// +optional
	MemoryGBHour string `json:"memoryGiBHour,omitempty"`

	// Currency is the ISO 4217 code shown next to costs. Defaults to USD.
	// +kubebuilder:validation:Pattern=`^[A-Za-z]{3}$`
	// +optional
	Currency string `json:"currency,omitempty"`

	// StorageGiBMonth is the price of one GiB of persistent volume for a month, e.g.
	// "0.08". Without it volumes are priced at the cloud's list price for their disk type.
	// +kubebuilder:validation:Pattern=`^[0-9]+(\.[0-9]+)?$`
	// +optional
	StorageGiBMonth string `json:"storageGiBMonth,omitempty"`

	// LoadBalancerMonth is the price of one LoadBalancer Service for a month, before
	// traffic, e.g. "18". Without it the cloud's list price is used.
	// +kubebuilder:validation:Pattern=`^[0-9]+(\.[0-9]+)?$`
	// +optional
	LoadBalancerMonth string `json:"loadBalancerMonth,omitempty"`
}

// ReportsConfig schedules the reports CostDeck sends by itself.
type ReportsConfig struct {
	// Digest posts a cost summary to the Webex space on a schedule.
	// +optional
	Digest DigestSchedule `json:"digest,omitempty"`
}

// DigestSchedule says when the cost digest is sent.
type DigestSchedule struct {
	// Enabled turns the scheduled digest on. It needs Webex with a space ID.
	// +optional
	Enabled bool `json:"enabled,omitempty"`

	// Frequency is weekly (sent on Mondays, covering the previous Monday to Sunday) or
	// monthly (sent on the 1st, covering the previous month).
	// +kubebuilder:validation:Enum=weekly;monthly
	// +optional
	Frequency string `json:"frequency,omitempty"`

	// Time of day to send it, HH:MM in Timezone. Defaults to 09:00.
	// +kubebuilder:validation:Pattern=`^([01][0-9]|2[0-3]):[0-5][0-9]$`
	// +optional
	Time string `json:"time,omitempty"`

	// Timezone is an IANA time zone such as Europe/Berlin. Defaults to UTC.
	// +optional
	Timezone string `json:"timezone,omitempty"`
}

// Budget caps what part of the cluster may cost in a calendar month.
type Budget struct {
	// Name identifies the budget in the dashboard and in alerts.
	// +kubebuilder:validation:Pattern=`^[a-z0-9]([-a-z0-9]*[a-z0-9])?$`
	// +kubebuilder:validation:MaxLength=63
	Name string `json:"name"`

	// Scope is what the budget covers: the whole cluster, namespaces, a team (the
	// namespaces whose team label has Value) or an environment.
	// +kubebuilder:validation:Enum=cluster;namespace;team;environment
	Scope string `json:"scope"`

	// Value names the team or environment (production, non-production, system or
	// unclassified), or one namespace. Empty for the cluster.
	// +optional
	Value string `json:"value,omitempty"`

	// Namespaces are the namespaces a namespace budget covers together, besides Value.
	// +kubebuilder:validation:MaxItems=500
	// +kubebuilder:validation:items:MaxLength=63
	// +kubebuilder:validation:items:Pattern=`^[a-z0-9]([-a-z0-9]*[a-z0-9])?$`
	// +listType=set
	// +optional
	Namespaces []string `json:"namespaces,omitempty"`

	// MonthlyLimit is the budget for a calendar month in the cost currency, e.g. "1200".
	// +kubebuilder:validation:Pattern=`^[0-9]+(\.[0-9]+)?$`
	MonthlyLimit string `json:"monthlyLimit"`

	// Thresholds are percentages of the limit that send an alert when spending reaches
	// them. Defaults to 80 and 100.
	// +kubebuilder:validation:MaxItems=5
	// +listType=set
	// +optional
	Thresholds []int `json:"thresholds,omitempty"`

	// Forecast also alerts, once a month, when spending is forecast to exceed the limit.
	// +optional
	Forecast bool `json:"forecast,omitempty"`
}

// AlertsConfig configures cost alerts besides budgets.
type AlertsConfig struct {
	// Anomalies alerts when a namespace's daily cost jumps above its recent average.
	// +optional
	Anomalies AnomalyAlerts `json:"anomalies,omitempty"`
}

// AnomalyAlerts configures daily cost anomaly alerts.
type AnomalyAlerts struct {
	// Enabled turns anomaly alerts on.
	// +optional
	Enabled bool `json:"enabled,omitempty"`

	// Percent above the average of the seven days before that counts as an anomaly.
	// Defaults to 30.
	// +kubebuilder:validation:Minimum=5
	// +kubebuilder:validation:Maximum=1000
	// +optional
	Percent int `json:"percent,omitempty"`

	// MinimumDaily is the smallest daily increase worth an alert, in the cost currency,
	// so small namespaces do not page anyone. Defaults to "1".
	// +kubebuilder:validation:Pattern=`^[0-9]+(\.[0-9]+)?$`
	// +optional
	MinimumDaily string `json:"minimumDaily,omitempty"`
}

// BillingConfig reconciles cost estimates with the cloud bill, so discounts (Savings
// Plans, Reserved Instances, committed use, enterprise agreements) and spot prices show up
// in every figure. It uses the credentials of the matching cloud provider.
type BillingConfig struct {
	// Enabled turns reconciliation on.
	// +optional
	Enabled bool `json:"enabled,omitempty"`

	// AWS reads the amortized EC2 cost from Cost Explorer (ce:GetCostAndUsage) for the
	// instances that carry the cluster's tag. The tag must be activated as a cost
	// allocation tag in the billing console.
	// +optional
	AWS *AWSBilling `json:"aws,omitempty"`

	// Azure reads the amortized virtual machine cost of the cluster's node resource group
	// from Cost Management (Cost Management Reader on that group).
	// +optional
	Azure *AzureBilling `json:"azure,omitempty"`

	// GCP reads the Compute Engine cost of the cluster's nodes, credits included, from the
	// billing export in BigQuery (BigQuery Job User, and Data Viewer on the dataset).
	// +optional
	GCP *GCPBilling `json:"gcp,omitempty"`
}

// AWSBilling selects the cluster's instances in Cost Explorer.
type AWSBilling struct {
	// TagKey is the cost allocation tag on the cluster's instances. Defaults to
	// aws:eks:cluster-name.
	// +optional
	TagKey string `json:"tagKey,omitempty"`
	// TagValue is the tag's value, usually the cluster name.
	TagValue string `json:"tagValue"`
}

// AzureBilling selects the cluster's virtual machines in Cost Management.
type AzureBilling struct {
	// ResourceGroup is the AKS node resource group, e.g. MC_rg_cluster_westeurope.
	// +kubebuilder:validation:Pattern=`^[-\w._()]{1,90}$`
	ResourceGroup string `json:"resourceGroup"`
}

// GCPBilling selects the cluster's nodes in the BigQuery billing export.
type GCPBilling struct {
	// Table is the billing export table, project.dataset.table.
	// +kubebuilder:validation:Pattern=`^[a-z][-a-z0-9]{4,28}[a-z0-9]\.[A-Za-z0-9_]+\.[A-Za-z0-9_]+$`
	Table string `json:"table"`
	// ClusterName is the GKE cluster name, matched against the goog-k8s-cluster-name label.
	ClusterName string `json:"clusterName"`
}

// BillingStatus is the result of the last reconciliation.
type BillingStatus struct {
	// Source names the bill, e.g. "AWS Cost Explorer".
	// +optional
	Source string `json:"source,omitempty"`
	// Factor is billed cost over list price for the compared days; it scales every
	// compute cost. Below 1 means discounts.
	// +optional
	Factor string `json:"factor,omitempty"`
	// From and To are the first and last compared days (UTC, inclusive).
	// +optional
	From string `json:"from,omitempty"`
	// +optional
	To string `json:"to,omitempty"`
	// Billed and List are the compared totals.
	// +optional
	Billed string `json:"billed,omitempty"`
	// +optional
	List string `json:"list,omitempty"`
	// Currency of Billed.
	// +optional
	Currency string `json:"currency,omitempty"`
	// LastChecked is when the bill was last read.
	// +optional
	LastChecked metav1.Time `json:"lastChecked,omitempty"`
	// LastReconciled is when Factor was last computed.
	// +optional
	LastReconciled metav1.Time `json:"lastReconciled,omitempty"`
	// Error says why the last attempt failed.
	// +optional
	Error string `json:"error,omitempty"`
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

	// Pricing sets custom cost rates.
	// +optional
	Pricing PricingConfig `json:"pricing,omitempty"`

	// Reports schedules the cost digest.
	// +optional
	Reports ReportsConfig `json:"reports,omitempty"`

	// Budgets cap monthly spending of the cluster, namespaces, teams or environments and
	// alert when they are reached or forecast to be exceeded.
	// +kubebuilder:validation:MaxItems=100
	// +listType=map
	// +listMapKey=name
	// +optional
	Budgets []Budget `json:"budgets,omitempty"`

	// Alerts configures cost anomaly alerts.
	// +optional
	Alerts AlertsConfig `json:"alerts,omitempty"`

	// Billing reconciles estimates with the cloud bill.
	// +optional
	Billing BillingConfig `json:"billing,omitempty"`
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

	// Billing is the last reconciliation with the cloud bill.
	// +optional
	Billing *BillingStatus `json:"billing,omitempty"`

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
