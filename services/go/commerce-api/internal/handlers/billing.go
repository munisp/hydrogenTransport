package handlers

import (
	"encoding/json"
	"errors"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"go.uber.org/zap"

	auth "github.com/munisp/hydrogenTransport/packages/go-auth"
	"github.com/munisp/hydrogenTransport/services/go/commerce-api/internal/ledger"
)

// Corporate/employer group settlement (Wave-7 A2-06, plan-wave7.md): a
// corporate billing account is the payer behind a class of entitlements.
// Rides covered by a payer-backed entitlement charge the rider 0 (or the
// discounted fare) and accrue the COVERED amount to the billing account
// (commerce.billing_charges, inserted in the payment transaction). The
// account is settled periodically by an invoice; paying an invoice posts a
// TigerBeetle transfer from the account's 5xxx clearing account to operator
// revenue — fail-closed when the clearing account is unfunded (402).

// BillingAccount mirrors commerce.billing_accounts (0010).
type BillingAccount struct {
	ID              string    `json:"id"`
	Name            string    `json:"name"`
	Kind            string    `json:"kind"` // corporate|school|agency|municipal
	ContactEmail    string    `json:"contact_email"`
	LedgerAccountID uint64    `json:"ledger_account_id"`
	Status          string    `json:"status"`
	CreatedAt       time.Time `json:"created_at"`
}

const billingAccountCols = `id, name, kind, contact_email, ledger_account_id, status, created_at`

func scanBillingAccount(row pgx.Row) (BillingAccount, error) {
	var a BillingAccount
	err := row.Scan(&a.ID, &a.Name, &a.Kind, &a.ContactEmail, &a.LedgerAccountID, &a.Status, &a.CreatedAt)
	return a, err
}

// CreateBillingAccount handles POST /v1/billing/accounts (operator). The
// TigerBeetle clearing account id is allocated sequentially from 5001
// (ledger.FirstBillingAccountID); the unique index race is retried like
// riderAccount.
func (h *Handler) CreateBillingAccount(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Name         string `json:"name"`
		Kind         string `json:"kind"`
		ContactEmail string `json:"contact_email"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&req); err != nil || req.Name == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "body must include \"name\""})
		return
	}
	switch req.Kind {
	case "corporate", "school", "agency", "municipal":
	default:
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "kind must be corporate, school, agency or municipal"})
		return
	}

	var a BillingAccount
	var lastErr error
	for attempt := 0; attempt < 3; attempt++ {
		a, lastErr = scanBillingAccount(h.db.QueryRow(r.Context(), `
			INSERT INTO commerce.billing_accounts (name, kind, contact_email, ledger_account_id)
			SELECT $1, $2, $3, COALESCE(MAX(ledger_account_id), $4) + 1 FROM commerce.billing_accounts
			RETURNING `+billingAccountCols,
			req.Name, req.Kind, req.ContactEmail, int64(ledger.FirstBillingAccountID)-1))
		if lastErr == nil {
			break
		}
		var pgErr *pgconn.PgError
		if !errors.As(lastErr, &pgErr) || pgErr.Code != "23505" { // ledger_account_id race
			break
		}
	}
	if lastErr != nil {
		h.internal(w, "create billing account", lastErr)
		return
	}
	if err := h.pub.Publish(r.Context(), "billing.account.created", map[string]any{
		"billing_account_id": a.ID,
		"name":               a.Name,
		"kind":               a.Kind,
		"ledger_account_id":  a.LedgerAccountID,
		"created_by":         auth.Subject(r.Context()),
		"created_at":         time.Now().UTC().Format(time.RFC3339),
	}); err != nil {
		h.log.Error("failed to publish billing.account.created", zap.Error(err))
	}
	writeJSON(w, http.StatusCreated, a)
}

// ListBillingAccounts handles GET /v1/billing/accounts (operator), with the
// currently uninvoiced accrual total per account.
func (h *Handler) ListBillingAccounts(w http.ResponseWriter, r *http.Request) {
	rows, err := h.db.Query(r.Context(), `
		SELECT a.`+billingAccountCols+`,
		       COALESCE(sum(c.amount_minor) FILTER (WHERE c.invoice_id IS NULL), 0) AS uninvoiced_minor
		FROM commerce.billing_accounts a
		LEFT JOIN commerce.billing_charges c ON c.billing_account_id = a.id
		GROUP BY a.id ORDER BY a.created_at DESC LIMIT 200`)
	if err != nil {
		h.internal(w, "list billing accounts", err)
		return
	}
	defer rows.Close()
	type accountWithTotal struct {
		BillingAccount
		UninvoicedMinor int64 `json:"uninvoiced_minor"`
	}
	accounts := []accountWithTotal{}
	for rows.Next() {
		var a accountWithTotal
		if err := rows.Scan(&a.ID, &a.Name, &a.Kind, &a.ContactEmail, &a.LedgerAccountID,
			&a.Status, &a.CreatedAt, &a.UninvoicedMinor); err != nil {
			h.internal(w, "scan billing account", err)
			return
		}
		accounts = append(accounts, a)
	}
	if err := rows.Err(); err != nil {
		h.internal(w, "iterate billing accounts", err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"billing_accounts": accounts})
}

// GetBillingAccount handles GET /v1/billing/accounts/{id} (operator).
func (h *Handler) GetBillingAccount(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	a, err := scanBillingAccount(h.db.QueryRow(r.Context(),
		`SELECT `+billingAccountCols+` FROM commerce.billing_accounts WHERE id = $1`, id))
	if errors.Is(err, pgx.ErrNoRows) {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "billing account not found"})
		return
	}
	if err != nil {
		h.internal(w, "get billing account", err)
		return
	}
	var uninvoiced int64
	if err := h.db.QueryRow(r.Context(), `
		SELECT COALESCE(sum(amount_minor),0) FROM commerce.billing_charges
		WHERE billing_account_id = $1 AND invoice_id IS NULL`, id).Scan(&uninvoiced); err != nil {
		h.internal(w, "billing account accrual total", err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"billing_account":  a,
		"uninvoiced_minor": uninvoiced,
	})
}

// Invoice mirrors commerce.invoices (0010).
type Invoice struct {
	ID               string     `json:"id"`
	BillingAccountID string     `json:"billing_account_id"`
	PeriodStart      time.Time  `json:"period_start"`
	PeriodEnd        time.Time  `json:"period_end"`
	AmountMinor      int64      `json:"amount_minor"`
	ChargeCount      int        `json:"charge_count"`
	Status           string     `json:"status"` // issued|paid|void
	TBTransferID     *string    `json:"tb_transfer_id,omitempty"`
	IssuedAt         time.Time  `json:"issued_at"`
	PaidAt           *time.Time `json:"paid_at,omitempty"`
	CreatedAt        time.Time  `json:"created_at"`
}

const invoiceCols = `id, billing_account_id, period_start, period_end, amount_minor, charge_count, status, tb_transfer_id, issued_at, paid_at, created_at`

func scanInvoice(row pgx.Row) (Invoice, error) {
	var v Invoice
	err := row.Scan(&v.ID, &v.BillingAccountID, &v.PeriodStart, &v.PeriodEnd, &v.AmountMinor,
		&v.ChargeCount, &v.Status, &v.TBTransferID, &v.IssuedAt, &v.PaidAt, &v.CreatedAt)
	return v, err
}

// GenerateInvoice handles POST /v1/billing/accounts/{id}/invoices (operator):
// sweeps the account's uninvoiced charges inside [period_start, period_end)
// into a new invoice. Idempotent on (account, period): a replay returns the
// existing invoice. One transaction guarded by a per-account advisory lock,
// so two concurrent generations cannot split the charge set.
func (h *Handler) GenerateInvoice(w http.ResponseWriter, r *http.Request) {
	accountID := chi.URLParam(r, "id")
	var req struct {
		PeriodStart time.Time `json:"period_start"`
		PeriodEnd   time.Time `json:"period_end"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&req); err != nil ||
		req.PeriodStart.IsZero() || req.PeriodEnd.IsZero() {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "body must include \"period_start\" and \"period_end\""})
		return
	}
	if !req.PeriodEnd.After(req.PeriodStart) {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "period_end must be after period_start"})
		return
	}

	tx, err := h.db.Begin(r.Context())
	if err != nil {
		h.internal(w, "begin invoice generation", err)
		return
	}
	defer func() { _ = tx.Rollback(r.Context()) }()

	if _, err := tx.Exec(r.Context(), `SELECT pg_advisory_xact_lock(hashtext($1))`, "billing:"+accountID); err != nil {
		h.internal(w, "lock billing account", err)
		return
	}

	// Idempotent replay: (account, period) is UNIQUE — return the artifact
	// that already exists instead of erroring or duplicating.
	if existing, err := scanInvoice(tx.QueryRow(r.Context(),
		`SELECT `+invoiceCols+` FROM commerce.invoices
		 WHERE billing_account_id = $1 AND period_start = $2 AND period_end = $3`,
		accountID, req.PeriodStart, req.PeriodEnd)); err == nil {
		_ = tx.Commit(r.Context())
		writeJSON(w, http.StatusOK, existing)
		return
	} else if !errors.Is(err, pgx.ErrNoRows) {
		h.internal(w, "check existing invoice", err)
		return
	}

	var status string
	if err := tx.QueryRow(r.Context(),
		`SELECT status FROM commerce.billing_accounts WHERE id = $1 FOR UPDATE`, accountID).Scan(&status); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			writeJSON(w, http.StatusNotFound, map[string]string{"error": "billing account not found"})
			return
		}
		h.internal(w, "lock billing account row", err)
		return
	}
	if status != "active" {
		writeJSON(w, http.StatusUnprocessableEntity, map[string]string{"error": "billing account is " + status})
		return
	}

	invoiceID := uuid.NewString()
	tag, err := tx.Exec(r.Context(), `
		UPDATE commerce.billing_charges SET invoice_id = $1
		WHERE billing_account_id = $2 AND invoice_id IS NULL
		  AND created_at >= $3 AND created_at < $4`,
		invoiceID, accountID, req.PeriodStart, req.PeriodEnd)
	if err != nil {
		h.internal(w, "sweep billing charges", err)
		return
	}
	if tag.RowsAffected() == 0 {
		writeJSON(w, http.StatusUnprocessableEntity, map[string]string{"error": "no uninvoiced charges in the period"})
		return
	}

	inv, err := scanInvoice(tx.QueryRow(r.Context(), `
		INSERT INTO commerce.invoices (id, billing_account_id, period_start, period_end, amount_minor, charge_count)
		SELECT $1, $2, $3, $4, sum(amount_minor), count(*)
		FROM commerce.billing_charges WHERE invoice_id = $1
		RETURNING `+invoiceCols, invoiceID, accountID, req.PeriodStart, req.PeriodEnd))
	if err != nil {
		h.internal(w, "insert invoice", err)
		return
	}
	if err := tx.Commit(r.Context()); err != nil {
		h.internal(w, "commit invoice", err)
		return
	}

	if err := h.pub.Publish(r.Context(), "billing.invoice.issued", map[string]any{
		"invoice_id":         inv.ID,
		"billing_account_id": inv.BillingAccountID,
		"period_start":       inv.PeriodStart.UTC().Format(time.RFC3339),
		"period_end":         inv.PeriodEnd.UTC().Format(time.RFC3339),
		"amount_minor":       inv.AmountMinor,
		"charge_count":       inv.ChargeCount,
		"issued_at":          time.Now().UTC().Format(time.RFC3339),
	}); err != nil {
		h.log.Error("failed to publish billing.invoice.issued", zap.Error(err))
	}
	writeJSON(w, http.StatusCreated, inv)
}

// ListInvoices handles GET /v1/billing/invoices?billing_account_id=&status=
// (operator).
func (h *Handler) ListInvoices(w http.ResponseWriter, r *http.Request) {
	query := `SELECT ` + invoiceCols + ` FROM commerce.invoices`
	args := []any{}
	where := ""
	if account := r.URL.Query().Get("billing_account_id"); account != "" {
		where += ` WHERE billing_account_id = $1`
		args = append(args, account)
	}
	if status := r.URL.Query().Get("status"); status != "" {
		if where == "" {
			where += ` WHERE status = $1`
		} else {
			where += ` AND status = $2`
		}
		args = append(args, status)
	}
	rows, err := h.db.Query(r.Context(), query+where+` ORDER BY issued_at DESC LIMIT 200`, args...)
	if err != nil {
		h.internal(w, "list invoices", err)
		return
	}
	defer rows.Close()
	invoices := []Invoice{}
	for rows.Next() {
		v, err := scanInvoice(rows)
		if err != nil {
			h.internal(w, "scan invoice", err)
			return
		}
		invoices = append(invoices, v)
	}
	if err := rows.Err(); err != nil {
		h.internal(w, "iterate invoices", err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"invoices": invoices})
}

// GetInvoice handles GET /v1/billing/invoices/{id} (operator): the invoice
// plus its line charges.
func (h *Handler) GetInvoice(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	inv, err := scanInvoice(h.db.QueryRow(r.Context(),
		`SELECT `+invoiceCols+` FROM commerce.invoices WHERE id = $1`, id))
	if errors.Is(err, pgx.ErrNoRows) {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "invoice not found"})
		return
	}
	if err != nil {
		h.internal(w, "get invoice", err)
		return
	}
	rows, err := h.db.Query(r.Context(), `
		SELECT id, payment_id, entitlement_id, amount_minor, created_at
		FROM commerce.billing_charges WHERE invoice_id = $1 ORDER BY created_at`, id)
	if err != nil {
		h.internal(w, "list invoice charges", err)
		return
	}
	defer rows.Close()
	type charge struct {
		ID            string    `json:"id"`
		PaymentID     string    `json:"payment_id"`
		EntitlementID string    `json:"entitlement_id"`
		AmountMinor   int64     `json:"amount_minor"`
		CreatedAt     time.Time `json:"created_at"`
	}
	charges := []charge{}
	for rows.Next() {
		var c charge
		if err := rows.Scan(&c.ID, &c.PaymentID, &c.EntitlementID, &c.AmountMinor, &c.CreatedAt); err != nil {
			h.internal(w, "scan invoice charge", err)
			return
		}
		charges = append(charges, c)
	}
	if err := rows.Err(); err != nil {
		h.internal(w, "iterate invoice charges", err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"invoice": inv, "charges": charges})
}

// PayInvoice handles POST /v1/billing/invoices/{id}/pay (operator): settles
// the invoice by transferring its amount from the account's corporate
// clearing account (5xxx) to operator revenue. The transfer id is
// deterministic per invoice, so a retry can never double-post; the
// conditional UPDATE guards the issued→paid transition against concurrent
// payment attempts. An unfunded clearing account fails closed with 402.
func (h *Handler) PayInvoice(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")

	tx, err := h.db.Begin(r.Context())
	if err != nil {
		h.internal(w, "begin invoice payment", err)
		return
	}
	defer func() { _ = tx.Rollback(r.Context()) }()

	inv, err := scanInvoice(tx.QueryRow(r.Context(),
		`SELECT `+invoiceCols+` FROM commerce.invoices WHERE id = $1 FOR UPDATE`, id))
	if errors.Is(err, pgx.ErrNoRows) {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "invoice not found"})
		return
	}
	if err != nil {
		h.internal(w, "load invoice", err)
		return
	}
	if inv.Status == "paid" {
		_ = tx.Commit(r.Context())
		writeJSON(w, http.StatusOK, inv) // idempotent replay
		return
	}
	if inv.Status != "issued" {
		writeJSON(w, http.StatusConflict, map[string]string{"error": "only issued invoices can be paid (status " + inv.Status + ")"})
		return
	}

	var ledgerAccount uint64
	if err := tx.QueryRow(r.Context(),
		`SELECT ledger_account_id FROM commerce.billing_accounts WHERE id = $1`, inv.BillingAccountID).
		Scan(&ledgerAccount); err != nil {
		h.internal(w, "load billing account ledger id", err)
		return
	}

	transferID, err := h.ledger.Transfer(
		ledger.DeterministicTransferID("invoice:"+inv.ID),
		ledgerAccount, ledger.OperatorRevenueAccount, uint64(inv.AmountMinor), ledger.CodeBilling)
	if err != nil {
		if errors.Is(err, ledger.ErrInsufficientFunds) {
			writeJSON(w, http.StatusPaymentRequired, map[string]any{
				"error":   "insufficient_funds",
				"message": "corporate clearing account is not funded for this invoice; record the external settlement first",
				"invoice": inv,
			})
			return
		}
		h.internal(w, "invoice ledger transfer", err)
		return
	}

	paid, err := scanInvoice(tx.QueryRow(r.Context(), `
		UPDATE commerce.invoices
		SET status = 'paid', paid_at = now(), tb_transfer_id = $2
		WHERE id = $1 AND status = 'issued'
		RETURNING `+invoiceCols, inv.ID, transferID))
	if errors.Is(err, pgx.ErrNoRows) {
		// Lost a concurrent pay race — the deterministic transfer id dedups
		// the ledger posting; re-read and report the true state.
		paid, err = scanInvoice(tx.QueryRow(r.Context(),
			`SELECT `+invoiceCols+` FROM commerce.invoices WHERE id = $1`, inv.ID))
		if err == nil {
			_ = tx.Commit(r.Context())
			writeJSON(w, http.StatusConflict, map[string]any{
				"error": "invoice was concurrently settled; current state returned", "invoice": paid})
			return
		}
	}
	if err != nil {
		h.internal(w, "mark invoice paid", err)
		return
	}
	if err := tx.Commit(r.Context()); err != nil {
		h.internal(w, "commit invoice payment", err)
		return
	}

	if err := h.pub.Publish(r.Context(), "billing.invoice.paid", map[string]any{
		"invoice_id":         paid.ID,
		"billing_account_id": paid.BillingAccountID,
		"amount_minor":       paid.AmountMinor,
		"tb_transfer_id":     transferID,
		"paid_at":            time.Now().UTC().Format(time.RFC3339),
	}); err != nil {
		h.log.Error("failed to publish billing.invoice.paid", zap.Error(err))
	}
	writeJSON(w, http.StatusOK, paid)
}
