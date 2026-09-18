package onboarding

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"math/big"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"go.uber.org/zap"

	auth "github.com/munisp/hydrogenTransport/packages/go-auth"
	"github.com/munisp/hydrogenTransport/services/go/admin-api/internal/httpx"
	"github.com/munisp/hydrogenTransport/services/go/admin-api/internal/keycloak"
)

// Keycloak actions emailed to newly provisioned users.
var welcomeActions = []string{"VERIFY_EMAIL", "UPDATE_PASSWORD"}

var emailRe = regexp.MustCompile(`^[^@\s]+@[^@\s]+\.[^@\s]+$`)

// maxRequestsPerEmailPerDay is the Wave-8 per-email velocity cap (W8-2). It
// complements the per-IP APISIX limit: one botnet node rotation cannot file
// unlimited requests against the same victim address (email-bombing the
// actions email / queue spam).
const maxRequestsPerEmailPerDay = 5

// defaultPendingTTL is how long a pending request remains decidable before
// the TTL guard/sweep expires it (W8-7). Overridable via Handler.PendingTTL
// (ONBOARDING_PENDING_TTL_DAYS in main).
const defaultPendingTTL = 30 * 24 * time.Hour

// KeycloakClient is the subset of keycloak.AdminClient the onboarding
// handlers use (kept as a separate interface for easy mocking in tests).
type KeycloakClient interface {
	// CreateUser returns the user id and whether the email already belonged
	// to an existing account (Wave-9 W9-1: callers must never reset the
	// credentials of a pre-existing account).
	CreateUser(ctx context.Context, spec keycloak.CreateUserSpec) (id string, existed bool, err error)
	SetTemporaryPassword(ctx context.Context, userID, password string) error
	// EnsureRealmRole assigns the role only when missing (Wave-8 W8-4):
	// idempotent repair for users stranded by a mid-sequence failure.
	EnsureRealmRole(ctx context.Context, userID, role string) error
	SendActionsEmail(ctx context.Context, userID string, actions []string) error
}

// CaptchaConfig enables captcha verification on the public intake endpoints
// (Wave-8 W8-8). VerifyURL must implement the siteverify convention (form
// POST of secret+response → {"success": bool}); hCaptcha and Cloudflare
// Turnstile both do. Nil on the Handler = disabled (dev default).
type CaptchaConfig struct {
	VerifyURL string
	Secret    string
	HTTP      *http.Client // nil → default 5 s timeout client
}

// Handler serves the /v1/onboarding routes.
type Handler struct {
	store    Store
	kc       KeycloakClient
	log      *zap.Logger
	password func() string // temp-password generator (injectable for tests)

	// PendingTTL expires undecided requests (W8-7); default 30 days.
	PendingTTL time.Duration
	// Captcha, when non-nil, requires a valid captcha_token on the public
	// intake endpoints (W8-8); fail-closed when configured.
	Captcha *CaptchaConfig
}

// NewHandler wires the onboarding handlers. passwordGen may be nil (a
// crypto/rand generator is used).
func NewHandler(store Store, kc KeycloakClient, log *zap.Logger, passwordGen func() string) *Handler {
	if passwordGen == nil {
		passwordGen = generateTempPassword
	}
	return &Handler{store: store, kc: kc, log: log, password: passwordGen, PendingTTL: defaultPendingTTL}
}

type intakeBody struct {
	Email        string          `json:"email"`
	DisplayName  string          `json:"display_name"`
	Org          string          `json:"org"`
	Meta         json.RawMessage `json:"meta"`
	CaptchaToken string          `json:"captcha_token"`
}

func (b *intakeBody) validate() string {
	b.Email = strings.ToLower(strings.TrimSpace(b.Email)) // W9-2: case-insensitive identity
	b.DisplayName = strings.TrimSpace(b.DisplayName)
	b.Org = strings.TrimSpace(b.Org)
	if !emailRe.MatchString(b.Email) || len(b.Email) > 254 {
		return "email is missing or malformed"
	}
	if b.DisplayName == "" || len(b.DisplayName) > 120 {
		return "display_name is required (max 120 chars)"
	}
	if len(b.Org) > 200 {
		return "org too long (max 200 chars)"
	}
	if len(b.Meta) > 8192 {
		return "meta too large (max 8 KiB)"
	}
	if len(b.Meta) > 0 && !json.Valid(b.Meta) {
		return "meta must be valid JSON"
	}
	return ""
}

// validateForPersona applies the Wave-8 structured intake requirements
// (W8-3): every approval-gated persona names its organisation, and drivers
// must supply a licence number the approver can verify. Citizens stay
// org-optional (self-serve, no approval).
func (b *intakeBody) validateForPersona(persona string) string {
	if msg := b.validate(); msg != "" {
		return msg
	}
	if persona == PersonaCitizen {
		return ""
	}
	if b.Org == "" {
		return "org is required for " + persona + " onboarding"
	}
	if persona == PersonaDriver {
		var meta struct {
			LicenseNo string `json:"license_no"`
		}
		if len(b.Meta) == 0 || json.Unmarshal(b.Meta, &meta) != nil {
			return "driver onboarding requires meta.license_no"
		}
		if n := len(strings.TrimSpace(meta.LicenseNo)); n < 4 || n > 64 {
			return "meta.license_no is required (4–64 chars)"
		}
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

// verifyCaptcha checks the intake captcha token when a CaptchaConfig is set
// (W8-8). Verification is fail-closed: an unreachable verifier is a 502, a
// rejected token a 400. When no captcha is configured this is a no-op.
func (h *Handler) verifyCaptcha(w http.ResponseWriter, r *http.Request, token string) bool {
	if h.Captcha == nil {
		return true
	}
	if token == "" {
		httpx.Error(w, http.StatusBadRequest, "captcha_token is required")
		return false
	}
	client := h.Captcha.HTTP
	if client == nil {
		client = &http.Client{Timeout: 5 * time.Second}
	}
	resp, err := client.PostForm(h.Captcha.VerifyURL, url.Values{
		"secret":   {h.Captcha.Secret},
		"response": {token},
	})
	if err != nil {
		h.log.Error("captcha verify unreachable", zap.Error(err))
		httpx.Error(w, http.StatusBadGateway, "captcha verification unavailable")
		return false
	}
	defer resp.Body.Close()
	var out struct {
		Success bool `json:"success"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		h.log.Error("captcha verify undecodable", zap.Error(err))
		httpx.Error(w, http.StatusBadGateway, "captcha verification unavailable")
		return false
	}
	if !out.Success {
		httpx.Error(w, http.StatusBadRequest, "captcha verification failed")
		return false
	}
	return true
}

// overVelocity reports whether the email address filed too many requests in
// the last 24 h (W8-2).
func (h *Handler) overVelocity(w http.ResponseWriter, r *http.Request, email string) bool {
	n, err := h.store.CountRecent(r.Context(), email, time.Now().Add(-24*time.Hour))
	if err != nil {
		h.log.Error("onboarding velocity count", zap.Error(err))
		httpx.Error(w, http.StatusInternalServerError, "failed to create onboarding request")
		return true
	}
	if n >= maxRequestsPerEmailPerDay {
		httpx.Error(w, http.StatusTooManyRequests, "too many onboarding requests for this email address; try again later")
		return true
	}
	return false
}

// CitizenSelfServe handles POST /v1/onboarding/citizen (public): validates the
// intake, provisions the Keycloak user immediately with the citizen role,
// sends the verify-email/update-password actions email and records the
// request as completed.
//
// Wave-8 hardening: a retry after a failed provisioning adopts the orphaned
// pending row (W8-5) instead of stacking duplicates (W8-1), and the per-email
// velocity cap applies (W8-2).
func (h *Handler) CitizenSelfServe(w http.ResponseWriter, r *http.Request) {
	var body intakeBody
	if !decodeJSON(w, r, &body) {
		return
	}
	if msg := body.validate(); msg != "" {
		httpx.Error(w, http.StatusBadRequest, msg)
		return
	}
	if !h.verifyCaptcha(w, r, strings.TrimSpace(body.CaptchaToken)) {
		return
	}

	// W8-5: an earlier attempt that failed at the identity provider left a
	// pending row; adopt it and retry provisioning rather than duplicating.
	if existing, err := h.store.FindPending(r.Context(), PersonaCitizen, body.Email); err == nil {
		h.provisionCitizen(w, r, existing, true)
		return
	} else if !errors.Is(err, ErrNotFound) {
		h.log.Error("citizen dedup lookup", zap.Error(err))
		httpx.Error(w, http.StatusInternalServerError, "failed to create onboarding request")
		return
	}

	if h.overVelocity(w, r, body.Email) {
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
		// Raced duplicate (0011 partial unique index): replay the winner.
		if isUniqueViolation(err) {
			if existing, qerr := h.store.FindPending(r.Context(), PersonaCitizen, body.Email); qerr == nil {
				h.provisionCitizen(w, r, existing, true)
				return
			}
		}
		h.log.Error("create citizen onboarding request", zap.Error(err))
		httpx.Error(w, http.StatusInternalServerError, "failed to create onboarding request")
		return
	}
	h.provisionCitizen(w, r, req, false)
}

// provisionCitizen runs the provisioning half of citizen self-serve against
// a stored (fresh or adopted) request row.
func (h *Handler) provisionCitizen(w http.ResponseWriter, r *http.Request, req *Request, retried bool) {
	kcID, _, err := h.provision(r.Context(), PersonaCitizen, req.Email, req.DisplayName)
	if err != nil {
		// Never echo the Keycloak error to the client (SECURITY_AUDIT F4):
		// it would disclose whether the address is already registered and
		// leak internal details. The detail is logged; the row is marked so
		// a later retry (or an admin) can see what happened (W8-5).
		h.log.Error("citizen self-serve provisioning failed", zap.String("email", req.Email), zap.Error(err))
		if _, merr := h.store.MergeMeta(r.Context(), req.ID, map[string]any{
			"provision_error": "identity provisioning failed; safe to retry",
		}); merr != nil {
			h.log.Error("mark provision_error", zap.String("id", req.ID), zap.Error(merr))
		}
		httpx.Error(w, http.StatusBadGateway, "identity provisioning failed")
		return
	}
	final, err := h.store.Decide(r.Context(), req.ID, StatusCompleted, kcID, "self-service", "")
	if err != nil {
		h.log.Error("mark citizen request completed", zap.String("id", req.ID), zap.Error(err))
		httpx.Error(w, http.StatusInternalServerError, "failed to finalize onboarding request")
		return
	}
	status := http.StatusCreated
	if retried {
		status = http.StatusOK
	}
	httpx.JSON(w, status, map[string]any{
		"request": final,
		"message": "account created; check your email to verify the address and set a password",
	})
}

// Intake handles POST /v1/onboarding/{key} (public) for the approval-gated
// personas (driver, operator, station-staff, advertiser, data-partner,
// gov-viewer). The request is stored with status=pending.
//
// Wave-8: re-filing the same (persona, email) while pending replays the
// existing request with 200 (deduplicated:true) — the queue can never stack
// duplicates of one applicant (W8-1) — and the per-email velocity cap
// applies across personas (W8-2).
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
	if msg := body.validateForPersona(persona); msg != "" {
		httpx.Error(w, http.StatusBadRequest, msg)
		return
	}
	if !h.verifyCaptcha(w, r, strings.TrimSpace(body.CaptchaToken)) {
		return
	}

	if existing, err := h.store.FindPending(r.Context(), persona, body.Email); err == nil {
		httpx.JSON(w, http.StatusOK, map[string]any{"request": existing, "deduplicated": true})
		return
	} else if !errors.Is(err, ErrNotFound) {
		h.log.Error("intake dedup lookup", zap.String("persona", persona), zap.Error(err))
		httpx.Error(w, http.StatusInternalServerError, "failed to create onboarding request")
		return
	}

	if h.overVelocity(w, r, body.Email) {
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
		if isUniqueViolation(err) { // raced duplicate: replay the winner (W8-1)
			if existing, qerr := h.store.FindPending(r.Context(), persona, body.Email); qerr == nil {
				httpx.JSON(w, http.StatusOK, map[string]any{"request": existing, "deduplicated": true})
				return
			}
		}
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
		status != StatusRejected && status != StatusCompleted && status != StatusExpired {
		httpx.Error(w, http.StatusBadRequest, "invalid status filter (pending|approved|rejected|completed|expired)")
		return
	}
	persona := r.URL.Query().Get("persona")
	if persona != "" {
		if _, ok := personaRoles[persona]; !ok {
			httpx.Error(w, http.StatusBadRequest, "invalid persona filter")
			return
		}
	}
	reqs, err := h.store.List(r.Context(), status, persona, 100, 0)
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

// StatusPublic handles GET /v1/onboarding/status/{id} (public, Wave-8 W8-6).
// The applicant received the request id at intake; this endpoint lets them
// follow the decision (including a rejection) without an account. The uuid
// is the capability: the response carries status and timestamps only —
// never the email, name, org or meta, so it cannot be used to enumerate or
// confirm registrations.
func (h *Handler) StatusPublic(w http.ResponseWriter, r *http.Request) {
	req, err := h.store.Get(r.Context(), chi.URLParam(r, "id"))
	if err != nil {
		if errors.Is(err, ErrNotFound) {
			httpx.Error(w, http.StatusNotFound, "onboarding request not found")
			return
		}
		h.log.Error("public status lookup", zap.Error(err))
		httpx.Error(w, http.StatusInternalServerError, "failed to load onboarding request")
		return
	}
	httpx.JSON(w, http.StatusOK, map[string]any{
		"id":         req.ID,
		"persona":    req.Persona,
		"status":     req.Status,
		"created_at": req.CreatedAt,
		"decided_at": req.DecidedAt,
	})
}

// Approve handles POST /v1/onboarding/{key}/approve (role: platform-admin
// ONLY). It provisions the Keycloak user with the persona's mapped realm
// role and marks the request completed.
func (h *Handler) Approve(w http.ResponseWriter, r *http.Request) {
	h.decide(w, r, true)
}

// Reject handles POST /v1/onboarding/{key}/reject (role: platform-admin
// ONLY). Optional body: {"reason": "..."}.
func (h *Handler) Reject(w http.ResponseWriter, r *http.Request) {
	h.decide(w, r, false)
}

func (h *Handler) decide(w http.ResponseWriter, r *http.Request, approve bool) {
	// Defense in depth (SECURITY_AUDIT F3): approving an onboarding request
	// can provision a new operator account, so decisions are platform-admin
	// ONLY. Operators may list/view but never decide, regardless of how the
	// route was registered.
	if !auth.HasRole(r.Context(), "platform-admin") {
		httpx.Error(w, http.StatusForbidden, "onboarding decisions require the platform-admin role")
		return
	}
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
	// W8-7: a request that sat pending beyond the TTL is expired on the spot
	// (the sweep does this in bulk; this guard closes the decide path).
	if req.Status == StatusPending && h.PendingTTL > 0 && time.Since(req.CreatedAt) > h.PendingTTL {
		if _, err := h.store.Decide(r.Context(), id, StatusExpired, "", "system:ttl-expiry", ""); err != nil {
			h.log.Error("expire stale request", zap.String("id", id), zap.Error(err))
		}
		httpx.Error(w, http.StatusConflict, "request expired (pending longer than the onboarding TTL)")
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
		if len(body.Reason) > 500 {
			httpx.Error(w, http.StatusBadRequest, "reason too long (max 500 chars)")
			return
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

	kcID, existed, err := h.provision(r.Context(), req.Persona, req.Email, req.DisplayName)
	if err != nil {
		h.log.Error("approval provisioning failed",
			zap.String("id", id), zap.String("persona", req.Persona), zap.Error(err))
		// Never echo the Keycloak error to the client (SECURITY_AUDIT F4);
		// the detail is logged above.
		httpx.Error(w, http.StatusBadGateway, "identity provisioning failed")
		return
	}
	final, err := h.store.Decide(r.Context(), id, StatusCompleted, kcID, decidedBy, "")
	if err != nil {
		h.log.Error("finalize approved request", zap.String("id", id), zap.Error(err))
		httpx.Error(w, http.StatusInternalServerError, "failed to finalize onboarding request")
		return
	}
	// W9-1: when the email already belonged to a Keycloak account, the role
	// was added to that account (its credentials were NOT touched). Flag it:
	// a pre-existing account on an intake can be legitimate (same person,
	// new persona) or an impersonation attempt the admin should eyeball.
	if existed {
		h.log.Warn("approval attached role to a pre-existing Keycloak account",
			zap.String("id", id), zap.String("persona", req.Persona),
			zap.String("realm_role", RealmRole(req.Persona)), zap.String("decided_by", decidedBy))
	} else {
		h.log.Info("onboarding request approved",
			zap.String("id", id), zap.String("persona", req.Persona),
			zap.String("realm_role", RealmRole(req.Persona)), zap.String("decided_by", decidedBy))
	}
	httpx.JSON(w, http.StatusOK, map[string]any{"request": final, "existing_account": existed})
}

// Reconcile handles POST /v1/onboarding/reconcile (platform-admin, audited;
// Wave-8 W8-4). Provisioning is a non-transactional sequence of Keycloak
// calls, so a mid-sequence failure can strand a completed request whose user
// is missing its realm role. Reconcile re-asserts the persona role on every
// completed request's Keycloak user (EnsureRealmRole is idempotent — already
// correct users are untouched). Always 200 with a summary; failures are
// reported per id and logged, never silently swallowed. Pages through ALL
// completed rows (Wave-9 W9-4 — the Wave-8 version silently stopped at 500).
func (h *Handler) Reconcile(w http.ResponseWriter, r *http.Request) {
	if !auth.HasRole(r.Context(), "platform-admin") {
		httpx.Error(w, http.StatusForbidden, "onboarding reconcile requires the platform-admin role")
		return
	}
	checked := 0
	ensured := 0
	failedIDs := []string{}
	const page = 500
	for offset := 0; ; offset += page {
		reqs, err := h.store.List(r.Context(), StatusCompleted, "", page, offset)
		if err != nil {
			h.log.Error("reconcile list", zap.Error(err))
			httpx.Error(w, http.StatusInternalServerError, "failed to list completed requests")
			return
		}
		for _, req := range reqs {
			checked++
			if req.KeycloakSub == "" {
				continue
			}
			if err := h.kc.EnsureRealmRole(r.Context(), req.KeycloakSub, RealmRole(req.Persona)); err != nil {
				h.log.Error("reconcile ensure role failed",
					zap.String("id", req.ID), zap.String("kc_sub", req.KeycloakSub), zap.Error(err))
				failedIDs = append(failedIDs, req.ID)
				continue
			}
			ensured++
		}
		if len(reqs) < page {
			break
		}
	}
	h.log.Info("onboarding reconcile",
		zap.Int("checked", checked), zap.Int("ensured", ensured), zap.Int("failed", len(failedIDs)),
		zap.String("run_by", auth.Subject(r.Context())))
	httpx.JSON(w, http.StatusOK, map[string]any{
		"checked":    checked,
		"ensured":    ensured,
		"failed":     len(failedIDs),
		"failed_ids": failedIDs,
	})
}

// provision creates the Keycloak user, ensures the persona's realm role and
// sends the VERIFY_EMAIL + UPDATE_PASSWORD actions email. Returns the
// Keycloak user id and whether the email already belonged to an account.
//
// Wave-9 W9-1 (credential-reset protection): the temporary password is set
// ONLY for a brand-new user. When the email is already registered the
// account's credentials are never touched — the actions email goes to the
// address on file (proving ownership) and is the only recovery channel.
// Without this, the public citizen self-serve (and any approved intake)
// would let an unauthenticated party force a password reset on ANY
// registered account, including operators and platform-admins.
//
// The sequence is not transactional (Keycloak has no multi-call tx); each
// step is idempotent — EnsureRealmRole is read-then-assign — so a retry
// after a mid-sequence failure converges, and POST /v1/onboarding/reconcile
// repairs the rest (W8-4).
func (h *Handler) provision(ctx context.Context, persona, email, displayName string) (string, bool, error) {
	userID, existed, err := h.kc.CreateUser(ctx, keycloak.CreateUserSpec{
		Username:    email,
		Email:       email,
		DisplayName: displayName,
	})
	if err != nil {
		return "", false, err
	}
	if !existed {
		if err := h.kc.SetTemporaryPassword(ctx, userID, h.password()); err != nil {
			return "", false, err
		}
	}
	if err := h.kc.EnsureRealmRole(ctx, userID, RealmRole(persona)); err != nil {
		return "", false, err
	}
	if err := h.kc.SendActionsEmail(ctx, userID, welcomeActions); err != nil {
		return "", false, err
	}
	return userID, existed, nil
}

// isUniqueViolation reports a Postgres unique-constraint violation (23505) —
// for onboarding, the raced-duplicate signal from the 0011 pending index.
func isUniqueViolation(err error) bool {
	var pgErr *pgconn.PgError
	return errors.As(err, &pgErr) && pgErr.Code == "23505"
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
