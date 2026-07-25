// Package store implements the append-only, hash-chained audit trail in
// Postgres (platform.audit_log) plus the chain-integrity verifier.
//
// Tamper evidence: every entry carries `prev_hash` (the SHA-256 of the
// previous row, "" for the genesis row) and `hash` = SHA-256 over the
// canonical encoding of prev_hash + all payload fields. Any retroactive
// edit, deletion or reordering breaks the chain and is surfaced by Verify.
//
// Appends are serialized with a Postgres transaction-level advisory lock so
// concurrent writers cannot fork the chain. The table is append-only by
// convention AND by database role hardening (see README): the service only
// ever INSERTs/SELECTs; migrations grant no UPDATE/DELETE to the app role.
package store

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// advisoryLockKey serializes hash-chain appends (arbitrary constant).
const advisoryLockKey int64 = 726374686169 // arbitrary constant

// Entry is one audit record. Before/After hold the canonical jsonb text
// (as normalized by Postgres) so the hash is stable across round-trips.
type Entry struct {
	ID         int64            `json:"id"`
	ActorSub   string           `json:"actor_sub"`
	ActorRoles []string         `json:"actor_roles"`
	Action     string           `json:"action"`
	Entity     string           `json:"entity"`
	EntityID   string           `json:"entity_id,omitempty"`
	Before     *json.RawMessage `json:"before,omitempty"`
	After      *json.RawMessage `json:"after,omitempty"`
	IP         string           `json:"ip,omitempty"`
	UA         string           `json:"ua,omitempty"`
	TS         time.Time        `json:"ts"`
	PrevHash   string           `json:"prev_hash"`
	Hash       string           `json:"hash"`
}

// ChainHash computes the SHA-256 chain hash for an entry with the given
// prevHash. The encoding is length-prefixed so field boundaries are
// unambiguous (no separator-injection).
func ChainHash(prevHash string, e Entry) string {
	h := sha256.New()
	writeField := func(s string) {
		fmt.Fprintf(h, "%d:", len(s))
		h.Write([]byte(s))
	}
	writeField(prevHash)
	writeField(e.ActorSub)
	writeField(strings.Join(e.ActorRoles, ","))
	writeField(e.Action)
	writeField(e.Entity)
	writeField(e.EntityID)
	writeField(rawText(e.Before))
	writeField(rawText(e.After))
	writeField(e.IP)
	writeField(e.UA)
	writeField(e.TS.UTC().Format(time.RFC3339Nano))
	return hex.EncodeToString(h.Sum(nil))
}

func rawText(r *json.RawMessage) string {
	if r == nil {
		return ""
	}
	return string(*r)
}

// ListFilter narrows GET /v1/audit results.
type ListFilter struct {
	Actor  string
	Entity string
	From   time.Time
	Limit  int
}

// Store is the persistence contract (PGStore in production, fakes in tests).
type Store interface {
	EnsureSchema(ctx context.Context) error
	Ping(ctx context.Context) error
	// Append assigns ID, TS (when zero), PrevHash and Hash.
	Append(ctx context.Context, e *Entry) error
	List(ctx context.Context, f ListFilter) ([]Entry, error)
	// Verify walks the whole chain; returns the first offending row id (0 =
	// none) and the number of rows checked.
	Verify(ctx context.Context) (badID int64, checked int, err error)
}

// PGStore is the Postgres-backed Store.
type PGStore struct {
	db *pgxpool.Pool
}

// NewPGStore builds a PGStore.
func NewPGStore(db *pgxpool.Pool) *PGStore { return &PGStore{db: db} }

// EnsureSchema idempotently creates the platform schema and the audit_log
// table. Append-only hardening (revoking UPDATE/DELETE) is left to DBA role
// management; documented in README.
func (s *PGStore) EnsureSchema(ctx context.Context) error {
	_, err := s.db.Exec(ctx, `
CREATE SCHEMA IF NOT EXISTS platform;
CREATE TABLE IF NOT EXISTS platform.audit_log (
    id          bigserial PRIMARY KEY,
    actor_sub   text        NOT NULL,
    actor_roles jsonb       NOT NULL DEFAULT '[]'::jsonb,
    action      text        NOT NULL,
    entity      text        NOT NULL,
    entity_id   text        NOT NULL DEFAULT '',
    before      jsonb,
    after       jsonb,
    ip          text        NOT NULL DEFAULT '',
    ua          text        NOT NULL DEFAULT '',
    ts          timestamptz NOT NULL DEFAULT now(),
    prev_hash   text        NOT NULL DEFAULT '',
    hash        text        NOT NULL UNIQUE
);
CREATE INDEX IF NOT EXISTS audit_log_actor_idx  ON platform.audit_log (actor_sub, ts DESC);
CREATE INDEX IF NOT EXISTS audit_log_entity_idx ON platform.audit_log (entity, entity_id, ts DESC);
CREATE INDEX IF NOT EXISTS audit_log_ts_idx     ON platform.audit_log (ts DESC);`)
	return err
}

// Ping checks database reachability.
func (s *PGStore) Ping(ctx context.Context) error { return s.db.Ping(ctx) }

// Append inserts the entry at the head of the hash chain. Concurrent appends
// are serialized by a transaction-level advisory lock; the entry is mutated
// in place with its assigned id/ts/hashes.
func (s *PGStore) Append(ctx context.Context, e *Entry) error {
	if e.TS.IsZero() {
		e.TS = time.Now().UTC()
	}
	tx, err := s.db.Begin(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }()

	if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock($1)`, advisoryLockKey); err != nil {
		return fmt.Errorf("chain lock: %w", err)
	}

	var prevHash string
	err = tx.QueryRow(ctx,
		`SELECT hash FROM platform.audit_log ORDER BY id DESC LIMIT 1`).Scan(&prevHash)
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return fmt.Errorf("read chain head: %w", err)
	}

	// Normalize before/after through Postgres jsonb so the hash is computed
	// over the exact text the verifier will read back (jsonb::text is the
	// canonical form: sorted keys, single spaces).
	var beforeNorm, afterNorm *string
	if e.Before != nil {
		var s string
		if err := tx.QueryRow(ctx, `SELECT $1::jsonb::text`, string(*e.Before)).Scan(&s); err != nil {
			return fmt.Errorf("invalid before JSON: %w", err)
		}
		beforeNorm = &s
		rm := json.RawMessage(s)
		e.Before = &rm
	}
	if e.After != nil {
		var s string
		if err := tx.QueryRow(ctx, `SELECT $1::jsonb::text`, string(*e.After)).Scan(&s); err != nil {
			return fmt.Errorf("invalid after JSON: %w", err)
		}
		afterNorm = &s
		rm := json.RawMessage(s)
		e.After = &rm
	}

	e.PrevHash = prevHash
	e.Hash = ChainHash(prevHash, *e)

	roles, err := json.Marshal(e.ActorRoles)
	if err != nil {
		return err
	}
	err = tx.QueryRow(ctx, `
		INSERT INTO platform.audit_log
		    (actor_sub, actor_roles, action, entity, entity_id, before, after, ip, ua, ts, prev_hash, hash)
		VALUES ($1, $2::jsonb, $3, $4, $5, $6::jsonb, $7::jsonb, $8, $9, $10, $11, $12)
		RETURNING id`,
		e.ActorSub, string(roles), e.Action, e.Entity, e.EntityID,
		nullableStr(beforeNorm), nullableStr(afterNorm), e.IP, e.UA, e.TS, e.PrevHash, e.Hash,
	).Scan(&e.ID)
	if err != nil {
		return fmt.Errorf("insert audit entry: %w", err)
	}
	return tx.Commit(ctx)
}

func nullableStr(s *string) any {
	if s == nil {
		return nil
	}
	return *s
}

const selectCols = `id, actor_sub, actor_roles::text, action, entity, entity_id,
       before::text, after::text, ip, ua, ts, prev_hash, hash`

// scanEntry scans one selectCols row (before/after as nullable text).
func scanEntry(row pgx.Row, e *Entry) error {
	var roles string
	var before, after *string
	err := row.Scan(&e.ID, &e.ActorSub, &roles, &e.Action, &e.Entity, &e.EntityID,
		&before, &after, &e.IP, &e.UA, &e.TS, &e.PrevHash, &e.Hash)
	if err != nil {
		return err
	}
	if err := json.Unmarshal([]byte(roles), &e.ActorRoles); err != nil {
		e.ActorRoles = nil
	}
	if before != nil {
		rm := json.RawMessage(*before)
		e.Before = &rm
	}
	if after != nil {
		rm := json.RawMessage(*after)
		e.After = &rm
	}
	return nil
}

// List returns newest-first entries matching the filter.
func (s *PGStore) List(ctx context.Context, f ListFilter) ([]Entry, error) {
	if f.Limit <= 0 || f.Limit > 1000 {
		f.Limit = 100
	}
	query := `SELECT ` + selectCols + ` FROM platform.audit_log WHERE TRUE`
	args := []any{}
	n := 0
	if f.Actor != "" {
		n++
		query += fmt.Sprintf(` AND actor_sub = $%d`, n)
		args = append(args, f.Actor)
	}
	if f.Entity != "" {
		n++
		query += fmt.Sprintf(` AND entity = $%d`, n)
		args = append(args, f.Entity)
	}
	if !f.From.IsZero() {
		n++
		query += fmt.Sprintf(` AND ts >= $%d`, n)
		args = append(args, f.From)
	}
	n++
	query += fmt.Sprintf(` ORDER BY id DESC LIMIT $%d`, n)
	args = append(args, f.Limit)

	rows, err := s.db.Query(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Entry{}
	for rows.Next() {
		var e Entry
		if err := scanEntry(rows, &e); err != nil {
			return nil, err
		}
		out = append(out, e)
	}
	return out, rows.Err()
}

// Verify recomputes every hash in id order and checks prev-linkage.
func (s *PGStore) Verify(ctx context.Context) (int64, int, error) {
	rows, err := s.db.Query(ctx,
		`SELECT `+selectCols+` FROM platform.audit_log ORDER BY id ASC`)
	if err != nil {
		return 0, 0, err
	}
	defer rows.Close()
	prevHash := ""
	checked := 0
	for rows.Next() {
		var e Entry
		if err := scanEntry(rows, &e); err != nil {
			return 0, checked, err
		}
		checked++
		if e.PrevHash != prevHash {
			return e.ID, checked, nil
		}
		if ChainHash(prevHash, e) != e.Hash {
			return e.ID, checked, nil
		}
		prevHash = e.Hash
	}
	return 0, checked, rows.Err()
}
