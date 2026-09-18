package handlers

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5"
	"github.com/pashagolub/pgxmock/v4"
	"go.uber.org/zap"

	"github.com/munisp/hydrogenTransport/services/go/commerce-api/internal/ledger"
)

// Note: pgxmock assigns a column into a **string destination only when the
// row value is itself a *string (reflect-assignable), so nullable text
// columns scanned into pointer fields are added via strPtr (trading_test.go).

// --- Wave-7 A2-06: corporate group settlement --------------------------------

var billingAccountRowTime = time.Date(2026, 9, 1, 8, 0, 0, 0, time.UTC)

func billingAccountRow(id, name, kind, status string, ledgerID uint64) *pgxmock.Rows {
	return pgxmock.NewRows([]string{
		"id", "name", "kind", "contact_email", "ledger_account_id", "status", "created_at",
	}).AddRow(id, name, kind, "ops@example.com", ledgerID, status, billingAccountRowTime)
}

var invoicePeriodStart = time.Date(2026, 8, 1, 0, 0, 0, 0, time.UTC)
var invoicePeriodEnd = time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC)

func invoiceRow(id, accountID, status string, amount int64, charges int) *pgxmock.Rows {
	return pgxmock.NewRows([]string{
		"id", "billing_account_id", "period_start", "period_end", "amount_minor",
		"charge_count", "status", "tb_transfer_id", "issued_at", "paid_at", "created_at",
	}).AddRow(id, accountID, invoicePeriodStart, invoicePeriodEnd, amount, charges,
		status, nil, billingAccountRowTime, nil, billingAccountRowTime)
}

func billingRequest(body string) *http.Request {
	if body == "" {
		return httptest.NewRequest(http.MethodPost, "/", nil)
	}
	return httptest.NewRequest(http.MethodPost, "/", strings.NewReader(body))
}

func TestCreateBillingAccount_Happy(t *testing.T) {
	pool, err := pgxmock.NewPool()
	if err != nil {
		t.Fatalf("pgxmock.NewPool: %v", err)
	}
	defer pool.Close()
	h := &Handler{db: pool, pub: &fakePublisher{}, log: zap.NewExample()}

	pool.ExpectQuery(`INSERT INTO commerce\.billing_accounts`).
		WithArgs("Acme Mobility", "corporate", "ops@example.com", int64(5000)).
		WillReturnRows(billingAccountRow("acct-1", "Acme Mobility", "corporate", "active", 5001))

	rec := httptest.NewRecorder()
	h.CreateBillingAccount(rec, billingRequest(`{"name":"Acme Mobility","kind":"corporate","contact_email":"ops@example.com"}`))

	if rec.Code != http.StatusCreated {
		t.Fatalf("got %d, want 201 (body: %s)", rec.Code, rec.Body)
	}
	var a BillingAccount
	if err := json.Unmarshal(rec.Body.Bytes(), &a); err != nil {
		t.Fatalf("decode body: %v", err)
	}
	if a.LedgerAccountID != 5001 || a.Status != "active" {
		t.Fatalf("unexpected account: %+v", a)
	}
	if err := pool.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet db expectations: %v", err)
	}
}

func TestCreateBillingAccount_InvalidKind(t *testing.T) {
	h := &Handler{log: zap.NewExample()} // no db: must not be reached
	rec := httptest.NewRecorder()

	h.CreateBillingAccount(rec, billingRequest(`{"name":"X","kind":"union"}`))

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("got %d, want 400 (body: %s)", rec.Code, rec.Body)
	}
}

// A payer-backed grant validates the billing account is active before the
// entitlement is written.
func TestGrantEntitlement_PayerBacked(t *testing.T) {
	pool, err := pgxmock.NewPool()
	if err != nil {
		t.Fatalf("pgxmock.NewPool: %v", err)
	}
	defer pool.Close()
	h := &Handler{db: pool, log: zap.NewExample()}

	pool.ExpectQuery(`SELECT duration_days FROM commerce\.fare_products`).
		WithArgs("prod-pass").
		WillReturnRows(pgxmock.NewRows([]string{"duration_days"}).AddRow(30))
	pool.ExpectQuery(`SELECT status FROM commerce\.billing_accounts`).
		WithArgs("acct-1").
		WillReturnRows(pgxmock.NewRows([]string{"status"}).AddRow("active"))
	pool.ExpectQuery(`INSERT INTO commerce\.rider_entitlements`).
		WithArgs("rider-a", "prod-pass", pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), "ops-1").
		WillReturnRows(pgxmock.NewRows([]string{
			"id", "rider_sub", "product_id", "valid_from", "valid_to", "payer_account", "created_by", "created_at",
		}).AddRow("ent-1", "rider-a", "prod-pass", billingAccountRowTime, billingAccountRowTime, strPtr("acct-1"), "ops-1", billingAccountRowTime))

	rec := httptest.NewRecorder()
	h.GrantEntitlement(rec, withClaims(
		httptest.NewRequest(http.MethodPost, "/v1/entitlements",
			strings.NewReader(`{"rider_sub":"rider-a","product_id":"prod-pass","payer_account":"acct-1"}`)),
		"ops-1", "operator"))

	if rec.Code != http.StatusCreated {
		t.Fatalf("got %d, want 201 (body: %s)", rec.Code, rec.Body)
	}
	var e Entitlement
	if err := json.Unmarshal(rec.Body.Bytes(), &e); err != nil {
		t.Fatalf("decode body: %v", err)
	}
	if e.PayerAccount == nil || *e.PayerAccount != "acct-1" {
		t.Fatalf("entitlement must carry its payer account, got %+v", e)
	}
	if err := pool.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet db expectations: %v", err)
	}
}

func TestGrantEntitlement_PayerAccountSuspended(t *testing.T) {
	pool, err := pgxmock.NewPool()
	if err != nil {
		t.Fatalf("pgxmock.NewPool: %v", err)
	}
	defer pool.Close()
	h := &Handler{db: pool, log: zap.NewExample()}

	pool.ExpectQuery(`SELECT duration_days FROM commerce\.fare_products`).
		WithArgs("prod-pass").
		WillReturnRows(pgxmock.NewRows([]string{"duration_days"}).AddRow(30))
	pool.ExpectQuery(`SELECT status FROM commerce\.billing_accounts`).
		WithArgs("acct-1").
		WillReturnRows(pgxmock.NewRows([]string{"status"}).AddRow("suspended"))

	rec := httptest.NewRecorder()
	h.GrantEntitlement(rec, withClaims(
		httptest.NewRequest(http.MethodPost, "/v1/entitlements",
			strings.NewReader(`{"rider_sub":"rider-a","product_id":"prod-pass","payer_account":"acct-1"}`)),
		"ops-1", "operator"))

	if rec.Code != http.StatusUnprocessableEntity {
		t.Fatalf("got %d, want 422 (body: %s)", rec.Code, rec.Body)
	}
	if err := pool.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet db expectations: %v", err)
	}
}

// A ride covered by a payer-backed pass charges the rider 0 AND accrues the
// covered fare to the corporate account in the same transaction.
func TestCreatePayment_PayerEntitlementAccrues(t *testing.T) {
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
		WillReturnRows(pgxmock.NewRows([]string{"kind", "discount_pct", "code", "id", "payer_account"}).
			AddRow("pass", 0, "acme-staff-pass", "ent-1", strPtr("acct-1")))
	pool.ExpectExec(`INSERT INTO commerce\.fare_payments`).
		WithArgs(pgxmock.AnyArg(), "rider-a", int64(500), int64(0), "EUR", "idem-corp").
		WillReturnResult(pgxmock.NewResult("INSERT", 1))
	// The covered €5.00 accrues to Acme's billing account — same transaction.
	pool.ExpectExec(`INSERT INTO commerce\.billing_charges`).
		WithArgs("acct-1", pgxmock.AnyArg(), "ent-1", int64(500)).
		WillReturnResult(pgxmock.NewResult("INSERT", 1))
	pool.ExpectQuery(`UPDATE commerce\.fare_payments`).
		WithArgs(pgxmock.AnyArg(), "settled", pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(paymentRow("pay-corp", "rider-a", "idem-corp", "settled"))
	pool.ExpectCommit()

	rec := httptest.NewRecorder()
	h.CreatePayment("")(rec, createRequest(t, `{"amount_minor":500,"currency":"EUR"}`, "idem-corp", "rider-a"))

	if rec.Code != http.StatusCreated {
		t.Fatalf("got %d, want 201 (body: %s)", rec.Code, rec.Body)
	}
	if led.count() != 0 {
		t.Fatalf("a payer-covered ride must not touch the rider wallet, got %d transfers", led.count())
	}
	if err := pool.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet db expectations: %v", err)
	}
}

func TestGenerateInvoice_Happy(t *testing.T) {
	pool, err := pgxmock.NewPool()
	if err != nil {
		t.Fatalf("pgxmock.NewPool: %v", err)
	}
	defer pool.Close()
	pub := &fakePublisher{}
	h := &Handler{db: pool, pub: pub, log: zap.NewExample()}

	pool.ExpectBegin()
	pool.ExpectExec(`pg_advisory_xact_lock`).WithArgs("billing:acct-1").
		WillReturnResult(pgxmock.NewResult("SELECT", 1))
	pool.ExpectQuery(`FROM commerce\.invoices`).
		WithArgs("acct-1", invoicePeriodStart, invoicePeriodEnd).
		WillReturnError(pgx.ErrNoRows)
	pool.ExpectQuery(`SELECT status FROM commerce\.billing_accounts`).
		WithArgs("acct-1").
		WillReturnRows(pgxmock.NewRows([]string{"status"}).AddRow("active"))
	pool.ExpectExec(`UPDATE commerce\.billing_charges`).
		WithArgs(pgxmock.AnyArg(), "acct-1", invoicePeriodStart, invoicePeriodEnd).
		WillReturnResult(pgxmock.NewResult("UPDATE", 3))
	pool.ExpectQuery(`INSERT INTO commerce\.invoices`).
		WithArgs(pgxmock.AnyArg(), "acct-1", invoicePeriodStart, invoicePeriodEnd).
		WillReturnRows(invoiceRow("inv-1", "acct-1", "issued", 1250, 3))
	pool.ExpectCommit()

	r := chi.NewRouter()
	r.Post("/v1/billing/accounts/{id}/invoices", h.GenerateInvoice)
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, withClaims(httptest.NewRequest(http.MethodPost, "/v1/billing/accounts/acct-1/invoices",
		strings.NewReader(`{"period_start":"2026-08-01T00:00:00Z","period_end":"2026-09-01T00:00:00Z"}`)), "ops-1", "operator"))

	if rec.Code != http.StatusCreated {
		t.Fatalf("got %d, want 201 (body: %s)", rec.Code, rec.Body)
	}
	var v Invoice
	if err := json.Unmarshal(rec.Body.Bytes(), &v); err != nil {
		t.Fatalf("decode body: %v", err)
	}
	if v.AmountMinor != 1250 || v.ChargeCount != 3 || v.Status != "issued" {
		t.Fatalf("unexpected invoice: %+v", v)
	}
	topics := pub.published()
	if len(topics) != 1 || topics[0] != "billing.invoice.issued" {
		t.Fatalf("published topics %v, want [billing.invoice.issued]", topics)
	}
	if err := pool.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet db expectations: %v", err)
	}
}

// Generating the same (account, period) twice returns the existing invoice —
// never a duplicate, never an error.
func TestGenerateInvoice_IdempotentReplay(t *testing.T) {
	pool, err := pgxmock.NewPool()
	if err != nil {
		t.Fatalf("pgxmock.NewPool: %v", err)
	}
	defer pool.Close()
	h := &Handler{db: pool, pub: &fakePublisher{}, log: zap.NewExample()}

	pool.ExpectBegin()
	pool.ExpectExec(`pg_advisory_xact_lock`).WithArgs("billing:acct-1").
		WillReturnResult(pgxmock.NewResult("SELECT", 1))
	pool.ExpectQuery(`FROM commerce\.invoices`).
		WithArgs("acct-1", invoicePeriodStart, invoicePeriodEnd).
		WillReturnRows(invoiceRow("inv-1", "acct-1", "issued", 1250, 3))
	pool.ExpectCommit()

	r := chi.NewRouter()
	r.Post("/v1/billing/accounts/{id}/invoices", h.GenerateInvoice)
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, withClaims(httptest.NewRequest(http.MethodPost, "/v1/billing/accounts/acct-1/invoices",
		strings.NewReader(`{"period_start":"2026-08-01T00:00:00Z","period_end":"2026-09-01T00:00:00Z"}`)), "ops-1", "operator"))

	if rec.Code != http.StatusOK {
		t.Fatalf("replay: got %d, want 200 (body: %s)", rec.Code, rec.Body)
	}
	if err := pool.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet db expectations: %v", err)
	}
}

func TestGenerateInvoice_NoCharges(t *testing.T) {
	pool, err := pgxmock.NewPool()
	if err != nil {
		t.Fatalf("pgxmock.NewPool: %v", err)
	}
	defer pool.Close()
	h := &Handler{db: pool, pub: &fakePublisher{}, log: zap.NewExample()}

	pool.ExpectBegin()
	pool.ExpectExec(`pg_advisory_xact_lock`).WithArgs("billing:acct-1").
		WillReturnResult(pgxmock.NewResult("SELECT", 1))
	pool.ExpectQuery(`FROM commerce\.invoices`).
		WithArgs("acct-1", invoicePeriodStart, invoicePeriodEnd).
		WillReturnError(pgx.ErrNoRows)
	pool.ExpectQuery(`SELECT status FROM commerce\.billing_accounts`).
		WithArgs("acct-1").
		WillReturnRows(pgxmock.NewRows([]string{"status"}).AddRow("active"))
	pool.ExpectExec(`UPDATE commerce\.billing_charges`).
		WithArgs(pgxmock.AnyArg(), "acct-1", invoicePeriodStart, invoicePeriodEnd).
		WillReturnResult(pgxmock.NewResult("UPDATE", 0))
	pool.ExpectRollback()

	r := chi.NewRouter()
	r.Post("/v1/billing/accounts/{id}/invoices", h.GenerateInvoice)
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, withClaims(httptest.NewRequest(http.MethodPost, "/v1/billing/accounts/acct-1/invoices",
		strings.NewReader(`{"period_start":"2026-08-01T00:00:00Z","period_end":"2026-09-01T00:00:00Z"}`)), "ops-1", "operator"))

	if rec.Code != http.StatusUnprocessableEntity {
		t.Fatalf("got %d, want 422 (body: %s)", rec.Code, rec.Body)
	}
	if err := pool.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet db expectations: %v", err)
	}
}

// Paying an invoice posts exactly one deterministic transfer from the
// corporate clearing account (5001) to operator revenue (2001) under the
// billing code (500), then flips the invoice to paid.
func TestPayInvoice_Happy(t *testing.T) {
	pool, err := pgxmock.NewPool()
	if err != nil {
		t.Fatalf("pgxmock.NewPool: %v", err)
	}
	defer pool.Close()
	led, pub := &fakeLedger{}, &fakePublisher{}
	h := &Handler{db: pool, ledger: led, pub: pub, log: zap.NewExample()}

	pool.ExpectBegin()
	pool.ExpectQuery(`FROM commerce\.invoices WHERE id = \$1 FOR UPDATE`).
		WithArgs("inv-1").
		WillReturnRows(invoiceRow("inv-1", "acct-1", "issued", 1250, 3))
	pool.ExpectQuery(`SELECT ledger_account_id FROM commerce\.billing_accounts`).
		WithArgs("acct-1").
		WillReturnRows(pgxmock.NewRows([]string{"ledger_account_id"}).AddRow(uint64(5001)))
	pool.ExpectQuery(`UPDATE commerce\.invoices`).
		WithArgs("inv-1", pgxmock.AnyArg()).
		WillReturnRows(invoiceRow("inv-1", "acct-1", "paid", 1250, 3))
	pool.ExpectCommit()

	r := chi.NewRouter()
	r.Post("/v1/billing/invoices/{id}/pay", h.PayInvoice)
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, withClaims(httptest.NewRequest(http.MethodPost, "/v1/billing/invoices/inv-1/pay", nil), "ops-1", "operator"))

	if rec.Code != http.StatusOK {
		t.Fatalf("got %d, want 200 (body: %s)", rec.Code, rec.Body)
	}
	if led.count() != 1 {
		t.Fatalf("want exactly 1 settlement transfer, got %d", led.count())
	}
	tr := led.transfers[0]
	if tr.debit != 5001 || tr.credit != ledger.OperatorRevenueAccount || tr.amount != 1250 || tr.code != ledger.CodeBilling {
		t.Fatalf("settlement transfer wrong: %+v", tr)
	}
	topics := pub.published()
	if len(topics) != 1 || topics[0] != "billing.invoice.paid" {
		t.Fatalf("published topics %v, want [billing.invoice.paid]", topics)
	}
	if err := pool.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet db expectations: %v", err)
	}
}

// An unfunded corporate clearing account fails closed: 402, invoice stays
// issued, no paid_at, no event.
func TestPayInvoice_InsufficientFunds(t *testing.T) {
	pool, err := pgxmock.NewPool()
	if err != nil {
		t.Fatalf("pgxmock.NewPool: %v", err)
	}
	defer pool.Close()
	led := &fakeLedger{err: fmt.Errorf("debit account 5001: %w", ledger.ErrInsufficientFunds)}
	h := &Handler{db: pool, ledger: led, pub: &fakePublisher{}, log: zap.NewExample()}

	pool.ExpectBegin()
	pool.ExpectQuery(`FROM commerce\.invoices WHERE id = \$1 FOR UPDATE`).
		WithArgs("inv-1").
		WillReturnRows(invoiceRow("inv-1", "acct-1", "issued", 1250, 3))
	pool.ExpectQuery(`SELECT ledger_account_id FROM commerce\.billing_accounts`).
		WithArgs("acct-1").
		WillReturnRows(pgxmock.NewRows([]string{"ledger_account_id"}).AddRow(uint64(5001)))
	pool.ExpectRollback()

	r := chi.NewRouter()
	r.Post("/v1/billing/invoices/{id}/pay", h.PayInvoice)
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, withClaims(httptest.NewRequest(http.MethodPost, "/v1/billing/invoices/inv-1/pay", nil), "ops-1", "operator"))

	if rec.Code != http.StatusPaymentRequired {
		t.Fatalf("got %d, want 402 (body: %s)", rec.Code, rec.Body)
	}
	if !strings.Contains(rec.Body.String(), "insufficient_funds") {
		t.Fatalf("body should carry insufficient_funds: %s", rec.Body)
	}
	if err := pool.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet db expectations: %v", err)
	}
}

// Re-paying a settled invoice is an idempotent replay: 200, no transfer.
func TestPayInvoice_AlreadyPaid(t *testing.T) {
	pool, err := pgxmock.NewPool()
	if err != nil {
		t.Fatalf("pgxmock.NewPool: %v", err)
	}
	defer pool.Close()
	led := &fakeLedger{}
	h := &Handler{db: pool, ledger: led, pub: &fakePublisher{}, log: zap.NewExample()}

	pool.ExpectBegin()
	pool.ExpectQuery(`FROM commerce\.invoices WHERE id = \$1 FOR UPDATE`).
		WithArgs("inv-1").
		WillReturnRows(invoiceRow("inv-1", "acct-1", "paid", 1250, 3))
	pool.ExpectCommit()

	r := chi.NewRouter()
	r.Post("/v1/billing/invoices/{id}/pay", h.PayInvoice)
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, withClaims(httptest.NewRequest(http.MethodPost, "/v1/billing/invoices/inv-1/pay", nil), "ops-1", "operator"))

	if rec.Code != http.StatusOK {
		t.Fatalf("got %d, want 200 (body: %s)", rec.Code, rec.Body)
	}
	if led.count() != 0 {
		t.Fatalf("replay must not post another transfer (%d posted)", led.count())
	}
	if err := pool.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet db expectations: %v", err)
	}
}
