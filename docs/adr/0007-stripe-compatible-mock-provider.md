# The mock payment provider is a Stripe-compatible subset

Payments go through the real `stripe-go` SDK pointed at our own mock, so switching to Stripe test mode is a config change. The mock copies a subset of Stripe: PaymentIntents create/confirm/retrieve with `Idempotency-Key`, test payment methods (`pm_card_visa`, `pm_card_chargeDeclined`), and `payment_intent.succeeded` / `payment_intent.payment_failed` webhooks signed with Stripe's `Stripe-Signature` scheme. A separate chaos control API (not part of Stripe) injects duplicate, reordered, delayed and timed-out webhooks and declines. Card data never reaches our servers.

## Considered Options

- **Stripe test mode**: can't produce duplicate, out-of-order or late webhooks, or timeouts, on demand.
- **Stripe's official `stripe-mock`**: stateless, with no webhooks and no failure injection.

## Consequences

- Stripe can't look up a payment by idempotency key. So a Hold creates its PaymentIntent (`Idempotency-Key = hold_id`, `metadata.hold_id`), stores the returned ID, then confirms. Recovery retrieves by that ID, and a create that timed out is re-sent with the same key.
