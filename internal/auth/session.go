// Package auth authenticates dashboard and API users (built-in admin account and
// Microsoft Entra ID single sign-on) and authorizes them by role.
package auth

import (
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"strings"
	"time"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/migalsp/costdeck-operator/internal/config"
)

// Role is a coarse authorization level. Each role includes the ones below it.
type Role string

const (
	// RoleViewer can see everything except secrets.
	RoleViewer Role = "viewer"
	// RoleOperator can additionally scale, override schedules and apply right-sizing.
	RoleOperator Role = "operator"
	// RoleAdmin can additionally change settings and create or delete scaling objects.
	RoleAdmin Role = "admin"
)

var roleRank = map[Role]int{RoleViewer: 1, RoleOperator: 2, RoleAdmin: 3}

// ParseRole accepts the role names and the aliases used by other FinOps tools
// (owner = operator, reader = viewer). App role values such as "CostDeck.Admin" work too.
func ParseRole(s string) (Role, bool) {
	s = strings.ToLower(strings.TrimSpace(s))
	s = strings.TrimPrefix(s, "costdeck.")
	switch s {
	case "admin", "administrator":
		return RoleAdmin, true
	case "operator", "owner", "editor":
		return RoleOperator, true
	case "viewer", "reader", "readonly", "read-only":
		return RoleViewer, true
	}
	return "", false
}

// Allows reports whether r grants at least the permissions of min.
func (r Role) Allows(min Role) bool { return roleRank[r] >= roleRank[min] }

// Higher returns the more privileged of two roles.
func Higher(a, b Role) Role {
	if roleRank[b] > roleRank[a] {
		return b
	}
	return a
}

// Identity is the signed-in user carried in the session cookie.
type Identity struct {
	Subject   string `json:"sub"`
	Name      string `json:"name"`
	Email     string `json:"email,omitempty"`
	Role      Role   `json:"role"`
	Provider  string `json:"provider"` // local, entra or anonymous
	ExpiresAt int64  `json:"exp"`
}

// Session cookie settings.
const (
	SessionCookie = "costdeck-session"
	sessionTTL    = 12 * time.Hour
	keySecretName = "costdeck-session-key"
	keySecretKey  = "SESSION_KEY"
	keyEnv        = "COSTDECK_SESSION_KEY"
)

// Sessions signs and verifies session cookies with HMAC-SHA256.
type Sessions struct {
	key []byte
	ttl time.Duration
}

// NewSessions builds a session manager from a signing key of at least 32 bytes.
func NewSessions(key []byte) (*Sessions, error) {
	if len(key) < 32 {
		return nil, errors.New("session key must be at least 32 bytes")
	}
	return &Sessions{key: key, ttl: sessionTTL}, nil
}

// LoadOrCreateKey returns the session signing key: COSTDECK_SESSION_KEY when set,
// otherwise a random key kept in the costdeck-session-key Secret so that every replica
// (and every restart) accepts the same cookies.
func LoadOrCreateKey(ctx context.Context, c client.Client) ([]byte, error) {
	if k := os.Getenv(keyEnv); k != "" {
		return []byte(k), nil
	}
	key := client.ObjectKey{Name: keySecretName, Namespace: config.OperatorNamespace()}
	secret := &corev1.Secret{}
	err := c.Get(ctx, key, secret)
	if err == nil && len(secret.Data[keySecretKey]) >= 32 {
		return secret.Data[keySecretKey], nil
	}
	if err != nil && !apierrors.IsNotFound(err) {
		return nil, fmt.Errorf("read session key secret: %w", err)
	}

	fresh := make([]byte, 48)
	if _, err := rand.Read(fresh); err != nil {
		return nil, err
	}
	secret = &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name: key.Name, Namespace: key.Namespace,
			Labels: map[string]string{"app.kubernetes.io/managed-by": "costdeck-operator"},
		},
		Type: corev1.SecretTypeOpaque,
		Data: map[string][]byte{keySecretKey: fresh},
	}
	if err := c.Create(ctx, secret); err != nil {
		if apierrors.IsAlreadyExists(err) {
			// Another replica won the race; use its key.
			if err := c.Get(ctx, key, secret); err != nil {
				return nil, err
			}
			return secret.Data[keySecretKey], nil
		}
		return nil, fmt.Errorf("create session key secret: %w", err)
	}
	return fresh, nil
}

// Sign returns the signed, base64url-encoded form of v.
func (s *Sessions) Sign(v any) (string, error) {
	payload, err := json.Marshal(v)
	if err != nil {
		return "", err
	}
	mac := hmac.New(sha256.New, s.key)
	mac.Write(payload)
	enc := base64.RawURLEncoding
	return enc.EncodeToString(payload) + "." + enc.EncodeToString(mac.Sum(nil)), nil
}

// Verify checks a value produced by Sign and decodes it into v.
func (s *Sessions) Verify(token string, v any) error {
	payloadPart, sigPart, ok := strings.Cut(token, ".")
	if !ok {
		return errors.New("malformed token")
	}
	enc := base64.RawURLEncoding
	payload, err := enc.DecodeString(payloadPart)
	if err != nil {
		return errors.New("malformed token")
	}
	sig, err := enc.DecodeString(sigPart)
	if err != nil {
		return errors.New("malformed token")
	}
	mac := hmac.New(sha256.New, s.key)
	mac.Write(payload)
	if !hmac.Equal(sig, mac.Sum(nil)) {
		return errors.New("invalid signature")
	}
	return json.Unmarshal(payload, v)
}

// Issue sets the session cookie for id.
func (s *Sessions) Issue(w http.ResponseWriter, r *http.Request, id Identity) error {
	id.ExpiresAt = time.Now().Add(s.ttl).Unix()
	token, err := s.Sign(id)
	if err != nil {
		return err
	}
	http.SetCookie(w, &http.Cookie{
		Name:     SessionCookie,
		Value:    token,
		Path:     "/",
		HttpOnly: true,
		Secure:   isHTTPS(r),
		SameSite: http.SameSiteLaxMode,
		MaxAge:   int(s.ttl.Seconds()),
	})
	return nil
}

// Read returns the identity in the request's session cookie.
func (s *Sessions) Read(r *http.Request) (*Identity, error) {
	c, err := r.Cookie(SessionCookie)
	if err != nil {
		return nil, err
	}
	var id Identity
	if err := s.Verify(c.Value, &id); err != nil {
		return nil, err
	}
	if time.Now().Unix() > id.ExpiresAt {
		return nil, errors.New("session expired")
	}
	if _, ok := roleRank[id.Role]; !ok {
		return nil, errors.New("session carries an unknown role")
	}
	return &id, nil
}

// Clear removes the session cookie.
func (s *Sessions) Clear(w http.ResponseWriter, r *http.Request) {
	http.SetCookie(w, &http.Cookie{
		Name: SessionCookie, Value: "", Path: "/", HttpOnly: true, Secure: isHTTPS(r),
		SameSite: http.SameSiteLaxMode, MaxAge: -1,
	})
}

// isHTTPS tells whether the browser reached us over TLS, directly or through a proxy.
// Secure cookies are only set then; an unconditional Secure flag silently broke logins
// served over plain HTTP.
func isHTTPS(r *http.Request) bool {
	return r.TLS != nil || strings.EqualFold(r.Header.Get("X-Forwarded-Proto"), "https")
}
