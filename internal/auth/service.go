package auth

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"os"
	"strings"
	"sync"
	"time"

	"golang.org/x/crypto/bcrypt"
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
	Tokens   *Tokens
	Users    *Users
	SignIns  *SignIns

	localUser     string
	localPassword string
	// localHash is the built-in admin's password as a bcrypt hash, made on first use.
	localHash     []byte
	localHashOnce sync.Once
	// disabled is set by COSTDECK_AUTH_DISABLED=true, for local development only.
	disabled bool

	limitersMu sync.Mutex
	limiters   map[string]*rate.Limiter // client address -> password attempts
}

// Built-in admin account.
const (
	envAuthUser      = "COSTDECK_AUTH_USER"
	envAuthPassword  = "COSTDECK_AUTH_PASSWORD"
	envAuthDisabled  = "COSTDECK_AUTH_DISABLED"
	adminSecretName  = "costdeck-admin-credentials"
	defaultAdminUser = "costdeck"
)

// NewService loads (or creates) the session signing key and the built-in admin account:
// COSTDECK_AUTH_USER and COSTDECK_AUTH_PASSWORD when set (the Helm chart sets them),
// otherwise credentials generated once into the costdeck-admin-credentials Secret. An
// install that forgot to configure them is locked, never open.
func NewService(ctx context.Context, c client.Client) (*Service, error) {
	key, err := LoadOrCreateKey(ctx, c)
	if err != nil {
		return nil, err
	}
	sessions, err := NewSessions(key)
	if err != nil {
		return nil, err
	}
	svc := &Service{
		Client:        c,
		Sessions:      sessions,
		Entra:         &Entra{Client: c, Sessions: sessions},
		Tokens:        &Tokens{Client: c},
		localUser:     os.Getenv(envAuthUser),
		localPassword: os.Getenv(envAuthPassword),
		disabled:      strings.EqualFold(os.Getenv(envAuthDisabled), "true"),
	}
	if !svc.disabled && !svc.localConfigured() {
		data, err := loadOrCreateSecret(ctx, c, adminSecretName,
			func(d map[string][]byte) bool { return len(d["username"]) > 0 && len(d["password"]) >= 16 },
			func() (map[string][]byte, error) {
				return map[string][]byte{"username": []byte(defaultAdminUser), "password": []byte(rand.Text())}, nil
			})
		if err != nil {
			return nil, fmt.Errorf("prepare the built-in admin account: %w", err)
		}
		svc.localUser, svc.localPassword = string(data["username"]), string(data["password"])
		logf.FromContext(ctx).Info("Using generated admin credentials", "secret", adminSecretName,
			"namespace", config.OperatorNamespace(), "user", svc.localUser)
	}
	svc.Users = &Users{Client: c, Reserved: svc.localUser}
	svc.SignIns = &SignIns{Client: c}
	svc.Entra.OnSignIn = svc.recordSignIn
	return svc, nil
}

// BuiltinAdmin returns the name of the built-in admin account, empty when there is none.
func (s *Service) BuiltinAdmin() string {
	if !s.localConfigured() {
		return ""
	}
	return s.localUser
}

// recordSignIn notes a sign-in for the users page; a failure is only logged.
func (s *Service) recordSignIn(ctx context.Context, id Identity) {
	if s.SignIns == nil {
		return
	}
	if err := s.SignIns.Record(ctx, id); err != nil {
		logf.FromContext(ctx).Error(err, "Could not record a sign-in", "user", id.Name)
	}
}

// localConfigured reports whether the built-in admin account exists.
func (s *Service) localConfigured() bool { return s.localUser != "" && s.localPassword != "" }

// Disabled reports whether every request runs as an anonymous administrator. That needs
// COSTDECK_AUTH_DISABLED=true and no single sign-on: a development setting, never a
// fallback for missing configuration.
func (s *Service) Disabled(ctx context.Context) bool {
	return s.disabled && !s.Entra.Enabled(ctx)
}

// anonymousAdmin is the identity used while authentication is disabled.
var anonymousAdmin = &Identity{Subject: "anonymous", Name: "Anonymous", Role: RoleAdmin, Provider: "anonymous"}

// PublicPath reports whether a path never requires a session. The Webex webhook
// authenticates every request with its HMAC signature instead.
func PublicPath(path string) bool {
	switch path {
	case "/api/login", "/api/logout", "/api/auth/config", "/api/docs", "/api/openapi.yaml", "/api/openapi.json", "/api/webex/webhook":
		return true
	}
	return strings.HasPrefix(path, "/api/auth/entra/")
}

// protectedPath reports whether a path needs authentication: the REST API and the MCP
// endpoint. The dashboard's static files are public; the SPA asks /api/auth/me and shows
// the login page when that answers 401.
func protectedPath(path string) bool {
	return (strings.HasPrefix(path, "/api/") && !PublicPath(path)) || path == "/mcp" || strings.HasPrefix(path, "/mcp/")
}

// Middleware authenticates requests with a session cookie or an API token
// (Authorization: Bearer cdk_...).
func (s *Service) Middleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !protectedPath(r.URL.Path) {
			next.ServeHTTP(w, r)
			return
		}
		ctx := r.Context()
		if s.Disabled(ctx) {
			next.ServeHTTP(w, r.WithContext(WithIdentity(ctx, anonymousAdmin)))
			return
		}
		if bearer, ok := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer "); ok && s.Tokens != nil {
			if id, ok := s.Tokens.Authenticate(ctx, strings.TrimSpace(bearer)); ok {
				next.ServeHTTP(w, r.WithContext(WithIdentity(ctx, id)))
				return
			}
			w.Header().Set("WWW-Authenticate", `Bearer realm="costdeck"`)
			writeAuthJSON(w, http.StatusUnauthorized, map[string]string{"error": "Invalid or expired API token"})
			return
		}
		if crossSiteWrite(r) {
			writeAuthJSON(w, http.StatusForbidden, map[string]string{"error": "Cross-site request refused"})
			return
		}
		id, err := s.Sessions.Read(r)
		if err != nil {
			writeAuthJSON(w, http.StatusUnauthorized, map[string]string{"error": "Authentication required"})
			return
		}
		if _, managed := ManagedUser(id); managed && s.Users != nil {
			// A managed user's session follows the account: deleting or disabling it, or
			// resetting its password, ends the session, and role changes apply at once.
			user, err := s.Users.Session(ctx, id)
			if err != nil {
				s.Sessions.Clear(w, r)
				writeAuthJSON(w, http.StatusUnauthorized, map[string]string{"error": "Your session has ended: " + err.Error()})
				return
			}
			current := user.Identity()
			current.ExpiresAt, current.IssuedAt = id.ExpiresAt, id.IssuedAt
			id = &current
			if id.MustChangePassword && !passwordChangePath(r.URL.Path) {
				writeAuthJSON(w, http.StatusForbidden, map[string]string{
					"error": "Choose a new password before continuing.", "code": "password_change_required",
				})
				return
			}
		}
		next.ServeHTTP(w, r.WithContext(WithIdentity(ctx, id)))
	})
}

// crossSiteWrite reports whether a request that would change something with the session
// cookie comes from another site. SameSite=Lax keeps the cookie off cross-site requests but
// not off requests from a sibling subdomain, and the API accepts a JSON body whatever its
// Content-Type, so a page elsewhere on the same site could otherwise post a form to it.
// Browsers say where a request comes from in Sec-Fetch-Site, or at least in Origin; clients
// that send neither, such as curl, are not browsers and cannot be tricked into sending it.
func crossSiteWrite(r *http.Request) bool {
	switch r.Method {
	case http.MethodGet, http.MethodHead, http.MethodOptions:
		return false
	}
	if site := r.Header.Get("Sec-Fetch-Site"); site != "" {
		return site != "same-origin" && site != "none"
	}
	if origin := r.Header.Get("Origin"); origin != "" {
		u, err := url.Parse(origin)
		return err != nil || !strings.EqualFold(u.Host, r.Host)
	}
	return false
}

// passwordChangePath lists what a user who must choose a new password can still reach.
func passwordChangePath(path string) bool {
	return path == "/api/auth/me" || path == "/api/auth/password"
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

// HandleLogin signs in the built-in admin account or a local user: POST /api/login.
func (s *Service) HandleLogin(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	if s.Disabled(ctx) {
		writeAuthJSON(w, http.StatusOK, map[string]string{"status": "ok"})
		return
	}
	if !s.localConfigured() && (s.Users == nil || !s.Users.Any(ctx)) {
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

	var id Identity
	switch {
	case s.localConfigured() && constantTimeEqual(creds.Username, s.localUser):
		// A hidden password form (spec.auth.disableLocalLogin) does not disable the
		// built-in admin: it stays usable through the API as break-glass access.
		if !s.builtinPasswordOK(creds.Password) {
			s.rejectLogin(w, r, creds.Username, http.StatusUnauthorized, "Invalid credentials")
			return
		}
		id = Identity{Subject: "local:" + s.localUser, Name: s.localUser, Role: RoleAdmin, Provider: "local"}
	case s.Users != nil:
		if s.localLoginHidden(ctx) {
			s.rejectLogin(w, r, creds.Username, http.StatusForbidden, "Password sign-in is turned off; use single sign-on.")
			return
		}
		user, err := s.Users.Authenticate(ctx, creds.Username, creds.Password)
		switch {
		case errors.Is(err, ErrUserDisabled):
			s.rejectLogin(w, r, creds.Username, http.StatusForbidden, "This account is disabled. Ask an administrator.")
			return
		case errors.Is(err, ErrUserNotFound):
			s.rejectLogin(w, r, creds.Username, http.StatusUnauthorized, "Invalid credentials")
			return
		case err != nil:
			writeAuthJSON(w, http.StatusInternalServerError, map[string]string{"error": "Could not check the credentials"})
			return
		}
		id = user.Identity()
	default:
		s.rejectLogin(w, r, creds.Username, http.StatusUnauthorized, "Invalid credentials")
		return
	}

	if err := s.Sessions.Issue(w, r, id); err != nil {
		writeAuthJSON(w, http.StatusInternalServerError, map[string]string{"error": "Could not create session"})
		return
	}
	s.recordSignIn(ctx, id)
	writeAuthJSON(w, http.StatusOK, map[string]any{"status": "ok", "user": id})
}

func (s *Service) rejectLogin(w http.ResponseWriter, r *http.Request, user string, status int, msg string) {
	logf.FromContext(r.Context()).Info("Rejected a local sign-in", "user", user, "ip", clientIP(r), "reason", msg)
	writeAuthJSON(w, status, map[string]string{"error": msg})
}

// HandleChangePassword lets a local user replace their own password: POST
// /api/auth/password. Their other sessions end; this one is re-issued.
func (s *Service) HandleChangePassword(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	name, managed := ManagedUser(FromContext(ctx))
	if !managed || s.Users == nil {
		writeAuthJSON(w, http.StatusBadRequest, map[string]string{
			"error": "Only local users change their password here. The built-in admin's password is in the Helm release's admin-credentials Secret; single sign-on passwords are managed by your identity provider.",
		})
		return
	}
	if !s.limiter(clientIP(r)).Allow() {
		writeAuthJSON(w, http.StatusTooManyRequests, map[string]string{"error": "Too many attempts, wait a minute and try again."})
		return
	}
	var req struct {
		CurrentPassword string `json:"currentPassword"`
		NewPassword     string `json:"newPassword"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 16<<10)).Decode(&req); err != nil {
		writeAuthJSON(w, http.StatusBadRequest, map[string]string{"error": "Invalid request body"})
		return
	}
	if _, err := s.Users.Authenticate(ctx, name, req.CurrentPassword); err != nil {
		writeAuthJSON(w, http.StatusForbidden, map[string]string{"error": "The current password is wrong"})
		return
	}
	if req.NewPassword == req.CurrentPassword {
		writeAuthJSON(w, http.StatusBadRequest, map[string]string{"error": "Choose a password different from the current one"})
		return
	}
	if err := s.Users.SetPassword(ctx, name, req.NewPassword, false); err != nil {
		status := http.StatusInternalServerError
		if errors.Is(err, ErrInvalidUser) {
			status = http.StatusBadRequest
		}
		writeAuthJSON(w, status, map[string]string{"error": strings.TrimPrefix(err.Error(), ErrInvalidUser.Error()+": ")})
		return
	}
	user, err := s.Users.Authenticate(ctx, name, req.NewPassword)
	if err != nil {
		writeAuthJSON(w, http.StatusInternalServerError, map[string]string{"error": "Could not read the account back"})
		return
	}
	id := user.Identity()
	if err := s.Sessions.Issue(w, r, id); err != nil {
		writeAuthJSON(w, http.StatusInternalServerError, map[string]string{"error": "Could not create session"})
		return
	}
	logf.FromContext(ctx).Info("Changed a local user's password", "user", name)
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
		"localLogin": (s.localConfigured() || (s.Users != nil && s.Users.Any(ctx))) && !s.localLoginHidden(ctx),
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

const (
	loginBurst = 5
	// maxLimiters bounds the memory spent on tracking clients; idle entries are pruned
	// once it is reached.
	maxLimiters = 4096
)

// limiter allows five password attempts per minute per client address.
func (s *Service) limiter(ip string) *rate.Limiter {
	s.limitersMu.Lock()
	defer s.limitersMu.Unlock()
	if s.limiters == nil {
		s.limiters = map[string]*rate.Limiter{}
	}
	if l, ok := s.limiters[ip]; ok {
		return l
	}
	if len(s.limiters) >= maxLimiters {
		for k, l := range s.limiters {
			// A limiter back at full burst has seen no attempts for a minute.
			if l.Tokens() >= loginBurst {
				delete(s.limiters, k)
			}
		}
	}
	l := rate.NewLimiter(rate.Every(12*time.Second), loginBurst)
	s.limiters[ip] = l
	return l
}

// clientIP returns the address that rate limiting applies to. Behind an Ingress, that is
// the last X-Forwarded-For entry, appended by the proxy itself; earlier entries come
// from the client and would let it pick a fresh address for every attempt.
func clientIP(r *http.Request) string {
	if fwd := r.Header.Get("X-Forwarded-For"); fwd != "" {
		parts := strings.Split(fwd, ",")
		if last := strings.TrimSpace(parts[len(parts)-1]); last != "" {
			return last
		}
	}
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}

// builtinPasswordOK checks the built-in admin's password the way local users' passwords
// are checked, against a bcrypt hash, so a wrong password takes as long for both kinds of
// account. bcrypt reads at most 72 bytes; a longer password is compared directly.
func (s *Service) builtinPasswordOK(password string) bool {
	s.localHashOnce.Do(func() {
		s.localHash, _ = bcrypt.GenerateFromPassword([]byte(s.localPassword), bcryptCost)
	})
	if s.localHash == nil {
		return subtle.ConstantTimeCompare([]byte(password), []byte(s.localPassword)) == 1
	}
	return bcrypt.CompareHashAndPassword(s.localHash, []byte(password)) == nil
}

// constantTimeEqual compares user names without leaking their length or content through
// timing. Hashing first equalises the lengths.
func constantTimeEqual(a, b string) bool {
	ha, hb := sha256.Sum256([]byte(a)), sha256.Sum256([]byte(b))
	return subtle.ConstantTimeCompare(ha[:], hb[:]) == 1
}
