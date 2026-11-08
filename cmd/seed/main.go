// Command seed resets commerce to the demo set of Stores and Flash Sales.
package main

import (
	"context"
	"log/slog"
	"os"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/matthosch/avalanche/internal/config"
	"github.com/matthosch/avalanche/internal/seed"
)

func main() {
	ctx := context.Background()
	pool, err := pgxpool.New(ctx, config.CommerceDatabaseURL())
	if err != nil {
		slog.Error("connect", "err", err)
		os.Exit(1)
	}
	defer pool.Close()
	if err := seed.Run(ctx, pool, time.Now().UTC().Truncate(time.Second)); err != nil {
		slog.Error("seed", "err", err)
		os.Exit(1)
	}
	slog.Info("seeded commerce")
}
