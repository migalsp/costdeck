package auth

import (
	"context"
	"fmt"
	"net/http/httptest"
	"testing"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/runtime"
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	finopsv1 "github.com/migalsp/costdeck-operator/api/v1"
)

func bootstrapClient(t *testing.T) client.Client {
	t.Helper()
	t.Setenv("POD_NAMESPACE", "costdeck")
	t.Setenv(envAuthUser, "")
	t.Setenv(envAuthPassword, "")
	t.Setenv(envAuthDisabled, "")
	scheme := runtime.NewScheme()
	utilruntime.Must(clientgoscheme.AddToScheme(scheme))
	utilruntime.Must(finopsv1.AddToScheme(scheme))
	return fake.NewClientBuilder().WithScheme(scheme).Build()
}

func TestMissingCredentialsAreGeneratedNotOpen(t *testing.T) {
	c := bootstrapClient(t)
	first, err := NewService(context.Background(), c)
	if err != nil {
		t.Fatal(err)
	}
	if first.Disabled(context.Background()) {
		t.Fatal("an install without credentials must not run as anonymous admin")
	}
	var secret corev1.Secret
	if err := c.Get(context.Background(), client.ObjectKey{Namespace: "costdeck", Name: adminSecretName}, &secret); err != nil {
		t.Fatalf("admin Secret not created: %v", err)
	}
	if string(secret.Data["username"]) != defaultAdminUser || len(secret.Data["password"]) < 16 {
		t.Errorf("generated credentials = %q / %d chars", secret.Data["username"], len(secret.Data["password"]))
	}

	// A restart (or a second replica) reuses the same password.
	second, err := NewService(context.Background(), c)
	if err != nil {
		t.Fatal(err)
	}
	if second.localPassword != first.localPassword || first.localPassword != string(secret.Data["password"]) {
		t.Error("the generated password must be stable across restarts and replicas")
	}
}

func TestAuthDisabledOnlyWhenAskedFor(t *testing.T) {
	c := bootstrapClient(t)
	t.Setenv(envAuthDisabled, "true")
	svc, err := NewService(context.Background(), c)
	if err != nil {
		t.Fatal(err)
	}
	if !svc.Disabled(context.Background()) {
		t.Error("COSTDECK_AUTH_DISABLED=true must disable authentication")
	}
	if err := c.Get(context.Background(), client.ObjectKey{Namespace: "costdeck", Name: adminSecretName}, &corev1.Secret{}); err == nil {
		t.Error("no admin Secret should be generated while authentication is disabled")
	}
}

func TestLoginLimiterIgnoresSpoofedForwardedFor(t *testing.T) {
	svc := newTestService(t)
	for i := range 10 {
		r := httptest.NewRequest("POST", "/api/login", nil)
		// The client invents a new first entry each time; the proxy appends the real one.
		r.Header.Set("X-Forwarded-For", fmt.Sprintf("198.51.100.%d, 203.0.113.7", i))
		allowed := svc.limiter(clientIP(r)).Allow()
		if i >= loginBurst && allowed {
			t.Fatalf("attempt %d allowed: rotating X-Forwarded-For must not reset the limit", i+1)
		}
	}
}
