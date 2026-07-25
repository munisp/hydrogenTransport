// Package ops implements the NOC/SOC operations feed of admin-api:
//   - GET /v1/admin/health   — concurrent health sweep of all platform
//     services (HTTP /healthz) and middleware (TCP dial)
//   - GET /v1/admin/alerts   — Alertmanager /api/v2/alerts proxy
//   - GET /v1/admin/toggles  — toggle list enriched with domain + owning
//     services; PUT /v1/admin/toggles/{module} proxies the caller's JWT to
//     toggle-service (which owns feature_toggles).
package ops

import (
	"context"
	"net"
	"net/http"
	"sort"
	"sync"
	"time"
)

// Target is one health-sweep check: an HTTP service (/healthz) or a raw TCP
// endpoint (middleware).
type Target struct {
	Name string `json:"name"`
	Kind string `json:"kind"` // "http" | "tcp"
	Addr string `json:"addr"` // http: base URL; tcp: host:port
}

// Check is the per-target result of the sweep.
type Check struct {
	Name      string `json:"name"`
	Kind      string `json:"kind"`
	Target    string `json:"target"`
	Status    string `json:"status"` // "up" | "down"
	LatencyMs int64  `json:"latency_ms"`
}

// HealthResponse is the GET /v1/admin/health payload: a top-level array
// entry per checked target plus a summary.
type HealthResponse struct {
	GeneratedAt time.Time `json:"generated_at"`
	Checks      []Check   `json:"checks"`
	Summary     struct {
		Up   int `json:"up"`
		Down int `json:"down"`
	} `json:"summary"`
}

const checkTimeout = 2 * time.Second

// SweepHealth probes every target concurrently (HTTP GET <addr>/healthz for
// services, TCP dial for middleware) with a 2s per-check timeout.
func SweepHealth(ctx context.Context, targets []Target) HealthResponse {
	resp := HealthResponse{GeneratedAt: time.Now().UTC(), Checks: make([]Check, len(targets))}
	var wg sync.WaitGroup
	for i, t := range targets {
		wg.Add(1)
		go func(i int, t Target) {
			defer wg.Done()
			resp.Checks[i] = check(ctx, t)
		}(i, t)
	}
	wg.Wait()
	for _, c := range resp.Checks {
		if c.Status == "up" {
			resp.Summary.Up++
		} else {
			resp.Summary.Down++
		}
	}
	sort.Slice(resp.Checks, func(i, j int) bool { return resp.Checks[i].Name < resp.Checks[j].Name })
	return resp
}

func check(ctx context.Context, t Target) Check {
	c := Check{Name: t.Name, Kind: t.Kind, Target: t.Addr, Status: "down"}
	cctx, cancel := context.WithTimeout(ctx, checkTimeout)
	defer cancel()
	start := time.Now()
	var up bool
	if t.Kind == "tcp" {
		var d net.Dialer
		conn, err := d.DialContext(cctx, "tcp", t.Addr)
		if err == nil {
			_ = conn.Close()
			up = true
		}
	} else {
		req, err := http.NewRequestWithContext(cctx, http.MethodGet, t.Addr+"/healthz", nil)
		if err == nil {
			res, err := http.DefaultClient.Do(req)
			if err == nil {
				_ = res.Body.Close()
				up = res.StatusCode >= 200 && res.StatusCode < 300
			}
		}
	}
	c.LatencyMs = time.Since(start).Milliseconds()
	if up {
		c.Status = "up"
	}
	return c
}
