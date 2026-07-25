// Package onboarding implements the stakeholder-onboarding surface of
// admin-api: public persona intake, citizen self-serve provisioning, and
// operator/platform-admin approval that provisions Keycloak users.
//
// Storage: Postgres schema `platform` (created idempotently by EnsureSchema).
package onboarding

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Personas accepted by the onboarding intake (SPEC: stakeholder onboarding).
const (
	PersonaCitizen      = "citizen"
	PersonaDriver       = "driver"
	PersonaOperator     = "operator"
	PersonaStationStaff = "station-staff"
	PersonaAdvertiser   = "advertiser"
	PersonaDataPartner  = "data-partner"
	PersonaGovViewer    = "gov-viewer"
)

// Statuses of an onboarding request.
const (
	StatusPending   = "pending"
	StatusApproved  = "approved"
	StatusRejected  = "rejected"
	StatusCompleted = "completed"
)

// personaRoles maps each onboarding persona to the Keycloak realm role it is
// provisioned with. The h2fleet realm only defines platform-admin, operator,
// driver and citizen, so back-office read-only personas map onto citizen:
//
//	citizen       -> citizen   (self-serve, provisioned immediately)
//	driver        -> driver
//	operator      -> operator
//	station-staff -> operator  (station staff operate stations)
//	advertiser    -> citizen   (read-only portal access)
//	data-partner  -> citizen   (read-only; open-data API keys via APISIX consumer)
//	gov-viewer    -> citizen   (read-only dashboard access)
var personaRoles = map[string]string{
	PersonaCitizen:      "citizen",
	PersonaDriver:       "driver",
	PersonaOperator:     "operator",
	PersonaStationStaff: "operator",
	PersonaAdvertiser:   "citizen",
	PersonaDataPartner:  "citizen",
	PersonaGovViewer:    "citizen",
}

// RealmRole returns the Keycloak realm role provisioned for a persona.
func RealmRole(persona string) string { return personaRoles[persona] }

// IsIntakePersona reports whether persona is accepted by
// POST /v1/onboarding/{persona} (all except citizen, which is self-serve).
func IsIntakePersona(persona string) bool {
	_, ok := personaRoles[persona]
	return ok && persona != PersonaCitizen
}

// Request is one row of platform.onboarding_requests.
type Request struct {
	ID          string          `json:"id"`
	Persona     string          `json:"persona"`
	Email       string          `json:"email"`
	DisplayName string          `json:"display_name"`
	Org         string          `json:"org"`
	Status      string          `json:"status"` // pending|approved|rejected|completed
	KeycloakSub string          `json:"keycloak_sub"`
	Meta        json.RawMessage `json:"meta"`
	CreatedAt   time.Time       `json:"created_at"`
	DecidedAt   *time.Time      `json:"decided_at"`
	DecidedBy   string          `json:"decided_by"`
}

// ErrNotFound is returned when no onboarding request matches an id.
var ErrNotFound = errors.New("onboarding request not found")

// Store abstracts persistence so handlers are testable without Postgres.
type Store interface {
	EnsureSchema(ctx context.Context) error
	Ping(ctx context.Context) error
	Create(ctx context.Context, req *Request) error
	Get(ctx context.Context, id string) (*Request, error)
	List(ctx context.Context, status, persona string, limit int) ([]Request, error)
	// Decide transitions a request to its final status, stamping decided_at
	// and decided_by. keycloakSub is recorded on completion; reason (when
	// non-empty) is merged into meta as reject_reason.
	Decide(ctx context.Context, id, status, keycloakSub, decidedBy, reason string) (*Request, error)
}

// PGStore is the Postgres-backed Store.
type PGStore struct {
	pool *pgxpool.Pool
}

// NewPGStore wraps a pgx pool.
func NewPGStore(pool *pgxpool.Pool) *PGStore { return &PGStore{pool: pool} }

// EnsureSchema idempotently creates the platform schema and the
// onboarding_requests table (SPEC: platform.onboarding_requests).
func (s *PGStore) EnsureSchema(ctx context.Context) error {
	_, err := s.pool.Exec(ctx, `
CREATE SCHEMA IF NOT EXISTS platform;
CREATE TABLE IF NOT EXISTS platform.onboarding_requests (
    id           uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    persona      text NOT NULL,
    email        text NOT NULL,
    display_name text NOT NULL,
    org          text NOT NULL DEFAULT '',
    status       text NOT NULL DEFAULT 'pending'
                 CHECK (status IN ('pending','approved','rejected','completed')),
    keycloak_sub text NOT NULL DEFAULT '',
    meta         jsonb NOT NULL DEFAULT '{}'::jsonb,
    created_at   timestamptz NOT NULL DEFAULT now(),
    decided_at   timestamptz,
    decided_by   text NOT NULL DEFAULT ''
);
CREATE INDEX IF NOT EXISTS onboarding_requests_status_idx  ON platform.onboarding_requests (status);
CREATE INDEX IF NOT EXISTS onboarding_requests_persona_idx ON platform.onboarding_requests (persona);`)
	return err
}

// Ping verifies database connectivity (used by /healthz).
func (s *PGStore) Ping(ctx context.Context) error { return s.pool.Ping(ctx) }

func (s *PGStore) Create(ctx context.Context, req *Request) error {
	meta := req.Meta
	if len(meta) == 0 {
		meta = json.RawMessage(`{}`)
	}
	return s.pool.QueryRow(ctx, `
INSERT INTO platform.onboarding_requests (persona, email, display_name, org, status, meta)
VALUES ($1, $2, $3, $4, $5, $6)
RETURNING id, created_at`,
		req.Persona, req.Email, req.DisplayName, req.Org, req.Status, meta,
	).Scan(&req.ID, &req.CreatedAt)
}

const selectCols = `id, persona, email, display_name, org, status, keycloak_sub, meta, created_at, decided_at, decided_by`

func scanRequest(row pgx.Row) (*Request, error) {
	var r Request
	err := row.Scan(&r.ID, &r.Persona, &r.Email, &r.DisplayName, &r.Org, &r.Status,
		&r.KeycloakSub, &r.Meta, &r.CreatedAt, &r.DecidedAt, &r.DecidedBy)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, ErrNotFound
		}
		return nil, err
	}
	return &r, nil
}

func (s *PGStore) Get(ctx context.Context, id string) (*Request, error) {
	return scanRequest(s.pool.QueryRow(ctx,
		`SELECT `+selectCols+` FROM platform.onboarding_requests WHERE id = $1`, id))
}

func (s *PGStore) List(ctx context.Context, status, persona string, limit int) ([]Request, error) {
	if limit <= 0 || limit > 500 {
		limit = 100
	}
	rows, err := s.pool.Query(ctx, `
SELECT `+selectCols+` FROM platform.onboarding_requests
WHERE ($1 = '' OR status = $1) AND ($2 = '' OR persona = $2)
ORDER BY created_at DESC
LIMIT $3`, status, persona, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make([]Request, 0)
	for rows.Next() {
		r, err := scanRequest(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *r)
	}
	return out, rows.Err()
}

func (s *PGStore) Decide(ctx context.Context, id, status, keycloakSub, decidedBy, reason string) (*Request, error) {
	return scanRequest(s.pool.QueryRow(ctx, `
UPDATE platform.onboarding_requests
SET status = $2,
    keycloak_sub = CASE WHEN $3 = '' THEN keycloak_sub ELSE $3 END,
    decided_at = now(),
    decided_by = $4,
    meta = CASE WHEN $5 = '' THEN meta ELSE meta || jsonb_build_object('reject_reason', $5) END
WHERE id = $1
RETURNING `+selectCols, id, status, keycloakSub, decidedBy, reason))
}
