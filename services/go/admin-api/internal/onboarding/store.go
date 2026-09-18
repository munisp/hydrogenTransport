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
	// StatusExpired marks a pending request that outlived the pending TTL
	// (Wave-8 W8-7); expired requests can no longer be decided.
	StatusExpired = "expired"
)

// personaRoles maps each onboarding persona to the Keycloak realm role it is
// provisioned with. The h2fleet realm defines platform-admin, operator,
// driver, citizen and — since Wave 8 — station-staff, so only the back-office
// read-only personas still map onto citizen:
//
//	citizen       -> citizen       (self-serve, provisioned immediately)
//	driver        -> driver
//	operator      -> operator
//	station-staff -> station-staff (Wave-8 W8-9: own role, no longer full operator)
//	advertiser    -> citizen       (read-only portal access)
//	data-partner  -> citizen       (read-only; open-data API keys via APISIX consumer)
//	gov-viewer    -> citizen       (read-only dashboard access)
var personaRoles = map[string]string{
	PersonaCitizen:      "citizen",
	PersonaDriver:       "driver",
	PersonaOperator:     "operator",
	PersonaStationStaff: "station-staff",
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
	List(ctx context.Context, status, persona string, limit, offset int) ([]Request, error)
	// Decide transitions a request to its final status, stamping decided_at
	// and decided_by. keycloakSub is recorded on completion; reason (when
	// non-empty) is merged into meta as reject_reason.
	Decide(ctx context.Context, id, status, keycloakSub, decidedBy, reason string) (*Request, error)
	// FindPending returns the single pending request for (persona, email)
	// — at most one can exist (0011 partial unique index). ErrNotFound when
	// none. Used for intake dedup and citizen orphan-row retry (Wave-8).
	FindPending(ctx context.Context, persona, email string) (*Request, error)
	// CountRecent counts requests filed by an email address (any persona,
	// any status) since the given time — the per-email velocity cap (W8-2).
	CountRecent(ctx context.Context, email string, since time.Time) (int, error)
	// ExpirePending flips pending requests created before the cutoff to
	// expired and returns the number flipped (W8-7 sweep).
	ExpirePending(ctx context.Context, before time.Time) (int64, error)
	// MergeMeta merges patch keys into the request's meta jsonb (W8-5:
	// recording provision_error on failed citizen self-serve).
	MergeMeta(ctx context.Context, id string, patch map[string]any) (*Request, error)
}

// PGStore is the Postgres-backed Store.
type PGStore struct {
	pool *pgxpool.Pool
}

// NewPGStore wraps a pgx pool.
func NewPGStore(pool *pgxpool.Pool) *PGStore { return &PGStore{pool: pool} }

// EnsureSchema idempotently creates the platform schema and the
// onboarding_requests table (SPEC: platform.onboarding_requests). Wave-8/9
// parity with migrations 0011/0012: status CHECK incl. 'expired',
// pending-dedup partial unique index and velocity-count email index — both
// on lower(email) so case variants cannot bypass them (W9-2).
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
                 CHECK (status IN ('pending','approved','rejected','completed','expired')),
    keycloak_sub text NOT NULL DEFAULT '',
    meta         jsonb NOT NULL DEFAULT '{}'::jsonb,
    created_at   timestamptz NOT NULL DEFAULT now(),
    decided_at   timestamptz,
    decided_by   text NOT NULL DEFAULT ''
);
CREATE INDEX IF NOT EXISTS onboarding_requests_status_idx  ON platform.onboarding_requests (status);
CREATE INDEX IF NOT EXISTS onboarding_requests_persona_idx ON platform.onboarding_requests (persona);
CREATE UNIQUE INDEX IF NOT EXISTS onboarding_requests_pending_uq
    ON platform.onboarding_requests (persona, lower(email)) WHERE status = 'pending';
CREATE INDEX IF NOT EXISTS onboarding_requests_email_created_idx
    ON platform.onboarding_requests (lower(email), created_at DESC);`)
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

func (s *PGStore) List(ctx context.Context, status, persona string, limit, offset int) ([]Request, error) {
	if limit <= 0 || limit > 500 {
		limit = 100
	}
	if offset < 0 {
		offset = 0
	}
	rows, err := s.pool.Query(ctx, `
SELECT `+selectCols+` FROM platform.onboarding_requests
WHERE ($1 = '' OR status = $1) AND ($2 = '' OR persona = $2)
ORDER BY created_at DESC, id
LIMIT $3 OFFSET $4`, status, persona, limit, offset)
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

// FindPending implements the Wave-8 intake dedup: the caller replays the
// existing pending request instead of inserting a duplicate. The partial
// unique index (0011/0012) guarantees at most one row matches. Matching is
// case-insensitive (W9-2): the handler lowercases at validate, this query
// defends against legacy mixed-case rows.
func (s *PGStore) FindPending(ctx context.Context, persona, email string) (*Request, error) {
	return scanRequest(s.pool.QueryRow(ctx, `
SELECT `+selectCols+` FROM platform.onboarding_requests
WHERE persona = $1 AND lower(email) = lower($2) AND status = 'pending'`, persona, email))
}

// CountRecent implements the Wave-8 per-email velocity cap (case-insensitive
// since Wave-9 W9-2 — case variants must not bypass the cap).
func (s *PGStore) CountRecent(ctx context.Context, email string, since time.Time) (int, error) {
	var n int
	err := s.pool.QueryRow(ctx, `
SELECT count(*) FROM platform.onboarding_requests
WHERE lower(email) = lower($1) AND created_at >= $2`, email, since).Scan(&n)
	return n, err
}

// ExpirePending implements the Wave-8 TTL sweep.
func (s *PGStore) ExpirePending(ctx context.Context, before time.Time) (int64, error) {
	tag, err := s.pool.Exec(ctx, `
UPDATE platform.onboarding_requests
SET status = 'expired', decided_at = now(), decided_by = 'system:ttl-expiry'
WHERE status = 'pending' AND created_at < $1`, before)
	if err != nil {
		return 0, err
	}
	return tag.RowsAffected(), nil
}

// MergeMeta implements the Wave-8 provision_error recording.
func (s *PGStore) MergeMeta(ctx context.Context, id string, patch map[string]any) (*Request, error) {
	payload, err := json.Marshal(patch)
	if err != nil {
		return nil, err
	}
	return scanRequest(s.pool.QueryRow(ctx, `
UPDATE platform.onboarding_requests SET meta = meta || $2::jsonb
WHERE id = $1
RETURNING `+selectCols, id, string(payload)))
}
