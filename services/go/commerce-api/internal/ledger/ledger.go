// Package ledger is the TigerBeetle double-entry ledger client (SPEC §3.4:
// accounts RIDER_WALLET=1xxx, OPERATOR_REVENUE=2xxx, ENERGY_TRADE=3xxx,
// CARBON_FUND=4xxx). The real TigerBeetle cluster is the default: when
// TIGERBEETLE_ADDR is unset New fails closed, because a money path must never
// silently run on a fabricated ledger. A simulated in-memory ledger exists
// for local development only and requires the explicit opt-in
// H2_SIMULATED_LEDGER=true (SPEC §4 simulated fallback, env-gated).
package ledger

import (
	"crypto/sha256"
	"errors"
	"fmt"
	"os"
	"sync"

	tb "github.com/tigerbeetle/tigerbeetle-go"
	tb_types "github.com/tigerbeetle/tigerbeetle-go/pkg/types"
	"go.uber.org/zap"
)

// ErrInsufficientFunds is returned (wrapped) by Transfer when the debit
// account does not hold enough funds. Handlers map it to 402; it is never a
// panic and never produces a negative balance.
var ErrInsufficientFunds = errors.New("insufficient funds")

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
	// RiderFundingAccount is the platform cash-in source for wallet top-ups
	// (dev/simulated funding path; a real Mojaloop cash-in flow will replace
	// it). It intentionally has no balance cap.
	RiderFundingAccount uint64 = 2002
	EnergyTradeAccount  uint64 = 3001
	CarbonFundAccount   uint64 = 4001
	// CarbonIssuanceAccount is the platform issuance source for carbon
	// credits: when carbon-analytics issues a period credit, the
	// carbon.credit.issued consumer posts 4002 → 4001 (SPEC §3.4 CARBON_FUND
	// leg). Issuance creates the asset, so this account intentionally has no
	// balance cap.
	CarbonIssuanceAccount uint64 = 4002
)

// isRiderWallet reports whether id belongs to the per-rider wallet range
// (1xxx, allocated sequentially from 1001 via commerce.rider_accounts).
func isRiderWallet(id uint64) bool { return id >= 1001 && id < 2000 }

// Rider wallet accounts (1xxx) are assigned per rider via the persisted
// commerce.rider_accounts mapping (see handlers.riderAccount) — never derived
// by hashing the rider subject, which could collide across riders.

// DeterministicTransferID derives a stable TigerBeetle transfer ID (uint128)
// from an idempotency key: first 16 bytes of SHA-256 over a namespaced key.
// Retrying a request with the same key therefore targets the same transfer,
// which TigerBeetle deduplicates (TransferExists) instead of double-posting.
func DeterministicTransferID(idempotencyKey string) tb_types.Uint128 {
	sum := sha256.Sum256([]byte("h2fleet:tb-transfer:" + idempotencyKey))
	var b [16]byte
	copy(b[:], sum[:16])
	return tb_types.BytesToUint128(b)
}

// NewTransferID returns a fresh random transfer ID for flows that do not
// carry an idempotency key.
func NewTransferID() tb_types.Uint128 { return tb_types.ID() }

// Ledger posts double-entry transfers.
type Ledger interface {
	// EnsureAccount creates the account if missing (idempotent).
	EnsureAccount(id uint64, code uint16) error
	// Transfer posts amount (minor units) from debit to credit under the
	// given (caller-chosen, ideally deterministic) transfer ID; returns the
	// TigerBeetle transfer ID (hex). A transfer ID that was already posted
	// with identical parameters is a retry and returns success.
	Transfer(id tb_types.Uint128, debit, credit, amount uint64, code uint16) (string, error)
	Close()
}

// New returns a TigerBeetle-backed Ledger. When addr is empty it fails
// closed: the simulated in-memory ledger is only available behind the
// explicit dev opt-in H2_SIMULATED_LEDGER=true, never silently.
func New(addr string, log *zap.Logger) (Ledger, error) {
	if addr == "" {
		if os.Getenv("H2_SIMULATED_LEDGER") != "true" {
			return nil, errors.New("TIGERBEETLE_ADDR is required: the fare/wallet/trade money path must not run on a simulated ledger (set H2_SIMULATED_LEDGER=true to opt into the in-memory dev ledger)")
		}
		log.Warn("H2_SIMULATED_LEDGER=true: using simulated in-memory ledger (DEV ONLY — balances are fabricated and lost on restart)")
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
		{RiderFundingAccount, CodeFare},
		{EnergyTradeAccount, CodeEnergy},
		{CarbonFundAccount, CodeCarbon},
		{CarbonIssuanceAccount, CodeCarbon},
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
	// Rider wallets are created with debits_must_not_exceed_credits so an
	// unfunded wallet cannot spend: TigerBeetle rejects the overdraft
	// (TransferExceedsCredits) instead of letting the balance go negative.
	// The energy-trade clearing account gets the same flag: revenue may not
	// be conjured from an unfunded clearing account — it must be pre-funded
	// by an external buyer settlement (SPEC §3.8 workflow); until then an
	// unfunded trade is rejected and mapped to 402 by the handler.
	var flags uint16
	if isRiderWallet(id) || id == EnergyTradeAccount {
		flags = tb_types.AccountFlags{DebitsMustNotExceedCredits: true}.ToUint16()
	}
	results, err := l.client.CreateAccounts([]tb_types.Account{{
		ID:     tb_types.ToUint128(id),
		Ledger: LedgerID,
		Code:   code,
		Flags:  flags,
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

func (l *tbLedger) Transfer(id tb_types.Uint128, debit, credit, amount uint64, code uint16) (string, error) {
	if err := l.EnsureAccount(debit, code); err != nil {
		return "", fmt.Errorf("ensure debit account: %w", err)
	}
	if err := l.EnsureAccount(credit, code); err != nil {
		return "", fmt.Errorf("ensure credit account: %w", err)
	}
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
		if res.Result == tb_types.TransferExists {
			continue
		}
		if res.Result == tb_types.TransferExceedsCredits {
			// Overdraft rejection on a debits_must_not_exceed_credits wallet.
			return "", fmt.Errorf("debit account %d: %w", debit, ErrInsufficientFunds)
		}
		return "", fmt.Errorf("transfer failed: %s", res.Result.String())
	}
	return id.String(), nil
}

func (l *tbLedger) Close() { l.client.Close() }

// simulated is an in-memory ledger for local development without TigerBeetle,
// selected only via the explicit opt-in H2_SIMULATED_LEDGER=true (New fails
// closed otherwise). It enforces the same core invariants as the real ledger:
// a transfer that
// would drive the debit account negative is rejected, and re-posting an
// already-seen transfer ID is treated as an idempotent retry.
type simulated struct {
	mu        sync.Mutex
	balances  map[uint64]int64
	transfers map[tb_types.Uint128]string // posted transfer id → hex string
}

func newSimulated() *simulated {
	return &simulated{
		balances:  map[uint64]int64{},
		transfers: map[tb_types.Uint128]string{},
	}
}

func (s *simulated) EnsureAccount(id uint64, _ uint16) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.balances[id]; !ok {
		s.balances[id] = 0
	}
	return nil
}

func (s *simulated) Transfer(id tb_types.Uint128, debit, credit, amount uint64, _ uint16) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	hexID := id.String()
	if existing, ok := s.transfers[id]; ok {
		return existing, nil // idempotent retry of an already-posted transfer
	}
	if _, ok := s.balances[debit]; !ok {
		s.balances[debit] = 0
	}
	if _, ok := s.balances[credit]; !ok {
		s.balances[credit] = 0
	}
	if s.balances[debit]-int64(amount) < 0 {
		return "", fmt.Errorf("debit account %d (balance %d, amount %d): %w",
			debit, s.balances[debit], amount, ErrInsufficientFunds)
	}
	s.balances[debit] -= int64(amount)
	s.balances[credit] += int64(amount)
	s.transfers[id] = hexID
	return hexID, nil
}

func (s *simulated) Close() {}
