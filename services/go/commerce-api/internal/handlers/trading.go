package handlers

import (
	"encoding/json"
	"net/http"
	"time"

	"go.uber.org/zap"

	"github.com/munisp/hydrogenTransport/services/go/commerce-api/internal/ledger"
)

// Trade mirrors commerce.trades (energy-trading module).
type Trade struct {
	ID         string    `json:"id"`
	Kind       string    `json:"kind"` // e.g. "h2_surplus", "grid_energy"
	QuantityKg float64   `json:"quantity_kg"`
	PriceMinor int64     `json:"price_minor"`
	Status     string    `json:"status"`
	CreatedAt  time.Time `json:"created_at"`
}

const tradeCols = `id, kind, COALESCE(quantity_kg,0), COALESCE(price_minor,0), status, created_at`

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
		var t Trade
		if err := rows.Scan(&t.ID, &t.Kind, &t.QuantityKg, &t.PriceMinor, &t.Status, &t.CreatedAt); err != nil {
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

type createTradeRequest struct {
	Kind       string  `json:"kind"`
	QuantityKg float64 `json:"quantity_kg"`
	PriceMinor int64   `json:"price_minor"`
}

// CreateTrade handles POST /v1/energy/trades (Keycloak JWT). Executes the
// trade on the TigerBeetle ledger (ENERGY_TRADE 3xxx → OPERATOR_REVENUE 2xxx)
// and publishes energy.trade.executed (SPEC §3.3).
func (h *Handler) CreateTrade(w http.ResponseWriter, r *http.Request) {
	var req createTradeRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.Kind == "" || req.QuantityKg <= 0 || req.PriceMinor <= 0 {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "kind, positive quantity_kg and price_minor are required"})
		return
	}

	status := "executed"
	transferID, err := h.ledger.Transfer(
		ledger.EnergyTradeAccount, ledger.OperatorRevenueAccount,
		uint64(req.PriceMinor), ledger.CodeEnergy)
	if err != nil {
		h.log.Error("energy trade ledger transfer failed", zap.Error(err))
		status = "failed"
	}

	var t Trade
	err = h.db.QueryRow(r.Context(), `
		INSERT INTO commerce.trades (kind, quantity_kg, price_minor, status)
		VALUES ($1, $2, $3, $4) RETURNING `+tradeCols,
		req.Kind, req.QuantityKg, req.PriceMinor, status).
		Scan(&t.ID, &t.Kind, &t.QuantityKg, &t.PriceMinor, &t.Status, &t.CreatedAt)
	if err != nil {
		h.internal(w, "create trade", err)
		return
	}

	if status == "executed" {
		if err := h.pub.Publish(r.Context(), "energy.trade.executed", map[string]any{
			"trade_id":       t.ID,
			"kind":           t.Kind,
			"quantity_kg":    t.QuantityKg,
			"price_minor":    t.PriceMinor,
			"tb_transfer_id": transferID,
		}); err != nil {
			h.log.Error("failed to publish energy.trade.executed", zap.Error(err))
		}
	}
	writeJSON(w, http.StatusCreated, t)
}
