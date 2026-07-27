package handlers

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/pashagolub/pgxmock/v4"

	"github.com/munisp/hydrogenTransport/services/go/commerce-api/internal/ledger"
)

// Wave 5 — ev-v2g-export: the fleet sells kWh to the grid. Physical backing
// draws down infra.stations.available_kwh (like h2-sale draws available_kg);
// settlement is sale-direction (clearing → operator revenue); executed with
// tb_transfer_id + energy.trade.executed, same idempotency semantics.
func TestCreateTrade_EVV2GExport(t *testing.T) {
	h, pool := newMockHandler(t)
	led, pub := &fakeLedger{}, &fakePublisher{}
	h.ledger, h.pub = led, pub

	pool.ExpectExec(`INSERT INTO commerce\.trades`).
		WithArgs(pgxmock.AnyArg(), "ev-v2g-export", 120.0, int64(7200), "tk-v2g").
		WillReturnResult(pgxmock.NewResult("INSERT", 1))
	pool.ExpectBegin()
	pool.ExpectQuery(`SELECT id, COALESCE\(available_kwh,0\) FROM infra\.stations`).
		WillReturnRows(pgxmock.NewRows([]string{"id", "available_kwh"}).AddRow("st-ev", 300.0))
	pool.ExpectExec(`UPDATE infra\.stations SET available_kwh = available_kwh - \$2`).
		WithArgs("st-ev", 120.0).
		WillReturnResult(pgxmock.NewResult("UPDATE", 1))
	pool.ExpectCommit()
	pool.ExpectQuery(`UPDATE commerce\.trades SET status = 'executed'`).
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(tradeRow("tr-v2g", "ev-v2g-export", 120.0, 7200, "executed", strPtr("tb-v2g"), strPtr("tk-v2g")))

	rec := httptest.NewRecorder()
	h.CreateTrade(rec, tradeRequest(t, `{"kind":"ev-v2g-export","quantity_kg":120,"price_minor":7200}`, "tk-v2g"))

	if rec.Code != http.StatusCreated {
		t.Fatalf("got %d, want 201 (body: %s)", rec.Code, rec.Body)
	}
	var tr Trade
	if err := json.Unmarshal(rec.Body.Bytes(), &tr); err != nil {
		t.Fatalf("decode body: %v", err)
	}
	if tr.Status != "executed" || tr.TBTransferID == nil || *tr.TBTransferID != "tb-v2g" {
		t.Fatalf("executed trade must carry tb_transfer_id: %+v", tr)
	}
	// Sale direction: clearing (3001) → operator revenue (2001).
	if led.count() != 1 {
		t.Fatalf("want 1 ledger transfer, got %d", led.count())
	}
	xf := led.transfers[0]
	if xf.debit != ledger.EnergyTradeAccount || xf.credit != ledger.OperatorRevenueAccount || xf.amount != 7200 {
		t.Fatalf("v2g export must settle clearing→revenue: %+v", xf)
	}
	if topics := pub.published(); len(topics) != 1 || topics[0] != "energy.trade.executed" {
		t.Fatalf("published %v, want [energy.trade.executed]", topics)
	}
	if err := pool.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet db expectations: %v", err)
	}
}

// ev-v2g-export beyond the recorded kWh surplus is rejected (409,
// insufficient_surplus naming kwh), trade failed, no ledger transfer.
func TestCreateTrade_EVV2GExport_InsufficientKwh(t *testing.T) {
	h, pool := newMockHandler(t)
	led, pub := &fakeLedger{}, &fakePublisher{}
	h.ledger, h.pub = led, pub

	pool.ExpectExec(`INSERT INTO commerce\.trades`).
		WithArgs(pgxmock.AnyArg(), "ev-v2g-export", 500.0, int64(30000), "tk-v2g-short").
		WillReturnResult(pgxmock.NewResult("INSERT", 1))
	pool.ExpectBegin()
	pool.ExpectQuery(`SELECT id, COALESCE\(available_kwh,0\) FROM infra\.stations`).
		WillReturnRows(pgxmock.NewRows([]string{"id", "available_kwh"}).AddRow("st-ev", 100.0))
	// rollback: no UPDATE infra.stations expected
	pool.ExpectQuery(`UPDATE commerce\.trades SET status = 'failed'`).
		WithArgs(pgxmock.AnyArg()).
		WillReturnRows(tradeRow("tr-v2g2", "ev-v2g-export", 500.0, 30000, "failed", nil, strPtr("tk-v2g-short")))

	rec := httptest.NewRecorder()
	h.CreateTrade(rec, tradeRequest(t, `{"kind":"ev-v2g-export","quantity_kg":500,"price_minor":30000}`, "tk-v2g-short"))

	if rec.Code != http.StatusConflict {
		t.Fatalf("got %d, want 409 (body: %s)", rec.Code, rec.Body)
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

// ev-charge-purchase is inbound (like h2-purchase): no surplus draw-down and
// the settlement FUNDS the clearing account (operator revenue → clearing).
func TestCreateTrade_EVChargePurchase(t *testing.T) {
	h, pool := newMockHandler(t)
	led, pub := &fakeLedger{}, &fakePublisher{}
	h.ledger, h.pub = led, pub

	pool.ExpectExec(`INSERT INTO commerce\.trades`).
		WithArgs(pgxmock.AnyArg(), "ev-charge-purchase", 200.0, int64(9000), "tk-charge").
		WillReturnResult(pgxmock.NewResult("INSERT", 1))
	// no infra.stations queries expected
	pool.ExpectQuery(`UPDATE commerce\.trades SET status = 'executed'`).
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(tradeRow("tr-charge", "ev-charge-purchase", 200.0, 9000, "executed", strPtr("tb-c"), strPtr("tk-charge")))

	rec := httptest.NewRecorder()
	h.CreateTrade(rec, tradeRequest(t, `{"kind":"ev-charge-purchase","quantity_kg":200,"price_minor":9000}`, "tk-charge"))

	if rec.Code != http.StatusCreated {
		t.Fatalf("got %d, want 201 (body: %s)", rec.Code, rec.Body)
	}
	if led.count() != 1 {
		t.Fatalf("want exactly 1 ledger transfer, got %d", led.count())
	}
	xf := led.transfers[0]
	if xf.debit != ledger.OperatorRevenueAccount || xf.credit != ledger.EnergyTradeAccount || xf.amount != 9000 {
		t.Fatalf("charge purchase must settle revenue→clearing: %+v", xf)
	}
	if topics := pub.published(); len(topics) != 1 || topics[0] != "energy.trade.executed" {
		t.Fatalf("published %v, want [energy.trade.executed]", topics)
	}
	if err := pool.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet db expectations: %v", err)
	}
}
