package api

import (
	"context"
	"net/http"
	"strings"
	"time"

	authorizationv1 "k8s.io/api/authorization/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	finopsv1 "github.com/migalsp/costdeck-operator/api/v1"
	"github.com/migalsp/costdeck-operator/internal/cleanup"
	"github.com/migalsp/costdeck-operator/internal/config"
)

// NamespaceTTLSettings is spec.namespaceTTL as the settings API reads and writes it;
// durations are Go durations such as 168h0m0s, or days ("7d") when written.
type NamespaceTTLSettings struct {
	Enabled    bool   `json:"enabled"`
	DryRun     bool   `json:"dryRun"`
	Selector   string `json:"selector"`
	TTL        string `json:"ttl,omitempty"`
	WarnBefore string `json:"warnBefore,omitempty"`
}

// NamespaceTTLUpdateRequest changes spec.namespaceTTL; a field left out stays as it is.
type NamespaceTTLUpdateRequest struct {
	Enabled    *bool   `json:"enabled,omitempty"`
	DryRun     *bool   `json:"dryRun,omitempty"`
	Selector   *string `json:"selector,omitempty"`
	TTL        *string `json:"ttl,omitempty"`
	WarnBefore *string `json:"warnBefore,omitempty"`
}

func namespaceTTLSettings(t finopsv1.NamespaceTTLConfig) NamespaceTTLSettings {
	out := NamespaceTTLSettings{Enabled: t.Enabled, DryRun: t.DryRun, Selector: t.Selector}
	if t.TTL != nil {
		out.TTL = t.TTL.Duration.String()
	}
	if t.WarnBefore != nil {
		out.WarnBefore = t.WarnBefore.Duration.String()
	}
	return out
}

func applyNamespaceTTLSettings(_ context.Context, cfg *finopsv1.CostDeckConfig, req *SettingsUpdateRequest) error {
	u := req.NamespaceTTL
	if u == nil {
		return nil
	}
	t := &cfg.Spec.NamespaceTTL
	if u.Enabled != nil {
		t.Enabled = *u.Enabled
	}
	if u.DryRun != nil {
		t.DryRun = *u.DryRun
	}
	if u.Selector != nil {
		t.Selector = strings.TrimSpace(*u.Selector)
	}
	duration := func(raw string, what string) (*metav1.Duration, error) {
		if strings.TrimSpace(raw) == "" {
			return nil, nil
		}
		if raw == "0s" || raw == "0" {
			return &metav1.Duration{}, nil
		}
		d, off, err := cleanup.ParseTTL(raw)
		if err != nil || off {
			return nil, badRequestf("preview cleanup: %s %q is not a duration such as 7d or 36h", what, raw)
		}
		return &metav1.Duration{Duration: d}, nil
	}
	var err error
	if u.TTL != nil {
		if t.TTL, err = duration(*u.TTL, "the time to live"); err != nil {
			return err
		}
	}
	if u.WarnBefore != nil {
		if t.WarnBefore, err = duration(*u.WarnBefore, "the warning"); err != nil {
			return err
		}
	}
	if t.Selector != "" || t.Enabled {
		if _, err := cleanup.Selector(t.Selector); err != nil {
			return badRequestf("preview cleanup: %v", err)
		}
	}
	if t.Enabled && (t.TTL == nil || t.TTL.Duration < time.Hour) {
		return badRequestf("preview cleanup: the time to live must be at least an hour")
	}
	if t.TTL != nil && t.WarnBefore != nil && t.WarnBefore.Duration >= t.TTL.Duration {
		return badRequestf("preview cleanup: the warning must come before the time to live is up")
	}
	return nil
}

// namespaceTTLResponse is GET /api/namespace-ttl: the setting, what it will delete and
// when, and what it deleted.
type namespaceTTLResponse struct {
	NamespaceTTLSettings
	// Permitted says whether the operator may delete namespaces (namespaceTTL.enabled).
	Permitted  bool               `json:"permitted"`
	Error      string             `json:"error,omitempty"`
	Namespaces []cleanup.Entry    `json:"namespaces"`
	Deleted    []cleanup.Deletion `json:"deleted"`
}

// handleNamespaceTTL serves the plan of the saved setting, or of the unsaved selector and
// time to live in ?selector= and ?ttl=, so the form shows what it would do before saving.
func (s *Server) handleNamespaceTTL(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	cfg, err := config.Get(ctx, s.Client)
	if err != nil {
		writeK8sError(w, err)
		return
	}
	t := cfg.Spec.NamespaceTTL
	out := namespaceTTLResponse{NamespaceTTLSettings: namespaceTTLSettings(t), Namespaces: []cleanup.Entry{}, Deleted: []cleanup.Deletion{}, Permitted: s.mayDeleteNamespaces(ctx)}
	q := r.URL.Query()
	if q.Has("selector") {
		t.Selector = strings.TrimSpace(q.Get("selector"))
	}
	if raw := q.Get("ttl"); raw != "" {
		if d, off, err := cleanup.ParseTTL(raw); err == nil && !off {
			t.TTL = &metav1.Duration{Duration: d}
		}
	}
	if t.Selector != "" {
		if plan, err := cleanup.Plan(ctx, s.Client, t); err != nil {
			out.Error = err.Error()
		} else {
			out.Namespaces = plan
		}
	}
	if deleted, err := cleanup.Deleted(ctx, s.Client); err == nil {
		out.Deleted = deleted
	}
	writeJSON(w, http.StatusOK, out)
}

// mayDeleteNamespaces asks the API server whether the operator may delete namespaces,
// which the chart grants with namespaceTTL.enabled.
func (s *Server) mayDeleteNamespaces(ctx context.Context) bool {
	if s.K8sClient == nil {
		return false
	}
	review, err := s.K8sClient.AuthorizationV1().SelfSubjectAccessReviews().Create(ctx, &authorizationv1.SelfSubjectAccessReview{
		Spec: authorizationv1.SelfSubjectAccessReviewSpec{ResourceAttributes: &authorizationv1.ResourceAttributes{Verb: "delete", Resource: "namespaces"}},
	}, metav1.CreateOptions{})
	return err == nil && review.Status.Allowed
}

func (s *Server) handleExtendNamespace(w http.ResponseWriter, r *http.Request) {
	var req struct {
		By string `json:"by"`
	}
	if err := decodeJSON(w, r, &req); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	by, off, err := cleanup.ParseTTL(req.By)
	if err != nil || off || by > 90*24*time.Hour {
		writeError(w, http.StatusBadRequest, "extend by a duration such as 3d or 12h, at most 90d")
		return
	}
	ctx := r.Context()
	cfg, err := config.Get(ctx, s.Client)
	if err != nil {
		writeK8sError(w, err)
		return
	}
	if cfg.Spec.NamespaceTTL.Selector == "" {
		writeError(w, http.StatusBadRequest, "preview cleanup is not set up")
		return
	}
	entry, err := cleanup.Extend(ctx, s.Client, cfg.Spec.NamespaceTTL, r.PathValue("ns"), by, time.Now())
	switch {
	case apierrors.IsNotFound(err):
		writeError(w, http.StatusNotFound, "preview cleanup does not select "+r.PathValue("ns"))
	case apierrors.IsForbidden(err):
		writeError(w, http.StatusForbidden, "the operator may not change namespaces: install with namespaceTTL.enabled=true")
	case err != nil && entry.Protected != "":
		writeError(w, http.StatusBadRequest, err.Error())
	case err != nil:
		writeK8sError(w, err)
	default:
		writeJSON(w, http.StatusOK, entry)
	}
}
