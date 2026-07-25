// Package config loads admin-api configuration from environment variables
// (SPEC §3.5 env-config convention).
package config

import "os"

// Config holds the runtime configuration for admin-api.
type Config struct {
	Port           string // PORT (default 8085)
	DatabaseURL    string // DATABASE_URL (required)
	KeycloakIssuer string // KEYCLOAK_ISSUER — JWT validation (JWKS)
	ToggleURL      string // TOGGLE_URL — toggle-service base URL

	// Keycloak Admin REST (onboarding + user management).
	KeycloakAdminURL          string // KEYCLOAK_ADMIN_URL (default http://keycloak:8080)
	KeycloakRealm             string // KEYCLOAK_REALM (default h2fleet)
	KeycloakAdminClientID     string // KEYCLOAK_ADMIN_CLIENT_ID — unset => simulated dev fallback
	KeycloakAdminClientSecret string // KEYCLOAK_ADMIN_CLIENT_SECRET

	AlertmanagerURL string // ALERTMANAGER_URL (default http://alertmanager:9093)

	// Health-sweep service base URLs (each probed at <url>/healthz).
	ToggleServiceURL string // TOGGLE_SERVICE_URL (default http://toggle-service:8080)
	FleetAPIURL      string // FLEET_API_URL (default http://fleet-api:8081)
	InfraAPIURL      string // INFRA_API_URL (default http://infra-api:8082)
	CitizenAPIURL    string // CITIZEN_API_URL (default http://citizen-api:8083)
	CommerceAPIURL   string // COMMERCE_API_URL (default http://commerce-api:8084)
	AdminAPIURL      string // ADMIN_API_URL (default http://admin-api:8085)
	PredictiveURL    string // PREDICTIVE_MAINTENANCE_URL (default http://predictive-maintenance:8090)
	OptimizerURL     string // ROUTE_OPTIMIZER_URL (default http://route-optimizer:8091)
	DigitalTwinURL   string // DIGITAL_TWIN_URL (default http://digital-twin:8092)
	CarbonURL        string // CARBON_ANALYTICS_URL (default http://carbon-analytics:8094)

	// Health-sweep middleware TCP targets (host:port).
	KafkaAddr       string // KAFKA_TCP_ADDR (default kafka:9092)
	PostgresAddr    string // POSTGRES_TCP_ADDR (default postgres:5432)
	RedisAddr       string // REDIS_TCP_ADDR (default redis:6379)
	OpenSearchAddr  string // OPENSEARCH_TCP_ADDR (default opensearch:9200)
	TemporalAddr    string // TEMPORAL_TCP_ADDR (default temporal:7233)
	TigerBeetleAddr string // TIGERBEETLE_TCP_ADDR (default tigerbeetle:3000)
}

func envOr(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

// Load reads env config; PORT defaults to 8085.
func Load() Config {
	return Config{
		Port:           envOr("PORT", "8085"),
		DatabaseURL:    os.Getenv("DATABASE_URL"),
		KeycloakIssuer: os.Getenv("KEYCLOAK_ISSUER"),
		ToggleURL:      envOr("TOGGLE_URL", "http://toggle-service:8080"),

		KeycloakAdminURL:          envOr("KEYCLOAK_ADMIN_URL", "http://keycloak:8080"),
		KeycloakRealm:             envOr("KEYCLOAK_REALM", "h2fleet"),
		KeycloakAdminClientID:     os.Getenv("KEYCLOAK_ADMIN_CLIENT_ID"),
		KeycloakAdminClientSecret: os.Getenv("KEYCLOAK_ADMIN_CLIENT_SECRET"),

		AlertmanagerURL: envOr("ALERTMANAGER_URL", "http://alertmanager:9093"),

		ToggleServiceURL: envOr("TOGGLE_SERVICE_URL", "http://toggle-service:8080"),
		FleetAPIURL:      envOr("FLEET_API_URL", "http://fleet-api:8081"),
		InfraAPIURL:      envOr("INFRA_API_URL", "http://infra-api:8082"),
		CitizenAPIURL:    envOr("CITIZEN_API_URL", "http://citizen-api:8083"),
		CommerceAPIURL:   envOr("COMMERCE_API_URL", "http://commerce-api:8084"),
		AdminAPIURL:      envOr("ADMIN_API_URL", "http://admin-api:8085"),
		PredictiveURL:    envOr("PREDICTIVE_MAINTENANCE_URL", "http://predictive-maintenance:8090"),
		OptimizerURL:     envOr("ROUTE_OPTIMIZER_URL", "http://route-optimizer:8091"),
		DigitalTwinURL:   envOr("DIGITAL_TWIN_URL", "http://digital-twin:8092"),
		CarbonURL:        envOr("CARBON_ANALYTICS_URL", "http://carbon-analytics:8094"),

		KafkaAddr:       envOr("KAFKA_TCP_ADDR", "kafka:9092"),
		PostgresAddr:    envOr("POSTGRES_TCP_ADDR", "postgres:5432"),
		RedisAddr:       envOr("REDIS_TCP_ADDR", "redis:6379"),
		OpenSearchAddr:  envOr("OPENSEARCH_TCP_ADDR", "opensearch:9200"),
		TemporalAddr:    envOr("TEMPORAL_TCP_ADDR", "temporal:7233"),
		TigerBeetleAddr: envOr("TIGERBEETLE_TCP_ADDR", "tigerbeetle:3000"),
	}
}
