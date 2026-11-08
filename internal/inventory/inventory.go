// Package inventory owns a Flash Sale's stock in Redis from the moment it goes LIVE until
// Close-out (ADR-0001). Every change is one Lua script, so a check and its change are atomic.
package inventory

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"time"

	"github.com/google/uuid"
	"github.com/redis/go-redis/v9"
)

// ErrNotLoaded means the Flash Sale's stock has never been loaded into Redis.
var ErrNotLoaded = errors.New("inventory: flash sale not loaded")

// Counts is where a Flash Sale's units are right now. For a loaded Flash Sale,
// Available + Held + Paying + Sold always equals the units it was loaded with.
type Counts struct {
	Available int
	Held      int
	Paying    int
	Sold      int
}

// Inventory is the Redis-backed inventory of every Flash Sale.
type Inventory struct {
	rdb redis.UniversalClient
}

// New returns an Inventory backed by rdb.
func New(rdb redis.UniversalClient) *Inventory {
	return &Inventory{rdb: rdb}
}

// load sets up a Flash Sale's counts unless they already exist, so stock is loaded once
// however many times Load runs.
var load = redis.NewScript(`
if redis.call('EXISTS', KEYS[1]) == 1 then
  return 0
end
redis.call('HSET', KEYS[1], 'available', ARGV[1], 'held', 0, 'paying', 0, 'sold', 0)
return 1
`)

// Load puts units of stock into Redis for a Flash Sale going LIVE. Loading a Flash Sale that
// is already loaded changes nothing.
func (inv *Inventory) Load(ctx context.Context, flashSaleID uuid.UUID, units int) error {
	if units <= 0 {
		return fmt.Errorf("inventory: load %s: units must be positive, got %d", flashSaleID, units)
	}
	if err := load.Run(ctx, inv.rdb, []string{countsKey(flashSaleID)}, units).Err(); err != nil {
		return fmt.Errorf("inventory: load %s: %w", flashSaleID, err)
	}
	return nil
}

// Counts returns where a loaded Flash Sale's units are, or ErrNotLoaded.
func (inv *Inventory) Counts(ctx context.Context, flashSaleID uuid.UUID) (Counts, error) {
	values, err := inv.rdb.HMGet(ctx, countsKey(flashSaleID), "available", "held", "paying", "sold").Result()
	if err != nil {
		return Counts{}, fmt.Errorf("inventory: counts %s: %w", flashSaleID, err)
	}
	var n [4]int
	for i, v := range values {
		s, ok := v.(string)
		if !ok {
			return Counts{}, ErrNotLoaded
		}
		if n[i], err = strconv.Atoi(s); err != nil {
			return Counts{}, fmt.Errorf("inventory: counts %s: %w", flashSaleID, err)
		}
	}
	return Counts{Available: n[0], Held: n[1], Paying: n[2], Sold: n[3]}, nil
}

// Now returns Redis server time, the one clock every Flash Sale and Hold deadline is
// measured against, so app servers' clocks never matter.
func (inv *Inventory) Now(ctx context.Context) (time.Time, error) {
	now, err := inv.rdb.Time(ctx).Result()
	if err != nil {
		return time.Time{}, fmt.Errorf("inventory: redis time: %w", err)
	}
	return now.UTC(), nil
}

// countsKey is the hash of a Flash Sale's counts. The braces keep all of a Flash Sale's keys
// in one hash slot, so scripts touching several of them stay valid on a cluster.
func countsKey(flashSaleID uuid.UUID) string {
	return "inventory:{" + flashSaleID.String() + "}:counts"
}
