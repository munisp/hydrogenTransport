package handlers

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"
	"github.com/pashagolub/pgxmock/v4"
	"go.uber.org/zap"
)

// --- Wave-6 A2-03: currency validation ---------------------------------------

// A payment denominated in anything but PLATFORM_CURRENCY is rejected 422
// before any DB/ledger work — the single-currency ledger must never
// silently mis-post foreign minor units.
func TestCreatePayment_ForeignCurrencyRejected(t *testing.T) {
	h := &Handler{log: zap.NewExample()} // no db/ledger: must not be reached
	rec := httptest.NewRecorder()

	h.CreatePayment("")(rec, createRequest(t, `{"amount_minor":500,"currency":"USD"}`, "k-fx", "rider-a"))

	if rec.Code != http.StatusUnprocessableEntity {
		t.Fatalf("got %d, want 422 (body: %s)", rec.Code, rec.Body)
	}
	if !strings.Contains(rec.Body.String(), "USD") {
		t.Fatalf("error body should name the rejected currency: %s", rec.Body)
	}
}

// --- Wave-6 A2-01: entitlement resolution ------------------------------------

// An active monthly pass covers the ride entirely: charge 0, no ledger
// posting, settled and recorded with the requested fare for statistics.
func TestCreatePayment_PassCoversRide(t *testing.T) {
	pool, err := pgxmock.NewPool()
	if err != nil {
		t.Fatalf("pgxmock.NewPool: %v", err)
	}
	defer pool.Close()
	led, pub := &fakeLedger{}, &fakePublisher{}
	h := &Handler{db: pool, ledger: led, pub: pub, log: zap.NewExample()}

	pool.ExpectBegin()
	pool.ExpectExec(`pg_advisory_xact_lock`).WithArgs("rider-a").
		WillReturnResult(pgxmock.NewResult("SELECT", 1))
	pool.ExpectQuery(`FROM commerce\.rider_entitlements`).WithArgs("rider-a").
		WillReturnRows(pgxmock.NewRows([]string{"kind", "discount_pct", "code"}).
			AddRow("pass", 0, "monthly-pass"))
	// charge == 0 after the pass → no cap query, no ledger, no account.
	pool.ExpectExec(`INSERT INTO commerce\.fare_payments`).
		WithArgs(pgxmock.AnyArg(), "rider-a", int64(500), int64(0), "EUR", "idem-pass").
		WillReturnResult(pgxmock.NewResult("INSERT", 1))
	pool.ExpectQuery(`UPDATE commerce\.fare_payments`).
		WithArgs(pgxmock.AnyArg(), "settled", pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(paymentRow("pay-pass", "rider-a", "idem-pass", "settled"))
	pool.ExpectCommit()

	rec := httptest.NewRecorder()
	h.CreatePayment("")(rec, createRequest(t, `{"amount_minor":500,"currency":"EUR"}`, "idem-pass", "rider-a"))

	if rec.Code != http.StatusCreated {
		t.Fatalf("got %d, want 201 (body: %s)", rec.Code, rec.Body)
	}
	if led.count() != 0 {
		t.Fatalf("a pass-covered ride must not post a ledger transfer, got %d", led.count())
	}
	if err := pool.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet db expectations: %v", err)
	}
}

// An active 50 % student concession halves the charge before the cap.
func TestCreatePayment_DiscountEntitlementHalvesFare(t *testing.T) {
	pool, err := pgxmock.NewPool()
	if err != nil {
		t.Fatalf("pgxmock.NewPool: %v", err)
	}
	defer pool.Close()
	led, pub := &fakeLedger{}, &fakePublisher{}
	h := &Handler{db: pool, ledger: led, pub: pub, log: zap.NewExample()}

	pool.ExpectBegin()
	pool.ExpectExec(`pg_advisory_xact_lock`).WithArgs("rider-a").
		WillReturnResult(pgxmock.NewResult("SELECT", 1))
	pool.ExpectQuery(`FROM commerce\.rider_entitlements`).WithArgs("rider-a").
		WillReturnRows(pgxmock.NewRows([]string{"kind", "discount_pct", "code"}).
			AddRow("discount", 50, "student-50"))
	pool.ExpectQuery(`sum\(COALESCE\(charged_minor`).
		WithArgs("rider-a", "UTC").
		WillReturnRows(pgxmock.NewRows([]string{"sum"}).AddRow(int64(0)))
	pool.ExpectExec(`INSERT INTO commerce\.fare_payments`).
		WithArgs(pgxmock.AnyArg(), "rider-a", int64(500), int64(250), "EUR", "idem-stud").
		WillReturnResult(pgxmock.NewResult("INSERT", 1))
	pool.ExpectBegin()
	pool.ExpectExec(`INSERT INTO commerce\.rider_accounts`).WithArgs("rider-a").
		WillReturnResult(pgxmock.NewResult("INSERT", 1))
	pool.ExpectQuery(`SELECT account_id FROM commerce\.rider_accounts`).WithArgs("rider-a").
		WillReturnRows(pgxmock.NewRows([]string{"account_id"}).AddRow(uint64(1001)))
	pool.ExpectCommit()
	pool.ExpectQuery(`UPDATE commerce\.fare_payments`).
		WithArgs(pgxmock.AnyArg(), "settled", pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(paymentRow("pay-stud", "rider-a", "idem-stud", "settled"))
	pool.ExpectCommit()

	rec := httptest.NewRecorder()
	h.CreatePayment("")(rec, createRequest(t, `{"amount_minor":500,"currency":"EUR"}`, "idem-stud", "rider-a"))

	if rec.Code != http.StatusCreated {
		t.Fatalf("got %d, want 201 (body: %s)", rec.Code, rec.Body)
	}
	if led.count() != 1 || led.transfers[0].amount != 250 {
		t.Fatalf("ledger must charge the discounted €2.50, got %+v", led.transfers)
	}
	if err := pool.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet db expectations: %v", err)
	}
}

// --- Wave-6 A2-02: partial refunds -------------------------------------------

var charged500 = int64(500)

// refundRow is paymentRow with a charged amount and a refunded_minor
// accumulator (partial-refund scenarios).
func refundRow(id, rider, key, status string, refunded int64) *pgxmock.Rows {
	return pgxmock.NewRows([]string{
		"id", "rider_sub", "amount_minor", "charged_minor", "currency", "mojaloop_transfer_id",
		"tb_transfer_id", "idempotency_key", "status", "refunded_minor", "created_at",
	}).AddRow(id, rider, int64(500), &charged500, "EUR", nil, nil, &key, status, refunded,
		paymentRowTime)
}

// A €2.00 partial refund of a €5.00 payment posts a 200-minor reversal,
// leaves the payment 'partially_refunded' with a 300-minor remainder, and
// claws back the proportional loyalty points.
func TestRefundPayment_Partial(t *testing.T) {
	pool, err := pgxmock.NewPool()
	if err != nil {
		t.Fatalf("pgxmock.NewPool: %v", err)
	}
	defer pool.Close()
	led, pub := &fakeLedger{}, &fakePublisher{}
	h := &Handler{db: pool, ledger: led, pub: pub, log: zap.NewExample()}

	pool.ExpectQuery(`SELECT .* FROM commerce\.fare_payments WHERE id = \$1`).
		WithArgs("pay-p").
		WillReturnRows(refundRow("pay-p", "rider-a", "k-p", "settled", 0))
	pool.ExpectBegin()
	pool.ExpectExec(`INSERT INTO commerce\.rider_accounts`).WithArgs("rider-a").
		WillReturnResult(pgxmock.NewResult("INSERT", 1))
	pool.ExpectQuery(`SELECT account_id FROM commerce\.rider_accounts`).WithArgs("rider-a").
		WillReturnRows(pgxmock.NewRows([]string{"account_id"}).AddRow(uint64(1001)))
	pool.ExpectCommit()
	pool.ExpectQuery(`UPDATE commerce\.fare_payments`).
		WithArgs("pay-p", int64(200), "partially_refunded", int64(0)).
		WillReturnRows(refundRow("pay-p", "rider-a", "k-p", "partially_refunded", 200))
	// Proportional clawback: 200 minor → 2 points, per-step ref refund:pay-p:200.
	pool.ExpectBegin()
	pool.ExpectExec(`INSERT INTO commerce\.loyalty_ledger`).
		WithArgs(pgxmock.AnyArg(), "rider-a", int64(-2), "refund:pay-p:200").
		WillReturnResult(pgxmock.NewResult("INSERT", 1))
	pool.ExpectExec(`UPDATE commerce\.loyalty_accounts`).
		WithArgs("rider-a", int64(2)).
		WillReturnResult(pgxmock.NewResult("UPDATE", 1))
	pool.ExpectCommit()

	r := chi.NewRouter()
	r.Post("/v1/payments/{id}/refund", h.RefundPayment)
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, withClaims(httptest.NewRequest(http.MethodPost, "/v1/payments/pay-p/refund",
		strings.NewReader(`{"amount_minor":200}`)), "ops-1", "operator"))

	if rec.Code != http.StatusOK {
		t.Fatalf("got %d, want 200 (body: %s)", rec.Code, rec.Body)
	}
	if led.count() != 1 || led.transfers[0].amount != 200 {
		t.Fatalf("partial refund must reverse exactly €2.00, got %+v", led.transfers)
	}
	var p Payment
	if err := json.Unmarshal(rec.Body.Bytes(), &p); err != nil {
		t.Fatalf("decode body: %v", err)
	}
	if p.Status != "partially_refunded" || p.RefundedMinor != 200 {
		t.Fatalf("expected partially_refunded with 200 refunded, got %+v", p)
	}
	if err := pool.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet db expectations: %v", err)
	}
}

// A partial refund larger than the refundable remainder is rejected 422
// before any ledger movement.
func TestRefundPayment_PartialExceedsRemainder(t *testing.T) {
	pool, err := pgxmock.NewPool()
	if err != nil {
		t.Fatalf("pgxmock.NewPool: %v", err)
	}
	defer pool.Close()
	h := &Handler{db: pool, ledger: &fakeLedger{}, pub: &fakePublisher{}, log: zap.NewExample()}

	pool.ExpectQuery(`SELECT .* FROM commerce\.fare_payments WHERE id = \$1`).
		WithArgs("pay-q").
		WillReturnRows(refundRow("pay-q", "rider-a", "k-q", "partially_refunded", 400))

	r := chi.NewRouter()
	r.Post("/v1/payments/{id}/refund", h.RefundPayment)
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, withClaims(httptest.NewRequest(http.MethodPost, "/v1/payments/pay-q/refund",
		strings.NewReader(`{"amount_minor":200}`)), "ops-1", "operator"))

	if rec.Code != http.StatusUnprocessableEntity {
		t.Fatalf("got %d, want 422 (body: %s)", rec.Code, rec.Body)
	}
	if err := pool.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet db expectations: %v", err)
	}
}
