package consumers

import (
	"errors"
	"testing"

	tb_types "github.com/tigerbeetle/tigerbeetle-go/pkg/types"

	"github.com/munisp/hydrogenTransport/services/go/commerce-api/internal/ledger"
)

type fakeLedger struct {
	debit, credit, amount uint64
	code                  uint16
	calls                 int
	err                   error
}

func (f *fakeLedger) EnsureAccount(id uint64, code uint16) error { return nil }
func (f *fakeLedger) Transfer(_ tb_types.Uint128, debit, credit, amount uint64, code uint16) (string, error) {
	f.calls++
	if f.err != nil {
		return "", f.err
	}
	f.debit, f.credit, f.amount, f.code = debit, credit, amount, code
	return "tb-carbon-1", nil
}
func (f *fakeLedger) Close() {}

// carbon.credit.issued posts the CARBON_FUND leg: issuance source (4002) →
// fund (4001), amount = kg CO2 avoided, deterministic id per credit id.
func TestPostCarbonFundLeg(t *testing.T) {
	led := &fakeLedger{}
	id, err := PostCarbonFundLeg(CarbonCreditIssued{
		CreditID: "credit-2026-W30", Period: "2026-W30", KgCO2Avoided: 1200.4, Credits: 1.2,
	}, ledger.Ledger(led))
	_ = id
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if led.calls != 1 {
		t.Fatalf("want 1 transfer, got %d", led.calls)
	}
	if led.debit != ledger.CarbonIssuanceAccount || led.credit != ledger.CarbonFundAccount {
		t.Fatalf("leg must be issuance→fund, got %d→%d", led.debit, led.credit)
	}
	if led.amount != 1200 {
		t.Fatalf("amount = kg CO2 avoided rounded, got %d", led.amount)
	}
	if led.code != ledger.CodeCarbon {
		t.Fatalf("code = CodeCarbon, got %d", led.code)
	}
}

// Zero-avoidance periods post nothing (no fabricated fund movement).
func TestPostCarbonFundLeg_ZeroAvoidance(t *testing.T) {
	led := &fakeLedger{}
	if _, err := PostCarbonFundLeg(CarbonCreditIssued{CreditID: "c", Period: "p", KgCO2Avoided: 0}, ledger.Ledger(led)); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if led.calls != 0 {
		t.Fatalf("zero-avoidance must not post, got %d transfers", led.calls)
	}
}

// Malformed payloads are rejected (uncommitted → redelivered); ledger errors
// propagate so the consumer retries instead of committing a lost leg.
func TestPostCarbonFundLeg_Errors(t *testing.T) {
	led := &fakeLedger{}
	if _, err := PostCarbonFundLeg(CarbonCreditIssued{}, ledger.Ledger(led)); err == nil {
		t.Fatal("expected error for missing credit_id/period")
	}
	led.err = errors.New("tb unavailable")
	if _, err := PostCarbonFundLeg(CarbonCreditIssued{CreditID: "c", Period: "p", KgCO2Avoided: 5}, ledger.Ledger(led)); err == nil {
		t.Fatal("expected ledger error to propagate")
	}
}
