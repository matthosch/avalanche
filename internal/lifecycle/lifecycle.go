// Package lifecycle moves Flash Sales through SCHEDULED → LIVE → ENDED on their own, by
// their start and end times measured on Redis server time.
package lifecycle

import (
	"context"
	"errors"
	"log/slog"
	"time"

	"github.com/matthosch/avalanche/internal/commerce"
	"github.com/matthosch/avalanche/internal/inventory"
)

// Advance makes one pass: every SCHEDULED Flash Sale past its start time has its stock
// loaded and goes LIVE, then every LIVE Flash Sale past its end time goes ENDED.
//
// It is safe to run repeatedly or concurrently. Stock is loaded before the status changes,
// so a LIVE Flash Sale always has inventory in Redis; if a pass dies in between, the next one
// repeats Load, which keeps the stock from the first. A Flash Sale whose whole window passed
// while no worker ran still goes through LIVE (with its stock loaded) on its way to ENDED, so
// every ended Flash Sale reaches Close-out the same way.
//
// A Flash Sale that fails to go LIVE is retried on the next pass; it doesn't stop the others
// from going LIVE, or LIVE ones from ending.
func Advance(ctx context.Context, db commerce.Querier, inv *inventory.Inventory) error {
	now, err := inv.Now(ctx)
	if err != nil {
		return err
	}
	due, err := commerce.ListDueToGoLive(ctx, db, now)
	if err != nil {
		return err
	}
	var errs []error
	for _, flashSale := range due {
		if err := goLive(ctx, db, inv, flashSale); err != nil {
			errs = append(errs, err)
		}
	}
	errs = append(errs, commerce.EndDue(ctx, db, now))
	return errors.Join(errs...)
}

func goLive(ctx context.Context, db commerce.Querier, inv *inventory.Inventory, flashSale commerce.DueToGoLive) error {
	if err := inv.Load(ctx, flashSale.ID, flashSale.Units); err != nil {
		return err
	}
	if err := commerce.MarkLive(ctx, db, flashSale.ID); err != nil {
		return err
	}
	slog.InfoContext(ctx, "flash sale live", "flash_sale_id", flashSale.ID, "units", flashSale.Units)
	return nil
}

// Run calls Advance every interval until ctx is done. A failed pass is logged and retried on
// the next tick.
func Run(ctx context.Context, db commerce.Querier, inv *inventory.Inventory, interval time.Duration) {
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		if err := Advance(ctx, db, inv); err != nil && ctx.Err() == nil {
			slog.ErrorContext(ctx, "advance flash sale lifecycle", "err", err)
		}
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}
