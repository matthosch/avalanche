# Ledger writes are events, not RPC

Commerce publishes facts and the ledger owns the accounting. The Order row and its outbox row are written in one transaction. A relay claims outbox rows (`FOR UPDATE SKIP LOCKED`) and publishes `OrderCompleted` to the Redis Stream `orders.completed`, and the ledger consumes it through a consumer group. Nobody waits on the ledger's answer, other services can subscribe, the ledger can be rebuilt by replaying, and it pulls at its own pace, so a ledger outage doesn't affect sales.

## Consequences

- Delivery is at-least-once. Each Journal Entry stores the event's unique `event_id` in the same transaction as its Postings, `XACK` happens only after commit, and a message moves to a dead-letter stream after 5 deliveries.
- `OrderCompleted` carries the Fee rate (`fee_bps`) captured at Order time, so replaying never depends on today's configuration.
- Ledger reads go through internal REST called only by `api`; the browser never calls the ledger.
