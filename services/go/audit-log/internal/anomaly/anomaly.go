// Package anomaly implements the in-service insider-threat anomaly detector:
// a sliding-window counter of sensitive actions per actor. When one actor
// exceeds the configured threshold inside the window the detector logs a
// warning, increments a Prometheus counter and fires a best-effort alert to
// Alertmanager (rate-limited per actor by a cooldown).
package anomaly

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"sync"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
	"go.uber.org/zap"
)

var alertsTotal = promauto.NewCounterVec(prometheus.CounterOpts{
	Name: "audit_anomaly_alerts_total",
	Help: "Anomaly alerts fired for actors exceeding the sensitive-action rate threshold.",
}, []string{"actor"})

// Detector counts actions per actor over a sliding window.
type Detector struct {
	threshold int
	window    time.Duration
	cooldown  time.Duration
	amURL     string // Alertmanager base URL ("" = log+counter only)
	log       *zap.Logger
	http      *http.Client
	now       func() time.Time // test hook

	mu      sync.Mutex
	seen    map[string][]time.Time
	alerted map[string]time.Time
}

// New builds a Detector. threshold/window come from config; cooldown is fixed
// at 5 minutes so one burst produces one alert, not a page storm.
func New(threshold int, window time.Duration, amURL string, log *zap.Logger) *Detector {
	return &Detector{
		threshold: threshold,
		window:    window,
		cooldown:  5 * time.Minute,
		amURL:     amURL,
		log:       log,
		http:      &http.Client{Timeout: 2 * time.Second},
		now:       time.Now,
		seen:      make(map[string][]time.Time),
		alerted:   make(map[string]time.Time),
	}
}

// Observe records one sensitive action by actor and fires when the rate
// threshold is crossed. actor empty is ignored (unauthenticated writes are
// rejected upstream anyway).
func (d *Detector) Observe(actor, action, entity string) {
	if actor == "" {
		return
	}
	now := d.now()
	d.mu.Lock()
	// prune outside the window
	kept := d.seen[actor][:0]
	cutoff := now.Add(-d.window)
	for _, t := range d.seen[actor] {
		if t.After(cutoff) {
			kept = append(kept, t)
		}
	}
	kept = append(kept, now)
	d.seen[actor] = kept
	count := len(kept)
	lastAlert, alertedBefore := d.alerted[actor]
	onCooldown := alertedBefore && now.Sub(lastAlert) < d.cooldown
	if count <= d.threshold || onCooldown {
		d.mu.Unlock()
		return
	}
	d.alerted[actor] = now
	d.mu.Unlock()

	d.log.Warn("AUDIT ANOMALY: actor exceeded sensitive-action rate",
		zap.String("actor", actor),
		zap.String("last_action", action),
		zap.String("last_entity", entity),
		zap.Int("count", count),
		zap.Int("threshold", d.threshold),
		zap.Duration("window", d.window))
	alertsTotal.WithLabelValues(actor).Inc()
	d.fireAlertmanager(actor, count)
}

// alert is the Alertmanager v1/v2 POST /api/v1/alerts payload shape.
type alert struct {
	Labels      map[string]string `json:"labels"`
	Annotations map[string]string `json:"annotations"`
	StartsAt    time.Time         `json:"startsAt"`
}

// fireAlertmanager posts a best-effort alert; failures are logged only.
func (d *Detector) fireAlertmanager(actor string, count int) {
	if d.amURL == "" {
		return
	}
	payload, err := json.Marshal([]alert{{
		Labels: map[string]string{
			"alertname": "H2FleetAuditAnomaly",
			"severity":  "warning",
			"service":   "audit-log",
			"actor":     actor,
		},
		Annotations: map[string]string{
			"summary": "Insider-threat anomaly: actor exceeded sensitive-action rate",
		},
		StartsAt: d.now(),
	}})
	if err != nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost,
		d.amURL+"/api/v1/alerts", bytes.NewReader(payload))
	if err != nil {
		return
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := d.http.Do(req)
	if err != nil {
		d.log.Warn("alertmanager notify failed", zap.Error(err))
		return
	}
	_ = resp.Body.Close()
}
