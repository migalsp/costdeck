package api

import (
	"errors"
	"fmt"
	"net/http"
	"time"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/util/retry"
	"sigs.k8s.io/controller-runtime/pkg/client"

	finopsv1 "github.com/migalsp/costdeck-operator/api/v1"
	"github.com/migalsp/costdeck-operator/internal/config"
	"github.com/migalsp/costdeck-operator/internal/scaling"
)

// writeK8sError maps an API server error onto the matching HTTP status.
func writeK8sError(w http.ResponseWriter, err error) {
	switch {
	case apierrors.IsNotFound(err):
		writeError(w, http.StatusNotFound, err.Error())
	case apierrors.IsAlreadyExists(err), apierrors.IsConflict(err):
		writeError(w, http.StatusConflict, err.Error())
	case apierrors.IsInvalid(err), apierrors.IsBadRequest(err):
		writeError(w, http.StatusUnprocessableEntity, err.Error())
	case apierrors.IsForbidden(err):
		writeError(w, http.StatusForbidden, err.Error())
	default:
		writeError(w, http.StatusInternalServerError, err.Error())
	}
}

// ─── ScalingGroups ──────────────────────────────────────────────────────────

func (s *Server) listScalingGroups(w http.ResponseWriter, r *http.Request) {
	var list finopsv1.ScalingGroupList
	if err := s.Client.List(r.Context(), &list, client.InNamespace(config.OperatorNamespace())); err != nil {
		writeK8sError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, list.Items)
}

func (s *Server) createScalingGroup(w http.ResponseWriter, r *http.Request) {
	var group finopsv1.ScalingGroup
	if err := decodeJSON(w, r, &group); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	group.Namespace = config.OperatorNamespace()
	group.ResourceVersion = ""
	group.Status = finopsv1.ScalingGroupStatus{}
	// A new group must start schedule-driven: an override seeded at creation time would
	// make it permanently immune to its own schedule.
	group.Spec.Active = nil
	group.Spec.ActiveUntil = nil
	if err := s.Client.Create(r.Context(), &group); err != nil {
		writeK8sError(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, group)
}

func (s *Server) getScalingGroup(w http.ResponseWriter, r *http.Request) {
	group := &finopsv1.ScalingGroup{}
	if err := s.Client.Get(r.Context(), s.scalingKey(r), group); err != nil {
		writeK8sError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, group)
}

// updateScalingGroup replaces the whole spec. Fields the client omits are dropped, which
// is how saving a schedule from the dashboard clears a manual override.
func (s *Server) updateScalingGroup(w http.ResponseWriter, r *http.Request) {
	var updated finopsv1.ScalingGroup
	if err := decodeJSON(w, r, &updated); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}

	current := &finopsv1.ScalingGroup{}
	err := retry.RetryOnConflict(retry.DefaultRetry, func() error {
		if err := s.Client.Get(r.Context(), s.scalingKey(r), current); err != nil {
			return err
		}
		current.Spec = updated.Spec
		return s.Client.Update(r.Context(), current)
	})
	if err != nil {
		writeK8sError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, current)
}

func (s *Server) deleteScalingGroup(w http.ResponseWriter, r *http.Request) {
	group := &finopsv1.ScalingGroup{}
	group.Name, group.Namespace = r.PathValue("name"), config.OperatorNamespace()
	if err := s.Client.Delete(r.Context(), group); err != nil {
		writeK8sError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) scalingKey(r *http.Request) client.ObjectKey {
	return client.ObjectKey{Name: r.PathValue("name"), Namespace: config.OperatorNamespace()}
}

// manualOverrideRequest is the payload of the /manual endpoints.
//
// A null (or omitted) "active" clears the override and hands control back to the
// schedule -- this is the only way out of a forced state, so it must stay supported.
// The override can be bounded in time either with an absolute "activeUntil" or with
// "until": "nextTransition" (hold it until the schedule would change state on its own)
// or a duration such as "4h".
type manualOverrideRequest struct {
	Active      *bool        `json:"active"`
	ActiveUntil *metav1.Time `json:"activeUntil,omitempty"`
	Until       string       `json:"until,omitempty"`
}

// UntilNextTransition holds an override until the next schedule change.
const UntilNextTransition = "nextTransition"

// ResolveOverride normalises an override request against the schedules: an override
// deadline is meaningless without an override, and a deadline already in the past would be
// a no-op the user cannot see.
func ResolveOverride(active *bool, activeUntil *metav1.Time, until string, schedules []finopsv1.ScalingSchedule, now time.Time) (*bool, *metav1.Time, error) {
	if active == nil {
		return nil, nil, nil
	}
	switch {
	case until == UntilNextTransition:
		next := (&scaling.Engine{}).NextScheduleChange(now, schedules)
		if next == nil {
			return nil, nil, fmt.Errorf("the schedule never changes state on its own; use a duration or an explicit activeUntil")
		}
		t := metav1.NewTime(*next)
		activeUntil = &t
	case until != "":
		d, err := time.ParseDuration(until)
		if err != nil || d <= 0 {
			return nil, nil, fmt.Errorf("until must be %q or a positive duration such as 4h", UntilNextTransition)
		}
		t := metav1.NewTime(now.Add(d).Truncate(time.Second))
		activeUntil = &t
	}
	if activeUntil == nil || activeUntil.IsZero() {
		return active, nil, nil
	}
	if !activeUntil.After(now) {
		return nil, nil, fmt.Errorf("activeUntil must be in the future")
	}
	return active, activeUntil, nil
}

func decodeManualOverride(w http.ResponseWriter, r *http.Request) (manualOverrideRequest, bool) {
	var req manualOverrideRequest
	if err := decodeJSON(w, r, &req); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return req, false
	}
	return req, true
}

// errBadOverride marks override validation failures inside a retry loop.
type errBadOverride struct{ error }

func (s *Server) handleScalingGroupManual(w http.ResponseWriter, r *http.Request) {
	req, ok := decodeManualOverride(w, r)
	if !ok {
		return
	}

	updated := &finopsv1.ScalingGroup{}
	err := retry.RetryOnConflict(retry.DefaultRetry, func() error {
		if err := s.Client.Get(r.Context(), s.scalingKey(r), updated); err != nil {
			return err
		}
		active, until, err := ResolveOverride(req.Active, req.ActiveUntil, req.Until, updated.Spec.Schedules, time.Now())
		if err != nil {
			return errBadOverride{err}
		}
		updated.Spec.Active = active
		updated.Spec.ActiveUntil = until
		if active == nil {
			delete(updated.Annotations, scaling.LegacyOverrideAnnotation)
		}
		return s.Client.Update(r.Context(), updated)
	})
	writeOverrideResult(w, err, updated)
}

func writeOverrideResult(w http.ResponseWriter, err error, obj any) {
	var bad errBadOverride
	switch {
	case errors.As(err, &bad):
		writeError(w, http.StatusBadRequest, bad.Error())
	case err != nil:
		writeK8sError(w, err)
	default:
		writeJSON(w, http.StatusOK, obj)
	}
}

func (s *Server) handleScalingGroupEvents(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	var events corev1.EventList
	// Field selectors on involvedObject are not indexed in the cache, so filter in memory.
	if err := s.Client.List(r.Context(), &events, client.InNamespace(config.OperatorNamespace())); err != nil {
		writeK8sError(w, err)
		return
	}

	filtered := []corev1.Event{}
	for _, e := range events.Items {
		if e.InvolvedObject.Kind == "ScalingGroup" && e.InvolvedObject.Name == name {
			filtered = append(filtered, e)
		}
	}
	writeJSON(w, http.StatusOK, filtered)
}

// ─── ScalingConfigs ─────────────────────────────────────────────────────────

func (s *Server) listScalingConfigs(w http.ResponseWriter, r *http.Request) {
	var list finopsv1.ScalingConfigList
	if err := s.Client.List(r.Context(), &list, client.InNamespace(config.OperatorNamespace())); err != nil {
		writeK8sError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, list.Items)
}

func (s *Server) createScalingConfig(w http.ResponseWriter, r *http.Request) {
	var cfg finopsv1.ScalingConfig
	if err := decodeJSON(w, r, &cfg); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	cfg.Namespace = config.OperatorNamespace()
	cfg.ResourceVersion = ""
	cfg.Status = finopsv1.ScalingConfigStatus{}
	cfg.Spec.Active = nil
	cfg.Spec.ActiveUntil = nil
	if err := s.Client.Create(r.Context(), &cfg); err != nil {
		writeK8sError(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, cfg)
}

func (s *Server) getScalingConfig(w http.ResponseWriter, r *http.Request) {
	cfg := &finopsv1.ScalingConfig{}
	if err := s.Client.Get(r.Context(), s.scalingKey(r), cfg); err != nil {
		writeK8sError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, cfg)
}

func (s *Server) updateScalingConfig(w http.ResponseWriter, r *http.Request) {
	var updated finopsv1.ScalingConfig
	if err := decodeJSON(w, r, &updated); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}

	current := &finopsv1.ScalingConfig{}
	err := retry.RetryOnConflict(retry.DefaultRetry, func() error {
		if err := s.Client.Get(r.Context(), s.scalingKey(r), current); err != nil {
			return err
		}
		current.Spec = updated.Spec
		return s.Client.Update(r.Context(), current)
	})
	if err != nil {
		writeK8sError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, current)
}

func (s *Server) deleteScalingConfig(w http.ResponseWriter, r *http.Request) {
	cfg := &finopsv1.ScalingConfig{}
	cfg.Name, cfg.Namespace = r.PathValue("name"), config.OperatorNamespace()
	if err := s.Client.Delete(r.Context(), cfg); err != nil {
		writeK8sError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) handleScalingConfigManual(w http.ResponseWriter, r *http.Request) {
	req, ok := decodeManualOverride(w, r)
	if !ok {
		return
	}

	updated := &finopsv1.ScalingConfig{}
	err := retry.RetryOnConflict(retry.DefaultRetry, func() error {
		if err := s.Client.Get(r.Context(), s.scalingKey(r), updated); err != nil {
			return err
		}
		active, until, err := ResolveOverride(req.Active, req.ActiveUntil, req.Until, updated.Spec.Schedules, time.Now())
		if err != nil {
			return errBadOverride{err}
		}
		updated.Spec.Active = active
		updated.Spec.ActiveUntil = until
		if active == nil {
			delete(updated.Annotations, scaling.LegacyOverrideAnnotation)
		}
		return s.Client.Update(r.Context(), updated)
	})
	writeOverrideResult(w, err, updated)
}
