package handlers

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/pashagolub/pgxmock/v4"
	"go.uber.org/zap"

	"github.com/munisp/hydrogenTransport/services/go/commerce-api/internal/ledger"
)

// Top-up credits the caller's own wallet from the platform funding account,
// provisioning the wallet lazily on first use.
func TestTopUpWallet_HappyPath(t *testing.T) {
	t.Setenv("WALLET_TOPUP_ENABLED", "true")
	pool, err := pgxmock.NewPool()
	if err != nil {
		t.Fatalf("pgxmock.NewPool: %v", err)
	}
	defer pool.Close()
	led := &fakeLedger{}
	h := &Handler{db: pool, ledger: led, pub: &fakePublisher{}, log: zap.NewExample()}

	pool.ExpectBegin()
	pool.ExpectExec(`INSERT INTO commerce\.rider_accounts`).
		WithArgs("rider-a").
		WillReturnResult(pgxmock.NewResult("INSERT", 1))
	pool.ExpectQuery(`SELECT account_id FROM commerce\.rider_accounts`).
		WithArgs("rider-a").
		WillReturnRows(pgxmock.NewRows([]string{"account_id"}).AddRow(uint64(1001)))
	pool.ExpectCommit()

	req := withClaims(httptest.NewRequest(http.MethodPost, "/v1/wallets/topup",
		strings.NewReader(`{"amount_minor":1000}`)), "rider-a")
	req.Header.Set("Idempotency-Key", "topup-1")
	rec := httptest.NewRecorder()
	h.TopUpWallet(rec, req)

	if rec.Code != http.StatusCreated {
		t.Fatalf("got %d, want 201 (body: %s)", rec.Code, rec.Body)
	}
	var resp map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode body: %v", err)
	}
	if resp["rider_sub"] != "rider-a" || resp["amount_minor"] != float64(1000) {
		t.Fatalf("unexpected topup response: %v", resp)
	}
	if led.count() != 1 {
		t.Fatalf("want exactly 1 ledger transfer, got %d", led.count())
	}
	tr := led.transfers[0]
	if tr.debit != ledger.RiderFundingAccount || tr.credit != 1001 || tr.amount != 1000 {
		t.Fatalf("topup transfer wrong: %+v", tr)
	}
	if err := pool.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet db expectations: %v", err)
	}
}

// With the endpoint disabled (production default against a real TigerBeetle),
// top-up is not reachable.
func TestTopUpWallet_Disabled(t *testing.T) {
	t.Setenv("WALLET_TOPUP_ENABLED", "false")
	h := &Handler{log: zap.NewExample()} // no db/ledger: must not be reached
	req := withClaims(httptest.NewRequest(http.MethodPost, "/v1/wallets/topup",
		strings.NewReader(`{"amount_minor":1000}`)), "rider-a")
	req.Header.Set("Idempotency-Key", "topup-1")
	rec := httptest.NewRecorder()
	h.TopUpWallet(rec, req)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("got %d, want 404 (body: %s)", rec.Code, rec.Body)
	}
}

// Default enablement: simulated ledger (no TIGERBEETLE_ADDR) → enabled;
// real TigerBeetle → disabled unless explicitly overridden.
func TestTopUpEnabled_Defaults(t *testing.T) {
	get := func(vals map[string]string) func(string) string {
		return func(k string) string { return vals[k] }
	}
	if !TopUpEnabled(get(map[string]string{})) {
		t.Error("simulated ledger default should enable top-up")
	}
	if TopUpEnabled(get(map[string]string{"TIGERBEETLE_ADDR": "tb:3000"})) {
		t.Error("real TigerBeetle default should disable top-up")
	}
	if !TopUpEnabled(get(map[string]string{"TIGERBEETLE_ADDR": "tb:3000", "WALLET_TOPUP_ENABLED": "true"})) {
		t.Error("explicit WALLET_TOPUP_ENABLED=true should win")
	}
}
