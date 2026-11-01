package api

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	corev1 "k8s.io/api/core/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/migalsp/costdeck-operator/internal/auth"
	"github.com/migalsp/costdeck-operator/internal/config"
)

func TestOIDCSettings(t *testing.T) {
	t.Setenv("POD_NAMESPACE", "costdeck")
	server := buildMockServerWithK8s()
	seedCalendars(t, server)
	ok := `{"auth":{"oidc":{"enabled":true,"displayName":"Keycloak","issuerUrl":"https://keycloak.example.com/realms/costdeck/","clientId":"costdeck",
		"clientSecret":"s3cr3t","scopes":["openid","profile","email","groups","groups"],"groupsClaim":"groups","rolesClaim":"realm_access.roles",
		"defaultRole":"viewer","groupRoleMapping":{"/costdeck-admins":"admin"},"roleMapping":{"platform-admins":"admin"}}}}`
	if rr := send(t, server, "PUT", "/api/settings", ok); rr.Code != http.StatusOK {
		t.Fatalf("valid settings: %d %s", rr.Code, rr.Body.String())
	}
	cfg, _ := config.Get(context.Background(), server.Client)
	o := cfg.Spec.Auth.OIDC
	if o == nil || o.IssuerURL != "https://keycloak.example.com/realms/costdeck" || strings.Join(o.Scopes, " ") != "profile email groups" ||
		o.ClientSecretRef != "costdeck-oidc-credentials" || o.GroupRoleMapping["/costdeck-admins"] != string(auth.RoleAdmin) ||
		o.RoleMapping["platform-admins"] != string(auth.RoleAdmin) {
		t.Fatalf("saved = %+v", o)
	}
	var secret corev1.Secret
	if err := server.Client.Get(context.Background(), client.ObjectKey{Namespace: "costdeck", Name: "costdeck-oidc-credentials"}, &secret); err != nil || string(secret.Data["CLIENT_SECRET"]) != "s3cr3t" {
		t.Errorf("client secret = %v (%v)", secret.Data, err)
	}
	rr := send(t, server, "GET", "/api/settings", "")
	if strings.Contains(rr.Body.String(), "s3cr3t") {
		t.Error("the client secret came back")
	}
	var got struct {
		Auth AuthSettingsResponse `json:"auth"`
	}
	_ = json.Unmarshal(rr.Body.Bytes(), &got)
	if got.Auth.OIDC == nil || !got.Auth.OIDC.HasClientSecret || got.Auth.OIDC.DisplayName != "Keycloak" || !got.Auth.OIDC.AutoProvision ||
		got.Auth.OIDC.RoleMapping["platform-admins"] != string(auth.RoleAdmin) {
		t.Errorf("settings = %+v", got.Auth.OIDC)
	}
	for name, body := range map[string]string{
		"an HTTP issuer":         `{"auth":{"oidc":{"issuerUrl":"http://keycloak.example.com/realms/x"}}}`,
		"a missing client":       `{"auth":{"oidc":{"enabled":true,"clientId":""}}}`,
		"an unknown role":        `{"auth":{"oidc":{"groupRoleMapping":{"g":"root"}}}}`,
		"an unknown mapped role": `{"auth":{"oidc":{"roleMapping":{"r":"root"}}}}`,
		"an unknown default":     `{"auth":{"oidc":{"defaultRole":"superuser"}}}`,
	} {
		if rr := send(t, server, "PUT", "/api/settings", body); rr.Code != http.StatusBadRequest {
			t.Errorf("%s: %d %s", name, rr.Code, rr.Body.String())
		}
	}
}
