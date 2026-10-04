package auth

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/json"
	"net"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"

	"golang.org/x/time/rate"
	"sigs.k8s.io/controller-runtime/pkg/client"
	logf "sigs.k8s.io/controller-runtime/pkg/log"

	"github.com/migalsp/costdeck-operator/internal/config"
)

type contextKey struct{}

// FromContext returns the identity the middleware attached to the request.
func FromContext(ctx context.Context) *Identity {
	id, _ := ctx.Value(contextKey{}).(*Identity)
	return id
}

// WithIdentity attaches an identity to a context (used by the middleware and tests).
func WithIdentity(ctx context.Context, id *Identity) context.Context {
	return context.WithValue(ctx, contextKey{}, id)
}

// Service bundles session handling, local login and Entra SSO.
type Service struct {
	Client   client.Client
	Sessions *Sessions
	Entra    *Entra

	localUser     string
	localPassword string
	limiters      sync.Map // client IP -> *rate.Limiter
}

// NewService reads the built-in admin credentials from COSTDECK_AUTH_USER and
// COSTDECK_AUTH_PASSWORD and loads (or creates) the session signing key.
func NewService(ctx context.Context, c client.Client) (*Service, error) {
	key, err := LoadOrCreateKey(ctx, c)
	if err != nil {
		return nil, err
	}
	sessions, err := NewSessions(key)
	if err != nil {
		return nil, err
	}
	return &Service{
		Client:        c,
		Sessions:      sessions,
		Entra:         &Entra{Client: c, Sessions: sessions},
		localUser:     os.Getenv("COSTDECK_AUTH_USER"),
		localPassword: os.Getenv("COSTDECK_AUTH_PASSWORD"),
	}, nil
}

// localConfigured reports whether the built-in admin account exists.
func (s *Service) localConfigured() bool { return s.localUser != "" && s.localPassword != "" }

// Disabled reports whether no sign-in method is configured at all, in which case every
// request is treated as an anonymous administrator (development mode).
func (s *Service) Disabled(ctx context.Context) bool {
	return !s.localConfigured() && !s.Entra.Enabled(ctx)
}

// anonymousAdmin is the identity used while authentication is disabled.
var anonymousAdmin = &Identity{Subject: "anonymous", Name: "Anonymous", Role: RoleAdmin, Provider: "anonymous"}

// publicPaths never require a session. The Webex webhook authenticates every request with
// its HMAC signature instead.
func publicPath(path string) bool {
	switch path {
	case "/api/login", "/api/logout", "/api/auth/config", "/api/docs", "/api/openapi.yaml", "/api/webex/webhook":
		return true
	}
	return strings.HasPrefix(path, "/api/auth/entra/")
}

// Middleware authenticates /api/ requests. The dashboard's static files are public; the
// SPA asks /api/auth/me and shows the login page when that answers 401.
func (s *Service) Middleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasPrefix(r.URL.Path, "/api/") || publicPath(r.URL.Path) {
			next.ServeHTTP(w, r)
			return
		}
		if s.Disabled(r.Context()) {
			next.ServeHTTP(w, r.WithContext(WithIdentity(r.Context(), anonymousAdmin)))
			return
		}
		id, err := s.Sessions.Read(r)
		if err != nil {
			writeAuthJSON(w, http.StatusUnauthorized, map[string]string{"error": "Authentication required"})
			return
		}
		next.ServeHTTP(w, r.WithContext(WithIdentity(r.Context(), id)))
	})
}

// Require wraps a handler with a minimum role.
func Require(min Role, next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		id := FromContext(r.Context())
		if id == nil {
			// Routes reached without the middleware (tests, internal calls) are trusted.
			next(w, r)
			return
		}
		if !id.Role.Allows(min) {
			writeAuthJSON(w, http.StatusForbidden, map[string]string{
				"error": "This action needs the " + string(min) + " role; you are signed in as " + string(id.Role) + ".",
			})
			return
		}
		next(w, r)
	}
}

// HandleLogin authenticates the built-in admin account: POST /api/login.
func (s *Service) HandleLogin(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	if s.Disabled(ctx) {
		writeAuthJSON(w, http.StatusOK, map[string]string{"status": "ok"})
		return
	}
	// A hidden password form (spec.auth.disableLocalLogin) does not disable this endpoint:
	// the built-in admin stays usable through the API as break-glass access.
	if !s.localConfigured() {
		writeAuthJSON(w, http.StatusForbidden, map[string]string{"error": "Password sign-in is not configured; use single sign-on."})
		return
	}
	if !s.limiter(clientIP(r)).Allow() {
		writeAuthJSON(w, http.StatusTooManyRequests, map[string]string{"error": "Too many attempts, wait a minute and try again."})
		return
	}

	var creds struct {
		Username string `json:"username"`
		Password string `json:"password"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 16<<10)).Decode(&creds); err != nil {
		writeAuthJSON(w, http.StatusBadRequest, map[string]string{"error": "Invalid request body"})
		return
	}
	if !constantTimeEqual(creds.Username, s.localUser) || !constantTimeEqual(creds.Password, s.localPassword) {
		logf.FromContext(ctx).Info("Rejected a local sign-in", "user", creds.Username, "ip", clientIP(r))
		writeAuthJSON(w, http.StatusUnauthorized, map[string]string{"error": "Invalid credentials"})
		return
	}

	id := Identity{Subject: "local:" + s.localUser, Name: s.localUser, Role: RoleAdmin, Provider: "local"}
	if err := s.Sessions.Issue(w, r, id); err != nil {
		writeAuthJSON(w, http.StatusInternalServerError, map[string]string{"error": "Could not create session"})
		return
	}
	writeAuthJSON(w, http.StatusOK, map[string]any{"status": "ok", "user": id})
}

// HandleLogout clears the session: POST /api/logout.
func (s *Service) HandleLogout(w http.ResponseWriter, r *http.Request) {
	s.Sessions.Clear(w, r)
	writeAuthJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

// HandleMe returns the signed-in identity: GET /api/auth/me.
func (s *Service) HandleMe(w http.ResponseWriter, r *http.Request) {
	id := FromContext(r.Context())
	if id == nil {
		writeAuthJSON(w, http.StatusUnauthorized, map[string]string{"error": "Authentication required"})
		return
	}
	writeAuthJSON(w, http.StatusOK, id)
}

// HandleConfig tells the login page which sign-in methods to offer: GET /api/auth/config.
func (s *Service) HandleConfig(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	writeAuthJSON(w, http.StatusOK, map[string]any{
		"disabled":   s.Disabled(ctx),
		"localLogin": s.localConfigured() && !s.localLoginHidden(ctx),
		"entra":      map[string]bool{"enabled": s.Entra.Enabled(ctx)},
	})
}

// localLoginHidden reports whether the admin disabled the password form. It only takes
// effect while SSO is enabled, so nobody can lock themselves out.
func (s *Service) localLoginHidden(ctx context.Context) bool {
	cfg, err := config.Get(ctx, s.Client)
	if err != nil || !cfg.Spec.Auth.DisableLocalLogin {
		return false
	}
	return s.Entra.Enabled(ctx)
}

// limiter allows five password attempts per minute per client address.
func (s *Service) limiter(ip string) *rate.Limiter {
	l, _ := s.limiters.LoadOrStore(ip, rate.NewLimiter(rate.Every(12*time.Second), 5))
	return l.(*rate.Limiter)
}

func clientIP(r *http.Request) string {
	if fwd := r.Header.Get("X-Forwarded-For"); fwd != "" {
		return strings.TrimSpace(strings.Split(fwd, ",")[0])
	}
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}

// constantTimeEqual compares secrets without leaking their length or content through
// timing. Hashing first equalises the lengths.
func constantTimeEqual(a, b string) bool {
	ha, hb := sha256.Sum256([]byte(a)), sha256.Sum256([]byte(b))
	return subtle.ConstantTimeCompare(ha[:], hb[:]) == 1
}
