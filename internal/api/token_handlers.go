package api

import (
	"net/http"
	"time"

	"github.com/migalsp/costdeck-operator/internal/auth"
)

// API tokens let machine clients (MCP clients, CI, scripts) call CostDeck with
// Authorization: Bearer cdk_... instead of a browser session.

func (s *Server) tokens() *auth.Tokens {
	if s.Auth != nil && s.Auth.Tokens != nil {
		return s.Auth.Tokens
	}
	return &auth.Tokens{Client: s.Client}
}

func (s *Server) listTokens(w http.ResponseWriter, r *http.Request) {
	list, err := s.tokens().List(r.Context())
	if err != nil {
		writeK8sError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, list)
}

func (s *Server) createToken(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Name          string `json:"name"`
		Role          string `json:"role"`
		ExpiresInDays int    `json:"expiresInDays"`
	}
	if err := decodeJSON(w, r, &req); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	role, ok := auth.ParseRole(req.Role)
	if !ok {
		writeError(w, http.StatusBadRequest, "role must be viewer, operator or admin")
		return
	}
	if req.ExpiresInDays < 0 || req.ExpiresInDays > 3650 {
		writeError(w, http.StatusBadRequest, "expiresInDays must be between 0 (never) and 3650")
		return
	}
	createdBy := ""
	if id := auth.FromContext(r.Context()); id != nil {
		createdBy = id.Subject
	}
	token, info, err := s.tokens().Create(r.Context(), req.Name, role, time.Duration(req.ExpiresInDays)*24*time.Hour, createdBy)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	writeJSON(w, http.StatusCreated, map[string]any{"token": token, "info": info})
}

func (s *Server) deleteToken(w http.ResponseWriter, r *http.Request) {
	if err := s.tokens().Delete(r.Context(), r.PathValue("name")); err != nil {
		writeK8sError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
