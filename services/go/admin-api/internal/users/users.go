// Package users implements the platform-admin user-management surface of
// admin-api: list/create users, assign/revoke realm roles, disable/enable
// accounts and trigger password resets. All operations go through the
// Keycloak Admin REST client (or the simulated dev fallback).
package users

import (
	"encoding/json"
	"net/http"
	"strings"

	"github.com/go-chi/chi/v5"
	"go.uber.org/zap"

	"github.com/munisp/hydrogenTransport/services/go/admin-api/internal/httpx"
	"github.com/munisp/hydrogenTransport/services/go/admin-api/internal/keycloak"
)

// Handler serves the /v1/users routes (all RequireRole platform-admin).
type Handler struct {
	kc  keycloak.AdminClient
	log *zap.Logger
}

// NewHandler wires the user-management handlers.
func NewHandler(kc keycloak.AdminClient, log *zap.Logger) *Handler {
	return &Handler{kc: kc, log: log}
}

// List handles GET /v1/users?role=&q= -> {"users": [...]}.
func (h *Handler) List(w http.ResponseWriter, r *http.Request) {
	role := strings.TrimSpace(r.URL.Query().Get("role"))
	q := strings.TrimSpace(r.URL.Query().Get("q"))
	users, err := h.kc.ListUsers(r.Context(), q, role, 100)
	if err != nil {
		h.log.Error("list users", zap.Error(err))
		httpx.Error(w, http.StatusBadGateway, "failed to list users: "+err.Error())
		return
	}
	httpx.JSON(w, http.StatusOK, map[string]any{"users": users})
}

type createBody struct {
	Email       string   `json:"email"`
	DisplayName string   `json:"display_name"`
	Roles       []string `json:"roles"`
}

// Create handles POST /v1/users with {email, display_name, roles?}. The user
// is provisioned with a temporary password and a VERIFY_EMAIL +
// UPDATE_PASSWORD actions email.
func (h *Handler) Create(w http.ResponseWriter, r *http.Request) {
	var body createBody
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&body); err != nil {
		httpx.Error(w, http.StatusBadRequest, "invalid JSON body: "+err.Error())
		return
	}
	body.Email = strings.TrimSpace(body.Email)
	body.DisplayName = strings.TrimSpace(body.DisplayName)
	if body.Email == "" || !strings.Contains(body.Email, "@") {
		httpx.Error(w, http.StatusBadRequest, "email is required")
		return
	}
	if body.DisplayName == "" {
		httpx.Error(w, http.StatusBadRequest, "display_name is required")
		return
	}
	id, err := h.kc.CreateUser(r.Context(), keycloak.CreateUserSpec{
		Username:    body.Email,
		Email:       body.Email,
		DisplayName: body.DisplayName,
	})
	if err != nil {
		h.log.Error("create user", zap.String("email", body.Email), zap.Error(err))
		httpx.Error(w, http.StatusBadGateway, "failed to create user: "+err.Error())
		return
	}
	for _, role := range body.Roles {
		if err := h.kc.AssignRealmRole(r.Context(), id, role); err != nil {
			h.log.Error("assign role on create", zap.String("role", role), zap.Error(err))
			httpx.Error(w, http.StatusBadGateway, "user created but role assignment failed: "+err.Error())
			return
		}
	}
	if err := h.kc.SendActionsEmail(r.Context(), id, []string{"VERIFY_EMAIL", "UPDATE_PASSWORD"}); err != nil {
		// Non-fatal: the account exists, the email can be re-sent via
		// POST /v1/users/{id}/reset-password.
		h.log.Warn("actions email failed after create", zap.String("user_id", id), zap.Error(err))
	}
	httpx.JSON(w, http.StatusCreated, map[string]any{"id": id})
}

type rolesBody struct {
	Add    []string `json:"add"`
	Remove []string `json:"remove"`
}

// UpdateRoles handles PUT /v1/users/{id}/roles with {add: [...], remove: [...]}
// to assign/revoke Keycloak realm roles.
func (h *Handler) UpdateRoles(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	var body rolesBody
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&body); err != nil {
		httpx.Error(w, http.StatusBadRequest, "invalid JSON body: "+err.Error())
		return
	}
	if len(body.Add) == 0 && len(body.Remove) == 0 {
		httpx.Error(w, http.StatusBadRequest, "provide add and/or remove role lists")
		return
	}
	for _, role := range body.Add {
		if err := h.kc.AssignRealmRole(r.Context(), id, role); err != nil {
			h.log.Error("assign role", zap.String("user_id", id), zap.String("role", role), zap.Error(err))
			httpx.Error(w, http.StatusBadGateway, "failed to assign role "+role+": "+err.Error())
			return
		}
	}
	for _, role := range body.Remove {
		if err := h.kc.RevokeRealmRole(r.Context(), id, role); err != nil {
			h.log.Error("revoke role", zap.String("user_id", id), zap.String("role", role), zap.Error(err))
			httpx.Error(w, http.StatusBadGateway, "failed to revoke role "+role+": "+err.Error())
			return
		}
	}
	httpx.JSON(w, http.StatusOK, map[string]any{"id": id, "added": body.Add, "removed": body.Remove})
}

// Disable handles POST /v1/users/{id}/disable.
func (h *Handler) Disable(w http.ResponseWriter, r *http.Request) {
	h.setEnabled(w, r, false)
}

// Enable handles POST /v1/users/{id}/enable.
func (h *Handler) Enable(w http.ResponseWriter, r *http.Request) {
	h.setEnabled(w, r, true)
}

func (h *Handler) setEnabled(w http.ResponseWriter, r *http.Request, enabled bool) {
	id := chi.URLParam(r, "id")
	if err := h.kc.SetEnabled(r.Context(), id, enabled); err != nil {
		h.log.Error("set user enabled", zap.String("user_id", id), zap.Bool("enabled", enabled), zap.Error(err))
		httpx.Error(w, http.StatusBadGateway, "failed to update user: "+err.Error())
		return
	}
	httpx.JSON(w, http.StatusOK, map[string]any{"id": id, "enabled": enabled})
}

// ResetPassword handles POST /v1/users/{id}/reset-password: sends the
// Keycloak execute-actions email with UPDATE_PASSWORD.
func (h *Handler) ResetPassword(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	if err := h.kc.SendActionsEmail(r.Context(), id, []string{"UPDATE_PASSWORD"}); err != nil {
		h.log.Error("reset password", zap.String("user_id", id), zap.Error(err))
		httpx.Error(w, http.StatusBadGateway, "failed to trigger password reset: "+err.Error())
		return
	}
	httpx.JSON(w, http.StatusOK, map[string]any{"id": id, "message": "password-reset email sent"})
}
