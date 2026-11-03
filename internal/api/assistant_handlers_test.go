package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	corev1 "k8s.io/api/core/v1"

	finopsv1 "github.com/migalsp/costdeck-operator/api/v1"
	"github.com/migalsp/costdeck-operator/internal/ai"
)

const storedAIKey = "stored-ai-key"

func modelCatalog(t *testing.T, models ...string) (*httptest.Server, *requestLog) {
	t.Helper()
	return recordingServer(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/models" {
			http.NotFound(w, r)
			return
		}
		data := make([]map[string]string, 0, len(models))
		for _, m := range models {
			data = append(data, map[string]string{"id": m})
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"data": data})
	})
}

func storedAI(t *testing.T, server *Server, provider string) {
	t.Helper()
	createAll(t, server.Client, &corev1.Secret{Name: "costdeck-ai-credentials", Namespace: operatorNamespace, Data: map[string][]byte{ai.SecretKeyAPIKey: []byte(storedAIKey)}})
	seedConfig(t, server, finopsv1.CostDeckConfigSpec{Integrations: finopsv1.IntegrationsConfig{
		AI: &finopsv1.AIIntegrationConfig{Enabled: true, Provider: provider, Model: "llama3", SecretRef: "costdeck-ai-credentials"},
	}})
}

func TestUserMessageExplainsAgentErrors(t *testing.T) {
	for _, tc := range []struct {
		err  error
		want string
	}{
		{fmt.Errorf("anthropic: %w", ai.ErrRefused), "The model declined to answer this request. Rephrase it, or pick another model under Settings → AI Models."},
		{fmt.Errorf("stream: %w", context.Canceled), "The request was cancelled."},
		{errors.New("could not reach local: connection refused"), "could not reach local: connection refused"},
	} {
		if got := userMessage(tc.err); got != tc.want {
			t.Errorf("userMessage(%v) = %q, want %q", tc.err, got, tc.want)
		}
	}
}

func TestAIModelsListsTheModelsWithTheStoredKey(t *testing.T) {
	t.Setenv("POD_NAMESPACE", operatorNamespace)
	server := buildMockServerWithK8s()
	storedAI(t, server, "local")
	catalog, calls := modelCatalog(t, "qwen", "llama3")

	rr := send(t, server, http.MethodPost, "/api/settings/ai/models", `{"baseUrl":"`+catalog.URL+`/v1"}`)
	if rr.Code != http.StatusOK {
		t.Fatalf("models = %d %s", rr.Code, rr.Body.String())
	}
	got := decodeInto[struct {
		Models  []string `json:"models"`
		Default string   `json:"default"`
	}](t, rr)
	if strings.Join(got.Models, ",") != "llama3,qwen" || got.Default != "" {
		t.Errorf("models = %+v, want the sorted catalog and no default for a local model server", got)
	}
	if c := calls.all(); len(c) != 1 || c[0].Authorization != "Bearer "+storedAIKey {
		t.Errorf("model server calls = %+v, want the stored key", c)
	}

	send(t, server, http.MethodPost, "/api/settings/ai/models", `{"baseUrl":"`+catalog.URL+`/v1","apiKey":"typed-key"}`)
	if c := calls.all(); len(c) != 2 || c[1].Authorization != "Bearer typed-key" {
		t.Errorf("an unsaved key typed into the form must win, calls = %+v", c)
	}
}

func TestAIModelsNeverSendsTheStoredKeyToAnotherProvider(t *testing.T) {
	t.Setenv("POD_NAMESPACE", operatorNamespace)
	server := buildMockServerWithK8s()
	storedAI(t, server, "openai")
	catalog, calls := modelCatalog(t, "llama3")

	rr := send(t, server, http.MethodPost, "/api/settings/ai/models", `{"provider":"local","baseUrl":"`+catalog.URL+`/v1"}`)
	if rr.Code != http.StatusOK {
		t.Fatalf("models = %d %s", rr.Code, rr.Body.String())
	}
	if c := calls.all(); len(c) != 1 || c[0].Authorization != "" {
		t.Errorf("the OpenAI key went to a different provider's endpoint: %+v", c)
	}
}

func TestAIModelsRefusesInternalAddressesForCloudProviders(t *testing.T) {
	t.Setenv("POD_NAMESPACE", operatorNamespace)
	server := buildMockServerWithK8s()
	catalog, calls := modelCatalog(t, "gpt-4o")

	for body, want := range map[string]string{
		`{"provider":"openai","apiKey":"k","baseUrl":"` + catalog.URL + `/v1"}`:  "may not use the internal address",
		`{"provider":"local","model":"m","baseUrl":"http://169.254.169.254/v1"}`: "link-local and cloud metadata addresses are not allowed",
		`{"provider":"mistral"}`: "unsupported AI provider",
	} {
		rr := send(t, server, http.MethodPost, "/api/settings/ai/models", body)
		if rr.Code != http.StatusBadRequest || !strings.Contains(errorMessage(t, rr), want) {
			t.Errorf("%s = %d %s, want 400 %q", body, rr.Code, rr.Body.String(), want)
		}
	}
	if c := calls.all(); len(c) != 0 {
		t.Errorf("refused settings still reached the endpoint: %+v", c)
	}
	if rr := send(t, server, http.MethodPost, "/api/settings/ai/models", `{"provider":`); rr.Code != http.StatusBadRequest {
		t.Errorf("malformed body = %d", rr.Code)
	}
}

func TestAIModelsReportsAProviderFailureAsBadGateway(t *testing.T) {
	t.Setenv("POD_NAMESPACE", operatorNamespace)
	server := buildMockServerWithK8s()
	srv, _ := recordingServer(t, func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, `{"error":{"message":"invalid key"}}`, http.StatusUnauthorized)
	})
	rr := send(t, server, http.MethodPost, "/api/settings/ai/models", `{"provider":"local","model":"m","baseUrl":"`+srv.URL+`/v1"}`)
	if rr.Code != http.StatusBadGateway || !strings.Contains(errorMessage(t, rr), "rejected the API key: invalid key") {
		t.Errorf("provider failure = %d %s, want 502 with the provider's reason", rr.Code, rr.Body.String())
	}
}

func TestAIModelsListsALocalServersModelsBeforeOneIsChosen(t *testing.T) {
	t.Setenv("POD_NAMESPACE", operatorNamespace)
	server := buildMockServerWithK8s()
	catalog, _ := modelCatalog(t, "llama3", "qwen")

	rr := send(t, server, http.MethodPost, "/api/settings/ai/models", `{"provider":"local","baseUrl":"`+catalog.URL+`/v1","skipSslVerify":false}`)
	if rr.Code != http.StatusOK || !strings.Contains(rr.Body.String(), `"llama3"`) {
		t.Errorf("the settings form asks for the models of a fresh local setup and gets %d %s", rr.Code, rr.Body.String())
	}
}

func TestAIProviderTestChecksTheChosenModel(t *testing.T) {
	t.Setenv("POD_NAMESPACE", operatorNamespace)
	server := buildMockServerWithK8s()
	catalog, _ := modelCatalog(t, "llama3", "qwen")
	empty, _ := modelCatalog(t)
	failing, _ := recordingServer(t, func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "upstream down", http.StatusServiceUnavailable)
	})
	type result struct {
		Connected      bool   `json:"connected"`
		Message        string `json:"message"`
		Error          string `json:"error"`
		ModelAvailable bool   `json:"modelAvailable"`
	}
	test := func(body string) result {
		rr := send(t, server, http.MethodPost, "/api/settings/providers/ai/test", body)
		if rr.Code != http.StatusOK {
			t.Fatalf("test %s = %d %s", body, rr.Code, rr.Body.String())
		}
		return decodeInto[result](t, rr)
	}

	if got := test(`{"provider":"local","model":"qwen","baseUrl":"` + catalog.URL + `/v1"}`); !got.Connected || !got.ModelAvailable || got.Message != "Connected - 2 models available" {
		t.Errorf("an offered model = %+v", got)
	}
	if got := test(`{"provider":"local","model":"mixtral","baseUrl":"` + catalog.URL + `/v1"}`); !got.Connected || got.ModelAvailable || !strings.Contains(got.Message, `"mixtral" is not among them`) {
		t.Errorf("a model the account lacks = %+v", got)
	}
	if got := test(`{"provider":"local","model":"mixtral","baseUrl":"` + empty.URL + `/v1"}`); !got.Connected || !got.ModelAvailable {
		t.Errorf("an endpoint that lists no models cannot rule the model out: %+v", got)
	}
	if got := test(`{"provider":"local","model":"qwen","baseUrl":"` + failing.URL + `/v1"}`); got.Connected || !strings.Contains(got.Error, "upstream down") {
		t.Errorf("an unreachable model server = %+v", got)
	}
	if got := test(`{"provider":"mistral"}`); got.Connected || !strings.Contains(got.Error, "unsupported AI provider") {
		t.Errorf("an unknown provider = %+v", got)
	}
	if got := test(`{"provider":"openai","apiKey":"k","baseUrl":"` + catalog.URL + `/v1"}`); got.Connected || !strings.Contains(got.Error, "internal address") {
		t.Errorf("a cloud provider at a loopback address = %+v", got)
	}
	if rr := send(t, server, http.MethodPost, "/api/settings/providers/ai/test", `{"model":`); rr.Code != http.StatusBadRequest {
		t.Errorf("malformed body = %d", rr.Code)
	}
}

func TestAIChatRejectsAnEmptyConversation(t *testing.T) {
	t.Setenv("POD_NAMESPACE", operatorNamespace)
	model, calls := fakeModel(t)
	server := assistantServer(t, 0, model.URL)
	for _, body := range []string{`{"messages":[]}`, `{"messages":`, ``} {
		rr := send(t, server, http.MethodPost, "/api/ai/chat", body)
		if rr.Code != http.StatusBadRequest || !strings.Contains(errorMessage(t, rr), `expected {"messages"`) {
			t.Errorf("chat %q = %d %s", body, rr.Code, rr.Body.String())
		}
	}
	if calls.Load() != 0 {
		t.Error("the model was asked without a question")
	}
}
