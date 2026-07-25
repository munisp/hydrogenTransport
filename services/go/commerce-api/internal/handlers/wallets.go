package handlers

import (
	"encoding/json"
	"errors"
	"net/http"
	"os"

	auth "github.com/munisp/hydrogenTransport/packages/go-auth"
	"github.com/munisp/hydrogenTransport/services/go/commerce-api/internal/ledger"
)

// maxTopUpMinor bounds a single dev top-up (€10,000) so a typo cannot mint
// absurd wallet balances.
const maxTopUpMinor int64 = 1_000_000

// TopUpEnabled reports whether the dev/simulated wallet-funding endpoint is
// active. It is ON by default when the simulated ledger is in use
// (TIGERBEETLE_ADDR unset — every dev wallet starts empty and needs a
// funding path, per the business-logic audit), and OFF by default against a
// real TigerBeetle cluster unless WALLET_TOPUP_ENABLED=true is set
// explicitly. A production cash-in flow over Mojaloop rails will replace
// this endpoint.
func TopUpEnabled(getenv func(string) string) bool {
	if v := getenv("WALLET_TOPUP_ENABLED"); v != "" {
		return v == "true"
	}
	return getenv("TIGERBEETLE_ADDR") == ""
}

type topUpRequest struct {
	AmountMinor int64 `json:"amount_minor"`
}

// TopUpWallet handles POST /v1/wallets/topup (Keycloak JWT,
// Idempotency-Key required). Dev/simulated funding path: credits the
// caller's own rider wallet from the platform cash-in account so the fare
// flow is operable end-to-end. The wallet (commerce.rider_accounts row +
// TigerBeetle account) is provisioned lazily on first use. Retries with the
// same Idempotency-Key hit the same deterministic TigerBeetle transfer and
// cannot double-fund.
func (h *Handler) TopUpWallet(w http.ResponseWriter, r *http.Request) {
	if !TopUpEnabled(os.Getenv) {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "wallet top-up is disabled"})
		return
	}
	sub := auth.Subject(r.Context())
	if sub == "" {
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "authenticated subject required"})
		return
	}
	idemKey := r.Header.Get("Idempotency-Key")
	if idemKey == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "Idempotency-Key header is required"})
		return
	}
	var req topUpRequest
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid JSON body"})
		return
	}
	if req.AmountMinor <= 0 || req.AmountMinor > maxTopUpMinor {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "amount_minor must be between 1 and 1000000"})
		return
	}

	account, err := h.riderAccount(r.Context(), sub)
	if err != nil {
		h.internal(w, "ensure rider ledger account", err)
		return
	}
	transferID, err := h.ledger.Transfer(
		ledger.DeterministicTransferID("topup:"+idemKey),
		ledger.RiderFundingAccount, account, uint64(req.AmountMinor), ledger.CodeFare)
	if err != nil {
		if errors.Is(err, ledger.ErrInsufficientFunds) {
			h.internal(w, "top up wallet", errors.New("platform funding account exhausted"))
			return
		}
		h.internal(w, "top up wallet", err)
		return
	}
	writeJSON(w, http.StatusCreated, map[string]any{
		"rider_sub":      sub,
		"account_id":     account,
		"amount_minor":   req.AmountMinor,
		"tb_transfer_id": transferID,
		"funding":        "simulated",
	})
}
