package handlers

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	jwt "github.com/golang-jwt/jwt/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/pashagolub/pgxmock/v4"
	tb_types "github.com/tigerbeetle/tigerbeetle-go/pkg/types"
	"go.uber.org/zap"

	auth "github.com/munisp/hydrogenTransport/packages/go-auth"
	"github.com/munisp/hydrogenTransport/services/go/commerce-api/internal/ledger"
)

// --- test doubles -----------------------------------------------------------

type fakeLedger struct {
	mu        sync.Mutex
	transfers []fakeTransfer
	err       error
}

type fakeTransfer struct {
	debit, credit, amount uint64
	code                  uint16
}

func (l *fakeLedger) EnsureAccount(id uint64, code uint16) error { return nil }
func (l *fakeLedger) Transfer(_ tb_types.Uint128, debit, credit, amount uint64, code uint16) (string, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.err != nil {
		return "", l.err
	}
	l.transfers = append(l.transfers, fakeTransfer{debit, credit, amount, code})
	return "000000000000000000000000deadbeef", nil
}
func (l *fakeLedger) Close() {}
func (l *fakeLedger) count() int {
	l.mu.Lock()
	defer l.mu.Unlock()
	return len(l.transfers)
}

type fakePublisher struct {
	mu     sync.Mutex
	topics []string
}

func (p *fakePublisher) Publish(_ context.Context, topic string, _ any) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.topics = append(p.topics, topic)
	return nil
}
func (p *fakePublisher) Close() {}
func (p *fakePublisher) published() []string {
	p.mu.Lock()
	defer p.mu.Unlock()
	return append([]string(nil), p.topics...)
}

// withClaims injects validated JWT claims as the auth middleware would.
func withClaims(r *http.Request, sub string, roles ...string) *http.Request {
	claims := jwt.MapClaims{"sub": sub}
	if len(roles) > 0 {
		rs := make([]any, len(roles))
		for i, role := range roles {
			rs[i] = role
		}
		claims["realm_access"] = map[string]any{"roles": rs}
	}
	return r.WithContext(context.WithValue(r.Context(), auth.ClaimsKey, claims))
}

func paymentRow(id, rider, key, status string) *pgxmock.Rows {
	return pgxmock.NewRows([]string{
		"id", "rider_sub", "amount_minor", "charged_minor", "currency", "mojaloop_transfer_id",
		"tb_transfer_id", "idempotency_key", "status", "created_at",
	}).AddRow(id, rider, int64(500), nil, "EUR", nil, nil, &key, status,
		time.Date(2026, 7, 24, 9, 0, 0, 0, time.UTC))
}

func createRequest(t *testing.T, body, idemKey, sub string) *http.Request {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/v1/payments", strings.NewReader(body))
	if idemKey != "" {
		req.Header.Set("Idempotency-Key", idemKey)
	}
	if sub != "" {
		req = withClaims(req, sub)
	}
	return req
}

// --- negative path ----------------------------------------------------------

// SPEC/payment integrity: POST /v1/payments without Idempotency-Key → 400,
// before any database or ledger interaction.
func TestCreatePayment_MissingIdempotencyKey(t *testing.T) {
	h := &Handler{log: zap.NewExample()} // no db/ledger: must not be reached
	rec := httptest.NewRecorder()

	h.CreatePayment("")(rec, createRequest(t, `{"amount_minor":500}`, "", "rider-a"))

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("got %d, want 400 (body: %s)", rec.Code, rec.Body)
	}
	if !strings.Contains(rec.Body.String(), "Idempotency-Key") {
		t.Fatalf("error body should name the missing header: %s", rec.Body)
	}
}

func TestCreatePayment_InvalidAmount(t *testing.T) {
	h := &Handler{log: zap.NewExample()}
	rec := httptest.NewRecorder()

	h.CreatePayment("")(rec, createRequest(t, `{"amount_minor":0}`, "k-1", "rider-a"))

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("got %d, want 400 (body: %s)", rec.Code, rec.Body)
	}
}

// --- P0-1: rider identity comes from the JWT, never the body ----------------

// SECURITY (P0-1): a client-supplied rider_sub that does not match the JWT
// subject is a wallet-spoofing attempt → 403 before any DB/ledger work.
func TestCreatePayment_RiderSubMismatchRejected(t *testing.T) {
	h := &Handler{log: zap.NewExample()} // no db/ledger: must not be reached
	rec := httptest.NewRecorder()

	h.CreatePayment("")(rec, createRequest(t,
		`{"amount_minor":100000,"currency":"EUR","rider_sub":"victim-sub"}`, "atk-001", "mallory"))

	if rec.Code != http.StatusForbidden {
		t.Fatalf("got %d, want 403 (body: %s)", rec.Code, rec.Body)
	}
	if !strings.Contains(rec.Body.String(), "rider_sub") {
		t.Fatalf("error body should name rider_sub: %s", rec.Body)
	}
}

// SECURITY (P0-1): a body rider_sub that MATCHES the JWT subject is accepted
// (backwards compatibility) and the payment is owned by the caller.
func TestCreatePayment_RiderSubMatchingSubjectAccepted(t *testing.T) {
	pool, err := pgxmock.NewPool()
	if err != nil {
		t.Fatalf("pgxmock.NewPool: %v", err)
	}
	defer pool.Close()
	led, pub := &fakeLedger{}, &fakePublisher{}
	h := &Handler{db: pool, ledger: led, pub: pub, log: zap.NewExample()}

	pool.ExpectQuery(`sum\(COALESCE\(charged_minor`).
		WithArgs("rider-a").
		WillReturnRows(pgxmock.NewRows([]string{"sum"}).AddRow(int64(0)))
	pool.ExpectExec(`INSERT INTO commerce\.fare_payments`).
		WithArgs(pgxmock.AnyArg(), "rider-a", int64(500), int64(500), "EUR", "idem-match").
		WillReturnResult(pgxmock.NewResult("INSERT", 1))
	pool.ExpectBegin()
	pool.ExpectExec(`INSERT INTO commerce\.rider_accounts`).
		WithArgs("rider-a").
		WillReturnResult(pgxmock.NewResult("INSERT", 1))
	pool.ExpectQuery(`SELECT account_id FROM commerce\.rider_accounts`).
		WithArgs("rider-a").
		WillReturnRows(pgxmock.NewRows([]string{"account_id"}).AddRow(uint64(1001)))
	pool.ExpectCommit()
	pool.ExpectQuery(`UPDATE commerce\.fare_payments`).
		WithArgs(pgxmock.AnyArg(), "settled", pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(paymentRow("pay-new", "rider-a", "idem-match", "settled"))
	// Loyalty accrual on the settled payment (500 cents → 5 points).
	pool.ExpectBegin()
	pool.ExpectExec(`INSERT INTO commerce\.loyalty_ledger`).
		WithArgs(pgxmock.AnyArg(), "rider-a", int64(5), pgxmock.AnyArg()).
		WillReturnResult(pgxmock.NewResult("INSERT", 1))
	pool.ExpectExec(`INSERT INTO commerce\.loyalty_accounts`).
		WithArgs("rider-a", int64(5)).
		WillReturnResult(pgxmock.NewResult("INSERT", 1))
	pool.ExpectCommit()

	rec := httptest.NewRecorder()
	h.CreatePayment("")(rec, createRequest(t,
		`{"amount_minor":500,"currency":"EUR","rider_sub":"rider-a"}`, "idem-match", "rider-a"))

	if rec.Code != http.StatusCreated {
		t.Fatalf("got %d, want 201 (body: %s)", rec.Code, rec.Body)
	}
	var p Payment
	if err := json.Unmarshal(rec.Body.Bytes(), &p); err != nil {
		t.Fatalf("decode body: %v", err)
	}
	if p.RiderSub != "rider-a" {
		t.Fatalf("payment must be owned by the JWT subject, got %+v", p)
	}
	if err := pool.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet db expectations: %v", err)
	}
}

// --- unfunded wallet → 402 ---------------------------------------------------

// A payment from a provisioned-but-unfunded wallet fails cleanly: 402 with a
// machine-readable error code, the row is recorded as failed, and
// fare.payment.failed is still published (never a panic, never negative).
func TestCreatePayment_InsufficientFunds(t *testing.T) {
	pool, err := pgxmock.NewPool()
	if err != nil {
		t.Fatalf("pgxmock.NewPool: %v", err)
	}
	defer pool.Close()
	pub := &fakePublisher{}
	led := &fakeLedger{err: fmt.Errorf("debit account 1001: %w", ledger.ErrInsufficientFunds)}
	h := &Handler{db: pool, ledger: led, pub: pub, log: zap.NewExample()}

	pool.ExpectQuery(`sum\(COALESCE\(charged_minor`).
		WithArgs("rider-a").
		WillReturnRows(pgxmock.NewRows([]string{"sum"}).AddRow(int64(0)))
	pool.ExpectExec(`INSERT INTO commerce\.fare_payments`).
		WithArgs(pgxmock.AnyArg(), "rider-a", int64(500), int64(500), "EUR", "idem-broke").
		WillReturnResult(pgxmock.NewResult("INSERT", 1))
	pool.ExpectBegin()
	pool.ExpectExec(`INSERT INTO commerce\.rider_accounts`).
		WithArgs("rider-a").
		WillReturnResult(pgxmock.NewResult("INSERT", 1))
	pool.ExpectQuery(`SELECT account_id FROM commerce\.rider_accounts`).
		WithArgs("rider-a").
		WillReturnRows(pgxmock.NewRows([]string{"account_id"}).AddRow(uint64(1001)))
	pool.ExpectCommit()
	pool.ExpectQuery(`UPDATE commerce\.fare_payments`).
		WithArgs(pgxmock.AnyArg(), "failed", pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(paymentRow("pay-broke", "rider-a", "idem-broke", "failed"))

	rec := httptest.NewRecorder()
	h.CreatePayment("")(rec, createRequest(t, `{"amount_minor":500,"currency":"EUR"}`, "idem-broke", "rider-a"))

	if rec.Code != http.StatusPaymentRequired {
		t.Fatalf("got %d, want 402 (body: %s)", rec.Code, rec.Body)
	}
	if !strings.Contains(rec.Body.String(), "insufficient_funds") {
		t.Fatalf("error body should carry the insufficient_funds code: %s", rec.Body)
	}
	topics := pub.published()
	if len(topics) != 2 || topics[0] != "fare.payment.initiated" || topics[1] != "fare.payment.failed" {
		t.Fatalf("published topics %v, want [fare.payment.initiated fare.payment.failed]", topics)
	}
	if err := pool.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet db expectations: %v", err)
	}
}

// --- idempotent replay ------------------------------------------------------

// Replaying the same Idempotency-Key returns the SAME payment (200, same id)
// and performs exactly one INSERT attempt — no duplicate row, no ledger
// transfer, no events.
func TestCreatePayment_IdempotentReplay(t *testing.T) {
	pool, err := pgxmock.NewPool()
	if err != nil {
		t.Fatalf("pgxmock.NewPool: %v", err)
	}
	defer pool.Close()
	led, pub := &fakeLedger{}, &fakePublisher{}
	h := &Handler{db: pool, ledger: led, pub: pub, log: zap.NewExample()}

	pool.ExpectQuery(`sum\(COALESCE\(charged_minor`).
		WithArgs("rider-a").
		WillReturnRows(pgxmock.NewRows([]string{"sum"}).AddRow(int64(0)))
	pool.ExpectExec(`INSERT INTO commerce\.fare_payments`).
		WithArgs(pgxmock.AnyArg(), "rider-a", int64(500), int64(500), "EUR", "idem-123").
		WillReturnError(&pgconn.PgError{Code: "23505"}) // unique violation on idempotency_key
	pool.ExpectQuery(`WHERE idempotency_key = \$1`).
		WithArgs("idem-123").
		WillReturnRows(paymentRow("pay-existing", "rider-a", "idem-123", "settled"))

	rec := httptest.NewRecorder()
	h.CreatePayment("")(rec, createRequest(t, `{"amount_minor":500,"currency":"EUR"}`, "idem-123", "rider-a"))

	if rec.Code != http.StatusOK {
		t.Fatalf("replay: got %d, want 200 (body: %s)", rec.Code, rec.Body)
	}
	var p Payment
	if err := json.Unmarshal(rec.Body.Bytes(), &p); err != nil {
		t.Fatalf("decode body: %v", err)
	}
	if p.ID != "pay-existing" || p.Status != "settled" || p.RiderSub != "rider-a" {
		t.Fatalf("replay must return the original payment, got %+v", p)
	}
	if led.count() != 0 {
		t.Fatalf("replay must not post another ledger transfer (%d posted)", led.count())
	}
	if n := len(pub.published()); n != 0 {
		t.Fatalf("replay must not republish events (%d published)", n)
	}
	if err := pool.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet db expectations: %v", err)
	}
}

// Idempotency keys are owner-scoped: replaying someone else's key must not
// leak their payment — 404 (existence not leaked).
func TestCreatePayment_ReplayScopedToOwner(t *testing.T) {
	pool, err := pgxmock.NewPool()
	if err != nil {
		t.Fatalf("pgxmock.NewPool: %v", err)
	}
	defer pool.Close()
	h := &Handler{db: pool, ledger: &fakeLedger{}, pub: &fakePublisher{}, log: zap.NewExample()}

	pool.ExpectQuery(`sum\(COALESCE\(charged_minor`).
		WithArgs("mallory").
		WillReturnRows(pgxmock.NewRows([]string{"sum"}).AddRow(int64(0)))
	pool.ExpectExec(`INSERT INTO commerce\.fare_payments`).
		WithArgs(pgxmock.AnyArg(), "mallory", int64(500), int64(500), "EUR", "idem-123").
		WillReturnError(&pgconn.PgError{Code: "23505"})
	pool.ExpectQuery(`WHERE idempotency_key = \$1`).
		WithArgs("idem-123").
		WillReturnRows(paymentRow("pay-existing", "rider-a", "idem-123", "settled"))

	rec := httptest.NewRecorder()
	h.CreatePayment("")(rec, createRequest(t, `{"amount_minor":500,"currency":"EUR"}`, "idem-123", "mallory"))

	if rec.Code != http.StatusNotFound {
		t.Fatalf("cross-owner replay: got %d, want 404 (body: %s)", rec.Code, rec.Body)
	}
	if err := pool.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet db expectations: %v", err)
	}
}

// --- ownership scoping on reads ---------------------------------------------

func TestGetPayment_OwnershipScoping(t *testing.T) {
	newPool := func(t *testing.T) pgxmock.PgxPoolIface {
		pool, err := pgxmock.NewPool()
		if err != nil {
			t.Fatalf("pgxmock.NewPool: %v", err)
		}
		t.Cleanup(pool.Close)
		pool.ExpectQuery(`FROM commerce\.fare_payments WHERE id = \$1`).
			WithArgs("pay-1").
			WillReturnRows(paymentRow("pay-1", "rider-b", "k-9", "settled"))
		return pool
	}
	serve := func(t *testing.T, pool pgxmock.PgxPoolIface, sub string, roles ...string) *httptest.ResponseRecorder {
		h := &Handler{db: pool, log: zap.NewExample()}
		r := chi.NewRouter()
		r.Get("/v1/payments/{id}", h.GetPayment)
		req := withClaims(httptest.NewRequest(http.MethodGet, "/v1/payments/pay-1", nil), sub, roles...)
		rec := httptest.NewRecorder()
		r.ServeHTTP(rec, req)
		return rec
	}

	t.Run("non-owner gets 404", func(t *testing.T) {
		pool := newPool(t)
		if rec := serve(t, pool, "rider-a"); rec.Code != http.StatusNotFound {
			t.Fatalf("caller A reading B's payment: got %d, want 404", rec.Code)
		}
		if err := pool.ExpectationsWereMet(); err != nil {
			t.Fatalf("unmet db expectations: %v", err)
		}
	})

	t.Run("owner gets 200", func(t *testing.T) {
		pool := newPool(t)
		rec := serve(t, pool, "rider-b")
		if rec.Code != http.StatusOK {
			t.Fatalf("owner reading own payment: got %d, want 200 (body: %s)", rec.Code, rec.Body)
		}
		var p Payment
		if err := json.Unmarshal(rec.Body.Bytes(), &p); err != nil {
			t.Fatalf("decode body: %v", err)
		}
		if p.ID != "pay-1" || p.RiderSub != "rider-b" {
			t.Fatalf("wrong payment returned: %+v", p)
		}
		if err := pool.ExpectationsWereMet(); err != nil {
			t.Fatalf("unmet db expectations: %v", err)
		}
	})

	t.Run("platform-admin may read any payment", func(t *testing.T) {
		pool := newPool(t)
		if rec := serve(t, pool, "ops-1", "platform-admin"); rec.Code != http.StatusOK {
			t.Fatalf("admin reading payment: got %d, want 200", rec.Code)
		}
		if err := pool.ExpectationsWereMet(); err != nil {
			t.Fatalf("unmet db expectations: %v", err)
		}
	})
}

// --- happy path -------------------------------------------------------------

// A new payment flows insert → rider wallet account → deterministic ledger
// transfer → status update → events, and returns 201 with the settled row.
func TestCreatePayment_Settled(t *testing.T) {
	pool, err := pgxmock.NewPool()
	if err != nil {
		t.Fatalf("pgxmock.NewPool: %v", err)
	}
	defer pool.Close()
	led, pub := &fakeLedger{}, &fakePublisher{}
	h := &Handler{db: pool, ledger: led, pub: pub, log: zap.NewExample()}

	pool.ExpectQuery(`sum\(COALESCE\(charged_minor`).
		WithArgs("rider-a").
		WillReturnRows(pgxmock.NewRows([]string{"sum"}).AddRow(int64(0)))
	pool.ExpectExec(`INSERT INTO commerce\.fare_payments`).
		WithArgs(pgxmock.AnyArg(), "rider-a", int64(500), int64(500), "EUR", "idem-happy").
		WillReturnResult(pgxmock.NewResult("INSERT", 1))
	pool.ExpectBegin()
	pool.ExpectExec(`INSERT INTO commerce\.rider_accounts`).
		WithArgs("rider-a").
		WillReturnResult(pgxmock.NewResult("INSERT", 1))
	pool.ExpectQuery(`SELECT account_id FROM commerce\.rider_accounts`).
		WithArgs("rider-a").
		WillReturnRows(pgxmock.NewRows([]string{"account_id"}).AddRow(uint64(1001)))
	pool.ExpectCommit()
	pool.ExpectQuery(`UPDATE commerce\.fare_payments`).
		WithArgs(pgxmock.AnyArg(), "settled", pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(paymentRow("pay-new", "rider-a", "idem-happy", "settled"))
	// Loyalty accrual on settle: 500 cents → 5 points, idempotent via
	// loyalty_ledger.ref_id = payment id.
	pool.ExpectBegin()
	pool.ExpectExec(`INSERT INTO commerce\.loyalty_ledger`).
		WithArgs(pgxmock.AnyArg(), "rider-a", int64(5), pgxmock.AnyArg()).
		WillReturnResult(pgxmock.NewResult("INSERT", 1))
	pool.ExpectExec(`INSERT INTO commerce\.loyalty_accounts`).
		WithArgs("rider-a", int64(5)).
		WillReturnResult(pgxmock.NewResult("INSERT", 1))
	pool.ExpectCommit()

	rec := httptest.NewRecorder()
	h.CreatePayment("")(rec, createRequest(t, `{"amount_minor":500,"currency":"EUR"}`, "idem-happy", "rider-a"))

	if rec.Code != http.StatusCreated {
		t.Fatalf("got %d, want 201 (body: %s)", rec.Code, rec.Body)
	}
	var p Payment
	if err := json.Unmarshal(rec.Body.Bytes(), &p); err != nil {
		t.Fatalf("decode body: %v", err)
	}
	if p.ID != "pay-new" || p.Status != "settled" {
		t.Fatalf("unexpected payment: %+v", p)
	}
	if led.count() != 1 {
		t.Fatalf("want exactly 1 ledger transfer, got %d", led.count())
	}
	tr := led.transfers[0]
	if tr.debit != 1001 || tr.credit != 2001 || tr.amount != 500 || tr.code != 100 {
		t.Fatalf("ledger transfer wrong: %+v", tr)
	}
	topics := pub.published()
	if len(topics) != 2 || topics[0] != "fare.payment.initiated" || topics[1] != "fare.payment.settled" {
		t.Fatalf("published topics %v, want [fare.payment.initiated fare.payment.settled]", topics)
	}
	if err := pool.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet db expectations: %v", err)
	}
}
