package inventory_test

import (
	"context"
	"errors"
	"testing"

	"github.com/google/uuid"
	"github.com/redis/go-redis/v9"
	tcredis "github.com/testcontainers/testcontainers-go/modules/redis"

	"github.com/matthosch/avalanche/internal/inventory"
)

// newInventory starts a throwaway Redis (seam 2 in spec #1: the inventory module against real
// Redis) and returns an Inventory backed by it.
func newInventory(t *testing.T) *inventory.Inventory {
	t.Helper()
	ctx := context.Background()
	container, err := tcredis.Run(ctx, "redis:8.2-alpine")
	if err != nil {
		t.Fatalf("start redis: %v", err)
	}
	t.Cleanup(func() { _ = container.Terminate(context.Background()) })
	url, err := container.ConnectionString(ctx)
	if err != nil {
		t.Fatal(err)
	}
	opts, err := redis.ParseURL(url)
	if err != nil {
		t.Fatal(err)
	}
	client := redis.NewClient(opts)
	t.Cleanup(func() { _ = client.Close() })
	return inventory.New(client)
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
