package auth

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/runtime"
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"
)

func readSessionCookie(s *Sessions, value string) (*Identity, error) {
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.AddCookie(&http.Cookie{Name: SessionCookie, Value: value})
	return s.Read(req)
}

func TestNewSessionsRefusesAShortKey(t *testing.T) {
	if _, err := NewSessions([]byte(strings.Repeat("k", 31))); err == nil {
		t.Error("a signing key under 32 bytes must be refused")
	}
}

func TestSessionsRejectForeignKeysUnknownRolesAndMalformedTokens(t *testing.T) {
	s := testSessions(t)
	other, err := NewSessions([]byte(strings.Repeat("o", 32)))
	if err != nil {
		t.Fatal(err)
	}
	future := time.Now().Add(time.Hour).Unix()

	signedElsewhere, _ := other.Sign(Identity{Subject: "local:admin", Role: RoleAdmin, ExpiresAt: future})
	unknownRole, _ := s.Sign(Identity{Subject: "u", Role: "root", ExpiresAt: future})
	noRole, _ := s.Sign(Identity{Subject: "u", ExpiresAt: future})
	signInTransaction, _ := s.Sign(oidcTxn{State: "st", Nonce: "n", ExpiresAt: future})
	valid, _ := s.Sign(Identity{Subject: "u", Role: RoleViewer, ExpiresAt: future})
	payload, signature, _ := strings.Cut(valid, ".")

	for name, value := range map[string]string{
		"signed with another installation's key": signedElsewhere,
		"unknown role":                           unknownRole,
		"no role":                                noRole,
		"a signed sign-in transaction":           signInTransaction,
		"no signature":                           payload,
		"signature only":                         "." + signature,
		"payload not base64":                     "%%%." + signature,
		"signature not base64":                   payload + ".%%%",
		"truncated signature":                    payload + "." + signature[:len(signature)-2],
		"empty":                                  "",
	} {
		if id, err := readSessionCookie(s, value); err == nil {
			t.Errorf("%s: accepted as %+v", name, id)
		}
	}
	if id, err := readSessionCookie(s, valid); err != nil || id.Subject != "u" {
		t.Fatalf("the untouched token must still be accepted: %+v, %v", id, err)
	}
}

func TestSessionsIssueStampsLifetimeAndCookieFlags(t *testing.T) {
	s := testSessions(t)
	rr := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/login", nil)
	req.Header.Set("X-Forwarded-Proto", "https")
	before := time.Now().Unix()
	if err := s.Issue(rr, req, Identity{Subject: "u", Role: RoleOperator, ExpiresAt: time.Now().Add(1000 * time.Hour).Unix()}); err != nil {
		t.Fatal(err)
	}
	cookies := rr.Result().Cookies()
	if len(cookies) != 1 {
		t.Fatalf("cookies = %+v", cookies)
	}
	c := cookies[0]
	if c.Name != SessionCookie || !c.HttpOnly || !c.Secure || c.SameSite != http.SameSiteLaxMode || c.Path != "/" || c.MaxAge != int(sessionTTL.Seconds()) {
		t.Errorf("cookie = %+v, want HttpOnly, Secure behind a TLS proxy, SameSite=Lax, Path=/, MaxAge 12h", c)
	}
	id, err := readSessionCookie(s, c.Value)
	if err != nil {
		t.Fatal(err)
	}
	if id.IssuedAt < before || id.IssuedAt > time.Now().Unix() {
		t.Errorf("IssuedAt = %d, want the time of issue", id.IssuedAt)
	}
	if id.ExpiresAt != id.IssuedAt+int64(sessionTTL.Seconds()) {
		t.Errorf("ExpiresAt = %d, want IssuedAt + 12h whatever the caller passed", id.ExpiresAt)
	}
}

func TestSessionsClearExpiresTheCookie(t *testing.T) {
	s := testSessions(t)
	rr := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "https://costdeck.example.com/api/logout", nil)
	s.Clear(rr, req)
	cookies := rr.Result().Cookies()
	if len(cookies) != 1 {
		t.Fatalf("cookies = %+v", cookies)
	}
	c := cookies[0]
	if c.Name != SessionCookie || c.Value != "" || c.MaxAge >= 0 || c.Path != "/" || !c.HttpOnly || !c.Secure {
		t.Errorf("clearing cookie = %+v, want an empty, expired, HttpOnly cookie on /", c)
	}
}

func TestLoadOrCreateKeyRepairsAnUnusableSecret(t *testing.T) {
	c := bootstrapClient(t)
	t.Setenv(keyEnv, "")
	ctx := context.Background()
	if err := c.Create(ctx, &corev1.Secret{Name: keySecretName, Namespace: "costdeck", Data: map[string][]byte{keySecretKey: []byte("too-short")}}); err != nil {
		t.Fatal(err)
	}

	key, err := LoadOrCreateKey(ctx, c)
	if err != nil {
		t.Fatal(err)
	}
	if len(key) < 32 || string(key) == "too-short" {
		t.Fatalf("key = %q, want a fresh key of at least 32 bytes", key)
	}
	var stored corev1.Secret
	if err := c.Get(ctx, client.ObjectKey{Namespace: "costdeck", Name: keySecretName}, &stored); err != nil {
		t.Fatal(err)
	}
	if string(stored.Data[keySecretKey]) != string(key) {
		t.Error("the repaired key was not written back, other replicas would sign with another key")
	}

	again, err := LoadOrCreateKey(ctx, c)
	if err != nil || string(again) != string(key) {
		t.Errorf("a restart must reuse the stored key: %v", err)
	}

	t.Setenv(keyEnv, strings.Repeat("e", 40))
	fromEnv, err := LoadOrCreateKey(ctx, c)
	if err != nil || string(fromEnv) != strings.Repeat("e", 40) {
		t.Errorf("COSTDECK_SESSION_KEY must win over the Secret: %q, %v", fromEnv, err)
	}
}

func TestLoadOrCreateKeyAdoptsTheKeyOfAReplicaThatWonTheRace(t *testing.T) {
	t.Setenv("POD_NAMESPACE", "costdeck")
	t.Setenv(keyEnv, "")
	winner := strings.Repeat("w", 48)
	scheme := runtime.NewScheme()
	utilruntime.Must(clientgoscheme.AddToScheme(scheme))
	var gets atomic.Int32
	c := fake.NewClientBuilder().WithScheme(scheme).
		WithObjects(&corev1.Secret{Name: keySecretName, Namespace: "costdeck", Data: map[string][]byte{keySecretKey: []byte(winner)}}).
		WithInterceptorFuncs(interceptor.Funcs{
			Get: func(ctx context.Context, c client.WithWatch, key client.ObjectKey, obj client.Object, opts ...client.GetOption) error {
				if gets.Add(1) == 1 {
					return apierrors.NewNotFound(corev1.Resource("secrets"), key.Name)
				}
				return c.Get(ctx, key, obj, opts...)
			},
		}).Build()

	key, err := LoadOrCreateKey(context.Background(), c)
	if err != nil {
		t.Fatal(err)
	}
	if string(key) != winner {
		t.Errorf("key = %q, want the key the other replica created", key)
	}
}
