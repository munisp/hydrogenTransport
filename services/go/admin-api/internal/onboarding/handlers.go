package onboarding

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"math/big"
	"net/http"
	"regexp"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"go.uber.org/zap"

	auth "github.com/munisp/hydrogenTransport/packages/go-auth"
	"github.com/munisp/hydrogenTransport/services/go/admin-api/internal/httpx"
	"github.com/munisp/hydrogenTransport/services/go/admin-api/internal/keycloak"
)

// Keycloak actions emailed to newly provisioned users.
var welcomeActions = []string{"VERIFY_EMAIL", "UPDATE_PASSWORD"}

var emailRe = regexp.MustCompile(`^[^@\s]+@[^@\s]+\.[^@\s]+$`)

// KeycloakClient is the subset of keycloak.AdminClient the onboarding
// handlers use (kept as a separate interface for easy mocking in tests).
type KeycloakClient interface {
	CreateUser(ctx context.Context, spec keycloak.CreateUserSpec) (string, error)
	SetTemporaryPassword(ctx context.Context, userID, password string) error
	AssignRealmRole(ctx context.Context, userID, role string) error
	SendActionsEmail(ctx context.Context, userID string, actions []string) error
}

// Handler serves the /v1/onboarding routes.
type Handler struct {
	store    Store
	kc       KeycloakClient
	log      *zap.Logger
	password func() string // temp-password generator (injectable for tests)
}

// NewHandler wires the onboarding handlers. passwordGen may be nil (a
// crypto/rand generator is used).
func NewHandler(store Store, kc KeycloakClient, log *zap.Logger, passwordGen func() string) *Handler {
	if passwordGen == nil {
		passwordGen = generateTempPassword
	}
	return &Handler{store: store, kc: kc, log: log, password: passwordGen}
}

type intakeBody struct {
	Email       string          `json:"email"`
	DisplayName string          `json:"display_name"`
	Org         string          `json:"org"`
	Meta        json.RawMessage `json:"meta"`
}

func (b *intakeBody) validate() string {
	b.Email = strings.TrimSpace(b.Email)
	b.DisplayName = strings.TrimSpace(b.DisplayName)
	b.Org = strings.TrimSpace(b.Org)
	if !emailRe.MatchString(b.Email) {
		return "email is missing or malformed"
	}
	if b.DisplayName == "" || len(b.DisplayName) > 120 {
		return "display_name is required (max 120 chars)"
	}
	if len(b.Org) > 200 {
		return "org too long (max 200 chars)"
	}
	if len(b.Meta) > 0 && !json.Valid(b.Meta) {
		return "meta must be valid JSON"
	}
	return ""
}

func decodeJSON(w http.ResponseWriter, r *http.Request, v any) bool {
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20))
	if err := dec.Decode(v); err != nil {
		httpx.Error(w, http.StatusBadRequest, "invalid JSON body: "+err.Error())
		return false
	}
	return true
}

// CitizenSelfServe handles POST /v1/onboarding/citizen (public): validates the
// intake, provisions the Keycloak user immediately with the citizen role,
// sends the verify-email/update-password actions email and records the
// request as completed.
func (h *Handler) CitizenSelfServe(w http.ResponseWriter, r *http.Request) {
	var body intakeBody
	if !decodeJSON(w, r, &body) {
		return
	}
	if msg := body.validate(); msg != "" {
		httpx.Error(w, http.StatusBadRequest, msg)
		return
	}

	req := &Request{
		Persona:     PersonaCitizen,
		Email:       body.Email,
		DisplayName: body.DisplayName,
		Org:         body.Org,
		Status:      StatusPending,
		Meta:        body.Meta,
	}
	if err := h.store.Create(r.Context(), req); err != nil {
		h.log.Error("create citizen onboarding request", zap.Error(err))
		httpx.Error(w, http.StatusInternalServerError, "failed to create onboarding request")
		return
	}

	kcID, err := h.provision(r.Context(), PersonaCitizen, body.Email, body.DisplayName)
	if err != nil {
		h.log.Error("citizen self-serve provisioning failed", zap.String("email", body.Email), zap.Error(err))
		httpx.Error(w, http.StatusBadGateway, "identity provisioning failed: "+err.Error())
		return
	}
	final, err := h.store.Decide(r.Context(), req.ID, StatusCompleted, kcID, "self-service", "")
	if err != nil {
		h.log.Error("mark citizen request completed", zap.String("id", req.ID), zap.Error(err))
		httpx.Error(w, http.StatusInternalServerError, "failed to finalize onboarding request")
		return
	}
	httpx.JSON(w, http.StatusCreated, map[string]any{
		"request": final,
		"message": "account created; check your email to verify the address and set a password",
	})
}

// Intake handles POST /v1/onboarding/{key} (public) for the approval-gated
// personas (driver, operator, station-staff, advertiser, data-partner,
// gov-viewer). The request is stored with status=pending.
func (h *Handler) Intake(w http.ResponseWriter, r *http.Request) {
	persona := chi.URLParam(r, "key")
	if !IsIntakePersona(persona) {
		if persona == PersonaCitizen {
			httpx.Error(w, http.StatusBadRequest, "citizens self-serve via POST /v1/onboarding/citizen")
			return
		}
		httpx.Error(w, http.StatusNotFound, "unknown onboarding persona: "+persona)
		return
	}
	var body intakeBody
	if !decodeJSON(w, r, &body) {
		return
	}
	if msg := body.validate(); msg != "" {
		httpx.Error(w, http.StatusBadRequest, msg)
		return
	}
	req := &Request{
		Persona:     persona,
		Email:       body.Email,
		DisplayName: body.DisplayName,
		Org:         body.Org,
		Status:      StatusPending,
		Meta:        body.Meta,
	}
	if err := h.store.Create(r.Context(), req); err != nil {
		h.log.Error("create onboarding request", zap.String("persona", persona), zap.Error(err))
		httpx.Error(w, http.StatusInternalServerError, "failed to create onboarding request")
		return
	}
	httpx.JSON(w, http.StatusCreated, map[string]any{"request": req})
}

// List handles GET /v1/onboarding?status=&persona= (roles: platform-admin, operator).
func (h *Handler) List(w http.ResponseWriter, r *http.Request) {
	status := r.URL.Query().Get("status")
	if status != "" && status != StatusPending && status != StatusApproved &&
		status != StatusRejected && status != StatusCompleted {
		httpx.Error(w, http.StatusBadRequest, "invalid status filter (pending|approved|rejected|completed)")
		return
	}
	persona := r.URL.Query().Get("persona")
	if persona != "" {
		if _, ok := personaRoles[persona]; !ok {
			httpx.Error(w, http.StatusBadRequest, "invalid persona filter")
			return
		}
	}
	reqs, err := h.store.List(r.Context(), status, persona, 100)
	if err != nil {
		h.log.Error("list onboarding requests", zap.Error(err))
		httpx.Error(w, http.StatusInternalServerError, "failed to list onboarding requests")
		return
	}
	httpx.JSON(w, http.StatusOK, map[string]any{"requests": reqs})
}

// Get handles GET /v1/onboarding/{key} (roles: platform-admin, operator).
func (h *Handler) Get(w http.ResponseWriter, r *http.Request) {
	req, err := h.store.Get(r.Context(), chi.URLParam(r, "key"))
	if err != nil {
		if errors.Is(err, ErrNotFound) {
			httpx.Error(w, http.StatusNotFound, "onboarding request not found")
			return
		}
		h.log.Error("get onboarding request", zap.Error(err))
		httpx.Error(w, http.StatusInternalServerError, "failed to load onboarding request")
		return
	}
	httpx.JSON(w, http.StatusOK, map[string]any{"request": req})
}

// Approve handles POST /v1/onboarding/{key}/approve (roles: platform-admin,
// operator). It provisions the Keycloak user with the persona's mapped realm
// role and marks the request completed.
func (h *Handler) Approve(w http.ResponseWriter, r *http.Request) {
	h.decide(w, r, true)
}

// Reject handles POST /v1/onboarding/{key}/reject (roles: platform-admin,
// operator). Optional body: {"reason": "..."}.
func (h *Handler) Reject(w http.ResponseWriter, r *http.Request) {
	h.decide(w, r, false)
}

func (h *Handler) decide(w http.ResponseWriter, r *http.Request, approve bool) {
	id := chi.URLParam(r, "key")
	req, err := h.store.Get(r.Context(), id)
	if err != nil {
		if errors.Is(err, ErrNotFound) {
			httpx.Error(w, http.StatusNotFound, "onboarding request not found")
			return
		}
		h.log.Error("load onboarding request", zap.String("id", id), zap.Error(err))
		httpx.Error(w, http.StatusInternalServerError, "failed to load onboarding request")
		return
	}
	if req.Status != StatusPending {
		httpx.Error(w, http.StatusConflict, "request already decided (status="+req.Status+")")
		return
	}
	decidedBy := auth.Subject(r.Context())

	if !approve {
		var body struct {
			Reason string `json:"reason"`
		}
		if r.Body != nil && r.ContentLength != 0 {
			if !decodeJSON(w, r, &body) {
				return
			}
		}
		final, err := h.store.Decide(r.Context(), id, StatusRejected, "", decidedBy, strings.TrimSpace(body.Reason))
		if err != nil {
			h.log.Error("reject onboarding request", zap.String("id", id), zap.Error(err))
			httpx.Error(w, http.StatusInternalServerError, "failed to reject onboarding request")
			return
		}
		httpx.JSON(w, http.StatusOK, map[string]any{"request": final})
		return
	}

	kcID, err := h.provision(r.Context(), req.Persona, req.Email, req.DisplayName)
	if err != nil {
		h.log.Error("approval provisioning failed",
			zap.String("id", id), zap.String("persona", req.Persona), zap.Error(err))
		httpx.Error(w, http.StatusBadGateway, "identity provisioning failed: "+err.Error())
		return
	}
	final, err := h.store.Decide(r.Context(), id, StatusCompleted, kcID, decidedBy, "")
	if err != nil {
		h.log.Error("finalize approved request", zap.String("id", id), zap.Error(err))
		httpx.Error(w, http.StatusInternalServerError, "failed to finalize onboarding request")
		return
	}
	h.log.Info("onboarding request approved",
		zap.String("id", id), zap.String("persona", req.Persona),
		zap.String("realm_role", RealmRole(req.Persona)), zap.String("decided_by", decidedBy))
	httpx.JSON(w, http.StatusOK, map[string]any{"request": final})
}

// provision creates the Keycloak user, sets a temporary password, assigns the
// persona's realm role and sends the VERIFY_EMAIL + UPDATE_PASSWORD actions
// email. Returns the Keycloak user id.
func (h *Handler) provision(ctx context.Context, persona, email, displayName string) (string, error) {
	userID, err := h.kc.CreateUser(ctx, keycloak.CreateUserSpec{
		Username:    email,
		Email:       email,
		DisplayName: displayName,
	})
	if err != nil {
		return "", err
	}
	if err := h.kc.SetTemporaryPassword(ctx, userID, h.password()); err != nil {
		return "", err
	}
	if err := h.kc.AssignRealmRole(ctx, userID, RealmRole(persona)); err != nil {
		return "", err
	}
	if err := h.kc.SendActionsEmail(ctx, userID, welcomeActions); err != nil {
		return "", err
	}
	return userID, nil
}

// generateTempPassword returns a random temporary password (letters+digits,
// 16 chars) using crypto/rand.
func generateTempPassword() string {
	const alphabet = "abcdefghijkmnpqrstuvwxyzABCDEFGHJKLMNPQRSTUVWXYZ23456789"
	var b strings.Builder
	b.Grow(16)
	for i := 0; i < 16; i++ {
		n, err := rand.Int(rand.Reader, big.NewInt(int64(len(alphabet))))
		if err != nil {
			// rand.Reader failures are not realistically recoverable; fall
			// back to time-based selection so provisioning never deadlocks.
			b.WriteByte(alphabet[time.Now().UnixNano()%int64(len(alphabet))])
			continue
		}
		b.WriteByte(alphabet[n.Int64()])
	}
	return b.String()
}
