//go:build e2e

// Package e2e drives the running stack from outside, through api's public HTTP endpoints
// (the system-boundary seam in spec #1). Run with `task test:e2e` while `task infra:up` and
// `task dev:api` are running. Tests re-seed commerce, so they reset its demo data.
package e2e

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/matthosch/avalanche/internal/api/gen"
	"github.com/matthosch/avalanche/internal/config"
	"github.com/matthosch/avalanche/internal/seed"
)

func TestShopperSeesSeededFlashSales(t *testing.T) {
	ctx := context.Background()
	pool, err := pgxpool.New(ctx, config.CommerceDatabaseURL())
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()

	now := time.Now().UTC().Truncate(time.Second)
	if err := seed.Run(ctx, pool, now); err != nil {
		t.Fatalf("seed: %v", err)
	}

	res, err := http.Get(config.Env("API_URL", "http://localhost:8080") + "/api/flash-sales")
	if err != nil {
		t.Fatalf("list flash sales: %v", err)
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", res.StatusCode)
	}
	var body gen.ListFlashSales200JSONResponse
	if err := json.NewDecoder(res.Body).Decode(&body); err != nil {
		t.Fatal(err)
	}

	stores := map[string]bool{}
	for _, flashSale := range body.FlashSales {
		stores[flashSale.Store.Name] = true
	}
	if len(stores) < 3 {
		t.Errorf("got Flash Sales from %d Stores, want at least 3: %v", len(stores), stores)
	}

	// A known seeded Flash Sale, checked field by field.
	var found bool
	for _, flashSale := range body.FlashSales {
		if flashSale.ProductName != "Aurora Trail Runner" {
			continue
		}
		found = true
		if flashSale.Store.Name != "Summit Supply Co." {
			t.Errorf("store = %q, want Summit Supply Co.", flashSale.Store.Name)
		}
		if flashSale.PriceCents != 12900 || flashSale.Currency != "USD" || flashSale.Units != 500 {
			t.Errorf("price/currency/units = %d %s %d, want 12900 USD 500", flashSale.PriceCents, flashSale.Currency, flashSale.Units)
		}
		if !flashSale.StartsAt.Equal(now.Add(2*time.Minute)) || !flashSale.EndsAt.Equal(now.Add(32*time.Minute)) {
			t.Errorf("schedule = %s–%s, want now+2m–now+32m", flashSale.StartsAt, flashSale.EndsAt)
		}
	}
	if !found {
		t.Fatalf("seeded Flash Sale %q not listed; got %d Flash Sales", "Aurora Trail Runner", len(body.FlashSales))
	}

	for i := 1; i < len(body.FlashSales); i++ {
		if body.FlashSales[i].StartsAt.Before(body.FlashSales[i-1].StartsAt) {
			t.Errorf("Flash Sales not ordered by start time at index %d", i)
		}
	}
}
