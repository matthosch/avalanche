# Redis owns inventory while a Flash Sale is live

While a Flash Sale is LIVE, and until it is closed out, Redis is the source of truth for its stock. Stock is loaded into Redis when the sale starts, and every change goes through Lua scripts (`reserve`, `beginPayment`, `confirm`, `release`), so a check and a decrement can never race. At Close-out the final counts are written to commerce Postgres, which is the permanent record from then on. We chose this because the `reserve` endpoint takes about 100k attempts in seconds for a few hundred units. Row locks on a Postgres stock row would put every request in a queue on one row. Everything after a Hold is low-volume, so it stays in Postgres.

## Consequences

- Redis runs with AOF `everysec`, so up to ~1 s of inventory changes can be lost on a crash. A reconciliation job (every 30 s while LIVE, and once at Close-out) checks Redis sold count = Orders = Journal Entries and republishes anything missing.
- Each API process keeps a "sold out" flag with a ~250 ms TTL, so a sold-out Flash Sale doesn't hammer Redis, while stock that comes back from released Holds can still be bought.
