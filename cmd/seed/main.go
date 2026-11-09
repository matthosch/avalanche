// Command seed resets commerce to the demo set of Stores and Flash Sales.
package main

import (
	"context"
	"flag"
	"log/slog"
	"os"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/matthosch/avalanche/internal/config"
	"github.com/matthosch/avalanche/internal/seed"
)

func main() {
	ago := flag.Duration("ago", 0, "schedule the demo set as if seeded this long ago (e.g. 26m puts Flash Sales in every state)")
	flag.Parse()

	ctx := context.Background()
	pool, err := pgxpool.New(ctx, config.CommerceDatabaseURL())
	if err != nil {
		slog.Error("connect", "err", err)
		os.Exit(1)
	}
	defer pool.Close()
	if err := seed.Run(ctx, pool, time.Now().UTC().Truncate(time.Second).Add(-*ago)); err != nil {
		slog.Error("seed", "err", err)
		os.Exit(1)
	}
	slog.Info("seeded commerce", "ago", *ago)
}
