# The Order is written before the Hold is marked SOLD

When a payment succeeds, commerce first writes the Order and its outbox row in one Postgres transaction (unique on `hold_id`), and only then moves the Hold `PAYING → SOLD` in Redis. Both the confirm response and the payment webhook can trigger this; whichever comes first wins, and the rest are no-ops. The Order is the durable record that payment happened, and the Hold's state in Redis is derived from it.

## Considered Options

- **Redis `SOLD` first**: rejected. A crash between the two writes would count stock as sold with no Order, and reconciliation would have to rebuild the Order from the payment provider.

## Consequences

- A crash between the writes leaves a Hold stuck in `PAYING`. The reaper never touches `PAYING`, so no stock leaks. A `PAYING` sweep (for Holds in `PAYING` longer than 30 s) finishes the move to `SOLD` if the Order exists. Otherwise it retrieves the PaymentIntent by its stored ID and acts on its status.
