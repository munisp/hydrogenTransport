// Package ledger is the TigerBeetle double-entry ledger client (SPEC §3.4:
// accounts RIDER_WALLET=1xxx, OPERATOR_REVENUE=2xxx, ENERGY_TRADE=3xxx,
// CARBON_FUND=4xxx). When TIGERBEETLE_ADDR is unset a simulated in-memory
// ledger is used so the service still runs in minimal dev environments
// (simulated fallback allowed per SPEC §4).
package ledger

import (
	"fmt"
	"hash/crc32"
	"sync"

	tb "github.com/tigerbeetle/tigerbeetle-go"
	tb_types "github.com/tigerbeetle/tigerbeetle-go/pkg/types"
	"go.uber.org/zap"
)

// LedgerID is the single TigerBeetle ledger used by H2Fleet.
const LedgerID uint32 = 1

// Transfer codes.
const (
	CodeFare   uint16 = 100 // fare payment: rider wallet → operator revenue
	CodeEnergy uint16 = 300 // energy trade settlement
	CodeCarbon uint16 = 400 // carbon fund movements
)

// Well-known platform accounts (SPEC §3.4).
const (
	OperatorRevenueAccount uint64 = 2001
	EnergyTradeAccount     uint64 = 3001
	CarbonFundAccount      uint64 = 4001
)

// RiderAccount maps a rider subject to a deterministic 1xxx wallet account ID.
func RiderAccount(sub string) uint64 {
	return 1000 + uint64(crc32.ChecksumIEEE([]byte(sub))%900)
}

// Ledger posts double-entry transfers.
type Ledger interface {
	// EnsureAccount creates the account if missing (idempotent).
	EnsureAccount(id uint64, code uint16) error
	// Transfer posts amount (minor units) from debit to credit; returns the
	// TigerBeetle transfer ID (hex).
	Transfer(debit, credit, amount uint64, code uint16) (string, error)
	Close()
}

// New returns a TigerBeetle-backed Ledger when addr is set, otherwise a
// simulated in-memory ledger.
func New(addr string, log *zap.Logger) (Ledger, error) {
	if addr == "" {
		log.Warn("TIGERBEETLE_ADDR not set; using simulated in-memory ledger")
		return newSimulated(), nil
	}
	client, err := tb.NewClient(tb_types.ToUint128(0), []string{addr})
	if err != nil {
		return nil, fmt.Errorf("tigerbeetle client init: %w", err)
	}
	l := &tbLedger{client: client, log: log}
	// Bootstrap well-known platform accounts.
	for _, acc := range []struct {
		id   uint64
		code uint16
	}{
		{OperatorRevenueAccount, CodeFare},
		{EnergyTradeAccount, CodeEnergy},
		{CarbonFundAccount, CodeCarbon},
	} {
		if err := l.EnsureAccount(acc.id, acc.code); err != nil {
			client.Close()
			return nil, fmt.Errorf("bootstrap account %d: %w", acc.id, err)
		}
	}
	log.Info("tigerbeetle ledger connected", zap.String("addr", addr))
	return l, nil
}

type tbLedger struct {
	client tb.Client
	log    *zap.Logger
}

func (l *tbLedger) EnsureAccount(id uint64, code uint16) error {
	results, err := l.client.CreateAccounts([]tb_types.Account{{
		ID:     tb_types.ToUint128(id),
		Ledger: LedgerID,
		Code:   code,
	}})
	if err != nil {
		return err
	}
	for _, res := range results {
		if res.Result != tb_types.AccountExists {
			return fmt.Errorf("create account %d: %s", id, res.Result.String())
		}
	}
	return nil
}

func (l *tbLedger) Transfer(debit, credit, amount uint64, code uint16) (string, error) {
	if err := l.EnsureAccount(debit, code); err != nil {
		return "", fmt.Errorf("ensure debit account: %w", err)
	}
	if err := l.EnsureAccount(credit, code); err != nil {
		return "", fmt.Errorf("ensure credit account: %w", err)
	}
	id := tb_types.ID()
	results, err := l.client.CreateTransfers([]tb_types.Transfer{{
		ID:              id,
		DebitAccountID:  tb_types.ToUint128(debit),
		CreditAccountID: tb_types.ToUint128(credit),
		Amount:          tb_types.ToUint128(amount),
		Ledger:          LedgerID,
		Code:            code,
	}})
	if err != nil {
		return "", err
	}
	for _, res := range results {
		// TransferExists means an identical transfer was already posted
		// (retry-safe); treat as success with the same ID.
		if res.Result != tb_types.TransferExists {
			return "", fmt.Errorf("transfer failed: %s", res.Result.String())
		}
	}
	return id.String(), nil
}

func (l *tbLedger) Close() { l.client.Close() }

// simulated is an in-memory ledger for dev environments without TigerBeetle.
type simulated struct {
	mu       sync.Mutex
	balances map[uint64]int64
	seq      uint64
}

func newSimulated() *simulated {
	return &simulated{balances: map[uint64]int64{}}
}

func (s *simulated) EnsureAccount(id uint64, _ uint16) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.balances[id]; !ok {
		s.balances[id] = 0
	}
	return nil
}

func (s *simulated) Transfer(debit, credit, amount uint64, _ uint16) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.balances[debit]; !ok {
		s.balances[debit] = 0
	}
	if _, ok := s.balances[credit]; !ok {
		s.balances[credit] = 0
	}
	s.balances[debit] -= int64(amount)
	s.balances[credit] += int64(amount)
	s.seq++
	// Deterministic, TigerBeetle-shaped (32-hex) transfer ID.
	return fmt.Sprintf("%032x", s.seq), nil
}

func (s *simulated) Close() {}
