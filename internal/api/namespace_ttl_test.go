package api

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/migalsp/costdeck-operator/internal/cleanup"
	"github.com/migalsp/costdeck-operator/internal/config"
)

func TestNamespaceTTLSettingsAndPlan(t *testing.T) {
	t.Setenv("POD_NAMESPACE", "costdeck")
	server := buildMockServerWithK8s()
	ctx := context.Background()

	for body, why := range map[string]string{
		`{"namespaceTTL":{"enabled":true,"ttl":"7d"}}`:                              "no selector",
		`{"namespaceTTL":{"enabled":true,"selector":" ","ttl":"7d"}}`:               "an empty selector",
		`{"namespaceTTL":{"enabled":true,"selector":"env in (preview","ttl":"7d"}}`: "a broken selector",
		`{"namespaceTTL":{"enabled":true,"selector":"env=preview","ttl":"30m"}}`:    "under an hour",
		`{"namespaceTTL":{"enabled":true,"selector":"env=preview"}}`:                "no time to live",
		`{"namespaceTTL":{"selector":"env=preview","ttl":"1d","warnBefore":"2d"}}`:  "a warning after the end",
		`{"namespaceTTL":{"enabled":true,"selector":"env=preview","ttl":"a week"}}`: "not a duration",
	} {
		if rr := send(t, server, "PUT", "/api/settings", body); rr.Code != http.StatusBadRequest {
			t.Errorf("%s: %d %s", why, rr.Code, rr.Body.String())
		}
	}
	if rr := send(t, server, "PUT", "/api/settings", `{"namespaceTTL":{"enabled":true,"dryRun":true,"selector":"env=preview","ttl":"7d","warnBefore":"24h"}}`); rr.Code != http.StatusOK {
		t.Fatalf("valid: %d %s", rr.Code, rr.Body.String())
	}
	cfg, _ := config.Get(ctx, server.Client)
	if tt := cfg.Spec.NamespaceTTL; !tt.Enabled || !tt.DryRun || tt.TTL.Duration != 7*24*time.Hour || tt.WarnBefore.Duration != 24*time.Hour {
		t.Fatalf("saved = %+v", tt)
	}
	var settings struct {
		NamespaceTTL NamespaceTTLSettings `json:"namespaceTTL"`
	}
	_ = json.Unmarshal(send(t, server, "GET", "/api/settings", "").Body.Bytes(), &settings)
	if settings.NamespaceTTL.TTL != "168h0m0s" || settings.NamespaceTTL.Selector != "env=preview" {
		t.Errorf("settings = %+v", settings.NamespaceTTL)
	}

	ns := func(name string, age time.Duration, labels map[string]string) {
		t.Helper()
		o := &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: name, Labels: labels, CreationTimestamp: metav1.NewTime(time.Now().Add(-age))}}
		if err := server.Client.Create(ctx, o); err != nil {
			t.Fatal(err)
		}
	}
	ns("pr-1", 2*24*time.Hour, map[string]string{"env": "preview"})
	ns("pr-kept", 9*24*time.Hour, map[string]string{"env": "preview", cleanup.TTLLabel: "off"})
	ns("other", 30*24*time.Hour, nil)

	var plan namespaceTTLResponse
	_ = json.Unmarshal(send(t, server, "GET", "/api/namespace-ttl", "").Body.Bytes(), &plan)
	if len(plan.Namespaces) != 2 || plan.Namespaces[0].Namespace != "pr-1" || plan.Namespaces[1].Protected == "" || plan.Permitted || !plan.DryRun {
		t.Fatalf("plan = %+v", plan)
	}

	// Unsaved values from the form: a shorter time and another selector.
	var draft namespaceTTLResponse
	_ = json.Unmarshal(send(t, server, "GET", "/api/namespace-ttl?selector=env%3Dpreview&ttl=1d", "").Body.Bytes(), &draft)
	if len(draft.Namespaces) != 2 || draft.Namespaces[0].ExpiresAt.Sub(draft.Namespaces[0].CreatedAt) != 24*time.Hour || draft.TTL != "168h0m0s" {
		t.Errorf("draft plan = %+v", draft)
	}

	rr := send(t, server, "POST", "/api/namespace-ttl/pr-1/extend", `{"by":"3d"}`)
	var e cleanup.Entry
	_ = json.Unmarshal(rr.Body.Bytes(), &e)
	if rr.Code != http.StatusOK || e.Source != cleanup.SourceExtended || e.ExpiresAt.Sub(plan.Namespaces[0].ExpiresAt) != 3*24*time.Hour {
		t.Errorf("extend: %d %s", rr.Code, rr.Body.String())
	}
	var got corev1.Namespace
	_ = server.Client.Get(ctx, client.ObjectKey{Name: "pr-1"}, &got)
	if got.Annotations[cleanup.ExpiresAnnotation] == "" {
		t.Error("the new time is not stored on the namespace")
	}
	for path, want := range map[string]int{
		"/api/namespace-ttl/pr-kept/extend": http.StatusBadRequest,
		"/api/namespace-ttl/other/extend":   http.StatusNotFound,
	} {
		if rr := send(t, server, "POST", path, `{"by":"1d"}`); rr.Code != want {
			t.Errorf("%s: %d %s", path, rr.Code, rr.Body.String())
		}
	}
	if rr := send(t, server, "POST", "/api/namespace-ttl/pr-1/extend", `{"by":"forever"}`); rr.Code != http.StatusBadRequest {
		t.Errorf("a bad duration: %d", rr.Code)
	}
}
