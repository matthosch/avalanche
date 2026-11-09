// Package config reads service configuration from the environment, with defaults that
// match the local docker-compose stack.
package config

import (
	"fmt"
	"os"
	"strconv"
)

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

// LedgerDatabaseURL is the ledger Postgres connection string.
func LedgerDatabaseURL() string {
	return Env("LEDGER_DATABASE_URL", "postgres://avalanche:avalanche@localhost:5432/ledger")
}

// LedgerURL is the base URL of the ledger's internal REST API, which only api calls.
func LedgerURL() string {
	return Env("LEDGER_URL", "http://localhost:8081")
}

// RedisURL is the Redis connection string.
func RedisURL() string {
	return Env("REDIS_URL", "redis://localhost:6379/0")
}

// PaymentsURL is the payment provider's API base URL: the Stripe-compatible mock locally
// (ADR-0007), or https://api.stripe.com for Stripe test mode.
func PaymentsURL() string {
	return Env("PAYMENTS_URL", "http://localhost:12111")
}

// PaymentsSecretKey is the payment provider's secret API key. The mock accepts any key.
func PaymentsSecretKey() string {
	return Env("PAYMENTS_SECRET_KEY", "sk_test_avalanche")
}

// FeeBPS is the Platform's Fee rate in basis points, one Platform-wide value captured into
// each Order's OrderCompleted event at the moment of the Order.
func FeeBPS() (int, error) {
	v := Env("FEE_BPS", "500")
	bps, err := strconv.Atoi(v)
	if err != nil || bps < 0 || bps > 10000 {
		return 0, fmt.Errorf("config: FEE_BPS must be 0–10000 basis points, got %q", v)
	}
	return bps, nil
}
