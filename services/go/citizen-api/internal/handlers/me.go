package handlers

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"os"
	"time"

	auth "github.com/munisp/hydrogenTransport/packages/go-auth"
)

// GDPR data-subject endpoints (Wave-6 A3-01/A3-02, docs/GDPR.md).
//
// The platform's identity model is already pseudonymous: riders appear in
// domain tables only as their Keycloak `sub` (a UUID, not a name or email).
// That is what makes both endpoints below tractable and honest:
//
//   - ACCESS (Art. 15): assemble every row keyed by the caller's sub.
//   - ERASURE (Art. 17): break the linkability. The sub is replaced by an
//     unkeyed-looking tombstone derived with a server-side salt, in every
//     domain table, in one transaction. The hash-chained audit log is NOT
//     rewritten (it is the WORM compliance record); its entries reference
//     only the pseudonymous sub, which no longer resolves to a person once
//     the Keycloak account is deleted (docs/GDPR.md §erasure).

// erasureMarker derives the tombstone that replaces a rider sub on erasure.
// The salt makes the marker unrecoverable-by-dictionary: without
// GDPR_ERASURE_SALT the sub cannot be reconstructed from the marker.
func erasureMarker(sub, salt string) string {
	sum := sha256.Sum256([]byte(salt + "|" + sub))
	return "erased:" + hex.EncodeToString(sum[:8])
}

// ExportMyData handles GET /v1/me/data-export (Keycloak JWT, self only):
// one JSON document with every domain row keyed by the caller's subject.
func (h *Handler) ExportMyData(w http.ResponseWriter, r *http.Request) {
	if !h.requireDB(w) {
		return
	}
	sub := auth.Subject(r.Context())
	if sub == "" {
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "authenticated subject required"})
		return
	}
	ctx := r.Context()
	export := map[string]any{
		"subject":      sub,
		"generated_at": time.Now().UTC().Format(time.RFC3339),
	}

	// Each section is a best-effort array; a section failure fails the whole
	// export (a partial export is a compliance liability).
	type section struct {
		name  string
		query string
	}
	sections := []section{
		{"drt_requests", `SELECT jsonb_agg(to_jsonb(t)) FROM (
			SELECT id, status, pickup_label, dropoff_label, passengers, requires_wheelchair,
			       vehicle_id, requested_at, assigned_at
			FROM citizen.drt_requests WHERE user_sub = $1 ORDER BY requested_at DESC) t`},
		{"fare_payments", `SELECT jsonb_agg(to_jsonb(t)) FROM (
			SELECT id, amount_minor, charged_minor, refunded_minor, currency, status, created_at, refunded_at
			FROM commerce.fare_payments WHERE rider_sub = $1 ORDER BY created_at DESC) t`},
		{"loyalty_account", `SELECT jsonb_agg(to_jsonb(t)) FROM (
			SELECT points, updated_at FROM commerce.loyalty_accounts WHERE rider_sub = $1) t`},
		{"loyalty_ledger", `SELECT jsonb_agg(to_jsonb(t)) FROM (
			SELECT delta, reason, created_at FROM commerce.loyalty_ledger WHERE rider_sub = $1
			ORDER BY created_at DESC) t`},
		{"loyalty_redemptions", `SELECT jsonb_agg(to_jsonb(t)) FROM (
			SELECT offer_id, points_spent, status, created_at FROM commerce.loyalty_redemptions
			WHERE rider_sub = $1 ORDER BY created_at DESC) t`},
		{"entitlements", `SELECT jsonb_agg(to_jsonb(t)) FROM (
			SELECT product_id, valid_from, valid_to, created_at FROM commerce.rider_entitlements
			WHERE rider_sub = $1 ORDER BY valid_to DESC) t`},
	}
	for _, s := range sections {
		var data []byte
		if err := h.db.QueryRow(ctx, s.query, sub).Scan(&data); err != nil {
			h.internal(w, "export section "+s.name, err)
			return
		}
		if len(data) == 0 {
			data = []byte(`null`)
		}
		var v any
		if err := json.Unmarshal(data, &v); err != nil {
			h.internal(w, "decode section "+s.name, err)
			return
		}
		if v == nil {
			v = []any{}
		}
		export[s.name] = v
	}
	writeJSON(w, http.StatusOK, export)
}

// EraseMyData handles POST /v1/me/erasure (Keycloak JWT, self only):
// tombstone-pseudonymizes every domain row owned by the caller in one
// transaction, then publishes gdpr.erasure.completed carrying only the
// tombstone marker (never the original sub). Fail-closed: without
// GDPR_ERASURE_SALT the endpoint refuses (weak anonymization is worse than
// none). The Keycloak account itself is deleted by the realm admin flow
// documented in docs/GDPR.md — this endpoint erases the platform data.
func (h *Handler) EraseMyData(w http.ResponseWriter, r *http.Request) {
	if !h.requireDB(w) {
		return
	}
	sub := auth.Subject(r.Context())
	if sub == "" {
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "authenticated subject required"})
		return
	}
	salt := os.Getenv("GDPR_ERASURE_SALT")
	if salt == "" {
		h.log.Error("GDPR_ERASURE_SALT is not configured; refusing erasure (fail-closed)")
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "erasure is not configured on this deployment"})
		return
	}
	marker := erasureMarker(sub, salt)

	tx, err := h.db.Begin(r.Context())
	if err != nil {
		h.internal(w, "begin erasure transaction", err)
		return
	}
	defer func() { _ = tx.Rollback(r.Context()) }()

	updates := []struct {
		name, sql string
	}{
		{"drt_requests", `UPDATE citizen.drt_requests SET user_sub = $2 WHERE user_sub = $1`},
		{"fare_payments", `UPDATE commerce.fare_payments SET rider_sub = $2 WHERE rider_sub = $1`},
		{"loyalty_accounts", `UPDATE commerce.loyalty_accounts SET rider_sub = $2 WHERE rider_sub = $1`},
		{"loyalty_ledger", `UPDATE commerce.loyalty_ledger SET rider_sub = $2 WHERE rider_sub = $1`},
		{"loyalty_redemptions", `UPDATE commerce.loyalty_redemptions SET rider_sub = $2 WHERE rider_sub = $1`},
		{"rider_accounts", `UPDATE commerce.rider_accounts SET rider_sub = $2 WHERE rider_sub = $1`},
		{"entitlements", `UPDATE commerce.rider_entitlements SET rider_sub = $2 WHERE rider_sub = $1`},
	}
	affected := map[string]int64{}
	for _, u := range updates {
		tag, err := tx.Exec(r.Context(), u.sql, sub, marker)
		if err != nil {
			h.internal(w, "erase "+u.name, err)
			return
		}
		affected[u.name] = tag.RowsAffected()
	}
	if err := tx.Commit(r.Context()); err != nil {
		h.internal(w, "commit erasure", err)
		return
	}

	// The event carries the tombstone only — the whole point is that the
	// original subject no longer appears in new data.
	if err := h.pub.Publish(r.Context(), "gdpr.erasure.completed", map[string]any{
		"marker":     marker,
		"erased_at":  time.Now().UTC().Format(time.RFC3339),
		"row_counts": affected,
	}); err != nil {
		h.log.Error("failed to publish gdpr.erasure.completed")
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"erased":     true,
		"marker":     marker,
		"row_counts": affected,
	})
}
