package api

import (
	"errors"
	"net/http"
	"strings"

	logf "sigs.k8s.io/controller-runtime/pkg/log"

	"github.com/migalsp/costdeck-operator/internal/auth"
	"github.com/migalsp/costdeck-operator/internal/config"
)

// Local user management: administrators create accounts for people without single
// sign-on, choose their role, reset passwords and disable or delete them.

func (s *Server) users() *auth.Users {
	if s.Auth != nil && s.Auth.Users != nil {
		return s.Auth.Users
	}
	return &auth.Users{Client: s.Client}
}

func (s *Server) signIns() *auth.SignIns {
	if s.Auth != nil && s.Auth.SignIns != nil {
		return s.Auth.SignIns
	}
	return &auth.SignIns{Client: s.Client}
}

// BuiltinAccount describes the break-glass admin the Helm chart provisions.
type BuiltinAccount struct {
	Username string `json:"username"`
	Secret   string `json:"secret"`
}

func (s *Server) listUsers(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	users, err := s.users().List(ctx)
	if err != nil {
		writeK8sError(w, err)
		return
	}
	signIns, err := s.signIns().List(ctx)
	if err != nil {
		logf.FromContext(ctx).Error(err, "Could not read the sign-in records")
		signIns = []auth.SignIn{}
	}
	var builtin *BuiltinAccount
	if s.Auth != nil && s.Auth.BuiltinAdmin() != "" {
		builtin = &BuiltinAccount{Username: s.Auth.BuiltinAdmin(), Secret: config.OperatorNamespace() + "/costdeck-operator-admin-credentials"}
	}
	sso := false
	if s.Auth != nil {
		sso = s.Auth.Entra.Enabled(ctx)
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"builtin": builtin, "users": users, "signIns": signIns, "sso": sso,
		"minPasswordLength": auth.MinPasswordLength,
	})
}

func (s *Server) createUser(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Username string `json:"username"`
		Name     string `json:"name"`
		Email    string `json:"email"`
		Role     string `json:"role"`
		// Password is generated when empty and returned once.
		Password           string `json:"password"`
		MustChangePassword *bool  `json:"mustChangePassword"`
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
	generated := ""
	if req.Password == "" {
		generated = auth.GeneratePassword()
		req.Password = generated
	}
	mustChange := req.MustChangePassword == nil || *req.MustChangePassword
	by := actor(r)
	user, err := s.users().Create(r.Context(), auth.User{Username: req.Username, Name: req.Name, Email: req.Email, Role: role}, req.Password, mustChange, by)
	if err != nil {
		writeUserError(w, err)
		return
	}
	logf.FromContext(r.Context()).Info("Created a local user", "user", user.Username, "role", user.Role, "by", by)
	resp := map[string]any{"user": user}
	if generated != "" {
		resp["password"] = generated
	}
	writeJSON(w, http.StatusCreated, resp)
}

func (s *Server) updateUser(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("username")
	var req struct {
		Name     *string `json:"name"`
		Email    *string `json:"email"`
		Role     *string `json:"role"`
		Disabled *bool   `json:"disabled"`
	}
	if err := decodeJSON(w, r, &req); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	patch := auth.UserPatch{Name: req.Name, Email: req.Email, Disabled: req.Disabled}
	if req.Role != nil {
		role, ok := auth.ParseRole(*req.Role)
		if !ok {
			writeError(w, http.StatusBadRequest, "role must be viewer, operator or admin")
			return
		}
		patch.Role = &role
	}
	if isSelf(r, name) && ((patch.Disabled != nil && *patch.Disabled) || (patch.Role != nil && *patch.Role != auth.RoleAdmin)) {
		writeError(w, http.StatusBadRequest, "You cannot disable your own account or take away your own admin role")
		return
	}
	user, err := s.users().Update(r.Context(), name, patch)
	if err != nil {
		writeUserError(w, err)
		return
	}
	logf.FromContext(r.Context()).Info("Updated a local user", "user", name, "role", user.Role, "disabled", user.Disabled, "by", actor(r))
	writeJSON(w, http.StatusOK, user)
}

func (s *Server) resetUserPassword(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("username")
	var req struct {
		Password           string `json:"password"`
		MustChangePassword *bool  `json:"mustChangePassword"`
	}
	if err := decodeJSON(w, r, &req); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	generated := ""
	if req.Password == "" {
		generated = auth.GeneratePassword()
		req.Password = generated
	}
	mustChange := req.MustChangePassword == nil || *req.MustChangePassword
	if err := s.users().SetPassword(r.Context(), name, req.Password, mustChange); err != nil {
		writeUserError(w, err)
		return
	}
	logf.FromContext(r.Context()).Info("Reset a local user's password", "user", name, "by", actor(r))
	resp := map[string]any{"status": "ok"}
	if generated != "" {
		resp["password"] = generated
	}
	writeJSON(w, http.StatusOK, resp)
}

func (s *Server) deleteUser(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("username")
	if isSelf(r, name) {
		writeError(w, http.StatusBadRequest, "You cannot delete your own account")
		return
	}
	if err := s.users().Delete(r.Context(), name); err != nil {
		writeUserError(w, err)
		return
	}
	logf.FromContext(r.Context()).Info("Deleted a local user", "user", name, "by", actor(r))
	w.WriteHeader(http.StatusNoContent)
}

// actor names who made a change, for logs and the createdBy field.
func actor(r *http.Request) string {
	if id := auth.FromContext(r.Context()); id != nil {
		return id.Subject
	}
	return ""
}

func isSelf(r *http.Request, username string) bool {
	name, ok := auth.ManagedUser(auth.FromContext(r.Context()))
	return ok && name == username
}

func writeUserError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, auth.ErrUserNotFound):
		writeError(w, http.StatusNotFound, "No such user")
	case errors.Is(err, auth.ErrInvalidUser):
		writeError(w, http.StatusBadRequest, strings.TrimPrefix(err.Error(), auth.ErrInvalidUser.Error()+": "))
	default:
		writeK8sError(w, err)
	}
}
