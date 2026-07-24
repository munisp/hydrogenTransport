package handlers

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"time"

	"go.uber.org/zap"
)

// OpenData proxies open-dataset search to OpenSearch (open-data-portal module,
// SPEC §3.8) and serves the static dataset catalog.
type OpenData struct {
	searchURL string // OPENSEARCH_URL, e.g. http://opensearch:9200
	index     string // OPENSEARCH_INDEX
	client    *http.Client
	log       *zap.Logger
}

// NewOpenData builds the OpenData handler. searchURL empty disables search (503).
func NewOpenData(searchURL, index string, log *zap.Logger) *OpenData {
	if index == "" {
		index = "h2fleet-open"
	}
	return &OpenData{
		searchURL: strings.TrimSuffix(searchURL, "/"),
		index:     index,
		client:    &http.Client{Timeout: 8 * time.Second},
		log:       log,
	}
}

// Dataset is an open-data catalog entry.
type Dataset struct {
	ID          string `json:"id"`
	Title       string `json:"title"`
	Description string `json:"description"`
	Format      string `json:"format"`
	URL         string `json:"url"`
}

var datasetCatalog = []Dataset{
	{ID: "gtfs-static", Title: "GTFS static feed", Description: "Stops, routes and timetable of the H2 bus network.", Format: "GTFS", URL: "/api/citizen/v1/passenger/routes"},
	{ID: "arrivals", Title: "Stop arrivals", Description: "Next departures per stop (derived from the static timetable).", Format: "JSON", URL: "/api/citizen/v1/passenger/arrivals?stop_id=S001"},
	{ID: "carbon-credits", Title: "CO2 avoidance ledger", Description: "Issued carbon credits per reporting period.", Format: "JSON", URL: "/api/citizen/v1/carbon/credits"},
	{ID: "service-alerts", Title: "Service alerts", Description: "Active service disruptions and notices.", Format: "JSON", URL: "/api/citizen/v1/passenger/alerts"},
}

// ListDatasets handles GET /v1/opendata/datasets.
func (o *OpenData) ListDatasets(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{"datasets": datasetCatalog})
}

// Search handles GET /v1/opendata/search?q= by proxying an OpenSearch
// match query against the open-data index.
func (o *OpenData) Search(w http.ResponseWriter, r *http.Request) {
	if o.searchURL == "" {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "search backend not configured (OPENSEARCH_URL unset)"})
		return
	}
	q := r.URL.Query().Get("q")
	if q == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "q is required"})
		return
	}

	osQuery := map[string]any{
		"size": 20,
		"query": map[string]any{
			"multi_match": map[string]any{
				"query":  q,
				"fields": []string{"title^2", "description", "content"},
			},
		},
	}
	body, _ := json.Marshal(osQuery)

	ctx, cancel := context.WithTimeout(r.Context(), 8*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost,
		o.searchURL+"/"+o.index+"/_search", bytes.NewReader(body))
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "search request build failed"})
		return
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := o.client.Do(req)
	if err != nil {
		o.log.Error("opensearch call failed", zap.Error(err))
		writeJSON(w, http.StatusBadGateway, map[string]string{"error": "search backend unavailable"})
		return
	}
	defer resp.Body.Close()

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(resp.StatusCode)
	if _, err := io.Copy(w, io.LimitReader(resp.Body, 4<<20)); err != nil {
		o.log.Error("streaming search response failed", zap.Error(err))
	}
}
