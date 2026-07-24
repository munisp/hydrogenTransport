// Package config loads service configuration from environment variables (SPEC §3.5).
package config

import "os"

// Config holds the runtime configuration shared by all H2Fleet Go services.
type Config struct {
	Port           string // PORT
	DatabaseURL    string // DATABASE_URL
	KafkaBrokers   string // KAFKA_BROKERS (comma-separated)
	RedisAddr      string // REDIS_ADDR
	ToggleURL      string // TOGGLE_URL
	KeycloakIssuer string // KEYCLOAK_ISSUER
}

// Load reads env config; defaultPort is used when PORT is unset.
func Load(defaultPort string) Config {
	c := Config{
		Port:           os.Getenv("PORT"),
		DatabaseURL:    os.Getenv("DATABASE_URL"),
		KafkaBrokers:   os.Getenv("KAFKA_BROKERS"),
		RedisAddr:      os.Getenv("REDIS_ADDR"),
		ToggleURL:      os.Getenv("TOGGLE_URL"),
		KeycloakIssuer: os.Getenv("KEYCLOAK_ISSUER"),
	}
	if c.Port == "" {
		c.Port = defaultPort
	}
	return c
}
