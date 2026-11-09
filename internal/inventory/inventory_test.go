package inventory_test

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/redis/go-redis/v9"
	tcredis "github.com/testcontainers/testcontainers-go/modules/redis"

	"github.com/matthosch/avalanche/internal/inventory"
)

// redisClient is one throwaway Redis shared by every test in the package (seam 2 in spec #1:
// the inventory module against real Redis). Tests stay independent because each uses fresh
// Flash Sale IDs.
var redisClient *redis.Client

func TestMain(m *testing.M) {
	os.Exit(runWithRedis(m))
}

func runWithRedis(m *testing.M) int {
	ctx := context.Background()
	container, err := tcredis.Run(ctx, "redis:8.2-alpine")
	if err != nil {
		fmt.Fprintln(os.Stderr, "start redis:", err)
		return 1
	}
	defer func() { _ = container.Terminate(context.Background()) }()
	url, err := container.ConnectionString(ctx)
	if err != nil {
		fmt.Fprintln(os.Stderr, "redis url:", err)
		return 1
	}
	opts, err := redis.ParseURL(url)
	if err != nil {
		fmt.Fprintln(os.Stderr, "parse redis url:", err)
		return 1
	}
	redisClient = redis.NewClient(opts)
	defer redisClient.Close()
	return m.Run()
}

// inAnHour is an end time comfortably after Redis server time while any test runs, for
// Flash Sales that should stay LIVE.
var inAnHour = time.Now().Add(time.Hour)

func newInventory(t *testing.T) *inventory.Inventory {
	t.Helper()
	return inventory.New(redisClient)
}

// loadLive loads a LIVE Flash Sale of units and returns its ID.
func loadLive(t *testing.T, inv *inventory.Inventory, units int) uuid.UUID {
	t.Helper()
	flashSale := uuid.Must(uuid.NewV7())
	if err := inv.Load(context.Background(), flashSale, units, inAnHour); err != nil {
		t.Fatalf("load: %v", err)
	}
	return flashSale
}

func TestLoadMakesEveryUnitAvailable(t *testing.T) {
	ctx := context.Background()
	inv := newInventory(t)
	flashSale := uuid.Must(uuid.NewV7())

	if err := inv.Load(ctx, flashSale, 500, inAnHour); err != nil {
		t.Fatalf("load: %v", err)
	}

	counts, err := inv.Counts(ctx, flashSale)
	if err != nil {
		t.Fatalf("counts: %v", err)
	}
	want := inventory.Counts{Available: 500}
	if counts != want {
		t.Errorf("counts = %+v, want %+v", counts, want)
	}
}

func TestRepeatedLoadKeepsTheFirstStock(t *testing.T) {
	ctx := context.Background()
	inv := newInventory(t)
	flashSale := uuid.Must(uuid.NewV7())

	if err := inv.Load(ctx, flashSale, 500, inAnHour); err != nil {
		t.Fatalf("first load: %v", err)
	}
	// A transition that runs twice must not reload stock: with a different unit count the
	// second Load would be visible in Counts if it applied.
	if err := inv.Load(ctx, flashSale, 999, inAnHour); err != nil {
		t.Fatalf("second load: %v", err)
	}

	counts, err := inv.Counts(ctx, flashSale)
	if err != nil {
		t.Fatalf("counts: %v", err)
	}
	if want := (inventory.Counts{Available: 500}); counts != want {
		t.Errorf("counts = %+v, want %+v", counts, want)
	}
}

func TestCountsOfAFlashSaleNeverLoaded(t *testing.T) {
	inv := newInventory(t)

	_, err := inv.Counts(context.Background(), uuid.Must(uuid.NewV7()))
	if !errors.Is(err, inventory.ErrNotLoaded) {
		t.Errorf("err = %v, want ErrNotLoaded", err)
	}
}

func TestFlashSalesHaveSeparateStock(t *testing.T) {
	ctx := context.Background()
	inv := newInventory(t)
	first, second := uuid.Must(uuid.NewV7()), uuid.Must(uuid.NewV7())

	if err := inv.Load(ctx, first, 500, inAnHour); err != nil {
		t.Fatal(err)
	}
	if err := inv.Load(ctx, second, 150, inAnHour); err != nil {
		t.Fatal(err)
	}

	for flashSale, units := range map[uuid.UUID]int{first: 500, second: 150} {
		counts, err := inv.Counts(ctx, flashSale)
		if err != nil {
			t.Fatal(err)
		}
		if want := (inventory.Counts{Available: units}); counts != want {
			t.Errorf("counts(%s) = %+v, want %+v", flashSale, counts, want)
		}
	}
}

func TestReserveGivesTheShopperAHeldHold(t *testing.T) {
	ctx := context.Background()
	inv := newInventory(t)
	flashSale := loadLive(t, inv, 10)
	shopper := uuid.Must(uuid.NewV7())

	before, err := inv.Now(ctx)
	if err != nil {
		t.Fatal(err)
	}
	hold, err := inv.Reserve(ctx, flashSale, shopper)
	if err != nil {
		t.Fatalf("reserve: %v", err)
	}
	after, err := inv.Now(ctx)
	if err != nil {
		t.Fatal(err)
	}

	if hold.ID.Version() != 7 {
		t.Errorf("hold ID %s is UUID version %d, want 7", hold.ID, hold.ID.Version())
	}
	if hold.FlashSaleID != flashSale || hold.ShopperID != shopper {
		t.Errorf("hold is for flash sale %s, shopper %s; want %s, %s", hold.FlashSaleID, hold.ShopperID, flashSale, shopper)
	}
	if hold.State != inventory.Held {
		t.Errorf("state = %s, want HELD", hold.State)
	}
	// The Hold window is 2 minutes from the moment of reserving, on Redis server time.
	earliest, latest := before.Add(2*time.Minute).Truncate(time.Millisecond), after.Add(2*time.Minute)
	if hold.ExpiresAt.Before(earliest) || hold.ExpiresAt.After(latest) {
		t.Errorf("expires at %s, want between %s and %s", hold.ExpiresAt, earliest, latest)
	}

	counts, err := inv.Counts(ctx, flashSale)
	if err != nil {
		t.Fatal(err)
	}
	if want := (inventory.Counts{Available: 9, Held: 1}); counts != want {
		t.Errorf("counts = %+v, want %+v", counts, want)
	}
}

func TestReserveOnAFlashSaleNeverLoadedIsNotLive(t *testing.T) {
	inv := newInventory(t)

	_, err := inv.Reserve(context.Background(), uuid.Must(uuid.NewV7()), uuid.Must(uuid.NewV7()))
	if !errors.Is(err, inventory.ErrNotLive) {
		t.Errorf("err = %v, want ErrNotLive", err)
	}
}

func TestReserveAfterTheEndTimeIsNotLive(t *testing.T) {
	ctx := context.Background()
	inv := newInventory(t)
	now, err := inv.Now(ctx)
	if err != nil {
		t.Fatal(err)
	}
	flashSale := uuid.Must(uuid.NewV7())
	if err := inv.Load(ctx, flashSale, 10, now.Add(-time.Second)); err != nil {
		t.Fatal(err)
	}

	_, err = inv.Reserve(ctx, flashSale, uuid.Must(uuid.NewV7()))
	if !errors.Is(err, inventory.ErrNotLive) {
		t.Errorf("err = %v, want ErrNotLive", err)
	}
	counts, err := inv.Counts(ctx, flashSale)
	if err != nil {
		t.Fatal(err)
	}
	if want := (inventory.Counts{Available: 10}); counts != want {
		t.Errorf("counts = %+v, want %+v (an ended Flash Sale keeps its stock)", counts, want)
	}
}

func TestReserveWithNoUnitsAvailableIsSoldOut(t *testing.T) {
	ctx := context.Background()
	inv := newInventory(t)
	flashSale := loadLive(t, inv, 2)
	for range 2 {
		if _, err := inv.Reserve(ctx, flashSale, uuid.Must(uuid.NewV7())); err != nil {
			t.Fatalf("reserve: %v", err)
		}
	}

	_, err := inv.Reserve(ctx, flashSale, uuid.Must(uuid.NewV7()))
	if !errors.Is(err, inventory.ErrSoldOut) {
		t.Errorf("err = %v, want ErrSoldOut", err)
	}
	counts, err := inv.Counts(ctx, flashSale)
	if err != nil {
		t.Fatal(err)
	}
	if want := (inventory.Counts{Held: 2}); counts != want {
		t.Errorf("counts = %+v, want %+v", counts, want)
	}
}

func TestShopperWithAHoldIsAlreadyHolding(t *testing.T) {
	ctx := context.Background()
	inv := newInventory(t)
	flashSale, other := loadLive(t, inv, 10), loadLive(t, inv, 10)
	shopper := uuid.Must(uuid.NewV7())
	first, err := inv.Reserve(ctx, flashSale, shopper)
	if err != nil {
		t.Fatalf("first reserve: %v", err)
	}

	_, err = inv.Reserve(ctx, flashSale, shopper)
	if !errors.Is(err, inventory.ErrAlreadyHolding) {
		t.Errorf("second reserve: err = %v, want ErrAlreadyHolding", err)
	}
	// The refusal hands back the Hold the Shopper already has, so a Shopper who lost the first
	// reply (or reloaded the page) can still find and pay for it.
	var holding *inventory.AlreadyHoldingError
	if !errors.As(err, &holding) {
		t.Fatalf("second reserve: err = %v, want an *AlreadyHoldingError", err)
	}
	if holding.Hold != first {
		t.Errorf("already holding %+v, want the first Hold %+v", holding.Hold, first)
	}
	counts, err := inv.Counts(ctx, flashSale)
	if err != nil {
		t.Fatal(err)
	}
	if want := (inventory.Counts{Available: 9, Held: 1}); counts != want {
		t.Errorf("counts = %+v, want %+v", counts, want)
	}

	// The limit is per Flash Sale: the same Shopper can still hold a unit of another one.
	if _, err := inv.Reserve(ctx, other, shopper); err != nil {
		t.Errorf("reserve another flash sale: %v", err)
	}
}

// race has attempts goroutines each call reserve once, all released at the same moment, and
// tallies the outcomes by refusal (nil for a Hold).
func race(t *testing.T, attempts int, reserve func(i int) error) map[error]int {
	t.Helper()
	var (
		start = make(chan struct{})
		mu    sync.Mutex
		wg    sync.WaitGroup
		tally = map[error]int{}
	)
	for i := range attempts {
		wg.Go(func() {
			<-start
			outcome := refusal(reserve(i))
			mu.Lock()
			defer mu.Unlock()
			tally[outcome]++
		})
	}
	close(start)
	wg.Wait()
	return tally
}

// refusal is which of the inventory's refusals err is, or err itself if none.
func refusal(err error) error {
	for _, r := range []error{inventory.ErrSoldOut, inventory.ErrAlreadyHolding, inventory.ErrNotLive} {
		if errors.Is(err, r) {
			return r
		}
	}
	return err
}

func TestRacingShoppersGetExactlyAsManyHoldsAsUnits(t *testing.T) {
	ctx := context.Background()
	inv := newInventory(t)
	const units, shoppers = 100, 1000
	flashSale := loadLive(t, inv, units)

	tally := race(t, shoppers, func(int) error {
		_, err := inv.Reserve(ctx, flashSale, uuid.Must(uuid.NewV7()))
		return err
	})

	want := map[error]int{nil: units, inventory.ErrSoldOut: shoppers - units}
	if !maps.Equal(tally, want) {
		t.Errorf("outcomes = %v, want %v", tally, want)
	}
	counts, err := inv.Counts(ctx, flashSale)
	if err != nil {
		t.Fatal(err)
	}
	if want := (inventory.Counts{Held: units}); counts != want {
		t.Errorf("counts = %+v, want %+v", counts, want)
	}
}

func TestShopperRacingThemselvesGetsOneHold(t *testing.T) {
	ctx := context.Background()
	inv := newInventory(t)
	const attempts = 200
	flashSale := loadLive(t, inv, 10)
	shopper := uuid.Must(uuid.NewV7())

	var (
		mu   sync.Mutex
		seen = map[inventory.Hold]int{} // Hold -> attempts that got it, as a Hold or as already holding
	)
	tally := race(t, attempts, func(int) error {
		hold, err := inv.Reserve(ctx, flashSale, shopper)
		var holding *inventory.AlreadyHoldingError
		if errors.As(err, &holding) {
			hold = holding.Hold
		} else if err != nil {
			return err
		}
		mu.Lock()
		defer mu.Unlock()
		seen[hold]++
		return err
	})

	want := map[error]int{nil: 1, inventory.ErrAlreadyHolding: attempts - 1}
	if !maps.Equal(tally, want) {
		t.Errorf("outcomes = %v, want %v", tally, want)
	}
	if len(seen) != 1 {
		t.Errorf("attempts saw %d different Holds, want all to see one Hold: %v", len(seen), seen)
	}
	counts, err := inv.Counts(ctx, flashSale)
	if err != nil {
		t.Fatal(err)
	}
	if want := (inventory.Counts{Available: 9, Held: 1}); counts != want {
		t.Errorf("counts = %+v, want %+v", counts, want)
	}
}

func TestFlashSalesRacingAtOnceKeepSeparateCounts(t *testing.T) {
	ctx := context.Background()
	inv := newInventory(t)
	const shoppers = 400
	first, second := loadLive(t, inv, 50), loadLive(t, inv, 120)

	// Even attempts go to the first Flash Sale, odd ones to the second, interleaved.
	tally := race(t, 2*shoppers, func(i int) error {
		flashSale := first
		if i%2 == 1 {
			flashSale = second
		}
		_, err := inv.Reserve(ctx, flashSale, uuid.Must(uuid.NewV7()))
		return err
	})

	want := map[error]int{nil: 50 + 120, inventory.ErrSoldOut: 2*shoppers - 50 - 120}
	if !maps.Equal(tally, want) {
		t.Errorf("outcomes = %v, want %v", tally, want)
	}
	for flashSale, units := range map[uuid.UUID]int{first: 50, second: 120} {
		counts, err := inv.Counts(ctx, flashSale)
		if err != nil {
			t.Fatal(err)
		}
		if want := (inventory.Counts{Held: units}); counts != want {
			t.Errorf("counts(%s) = %+v, want %+v", flashSale, counts, want)
		}
	}
}

func TestShopperGetsTheirHoldBackAfterTheFlashSaleEnds(t *testing.T) {
	ctx := context.Background()
	inv := newInventory(t)
	now, err := inv.Now(ctx)
	if err != nil {
		t.Fatal(err)
	}
	flashSale := uuid.Must(uuid.NewV7())
	endsAt := now.Add(300 * time.Millisecond)
	if err := inv.Load(ctx, flashSale, 10, endsAt); err != nil {
		t.Fatal(err)
	}
	shopper := uuid.Must(uuid.NewV7())
	first, err := inv.Reserve(ctx, flashSale, shopper)
	if err != nil {
		t.Fatalf("reserve while live: %v", err)
	}
	for {
		if now, err = inv.Now(ctx); err != nil {
			t.Fatal(err)
		}
		if !now.Before(endsAt) {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}

	// A Hold made before the end can still be paid until it expires, so a Shopper who reloads
	// or lost the first reply after the end still gets it back.
	_, err = inv.Reserve(ctx, flashSale, shopper)
	var holding *inventory.AlreadyHoldingError
	if !errors.As(err, &holding) || holding.Hold != first {
		t.Errorf("reserve after the end: err = %v, want already holding %+v", err, first)
	}
	// Anyone without a Hold is still refused.
	if _, err := inv.Reserve(ctx, flashSale, uuid.Must(uuid.NewV7())); !errors.Is(err, inventory.ErrNotLive) {
		t.Errorf("new Shopper after the end: err = %v, want ErrNotLive", err)
	}
}

// reserveOne reserves a unit of flashSale for a new Shopper and returns the Hold.
func reserveOne(t *testing.T, inv *inventory.Inventory, flashSale uuid.UUID) inventory.Hold {
	t.Helper()
	hold, err := inv.Reserve(context.Background(), flashSale, uuid.Must(uuid.NewV7()))
	if err != nil {
		t.Fatalf("reserve: %v", err)
	}
	return hold
}

// wantCounts fails the test unless flashSale's counts are want.
func wantCounts(t *testing.T, inv *inventory.Inventory, flashSale uuid.UUID, want inventory.Counts) {
	t.Helper()
	counts, err := inv.Counts(context.Background(), flashSale)
	if err != nil {
		t.Fatal(err)
	}
	if counts != want {
		t.Errorf("counts = %+v, want %+v", counts, want)
	}
}

// wantWrongState fails the test unless err refuses a Hold that is in state got where the
// operation needed want.
func wantWrongState(t *testing.T, what string, err error, want, got inventory.HoldState) {
	t.Helper()
	var wrong *inventory.WrongStateError
	if !errors.As(err, &wrong) || !errors.Is(err, inventory.ErrWrongState) {
		t.Errorf("%s: err = %v, want a *WrongStateError", what, err)
		return
	}
	if wrong.Want != want || wrong.Got != got {
		t.Errorf("%s: wrong state want %s, got %s; expected want %s, got %s", what, wrong.Want, wrong.Got, want, got)
	}
}

func TestBeginPaymentMovesTheHoldToPaying(t *testing.T) {
	ctx := context.Background()
	inv := newInventory(t)
	flashSale := loadLive(t, inv, 10)
	hold := reserveOne(t, inv, flashSale)

	paying, err := inv.BeginPayment(ctx, flashSale, hold.ID, hold.ShopperID)
	if err != nil {
		t.Fatalf("begin payment: %v", err)
	}

	want := hold
	want.State = inventory.Paying
	if paying != want {
		t.Errorf("hold = %+v, want %+v", paying, want)
	}
	wantCounts(t, inv, flashSale, inventory.Counts{Available: 9, Paying: 1})
}

func TestOnlyTheHoldsShopperCanBeginPayment(t *testing.T) {
	ctx := context.Background()
	inv := newInventory(t)
	flashSale, other := loadLive(t, inv, 10), loadLive(t, inv, 10)
	hold := reserveOne(t, inv, flashSale)

	for _, attempt := range []struct {
		name                     string
		flashSale, hold, shopper uuid.UUID
	}{
		{"another Shopper", flashSale, hold.ID, uuid.Must(uuid.NewV7())},
		{"unknown Hold", flashSale, uuid.Must(uuid.NewV7()), hold.ShopperID},
		{"another Flash Sale", other, hold.ID, hold.ShopperID},
	} {
		if _, err := inv.BeginPayment(ctx, attempt.flashSale, attempt.hold, attempt.shopper); !errors.Is(err, inventory.ErrHoldNotFound) {
			t.Errorf("%s: err = %v, want ErrHoldNotFound", attempt.name, err)
		}
	}
	wantCounts(t, inv, flashSale, inventory.Counts{Available: 9, Held: 1})
	wantCounts(t, inv, other, inventory.Counts{Available: 10})
}

func TestBeginPaymentOnlyMovesAHeldHold(t *testing.T) {
	ctx := context.Background()
	inv := newInventory(t)
	flashSale := loadLive(t, inv, 10)
	hold := reserveOne(t, inv, flashSale)
	if _, err := inv.BeginPayment(ctx, flashSale, hold.ID, hold.ShopperID); err != nil {
		t.Fatalf("begin payment: %v", err)
	}

	_, err := inv.BeginPayment(ctx, flashSale, hold.ID, hold.ShopperID)
	wantWrongState(t, "begin payment again", err, inventory.Held, inventory.Paying)
	wantCounts(t, inv, flashSale, inventory.Counts{Available: 9, Paying: 1})
}

func TestPayingHoldKeepsItsPaymentIntent(t *testing.T) {
	ctx := context.Background()
	inv := newInventory(t)
	flashSale := loadLive(t, inv, 10)
	hold := reserveOne(t, inv, flashSale)
	paying, err := inv.BeginPayment(ctx, flashSale, hold.ID, hold.ShopperID)
	if err != nil {
		t.Fatalf("begin payment: %v", err)
	}

	if err := inv.RecordPaymentIntent(ctx, flashSale, hold.ID, "pi_3Q1example"); err != nil {
		t.Fatalf("record payment intent: %v", err)
	}

	got, err := inv.Hold(ctx, flashSale, hold.ID)
	if err != nil {
		t.Fatalf("hold: %v", err)
	}
	want := paying
	want.PaymentIntentID = "pi_3Q1example"
	if got != want {
		t.Errorf("hold = %+v, want %+v", got, want)
	}
}

func TestOnlyAPayingHoldTakesAPaymentIntent(t *testing.T) {
	ctx := context.Background()
	inv := newInventory(t)
	flashSale := loadLive(t, inv, 10)
	hold := reserveOne(t, inv, flashSale)

	err := inv.RecordPaymentIntent(ctx, flashSale, hold.ID, "pi_3Q1example")
	wantWrongState(t, "record on a HELD Hold", err, inventory.Paying, inventory.Held)
	if err := inv.RecordPaymentIntent(ctx, flashSale, uuid.Must(uuid.NewV7()), "pi_3Q1example"); !errors.Is(err, inventory.ErrHoldNotFound) {
		t.Errorf("record on an unknown Hold: err = %v, want ErrHoldNotFound", err)
	}
	if got, err := inv.Hold(ctx, flashSale, hold.ID); err != nil || got != hold {
		t.Errorf("hold = %+v, %v; want it unchanged: %+v", got, err, hold)
	}
}

func TestLookingUpAnUnknownHold(t *testing.T) {
	inv := newInventory(t)
	flashSale := loadLive(t, inv, 10)

	if _, err := inv.Hold(context.Background(), flashSale, uuid.Must(uuid.NewV7())); !errors.Is(err, inventory.ErrHoldNotFound) {
		t.Errorf("err = %v, want ErrHoldNotFound", err)
	}
}

func TestAPaidUnitMovesFromHeldThroughPayingToSold(t *testing.T) {
	ctx := context.Background()
	inv := newInventory(t)
	flashSale := loadLive(t, inv, 10)
	hold := reserveOne(t, inv, flashSale)
	wantCounts(t, inv, flashSale, inventory.Counts{Available: 9, Held: 1})
	if _, err := inv.BeginPayment(ctx, flashSale, hold.ID, hold.ShopperID); err != nil {
		t.Fatalf("begin payment: %v", err)
	}
	wantCounts(t, inv, flashSale, inventory.Counts{Available: 9, Paying: 1})

	if err := inv.Confirm(ctx, flashSale, hold.ID); err != nil {
		t.Fatalf("confirm: %v", err)
	}

	wantCounts(t, inv, flashSale, inventory.Counts{Available: 9, Sold: 1})
	if got, err := inv.Hold(ctx, flashSale, hold.ID); err != nil || got.State != inventory.Sold {
		t.Errorf("hold = %+v, %v; want SOLD", got, err)
	}
}

func TestConfirmingASoldHoldAgainChangesNothing(t *testing.T) {
	ctx := context.Background()
	inv := newInventory(t)
	flashSale := loadLive(t, inv, 10)
	hold := reserveOne(t, inv, flashSale)
	if _, err := inv.BeginPayment(ctx, flashSale, hold.ID, hold.ShopperID); err != nil {
		t.Fatalf("begin payment: %v", err)
	}
	if err := inv.Confirm(ctx, flashSale, hold.ID); err != nil {
		t.Fatalf("confirm: %v", err)
	}

	// The confirm response and the payment webhook can both confirm; the second is a no-op.
	for range 3 {
		if err := inv.Confirm(ctx, flashSale, hold.ID); err != nil {
			t.Errorf("confirm again: %v", err)
		}
	}

	wantCounts(t, inv, flashSale, inventory.Counts{Available: 9, Sold: 1})
}

func TestConfirmOnlySellsAPayingHold(t *testing.T) {
	ctx := context.Background()
	inv := newInventory(t)
	flashSale := loadLive(t, inv, 10)
	hold := reserveOne(t, inv, flashSale)

	err := inv.Confirm(ctx, flashSale, hold.ID)
	wantWrongState(t, "confirm a HELD Hold", err, inventory.Paying, inventory.Held)
	if err := inv.Confirm(ctx, flashSale, uuid.Must(uuid.NewV7())); !errors.Is(err, inventory.ErrHoldNotFound) {
		t.Errorf("confirm an unknown Hold: err = %v, want ErrHoldNotFound", err)
	}
	wantCounts(t, inv, flashSale, inventory.Counts{Available: 9, Held: 1})
}
