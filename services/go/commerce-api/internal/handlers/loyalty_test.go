package handlers

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/pashagolub/pgxmock/v4"
	"go.uber.org/zap"
)

func redemptionRow(id, rider, offer string, spent int, key, status string) *pgxmock.Rows {
	return pgxmock.NewRows([]string{
		"id", "rider_sub", "offer_id", "points_spent", "idempotency_key", "status", "created_at",
	}).AddRow(id, rider, offer, spent, key, status,
		time.Date(2026, 7, 24, 9, 0, 0, 0, time.UTC))
}

// GET /v1/loyalty/balance returns the real balance, lazily creating a
// zero-balance account for new riders.
func TestGetLoyaltyBalance_NewRider(t *testing.T) {
	pool, err := pgxmock.NewPool()
	if err != nil {
		t.Fatalf("pgxmock.NewPool: %v", err)
	}
	defer pool.Close()
	h := &Handler{db: pool, log: zap.NewExample()}

	pool.ExpectQuery(`INSERT INTO commerce\.loyalty_accounts`).
		WithArgs("rider-a").
		WillReturnRows(pgxmock.NewRows([]string{"rider_sub", "points", "updated_at"}).
			AddRow("rider-a", 0, time.Date(2026, 7, 24, 9, 0, 0, 0, time.UTC)))

	req := withClaims(httptest.NewRequest(http.MethodGet, "/v1/loyalty/balance", nil), "rider-a")
	rec := httptest.NewRecorder()
	h.GetLoyaltyBalance(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("got %d, want 200 (body: %s)", rec.Code, rec.Body)
	}
	var b LoyaltyBalance
	if err := json.Unmarshal(rec.Body.Bytes(), &b); err != nil {
		t.Fatalf("decode body: %v", err)
	}
	if b.RiderSub != "rider-a" || b.Points != 0 {
		t.Fatalf("new rider must see 0 points, got %+v", b)
	}
	if err := pool.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet db expectations: %v", err)
	}
}

// Loyalty accrual awards 1 point per full €1 of settled fare, idempotently:
// a replay for the same payment id (loyalty_ledger.ref_id conflict) must not
// increment the balance a second time.
func TestAccrueLoyaltyPoints_IdempotentOnPaymentRetry(t *testing.T) {
	pool, err := pgxmock.NewPool()
	if err != nil {
		t.Fatalf("pgxmock.NewPool: %v", err)
	}
	defer pool.Close()
	h := &Handler{db: pool, log: zap.NewExample()}

	// First award for payment pay-1 (550 cents → 5 points): ledger entry
	// lands (1 row) → balance increment runs.
	pool.ExpectBegin()
	pool.ExpectExec(`INSERT INTO commerce\.loyalty_ledger`).
		WithArgs(pgxmock.AnyArg(), "rider-a", int64(5), "pay-1").
		WillReturnResult(pgxmock.NewResult("INSERT", 1))
	pool.ExpectExec(`INSERT INTO commerce\.loyalty_accounts`).
		WithArgs("rider-a", int64(5)).
		WillReturnResult(pgxmock.NewResult("INSERT", 1))
	pool.ExpectCommit()

	// Retry of the same payment: ledger insert conflicts (0 rows) → the
	// balance increment must NOT run again.
	pool.ExpectBegin()
	pool.ExpectExec(`INSERT INTO commerce\.loyalty_ledger`).
		WithArgs(pgxmock.AnyArg(), "rider-a", int64(5), "pay-1").
		WillReturnResult(pgxmock.NewResult("INSERT", 0))
	pool.ExpectCommit()

	ctx := context.Background()
	if err := h.accrueLoyaltyPoints(ctx, "pay-1", "rider-a", 550); err != nil {
		t.Fatalf("first accrual: %v", err)
	}
	if err := h.accrueLoyaltyPoints(ctx, "pay-1", "rider-a", 550); err != nil {
		t.Fatalf("accrual retry: %v", err)
	}
	if err := pool.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet db expectations: %v", err)
	}
}

// Fares below €1 award no points and touch no tables.
func TestAccrueLoyaltyPoints_SubEuroAwardsNothing(t *testing.T) {
	h := &Handler{log: zap.NewExample()} // no db: must not be reached
	if err := h.accrueLoyaltyPoints(context.Background(), "pay-2", "rider-a", 99); err != nil {
		t.Fatalf("sub-euro accrual: %v", err)
	}
}

// Redeem happy path: offer locked, balance checked and decremented,
// redemption + ledger audit entry recorded — all in one transaction.
func TestRedeemOffer_HappyPath(t *testing.T) {
	pool, err := pgxmock.NewPool()
	if err != nil {
		t.Fatalf("pgxmock.NewPool: %v", err)
	}
	defer pool.Close()
	h := &Handler{db: pool, log: zap.NewExample()}

	pool.ExpectBegin()
	pool.ExpectQuery(`FROM commerce\.loyalty_redemptions WHERE idempotency_key`).
		WithArgs("offer-1:rider-a").
		WillReturnError(pgx.ErrNoRows)
	pool.ExpectQuery(`SELECT cost_points FROM commerce\.marketplace_offers`).
		WithArgs("offer-1").
		WillReturnRows(pgxmock.NewRows([]string{"cost_points"}).AddRow(50))
	pool.ExpectExec(`INSERT INTO commerce\.loyalty_accounts`).
		WithArgs("rider-a").
		WillReturnResult(pgxmock.NewResult("INSERT", 1))
	pool.ExpectQuery(`UPDATE commerce\.loyalty_accounts`).
		WithArgs("rider-a", 50).
		WillReturnRows(pgxmock.NewRows([]string{"points"}).AddRow(30))
	pool.ExpectExec(`INSERT INTO commerce\.loyalty_redemptions`).
		WithArgs(pgxmock.AnyArg(), "rider-a", "offer-1", 50, "offer-1:rider-a").
		WillReturnResult(pgxmock.NewResult("INSERT", 1))
	pool.ExpectExec(`INSERT INTO commerce\.loyalty_ledger`).
		WithArgs(pgxmock.AnyArg(), "rider-a", -50, "redeem:offer-1:rider-a").
		WillReturnResult(pgxmock.NewResult("INSERT", 1))
	pool.ExpectCommit()

	req := withClaims(httptest.NewRequest(http.MethodPost, "/v1/loyalty/redeem",
		strings.NewReader(`{"offer_id":"offer-1"}`)), "rider-a")
	rec := httptest.NewRecorder()
	h.RedeemOffer(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("got %d, want 200 (body: %s)", rec.Code, rec.Body)
	}
	var resp map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode body: %v", err)
	}
	if resp["points_spent"] != float64(50) || resp["remaining_points"] != float64(30) {
		t.Fatalf("unexpected redeem response: %v", resp)
	}
	if resp["redemption_id"] == nil || resp["idempotency_key"] != "offer-1:rider-a" {
		t.Fatalf("redemption record fields missing: %v", resp)
	}
	if err := pool.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet db expectations: %v", err)
	}
}

// Redeem with insufficient balance → 402, no redemption recorded, no deduction.
func TestRedeemOffer_InsufficientFunds(t *testing.T) {
	pool, err := pgxmock.NewPool()
	if err != nil {
		t.Fatalf("pgxmock.NewPool: %v", err)
	}
	defer pool.Close()
	h := &Handler{db: pool, log: zap.NewExample()}

	pool.ExpectBegin()
	pool.ExpectQuery(`FROM commerce\.loyalty_redemptions WHERE idempotency_key`).
		WithArgs("offer-1:rider-a").
		WillReturnError(pgx.ErrNoRows)
	pool.ExpectQuery(`SELECT cost_points FROM commerce\.marketplace_offers`).
		WithArgs("offer-1").
		WillReturnRows(pgxmock.NewRows([]string{"cost_points"}).AddRow(50))
	pool.ExpectExec(`INSERT INTO commerce\.loyalty_accounts`).
		WithArgs("rider-a").
		WillReturnResult(pgxmock.NewResult("INSERT", 1))
	pool.ExpectQuery(`UPDATE commerce\.loyalty_accounts`).
		WithArgs("rider-a", 50).
		WillReturnError(pgx.ErrNoRows) // points >= 50 guard failed

	req := withClaims(httptest.NewRequest(http.MethodPost, "/v1/loyalty/redeem",
		strings.NewReader(`{"offer_id":"offer-1"}`)), "rider-a")
	rec := httptest.NewRecorder()
	h.RedeemOffer(rec, req)

	if rec.Code != http.StatusPaymentRequired {
		t.Fatalf("got %d, want 402 (body: %s)", rec.Code, rec.Body)
	}
	if !strings.Contains(rec.Body.String(), "insufficient") {
		t.Fatalf("error body should say insufficient: %s", rec.Body)
	}
	if err := pool.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet db expectations: %v", err)
	}
}

// Retrying a redeem with the same idempotency key returns the original
// redemption WITHOUT deducting points again.
func TestRedeemOffer_IdempotentRetrySameKey(t *testing.T) {
	pool, err := pgxmock.NewPool()
	if err != nil {
		t.Fatalf("pgxmock.NewPool: %v", err)
	}
	defer pool.Close()
	h := &Handler{db: pool, log: zap.NewExample()}

	pool.ExpectBegin()
	pool.ExpectQuery(`FROM commerce\.loyalty_redemptions WHERE idempotency_key`).
		WithArgs("idem-redeem-1").
		WillReturnRows(redemptionRow("red-1", "rider-a", "offer-1", 50, "idem-redeem-1", "completed"))
	pool.ExpectCommit()
	// Deliberately no UPDATE commerce.loyalty_accounts expectation: a retry
	// must never deduct again (ExpectationsWereMet would fail if it did).

	req := withClaims(httptest.NewRequest(http.MethodPost, "/v1/loyalty/redeem",
		strings.NewReader(`{"offer_id":"offer-1"}`)), "rider-a")
	req.Header.Set("Idempotency-Key", "idem-redeem-1")
	rec := httptest.NewRecorder()
	h.RedeemOffer(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("got %d, want 200 (body: %s)", rec.Code, rec.Body)
	}
	var resp map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode body: %v", err)
	}
	if resp["redemption_id"] != "red-1" || resp["points_spent"] != float64(50) {
		t.Fatalf("retry must return the original redemption, got %v", resp)
	}
	if _, deducted := resp["remaining_points"]; deducted {
		t.Fatalf("replay must not report a fresh balance deduction: %v", resp)
	}
	if err := pool.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet db expectations: %v", err)
	}
}

// Reusing an idempotency key for a DIFFERENT offer is a conflict, not a retry.
func TestRedeemOffer_KeyReuseDifferentOffer(t *testing.T) {
	pool, err := pgxmock.NewPool()
	if err != nil {
		t.Fatalf("pgxmock.NewPool: %v", err)
	}
	defer pool.Close()
	h := &Handler{db: pool, log: zap.NewExample()}

	pool.ExpectBegin()
	pool.ExpectQuery(`FROM commerce\.loyalty_redemptions WHERE idempotency_key`).
		WithArgs("idem-redeem-1").
		WillReturnRows(redemptionRow("red-1", "rider-a", "offer-1", 50, "idem-redeem-1", "completed"))
	pool.ExpectCommit()

	req := withClaims(httptest.NewRequest(http.MethodPost, "/v1/loyalty/redeem",
		strings.NewReader(`{"offer_id":"offer-2"}`)), "rider-a")
	req.Header.Set("Idempotency-Key", "idem-redeem-1")
	rec := httptest.NewRecorder()
	h.RedeemOffer(rec, req)

	if rec.Code != http.StatusConflict {
		t.Fatalf("got %d, want 409 (body: %s)", rec.Code, rec.Body)
	}
	if err := pool.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet db expectations: %v", err)
	}
}
