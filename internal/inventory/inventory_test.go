package inventory_test

import (
	"context"
	"errors"
	"fmt"
	"os"
	"testing"

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

func newInventory(t *testing.T) *inventory.Inventory {
	t.Helper()
	return inventory.New(redisClient)
}

func TestLoadMakesEveryUnitAvailable(t *testing.T) {
	ctx := context.Background()
	inv := newInventory(t)
	flashSale := uuid.Must(uuid.NewV7())

	if err := inv.Load(ctx, flashSale, 500); err != nil {
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

	if err := inv.Load(ctx, flashSale, 500); err != nil {
		t.Fatalf("first load: %v", err)
	}
	// A transition that runs twice must not reload stock: with a different unit count the
	// second Load would be visible in Counts if it applied.
	if err := inv.Load(ctx, flashSale, 999); err != nil {
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

	if err := inv.Load(ctx, first, 500); err != nil {
		t.Fatal(err)
	}
	if err := inv.Load(ctx, second, 150); err != nil {
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
