// Package config reads service configuration from the environment, with defaults that
// match the local docker-compose stack.
package config

import "os"

// Env returns the environment variable key, or fallback when it is unset or empty.
func Env(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

// CommerceDatabaseURL is the commerce Postgres connection string.
func CommerceDatabaseURL() string {
	return Env("COMMERCE_DATABASE_URL", "postgres://avalanche:avalanche@localhost:5432/commerce")
}
