package handlers

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"go.uber.org/zap"

	"github.com/munisp/hydrogenTransport/services/go/commerce-api/internal/ledger"
)

// Trade mirrors commerce.trades (energy-trading module).
type Trade struct {
	ID   string `json:"id"`
	Kind string `json:"kind"` // h2-sale|h2-purchase|energy-export|ev-v2g-export|ev-charge-purchase
	// QuantityKg is the traded amount in the kind's native unit — kg for the
	// H2 kinds, kWh for the ev-* kinds (Wave 5; the column name is kept for
	// backward compatibility, the kind names the unit).
	QuantityKg     float64   `json:"quantity_kg"`
	PriceMinor     int64     `json:"price_minor"`
	Status         string    `json:"status"` // proposed|executed|failed
	TBTransferID   *string   `json:"tb_transfer_id,omitempty"`
	IdempotencyKey *string   `json:"idempotency_key,omitempty"`
	CreatedAt      time.Time `json:"created_at"`
}

const tradeCols = `id, kind, COALESCE(quantity_kg,0), COALESCE(price_minor,0), status,
	tb_transfer_id, idempotency_key, created_at`

// validTradeKinds enumerates the trade kinds: migration 0001 plus the Wave-5
// EV kinds (ev-v2g-export = fleet sells kWh to the grid; ev-charge-purchase
// = inbound charging purchase).
var validTradeKinds = map[string]bool{
	"h2-sale": true, "h2-purchase": true, "energy-export": true,
	"ev-v2g-export": true, "ev-charge-purchase": true,
}

// surplusColumn resolves the physical backing of a trade kind (Wave 5):
// sale/export kinds consume station surplus — h2 kinds draw
// infra.stations.available_kg (unit kg), ev-v2g-export draws available_kwh
// (unit kwh). Purchase kinds are inbound supply: recording the physical
// receipt is station-ops' job, so they touch no inventory.
func surplusColumn(kind string) (column, unit string, draws bool) {
	switch kind {
	case "ev-v2g-export":
		return "available_kwh", "kwh", true
	case "h2-purchase", "ev-charge-purchase":
		return "", "", false
	default: // h2-sale, energy-export
		return "available_kg", "kg", true
	}
}

// ListTrades handles GET /v1/energy/trades.
func (h *Handler) ListTrades(w http.ResponseWriter, r *http.Request) {
	rows, err := h.db.Query(r.Context(),
		`SELECT `+tradeCols+` FROM commerce.trades ORDER BY created_at DESC LIMIT 200`)
	if err != nil {
		h.internal(w, "list trades", err)
		return
	}
	defer rows.Close()

	trades := []Trade{}
	for rows.Next() {
		t, err := scanTrade(rows)
		if err != nil {
			h.internal(w, "scan trade", err)
			return
		}
		trades = append(trades, t)
	}
	if err := rows.Err(); err != nil {
		h.internal(w, "iterate trades", err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"trades": trades})
}

func scanTrade(row interface{ Scan(...any) error }) (Trade, error) {
	var t Trade
	err := row.Scan(&t.ID, &t.Kind, &t.QuantityKg, &t.PriceMinor, &t.Status,
		&t.TBTransferID, &t.IdempotencyKey, &t.CreatedAt)
	return t, err
}

type createTradeRequest struct {
	Kind       string  `json:"kind"`
	QuantityKg float64 `json:"quantity_kg"`
	PriceMinor int64   `json:"price_minor"`
}

// errInsufficientSurplus marks a trade whose quantity exceeds the recorded
// station H2 surplus.
var errInsufficientSurplus = errors.New("insufficient station surplus")

// stationDraw records how much energy was drawn from one station so a failed
// settlement can be compensated exactly (amount in the draw column's unit).
type stationDraw struct {
	id     string
	amount float64
}

// CreateTrade handles POST /v1/energy/trades (Keycloak JWT, operator,
// Idempotency-Key required). Flow (ordering fixed so a DB failure cannot
// orphan a ledger transfer):
//  1. insert commerce.trades row as 'proposed' (idempotent on the key);
//  2. physical backing: sale kinds draw down infra.stations.available_kg in
//     one transaction — a trade for more H2 than the recorded surplus is
//     rejected (409, trade marked failed, energy.trade.failed published);
//  3. TigerBeetle settlement (deterministic transfer id from the
//     Idempotency-Key). Sale kinds settle clearing 3001 → operator revenue
//     2001; h2-purchase settles 2001 → 3001, funding the clearing account
//     (the buyer leg). The clearing account is overdraft-protected, so an
//     unfunded sale is rejected (402), the surplus draw-down is compensated,
//     the trade lands in 'failed' and energy.trade.failed is published;
//  4. on success the trade becomes 'executed' with tb_transfer_id persisted
//     and energy.trade.executed is published (SPEC §3.3).
func (h *Handler) CreateTrade(w http.ResponseWriter, r *http.Request) {
	idemKey := r.Header.Get("Idempotency-Key")
	if idemKey == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "Idempotency-Key header is required"})
		return
	}
	var req createTradeRequest
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid JSON body"})
		return
	}
	if !validTradeKinds[req.Kind] {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "kind must be one of h2-sale|h2-purchase|energy-export|ev-v2g-export|ev-charge-purchase"})
		return
	}
	if req.QuantityKg <= 0 || req.PriceMinor <= 0 {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "positive quantity_kg and price_minor are required"})
		return
	}

	// Step 1: idempotent insert. A replay with the same key returns the
	// already-recorded trade unchanged (no second transfer, no second
	// surplus draw-down).
	tradeID := uuid.NewString()
	_, err := h.db.Exec(r.Context(), `
		INSERT INTO commerce.trades (id, kind, quantity_kg, price_minor, status, idempotency_key)
		VALUES ($1, $2, $3, $4, 'proposed', $5)`,
		tradeID, req.Kind, req.QuantityKg, req.PriceMinor, idemKey)
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) && pgErr.Code == "23505" {
		existing, qerr := scanTrade(h.db.QueryRow(r.Context(),
			`SELECT `+tradeCols+` FROM commerce.trades WHERE idempotency_key = $1`, idemKey))
		if qerr != nil {
			h.internal(w, "load idempotent trade", qerr)
			return
		}
		writeJSON(w, http.StatusOK, existing)
		return
	}
	if err != nil {
		h.internal(w, "insert trade", err)
		return
	}

	event := map[string]any{
		"trade_id":    tradeID,
		"kind":        req.Kind,
		"quantity_kg": req.QuantityKg,
		"price_minor": req.PriceMinor,
	}
	fail := func(statusCode int, code, reason string) {
		t, uerr := scanTrade(h.db.QueryRow(r.Context(), `
			UPDATE commerce.trades SET status = 'failed' WHERE id = $1 RETURNING `+tradeCols, tradeID))
		if uerr != nil {
			h.internal(w, "mark trade failed", uerr)
			return
		}
		event["reason"] = reason
		event["failed_at"] = time.Now().UTC().Format(time.RFC3339)
		if err := h.pub.Publish(r.Context(), "energy.trade.failed", event); err != nil {
			h.log.Error("failed to publish energy.trade.failed", zap.Error(err))
		}
		writeJSON(w, statusCode, map[string]any{"error": code, "message": reason, "trade": t})
	}

	// Step 2: physical backing — draw down the station surplus (available_kg
	// for H2 kinds, available_kwh for ev-v2g-export; purchases skip).
	var draws []stationDraw
	drawColumn, drawUnit, drawsSurplus := surplusColumn(req.Kind)
	if drawsSurplus {
		draws, err = h.drawDownSurplus(r.Context(), req.QuantityKg, drawColumn, drawUnit)
		if errors.Is(err, errInsufficientSurplus) {
			fail(http.StatusConflict, "insufficient_surplus",
				fmt.Sprintf("trade quantity %.2f %s exceeds recorded station surplus", req.QuantityKg, drawUnit))
			return
		}
		if err != nil {
			h.internal(w, "draw down station surplus", err)
			return
		}
	}

	// Step 3: ledger settlement. Direction follows the trade kind
	// (BUSINESS_LOGIC_AUDIT §18: no buyer/counterparty account existed, so
	// clearing was never funded):
	//   - sale/export kinds (h2-sale, energy-export, ev-v2g-export): clearing
	//     (3001) → operator revenue (2001). The clearing account is
	//     overdraft-protected, so a sale can only settle against funds a
	//     purchase previously paid in — revenue is never conjured from an
	//     unfunded clearing account.
	//   - purchase kinds (h2-purchase, ev-charge-purchase): operator revenue
	//     (2001) → clearing (3001). Buying supply costs the operator and
	//     FUNDS the clearing account; this is the buyer/counterparty
	//     settlement leg.
	// The deterministic transfer id makes client retries safe.
	debit, credit := ledger.EnergyTradeAccount, ledger.OperatorRevenueAccount
	if strings.HasSuffix(req.Kind, "-purchase") {
		debit, credit = ledger.OperatorRevenueAccount, ledger.EnergyTradeAccount
	}
	transferID, err := h.ledger.Transfer(
		ledger.DeterministicTransferID("trade:"+idemKey),
		debit, credit,
		uint64(req.PriceMinor), ledger.CodeEnergy)
	if err != nil {
		h.log.Error("energy trade ledger transfer failed", zap.String("trade", tradeID), zap.Error(err))
		// Compensate the physical draw-down so energy is not lost from inventory.
		if rerr := h.restoreSurplus(r.Context(), draws, drawColumn); rerr != nil {
			h.log.Error("failed to restore station surplus", zap.String("trade", tradeID), zap.Error(rerr))
		}
		if errors.Is(err, ledger.ErrInsufficientFunds) {
			fail(http.StatusPaymentRequired, "insufficient_funds",
				"energy clearing account is not funded by a buyer settlement")
			return
		}
		fail(http.StatusBadGateway, "ledger_error", "ledger transfer failed")
		return
	}

	// Step 4: finalize.
	t, err := scanTrade(h.db.QueryRow(r.Context(), `
		UPDATE commerce.trades SET status = 'executed', tb_transfer_id = $2
		WHERE id = $1 RETURNING `+tradeCols, tradeID, transferID))
	if err != nil {
		h.internal(w, "finalize trade", err)
		return
	}
	event["tb_transfer_id"] = transferID
	event["executed_at"] = time.Now().UTC().Format(time.RFC3339)
	if err := h.pub.Publish(r.Context(), "energy.trade.executed", event); err != nil {
		h.log.Error("failed to publish energy.trade.executed", zap.Error(err))
	}
	writeJSON(w, http.StatusCreated, t)
}

// CancelTrade handles POST /v1/energy/trades/{id}/cancel (Keycloak JWT,
// operator). Only 'proposed' trades can be cancelled — a proposed trade has
// not drawn surplus and not settled, so cancellation is a pure status
// transition (executed trades are final; failed trades are already
// terminal). Publishes energy.trade.cancelled.
func (h *Handler) CancelTrade(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	t, err := scanTrade(h.db.QueryRow(r.Context(), `
		UPDATE commerce.trades SET status = 'cancelled'
		WHERE id = $1 AND status = 'proposed'
		RETURNING `+tradeCols, id))
	if errors.Is(err, pgx.ErrNoRows) {
		writeJSON(w, http.StatusConflict, map[string]string{"error": "trade not found or not in proposed status"})
		return
	}
	if err != nil {
		h.internal(w, "cancel trade", err)
		return
	}
	if err := h.pub.Publish(r.Context(), "energy.trade.cancelled", map[string]any{
		"trade_id":     t.ID,
		"kind":         t.Kind,
		"quantity_kg":  t.QuantityKg,
		"price_minor":  t.PriceMinor,
		"cancelled_at": time.Now().UTC().Format(time.RFC3339),
	}); err != nil {
		h.log.Error("failed to publish energy.trade.cancelled", zap.Error(err))
	}
	writeJSON(w, http.StatusOK, t)
}

// drawDownSurplus atomically verifies that the recorded station surplus
// covers amount and decrements it greedily across stations (ordered, locked
// FOR UPDATE). column is available_kg (unit kg) for the H2 kinds or
// available_kwh (unit kwh) for ev-v2g-export — one numeric path, Wave 5.
// It returns the per-station allocations for exact compensation.
func (h *Handler) drawDownSurplus(ctx context.Context, amount float64, column, unit string) ([]stationDraw, error) {
	tx, err := h.db.Begin(ctx)
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback(ctx) }()

	rows, err := tx.Query(ctx, `
		SELECT id, COALESCE(`+column+`,0) FROM infra.stations
		WHERE `+column+` > 0 ORDER BY id FOR UPDATE`)
	if err != nil {
		return nil, err
	}
	var stations []stationDraw
	var total float64
	for rows.Next() {
		var s stationDraw
		if err := rows.Scan(&s.id, &s.amount); err != nil {
			rows.Close()
			return nil, err
		}
		stations = append(stations, s)
		total += s.amount
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if total+1e-9 < amount {
		return nil, fmt.Errorf("%w (available %.2f %s, requested %.2f %s)", errInsufficientSurplus, total, unit, amount, unit)
	}

	remaining := amount
	draws := []stationDraw{}
	for _, s := range stations {
		if remaining <= 1e-9 {
			break
		}
		take := s.amount
		if take > remaining {
			take = remaining
		}
		if _, err := tx.Exec(ctx, `
			UPDATE infra.stations SET `+column+` = `+column+` - $2 WHERE id = $1`,
			s.id, take); err != nil {
			return nil, err
		}
		draws = append(draws, stationDraw{id: s.id, amount: take})
		remaining -= take
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, err
	}
	return draws, nil
}

// restoreSurplus compensates a previous drawDownSurplus after a failed
// settlement, returning the exact per-station allocations.
func (h *Handler) restoreSurplus(ctx context.Context, draws []stationDraw, column string) error {
	for _, d := range draws {
		if _, err := h.db.Exec(ctx, `
			UPDATE infra.stations SET `+column+` = `+column+` + $2 WHERE id = $1`,
			d.id, d.amount); err != nil {
			return err
		}
	}
	return nil
}
