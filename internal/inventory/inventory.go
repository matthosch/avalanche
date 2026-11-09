// Package inventory owns a Flash Sale's stock in Redis from the moment it goes LIVE until
// Close-out (ADR-0001). Every change is one Lua script, so a check and its change are atomic.
package inventory

import (
	"context"
	_ "embed"
	"errors"
	"fmt"
	"strconv"
	"time"

	"github.com/google/uuid"
	"github.com/redis/go-redis/v9"
)

// ErrNotLoaded means the Flash Sale's stock has never been loaded into Redis.
var ErrNotLoaded = errors.New("inventory: flash sale not loaded")

// ErrNotLive means Reserve found the Flash Sale not LIVE: never loaded, so not started, or
// past its end time.
var ErrNotLive = errors.New("inventory: flash sale not live")

// ErrSoldOut means Reserve found no units available. Released Holds put units back, so a
// sold-out Flash Sale can have units again later.
var ErrSoldOut = errors.New("inventory: flash sale sold out")

// ErrAlreadyHolding means Reserve found the Shopper already has a HELD, PAYING or SOLD Hold
// on the Flash Sale: the limit is one per Shopper per Flash Sale. Reserve returns it as an
// *AlreadyHoldingError carrying that Hold.
var ErrAlreadyHolding = errors.New("inventory: shopper already holding")

// AlreadyHoldingError is Reserve's refusal when the Shopper already holds a unit of the Flash
// Sale. It carries their Hold, read in the same script that refused, so a Shopper who lost the
// reply to their first reserve, or retried it, still gets the Hold. It matches
// ErrAlreadyHolding with errors.Is.
type AlreadyHoldingError struct {
	Hold Hold
}

func (e *AlreadyHoldingError) Error() string {
	return fmt.Sprintf("%v: hold %s", ErrAlreadyHolding, e.Hold.ID)
}

func (e *AlreadyHoldingError) Is(target error) bool { return target == ErrAlreadyHolding }

// refusals maps each reason the reserve script gives for not creating a Hold to its error.
var refusals = map[string]error{
	"NOT_LIVE": ErrNotLive,
	"SOLD_OUT": ErrSoldOut,
}

// Counts is where a Flash Sale's units are right now. For a loaded Flash Sale,
// Available + Held + Paying + Sold always equals the units it was loaded with.
type Counts struct {
	Available int
	Held      int
	Paying    int
	Sold      int
}

// HoldState is where a Hold is in its life: HELD → PAYING → SOLD, or RELEASED.
type HoldState string

const (
	Held     HoldState = "HELD"
	Paying   HoldState = "PAYING"
	Sold     HoldState = "SOLD"
	Released HoldState = "RELEASED"
)

// Hold is a Shopper's temporary claim on one unit of a Flash Sale.
type Hold struct {
	ID          uuid.UUID
	FlashSaleID uuid.UUID
	ShopperID   uuid.UUID
	State       HoldState
	// ExpiresAt is when the Hold window closes, on Redis server time.
	ExpiresAt time.Time
}

// HoldWindow is how long a Shopper has to pay for a Hold.
const HoldWindow = 2 * time.Minute

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
//
//go:embed scripts/load.lua
var loadSrc string
var load = redis.NewScript(loadSrc)

// Load puts units of stock into Redis for a Flash Sale going LIVE, with the end time after
// which it takes no more Holds. Loading a Flash Sale that is already loaded changes nothing.
func (inv *Inventory) Load(ctx context.Context, flashSaleID uuid.UUID, units int, endsAt time.Time) error {
	if units <= 0 {
		return fmt.Errorf("inventory: load %s: units must be positive, got %d", flashSaleID, units)
	}
	if err := load.Run(ctx, inv.rdb, []string{countsKey(flashSaleID)}, units, endsAt.UnixMilli()).Err(); err != nil {
		return fmt.Errorf("inventory: load %s: %w", flashSaleID, err)
	}
	return nil
}

//go:embed scripts/reserve.lua
var reserveSrc string
var reserve = redis.NewScript(reserveSrc)

// Reserve gives a Shopper a HELD Hold on one unit of a Flash Sale, expiring HoldWindow from
// now. It decides whether the Flash Sale is LIVE from Redis alone (ADR-0001): the stock must
// be loaded and Redis server time before the end time given to Load; otherwise ErrNotLive.
// With no units available it refuses with ErrSoldOut.
//
// A Shopper who already has a HELD, PAYING or SOLD Hold on the Flash Sale gets an
// *AlreadyHoldingError carrying it, even after the end time, so reserving again is how a
// Shopper recovers a Hold whose reply they lost.
func (inv *Inventory) Reserve(ctx context.Context, flashSaleID, shopperID uuid.UUID) (Hold, error) {
	holdID, err := uuid.NewV7()
	if err != nil {
		return Hold{}, fmt.Errorf("inventory: reserve %s: %w", flashSaleID, err)
	}
	keys := []string{countsKey(flashSaleID), shoppersKey(flashSaleID), holdsKey(flashSaleID), expiriesKey(flashSaleID)}
	args := []any{shopperID.String(), holdID.String(), HoldWindow.Milliseconds()}
	result, err := reserve.Run(ctx, inv.rdb, keys, args...).Result()
	if err != nil {
		return Hold{}, fmt.Errorf("inventory: reserve %s: %w", flashSaleID, err)
	}
	switch r := result.(type) {
	case int64:
		return newHold(holdID, flashSaleID, shopperID, Held, r), nil
	case []any:
		existing, err := existingHold(flashSaleID, shopperID, r)
		if err != nil {
			return Hold{}, fmt.Errorf("inventory: reserve %s: %w", flashSaleID, err)
		}
		return Hold{}, &AlreadyHoldingError{Hold: existing}
	}
	if refusal, ok := refusals[fmt.Sprint(result)]; ok {
		return Hold{}, refusal
	}
	return Hold{}, fmt.Errorf("inventory: reserve %s: unexpected result %v", flashSaleID, result)
}

// existingHold decodes the reserve script's {'ALREADY_HOLDING', id, state, expires_at}.
func existingHold(flashSaleID, shopperID uuid.UUID, r []any) (Hold, error) {
	if len(r) != 4 || r[0] != "ALREADY_HOLDING" {
		return Hold{}, fmt.Errorf("unexpected result %v", r)
	}
	id, err := uuid.Parse(fmt.Sprint(r[1]))
	if err != nil {
		return Hold{}, fmt.Errorf("existing hold id: %w", err)
	}
	expiresAt, err := strconv.ParseInt(fmt.Sprint(r[3]), 10, 64)
	if err != nil {
		return Hold{}, fmt.Errorf("existing hold %s expiry: %w", id, err)
	}
	return newHold(id, flashSaleID, shopperID, HoldState(fmt.Sprint(r[2])), expiresAt), nil
}

// newHold builds a Hold from what Redis stores, its expiry in Unix milliseconds.
func newHold(id, flashSaleID, shopperID uuid.UUID, state HoldState, expiresAtMs int64) Hold {
	return Hold{
		ID:          id,
		FlashSaleID: flashSaleID,
		ShopperID:   shopperID,
		State:       state,
		ExpiresAt:   time.UnixMilli(expiresAtMs).UTC(),
	}
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

// shoppersKey is the hash of each Shopper's latest Hold on a Flash Sale.
func shoppersKey(flashSaleID uuid.UUID) string {
	return "inventory:{" + flashSaleID.String() + "}:shoppers"
}

// holdsKey is the hash of every Hold of a Flash Sale, as '<Hold ID>:<field>' fields.
func holdsKey(flashSaleID uuid.UUID) string {
	return "inventory:{" + flashSaleID.String() + "}:holds"
}

// expiriesKey is the sorted set of a Flash Sale's Hold IDs, scored by expiry (ADR-0002).
func expiriesKey(flashSaleID uuid.UUID) string {
	return "inventory:{" + flashSaleID.String() + "}:expiries"
}
