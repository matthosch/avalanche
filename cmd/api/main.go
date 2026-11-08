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

	"github.com/matthosch/avalanche/internal/api"
	"github.com/matthosch/avalanche/internal/commerce"
	"github.com/matthosch/avalanche/internal/config"
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

	pool, err := pgxpool.New(ctx, config.CommerceDatabaseURL())
	if err != nil {
		return err
	}
	defer pool.Close()
	if err := commerce.Migrate(ctx, pool); err != nil {
		return err
	}

	addr := config.Env("API_ADDR", ":8080")
	srv := &http.Server{Addr: addr, Handler: api.NewHandler(pool), ReadHeaderTimeout: 5 * time.Second}
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
