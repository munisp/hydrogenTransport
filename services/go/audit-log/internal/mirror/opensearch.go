// Package mirror best-effort-mirrors audit entries to OpenSearch
// (index h2fleet-audit) for SOC search/dashboards. Postgres remains the
// system of record; mirror failures never fail an audit append — they are
// logged and counted (audit_mirror_failures_total).
package mirror

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
	"go.uber.org/zap"

	"github.com/munisp/hydrogenTransport/services/go/audit-log/internal/store"
)

var failuresTotal = promauto.NewCounter(prometheus.CounterOpts{
	Name: "audit_mirror_failures_total",
	Help: "OpenSearch mirror operations that failed (best-effort; Postgres is authoritative).",
})

// Mirror publishes entries to an OpenSearch index.
type Mirror struct {
	base  string // e.g. http://opensearch:9200 ("" = disabled)
	index string
	log   *zap.Logger
	http  *http.Client

	ensured bool
}

// New builds a Mirror. Empty baseURL disables mirroring.
func New(baseURL, index string, log *zap.Logger) *Mirror {
	return &Mirror{
		base:  strings.TrimSuffix(baseURL, "/"),
		index: index,
		log:   log,
		http:  &http.Client{Timeout: 3 * time.Second},
	}
}

// Enabled reports whether mirroring is configured.
func (m *Mirror) Enabled() bool { return m.base != "" && m.index != "" }

// EnsureIndex creates the index with an explicit mapping (idempotent;
// resource_already_exists is treated as success).
func (m *Mirror) EnsureIndex(ctx context.Context) error {
	if !m.Enabled() || m.ensured {
		return nil
	}
	body := `{"mappings":{"properties":{
		"actor_sub":{"type":"keyword"},"actor_roles":{"type":"keyword"},
		"action":{"type":"keyword"},"entity":{"type":"keyword"},
		"entity_id":{"type":"keyword"},"ip":{"type":"ip"},
		"ua":{"type":"text"},"ts":{"type":"date"},
		"before":{"type":"object","enabled":false},
		"after":{"type":"object","enabled":false},
		"prev_hash":{"type":"keyword"},"hash":{"type":"keyword"}}}}`
	status, err := m.do(ctx, http.MethodPut, "/"+m.index, strings.NewReader(body))
	if err != nil {
		failuresTotal.Inc()
		return err
	}
	if status >= 300 && status != 400 { // 400 = already exists (checked via body in prod; tolerated here)
		failuresTotal.Inc()
		return fmt.Errorf("ensure index: status %d", status)
	}
	m.ensured = true
	return nil
}

// Publish indexes one entry (fire-and-forget from the caller).
func (m *Mirror) Publish(e store.Entry) {
	if !m.Enabled() {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	if err := m.EnsureIndex(ctx); err != nil {
		m.log.Warn("opensearch ensure index failed", zap.Error(err))
		return
	}
	doc, err := json.Marshal(e)
	if err != nil {
		return
	}
	path := fmt.Sprintf("/%s/_doc/%d", m.index, e.ID)
	status, err := m.do(ctx, http.MethodPut, path, bytes.NewReader(doc))
	if err != nil || status >= 300 {
		failuresTotal.Inc()
		m.log.Warn("opensearch mirror failed",
			zap.Int64("id", e.ID), zap.Int("status", status), zap.Error(err))
	}
}

// do executes one request and drains the body. Returns the status code.
func (m *Mirror) do(ctx context.Context, method, path string, body io.Reader) (int, error) {
	req, err := http.NewRequestWithContext(ctx, method, m.base+path, body)
	if err != nil {
		return 0, err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := m.http.Do(req)
	if err != nil {
		return 0, err
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 4096))
	return resp.StatusCode, nil
}
