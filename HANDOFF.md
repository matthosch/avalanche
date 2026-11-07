# Avalanche: handoff from the design session

A flash-sale commerce platform (a proof of concept in the style of Shopify), built as a portfolio piece to **show off resilience and idempotency**. Repo: https://github.com/matthosch/avalanche. Local path: `C:\Users\matt_\Workspace\avalanche` (Windows host).

The design below was settled in conversation. Nothing has been built yet. Next step: run `/grill-with-docs` on this file (see the end).

## Core framing

The system is a **funnel**: 100k reserve attempts → 500 holds → ~450 payments → ~450 ledger entries. Only the **reserve** endpoint has to survive the flood. At most `stock` sales can succeed, so everything after a hold is low-volume and plain Postgres is fine there.

## Decisions (with reasons)

1. **Redis is the source of truth for inventory during a sale.** Stock is loaded into Redis when the sale starts. Every change goes through Lua scripts (`reserve`, `beginPayment`, `confirm`, `release`), so checking and decrementing stock can never race. An in-memory "sold out" flag in each API process turns requests away before they reach Redis.
2. **Holds expire via a sorted set + reaper, not key-expiry events.** `holds:{sale}` is a sorted set scored by expiry time. A worker releases expired holds with a Lua compare-and-set and puts their stock back. Use Redis server time, not app clocks. Reason: keyspace notifications are fire-and-forget pub/sub (lost if no listener is connected) and can fire late, so stock would leak.
3. **Hold state machine:** `HELD → PAYING → SOLD`; `HELD → RELEASED` (reaper, on expiry); `PAYING → RELEASED` (charge failed). The reaper only ever touches `HELD`. If a charge in `PAYING` times out, ask the payment provider by idempotency key (the hold ID) before releasing. Otherwise we risk selling an item the customer already paid for.
4. **Card data never touches our servers.** Payments use tokens only.
5. **Commerce Postgres** holds catalog, stores, orders and the outbox. The order row and its outbox row are written in one transaction.
6. **Ledger writes are events, not RPC.** Outbox → relay (claims rows with `FOR UPDATE SKIP LOCKED`) → Redis Stream `sales.completed` → ledger consumer group → ledger Postgres. Commerce publishes facts (`SaleCompleted`); the ledger owns the accounting. Reasons: nobody waits on the result; other services can subscribe; the ledger can be rebuilt by replaying; the ledger pulls at its own pace.
7. **The ledger is double-entry.** Amounts are `BIGINT` cents. Each entry's lines must sum to 0, enforced in the database. The ledger is append-only: refunds are reversing entries, never updates. Rounding rule: round the fee, and the merchant gets the remainder. There is no stored running balance on the platform-revenue account, because every sale would lock that one row. Example ($100 sale, 5% fee): Dr Processor receivable 10000 / Cr Merchant payable store_42 9500 / Cr Platform fee revenue 500.
8. **Ledger reads and payouts use internal REST**, called only by `api`, which handles auth. The browser never calls the ledger directly. A payout checks the balance and posts in one transaction, under a per-store lock (`FOR UPDATE` or `pg_advisory_xact_lock`).
9. **Contracts are REST + zod** in a shared `packages/contracts`, covering both event payloads and read endpoints. Switch to Connect + protobuf only if a service is written in another language.
10. **Live dashboard:** `api` reads `sales.completed` as a second consumer group and pushes updates to the browser over SSE. Balances still come from the ledger.
11. **Consumers assume at-least-once delivery.** Each journal entry carries a unique `event_id`, inserted in the same transaction as its postings. `XACK` only after commit. After N deliveries (the count `XPENDING` reports), move the message to a dead-letter stream.
12. **Redis durability:** AOF with `everysec`. A reconciliation job checks Redis sold count = orders = ledger entries and republishes anything missing.

## Additions agreed for the showcase (ranked)

1. OpenTelemetry tracing across the async hop (`traceparent` in stream messages), plus Grafana panels: reserve latency, stream lag, holds by state.
2. `Idempotency-Key` header on checkout. Store the key, a request hash and the response. Same key + same body → replay the saved response; same key + different body → 422; still in progress → 409.
3. A hostile mock payment provider: webhooks that arrive duplicated, out of order, late, and signed with HMAC + a timestamp.
4. Resilience on outbound calls: timeouts, retries with backoff and jitter, a circuit breaker on payments, load shedding (503 + `Retry-After`, via `@fastify/under-pressure`), and graceful shutdown on SIGTERM.
5. Multiple replicas (3 `api`, 2 workers) to prove nothing depends on running a single instance.
6. Concurrent payouts can never overdraw a balance.
7. Sale-start traffic spike: request coalescing, serving slightly stale data while refreshing.
8. Event and schema versioning: `SaleCompleted` v2 alongside v1; expand/contract migrations.

Skip: Kubernetes, Terraform, Kafka, more services, event-sourcing everything.

## Failure-scenario catalog (acceptance tests, run by one chaos command)

| Fault injected | Expected behavior | Proof |
|---|---|---|
| Client sends Pay 3× | One charge, one order | Idempotency-Key test |
| Payment webhook arrives 3×, out of order | One `SOLD` | Webhook fuzz test |
| Payment provider times out | No double charge, no stock released for a paid order | `PAYING` recovery test |
| Ledger down for 60s mid-sale | Sales unaffected; lag climbs then recovers | Stream-lag graph; reconciliation finds 0 differences |
| Worker killed (`kill -9`) mid-reap | No lost stock | Invariant checker |
| Rolling restart of `api` under load | 0 failed requests | k6 error rate |
| 50 concurrent payouts | Balance never negative | Postgres concurrency test |

Invariants checked after every load run: `available + held + paying + sold == initial`; the sum of all postings is 0; orders = ledger entries.

## Proposed layout (TypeScript monorepo, pnpm workspaces)

```
apps/web       storefront + merchant/platform dashboard (Vite/React)
apps/api       hot path: reserve, checkout, BFF (Fastify)
apps/worker    hold reaper + outbox relay
apps/ledger    event consumer + internal REST (Postgres)
packages/contracts  zod schemas: events + endpoints
packages/shared     money utils, shared types
infra/docker-compose.yml   Redis, Postgres (separate databases: commerce, ledger)
load/          k6 scripts
```

## Open questions for `/grill-with-docs` (proposed defaults in brackets)

- Stack: [TypeScript everywhere: Fastify, Vite/React, pnpm]
- Payments: [mock provider, so we can inject failures] vs Stripe test mode
- Hold window: [2 min, configurable]. Per-user limit: [1 item per sale]
- Where it runs: [local docker-compose only]
- **Windows host:** is Docker Desktop (WSL2) available? Use pnpm scripts instead of `make` so commands work on Windows.
- Issue tracker: [GitHub Issues on matthosch/avalanche] vs local `.scratch/`
- Where does the fee rate live (platform-wide or per store plan), and should `SaleCompleted` carry a snapshot of the rate at sale time?
- Glossary terms to pin down in `CONTEXT.md`: "account" (a merchant's login vs. a ledger account), hold vs. reservation, entry vs. posting, sale (the event) vs. order.

## ADRs to record

Redis owns inventory during a sale · sorted-set reaper instead of keyspace notifications · events for ledger writes · REST + zod instead of RPC · double-entry ledger rules (from decision 7).

## Route (from `/ask-matt`)

1. `/setup-matt-pocock-skills`
2. `/grill-with-docs`, seeded with this file → `CONTEXT.md` + ADRs, and settle the open questions
3. `/to-spec` → `/to-tickets` in the same context window. Ticket 1 is a thin end-to-end slice (reserve → mock payment → order → event → ledger entry, no load). After that, roughly one ticket per catalog row, with that row as the acceptance test. Do the inventory core and its load test early, since that's the riskiest part.
4. `/implement` once per ticket, `/clear` between tickets.
