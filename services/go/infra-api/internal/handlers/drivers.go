package handlers

import (
	"encoding/json"
	"errors"
	"net/http"
	"strings"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5"
	"go.uber.org/zap"

	auth "github.com/munisp/hydrogenTransport/packages/go-auth"
)

// drivers.go — Wave-9 W9-3: closes the last mile of driver onboarding.
//
// Approving a driver intake (admin-api) provisions the Keycloak account and
// the `driver` realm role, but dispatch jobs reference infra.drivers(sub) via
// a foreign key: without a drivers row the new driver cannot be assigned or
// accept any job (422). Before Wave-9 the only writers into infra.drivers
// were charter placeholder rows — a real driver had no path at all.
//
// Two paths create the row, both idempotent on sub:
//   - POST /v1/drivers/register — self-service for the authenticated driver
//     (sub taken from the JWT, never from the body);
//   - POST /v1/drivers — operator-managed registration.

type driverBody struct {
	Name      string `json:"name"`
	LicenseNo string `json:"license_no"`
}

func (b *driverBody) validate() string {
	b.Name = strings.TrimSpace(b.Name)
	b.LicenseNo = strings.TrimSpace(b.LicenseNo)
	if b.Name == "" || len(b.Name) > 120 {
		return "name is required (max 120 chars)"
	}
	if n := len(b.LicenseNo); n < 4 || n > 64 {
		return "license_no is required (4–64 chars)"
	}
	return ""
}

// upsertDriver inserts (or refreshes the name/licence of) a drivers row.
// Operator-managed corrections only — self-service registration is
// insert-only (W10-3). Returns true when the row was newly inserted.
func (h *Handler) upsertDriver(w http.ResponseWriter, r *http.Request, sub, name, licenseNo string) (bool, bool) {
	var inserted bool
	err := h.db.QueryRow(r.Context(), `
		INSERT INTO infra.drivers (sub, name, license_no, status)
		VALUES ($1, $2, $3, 'active')
		ON CONFLICT (sub) DO UPDATE
		  SET name = EXCLUDED.name, license_no = EXCLUDED.license_no
		RETURNING (xmax = 0)`, sub, name, licenseNo).Scan(&inserted)
	if err != nil {
		h.internal(w, "upsert driver", err)
		return false, false
	}
	return inserted, true
}

// RegisterDriver handles POST /v1/drivers/register (role: driver). The
// driver's own sub comes from the validated JWT — a driver can only ever
// register themselves.
//
// Wave-10 W10-3: registration is INSERT-ONLY. The licence number was
// verified by a platform-admin at intake approval; letting the driver
// rewrite it (or their display name) afterwards would silently replace the
// verified identity. Corrections go through POST /v1/drivers (operator).
func (h *Handler) RegisterDriver(w http.ResponseWriter, r *http.Request) {
	sub := auth.Subject(r.Context())
	if sub == "" {
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "unauthorized"})
		return
	}
	var body driverBody
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&body); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid JSON body: " + err.Error()})
		return
	}
	if msg := body.validate(); msg != "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": msg})
		return
	}
	var inserted string
	err := h.db.QueryRow(r.Context(), `
		INSERT INTO infra.drivers (sub, name, license_no, status)
		VALUES ($1, $2, $3, 'active')
		ON CONFLICT (sub) DO NOTHING
		RETURNING sub`, sub, body.Name, body.LicenseNo).Scan(&inserted)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			// Already registered: idempotent 200, the stored (verified)
			// record is left untouched.
			writeJSON(w, http.StatusOK, map[string]any{
				"sub": sub, "registered": true, "already_registered": true,
				"message": "already registered; profile corrections go through an operator (POST /v1/drivers)",
			})
			return
		}
		h.internal(w, "register driver", err)
		return
	}
	h.log.Info("driver self-registered", zap.String("sub", sub))
	writeJSON(w, http.StatusCreated, map[string]any{"sub": sub, "name": body.Name, "registered": true})
}

// CreateDriver handles POST /v1/drivers (role: operator) — operator-managed
// registration for drivers who cannot self-register (e.g. kiosk onboarding).
// The sub must be the driver's Keycloak subject (from the approved onboarding
// request / user directory).
func (h *Handler) CreateDriver(w http.ResponseWriter, r *http.Request) {
	var body struct {
		driverBody
		Sub string `json:"sub"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&body); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid JSON body: " + err.Error()})
		return
	}
	body.Sub = strings.TrimSpace(body.Sub)
	if body.Sub == "" || len(body.Sub) > 128 {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "sub (Keycloak subject) is required (max 128 chars)"})
		return
	}
	if msg := body.driverBody.validate(); msg != "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": msg})
		return
	}
	inserted, ok := h.upsertDriver(w, r, body.Sub, body.Name, body.LicenseNo)
	if !ok {
		return
	}
	status := http.StatusOK
	if inserted {
		status = http.StatusCreated
	}
	h.log.Info("driver registered by operator",
		zap.String("sub", body.Sub), zap.String("operator", auth.Subject(r.Context())))
	writeJSON(w, status, map[string]any{"sub": body.Sub, "name": body.Name, "registered": true})
}

// SetDriverStatus handles POST /v1/drivers/{sub}/status (role: operator) —
// the driver offboarding/lifecycle lever (Wave-10 W10-2): `suspended`
// immediately blocks job acceptance, `active` re-enables, `off-duty` marks
// the driver unavailable for new assignments.
func (h *Handler) SetDriverStatus(w http.ResponseWriter, r *http.Request) {
	sub := chi.URLParam(r, "sub")
	var body struct {
		Status string `json:"status"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&body); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid JSON body: " + err.Error()})
		return
	}
	switch body.Status {
	case "active", "off-duty", "suspended":
	default:
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "status must be active|off-duty|suspended"})
		return
	}
	tag, err := h.db.Exec(r.Context(), `
		UPDATE infra.drivers SET status = $2 WHERE sub = $1`, sub, body.Status)
	if err != nil {
		h.internal(w, "set driver status", err)
		return
	}
	if tag.RowsAffected() == 0 {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "driver not found"})
		return
	}
	h.log.Info("driver status changed",
		zap.String("sub", sub), zap.String("status", body.Status),
		zap.String("operator", auth.Subject(r.Context())))
	writeJSON(w, http.StatusOK, map[string]any{"sub": sub, "status": body.Status})
}
