package api

import (
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"strings"
	"time"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/util/retry"
	"sigs.k8s.io/controller-runtime/pkg/client"

	finopsv1 "github.com/migalsp/costdeck-operator/api/v1"
)

func (s *Server) handleScalingGroups(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	operatorNs := getOperatorNamespace()

	switch r.Method {
	case http.MethodGet:
		var list finopsv1.ScalingGroupList
		if err := s.Client.List(ctx, &list, client.InNamespace(operatorNs)); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(list.Items)

	case http.MethodPost:
		var group finopsv1.ScalingGroup
		if err := json.NewDecoder(r.Body).Decode(&group); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		group.Namespace = operatorNs
		if err := s.Client.Create(ctx, &group); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		w.WriteHeader(http.StatusCreated)
		json.NewEncoder(w).Encode(group)

	default:
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
	}
}

func (s *Server) handleScalingGroupActions(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	parts := strings.Split(r.URL.Path, "/")
	if len(parts) < 5 {
		http.Error(w, "Invalid path", http.StatusBadRequest)
		return
	}
	name := parts[4]
	operatorNs := getOperatorNamespace()

	group := &finopsv1.ScalingGroup{}
	if err := s.Client.Get(ctx, client.ObjectKey{Name: name, Namespace: operatorNs}, group); err != nil {
		if errors.IsNotFound(err) {
			http.Error(w, "Group not found", http.StatusNotFound)
		} else {
			http.Error(w, err.Error(), http.StatusInternalServerError)
		}
		return
	}

	// Sub-actions like /api/scaling/groups/{name}/manual or /events
	if len(parts) > 5 {
		if parts[5] == "manual" {
			s.handleScalingGroupManual(w, r, group)
			return
		}
		if parts[5] == "events" {
			s.handleScalingGroupEvents(w, r, group)
			return
		}
	}

	switch r.Method {
	case http.MethodGet:
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(group)

	case http.MethodPut:
		var updated finopsv1.ScalingGroup
		if err := json.NewDecoder(r.Body).Decode(&updated); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}

		err := retry.RetryOnConflict(retry.DefaultRetry, func() error {
			current := &finopsv1.ScalingGroup{}
			if err := s.Client.Get(ctx, client.ObjectKey{Name: name, Namespace: operatorNs}, current); err != nil {
				return err
			}
			current.Spec = updated.Spec
			return s.Client.Update(ctx, current)
		})

		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		json.NewEncoder(w).Encode(updated)

	case http.MethodDelete:
		if err := s.Client.Delete(ctx, group); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		w.WriteHeader(http.StatusNoContent)

	default:
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
	}
}

// manualOverrideRequest is the payload of the /manual endpoints.
//
// A null (or omitted) "active" clears the override and hands control back to the
// schedule -- this is the only way out of a forced state, so it must stay supported.
// An optional "activeUntil" bounds the override in time.
type manualOverrideRequest struct {
	Active      *bool        `json:"active"`
	ActiveUntil *metav1.Time `json:"activeUntil,omitempty"`
}

// resolve normalises the request: an override deadline is meaningless without an
// override, and a deadline already in the past would be a no-op the user cannot see.
func (req manualOverrideRequest) resolve() (*bool, *metav1.Time, error) {
	if req.Active == nil {
		return nil, nil, nil
	}
	if req.ActiveUntil == nil || req.ActiveUntil.IsZero() {
		return req.Active, nil, nil
	}
	if !req.ActiveUntil.After(time.Now()) {
		return nil, nil, fmt.Errorf("activeUntil must be in the future")
	}
	return req.Active, req.ActiveUntil, nil
}

func decodeManualOverride(w http.ResponseWriter, r *http.Request) (*bool, *metav1.Time, bool) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return nil, nil, false
	}

	var req manualOverrideRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return nil, nil, false
	}

	active, until, err := req.resolve()
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return nil, nil, false
	}
	return active, until, true
}

func (s *Server) handleScalingGroupManual(w http.ResponseWriter, r *http.Request, group *finopsv1.ScalingGroup) {
	active, until, ok := decodeManualOverride(w, r)
	if !ok {
		return
	}

	ctx := r.Context()
	key := client.ObjectKeyFromObject(group)
	updated := &finopsv1.ScalingGroup{}
	err := retry.RetryOnConflict(retry.DefaultRetry, func() error {
		if err := s.Client.Get(ctx, key, updated); err != nil {
			return err
		}
		updated.Spec.Active = active
		updated.Spec.ActiveUntil = until
		return s.Client.Update(ctx, updated)
	})
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(updated)
}

func (s *Server) handleScalingGroupEvents(w http.ResponseWriter, r *http.Request, group *finopsv1.ScalingGroup) {
	if r.Method != http.MethodGet {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	ctx := r.Context()
	var events corev1.EventList

	// Filter events targeting this specific ScalingGroup
	err := s.Client.List(ctx, &events, client.InNamespace(group.Namespace), client.MatchingFields{"involvedObject.name": group.Name})
	if err != nil {
		// Log the error but return empty array if field selector fails
		// In some setups, field selectors might require index setup, try fallback filtering if needed.
		// For now, if exact field matching is strict, we fetch all in namespace and filter in memory.
		err = s.Client.List(ctx, &events, client.InNamespace(group.Namespace))
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
	}

	// Filter in memory to ensure we only return ScalingGroup events
	var filtered []corev1.Event
	for _, e := range events.Items {
		if e.InvolvedObject.Kind == "ScalingGroup" && e.InvolvedObject.Name == group.Name {
			filtered = append(filtered, e)
		}
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(filtered)
}

func (s *Server) handleScalingConfigs(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	operatorNs := getOperatorNamespace()

	switch r.Method {
	case http.MethodGet:
		var list finopsv1.ScalingConfigList
		if err := s.Client.List(ctx, &list, client.InNamespace(operatorNs)); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(list.Items)

	case http.MethodPost:
		var config finopsv1.ScalingConfig
		if err := json.NewDecoder(r.Body).Decode(&config); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		config.Namespace = operatorNs
		if err := s.Client.Create(ctx, &config); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		w.WriteHeader(http.StatusCreated)
		json.NewEncoder(w).Encode(config)

	default:
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
	}
}

func (s *Server) handleScalingConfigActions(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	parts := strings.Split(r.URL.Path, "/")
	if len(parts) < 5 {
		http.Error(w, "Invalid path", http.StatusBadRequest)
		return
	}
	name := parts[4]
	operatorNs := getOperatorNamespace()

	config := &finopsv1.ScalingConfig{}
	if err := s.Client.Get(ctx, client.ObjectKey{Name: name, Namespace: operatorNs}, config); err != nil {
		if errors.IsNotFound(err) {
			http.Error(w, "Config not found", http.StatusNotFound)
		} else {
			http.Error(w, err.Error(), http.StatusInternalServerError)
		}
		return
	}

	if len(parts) > 5 && parts[5] == "manual" {
		s.handleScalingConfigManual(w, r, config)
		return
	}

	switch r.Method {
	case http.MethodGet:
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(config)

	case http.MethodPut:
		var updated finopsv1.ScalingConfig
		if err := json.NewDecoder(r.Body).Decode(&updated); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}

		err := retry.RetryOnConflict(retry.DefaultRetry, func() error {
			current := &finopsv1.ScalingConfig{}
			if err := s.Client.Get(ctx, client.ObjectKey{Name: name, Namespace: operatorNs}, current); err != nil {
				return err
			}
			current.Spec = updated.Spec
			return s.Client.Update(ctx, current)
		})

		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		json.NewEncoder(w).Encode(updated)

	case http.MethodDelete:
		if err := s.Client.Delete(ctx, config); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		w.WriteHeader(http.StatusNoContent)

	default:
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
	}
}

func (s *Server) handleScalingConfigManual(w http.ResponseWriter, r *http.Request, config *finopsv1.ScalingConfig) {
	active, until, ok := decodeManualOverride(w, r)
	if !ok {
		return
	}

	ctx := r.Context()
	key := client.ObjectKeyFromObject(config)
	updated := &finopsv1.ScalingConfig{}
	err := retry.RetryOnConflict(retry.DefaultRetry, func() error {
		if err := s.Client.Get(ctx, key, updated); err != nil {
			return err
		}
		updated.Spec.Active = active
		updated.Spec.ActiveUntil = until
		return s.Client.Update(ctx, updated)
	})
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(updated)
}

func getOperatorNamespace() string {
	ns := os.Getenv("POD_NAMESPACE")
	if ns == "" {
		return "costdeck"
	}
	return ns
}
