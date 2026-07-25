package handlers

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgconn"
	"github.com/pashagolub/pgxmock/v4"
	"go.uber.org/zap"

	"github.com/munisp/hydrogenTransport/services/go/commerce-api/internal/ledger"
)

func tradeRow(id, kind string, kg float64, price int64, status string, tbID, key *string) *pgxmock.Rows {
	return pgxmock.NewRows([]string{
		"id", "kind", "quantity_kg", "price_minor", "status",
		"tb_transfer_id", "idempotency_key", "created_at",
	}).AddRow(id, kind, kg, price, status, tbID, key,
		time.Date(2026, 7, 24, 9, 0, 0, 0, time.UTC))
}

func tradeRequest(t *testing.T, body, idemKey string) *http.Request {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/v1/energy/trades", strings.NewReader(body))
	if idemKey != "" {
		req.Header.Set("Idempotency-Key", idemKey)
	}
	return withClaims(req, "ops-1", "operator")
}

func TestCreateTrade_MissingIdempotencyKey(t *testing.T) {
	h := &Handler{log: zap.NewExample()} // no db/ledger: must not be reached
	rec := httptest.NewRecorder()
	h.CreateTrade(rec, tradeRequest(t, `{"kind":"h2-sale","quantity_kg":10,"price_minor":5000}`, ""))
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("got %d, want 400 (body: %s)", rec.Code, rec.Body)
	}
}

func TestCreateTrade_InvalidKind(t *testing.T) {
	h := &Handler{log: zap.NewExample()}
	rec := httptest.NewRecorder()
	h.CreateTrade(rec, tradeRequest(t, `{"kind":"bitcoin","quantity_kg":10,"price_minor":5000}`, "tk-1"))
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("got %d, want 400 (body: %s)", rec.Code, rec.Body)
	}
}

// Happy path: proposed insert → surplus draw-down → ledger settlement →
// executed with tb_transfer_id persisted → energy.trade.executed.
func TestCreateTrade_Executed(t *testing.T) {
	h, pool := newMockHandler(t)
	led, pub := &fakeLedger{}, &fakePublisher{}
	h.ledger, h.pub = led, pub

	pool.ExpectExec(`INSERT INTO commerce\.trades`).
		WithArgs(pgxmock.AnyArg(), "h2-sale", 10.0, int64(5000), "tk-happy").
		WillReturnResult(pgxmock.NewResult("INSERT", 1))
	pool.ExpectBegin()
	pool.ExpectQuery(`SELECT id, COALESCE\(available_kg,0\) FROM infra\.stations`).
		WillReturnRows(pgxmock.NewRows([]string{"id", "available_kg"}).AddRow("st-1", 20.0))
	pool.ExpectExec(`UPDATE infra\.stations SET available_kg = available_kg - \$2`).
		WithArgs("st-1", 10.0).
		WillReturnResult(pgxmock.NewResult("UPDATE", 1))
	pool.ExpectCommit()
	pool.ExpectQuery(`UPDATE commerce\.trades SET status = 'executed'`).
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(tradeRow("tr-1", "h2-sale", 10.0, 5000, "executed", strPtr("tb-123"), strPtr("tk-happy")))

	rec := httptest.NewRecorder()
	h.CreateTrade(rec, tradeRequest(t, `{"kind":"h2-sale","quantity_kg":10,"price_minor":5000}`, "tk-happy"))

	if rec.Code != http.StatusCreated {
		t.Fatalf("got %d, want 201 (body: %s)", rec.Code, rec.Body)
	}
	var tr Trade
	if err := json.Unmarshal(rec.Body.Bytes(), &tr); err != nil {
		t.Fatalf("decode body: %v", err)
	}
	if tr.Status != "executed" || tr.TBTransferID == nil || *tr.TBTransferID != "tb-123" {
		t.Fatalf("executed trade must carry tb_transfer_id: %+v", tr)
	}
	if led.count() != 1 {
		t.Fatalf("want 1 ledger transfer, got %d", led.count())
	}
	xf := led.transfers[0]
	if xf.debit != ledger.EnergyTradeAccount || xf.credit != ledger.OperatorRevenueAccount || xf.amount != 5000 {
		t.Fatalf("trade transfer wrong: %+v", xf)
	}
	if topics := pub.published(); len(topics) != 1 || topics[0] != "energy.trade.executed" {
		t.Fatalf("published %v, want [energy.trade.executed]", topics)
	}
	if err := pool.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet db expectations: %v", err)
	}
}

// Replay with the same Idempotency-Key returns the original trade (200) —
// no second transfer, no second surplus draw-down.
func TestCreateTrade_IdempotentReplay(t *testing.T) {
	h, pool := newMockHandler(t)
	led, pub := &fakeLedger{}, &fakePublisher{}
	h.ledger, h.pub = led, pub

	pool.ExpectExec(`INSERT INTO commerce\.trades`).
		WithArgs(pgxmock.AnyArg(), "h2-sale", 10.0, int64(5000), "tk-dupe").
		WillReturnError(&pgconn.PgError{Code: "23505"})
	pool.ExpectQuery(`FROM commerce\.trades WHERE idempotency_key = \$1`).
		WithArgs("tk-dupe").
		WillReturnRows(tradeRow("tr-1", "h2-sale", 10.0, 5000, "executed", strPtr("tb-123"), strPtr("tk-dupe")))

	rec := httptest.NewRecorder()
	h.CreateTrade(rec, tradeRequest(t, `{"kind":"h2-sale","quantity_kg":10,"price_minor":5000}`, "tk-dupe"))

	if rec.Code != http.StatusOK {
		t.Fatalf("replay: got %d, want 200 (body: %s)", rec.Code, rec.Body)
	}
	if led.count() != 0 {
		t.Fatalf("replay must not post another transfer (%d)", led.count())
	}
	if n := len(pub.published()); n != 0 {
		t.Fatalf("replay must not republish events (%d)", n)
	}
	if err := pool.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet db expectations: %v", err)
	}
}

// A sale for more H2 than the recorded station surplus is rejected: trade
// marked failed, energy.trade.failed published, no ledger transfer.
func TestCreateTrade_InsufficientSurplus(t *testing.T) {
	h, pool := newMockHandler(t)
	led, pub := &fakeLedger{}, &fakePublisher{}
	h.ledger, h.pub = led, pub

	pool.ExpectExec(`INSERT INTO commerce\.trades`).
		WithArgs(pgxmock.AnyArg(), "h2-sale", 10.0, int64(5000), "tk-short").
		WillReturnResult(pgxmock.NewResult("INSERT", 1))
	pool.ExpectBegin()
	pool.ExpectQuery(`SELECT id, COALESCE\(available_kg,0\) FROM infra\.stations`).
		WillReturnRows(pgxmock.NewRows([]string{"id", "available_kg"}).AddRow("st-1", 5.0))
	// rollback: no UPDATE infra.stations expected
	pool.ExpectQuery(`UPDATE commerce\.trades SET status = 'failed'`).
		WithArgs(pgxmock.AnyArg()).
		WillReturnRows(tradeRow("tr-2", "h2-sale", 10.0, 5000, "failed", nil, strPtr("tk-short")))

	rec := httptest.NewRecorder()
	h.CreateTrade(rec, tradeRequest(t, `{"kind":"h2-sale","quantity_kg":10,"price_minor":5000}`, "tk-short"))

	if rec.Code != http.StatusConflict {
		t.Fatalf("got %d, want 409 (body: %s)", rec.Code, rec.Body)
	}
	if !strings.Contains(rec.Body.String(), "insufficient_surplus") {
		t.Fatalf("error body should carry insufficient_surplus: %s", rec.Body)
	}
	if led.count() != 0 {
		t.Fatalf("surplus-rejected trade must not reach the ledger (%d transfers)", led.count())
	}
	if topics := pub.published(); len(topics) != 1 || topics[0] != "energy.trade.failed" {
		t.Fatalf("published %v, want [energy.trade.failed]", topics)
	}
	if err := pool.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet db expectations: %v", err)
	}
}

// An unfunded energy clearing account rejects the settlement: 402 with a
// machine-readable code, the surplus draw-down is compensated, the trade
// lands in failed and energy.trade.failed is published — never "cleared".
func TestCreateTrade_InsufficientFunds(t *testing.T) {
	h, pool := newMockHandler(t)
	pub := &fakePublisher{}
	led := &fakeLedger{err: fmt.Errorf("debit account 3001: %w", ledger.ErrInsufficientFunds)}
	h.ledger, h.pub = led, pub

	pool.ExpectExec(`INSERT INTO commerce\.trades`).
		WithArgs(pgxmock.AnyArg(), "energy-export", 10.0, int64(5000), "tk-broke").
		WillReturnResult(pgxmock.NewResult("INSERT", 1))
	pool.ExpectBegin()
	pool.ExpectQuery(`SELECT id, COALESCE\(available_kg,0\) FROM infra\.stations`).
		WillReturnRows(pgxmock.NewRows([]string{"id", "available_kg"}).AddRow("st-1", 20.0))
	pool.ExpectExec(`UPDATE infra\.stations SET available_kg = available_kg - \$2`).
		WithArgs("st-1", 10.0).
		WillReturnResult(pgxmock.NewResult("UPDATE", 1))
	pool.ExpectCommit()
	// Compensation: the exact draw-down is restored.
	pool.ExpectExec(`UPDATE infra\.stations SET available_kg = available_kg \+ \$2`).
		WithArgs("st-1", 10.0).
		WillReturnResult(pgxmock.NewResult("UPDATE", 1))
	pool.ExpectQuery(`UPDATE commerce\.trades SET status = 'failed'`).
		WithArgs(pgxmock.AnyArg()).
		WillReturnRows(tradeRow("tr-3", "energy-export", 10.0, 5000, "failed", nil, strPtr("tk-broke")))

	rec := httptest.NewRecorder()
	h.CreateTrade(rec, tradeRequest(t, `{"kind":"energy-export","quantity_kg":10,"price_minor":5000}`, "tk-broke"))

	if rec.Code != http.StatusPaymentRequired {
		t.Fatalf("got %d, want 402 (body: %s)", rec.Code, rec.Body)
	}
	if !strings.Contains(rec.Body.String(), "insufficient_funds") {
		t.Fatalf("error body should carry insufficient_funds: %s", rec.Body)
	}
	if topics := pub.published(); len(topics) != 1 || topics[0] != "energy.trade.failed" {
		t.Fatalf("published %v, want [energy.trade.failed]", topics)
	}
	if err := pool.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet db expectations: %v", err)
	}
}

// h2-purchase is inbound supply: no surplus check/draw-down.
func TestCreateTrade_PurchaseSkipsSurplus(t *testing.T) {
	h, pool := newMockHandler(t)
	led, pub := &fakeLedger{}, &fakePublisher{}
	h.ledger, h.pub = led, pub

	pool.ExpectExec(`INSERT INTO commerce\.trades`).
		WithArgs(pgxmock.AnyArg(), "h2-purchase", 10.0, int64(5000), "tk-buy").
		WillReturnResult(pgxmock.NewResult("INSERT", 1))
	// no infra.stations queries expected
	pool.ExpectQuery(`UPDATE commerce\.trades SET status = 'executed'`).
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(tradeRow("tr-4", "h2-purchase", 10.0, 5000, "executed", strPtr("tb-9"), strPtr("tk-buy")))

	rec := httptest.NewRecorder()
	h.CreateTrade(rec, tradeRequest(t, `{"kind":"h2-purchase","quantity_kg":10,"price_minor":5000}`, "tk-buy"))

	if rec.Code != http.StatusCreated {
		t.Fatalf("got %d, want 201 (body: %s)", rec.Code, rec.Body)
	}
	if err := pool.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet db expectations: %v", err)
	}
}

func strPtr(s string) *string { return &s }
