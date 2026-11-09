// Package seed resets commerce to a known demo state: a few Stores, each running Flash Sales
// scheduled relative to a given time.
package seed

import (
	"context"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/matthosch/avalanche/internal/commerce"
)

type flashSale struct {
	product    string
	priceCents int64
	units      int32
	startsIn   time.Duration
	lasts      time.Duration
}

var stores = []struct {
	name       string
	flashSales []flashSale
}{
	{"Summit Supply Co.", []flashSale{
		{"Aurora Trail Runner", 12900, 500, 2 * time.Minute, 30 * time.Minute},
		{"Ridgeline Daypack", 8900, 200, 45 * time.Minute, 30 * time.Minute},
	}},
	{"Lumen Audio", []flashSale{
		{"Halo Wireless Headphones", 19900, 300, 5 * time.Minute, 20 * time.Minute},
	}},
	{"Fern & Fable Books", []flashSale{
		{"Signed First Edition Box Set", 4500, 150, 10 * time.Minute, 15 * time.Minute},
	}},
}

// Run migrates commerce, deletes every Store, Flash Sale, Order and outbox row, and inserts
// the demo set with schedules relative to now.
func Run(ctx context.Context, pool *pgxpool.Pool, now time.Time) error {
	if err := commerce.Migrate(ctx, pool); err != nil {
		return err
	}
	return pgx.BeginFunc(ctx, pool, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `TRUNCATE outbox, orders, flash_sales, stores`); err != nil {
			return fmt.Errorf("reset: %w", err)
		}
		for _, s := range stores {
			var storeID string
			if err := tx.QueryRow(ctx, `INSERT INTO stores (name) VALUES ($1) RETURNING id`, s.name).Scan(&storeID); err != nil {
				return fmt.Errorf("insert store %q: %w", s.name, err)
			}
			for _, flashSale := range s.flashSales {
				start := now.Add(flashSale.startsIn)
				if _, err := tx.Exec(ctx, `
					INSERT INTO flash_sales (store_id, product_name, price_cents, units, starts_at, ends_at)
					VALUES ($1, $2, $3, $4, $5, $6)`,
					storeID, flashSale.product, flashSale.priceCents, flashSale.units, start, start.Add(flashSale.lasts)); err != nil {
					return fmt.Errorf("insert flash sale %q: %w", flashSale.product, err)
				}
			}
		}
		return nil
	})
}
