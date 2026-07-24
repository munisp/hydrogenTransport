package ledger

import (
	"strings"
	"testing"
)

func TestDeterministicTransferID(t *testing.T) {
	a := DeterministicTransferID("key-1")
	b := DeterministicTransferID("key-1")
	c := DeterministicTransferID("key-2")
	if a != b {
		t.Error("same idempotency key must yield the same transfer id")
	}
	if a == c {
		t.Error("different idempotency keys must yield different transfer ids")
	}
	if got := len(a.String()); got != 32 {
		t.Errorf("transfer id should be 32 hex chars, got %d", got)
	}
}

func TestSimulatedRejectsNegativeBalance(t *testing.T) {
	l := newSimulated()
	debit, credit := uint64(1001), OperatorRevenueAccount
	if _, err := l.Transfer(NewTransferID(), debit, credit, 500, CodeFare); err == nil {
		t.Fatal("transfer exceeding balance must be rejected")
	} else if !strings.Contains(err.Error(), "negative") {
		t.Fatalf("unexpected error: %v", err)
	}
	if l.balances[debit] != 0 || l.balances[credit] != 0 {
		t.Fatalf("rejected transfer must not mutate balances: %+v", l.balances)
	}
}

func TestSimulatedIdempotentRetry(t *testing.T) {
	l := newSimulated()
	debit, credit := uint64(1001), OperatorRevenueAccount
	// Seed the debit account with a funded balance (in-package test access).
	l.balances[debit] = 1000
	id := DeterministicTransferID("pay-1")
	first, err := l.Transfer(id, debit, credit, 400, CodeFare)
	if err != nil {
		t.Fatalf("first transfer: %v", err)
	}
	second, err := l.Transfer(id, debit, credit, 400, CodeFare)
	if err != nil {
		t.Fatalf("retry must succeed idempotently: %v", err)
	}
	if first != second {
		t.Errorf("retry returned different id: %q vs %q", first, second)
	}
	if l.balances[debit] != 600 || l.balances[credit] != 400 {
		t.Errorf("retry double-posted: balances %+v", l.balances)
	}
}
