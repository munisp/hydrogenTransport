package handlers

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/pashagolub/pgxmock/v4"
	"go.uber.org/zap"

	"github.com/munisp/hydrogenTransport/services/go/commerce-api/internal/ledger"
)

// --- fare capping -----------------------------------------------------------

// A rider who already settled €7.00 today (cap €8.00) is charged only €1.00
// of a €5.00 fare; the rest rides under the cap.
func TestCreatePayment_FareCapClampsCharge(t *testing.T) {
	pool, err := pgxmock.NewPool()
	if err != nil {
		t.Fatalf("pgxmock.NewPool: %v", err)
	}
	defer pool.Close()
	led, pub := &fakeLedger{}, &fakePublisher{}
	h := &Handler{db: pool, ledger: led, pub: pub, log: zap.NewExample()}

	pool.ExpectQuery(`sum\(COALESCE\(charged_minor`).
		WithArgs("rider-a").
		WillReturnRows(pgxmock.NewRows([]string{"sum"}).AddRow(int64(700)))
	pool.ExpectExec(`INSERT INTO commerce\.fare_payments`).
		WithArgs(pgxmock.AnyArg(), "rider-a", int64(500), int64(100), "EUR", "idem-cap").
		WillReturnResult(pgxmock.NewResult("INSERT", 1))
	pool.ExpectBegin()
	pool.ExpectExec(`INSERT INTO commerce\.rider_accounts`).WithArgs("rider-a").
		WillReturnResult(pgxmock.NewResult("INSERT", 1))
	pool.ExpectQuery(`SELECT account_id FROM commerce\.rider_accounts`).WithArgs("rider-a").
		WillReturnRows(pgxmock.NewRows([]string{"account_id"}).AddRow(uint64(1001)))
	pool.ExpectCommit()
	pool.ExpectQuery(`UPDATE commerce\.fare_payments`).
		WithArgs(pgxmock.AnyArg(), "settled", pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(paymentRow("pay-cap", "rider-a", "idem-cap", "settled"))
	// Loyalty accrues on the CHARGED amount: 100 cents → 1 point.
	pool.ExpectBegin()
	pool.ExpectExec(`INSERT INTO commerce\.loyalty_ledger`).
		WithArgs(pgxmock.AnyArg(), "rider-a", int64(1), pgxmock.AnyArg()).
		WillReturnResult(pgxmock.NewResult("INSERT", 1))
	pool.ExpectExec(`INSERT INTO commerce\.loyalty_accounts`).
		WithArgs("rider-a", int64(1)).
		WillReturnResult(pgxmock.NewResult("INSERT", 1))
	pool.ExpectCommit()

	rec := httptest.NewRecorder()
	h.CreatePayment("")(rec, createRequest(t, `{"amount_minor":500,"currency":"EUR"}`, "idem-cap", "rider-a"))

	if rec.Code != http.StatusCreated {
		t.Fatalf("got %d, want 201 (body: %s)", rec.Code, rec.Body)
	}
	if led.count() != 1 || led.transfers[0].amount != 100 {
		t.Fatalf("ledger must charge the capped €1.00, got %+v", led.transfers)
	}
	if err := pool.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet db expectations: %v", err)
	}
}

// At the cap the ride is free: no ledger posting, still recorded as settled
// with charged_minor = 0 (a capped ride is never a hidden charge).
func TestCreatePayment_FullyCappedRideIsFree(t *testing.T) {
	pool, err := pgxmock.NewPool()
	if err != nil {
		t.Fatalf("pgxmock.NewPool: %v", err)
	}
	defer pool.Close()
	led, pub := &fakeLedger{}, &fakePublisher{}
	h := &Handler{db: pool, ledger: led, pub: pub, log: zap.NewExample()}

	pool.ExpectQuery(`sum\(COALESCE\(charged_minor`).
		WithArgs("rider-a").
		WillReturnRows(pgxmock.NewRows([]string{"sum"}).AddRow(int64(800)))
	pool.ExpectExec(`INSERT INTO commerce\.fare_payments`).
		WithArgs(pgxmock.AnyArg(), "rider-a", int64(500), int64(0), "EUR", "idem-free").
		WillReturnResult(pgxmock.NewResult("INSERT", 1))
	// charge == 0 → no rider-account provisioning, no ledger transfer.
	pool.ExpectQuery(`UPDATE commerce\.fare_payments`).
		WithArgs(pgxmock.AnyArg(), "settled", pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(paymentRow("pay-free", "rider-a", "idem-free", "settled"))

	rec := httptest.NewRecorder()
	h.CreatePayment("")(rec, createRequest(t, `{"amount_minor":500,"currency":"EUR"}`, "idem-free", "rider-a"))

	if rec.Code != http.StatusCreated {
		t.Fatalf("got %d, want 201 (body: %s)", rec.Code, rec.Body)
	}
	if led.count() != 0 {
		t.Fatalf("fully capped ride must not post a ledger transfer, got %d", led.count())
	}
	if err := pool.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet db expectations: %v", err)
	}
}

// --- refunds ----------------------------------------------------------------

// Refunding a settled payment posts the reversal revenue→wallet, stamps
// refunded_at, claws back the accrued loyalty points, and publishes
// fare.payment.refunded.
func TestRefundPayment_Settled(t *testing.T) {
	pool, err := pgxmock.NewPool()
	if err != nil {
		t.Fatalf("pgxmock.NewPool: %v", err)
	}
	defer pool.Close()
	led, pub := &fakeLedger{}, &fakePublisher{}
	h := &Handler{db: pool, ledger: led, pub: pub, log: zap.NewExample()}

	pool.ExpectQuery(`SELECT .* FROM commerce\.fare_payments WHERE id = \$1`).
		WithArgs("pay-1").
		WillReturnRows(paymentRow("pay-1", "rider-a", "k-1", "settled"))
	pool.ExpectBegin()
	pool.ExpectExec(`INSERT INTO commerce\.rider_accounts`).WithArgs("rider-a").
		WillReturnResult(pgxmock.NewResult("INSERT", 1))
	pool.ExpectQuery(`SELECT account_id FROM commerce\.rider_accounts`).WithArgs("rider-a").
		WillReturnRows(pgxmock.NewRows([]string{"account_id"}).AddRow(uint64(1001)))
	pool.ExpectCommit()
	pool.ExpectQuery(`UPDATE commerce\.fare_payments SET status = 'refunded'`).
		WithArgs("pay-1").
		WillReturnRows(paymentRow("pay-1", "rider-a", "k-1", "refunded"))
	// Loyalty clawback: −5 points, idempotent on refund:pay-1.
	pool.ExpectBegin()
	pool.ExpectExec(`INSERT INTO commerce\.loyalty_ledger`).
		WithArgs(pgxmock.AnyArg(), "rider-a", int64(-5), "refund:pay-1").
		WillReturnResult(pgxmock.NewResult("INSERT", 1))
	pool.ExpectExec(`UPDATE commerce\.loyalty_accounts`).
		WithArgs("rider-a", int64(5)).
		WillReturnResult(pgxmock.NewResult("UPDATE", 1))
	pool.ExpectCommit()

	r := chi.NewRouter()
	r.Post("/v1/payments/{id}/refund", h.RefundPayment)
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, withClaims(httptest.NewRequest(http.MethodPost, "/v1/payments/pay-1/refund", nil), "ops-1", "operator"))

	if rec.Code != http.StatusOK {
		t.Fatalf("got %d, want 200 (body: %s)", rec.Code, rec.Body)
	}
	if led.count() != 1 {
		t.Fatalf("want exactly 1 reversal transfer, got %d", led.count())
	}
	tr := led.transfers[0]
	if tr.debit != ledger.OperatorRevenueAccount || tr.credit != 1001 || tr.amount != 500 {
		t.Fatalf("refund must reverse revenue→wallet, got %+v", tr)
	}
	topics := pub.published()
	if len(topics) != 1 || topics[0] != "fare.payment.refunded" {
		t.Fatalf("published topics %v, want [fare.payment.refunded]", topics)
	}
	if err := pool.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet db expectations: %v", err)
	}
}

// Only settled payments are refundable.
func TestRefundPayment_NotSettled(t *testing.T) {
	pool, err := pgxmock.NewPool()
	if err != nil {
		t.Fatalf("pgxmock.NewPool: %v", err)
	}
	defer pool.Close()
	h := &Handler{db: pool, ledger: &fakeLedger{}, pub: &fakePublisher{}, log: zap.NewExample()}

	pool.ExpectQuery(`SELECT .* FROM commerce\.fare_payments WHERE id = \$1`).
		WithArgs("pay-2").
		WillReturnRows(paymentRow("pay-2", "rider-a", "k-2", "failed"))

	r := chi.NewRouter()
	r.Post("/v1/payments/{id}/refund", h.RefundPayment)
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, withClaims(httptest.NewRequest(http.MethodPost, "/v1/payments/pay-2/refund", nil), "ops-1", "operator"))

	if rec.Code != http.StatusConflict {
		t.Fatalf("got %d, want 409 (body: %s)", rec.Code, rec.Body)
	}
	if err := pool.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet db expectations: %v", err)
	}
}

// --- ad placements & budget tracking ----------------------------------------

// committed (800) + requested (300) > budget (1000) → 409 budget_exceeded.
func TestCreateAdPlacement_BudgetExceeded(t *testing.T) {
	pool, err := pgxmock.NewPool()
	if err != nil {
		t.Fatalf("pgxmock.NewPool: %v", err)
	}
	defer pool.Close()
	h := &Handler{db: pool, log: zap.NewExample()}

	pool.ExpectBegin()
	pool.ExpectQuery(`SELECT status, COALESCE\(budget_minor,0\) FROM commerce\.ad_campaigns`).
		WithArgs("camp-1").
		WillReturnRows(pgxmock.NewRows([]string{"status", "budget"}).AddRow("active", int64(1000)))
	pool.ExpectQuery(`SELECT active FROM commerce\.ad_inventory`).
		WithArgs("inv-1").
		WillReturnRows(pgxmock.NewRows([]string{"active"}).AddRow(true))
	pool.ExpectQuery(`SELECT COALESCE\(sum\(cost_minor\),0\) FROM commerce\.ad_placements`).
		WithArgs("camp-1").
		WillReturnRows(pgxmock.NewRows([]string{"sum"}).AddRow(int64(800)))
	pool.ExpectRollback()

	body := `{"campaign_id":"camp-1","inventory_id":"inv-1","starts_at":"2026-08-01T00:00:00Z","ends_at":"2026-08-31T00:00:00Z","cost_minor":300}`
	rec := httptest.NewRecorder()
	h.CreateAdPlacement(rec, withClaims(httptest.NewRequest(http.MethodPost, "/v1/ads/placements", strings.NewReader(body)), "ops-1", "operator"))

	if rec.Code != http.StatusConflict {
		t.Fatalf("got %d, want 409 (body: %s)", rec.Code, rec.Body)
	}
	if !strings.Contains(rec.Body.String(), "budget_exceeded") {
		t.Fatalf("body should carry budget_exceeded: %s", rec.Body)
	}
	if err := pool.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet db expectations: %v", err)
	}
}

// A placement within budget is booked and the campaign reports committed /
// remaining spend.
func TestCreateAdPlacement_WithinBudget(t *testing.T) {
	pool, err := pgxmock.NewPool()
	if err != nil {
		t.Fatalf("pgxmock.NewPool: %v", err)
	}
	defer pool.Close()
	h := &Handler{db: pool, log: zap.NewExample()}

	created := time.Date(2026, 7, 25, 12, 0, 0, 0, time.UTC)
	starts := time.Date(2026, 8, 1, 0, 0, 0, 0, time.UTC)
	ends := time.Date(2026, 8, 31, 0, 0, 0, 0, time.UTC)
	pool.ExpectBegin()
	pool.ExpectQuery(`SELECT status, COALESCE\(budget_minor,0\) FROM commerce\.ad_campaigns`).
		WithArgs("camp-1").
		WillReturnRows(pgxmock.NewRows([]string{"status", "budget"}).AddRow("active", int64(1000)))
	pool.ExpectQuery(`SELECT active FROM commerce\.ad_inventory`).
		WithArgs("inv-1").
		WillReturnRows(pgxmock.NewRows([]string{"active"}).AddRow(true))
	pool.ExpectQuery(`SELECT COALESCE\(sum\(cost_minor\),0\) FROM commerce\.ad_placements`).
		WithArgs("camp-1").
		WillReturnRows(pgxmock.NewRows([]string{"sum"}).AddRow(int64(100)))
	pool.ExpectQuery(`INSERT INTO commerce\.ad_placements`).
		WithArgs("camp-1", "inv-1", starts, ends, int64(300)).
		WillReturnRows(pgxmock.NewRows([]string{"id", "campaign_id", "inventory_id", "starts_at", "ends_at", "cost_minor", "created_at"}).
			AddRow("pl-1", "camp-1", "inv-1", starts, ends, int64(300), created))
	pool.ExpectCommit()

	body := `{"campaign_id":"camp-1","inventory_id":"inv-1","starts_at":"2026-08-01T00:00:00Z","ends_at":"2026-08-31T00:00:00Z","cost_minor":300}`
	rec := httptest.NewRecorder()
	h.CreateAdPlacement(rec, withClaims(httptest.NewRequest(http.MethodPost, "/v1/ads/placements", strings.NewReader(body)), "ops-1", "operator"))

	if rec.Code != http.StatusCreated {
		t.Fatalf("got %d, want 201 (body: %s)", rec.Code, rec.Body)
	}
	var p AdPlacement
	if err := json.Unmarshal(rec.Body.Bytes(), &p); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if p.CostMinor != 300 || p.CampaignID != "camp-1" {
		t.Fatalf("unexpected placement: %+v", p)
	}
	if err := pool.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet db expectations: %v", err)
	}
}
