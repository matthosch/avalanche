# Avalanche: handoff

A multi-store flash-sale commerce platform (a proof of concept in the style of Shopify), built as a portfolio piece to **show off resilience and idempotency**, with observability evidence for every failure scenario. Repo: https://github.com/matthosch/avalanche. Local path: `C:\Users\matt_\Workspace\avalanche` (Windows host).

The design was settled in a `/grill-with-docs` session on 2026-11-07. Nothing has been built yet.

- Vocabulary: [`GLOSSARY.md`](GLOSSARY.md). Use its terms (Flash Sale, Hold, Order, Journal Entry, Posting, Ledger Account…), not the synonyms it avoids.
- Decisions and their reasons: [`docs/adr/`](docs/adr/). This file summarizes them; when the two disagree, the ADR wins.

## Core framing

The system is a **funnel**: 100k reserve attempts → 500 Holds → ~450 payments → ~450 Journal Entries. Only the **reserve** endpoint has to survive the flood. At most `stock` Orders can succeed, so everything after a Hold is low-volume and plain Postgres is fine there.

## Domain

- The Platform hosts many Stores; one Merchant operates exactly one Store. Any number of Flash Sales can be LIVE at once, including more than one per Store; all inventory state is keyed per Flash Sale.
- A Flash Sale sells N units of **one product** at one price. Lifecycle: `SCHEDULED → LIVE → ENDED → CLOSED`. `reserve` is accepted only while LIVE. A Flash Sale ends at its end time (selling out doesn't end it, because released Holds put stock back). Holds made before the end may still pay until they expire. **Close-out** happens once no `HELD`/`PAYING` Holds remain: final counts are written to commerce Postgres, invariants are checked, and its Redis keys get a TTL.
- Shoppers sign in through a stub (no passwords) that issues a Platform-wide Shopper ID. Limit: **1 Hold per Shopper per Flash Sale**, counting only `HELD`/`PAYING`/`SOLD`; a `RELEASED` Hold frees the Shopper to try again. Enforced inside the `reserve` Lua script.
- Hold window: **2 minutes**, configurable per deploy.
- Flash Sales are created by a seed script / admin endpoint. Dashboards are read-only: a live counter, Orders and Store balance for Merchants; fee revenue for the Platform.
- Hold state machine: `HELD → PAYING → SOLD`; `HELD → RELEASED` (reaper, on expiry); `PAYING → RELEASED` (charge failed).

## Decisions

1. **Redis owns inventory while a Flash Sale is LIVE**, until Close-out; then Postgres is the record. Lua scripts `reserve`, `beginPayment`, `confirm`, `release`. Each API process has a sold-out flag with a ~250 ms TTL. → ADR-0001
2. **Hold expiry: sorted set + reaper**, scored by Redis server time; Lua compare-and-set; the reaper only touches `HELD`. → ADR-0002
3. **Payments** go through `stripe-go` against a Stripe-compatible mock. `beginPayment` creates the PaymentIntent (`Idempotency-Key = hold_id`, `metadata.hold_id`), stores the PaymentIntent ID on the Hold, then confirms. Recovery retrieves by that ID; a create that timed out is re-sent with the same key. Card data never touches our servers. → ADR-0007
4. **Marking SOLD:** both the confirm response and the webhook count; the first wins, later ones are no-ops, and `payment_failed` is ignored once a Hold is `SOLD`. The **Order + outbox are written first** (unique on `hold_id`), then Redis `PAYING → SOLD`. A `PAYING` sweep (Holds in `PAYING` > 30 s) repairs the gap. → ADR-0006
5. **Commerce Postgres** holds catalog, Stores, Flash Sales, Orders, idempotency keys and the outbox.
6. **Ledger writes are events:** outbox → relay (`FOR UPDATE SKIP LOCKED`) → Redis Stream `orders.completed` (`OrderCompleted` events) → ledger consumer group → ledger Postgres. `OrderCompleted` carries `fee_bps`. → ADR-0003
7. **Double-entry, append-only ledger**, `BIGINT` cents, USD only. Postings sum to 0, enforced in the database. Round the Fee; the Store gets the remainder. No stored running balance. Ledger Accounts: `processor_receivable`, `store_payable:{store_id}`, `platform_fee_revenue`. No processor fees modeled. → ADR-0004
8. **Ledger reads** use internal REST, called only by `api`.
9. **Go backend + OpenAPI contracts.** One OpenAPI spec for `api` → `oapi-codegen` (Go) + `openapi-typescript` (web). Events are shared Go structs with `schema_version`. → ADR-0005
10. **Live dashboard:** `api` reads `orders.completed` as a second consumer group and pushes updates over SSE. Balances come from the ledger.
11. **At-least-once consumers:** each Journal Entry stores its unique `event_id` in the same transaction as its Postings; `XACK` after commit; dead-letter after **5** deliveries.
12. **Redis durability:** AOF `everysec`. Reconciliation (every 30 s while LIVE, and once at Close-out) checks Redis sold count = Orders = Journal Entries and republishes anything missing.
13. **Checkout idempotency:** `POST /flash-sales/{flash_sale_id}/holds/{id}/pay` takes an `Idempotency-Key` header, scoped to `(shopper_id, key)` and stored in commerce Postgres (key, request hash, status, saved response), kept 24h. Same key + same body → replay the saved response; different body → 422; still in progress → 409.
14. **IDs:** UUIDv7 everywhere. Postgres 18 `DEFAULT uuidv7()`; Hold IDs are generated in Go (`uuid.NewV7()`), since Holds are born in Redis.

## Stripe-compatible mock (`cmd/payments-mock`)

- `POST /v1/payment_intents` (with `Idempotency-Key`), `POST /v1/payment_intents/:id/confirm`, `GET /v1/payment_intents/:id`.
- Test payment methods `pm_card_visa`, `pm_card_chargeDeclined`.
- Webhooks `payment_intent.succeeded`, `payment_intent.payment_failed`, signed with the real `Stripe-Signature: t=…,v1=…` scheme so `webhook.ConstructEvent` verifies them unchanged.
- A separate **chaos control API** (not part of Stripe): duplicate, reorder and delay webhooks; time out or decline charges.

## Observability (MVP)

- OpenTelemetry Collector → Prometheus (metrics), Tempo (traces), Loki (logs), all in Grafana, as separate containers in compose.
- `traceparent` carried in stream messages, so one Order's trace runs api → outbox → relay → ledger.
- Exemplars linking latency panels to traces.
- SLO burn-rate alerts (see targets).
- k6 metrics sent to Prometheus, so load and the system's response share one timeline.
- One saved Grafana dashboard per failure-catalog row: the evidence for each scenario.

## Targets

- Load: 100k reserve attempts in 10 s against a 500-unit Flash Sale, on one laptop under docker-compose.
- Reserve SLO: 99.9% non-5xx (a sold-out 409 counts as success); p99 < 50 ms.
- Funnel lag SLO: an Order appears in the ledger within 5 s at p99 in steady state.

These targets are written down up front; real measurements may tune them.

## Failure-scenario catalog (MVP acceptance tests, run by `cmd/chaos`)

| Fault injected | Expected behavior | Proof |
|---|---|---|
| Client sends Pay 3× | One charge, one Order | Idempotency-Key test |
| Payment webhook arrives 3×, out of order | One `SOLD` | Webhook fuzz test |
| Payment provider times out | No double charge, no stock released for a paid Order | `PAYING` recovery test |
| Ledger down for 60s mid-sale | Sales unaffected; lag climbs then recovers | Stream-lag graph; reconciliation finds 0 differences |
| Worker killed (`kill -9`) mid-reap | No lost stock | Invariant checker |

Invariants checked after every load run: `available + held + paying + sold == initial`; the sum of all Postings is 0; Orders = Journal Entries.

`cmd/chaos` brings up compose, runs k6, injects each fault (Docker API for `kill -9` / pausing the ledger; the mock's chaos API for payment faults), runs the invariant checker (a reusable package), and prints a pass/fail table per row. Go integration tests (testcontainers) cover the Lua scripts and ledger constraints separately.

## Layout

```
cmd/api            hot path: reserve, checkout, webhooks, BFF, SSE
cmd/worker         hold reaper, PAYING sweep, outbox relay, reconciliation
cmd/ledger         event consumer + internal REST
cmd/payments-mock  Stripe-compatible subset + chaos control API
cmd/chaos          failure-catalog runner + invariant checker
internal/...       shared Go packages (events, money, invariants, redis scripts)
api/openapi.yaml   contract for cmd/api
web/               storefront + Merchant/Platform dashboards (Vite/React/TS, pnpm)
infra/docker-compose.yml   Redis, Postgres 18 (databases: commerce, ledger), OTel Collector, Prometheus, Tempo, Loki, Grafana
load/              k6 scripts
Taskfile.yml       task runner (works on Windows; no make)
```

Environment: Docker Desktop (WSL2 backend) is installed; start it before running compose. Node v26 is installed; enable pnpm with `corepack enable` and pin it via `"packageManager"`.

## Nice-to-have (not in MVP; never put these in acceptance criteria)

- Payouts (per-Store lock, balance check + posting in one transaction) and the "50 concurrent payouts → balance never negative" row.
- Refunds as reversing Journal Entries.
- Multiple replicas (3 `api`, 2 workers) and the "rolling restart under load → 0 failed requests" row.
- Resilience kit: timeouts, retries with backoff + jitter, circuit breaker on payments, load shedding (503 + `Retry-After`), graceful shutdown on SIGTERM.
- Sale-start traffic spike handling: request coalescing, serving slightly stale data while refreshing.
- Event and schema versioning (`OrderCompleted` v2 alongside v1; expand/contract migrations).
- Pub/sub "stock returned" message to clear the sold-out flag early.
- Flash Sale creation UI for Merchants.
- Real auth, per-Store-plan Fee rates, processor fees, multiple currencies.
- Real Stripe test mode.
- Pyroscope continuous profiling, Alertmanager routing, runbooks.

Skip entirely: Kubernetes, Terraform, Kafka, more services, event-sourcing everything.

## Next steps

1. `/to-spec` → `/to-tickets` in the same context window. Ticket 1 is a thin end-to-end slice (reserve → mock payment → Order → event → Journal Entry, no load). After that, roughly one ticket per catalog row, with that row as the acceptance test. Do the inventory core and its load test early, since that's the riskiest part. Label tickets `ready-for-human`.
2. Implement once per ticket, `/clear` between tickets.
