package ops

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"sort"
	"time"

	"github.com/go-chi/chi/v5"
	"go.uber.org/zap"

	"github.com/munisp/hydrogenTransport/services/go/admin-api/internal/httpx"
	"github.com/munisp/hydrogenTransport/services/go/admin-api/internal/modules"
)

// Handler serves the /v1/admin operations feed.
type Handler struct {
	targets         []Target
	alertmanagerURL string
	toggleURL       string
	http            *http.Client
	log             *zap.Logger
}

// NewHandler wires the ops handlers. targets is the health-sweep list
// (services + middleware), alertmanagerURL the Alertmanager base URL and
// toggleURL the toggle-service base URL.
func NewHandler(targets []Target, alertmanagerURL, toggleURL string, log *zap.Logger) *Handler {
	return &Handler{
		targets:         targets,
		alertmanagerURL: alertmanagerURL,
		toggleURL:       toggleURL,
		http:            &http.Client{Timeout: 3 * time.Second},
		log:             log,
	}
}

// Health handles GET /v1/admin/health: concurrent sweep of all targets.
func (h *Handler) Health(w http.ResponseWriter, r *http.Request) {
	httpx.JSON(w, http.StatusOK, SweepHealth(r.Context(), h.targets))
}

// Alerts handles GET /v1/admin/alerts: proxies Alertmanager
// GET /api/v2/alerts. When Alertmanager is unreachable or errors the feed
// must never 5xx the NOC dashboard — but a bare empty array would look like
// "no alerts" (empty-but-plausible), so failures return an explicit degraded
// envelope instead (BUSINESS_LOGIC_AUDIT: honest-null KPI rule).
func (h *Handler) Alerts(w http.ResponseWriter, r *http.Request) {
	req, err := http.NewRequestWithContext(r.Context(), http.MethodGet,
		h.alertmanagerURL+"/api/v2/alerts", nil)
	if err != nil {
		h.degradedAlerts(w)
		return
	}
	resp, err := h.http.Do(req)
	if err != nil {
		h.log.Warn("alertmanager unreachable; returning degraded alert feed", zap.Error(err))
		h.degradedAlerts(w)
		return
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if err != nil || resp.StatusCode != http.StatusOK || !json.Valid(body) {
		h.log.Warn("alertmanager returned non-OK; returning degraded alert feed",
			zap.Int("status", resp.StatusCode))
		h.degradedAlerts(w)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(body)
}

// degradedAlerts answers the alerts feed honestly when Alertmanager cannot
// provide data: alerts is empty AND degraded is true so no consumer mistakes
// the feed for "zero active alerts".
func (h *Handler) degradedAlerts(w http.ResponseWriter) {
	httpx.JSON(w, http.StatusOK, map[string]any{
		"alerts":   []any{},
		"degraded": true,
		"source":   "alertmanager",
	})
}

// ToggleView is one enriched toggle entry.
type ToggleView struct {
	Module         string   `json:"module"`
	Domain         string   `json:"domain"`
	Enabled        bool     `json:"enabled"`
	OwningServices []string `json:"owning_services"`
}

// ListToggles handles GET /v1/admin/toggles: fetches the toggle map from
// toggle-service and enriches each module with its domain and owning
// services (static registry, SPEC §3.1).
func (h *Handler) ListToggles(w http.ResponseWriter, r *http.Request) {
	req, err := http.NewRequestWithContext(r.Context(), http.MethodGet, h.toggleURL+"/v1/toggles", nil)
	if err != nil {
		httpx.Error(w, http.StatusInternalServerError, "failed to build toggle request")
		return
	}
	resp, err := h.http.Do(req)
	if err != nil {
		h.log.Error("toggle-service unreachable", zap.Error(err))
		httpx.Error(w, http.StatusBadGateway, "toggle-service unreachable")
		return
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		httpx.Error(w, http.StatusBadGateway, "toggle-service returned non-OK status")
		return
	}
	var body struct {
		Toggles map[string]bool `json:"toggles"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		httpx.Error(w, http.StatusBadGateway, "toggle-service returned malformed payload")
		return
	}
	views := make([]ToggleView, 0, len(body.Toggles))
	for module, enabled := range body.Toggles {
		info, ok := modules.Registry[module]
		v := ToggleView{Module: module, Enabled: enabled, OwningServices: []string{}}
		if ok {
			v.Domain = info.Domain
			v.OwningServices = info.OwningServices
		} else {
			v.Domain = "unknown"
		}
		views = append(views, v)
	}
	sort.Slice(views, func(i, j int) bool {
		if views[i].Domain != views[j].Domain {
			return views[i].Domain < views[j].Domain
		}
		return views[i].Module < views[j].Module
	})
	httpx.JSON(w, http.StatusOK, map[string]any{"toggles": views})
}

// UpdateToggle handles PUT /v1/admin/toggles/{module}: proxies the mutation
// to toggle-service (PUT /v1/toggles/{module}), forwarding the caller's JWT
// so toggle-service enforces its own platform-admin role check. toggle-service
// remains the sole owner of feature_toggles.
func (h *Handler) UpdateToggle(w http.ResponseWriter, r *http.Request) {
	module := chi.URLParam(r, "module")
	if _, ok := modules.Registry[module]; !ok {
		httpx.Error(w, http.StatusNotFound, "unknown module: "+module)
		return
	}
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, 1<<20))
	if err != nil {
		httpx.Error(w, http.StatusBadRequest, "failed to read body")
		return
	}
	req, err := http.NewRequestWithContext(r.Context(), http.MethodPut,
		h.toggleURL+"/v1/toggles/"+module, bytes.NewReader(body))
	if err != nil {
		httpx.Error(w, http.StatusInternalServerError, "failed to build toggle request")
		return
	}
	req.Header.Set("Content-Type", "application/json")
	if authz := r.Header.Get("Authorization"); authz != "" {
		req.Header.Set("Authorization", authz) // forward caller JWT
	}
	resp, err := h.http.Do(req)
	if err != nil {
		h.log.Error("toggle-service PUT failed", zap.String("module", module), zap.Error(err))
		httpx.Error(w, http.StatusBadGateway, "toggle-service unreachable")
		return
	}
	defer resp.Body.Close()
	respBody, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(resp.StatusCode)
	_, _ = w.Write(respBody)
}
