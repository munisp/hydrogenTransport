// Package auditclient is the shared, best-effort audit-emission client used
// by H2Fleet services to report sensitive mutations to the audit-log service
// (services/go/audit-log). It is deliberately tiny: one synchronous POST with
// a short timeout, a noop when AUDIT_LOG_URL is unset, and a chi middleware
// factory that records successful mutating requests.
//
// Emission never blocks business logic on failure: errors are logged as
// warnings (the caller's request still succeeds) and should be alerted on via
// the audit_emit_failures_total Prometheus counter.
package auditclient

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
	"go.uber.org/zap"

	auth "github.com/munisp/hydrogenTransport/packages/go-auth"
)

var emitFailures = promauto.NewCounterVec(prometheus.CounterOpts{
	Name: "audit_emit_failures_total",
	Help: "Failed audit-log emission attempts (best-effort; counted for alerting).",
}, []string{"service"})

// Entry is the POST /v1/audit payload (subset; audit-log assigns id/hashes).
type Entry struct {
	ActorSub   string           `json:"actor_sub"`
	ActorRoles []string         `json:"actor_roles,omitempty"`
	Action     string           `json:"action"`
	Entity     string           `json:"entity"`
	EntityID   string           `json:"entity_id,omitempty"`
	Before     *json.RawMessage `json:"before,omitempty"`
	After      *json.RawMessage `json:"after,omitempty"`
	IP         string           `json:"ip,omitempty"`
	UA         string           `json:"ua,omitempty"`
}

// Client emits entries to audit-log. The zero value is valid and disabled.
type Client struct {
	base    string // audit-log base URL ("" = disabled)
	token   string // X-Audit-Token shared secret (may be "")
	service string // emitting service name (metric label)
	hc      *http.Client
	log     *zap.Logger
}

// New builds a Client. Empty baseURL yields a disabled (noop) client so
// local dev without audit-log keeps working.
func New(baseURL, token, service string, log *zap.Logger) *Client {
	return &Client{
		base:    strings.TrimSuffix(baseURL, "/"),
		token:   token,
		service: service,
		hc:      &http.Client{Timeout: 1500 * time.Millisecond},
		log:     log,
	}
}

// FromEnv builds a Client from AUDIT_LOG_URL / AUDIT_INGEST_TOKEN.
// Empty AUDIT_LOG_URL disables emission.
func FromEnv(service string, log *zap.Logger, getenv func(string) string) *Client {
	c := New(getenv("AUDIT_LOG_URL"), getenv("AUDIT_INGEST_TOKEN"), service, log)
	if !c.Enabled() {
		log.Warn("AUDIT_LOG_URL not set; audit emission disabled")
	}
	return c
}

// Enabled reports whether emission is configured.
func (c *Client) Enabled() bool { return c != nil && c.base != "" }

// Emit posts one entry synchronously (1.5s timeout). Best-effort: failures
// are logged + counted, never returned.
func (c *Client) Emit(ctx context.Context, e Entry) {
	if !c.Enabled() {
		return
	}
	body, err := json.Marshal(e)
	if err != nil {
		return
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.base+"/v1/audit", bytes.NewReader(body))
	if err != nil {
		return
	}
	req.Header.Set("Content-Type", "application/json")
	if c.token != "" {
		req.Header.Set("X-Audit-Token", c.token)
	}
	resp, err := c.hc.Do(req)
	if err != nil {
		emitFailures.WithLabelValues(c.service).Inc()
		c.log.Warn("audit emission failed", zap.String("action", e.Action), zap.Error(err))
		return
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 2048))
	if resp.StatusCode >= 300 {
		emitFailures.WithLabelValues(c.service).Inc()
		c.log.Warn("audit emission rejected",
			zap.String("action", e.Action), zap.Int("status", resp.StatusCode))
	}
}

// RolesFromContext extracts Keycloak realm roles from validated JWT claims.
func RolesFromContext(ctx context.Context) []string {
	claims := auth.ClaimsFromContext(ctx)
	if claims == nil {
		return nil
	}
	ra, _ := claims["realm_access"].(map[string]any)
	raw, _ := ra["roles"].([]any)
	roles := make([]string, 0, len(raw))
	for _, v := range raw {
		if s, ok := v.(string); ok {
			roles = append(roles, s)
		}
	}
	return roles
}

// Middleware returns a chi-compatible middleware that emits an audit entry
// after the wrapped handler responds, but only for successful responses
// (status < 400) — rejected/forbidden attempts are already covered by the
// service logs, and audit records what actually changed.
//
//   - action:  dotted verb, e.g. "toggle.update", "payment.create"
//   - entity:  audited entity, e.g. "feature_toggle", "fare_payment"
//   - idParam: chi URL param holding the entity id ("" when none)
//   - captureBody: record the JSON request body as the entry's `after`
//
// actor_sub/actor_roles/ip/ua are taken from the request context (the JWT
// middleware must run before this middleware). A nil or disabled client is a
// pass-through.
func (c *Client) Middleware(action, entity, idParam string, captureBody bool) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if !c.Enabled() {
				next.ServeHTTP(w, r)
				return
			}
			var bodyCopy []byte
			if captureBody && r.Body != nil {
				bodyCopy, _ = io.ReadAll(io.LimitReader(r.Body, 64<<10))
				r.Body = io.NopCloser(bytes.NewReader(bodyCopy))
			}
			ww := middleware.NewWrapResponseWriter(w, r.ProtoMajor)
			next.ServeHTTP(ww, r)
			if status := ww.Status(); status >= 400 {
				return
			}
			e := Entry{
				ActorSub:   auth.Subject(r.Context()),
				ActorRoles: RolesFromContext(r.Context()),
				Action:     action,
				Entity:     entity,
				IP:         r.RemoteAddr,
				UA:         r.UserAgent(),
			}
			if idParam != "" {
				e.EntityID = chi.URLParam(r, idParam)
			}
			if captureBody && len(bodyCopy) > 0 && json.Valid(bodyCopy) {
				rm := json.RawMessage(bodyCopy)
				e.After = &rm
			}
			c.Emit(r.Context(), e)
		})
	}
}
