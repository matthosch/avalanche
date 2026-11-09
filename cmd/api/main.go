// Command api serves the browser-facing API: the hot path and the backend for the web app.
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

	"github.com/matthosch/avalanche/internal/api"
	"github.com/matthosch/avalanche/internal/checkout"
	"github.com/matthosch/avalanche/internal/commerce"
	"github.com/matthosch/avalanche/internal/config"
	"github.com/matthosch/avalanche/internal/inventory"
	"github.com/matthosch/avalanche/internal/payments"
)

func main() {
	if err := run(); err != nil {
		slog.Error("api stopped", "err", err)
		os.Exit(1)
	}
}

func run() error {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	feeBPS, err := config.FeeBPS()
	if err != nil {
		return err
	}
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

	inv := inventory.New(rdb)
	checkouts := checkout.New(pool, inv, payments.NewStripeClient(config.PaymentsURL(), config.PaymentsSecretKey()), feeBPS)

	addr := config.Env("API_ADDR", ":8080")
	srv := &http.Server{Addr: addr, Handler: api.NewHandler(pool, inv, checkouts), ReadHeaderTimeout: 5 * time.Second}
	go func() {
		<-ctx.Done()
		shutdown, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = srv.Shutdown(shutdown)
	}()

	slog.Info("api listening", "addr", addr)
	if err := srv.ListenAndServe(); !errors.Is(err, http.ErrServerClosed) {
		return err
	}
	return nil
}
