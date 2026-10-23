package auth

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/coreos/go-oidc/v3/oidc"
	"golang.org/x/oauth2"
	"sigs.k8s.io/controller-runtime/pkg/client"
	logf "sigs.k8s.io/controller-runtime/pkg/log"

	finopsv1 "github.com/migalsp/costdeck-operator/api/v1"
	"github.com/migalsp/costdeck-operator/internal/config"
)

// OIDC signs users in with any OpenID Connect provider (Keycloak, Okta, Auth0, Dex…), with
// the same flow as Entra: authorization code with PKCE, a nonce, and an ID token checked
// against the issuer's keys, audience and expiry. Groups and roles come from configurable,
// possibly nested claims.

const (
	// OIDCCallbackPath is the redirect URI to register with the provider.
	OIDCCallbackPath = "/api/auth/oidc/callback"
	oidcTxnCookie    = "costdeck-oidc-sso"
	defaultGroups    = "groups"
	providerOIDC     = "oidc"
)

// OIDC is the generic OpenID Connect sign-in.
type OIDC struct {
	Client   client.Reader
	Sessions *Sessions
	// OnSignIn is told about every completed sign-in; optional.
	OnSignIn func(context.Context, Identity)

	mu        sync.Mutex
	providers map[string]cachedOIDC
}

type oidcSettings struct {
	cfg          finopsv1.OIDCConfig
	clientSecret string
}

// Enabled reports whether OIDC sign-in is configured and switched on.
func (o *OIDC) Enabled(ctx context.Context) bool { return o.enabledConfig(ctx) != nil }

// DisplayName labels the sign-in button.
func (o *OIDC) DisplayName(ctx context.Context) string {
	if c := o.enabledConfig(ctx); c != nil && c.DisplayName != "" {
		return c.DisplayName
	}
	return "single sign-on"
}

func (o *OIDC) enabledConfig(ctx context.Context) *finopsv1.OIDCConfig {
	if o == nil {
		return nil
	}
	cfg, err := config.Get(ctx, o.Client)
	if err != nil {
		return nil
	}
	c := cfg.Spec.Auth.OIDC
	if c == nil || !c.Enabled || c.IssuerURL == "" || c.ClientID == "" {
		return nil
	}
	return c
}

func (o *OIDC) settings(ctx context.Context) (*oidcSettings, error) {
	c := o.enabledConfig(ctx)
	if c == nil {
		return nil, nil
	}
	s := &oidcSettings{cfg: *c}
	if c.ClientSecretRef != "" {
		data, err := config.SecretData(ctx, o.Client, c.ClientSecretRef)
		if err != nil {
			return nil, err
		}
		s.clientSecret = string(data[SecretKeyClientSecret])
	}
	return s, nil
}

// provider returns the (cached) provider of the issuer.
func (o *OIDC) provider(ctx context.Context, cfg finopsv1.OIDCConfig) (*oidc.Provider, context.Context, error) {
	ctx = oidc.ClientContext(ctx, httpClientFor(finopsv1.EntraConfig{SkipSSLVerify: cfg.SkipSSLVerify}))
	issuer := strings.TrimRight(cfg.IssuerURL, "/")
	key := fmt.Sprintf("%s|%t", issuer, cfg.SkipSSLVerify)
	o.mu.Lock()
	cached, ok := o.providers[key]
	o.mu.Unlock()
	if ok && time.Since(cached.at) < time.Hour {
		return cached.provider, ctx, nil
	}
	p, err := oidc.NewProvider(ctx, issuer)
	if err != nil {
		return nil, ctx, fmt.Errorf("discover the OpenID Connect issuer %s: %w", issuer, err)
	}
	o.mu.Lock()
	if o.providers == nil {
		o.providers = map[string]cachedOIDC{}
	}
	o.providers[key] = cachedOIDC{provider: p, at: time.Now()}
	o.mu.Unlock()
	return p, ctx, nil
}

func (s *oidcSettings) oauth(p *oidc.Provider, redirectURL string) *oauth2.Config {
	scopes := []string{oidc.ScopeOpenID}
	extra := s.cfg.Scopes
	if len(extra) == 0 {
		extra = []string{"profile", "email"}
	}
	for _, sc := range extra {
		if sc = strings.TrimSpace(sc); sc != "" && !slices.Contains(scopes, sc) {
			scopes = append(scopes, sc)
		}
	}
	return &oauth2.Config{ClientID: s.cfg.ClientID, ClientSecret: s.clientSecret, Endpoint: p.Endpoint(), RedirectURL: redirectURL, Scopes: scopes}
}

func oidcRedirectURL(r *http.Request, cfg finopsv1.OIDCConfig) string {
	if cfg.RedirectURL != "" {
		return cfg.RedirectURL
	}
	return strings.TrimSuffix(redirectURL(r, finopsv1.EntraConfig{}), CallbackPath) + OIDCCallbackPath
}

// HandleLogin starts a sign-in: GET /api/auth/oidc/login?return=/path.
func (o *OIDC) HandleLogin(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	s, err := o.settings(ctx)
	if err != nil || s == nil {
		redirectWithError(w, r, "/", &SignInError{Code: "sso_disabled", Detail: "Single sign-on is not configured"})
		return
	}
	p, _, err := o.provider(ctx, s.cfg)
	if err != nil {
		logf.FromContext(ctx).Error(err, "Could not start OIDC sign-in")
		redirectWithError(w, r, "/", &SignInError{Code: "sso_unavailable", Detail: "The sign-in provider could not be reached"})
		return
	}
	txn := oidcTxn{
		State: randomToken(), Nonce: randomToken(), Verifier: oauth2.GenerateVerifier(),
		RedirectURL: oidcRedirectURL(r, s.cfg), ReturnTo: safeReturnPath(r.URL.Query().Get("return")),
		ExpiresAt: time.Now().Add(txnTTL).Unix(),
	}
	token, err := o.Sessions.Sign(txn)
	if err != nil {
		http.Error(w, "could not start sign-in", http.StatusInternalServerError)
		return
	}
	// Lax: the callback is a top-level navigation back from the provider.
	http.SetCookie(w, &http.Cookie{Name: oidcTxnCookie, Value: token, Path: "/", HttpOnly: true, Secure: isHTTPS(r),
		SameSite: http.SameSiteLaxMode, MaxAge: int(txnTTL.Seconds())})
	http.Redirect(w, r, s.oauth(p, txn.RedirectURL).AuthCodeURL(txn.State, oidc.Nonce(txn.Nonce), oauth2.S256ChallengeOption(txn.Verifier)), http.StatusFound)
}

// HandleCallback completes a sign-in: the provider redirects the browser here.
func (o *OIDC) HandleCallback(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	id, returnTo, err := o.complete(w, r, q.Get("code"), q.Get("state"), q.Get("error"), q.Get("error_description"))
	if err != nil {
		redirectWithError(w, r, returnTo, err)
		return
	}
	if err := o.Sessions.Issue(w, r, *id); err != nil {
		redirectWithError(w, r, returnTo, &SignInError{Code: "session_failed", Detail: err.Error()})
		return
	}
	if o.OnSignIn != nil {
		o.OnSignIn(r.Context(), *id)
	}
	http.Redirect(w, r, returnTo, http.StatusFound)
}

func (o *OIDC) complete(w http.ResponseWriter, r *http.Request, code, state, errCode, errDesc string) (*Identity, string, error) {
	ctx := r.Context()
	log := logf.FromContext(ctx).WithName("oidc")
	var txn oidcTxn
	c, cookieErr := r.Cookie(oidcTxnCookie)
	http.SetCookie(w, &http.Cookie{Name: oidcTxnCookie, Value: "", Path: "/", MaxAge: -1, HttpOnly: true, Secure: isHTTPS(r), SameSite: http.SameSiteLaxMode})
	if cookieErr != nil || o.Sessions.Verify(c.Value, &txn) != nil || time.Now().Unix() > txn.ExpiresAt {
		return nil, "/", &SignInError{Code: "sign_in_expired", Detail: "The sign-in took too long or was started in another browser. Please try again."}
	}
	returnTo := safeReturnPath(txn.ReturnTo)
	if errCode != "" {
		return nil, returnTo, &SignInError{Code: errCode, Detail: errDesc}
	}
	if state == "" || state != txn.State {
		return nil, returnTo, &SignInError{Code: "state_mismatch", Detail: "The sign-in response does not belong to this browser session."}
	}
	s, err := o.settings(ctx)
	if err != nil || s == nil {
		return nil, returnTo, &SignInError{Code: "sso_disabled", Detail: "Single sign-on is not configured"}
	}
	p, ctx, err := o.provider(ctx, s.cfg)
	if err != nil {
		log.Error(err, "Could not reach the OIDC provider")
		return nil, returnTo, &SignInError{Code: "sso_unavailable", Detail: "The sign-in provider could not be reached"}
	}
	token, err := s.oauth(p, txn.RedirectURL).Exchange(ctx, code, oauth2.VerifierOption(txn.Verifier))
	if err != nil {
		log.Error(err, "Could not exchange the OIDC authorization code")
		return nil, returnTo, &SignInError{Code: "exchange_failed", Detail: "The provider rejected the sign-in (check the client secret and redirect URI)."}
	}
	rawID, _ := token.Extra("id_token").(string)
	if rawID == "" {
		return nil, returnTo, &SignInError{Code: "invalid_token", Detail: "The provider sent no identity token."}
	}
	idToken, err := p.Verifier(&oidc.Config{ClientID: s.cfg.ClientID}).Verify(ctx, rawID)
	if err != nil || idToken.Nonce != txn.Nonce {
		log.Error(errors.Join(err, errors.New("or nonce mismatch")), "Rejected an OIDC identity token")
		return nil, returnTo, &SignInError{Code: "invalid_token", Detail: "The identity token could not be verified."}
	}
	claims := map[string]any{}
	if err := idToken.Claims(&claims); err != nil {
		return nil, returnTo, &SignInError{Code: "invalid_token", Detail: "The identity token could not be read."}
	}
	groupsClaim := s.cfg.GroupsClaim
	if groupsClaim == "" {
		groupsClaim = defaultGroups
	}
	groups := ClaimStrings(claims, groupsClaim)
	var roles []string
	if s.cfg.RolesClaim != "" {
		roles = ClaimStrings(claims, s.cfg.RolesClaim)
	}
	str := func(k string) string { v, _ := claims[k].(string); return v }
	role, ok := ResolveOIDCRole(s.cfg, groups, roles)
	if !ok {
		log.Info("OIDC user is not provisioned for CostDeck", "user", firstNonEmpty(str("preferred_username"), str("email"), str("sub")))
		return nil, returnTo, &SignInError{Code: "user_not_provisioned", Detail: "Your account is not allowed to use CostDeck. Ask an administrator to map one of your groups or roles."}
	}
	id := &Identity{
		Subject:  "oidc:" + str("sub"),
		Name:     firstNonEmpty(str("name"), str("preferred_username"), str("email"), str("sub")),
		Email:    firstNonEmpty(str("email"), str("preferred_username")),
		Role:     role,
		Provider: providerOIDC,
	}
	log.Info("Signed in OIDC user", "user", firstNonEmpty(id.Email, id.Name), "role", id.Role)
	return id, returnTo, nil
}

// ClaimStrings reads a claim at a dotted path (realm_access.roles) as a list of strings: a
// list of strings, or one string.
func ClaimStrings(claims map[string]any, path string) []string {
	var v any = claims
	for key := range strings.SplitSeq(path, ".") {
		m, ok := v.(map[string]any)
		if !ok {
			return nil
		}
		v = m[key]
	}
	switch t := v.(type) {
	case string:
		return []string{t}
	case []any:
		out := make([]string, 0, len(t))
		for _, e := range t {
			if s, ok := e.(string); ok {
				out = append(out, s)
			}
		}
		return out
	}
	return nil
}

// ResolveOIDCRole maps groups and roles to a CostDeck role, the most privileged match
// winning; without a match AutoProvision (default on) grants DefaultRole (default viewer).
// A group mapping matches with or without a leading slash, as Keycloak's full group paths
// carry one.
func ResolveOIDCRole(cfg finopsv1.OIDCConfig, groups, roles []string) (Role, bool) {
	var role Role
	for _, g := range groups {
		bare := strings.TrimPrefix(g, "/")
		for _, key := range []string{g, bare, "/" + bare} {
			if r, ok := ParseRole(cfg.GroupRoleMapping[key]); ok {
				role = Higher(role, r)
			}
		}
	}
	for _, name := range roles {
		if r, ok := ParseRole(name); ok {
			role = Higher(role, r)
		}
	}
	if role != "" {
		return role, true
	}
	if cfg.AutoProvision != nil && !*cfg.AutoProvision {
		return "", false
	}
	if r, ok := ParseRole(cfg.DefaultRole); ok {
		return r, true
	}
	return RoleViewer, true
}

// TestOIDC checks a configuration without a user: the issuer must be discoverable.
func (o *OIDC) TestOIDC(ctx context.Context, cfg finopsv1.OIDCConfig) (string, error) {
	if cfg.IssuerURL == "" || cfg.ClientID == "" {
		return "", errors.New("issuer URL and client ID are required")
	}
	p, _, err := o.provider(ctx, cfg)
	if err != nil {
		return "", err
	}
	return "Found the issuer; its sign-in endpoint is " + p.Endpoint().AuthURL + ". Register " + OIDCCallbackPath + " on your host as the redirect URI.", nil
}
