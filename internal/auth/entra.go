package auth

import (
	"context"
	"crypto/rand"
	"crypto/tls"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/coreos/go-oidc/v3/oidc"
	"golang.org/x/oauth2"
	"golang.org/x/oauth2/clientcredentials"
	"sigs.k8s.io/controller-runtime/pkg/client"
	logf "sigs.k8s.io/controller-runtime/pkg/log"

	finopsv1 "github.com/migalsp/costdeck-operator/api/v1"
	"github.com/migalsp/costdeck-operator/internal/config"
)

// Entra endpoints and defaults.
const (
	DefaultAuthorityHost = "https://login.microsoftonline.com"
	// CallbackPath is the server-side redirect URI (mode A).
	CallbackPath = "/api/auth/entra/callback"
	// SPACallbackPath is the single-page-app redirect URI (mode B).
	SPACallbackPath = "/auth/callback"
	// SecretKeyClientSecret is the key of the client secret in ClientSecretRef.
	SecretKeyClientSecret = "CLIENT_SECRET"

	txnCookie = "costdeck-oidc"
	txnTTL    = 10 * time.Minute
)

// Entra signs users in with Microsoft Entra ID (OpenID Connect, authorization code flow
// with PKCE). The state, nonce and PKCE verifier travel in a short-lived signed cookie
// rather than in process memory, so any replica can complete a sign-in another started.
type Entra struct {
	Client   client.Reader
	Sessions *Sessions
	// OnSignIn is told about every completed sign-in; optional.
	OnSignIn func(context.Context, Identity)

	mu        sync.Mutex
	providers map[string]cachedOIDC
}

type cachedOIDC struct {
	provider *oidc.Provider
	at       time.Time
}

// entraSettings is the resolved, enabled configuration.
type entraSettings struct {
	cfg          finopsv1.EntraConfig
	clientSecret string
}

// oidcTxn is the in-flight sign-in, kept in the signed txnCookie.
type oidcTxn struct {
	State       string `json:"state"`
	Nonce       string `json:"nonce"`
	Verifier    string `json:"verifier"`
	RedirectURL string `json:"redirect"`
	ReturnTo    string `json:"return"`
	ExpiresAt   int64  `json:"exp"`
}

// entraClaims are the ID token claims CostDeck reads.
type entraClaims struct {
	Issuer            string            `json:"iss"`
	Subject           string            `json:"sub"`
	ObjectID          string            `json:"oid"`
	TenantID          string            `json:"tid"`
	Name              string            `json:"name"`
	PreferredUsername string            `json:"preferred_username"`
	Email             string            `json:"email"`
	Groups            []string          `json:"groups"`
	Roles             []string          `json:"roles"`
	ClaimNames        map[string]string `json:"_claim_names"`
}

// SignInError is a sign-in failure that can be shown to the user.
type SignInError struct {
	Code   string
	Detail string
}

func (e *SignInError) Error() string { return e.Code + ": " + e.Detail }

// Enabled reports whether Entra sign-in is configured and switched on. It only reads the
// cached CostDeckConfig, so it is cheap enough to call on every request.
func (e *Entra) Enabled(ctx context.Context) bool {
	return e.enabledConfig(ctx) != nil
}

func (e *Entra) enabledConfig(ctx context.Context) *finopsv1.EntraConfig {
	cfg, err := config.Get(ctx, e.Client)
	if err != nil {
		return nil
	}
	ec := cfg.Spec.Auth.Entra
	if ec == nil || !ec.Enabled || ec.TenantID == "" || ec.ClientID == "" {
		return nil
	}
	return ec
}

// settings returns the enabled configuration with its client secret, which lives in a
// Secret read straight from the API server, so it is only called during a sign-in.
func (e *Entra) settings(ctx context.Context) (*entraSettings, error) {
	ec := e.enabledConfig(ctx)
	if ec == nil {
		return nil, nil
	}
	s := &entraSettings{cfg: *ec}
	if ec.ClientSecretRef != "" {
		data, err := config.SecretData(ctx, e.Client, ec.ClientSecretRef)
		if err != nil {
			return nil, err
		}
		s.clientSecret = string(data[SecretKeyClientSecret])
	}
	return s, nil
}

func authority(cfg finopsv1.EntraConfig) string {
	host := strings.TrimRight(cfg.AuthorityHost, "/")
	if host == "" {
		host = DefaultAuthorityHost
	}
	return host
}

// multiTenant reports whether the tenant is one of the Entra multi-tenant aliases, whose
// discovery document carries a "{tenantid}" placeholder instead of a concrete issuer.
func multiTenant(tenant string) bool {
	switch strings.ToLower(tenant) {
	case "organizations", "common", "consumers":
		return true
	}
	return false
}

func httpClientFor(cfg finopsv1.EntraConfig) *http.Client {
	transport := http.DefaultTransport.(*http.Transport).Clone()
	if cfg.SkipSSLVerify {
		transport.TLSClientConfig = &tls.Config{InsecureSkipVerify: true} //nolint:gosec // Explicit opt-in for SSL-inspecting proxies.
	}
	return &http.Client{Timeout: 15 * time.Second, Transport: transport}
}

// provider returns the (cached) OIDC provider for the configured tenant.
func (e *Entra) provider(ctx context.Context, cfg finopsv1.EntraConfig) (*oidc.Provider, context.Context, error) {
	ctx = oidc.ClientContext(ctx, httpClientFor(cfg))
	issuer := authority(cfg) + "/" + cfg.TenantID + "/v2.0"
	key := fmt.Sprintf("%s|%t", issuer, cfg.SkipSSLVerify)

	e.mu.Lock()
	cached, ok := e.providers[key]
	e.mu.Unlock()
	if ok && time.Since(cached.at) < time.Hour {
		return cached.provider, ctx, nil
	}

	discoveryCtx := ctx
	if multiTenant(cfg.TenantID) {
		discoveryCtx = oidc.InsecureIssuerURLContext(ctx, authority(cfg)+"/{tenantid}/v2.0")
	}
	p, err := oidc.NewProvider(discoveryCtx, issuer)
	if err != nil {
		return nil, ctx, fmt.Errorf("discover Entra tenant %q: %w", cfg.TenantID, err)
	}
	e.mu.Lock()
	if e.providers == nil {
		e.providers = map[string]cachedOIDC{}
	}
	e.providers[key] = cachedOIDC{provider: p, at: time.Now()}
	e.mu.Unlock()
	return p, ctx, nil
}

func oauthConfig(p *oidc.Provider, s *entraSettings, redirectURL string) *oauth2.Config {
	return &oauth2.Config{
		ClientID:     s.cfg.ClientID,
		ClientSecret: s.clientSecret,
		Endpoint:     p.Endpoint(),
		RedirectURL:  redirectURL,
		Scopes:       []string{oidc.ScopeOpenID, "profile", "email"},
	}
}

// redirectURL is the configured redirect URI, or the server-side callback on the host the
// browser used. Entra rejects any URI not registered on the app, so a forged Host header
// cannot redirect a sign-in elsewhere.
func redirectURL(r *http.Request, cfg finopsv1.EntraConfig) string {
	if cfg.RedirectURL != "" {
		return cfg.RedirectURL
	}
	scheme := "http"
	if isHTTPS(r) {
		scheme = "https"
	}
	host := r.Host
	if fwd := r.Header.Get("X-Forwarded-Host"); fwd != "" {
		host = strings.TrimSpace(strings.Split(fwd, ",")[0])
	}
	return scheme + "://" + host + CallbackPath
}

// safeReturnPath keeps post-login redirects on this site.
func safeReturnPath(p string) string {
	if p == "" || !strings.HasPrefix(p, "/") || strings.HasPrefix(p, "//") || strings.Contains(p, `\`) ||
		strings.HasPrefix(p, CallbackPath) || strings.HasPrefix(p, SPACallbackPath) {
		return "/"
	}
	return p
}

func randomToken() string {
	b := make([]byte, 24)
	_, _ = rand.Read(b)
	return base64.RawURLEncoding.EncodeToString(b)
}

// HandleLogin starts a sign-in: GET /api/auth/entra/login?return=/path.
func (e *Entra) HandleLogin(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	s, err := e.settings(ctx)
	if err != nil || s == nil {
		redirectWithError(w, r, "/", &SignInError{Code: "sso_disabled", Detail: "Microsoft sign-in is not configured"})
		return
	}
	p, _, err := e.provider(ctx, s.cfg)
	if err != nil {
		logf.FromContext(ctx).Error(err, "Could not start Entra sign-in")
		redirectWithError(w, r, "/", &SignInError{Code: "sso_unavailable", Detail: "Microsoft Entra ID could not be reached"})
		return
	}

	txn := oidcTxn{
		State:       randomToken(),
		Nonce:       randomToken(),
		Verifier:    oauth2.GenerateVerifier(),
		RedirectURL: redirectURL(r, s.cfg),
		ReturnTo:    safeReturnPath(r.URL.Query().Get("return")),
		ExpiresAt:   time.Now().Add(txnTTL).Unix(),
	}
	token, err := e.Sessions.Sign(txn)
	if err != nil {
		http.Error(w, "could not start sign-in", http.StatusInternalServerError)
		return
	}
	http.SetCookie(w, &http.Cookie{
		Name: txnCookie, Value: token, Path: "/", HttpOnly: true, Secure: isHTTPS(r),
		// Lax, not Strict: the callback is a top-level navigation coming from
		// login.microsoftonline.com, on which Strict cookies are withheld.
		SameSite: http.SameSiteLaxMode, MaxAge: int(txnTTL.Seconds()),
	})

	authURL := oauthConfig(p, s, txn.RedirectURL).AuthCodeURL(txn.State,
		oidc.Nonce(txn.Nonce),
		oauth2.S256ChallengeOption(txn.Verifier),
		oauth2.SetAuthURLParam("response_mode", "query"),
	)
	http.Redirect(w, r, authURL, http.StatusFound)
}

// HandleCallback completes a server-side sign-in (mode A): Entra redirects the browser
// here, the session cookie is set and the browser continues to where it started.
func (e *Entra) HandleCallback(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	id, returnTo, err := e.complete(w, r, q.Get("code"), q.Get("state"), q.Get("error"), q.Get("error_description"))
	if err != nil {
		redirectWithError(w, r, returnTo, err)
		return
	}
	if err := e.Sessions.Issue(w, r, *id); err != nil {
		redirectWithError(w, r, returnTo, &SignInError{Code: "session_failed", Detail: err.Error()})
		return
	}
	e.signedIn(r.Context(), *id)
	http.Redirect(w, r, returnTo, http.StatusFound)
}

// HandleSPACallback completes a single-page-app sign-in (mode B): the dashboard route
// /auth/callback posts the code and state here.
func (e *Entra) HandleSPACallback(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Code             string `json:"code"`
		State            string `json:"state"`
		Error            string `json:"error"`
		ErrorDescription string `json:"error_description"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 64<<10)).Decode(&req); err != nil {
		writeAuthJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid request"})
		return
	}
	id, returnTo, err := e.complete(w, r, req.Code, req.State, req.Error, req.ErrorDescription)
	if err != nil {
		var sie *SignInError
		errors.As(err, &sie)
		writeAuthJSON(w, http.StatusUnauthorized, map[string]string{"error": sie.Code, "detail": sie.Detail})
		return
	}
	if err := e.Sessions.Issue(w, r, *id); err != nil {
		writeAuthJSON(w, http.StatusInternalServerError, map[string]string{"error": "session_failed"})
		return
	}
	e.signedIn(r.Context(), *id)
	writeAuthJSON(w, http.StatusOK, map[string]any{"user": id, "returnTo": returnTo})
}

func (e *Entra) signedIn(ctx context.Context, id Identity) {
	if e.OnSignIn != nil {
		e.OnSignIn(ctx, id)
	}
}

// complete validates the callback, exchanges the code and maps the user to a role.
func (e *Entra) complete(w http.ResponseWriter, r *http.Request, code, state, errCode, errDesc string) (*Identity, string, error) {
	ctx := r.Context()
	log := logf.FromContext(ctx).WithName("entra")

	var txn oidcTxn
	c, cookieErr := r.Cookie(txnCookie)
	http.SetCookie(w, &http.Cookie{Name: txnCookie, Value: "", Path: "/", MaxAge: -1, HttpOnly: true, Secure: isHTTPS(r), SameSite: http.SameSiteLaxMode})
	if cookieErr != nil || e.Sessions.Verify(c.Value, &txn) != nil || time.Now().Unix() > txn.ExpiresAt {
		return nil, "/", &SignInError{Code: "sign_in_expired", Detail: "The sign-in took too long or was started in another browser. Please try again."}
	}
	returnTo := safeReturnPath(txn.ReturnTo)
	if errCode != "" {
		return nil, returnTo, &SignInError{Code: errCode, Detail: errDesc}
	}
	if state == "" || state != txn.State {
		return nil, returnTo, &SignInError{Code: "state_mismatch", Detail: "The sign-in response does not belong to this browser session."}
	}

	s, err := e.settings(ctx)
	if err != nil || s == nil {
		return nil, returnTo, &SignInError{Code: "sso_disabled", Detail: "Microsoft sign-in is not configured"}
	}
	p, ctx, err := e.provider(ctx, s.cfg)
	if err != nil {
		log.Error(err, "Could not reach Entra ID")
		return nil, returnTo, &SignInError{Code: "sso_unavailable", Detail: "Microsoft Entra ID could not be reached"}
	}

	token, err := oauthConfig(p, s, txn.RedirectURL).Exchange(ctx, code, oauth2.VerifierOption(txn.Verifier))
	if err != nil {
		log.Error(err, "Could not exchange the Entra authorization code")
		return nil, returnTo, &SignInError{Code: "exchange_failed", Detail: "Entra ID rejected the sign-in (check the client secret and redirect URI)."}
	}
	rawID, _ := token.Extra("id_token").(string)
	claims, err := e.verify(ctx, p, s.cfg, rawID, txn.Nonce)
	if err != nil {
		log.Error(err, "Rejected an Entra ID token")
		return nil, returnTo, &SignInError{Code: "invalid_token", Detail: "The identity token could not be verified."}
	}

	if _, overage := claims.ClaimNames["groups"]; overage {
		log.Info("Entra ID omitted the groups claim (group overage); use app roles or assign groups to the application", "user", claims.PreferredUsername)
	}
	role, ok := ResolveRole(s.cfg, claims.Groups, claims.Roles)
	if !ok {
		log.Info("Entra user is not provisioned for CostDeck", "user", claims.PreferredUsername, "oid", claims.ObjectID)
		return nil, returnTo, &SignInError{Code: "user_not_provisioned", Detail: "Your account is not allowed to use CostDeck. Ask an administrator to map one of your groups to a role."}
	}

	id := &Identity{
		Subject:  "entra:" + firstNonEmpty(claims.ObjectID, claims.Subject),
		Name:     firstNonEmpty(claims.Name, claims.PreferredUsername, claims.Email),
		Email:    firstNonEmpty(claims.Email, claims.PreferredUsername),
		Role:     role,
		Provider: "entra",
	}
	log.Info("Signed in Entra user", "user", id.Email, "role", id.Role)
	return id, returnTo, nil
}

// verify checks the ID token signature against the tenant's JWKS, its audience, expiry,
// issuer and nonce.
func (e *Entra) verify(ctx context.Context, p *oidc.Provider, cfg finopsv1.EntraConfig, rawID, nonce string) (*entraClaims, error) {
	if rawID == "" {
		return nil, errors.New("token response carries no id_token")
	}
	mt := multiTenant(cfg.TenantID)
	tok, err := p.Verifier(&oidc.Config{ClientID: cfg.ClientID, SkipIssuerCheck: mt}).Verify(ctx, rawID)
	if err != nil {
		return nil, err
	}
	if tok.Nonce != nonce {
		return nil, errors.New("nonce mismatch")
	}
	var claims entraClaims
	if err := tok.Claims(&claims); err != nil {
		return nil, err
	}
	// Multi-tenant apps cannot pin one issuer; require the issuer of the user's own tenant.
	if mt && claims.Issuer != authority(cfg)+"/"+claims.TenantID+"/v2.0" {
		return nil, fmt.Errorf("unexpected issuer %q", claims.Issuer)
	}
	return &claims, nil
}

// ResolveRole maps group and app-role claims to a CostDeck role. The most privileged match
// wins; without a match, AutoProvision (default on) grants DefaultRole (default viewer).
//
// A multi-tenant registration only trusts group mappings. App-role assignments are made
// by the admins of each user's own tenant, so anyone who controls some tenant could grant
// themselves "admin", and auto-provisioning would admit every work account in the world.
// Group object IDs are unique across tenants, so a mapped group cannot be forged.
func ResolveRole(cfg finopsv1.EntraConfig, groups, appRoles []string) (Role, bool) {
	mt := multiTenant(cfg.TenantID)
	var role Role
	for _, g := range groups {
		if r, ok := ParseRole(cfg.GroupRoleMapping[g]); ok {
			role = Higher(role, r)
		}
	}
	if !mt {
		for _, ar := range appRoles {
			if r, ok := ParseRole(ar); ok {
				role = Higher(role, r)
			}
		}
	}
	if role != "" {
		return role, true
	}
	if mt || (cfg.AutoProvision != nil && !*cfg.AutoProvision) {
		return "", false
	}
	if r, ok := ParseRole(cfg.DefaultRole); ok {
		return r, true
	}
	return RoleViewer, true
}

// TestEntra checks a configuration without a user: the tenant must be discoverable and,
// for single-tenant apps, the client ID and secret must obtain a client-credentials token.
func (e *Entra) TestEntra(ctx context.Context, cfg finopsv1.EntraConfig, clientSecret string) (string, error) {
	if cfg.TenantID == "" || cfg.ClientID == "" {
		return "", errors.New("tenant ID and client ID are required")
	}
	p, ctx, err := e.provider(ctx, cfg)
	if err != nil {
		return "", err
	}
	if clientSecret == "" {
		return "", errors.New("no client secret configured")
	}
	if multiTenant(cfg.TenantID) {
		return "Tenant discovery succeeded. The client secret of a multi-tenant app is verified at the first sign-in.", nil
	}
	cc := clientcredentials.Config{
		ClientID: cfg.ClientID, ClientSecret: clientSecret, TokenURL: p.Endpoint().TokenURL,
		Scopes: []string{"https://graph.microsoft.com/.default"},
	}
	if _, err := cc.Token(ctx); err != nil {
		return "", fmt.Errorf("the client ID or secret was rejected: %w", err)
	}
	return "Tenant and client credentials are valid.", nil
}

func redirectWithError(w http.ResponseWriter, r *http.Request, returnTo string, err error) {
	code, detail := "sign_in_failed", err.Error()
	var sie *SignInError
	if errors.As(err, &sie) {
		code, detail = sie.Code, sie.Detail
	}
	q := url.Values{"sso_error": {code}, "sso_detail": {detail}}
	target := safeReturnPath(returnTo)
	sep := "?"
	if strings.Contains(target, "?") {
		sep = "&"
	}
	http.Redirect(w, r, target+sep+q.Encode(), http.StatusFound)
}

func writeAuthJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func firstNonEmpty(values ...string) string {
	for _, v := range values {
		if v != "" {
			return v
		}
	}
	return ""
}
