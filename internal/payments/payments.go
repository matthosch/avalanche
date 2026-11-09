// Package payments connects to the payment provider through the real stripe-go SDK, so the
// Stripe-compatible mock and Stripe test mode differ only in configuration (ADR-0007).
package payments

import "github.com/stripe/stripe-go/v87"

// NewStripeClient returns a Stripe client that sends every request to baseURL with secretKey.
func NewStripeClient(baseURL, secretKey string) *stripe.Client {
	backends := stripe.NewBackendsWithConfig(&stripe.BackendConfig{URL: stripe.String(baseURL)})
	return stripe.NewClient(secretKey, stripe.WithBackends(backends))
}
