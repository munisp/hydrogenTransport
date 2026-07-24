package handlers

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"go.uber.org/zap"

	auth "github.com/munisp/hydrogenTransport/packages/go-auth"
	"github.com/munisp/hydrogenTransport/services/go/commerce-api/internal/ledger"
)

// Payment mirrors commerce.fare_payments (fare-payments module).
type Payment struct {
	ID                 string    `json:"id"`
	RiderSub           string    `json:"rider_sub"`
	AmountMinor        int64     `json:"amount_minor"`
	Currency           string    `json:"currency"`
	MojaloopTransferID *string   `json:"mojaloop_transfer_id,omitempty"`
	TBTransferID       *string   `json:"tb_transfer_id,omitempty"`
	IdempotencyKey     *string   `json:"idempotency_key,omitempty"`
	Status             string    `json:"status"`
	CreatedAt          time.Time `json:"created_at"`
}

const paymentCols = `id, rider_sub, amount_minor, currency, mojaloop_transfer_id,
	tb_transfer_id, idempotency_key, status, created_at`

func scanPayment(row pgx.Row) (Payment, error) {
	var p Payment
	err := row.Scan(&p.ID, &p.RiderSub, &p.AmountMinor, &p.Currency,
		&p.MojaloopTransferID, &p.TBTransferID, &p.IdempotencyKey, &p.Status, &p.CreatedAt)
	return p, err
}

type createPaymentRequest struct {
	AmountMinor int64  `json:"amount_minor"`
	Currency    string `json:"currency"`
	RiderSub    string `json:"rider_sub"` // defaults to the JWT subject
	UseMojaloop bool   `json:"use_mojaloop"`
}

// CreatePayment handles POST /v1/payments (Keycloak JWT, Idempotency-Key
// header required). Flow: insert fare_payments row (idempotent on the key) →
// TigerBeetle transfer rider wallet → operator revenue → publish
// fare.payment.initiated / fare.payment.settled (SPEC §3.3). Optionally runs a
// Mojaloop transfer (real HTTP POST when MOJALOOP_ENDPOINT is set, simulated
// otherwise, SPEC §4).
func (h *Handler) CreatePayment(mojaloopEndpoint string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		idemKey := r.Header.Get("Idempotency-Key")
		if idemKey == "" {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "Idempotency-Key header is required"})
			return
		}
		var req createPaymentRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid JSON body"})
			return
		}
		if req.AmountMinor <= 0 {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "amount_minor must be positive"})
			return
		}
		if req.Currency == "" {
			req.Currency = "EUR"
		}
		if req.RiderSub == "" {
			req.RiderSub = auth.Subject(r.Context())
		}
		if req.RiderSub == "" {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "rider_sub could not be determined"})
			return
		}

		paymentID := uuid.NewString()
		_, err := h.db.Exec(r.Context(), `
			INSERT INTO commerce.fare_payments (id, rider_sub, amount_minor, currency, status, idempotency_key)
			VALUES ($1, $2, $3, $4, 'initiated', $5)`,
			paymentID, req.RiderSub, req.AmountMinor, req.Currency, idemKey)
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) && pgErr.Code == "23505" { // unique_violation on idempotency_key
			existing, qerr := scanPayment(h.db.QueryRow(r.Context(),
				`SELECT `+paymentCols+` FROM commerce.fare_payments WHERE idempotency_key = $1`, idemKey))
			if qerr != nil {
				h.internal(w, "load idempotent payment", qerr)
				return
			}
			if existing.RiderSub != auth.Subject(r.Context()) &&
				!auth.HasAnyRole(r.Context(), "operator", "platform-admin") {
				// Idempotency keys are scoped to their owner; replaying
				// someone else's key must not leak their payment.
				writeJSON(w, http.StatusNotFound, map[string]string{"error": "payment not found"})
				return
			}
			writeJSON(w, http.StatusOK, existing) // idempotent replay
			return
		}
		if err != nil {
			h.internal(w, "insert payment", err)
			return
		}

		event := map[string]any{
			"payment_id":   paymentID,
			"rider_sub":    req.RiderSub,
			"amount_minor": req.AmountMinor,
			"currency":     req.Currency,
		}
		if err := h.pub.Publish(r.Context(), "fare.payment.initiated", event); err != nil {
			h.log.Error("failed to publish fare.payment.initiated", zap.Error(err))
		}

		// Ledger transfer: rider wallet (1xxx, persisted per-rider mapping) →
		// operator revenue (2xxx). The TigerBeetle transfer ID is derived
		// deterministically from the Idempotency-Key so client retries of the
		// same request can never double-post the transfer.
		account, err := h.riderAccount(r.Context(), req.RiderSub)
		if err != nil {
			h.internal(w, "ensure rider ledger account", err)
			return
		}
		status := "settled"
		var tbID *string
		transferID, err := h.ledger.Transfer(
			ledger.DeterministicTransferID(idemKey), account, ledger.OperatorRevenueAccount,
			uint64(req.AmountMinor), ledger.CodeFare)
		if err != nil {
			h.log.Error("ledger transfer failed", zap.String("payment", paymentID), zap.Error(err))
			status = "failed"
		} else {
			tbID = &transferID
		}

		// Optional Mojaloop leg. A Mojaloop failure never fabricates a
		// transfer id — the payment is marked mojaloop_failed instead.
		var mlID *string
		var mlErr error
		if req.UseMojaloop && status == "settled" {
			id, err := h.mojaloopTransfer(r, mojaloopEndpoint, paymentID, req)
			if err != nil {
				h.log.Error("mojaloop transfer failed", zap.String("payment", paymentID), zap.Error(err))
				status = "mojaloop_failed"
				mlErr = err
			} else {
				mlID = &id
			}
		}

		// Persist the final status FIRST; domain events are published only
		// after the DB update commits (outbox-lite ordering) so consumers
		// never observe an event for a state that was never recorded.
		p, err := scanPayment(h.db.QueryRow(r.Context(), `
			UPDATE commerce.fare_payments
			SET status = $2, tb_transfer_id = $3, mojaloop_transfer_id = $4
			WHERE id = $1 RETURNING `+paymentCols, paymentID, status, tbID, mlID))
		if err != nil {
			h.internal(w, "finalize payment", err)
			return
		}

		if status != "settled" {
			reason := "ledger transfer failed"
			if status == "mojaloop_failed" {
				reason = "mojaloop transfer failed: " + mlErr.Error()
			}
			event["reason"] = reason
			event["failed_at"] = time.Now().UTC().Format(time.RFC3339)
			if err := h.pub.Publish(r.Context(), "fare.payment.failed", event); err != nil {
				h.log.Error("failed to publish fare.payment.failed", zap.Error(err))
			}
			writeJSON(w, http.StatusBadGateway, map[string]any{"error": reason, "payment": p})
			return
		}

		event["tb_transfer_id"] = tbID
		if mlID != nil {
			event["mojaloop_transfer_id"] = *mlID
		}
		event["settled_at"] = time.Now().UTC().Format(time.RFC3339)
		if err := h.pub.Publish(r.Context(), "fare.payment.settled", event); err != nil {
			h.log.Error("failed to publish fare.payment.settled", zap.Error(err))
		}
		writeJSON(w, http.StatusCreated, p)
	}
}

// riderAccount returns the persisted TigerBeetle wallet account for a rider,
// allocating one sequentially from 1001 on first use. The mapping lives in
// commerce.rider_accounts (no hash-derived IDs → no collisions). Allocation
// runs in a transaction: INSERT ... ON CONFLICT (rider_sub) DO NOTHING, then
// SELECT. A concurrent first allocation for a different rider can collide on
// the account_id unique index; those rare races are retried.
func (h *Handler) riderAccount(ctx context.Context, riderSub string) (uint64, error) {
	var lastErr error
	for attempt := 0; attempt < 3; attempt++ {
		tx, err := h.db.Begin(ctx)
		if err != nil {
			return 0, err
		}
		if _, err := tx.Exec(ctx, `
			INSERT INTO commerce.rider_accounts (rider_sub, account_id)
			SELECT $1, COALESCE(MAX(account_id), 1000) + 1 FROM commerce.rider_accounts
			ON CONFLICT (rider_sub) DO NOTHING`, riderSub); err != nil {
			_ = tx.Rollback(ctx)
			var pgErr *pgconn.PgError
			if errors.As(err, &pgErr) && pgErr.Code == "23505" { // account_id race
				lastErr = err
				continue
			}
			return 0, err
		}
		var accountID uint64
		if err := tx.QueryRow(ctx,
			`SELECT account_id FROM commerce.rider_accounts WHERE rider_sub = $1`, riderSub).Scan(&accountID); err != nil {
			_ = tx.Rollback(ctx)
			return 0, err
		}
		if err := tx.Commit(ctx); err != nil {
			return 0, err
		}
		return accountID, nil
	}
	return 0, fmt.Errorf("allocate rider account: %w", lastErr)
}

// mojaloopTransfer performs the Mojaloop leg. With MOJALOOP_ENDPOINT set it
// POSTs a transfer request and returns the real transfer id (the server
// echoed id, or the client-generated transferId we submitted when the switch
// accepts without a body); any transport error or non-2xx response is an
// error and persists NO id. Without an endpoint it returns a clearly-labelled
// simulated id (SPEC §4 simulated fallback).
func (h *Handler) mojaloopTransfer(r *http.Request, endpoint, paymentID string, req createPaymentRequest) (string, error) {
	if endpoint == "" {
		id := "ml-simulated-" + uuid.NewString()
		h.log.Info("mojaloop transfer simulated", zap.String("payment", paymentID), zap.String("transfer_id", id))
		return id, nil
	}
	transferID := uuid.NewString()
	body, _ := json.Marshal(map[string]any{
		"transferId": transferID,
		"payer":      map[string]string{"partyId": req.RiderSub},
		"payee":      map[string]string{"partyId": "h2fleet-operator"},
		"amount":     map[string]any{"amount": req.AmountMinor, "currency": req.Currency},
	})
	resp, err := h.httpClient().Post(endpoint+"/transfers", "application/json", bytes.NewReader(body))
	if err != nil {
		return "", fmt.Errorf("mojaloop transfer request failed: %w", err)
	}
	defer resp.Body.Close()
	respBody, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return "", fmt.Errorf("mojaloop transfer rejected: status %d: %s", resp.StatusCode, string(respBody))
	}
	var parsed struct {
		TransferID string `json:"transferId"`
	}
	if err := json.Unmarshal(respBody, &parsed); err == nil && parsed.TransferID != "" {
		return parsed.TransferID, nil
	}
	// Accepted (e.g. 202) without a body: the switch will settle under the
	// transferId we submitted — that id is genuine, not fabricated.
	return transferID, nil
}

func (h *Handler) httpClient() *http.Client {
	return &http.Client{Timeout: 10 * time.Second}
}

// GetPayment handles GET /v1/payments/{id} (Keycloak JWT, status polling).
// Payments are financial PII: callers may only read their own payment unless
// they carry the operator or platform-admin realm role.
func (h *Handler) GetPayment(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	p, err := scanPayment(h.db.QueryRow(r.Context(),
		`SELECT `+paymentCols+` FROM commerce.fare_payments WHERE id = $1`, id))
	if errors.Is(err, pgx.ErrNoRows) {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "payment not found"})
		return
	}
	if err != nil {
		h.internal(w, "get payment", err)
		return
	}
	if p.RiderSub != auth.Subject(r.Context()) &&
		!auth.HasAnyRole(r.Context(), "operator", "platform-admin") {
		// 404 (not 403) so payment existence is not leaked to non-owners.
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "payment not found"})
		return
	}
	writeJSON(w, http.StatusOK, p)
}

// ListPayments handles GET /v1/payments?rider_sub=&status= (Keycloak JWT).
// Non-operator callers are always scoped to their own rider_sub — the
// rider_sub filter may not exceed the caller's own subject.
func (h *Handler) ListPayments(w http.ResponseWriter, r *http.Request) {
	rider := r.URL.Query().Get("rider_sub")
	if !auth.HasAnyRole(r.Context(), "operator", "platform-admin") {
		rider = auth.Subject(r.Context())
	}
	query := `SELECT ` + paymentCols + ` FROM commerce.fare_payments`
	args := []any{}
	where := ""
	if rider != "" {
		where += ` WHERE rider_sub = $1`
		args = append(args, rider)
	}
	if status := r.URL.Query().Get("status"); status != "" {
		if where == "" {
			where += ` WHERE status = $1`
		} else {
			where += ` AND status = $2`
		}
		args = append(args, status)
	}
	rows, err := h.db.Query(r.Context(), query+where+` ORDER BY created_at DESC LIMIT 200`, args...)
	if err != nil {
		h.internal(w, "list payments", err)
		return
	}
	defer rows.Close()

	payments := []Payment{}
	for rows.Next() {
		p, err := scanPayment(rows)
		if err != nil {
			h.internal(w, "scan payment", err)
			return
		}
		payments = append(payments, p)
	}
	if err := rows.Err(); err != nil {
		h.internal(w, "iterate payments", err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"payments": payments})
}
