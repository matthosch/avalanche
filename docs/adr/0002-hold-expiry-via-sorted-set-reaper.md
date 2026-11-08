# Hold expiry uses a sorted set and a reaper, not key-expiry events

Holds that have expired are released by a worker. It scans `holds:{flash_sale}`, a sorted set scored by expiry time (Redis server time, never app clocks), and releases each one with a Lua compare-and-set that only moves `HELD → RELEASED` and puts the stock back. The reaper never touches `PAYING` Holds; a separate `PAYING` sweep handles those (see ADR-0006).

## Considered Options

- **Keyspace notifications on key TTL**: rejected. They are fire-and-forget pub/sub: an event is lost if no listener is connected at that moment, and Redis can fire expiry events late. Either way stock leaks with no way to recover it.
