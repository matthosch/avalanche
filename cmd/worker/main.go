// Command worker runs the background jobs: moving Flash Sales through their lifecycle at
// their start and end times, and relaying the commerce outbox to its stream.
package main

import (
	"context"
	"log/slog"
	"os"
	"os/signal"
	"sync"
	"syscall"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/redis/go-redis/v9"

	"github.com/matthosch/avalanche/internal/commerce"
	"github.com/matthosch/avalanche/internal/config"
	"github.com/matthosch/avalanche/internal/inventory"
	"github.com/matthosch/avalanche/internal/lifecycle"
	"github.com/matthosch/avalanche/internal/relay"
)

// lifecycleTick is how often Flash Sales are checked against their start and end times, so
// a Flash Sale goes LIVE or ENDED at most this long after its time.
const lifecycleTick = time.Second

// relayTick is how long the relay waits once the outbox is empty, so an Order reaches the
// stream within about this long of being written.
const relayTick = 200 * time.Millisecond

func main() {
	if err := run(); err != nil {
		slog.Error("worker stopped", "err", err)
		os.Exit(1)
	}
}

func run() error {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	pool, err := pgxpool.New(ctx, config.CommerceDatabaseURL())
	if err != nil {
		return err
	}
	defer pool.Close()
	if err := commerce.Migrate(ctx, pool); err != nil {
		return err
	}

	opts, err := redis.ParseURL(config.RedisURL())
	if err != nil {
		return err
	}
	rdb := redis.NewClient(opts)
	defer rdb.Close()

	slog.Info("worker running", "lifecycle_tick", lifecycleTick, "relay_tick", relayTick)
	var wg sync.WaitGroup
	wg.Go(func() { lifecycle.Run(ctx, pool, inventory.New(rdb), lifecycleTick) })
	wg.Go(func() { relay.Run(ctx, pool, rdb, relayTick) })
	wg.Wait()
	return nil
}
