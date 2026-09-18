package handlers

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"strconv"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"go.uber.org/zap"

	auth "github.com/munisp/hydrogenTransport/packages/go-auth"
	"github.com/munisp/hydrogenTransport/services/go/commerce-api/internal/ledger"
	"github.com/munisp/hydrogenTransport/services/go/commerce-api/internal/mojaloop"
)

// Payment mirrors commerce.fare_payments (fare-payments module).
// AmountMinor is the requested fare; ChargedMinor is what was actually
// debited after entitlement resolution and daily fare capping (null =
// uncapped legacy row, charged == amount_minor). RefundedMinor is the
// cumulative refunded amount (0009 G2; partial refunds leave a remainder).
type Payment struct {
	ID                 string    `json:"id"`
	RiderSub           string    `json:"rider_sub"`
	AmountMinor        int64     `json:"amount_minor"`
	ChargedMinor       *int64    `json:"charged_minor,omitempty"`
	Currency           string    `json:"currency"`
	MojaloopTransferID *string   `json:"mojaloop_transfer_id,omitempty"`
	TBTransferID       *string   `json:"tb_transfer_id,omitempty"`
	IdempotencyKey     *string   `json:"idempotency_key,omitempty"`
	Status             string    `json:"status"`
	RefundedMinor      int64     `json:"refunded_minor"`
	CreatedAt          time.Time `json:"created_at"`
}

const paymentCols = `id, rider_sub, amount_minor, charged_minor, currency, mojaloop_transfer_id,
	tb_transfer_id, idempotency_key, status, COALESCE(refunded_minor,0), created_at`

func scanPayment(row pgx.Row) (Payment, error) {
	var p Payment
	err := row.Scan(&p.ID, &p.RiderSub, &p.AmountMinor, &p.ChargedMinor, &p.Currency,
		&p.MojaloopTransferID, &p.TBTransferID, &p.IdempotencyKey, &p.Status, &p.RefundedMinor, &p.CreatedAt)
	return p, err
}

// effectiveCharged returns the debited amount (charged_minor when set, else
// amount_minor for pre-capping rows).
func (p Payment) effectiveCharged() int64 {
	if p.ChargedMinor != nil {
		return *p.ChargedMinor
	}
	return p.AmountMinor
}

// dailyCapMinor returns the per-rider daily fare cap in minor units
// (FARE_DAILY_CAP_MINOR, default €8.00; 0 disables capping). Fare capping
// (BUSINESS_LOGIC_AUDIT §16): a rider never pays more than the cap per
// civic day — once today's settled charges reach the cap, further rides
// settle at 0 (a capped free ride is still recorded with its requested
// fare).
func dailyCapMinor(getenv func(string) string) int64 {
	raw := getenv("FARE_DAILY_CAP_MINOR")
	if raw == "" {
		return 800
	}
	n, err := strconv.ParseInt(raw, 10, 64)
	if err != nil || n < 0 {
		return 800
	}
	return n
}

// fareCapTimezone returns the IANA timezone the daily-cap window is drawn
// in (FARE_CAP_TIMEZONE, default UTC). Wave-6 A1-02: the cap must reset at
// the city's civic midnight, not whenever the DB server's clock says so —
// set this to the operating city's zone (e.g. Europe/Berlin) in production.
func fareCapTimezone(getenv func(string) string) string {
	if tz := getenv("FARE_CAP_TIMEZONE"); tz != "" {
		return tz
	}
	return "UTC"
}

// platformCurrency returns the single settlement currency of the platform
// (PLATFORM_CURRENCY, default EUR). Wave-6 A2-03: the TigerBeetle ledger is
// single-currency, so any payment in another currency must be rejected
// loudly instead of silently posting foreign-denominated minor units.
func platformCurrency(getenv func(string) string) string {
	if c := getenv("PLATFORM_CURRENCY"); c != "" {
		return c
	}
	return "EUR"
}

// resolveEntitlement returns the charge after applying the rider's best
// active fare entitlement (0009 G1, Wave-6 A2-01). A 'free' or 'pass'
// product covers the ride entirely (charge 0); a 'discount' product reduces
// the fare by its percentage (best discount wins). The product code behind
// the resolution is returned for the domain event ("" = full fare).
func resolveEntitlement(ctx context.Context, tx pgx.Tx, riderSub string, amount int64) (int64, string, error) {
	rows, err := tx.Query(ctx, `
		SELECT p.kind, COALESCE(p.discount_pct,0), p.code
		FROM commerce.rider_entitlements e
		JOIN commerce.fare_products p ON p.id = e.product_id
		WHERE e.rider_sub = $1 AND e.valid_from <= now() AND e.valid_to > now() AND p.active`, riderSub)
	if err != nil {
		return 0, "", err
	}
	defer rows.Close()
	charge, code, bestDiscount := amount, "", 0
	for rows.Next() {
		var kind, productCode string
		var pct int
		if err := rows.Scan(&kind, &pct, &productCode); err != nil {
			return 0, "", err
		}
		switch kind {
		case "free", "pass":
			return 0, productCode, rows.Err() // full coverage beats any discount
		case "discount":
			if pct > bestDiscount {
				bestDiscount, code = pct, productCode
			}
		}
	}
	if err := rows.Err(); err != nil {
		return 0, "", err
	}
	if bestDiscount > 0 {
		charge = amount * int64(100-bestDiscount) / 100
	}
	return charge, code, nil
}

type createPaymentRequest struct {
	AmountMinor int64  `json:"amount_minor"`
	Currency    string `json:"currency"`
	// RiderSub is DEPRECATED: the paying rider is always the authenticated
	// JWT subject. A non-empty value that differs from the JWT subject is
	// rejected with 403 (P0-1); a matching value is accepted but ignored.
	RiderSub    string `json:"rider_sub"`
	UseMojaloop bool   `json:"use_mojaloop"`
}

// CreatePayment handles POST /v1/payments (Keycloak JWT, Idempotency-Key
// header required). Flow: validate currency (PLATFORM_CURRENCY, Wave-6
// A2-03) → per-rider advisory-locked transaction (Wave-6 A1-01): resolve
// the best active fare entitlement (Wave-6 A2-01: pass/free → 0, discount
// → reduced) → clamp to the remaining daily cap in FARE_CAP_TIMEZONE
// (Wave-6 A1-02) → insert fare_payments row (idempotent on the key) →
// TigerBeetle transfer rider wallet → operator revenue → commit → publish
// fare.payment.initiated / fare.payment.settled (SPEC §3.3). Optionally
// runs a Mojaloop transfer of the charged amount (real HTTP POST when
// MOJALOOP_ENDPOINT is set; without an endpoint the payment fails closed as
// mojaloop_unavailable unless the explicit dev opt-in
// H2_SIMULATED_MOJALOOP=true is set, SPEC §4).
func (h *Handler) CreatePayment(mojaloopEndpoint string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		idemKey := r.Header.Get("Idempotency-Key")
		if idemKey == "" {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "Idempotency-Key header is required"})
			return
		}
		var req createPaymentRequest
		if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&req); err != nil {
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
		// SECURITY (P0-1): the paying rider is ALWAYS the authenticated JWT
		// subject. The body's rider_sub is kept for backwards compatibility
		// but is never trusted: a value that does not match the JWT subject
		// is rejected (wallet-spoofing attempt), otherwise it is ignored.
		subject := auth.Subject(r.Context())
		if subject == "" {
			writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "authenticated subject required"})
			return
		}
		if req.RiderSub != "" && req.RiderSub != subject {
			writeJSON(w, http.StatusForbidden, map[string]string{"error": "rider_sub does not match the authenticated subject"})
			return
		}
		req.RiderSub = subject

		// Wave-6 A2-03: the TigerBeetle ledger is single-currency — a payment
		// in any other currency would silently mis-post minor units into an
		// EUR-denominated ledger. Reject loudly instead.
		if req.Currency != platformCurrency(os.Getenv) {
			writeJSON(w, http.StatusUnprocessableEntity, map[string]string{
				"error": "unsupported currency " + strconv.Quote(req.Currency) +
					" (platform settles in " + platformCurrency(os.Getenv) + ")"})
			return
		}

		// Wave-6 A1-01: entitlement resolution (A2-01), fare capping, the
		// insert and the final status update run in ONE transaction guarded
		// by a per-rider advisory lock, so two concurrent taps for the same
		// rider serialize and can never combine to exceed the daily cap (a
		// payment counts against the cap only once it settles, so the lock
		// is held until the final commit below). A crash rolls the row back;
		// the deterministic TigerBeetle transfer id (from the idempotency
		// key) makes the client retry safe.
		tx, err := h.db.Begin(r.Context())
		if err != nil {
			h.internal(w, "begin payment transaction", err)
			return
		}
		defer func() { _ = tx.Rollback(r.Context()) }()

		if _, err := tx.Exec(r.Context(), `SELECT pg_advisory_xact_lock(hashtext($1))`, req.RiderSub); err != nil {
			h.internal(w, "lock rider payment stream", err)
			return
		}

		// Wave-6 A2-01: passes/concessions resolve before the cap — a free
		// or pass entitlement covers the ride (charge 0), a discount
		// entitlement reduces the fare.
		charge, entitlementCode, err := resolveEntitlement(r.Context(), tx, req.RiderSub, req.AmountMinor)
		if err != nil {
			h.internal(w, "resolve fare entitlement", err)
			return
		}

		// Fare capping: compute today's already-settled spend for the rider
		// and clamp the charge to the remaining daily allowance. "Today" is
		// the civic day in FARE_CAP_TIMEZONE (Wave-6 A1-02), not the DB
		// server's local midnight.
		var spentToday int64
		if cap := dailyCapMinor(os.Getenv); cap > 0 && charge > 0 {
			if err := tx.QueryRow(r.Context(), `
				SELECT COALESCE(sum(COALESCE(charged_minor, amount_minor)),0)
				FROM commerce.fare_payments
				WHERE rider_sub = $1 AND status = 'settled'
				  AND created_at >= (date_trunc('day', now() AT TIME ZONE $2)) AT TIME ZONE $2`,
				req.RiderSub, fareCapTimezone(os.Getenv)).Scan(&spentToday); err != nil {
				h.internal(w, "compute daily fare spend", err)
				return
			}
			if remaining := cap - spentToday; remaining < charge {
				charge = max(remaining, 0)
			}
		}

		paymentID := uuid.NewString()
		_, err = tx.Exec(r.Context(), `
			INSERT INTO commerce.fare_payments (id, rider_sub, amount_minor, charged_minor, currency, status, idempotency_key)
			VALUES ($1, $2, $3, $4, $5, 'initiated', $6)`,
			paymentID, req.RiderSub, req.AmountMinor, charge, req.Currency, idemKey)
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
			"payment_id":    paymentID,
			"rider_sub":     req.RiderSub,
			"amount_minor":  req.AmountMinor,
			"charged_minor": charge,
			"currency":      req.Currency,
		}
		if entitlementCode != "" {
			event["entitlement"] = map[string]any{"product_code": entitlementCode}
		}
		if charge < req.AmountMinor && entitlementCode == "" {
			event["fare_cap"] = map[string]any{
				"applied":           true,
				"spent_today_minor": spentToday,
			}
		}

		// Ledger transfer: rider wallet (1xxx, persisted per-rider mapping) →
		// operator revenue (2xxx). The TigerBeetle transfer ID is derived
		// deterministically from the Idempotency-Key so client retries of the
		// same request can never double-post the transfer. A fully covered or
		// capped ride (charge == 0) settles without a ledger posting.
		status := "settled"
		var tbID *string
		insufficientFunds := false
		if charge > 0 {
			account, err := h.riderAccount(r.Context(), req.RiderSub)
			if err != nil {
				h.internal(w, "ensure rider ledger account", err)
				return
			}
			transferID, err := h.ledger.Transfer(
				ledger.DeterministicTransferID(idemKey), account, ledger.OperatorRevenueAccount,
				uint64(charge), ledger.CodeFare)
			if err != nil {
				h.log.Error("ledger transfer failed", zap.String("payment", paymentID), zap.Error(err))
				status = "failed"
				// An unfunded (but provisioned) rider wallet is a client-visible
				// funding problem, not an upstream failure: mapped to 402 below.
				insufficientFunds = errors.Is(err, ledger.ErrInsufficientFunds)
			} else {
				tbID = &transferID
			}
		}

		// Optional Mojaloop leg (full parties → quotes → transfers flow via
		// internal/mojaloop when MOJALOOP_ENDPOINT is set; fail-closed
		// otherwise unless H2_SIMULATED_MOJALOOP=true). A Mojaloop failure
		// never fabricates a transfer id — the payment is marked with the
		// classified mojaloop_* status instead. The leg moves the CHARGED
		// amount (Wave-6: it previously moved the uncapped requested amount
		// while the ledger charged the capped amount).
		var mlID *string
		var mlErr error
		if req.UseMojaloop && status == "settled" {
			id, err := h.mojaloopTransfer(r, mojaloopEndpoint, paymentID, idemKey, req, charge)
			if err != nil {
				h.log.Error("mojaloop transfer failed", zap.String("payment", paymentID), zap.Error(err))
				status = mojaloop.PaymentStatus(err)
				mlErr = err
			} else {
				mlID = &id
			}
		}

		// Persist the final status and commit FIRST; domain events are
		// published only after the commit (outbox-lite ordering) so consumers
		// never observe an event for a state that was never recorded.
		p, err := scanPayment(tx.QueryRow(r.Context(), `
			UPDATE commerce.fare_payments
			SET status = $2, tb_transfer_id = $3, mojaloop_transfer_id = $4
			WHERE id = $1 RETURNING `+paymentCols, paymentID, status, tbID, mlID))
		if err != nil {
			h.internal(w, "finalize payment", err)
			return
		}
		if err := tx.Commit(r.Context()); err != nil {
			h.internal(w, "commit payment", err)
			return
		}

		if err := h.pub.Publish(r.Context(), "fare.payment.initiated", event); err != nil {
			h.log.Error("failed to publish fare.payment.initiated", zap.Error(err))
		}

		if status != "settled" {
			reason := "ledger transfer failed"
			if mlErr != nil {
				reason = "mojaloop transfer failed: " + mlErr.Error()
			}
			event["reason"] = reason
			event["failed_at"] = time.Now().UTC().Format(time.RFC3339)
			if err := h.pub.Publish(r.Context(), "fare.payment.failed", event); err != nil {
				h.log.Error("failed to publish fare.payment.failed", zap.Error(err))
			}
			if insufficientFunds {
				// 402 + machine-readable code: the wallet exists but is not
				// funded. Never a panic, never a negative balance.
				writeJSON(w, http.StatusPaymentRequired, map[string]any{
					"error":   "insufficient_funds",
					"message": "rider wallet has insufficient funds; top up via POST /v1/wallets/topup",
					"payment": p,
				})
				return
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

		// Loyalty accrual (loyalty-marketplace module): 1 point per full €1
		// (100 minor units) of settled fare actually charged after capping.
		// Idempotent on the payment id via commerce.loyalty_ledger.ref_id, so
		// payment retries never double award. Accrual failure must not fail an
		// already-settled payment — it is logged for reconciliation.
		if err := h.accrueLoyaltyPoints(r.Context(), paymentID, req.RiderSub, charge); err != nil {
			h.log.Error("loyalty accrual failed",
				zap.String("payment", paymentID), zap.Error(err))
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
// runs the full FSPIOP payer flow (GET /parties → POST /quotes →
// POST /transfers) via internal/mojaloop, idempotent on the request's
// Idempotency-Key, and returns the real switch transfer id. Without an
// endpoint it fails closed (classified mojaloop_unavailable): a fabricated
// transfer id is only ever returned behind the explicit dev opt-in
// H2_SIMULATED_MOJALOOP=true (SPEC §4 simulated fallback, env-gated), and
// even then it is clearly labelled.
func (h *Handler) mojaloopTransfer(r *http.Request, endpoint, paymentID, idemKey string, req createPaymentRequest, amountMinor int64) (string, error) {
	if endpoint == "" {
		if envOr("H2_SIMULATED_MOJALOOP", "") != "true" {
			return "", &mojaloop.Error{
				Op:      "transfer",
				Kind:    mojaloop.KindUnavailable,
				Message: "MOJALOOP_ENDPOINT is not configured (set H2_SIMULATED_MOJALOOP=true to opt into the simulated dev transfer)",
			}
		}
		id := "ml-simulated-" + uuid.NewString()
		h.log.Warn("mojaloop transfer SIMULATED (H2_SIMULATED_MOJALOOP=true — no real switch transfer happened)",
			zap.String("payment", paymentID), zap.String("transfer_id", id))
		return id, nil
	}
	client, err := mojaloop.New(mojaloop.Config{
		Endpoint:       endpoint,
		DFSPID:         envOr("MOJALOOP_DFSP_ID", "h2fleet"),
		PayeePartyID:   envOr("MOJALOOP_PAYEE_PARTY_ID", "h2fleet-operator"),
		PayeePartyType: envOr("MOJALOOP_PAYEE_PARTY_TYPE", mojaloop.PartyTypeBusiness),
		Secret:         os.Getenv("MOJALOOP_ILP_SECRET"),
		// The mojaloop/simulator does not generate ILP material in quotes;
		// MOJALOOP_GENERATE_ILP=true (the compose default) makes the client
		// generate packet/condition itself. Against a real sdk-scheme-adapter
		// the payee's quote values are forwarded verbatim.
		GenerateILP:    envOr("MOJALOOP_GENERATE_ILP", "true") == "true",
		AttemptTimeout: 8 * time.Second,
		RetryBudget:    20 * time.Second,
	})
	if err != nil {
		return "", err
	}
	res, err := client.Transfer(r.Context(), mojaloop.PaymentRequest{
		IdempotencyKey: idemKey,
		PayerPartyID:   req.RiderSub,
		PayerPartyType: mojaloop.PartyTypeAlias,
		AmountMinor:    amountMinor, // the charged amount, not the requested fare
		Currency:       req.Currency,
	})
	if err != nil {
		return "", err
	}
	h.log.Info("mojaloop transfer committed",
		zap.String("payment", paymentID),
		zap.String("transfer_id", res.TransferID),
		zap.String("payee_fsp", res.PayeeFSPID),
		zap.Bool("fulfilment_verified", res.FulfilmentVerified))
	return res.TransferID, nil
}

func envOr(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

// RefundPayment handles POST /v1/payments/{id}/refund (Keycloak JWT,
// operator). Refunds a settled (or partially refunded) payment: reversal
// transfer operator revenue (2001) → rider wallet (1xxx). Wave-6 A2-02: an
// optional body {"amount_minor": N} requests a PARTIAL refund (0 < N ≤
// refundable remainder); without a body the full remainder is refunded.
// refunded_minor accumulates (0009 G2); the payment flips to 'refunded'
// when the charged amount is fully returned, otherwise it stays
// 'partially_refunded' and refundable for the remainder. The reversal
// transfer id is deterministic per payment AND per cumulative total, so a
// retried refund of the same step cannot double-post, and the conditional
// UPDATE guards the remainder against concurrent refunds. Loyalty points
// are clawed back in proportion to the refunded amount.
func (h *Handler) RefundPayment(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")

	// Optional body: {"amount_minor": N}. An empty body means "refund the
	// full remainder" (the pre-Wave-6 full-refund behavior).
	var req struct {
		AmountMinor *int64 `json:"amount_minor"`
	}
	if r.Body != nil && r.ContentLength != 0 {
		if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&req); err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid JSON body"})
			return
		}
	}

	p, err := scanPayment(h.db.QueryRow(r.Context(), `
		SELECT `+paymentCols+` FROM commerce.fare_payments WHERE id = $1`, id))
	if errors.Is(err, pgx.ErrNoRows) {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "payment not found"})
		return
	}
	if err != nil {
		h.internal(w, "load payment for refund", err)
		return
	}
	if p.Status == "refunded" {
		writeJSON(w, http.StatusOK, p) // idempotent replay: nothing left to refund
		return
	}
	if p.Status != "settled" && p.Status != "partially_refunded" {
		writeJSON(w, http.StatusConflict, map[string]string{"error": "only settled payments can be refunded (status " + p.Status + ")"})
		return
	}

	charged := p.effectiveCharged()
	remainder := charged - p.RefundedMinor
	if remainder < 0 {
		remainder = 0
	}
	amount := remainder
	if req.AmountMinor != nil {
		if *req.AmountMinor <= 0 || *req.AmountMinor > remainder {
			writeJSON(w, http.StatusUnprocessableEntity, map[string]string{
				"error": "amount_minor must be between 1 and the refundable remainder (" + strconv.FormatInt(remainder, 10) + ")"})
			return
		}
		amount = *req.AmountMinor
	}
	// A fully covered (0-charged) payment has amount == 0: no ledger posting,
	// the UPDATE below simply marks it refunded.

	// The deterministic transfer id is keyed by the cumulative total AFTER
	// this refund: a retry of this same refund step replays the same id
	// (TigerBeetle dedups), and a concurrent different-amount refund gets a
	// different id but loses the conditional UPDATE below.
	newTotal := p.RefundedMinor + amount
	var tbID *string
	if amount > 0 {
		account, err := h.riderAccount(r.Context(), p.RiderSub)
		if err != nil {
			h.internal(w, "load rider ledger account", err)
			return
		}
		transferID, err := h.ledger.Transfer(
			ledger.DeterministicTransferID(fmt.Sprintf("refund:%s:%d", p.ID, newTotal)),
			ledger.OperatorRevenueAccount, account, uint64(amount), ledger.CodeFare)
		if err != nil {
			h.internal(w, "refund ledger transfer", err)
			return
		}
		tbID = &transferID
	}

	newStatus := "partially_refunded"
	if newTotal >= charged {
		newStatus = "refunded"
	}
	p, err = scanPayment(h.db.QueryRow(r.Context(), `
		UPDATE commerce.fare_payments
		SET refunded_minor = $2, status = $3, refunded_at = now()
		WHERE id = $1 AND status IN ('settled','partially_refunded')
		  AND refunded_minor = $4
		RETURNING `+paymentCols, id, newTotal, newStatus, p.RefundedMinor))
	if errors.Is(err, pgx.ErrNoRows) {
		// Lost a refund race — the concurrent refund won the remainder; the
		// deterministic transfer id means our ledger posting dedups into
		// theirs only when identical, so re-read and report the true state.
		p, err = scanPayment(h.db.QueryRow(r.Context(), `
			SELECT `+paymentCols+` FROM commerce.fare_payments WHERE id = $1`, id))
		if err == nil {
			writeJSON(w, http.StatusConflict, map[string]any{
				"error": "concurrent refund changed the refundable remainder; retry against the current state", "payment": p})
			return
		}
	}
	if err != nil {
		h.internal(w, "mark payment refunded", err)
		return
	}

	// Loyalty clawback: reverse the points accrued for the refunded amount
	// (idempotent via per-step ref_id; balance never goes below 0).
	clawbackRef := fmt.Sprintf("refund:%s:%d", p.ID, newTotal)
	if err := h.clawbackLoyaltyPoints(r.Context(), clawbackRef, p.RiderSub, amount); err != nil {
		h.log.Error("loyalty clawback failed", zap.String("payment", p.ID), zap.Error(err))
	}

	if err := h.pub.Publish(r.Context(), "fare.payment.refunded", map[string]any{
		"payment_id":     p.ID,
		"rider_sub":      p.RiderSub,
		"amount_minor":   amount,
		"refunded_minor": p.RefundedMinor,
		"status":         p.Status,
		"currency":       p.Currency,
		"tb_transfer_id": tbID,
		"refunded_at":    time.Now().UTC().Format(time.RFC3339),
	}); err != nil {
		h.log.Error("failed to publish fare.payment.refunded", zap.Error(err))
	}
	writeJSON(w, http.StatusOK, p)
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