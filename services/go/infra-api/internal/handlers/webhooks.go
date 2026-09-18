package handlers

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5"
	"go.uber.org/zap"
)

// Outbound partner webhooks (Wave-6 A3-03, migration 0009 G4): external
// systems (city CAD, transport-authority dashboards) subscribe to the
// incident lifecycle instead of polling or getting internal Kafka access.
// Every delivery is HMAC-SHA256 signed (X-H2Fleet-Signature: sha256=<hex>)
// over the exact request body, so receivers can verify authenticity with
// their subscription secret. Delivery is synchronous with a 5 s budget and
// recorded in infra.webhook_deliveries; failures never fail the incident
// call itself, and failed deliveries can be re-driven via the retry
// endpoint.

// webhookHTTPClient is the bounded client used for deliveries.
var webhookHTTPClient = &http.Client{Timeout: 5 * time.Second}

// WebhookSubscription mirrors infra.webhook_subscriptions (secret redacted
// in every response).
type WebhookSubscription struct {
	ID          string    `json:"id"`
	URL         string    `json:"url"`
	MinSeverity string    `json:"min_severity"`
	Active      bool      `json:"active"`
	Description string    `json:"description"`
	CreatedAt   time.Time `json:"created_at"`
}

const webhookSubCols = `id, url, min_severity, active, description, created_at`

// CreateWebhookSubscription handles POST /v1/webhooks/subscriptions
// (platform-admin). The secret is the HMAC key the receiver verifies with;
// it is stored server-side and never returned by the API.
func (h *Handler) CreateWebhookSubscription(w http.ResponseWriter, r *http.Request) {
	var req struct {
		URL         string `json:"url"`
		Secret      string `json:"secret"`
		MinSeverity string `json:"min_severity"`
		Description string `json:"description"`
	}
	if err := decodeJSON(w, r, &req); err != nil || req.URL == "" || req.Secret == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "body must include \"url\" and \"secret\""})
		return
	}
	if req.MinSeverity == "" {
		req.MinSeverity = "high"
	}
	if _, ok := severityRank[req.MinSeverity]; !ok {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "min_severity must be low, medium, high or critical"})
		return
	}
	var s WebhookSubscription
	err := h.db.QueryRow(r.Context(), `
		INSERT INTO infra.webhook_subscriptions (url, secret, min_severity, description)
		VALUES ($1, $2, $3, $4)
		RETURNING `+webhookSubCols, req.URL, req.Secret, req.MinSeverity, req.Description).
		Scan(&s.ID, &s.URL, &s.MinSeverity, &s.Active, &s.Description, &s.CreatedAt)
	if err != nil {
		h.internal(w, "create webhook subscription", err)
		return
	}
	writeJSON(w, http.StatusCreated, s)
}

// ListWebhookSubscriptions handles GET /v1/webhooks/subscriptions
// (platform-admin).
func (h *Handler) ListWebhookSubscriptions(w http.ResponseWriter, r *http.Request) {
	rows, err := h.db.Query(r.Context(),
		`SELECT `+webhookSubCols+` FROM infra.webhook_subscriptions ORDER BY created_at DESC`)
	if err != nil {
		h.internal(w, "list webhook subscriptions", err)
		return
	}
	defer rows.Close()
	subs := []WebhookSubscription{}
	for rows.Next() {
		var s WebhookSubscription
		if err := rows.Scan(&s.ID, &s.URL, &s.MinSeverity, &s.Active, &s.Description, &s.CreatedAt); err != nil {
			h.internal(w, "scan webhook subscription", err)
			return
		}
		subs = append(subs, s)
	}
	if err := rows.Err(); err != nil {
		h.internal(w, "iterate webhook subscriptions", err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"subscriptions": subs})
}

// DeactivateWebhookSubscription handles DELETE /v1/webhooks/subscriptions/{id}
// (platform-admin): soft-off (active=false) so the delivery history stays.
func (h *Handler) DeactivateWebhookSubscription(w http.ResponseWriter, r *http.Request) {
	tag, err := h.db.Exec(r.Context(), `
		UPDATE infra.webhook_subscriptions SET active = false WHERE id = $1`,
		chi.URLParam(r, "id"))
	if err != nil {
		h.internal(w, "deactivate webhook subscription", err)
		return
	}
	if tag.RowsAffected() == 0 {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "subscription not found"})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"active": false})
}

// dispatchIncidentWebhooks fans an incident lifecycle event out to every
// active subscription whose severity threshold the incident meets. Delivery
// rows are recorded first, then each POST is attempted synchronously with a
// bounded client; any failure is logged and stored, never propagated.
func (h *Handler) dispatchIncidentWebhooks(ctx context.Context, i Incident, eventType string) {
	rows, err := h.db.Query(ctx, `
		SELECT id, url, secret, min_severity FROM infra.webhook_subscriptions WHERE active`)
	if err != nil {
		h.log.Error("load webhook subscriptions", zap.Error(err))
		return
	}
	type sub struct{ id, url, secret, minSeverity string }
	subs := []sub{}
	for rows.Next() {
		var s sub
		if err := rows.Scan(&s.id, &s.url, &s.secret, &s.minSeverity); err != nil {
			h.log.Error("scan webhook subscription", zap.Error(err))
			rows.Close()
			return
		}
		subs = append(subs, s)
	}
	rows.Close()
	if len(subs) == 0 {
		return
	}

	payload, err := json.Marshal(map[string]any{
		"event":    eventType,
		"incident": i,
		"sent_at":  time.Now().UTC().Format(time.RFC3339),
	})
	if err != nil {
		h.log.Error("marshal webhook payload", zap.Error(err))
		return
	}

	for _, s := range subs {
		if severityRank[s.minSeverity] > severityRank[i.Severity] {
			continue
		}
		var deliveryID string
		if err := h.db.QueryRow(ctx, `
			INSERT INTO infra.webhook_deliveries (subscription_id, incident_id, event_type, payload)
			VALUES ($1, $2, $3, $4::jsonb) RETURNING id`,
			s.id, i.ID, eventType, string(payload)).Scan(&deliveryID); err != nil {
			h.log.Error("record webhook delivery", zap.Error(err))
			continue
		}
		h.attemptWebhookDelivery(ctx, deliveryID, s.url, s.secret, eventType, payload)
	}
}

// attemptWebhookDelivery POSTs the signed payload and records the outcome.
func (h *Handler) attemptWebhookDelivery(ctx context.Context, deliveryID, url, secret, eventType string, payload []byte) {
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write(payload)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(payload))
	if err != nil {
		h.recordDeliveryOutcome(ctx, deliveryID, false, err.Error())
		return
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-H2Fleet-Event", eventType)
	req.Header.Set("X-H2Fleet-Signature", "sha256="+hex.EncodeToString(mac.Sum(nil)))

	resp, err := webhookHTTPClient.Do(req)
	if err != nil {
		h.recordDeliveryOutcome(ctx, deliveryID, false, err.Error())
		return
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 4096))
	if resp.StatusCode >= 200 && resp.StatusCode < 300 {
		h.recordDeliveryOutcome(ctx, deliveryID, true, "")
		return
	}
	h.recordDeliveryOutcome(ctx, deliveryID, false, "receiver returned "+resp.Status)
}

func (h *Handler) recordDeliveryOutcome(ctx context.Context, deliveryID string, ok bool, errText string) {
	if ok {
		if _, err := h.db.Exec(ctx, `
			UPDATE infra.webhook_deliveries
			SET status = 'delivered', attempts = attempts + 1, delivered_at = now(), last_error = NULL
			WHERE id = $1`, deliveryID); err != nil {
			h.log.Error("record webhook success", zap.Error(err))
		}
		return
	}
	if _, err := h.db.Exec(ctx, `
		UPDATE infra.webhook_deliveries
		SET status = 'failed', attempts = attempts + 1, last_error = $2
		WHERE id = $1`, deliveryID, errText); err != nil {
		h.log.Error("record webhook failure", zap.Error(err))
	}
}

// RetryWebhookDelivery handles POST /v1/webhooks/deliveries/{id}/retry
// (operator/platform-admin): re-drives a pending/failed delivery.
func (h *Handler) RetryWebhookDelivery(w http.ResponseWriter, r *http.Request) {
	var (
		deliveryID = chi.URLParam(r, "id")
		url, secret, eventType string
		payload []byte
		status string
	)
	err := h.db.QueryRow(r.Context(), `
		SELECT d.status, d.event_type, d.payload, s.url, s.secret
		FROM infra.webhook_deliveries d
		JOIN infra.webhook_subscriptions s ON s.id = d.subscription_id
		WHERE d.id = $1`, deliveryID).Scan(&status, &eventType, &payload, &url, &secret)
	if errors.Is(err, pgx.ErrNoRows) {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "delivery not found"})
		return
	}
	if err != nil {
		h.internal(w, "load webhook delivery", err)
		return
	}
	if status == "delivered" {
		writeJSON(w, http.StatusConflict, map[string]string{"error": "delivery already succeeded"})
		return
	}
	h.attemptWebhookDelivery(r.Context(), deliveryID, url, secret, eventType, payload)
	var finalStatus string
	if err := h.db.QueryRow(r.Context(),
		`SELECT status FROM infra.webhook_deliveries WHERE id = $1`, deliveryID).Scan(&finalStatus); err != nil {
		h.internal(w, "reload webhook delivery", err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"delivery_id": deliveryID, "status": finalStatus})
}
