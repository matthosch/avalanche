// Command ledger books each OrderCompleted from the orders.completed stream as a Journal Entry,
// and serves balances over internal REST to api.
package main

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/redis/go-redis/v9"

	"github.com/matthosch/avalanche/internal/config"
	"github.com/matthosch/avalanche/internal/ledger"
	"github.com/matthosch/avalanche/internal/stream"
)

// group is the ledger's consumer group on orders.completed.
const group = "ledger"

func main() {
	if err := run(); err != nil {
		slog.Error("ledger stopped", "err", err)
		os.Exit(1)
	}
}

func run() error {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	pool, err := pgxpool.New(ctx, config.LedgerDatabaseURL())
	if err != nil {
		return err
	}
	defer pool.Close()
	if err := ledger.Migrate(ctx, pool); err != nil {
		return err
	}

	opts, err := redis.ParseURL(config.RedisURL())
	if err != nil {
		return err
	}
	rdb := redis.NewClient(opts)
	defer rdb.Close()

	l := ledger.New(pool)
	// The consumer name stays the same across restarts, so a restarted ledger picks up the
	// messages it had read but not acknowledged.
	consumer, err := os.Hostname()
	if err != nil {
		return err
	}
	consumed := make(chan error, 1)
	go func() { consumed <- stream.Consume(ctx, rdb, group, consumer, l.Ingest) }()

	addr := config.Env("LEDGER_ADDR", ":8081")
	srv := &http.Server{Addr: addr, Handler: ledger.NewHandler(l), ReadHeaderTimeout: 5 * time.Second}
	served := make(chan error, 1)
	go func() { served <- srv.ListenAndServe() }()
	slog.Info("ledger listening", "addr", addr, "stream", stream.OrdersCompleted, "group", group, "consumer", consumer)

	select {
	case err = <-consumed:
	case err = <-served:
	case <-ctx.Done():
	}
	stop()
	shutdown, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_ = srv.Shutdown(shutdown)
	if errors.Is(err, http.ErrServerClosed) {
		return nil
	}
	return err
}
