package handlers

import (
	"context"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"go.uber.org/zap"
)

// Proxies holds upstream base URLs for the digital-twin and route-optimizer
// services (SPEC §3.6: /api/twin/*→digital-twin:8092, /api/optimize/*→route-optimizer:8091).
type Proxies struct {
	TwinURL      string // TWIN_URL, e.g. http://digital-twin:8092
	OptimizerURL string // OPTIMIZER_URL, e.g. http://route-optimizer:8091

	client *http.Client
	log    *zap.Logger
}

// NewProxies builds the proxy handlers.
func NewProxies(twinURL, optimizerURL string, log *zap.Logger) *Proxies {
	return &Proxies{
		TwinURL:      strings.TrimSuffix(twinURL, "/"),
		OptimizerURL: strings.TrimSuffix(optimizerURL, "/"),
		client:       &http.Client{Timeout: 10 * time.Second},
		log:          log,
	}
}

// GetTwin handles GET /v1/vehicles/{id}/twin by proxying the digital-twin
// service's GET /v1/twin/{bus_id} (digital-twin module).
func (p *Proxies) GetTwin(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	p.forward(w, r, http.MethodGet, p.TwinURL+"/v1/twin/"+id, nil)
}

// OptimizeRoute handles POST /v1/optimize/route by proxying the
// route-optimizer service (route-energy-optimizer module).
func (p *Proxies) OptimizeRoute(w http.ResponseWriter, r *http.Request) {
	p.forward(w, r, http.MethodPost, p.OptimizerURL+"/v1/optimize/route", r.Body)
}

func (p *Proxies) forward(w http.ResponseWriter, r *http.Request, method, target string, body io.Reader) {
	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, method, target, body)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "proxy request build failed"})
		return
	}
	req.Header.Set("Content-Type", r.Header.Get("Content-Type"))
	if req.Header.Get("Content-Type") == "" {
		req.Header.Set("Content-Type", "application/json")
	}

	resp, err := p.client.Do(req)
	if err != nil {
		p.log.Error("upstream call failed", zap.String("target", target), zap.Error(err))
		writeJSON(w, http.StatusBadGateway, map[string]string{"error": "upstream service unavailable"})
		return
	}
	defer resp.Body.Close()

	if ct := resp.Header.Get("Content-Type"); ct != "" {
		w.Header().Set("Content-Type", ct)
	} else {
		w.Header().Set("Content-Type", "application/json")
	}
	w.WriteHeader(resp.StatusCode)
	if _, err := io.Copy(w, io.LimitReader(resp.Body, 4<<20)); err != nil {
		p.log.Error("streaming upstream response failed", zap.String("target", target), zap.Error(err))
	}
}
