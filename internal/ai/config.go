package ai

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"

	"sigs.k8s.io/controller-runtime/pkg/client"

	finopsv1 "github.com/migalsp/costdeck-operator/api/v1"
	"github.com/migalsp/costdeck-operator/internal/config"
)

// Provider identifiers stored in CostDeckConfig.spec.integrations.ai.provider.
const (
	ProviderAnthropic = "anthropic"
	ProviderOpenAI    = "openai"
	ProviderGemini    = "gemini"
	// ProviderLocal is any OpenAI-compatible endpoint (Ollama, vLLM, LM Studio, a gateway).
	ProviderLocal = "local"
)

// SecretKeyAPIKey is the key of the API key in the AI credentials Secret.
const SecretKeyAPIKey = "API_KEY"

// DefaultModels is used when no model is configured.
var DefaultModels = map[string]string{
	ProviderAnthropic: "claude-opus-5-5",
	ProviderOpenAI:    "gpt-4o",
	ProviderGemini:    "gemini-2.5-flash",
}

var defaultBaseURLs = map[string]string{
	ProviderOpenAI: "https://api.openai.com/v1",
	ProviderGemini: "https://generativelanguage.googleapis.com/v1beta",
	ProviderLocal:  "http://localhost:11434/v1",
}

// Settings is a resolved AI configuration.
type Settings struct {
	Provider      string
	Model         string
	BaseURL       string
	APIKey        string
	SkipSSLVerify bool
}

// ErrDisabled means the AI integration is switched off.
var ErrDisabled = errors.New("AI features are disabled; enable them under Settings → AI Models")

// LoadSettings reads the AI configuration and API key from the CostDeckConfig.
func LoadSettings(ctx context.Context, c client.Reader) (*Settings, error) {
	cfg, err := config.Get(ctx, c)
	if err != nil {
		return nil, err
	}
	ai := cfg.Spec.Integrations.AI
	if ai == nil || !ai.Enabled {
		return nil, ErrDisabled
	}
	return FromConfig(ctx, c, ai, "")
}

// FromConfig resolves settings from an AI config. apiKey overrides the stored Secret
// (used to test unsaved settings).
func FromConfig(ctx context.Context, c client.Reader, ai *finopsv1.AIIntegrationConfig, apiKey string) (*Settings, error) {
	s := &Settings{
		Provider:      strings.ToLower(strings.TrimSpace(ai.Provider)),
		Model:         strings.TrimSpace(ai.Model),
		BaseURL:       strings.TrimSpace(ai.BaseURL),
		APIKey:        apiKey,
		SkipSSLVerify: ai.SkipSSLVerify,
	}
	if s.Provider == "" {
		s.Provider = ProviderOpenAI
	}
	if s.Model == "" {
		s.Model = DefaultModels[s.Provider]
	}
	if s.APIKey == "" && ai.SecretRef != "" {
		data, err := config.SecretData(ctx, c, ai.SecretRef)
		if err != nil {
			return nil, err
		}
		s.APIKey = string(data[SecretKeyAPIKey])
	}
	return s, s.Validate()
}

// Validate checks the provider, model and endpoint. Cloud providers may not be pointed at
// loopback or link-local addresses (cloud metadata endpoints); only the "local" provider
// may talk to in-cluster or on-host model servers.
func (s *Settings) Validate() error {
	switch s.Provider {
	case ProviderAnthropic, ProviderOpenAI, ProviderGemini, ProviderLocal:
	default:
		return fmt.Errorf("unsupported AI provider %q", s.Provider)
	}
	if s.Model == "" {
		return fmt.Errorf("no model configured for provider %q", s.Provider)
	}
	if s.BaseURL == "" {
		return nil
	}
	u, err := url.Parse(s.BaseURL)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return fmt.Errorf("invalid AI base URL %q: must be an http(s) URL", s.BaseURL)
	}
	if s.Provider != ProviderLocal && !allowInternalHosts && isInternalHost(u.Hostname()) {
		return fmt.Errorf("the %s provider may not use the internal address %q; use the local provider for self-hosted models", s.Provider, u.Hostname())
	}
	return nil
}

// allowInternalHosts lets tests point cloud providers at an httptest server.
var allowInternalHosts = false

func isInternalHost(host string) bool {
	if strings.EqualFold(host, "localhost") {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && (ip.IsLoopback() || ip.IsLinkLocalUnicast() || ip.IsLinkLocalMulticast() || ip.IsUnspecified())
}

// baseURL returns the configured base URL or the provider default.
func (s *Settings) baseURL() string {
	if s.BaseURL != "" {
		return strings.TrimRight(s.BaseURL, "/")
	}
	return defaultBaseURLs[s.Provider]
}

// httpClient builds the client for provider calls. There is no overall timeout: answers
// stream for minutes. Connection setup and the first response byte are bounded instead.
func (s *Settings) httpClient() *http.Client {
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.ResponseHeaderTimeout = 2 * time.Minute
	if s.SkipSSLVerify {
		transport.TLSClientConfig = &tls.Config{InsecureSkipVerify: true} //nolint:gosec // Explicit, admin-controlled opt-in.
	}
	return &http.Client{Transport: transport}
}

// NewProvider returns the provider for the settings.
func NewProvider(s *Settings) (Provider, error) {
	if err := s.Validate(); err != nil {
		return nil, err
	}
	switch s.Provider {
	case ProviderAnthropic:
		return newAnthropic(s), nil
	case ProviderGemini:
		return &geminiProvider{s: s}, nil
	default:
		return &openAIProvider{s: s}, nil
	}
}
