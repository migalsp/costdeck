package api

import (
	"net/http"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/migalsp/costdeck-operator/internal/config"
	"github.com/migalsp/costdeck-operator/internal/scaling"
)

// handleSelectNamespaces previews a namespace selector: GET /api/namespace-selector?selector=
// takes kubectl's syntax ("env=dev,team in (a,b),!legacy") and answers with the selector as
// a schedule stores it and the namespaces it matches now, so the dashboard needs no parser.
func (s *Server) handleSelectNamespaces(w http.ResponseWriter, r *http.Request) {
	text := r.URL.Query().Get("selector")
	if text == "" {
		writeError(w, http.StatusBadRequest, "selector is required, for example env=dev")
		return
	}
	selector, err := metav1.ParseToLabelSelector(text)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	var namespaces corev1.NamespaceList
	if err := s.Client.List(r.Context(), &namespaces); err != nil {
		writeK8sError(w, err)
		return
	}
	matched, err := scaling.SelectNamespaces(selector, namespaces.Items, config.OperatorNamespace())
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	if matched == nil {
		matched = []string{}
	}
	writeJSON(w, http.StatusOK, map[string]any{"selector": selector, "namespaces": matched})
}
