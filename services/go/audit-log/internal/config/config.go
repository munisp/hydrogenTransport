// Package config loads audit-log configuration from environment variables
// (SPEC §3.5 env-config convention).
package config

import (
	"os"
	"strconv"
	"time"
)

// Config holds the runtime configuration for audit-log.
type Config struct {
	Port           string // PORT (default 8086)
	DatabaseURL    string // DATABASE_URL (required)
	KeycloakIssuer string // KEYCLOAK_ISSUER — JWT validation (JWKS)

	// IngestToken is the shared-secret service-to-service token accepted on
	// POST /v1/audit via the X-Audit-Token header (LEAK-ingest-style). When
	// empty, only JWT bearer tokens are accepted.
	IngestToken string // AUDIT_INGEST_TOKEN

	OpenSearchURL   string // OPENSEARCH_URL (default http://opensearch:9200; empty disables mirror)
	OpenSearchIndex string // OPENSEARCH_INDEX (default h2fleet-audit)

	AlertmanagerURL string // ALERTMANAGER_URL (default http://alertmanager:9093)

	// Anomaly detection: more than AnomalyThreshold sensitive actions per
	// AnomalyWindow from a single actor triggers a log + Prometheus counter +
	// best-effort Alertmanager alert.
	AnomalyThreshold int           // AUDIT_ANOMALY_THRESHOLD (default 20)
	AnomalyWindow    time.Duration // AUDIT_ANOMALY_WINDOW (default 1m)
}

func envOr(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

// Load reads env config; PORT defaults to 8086.
func Load() Config {
	threshold := 20
	if v := os.Getenv("AUDIT_ANOMALY_THRESHOLD"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			threshold = n
		}
	}
	window := time.Minute
	if v := os.Getenv("AUDIT_ANOMALY_WINDOW"); v != "" {
		if d, err := time.ParseDuration(v); err == nil && d > 0 {
			window = d
		}
	}
	return Config{
		Port:             envOr("PORT", "8086"),
		DatabaseURL:      os.Getenv("DATABASE_URL"),
		KeycloakIssuer:   os.Getenv("KEYCLOAK_ISSUER"),
		IngestToken:      os.Getenv("AUDIT_INGEST_TOKEN"),
		OpenSearchURL:    envOr("OPENSEARCH_URL", "http://opensearch:9200"),
		OpenSearchIndex:  envOr("OPENSEARCH_INDEX", "h2fleet-audit"),
		AlertmanagerURL:  envOr("ALERTMANAGER_URL", "http://alertmanager:9093"),
		AnomalyThreshold: threshold,
		AnomalyWindow:    window,
	}
}
