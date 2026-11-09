//go:build e2e

// Package e2e drives the running stack from outside, through api's public HTTP endpoints
// (the system-boundary seam in spec #1). Run with `task test:e2e` while `task infra:up`,
// `task dev:api` and `task dev:worker` are running. Tests re-seed commerce, so they reset its demo data.
package e2e

import (
	"context"
	"encoding/json"
	"net/http"
	"strconv"
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

	flashSales := listFlashSales(t)

	stores := map[string]bool{}
	for _, flashSale := range flashSales {
		stores[flashSale.Store.Name] = true
	}
	if len(stores) < 3 {
		t.Errorf("got Flash Sales from %d Stores, want at least 3: %v", len(stores), stores)
	}

	// A known seeded Flash Sale, checked field by field.
	var found bool
	for _, flashSale := range flashSales {
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
		t.Fatalf("seeded Flash Sale %q not listed; got %d Flash Sales", "Aurora Trail Runner", len(flashSales))
	}

	for i := 1; i < len(flashSales); i++ {
		if flashSales[i].StartsAt.Before(flashSales[i-1].StartsAt) {
			t.Errorf("Flash Sales not ordered by start time at index %d", i)
		}
	}
}

func TestFlashSalesGoLiveAndEndOnSchedule(t *testing.T) {
	ctx := context.Background()
	pool, err := pgxpool.New(ctx, config.CommerceDatabaseURL())
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()

	// Seeding 26 minutes in the past puts the demo set in every state at once:
	// Aurora runs now-24m…now+6m, Ridgeline starts in 19m, Halo and the Box Set ended a minute ago.
	if err := seed.Run(ctx, pool, time.Now().UTC().Truncate(time.Second).Add(-26*time.Minute)); err != nil {
		t.Fatalf("seed: %v", err)
	}
	units := func(n int32) *int32 { return &n }
	wants := map[string]shown{
		"Aurora Trail Runner":          {gen.LIVE, units(500)},
		"Ridgeline Daypack":            {gen.SCHEDULED, nil},
		"Halo Wireless Headphones":     {gen.ENDED, nil},
		"Signed First Edition Box Set": {gen.ENDED, nil},
	}

	// The worker moves Flash Sales on its own tick, so poll until it has caught up.
	var got map[string]shown
	deadline := time.Now().Add(15 * time.Second)
	for {
		got = map[string]shown{}
		for _, flashSale := range listFlashSales(t) {
			got[flashSale.ProductName] = shown{flashSale.Status, flashSale.UnitsRemaining}
		}
		if matches(got, wants) || time.Now().After(deadline) {
			break
		}
		time.Sleep(250 * time.Millisecond)
	}
	for product, w := range wants {
		g, ok := got[product]
		if !ok {
			t.Errorf("%s: not listed", product)
			continue
		}
		if !g.equal(w) {
			t.Errorf("%s: status %s, units remaining %s; want %s, %s",
				product, g.status, showUnits(g.unitsRemaining), w.status, showUnits(w.unitsRemaining))
		}
	}
}

func listFlashSales(t *testing.T) []gen.FlashSale {
	t.Helper()
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
	return body.FlashSales
}

// shown is what the listing tells a Shopper about a Flash Sale's progress.
type shown struct {
	status         gen.FlashSaleStatus
	unitsRemaining *int32
}

func (s shown) equal(o shown) bool {
	a, b := s.unitsRemaining, o.unitsRemaining
	return s.status == o.status && ((a == nil && b == nil) || (a != nil && b != nil && *a == *b))
}

func matches(got, wants map[string]shown) bool {
	for product, w := range wants {
		if g, ok := got[product]; !ok || !g.equal(w) {
			return false
		}
	}
	return true
}

func showUnits(n *int32) string {
	if n == nil {
		return "absent"
	}
	return strconv.Itoa(int(*n))
}
